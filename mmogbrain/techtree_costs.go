package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gorilla/mux"
)

// Tech tree costs per ship on the admin dashboard (issue #15, the editing
// side of #2). Every research XP cost and purchase price in the tree is a
// GUESS from code (techTreeXPCostByTier, techTreeModuleXPCost,
// gatewayMarketCreditPrice, heroPriceGP). This lets them be set one node at
// a time -- by item id, the id the player's ship page shows and the charge
// is keyed by -- with a source and a status next to each value, so we can
// see how close to the original the tree is.
//
// One price for display and charge:
//   - XP: applied at the end of techTreeBaseItemsWith, which both
//     YA_GetTechTree and the research charge (techTreeResearchCost) read.
//     Capped like every tree cost (capTechTreeCost: the client shows 5
//     digits), so the shown and the charged cost stay the same.
//   - price: techTreePriceOr, used by the catalog's hull, module and hero
//     entries and by purchasePriceForItemChecked.
// The tree is built per request and the catalog per fetch, so a change is in
// the next YA_GetTechTree / catalog a client asks for (at login). Already
// spent XP and credits are not touched.

type techTreeCost struct {
	amount *int32 // nil: keep the code's value, only annotate it
	status string // guess, sourced, accepted
	source string
}

var techTreeCostStatuses = map[string]string{
	"guess":    "GUESS",
	"sourced":  "Sourced",
	"accepted": "Accepted design choice",
}

const maxTechTreeCost = 10000000

var techTreeCosts struct {
	once sync.Once
	mu   sync.RWMutex
	m    map[[2]string]techTreeCost // {item id, "xp"|"price"}
}

func techTreeCostKey(id int32, field string) [2]string {
	return [2]string{strconv.Itoa(int(id)), field}
}

// loadTechTreeCosts reads the overrides at startup (main): the tree is built
// inside request handlers, and a query from there could land inside someone
// else's open rows loop (one connection). Lazy for tests.
func loadTechTreeCosts() {
	techTreeCosts.once.Do(func() {
		m := map[[2]string]techTreeCost{}
		if database := currentMmogPlayerStateDB(); database != nil {
			if rows, err := database.Query(`SELECT item_id, field, amount, status, source FROM techtree_cost_overrides`); err == nil {
				for rows.Next() {
					var id int32
					var field, status, source string
					var amount sql.NullInt64
					if rows.Scan(&id, &field, &amount, &status, &source) == nil {
						c := techTreeCost{status: status, source: source}
						if amount.Valid {
							a := int32(amount.Int64)
							c.amount = &a
						}
						m[techTreeCostKey(id, field)] = c
					}
				}
				_ = rows.Close()
			}
		}
		techTreeCosts.mu.Lock()
		techTreeCosts.m = m
		techTreeCosts.mu.Unlock()
	})
}

func techTreeCostFor(id int32, field string) (techTreeCost, bool) {
	loadTechTreeCosts()
	techTreeCosts.mu.RLock()
	defer techTreeCosts.mu.RUnlock()
	c, ok := techTreeCosts.m[techTreeCostKey(id, field)]
	return c, ok
}

// techTreePriceOr is an item's purchase price: the dashboard's, or def.
func techTreePriceOr(id int32, def int32) int32 {
	if c, ok := techTreeCostFor(id, "price"); ok && c.amount != nil {
		return *c.amount
	}
	return def
}

// applyTechTreeXPOverrides sets the dashboard's research costs on the tree,
// keeping the code's value in xpDefault for the editor.
func applyTechTreeXPOverrides(items []techTreeItem) {
	for i := range items {
		items[i].xpDefault = items[i].xpCost
		if c, ok := techTreeCostFor(items[i].id, "xp"); ok && c.amount != nil {
			items[i].xpCost = capTechTreeCost(*c.amount)
		}
	}
}

// --- the ship pages --------------------------------------------------------------

type techTreeShip struct {
	ID           int32
	Name, Line   string
	Tier         int32
	Hero         bool
	Manufacturer string
}

func costEditorShips() []techTreeShip {
	var out []techTreeShip
	for _, h := range baseShipLoadouts {
		out = append(out, techTreeShip{ID: h.loadoutID, Name: h.name, Line: h.hullLine, Tier: h.tier,
			Manufacturer: baseShipManufacturerByClassSize[h.hullLine]})
	}
	for _, h := range heroShipLoadouts {
		out = append(out, techTreeShip{ID: h.loadoutID, Name: h.name, Line: h.hullLine, Tier: h.tier, Hero: true, Manufacturer: h.manufacturer})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Tier != out[j].Tier {
			return out[i].Tier < out[j].Tier
		}
		return !out[i].Hero && out[j].Hero
	})
	return out
}

type techTreeValue struct {
	Default    int32  `json:"default"`
	Value      int32  `json:"value"`
	Currency   string `json:"currency"`
	Overridden bool   `json:"overridden"`
	Status     string `json:"status"`
	Source     string `json:"source"`
}

type techTreeNode struct {
	ItemID   int32          `json:"item_id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"` // hull, hero, module, briefing
	Tier     int32          `json:"tier"`
	Shared   bool           `json:"shared"` // the same id on every ship (briefings)
	XP       *techTreeValue `json:"xp,omitempty"`
	Price    *techTreeValue `json:"price,omitempty"`
	Warnings []string       `json:"warnings"`
}

func techTreeItemName(id int32) string {
	for _, h := range baseShipLoadouts {
		if h.loadoutID == id {
			return h.name
		}
	}
	if h, ok := heroByID(id); ok {
		return h.name
	}
	if row, ok := perShipResearchRow(id); ok && row.Name != "" {
		return row.Name
	}
	if meta, ok := extractedMarketItemMetadataForID(id); ok && meta.displayName != "" {
		return meta.displayName
	}
	return "item " + strconv.Itoa(int(id))
}

func techTreeValueFor(id int32, field string, def int32, currency string) *techTreeValue {
	v := &techTreeValue{Default: def, Value: def, Currency: currency, Status: "guess"}
	if c, ok := techTreeCostFor(id, field); ok {
		v.Status, v.Source = c.status, c.source
		if c.amount != nil {
			v.Value, v.Overridden = *c.amount, true
		}
	}
	return v
}

// techTreeDefaultPrice is an item's price without the dashboard's override.
func techTreeDefaultPrice(id int32) int32 {
	if h, ok := heroByID(id); ok {
		return heroPriceGP(h.tier)
	}
	p, _ := purchasePriceDerived(id)
	return p
}

// techTreePage is a ship's page as the editor shows it: the hull (or hero),
// then every node filed under it. items is techTreeBaseItemsWith(false).
func techTreePage(ship techTreeShip, items []techTreeItem) []techTreeNode {
	if ship.Hero {
		n := techTreeNode{ItemID: ship.ID, Name: ship.Name, Kind: "hero", Tier: ship.Tier, Warnings: []string{},
			Price: techTreeValueFor(ship.ID, "price", techTreeDefaultPrice(ship.ID), "GP")}
		return []techTreeNode{n}
	}
	xpOf := map[int32]int32{}
	for _, it := range items {
		xpOf[it.id] = it.xpCost
	}
	var nodes []techTreeNode
	seen := map[int32]bool{}
	add := func(it techTreeItem, kind string) {
		if seen[it.id] {
			return
		}
		seen[it.id] = true
		n := techTreeNode{ItemID: it.id, Name: techTreeItemName(it.id), Kind: kind, Tier: it.tier, Warnings: []string{},
			Shared: kind == "briefing",
			XP:     techTreeValueFor(it.id, "xp", it.xpDefault, "XP")}
		if kind != "briefing" {
			n.Price = techTreeValueFor(it.id, "price", techTreeDefaultPrice(it.id), "credits")
		}
		if n.XP.Value > techTreeMaxDisplayableCost {
			n.Warnings = append(n.Warnings, "research cost over 99999: the client shows and charges 99999")
		}
		if n.Price != nil && n.Price.Value > techTreeMaxDisplayableCost {
			n.Warnings = append(n.Warnings, "price has 6+ digits: the tech tree panel shows at most 99999 (the store shows it in full)")
		}
		if kind == "hull" && len(it.prereq) > 0 {
			parent := it.prereq[0]
			if px, ok := xpOf[parent]; ok && n.XP.Value < px {
				n.Warnings = append(n.Warnings, "researches for less XP than the ship before it ("+techTreeItemName(parent)+")")
			}
			if n.Price != nil && n.Price.Value < techTreePriceOr(parent, techTreeDefaultPrice(parent)) {
				n.Warnings = append(n.Warnings, "costs fewer credits than the ship before it ("+techTreeItemName(parent)+")")
			}
		}
		nodes = append(nodes, n)
	}
	for _, it := range items {
		if it.id == ship.ID && !it.module {
			add(it, "hull")
		}
	}
	for _, it := range items {
		if it.module && it.classID == ship.ID {
			kind := "module"
			if isOfficerBriefing(it.id) {
				kind = "briefing"
			}
			add(it, kind)
		}
	}
	return nodes
}

// techTreeProgress counts values with a source or an accepted status.
func techTreeProgress(nodes []techTreeNode) (done, total int) {
	for _, n := range nodes {
		for _, v := range []*techTreeValue{n.XP, n.Price} {
			if v == nil {
				continue
			}
			total++
			if v.Status == "sourced" || v.Status == "accepted" {
				done++
			}
		}
	}
	return done, total
}

// --- admin API ---------------------------------------------------------------------

func registerAdminTechTree(api *mux.Router) {
	api.HandleFunc("/techtree/ships", adminAPITechTreeShips).Methods(http.MethodGet)
	api.HandleFunc("/techtree/ships/{id}", adminAPITechTreeShip).Methods(http.MethodGet)
	api.HandleFunc("/techtree/values", adminAPITechTreeSetValues).Methods(http.MethodPost)
	api.HandleFunc("/techtree/export", adminAPITechTreeExport).Methods(http.MethodGet)
	api.HandleFunc("/techtree/import", adminAPITechTreeImport).Methods(http.MethodPost)
}

func adminAPITechTreeShips(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		ID           int32  `json:"id"`
		Name         string `json:"name"`
		Line         string `json:"line"`
		Tier         int32  `json:"tier"`
		Hero         bool   `json:"hero"`
		Manufacturer string `json:"manufacturer"`
		Nodes        int    `json:"nodes"`
		Done         int    `json:"done"`
		Total        int    `json:"total"`
		Warnings     int    `json:"warnings"`
	}
	out := []row{}
	allDone, allTotal := 0, 0
	items := techTreeBaseItemsWith(false)
	for _, s := range costEditorShips() {
		nodes := techTreePage(s, items)
		done, total := techTreeProgress(nodes)
		warn := 0
		for _, n := range nodes {
			warn += len(n.Warnings)
		}
		allDone += done
		allTotal += total
		out = append(out, row{s.ID, s.Name, s.Line, s.Tier, s.Hero, s.Manufacturer, len(nodes), done, total, warn})
	}
	writeAdminJSON(w, map[string]any{"ships": out, "done": allDone, "total": allTotal, "statuses": techTreeCostStatuses})
}

func adminAPITechTreeShip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	for _, s := range costEditorShips() {
		if err == nil && s.ID == int32(id) {
			nodes := techTreePage(s, techTreeBaseItemsWith(false))
			done, total := techTreeProgress(nodes)
			writeAdminJSON(w, map[string]any{"ship": map[string]any{"id": s.ID, "name": s.Name, "line": s.Line, "tier": s.Tier, "hero": s.Hero},
				"nodes": nodes, "done": done, "total": total, "statuses": techTreeCostStatuses})
			return
		}
	}
	writeAdminError(w, http.StatusNotFound, "no such ship")
}

type techTreeValueEdit struct {
	ItemID int32  `json:"item_id"`
	Field  string `json:"field"`
	Amount *int64 `json:"amount"` // null: the code's value
	Status string `json:"status"`
	Source string `json:"source"`
}

// techTreeNodeIDs is every id the editor can set, with the fields it has.
func techTreeNodeIDs() map[int32]map[string]bool {
	ids := map[int32]map[string]bool{}
	items := techTreeBaseItemsWith(false)
	for _, s := range costEditorShips() {
		for _, n := range techTreePage(s, items) {
			f := map[string]bool{}
			if n.XP != nil {
				f["xp"] = true
			}
			if n.Price != nil {
				f["price"] = true
			}
			ids[n.ItemID] = f
		}
	}
	return ids
}

// saveTechTreeValues validates and stores edits; it returns the audit
// details or an error message.
func saveTechTreeValues(database *sql.DB, edits []techTreeValueEdit) ([]map[string]any, string) {
	if len(edits) == 0 || len(edits) > 5000 {
		return nil, "send 1 to 5000 values"
	}
	known := techTreeNodeIDs()
	for i := range edits {
		e := &edits[i]
		e.Source = strings.TrimSpace(e.Source)
		if e.Status == "" {
			e.Status = "guess"
		}
		if !known[e.ItemID][e.Field] {
			return nil, "item " + strconv.Itoa(int(e.ItemID)) + " has no " + e.Field + " value in the tech tree"
		}
		if techTreeCostStatuses[e.Status] == "" {
			return nil, "status must be guess, sourced or accepted"
		}
		if e.Status == "sourced" && e.Source == "" {
			return nil, techTreeItemName(e.ItemID) + ": a sourced value needs its source"
		}
		if utf8.RuneCountInString(e.Source) > 500 {
			return nil, "a source is 500 characters at most"
		}
		if e.Amount != nil && (*e.Amount < 0 || *e.Amount > maxTechTreeCost) {
			return nil, techTreeItemName(e.ItemID) + ": a cost must be 0 to 10,000,000"
		}
	}
	tx, err := database.Begin()
	if err != nil {
		return nil, "database unavailable"
	}
	defer func() { _ = tx.Rollback() }()
	var audit []map[string]any
	for _, e := range edits {
		before, _ := techTreeCostFor(e.ItemID, e.Field)
		var amount any
		if e.Amount != nil {
			amount = *e.Amount
		}
		if e.Amount == nil && e.Status == "guess" && e.Source == "" {
			_, err = tx.Exec(`DELETE FROM techtree_cost_overrides WHERE item_id=? AND field=?`, e.ItemID, e.Field)
		} else {
			_, err = tx.Exec(`INSERT INTO techtree_cost_overrides(item_id, field, amount, status, source) VALUES(?,?,?,?,?)
				ON CONFLICT(item_id, field) DO UPDATE SET amount=excluded.amount, status=excluded.status, source=excluded.source, updated_at=datetime('now')`,
				e.ItemID, e.Field, amount, e.Status, e.Source)
		}
		if err != nil {
			return nil, "could not save " + techTreeItemName(e.ItemID)
		}
		var from any = "default"
		if before.amount != nil {
			from = *before.amount
		}
		to := amount
		if to == nil {
			to = "default"
		}
		audit = append(audit, map[string]any{"item": e.ItemID, "name": techTreeItemName(e.ItemID), "field": e.Field,
			"from": from, "to": to, "status": e.Status, "source": e.Source})
	}
	if err := tx.Commit(); err != nil {
		return nil, "could not save"
	}
	techTreeCosts.mu.Lock()
	for _, e := range edits {
		k := techTreeCostKey(e.ItemID, e.Field)
		if e.Amount == nil && e.Status == "guess" && e.Source == "" {
			delete(techTreeCosts.m, k)
			continue
		}
		c := techTreeCost{status: e.Status, source: e.Source}
		if e.Amount != nil {
			a := int32(*e.Amount)
			c.amount = &a
		}
		techTreeCosts.m[k] = c
	}
	techTreeCosts.mu.Unlock()
	return audit, ""
}

func adminAPITechTreeSetValues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values []techTreeValueEdit `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	audit, why := saveTechTreeValues(database, req.Values)
	if why != "" {
		writeAdminError(w, http.StatusBadRequest, why)
		return
	}
	adminAudit(database, r, "tech tree costs", "", map[string]any{"changes": audit})
	writeAdminJSON(w, map[string]any{"ok": true, "saved": len(audit)})
}

// adminAPITechTreeExport is every stored value, as the import takes it (the
// file can be committed to the repo as data).
func adminAPITechTreeExport(w http.ResponseWriter, _ *http.Request) {
	loadTechTreeCosts()
	out := []map[string]any{}
	techTreeCosts.mu.RLock()
	for k, c := range techTreeCosts.m {
		id, _ := strconv.Atoi(k[0])
		row := map[string]any{"item_id": id, "name": techTreeItemName(int32(id)), "field": k[1], "amount": nil, "status": c.status, "source": c.source}
		if c.amount != nil {
			row["amount"] = *c.amount
		}
		out = append(out, row)
	}
	techTreeCosts.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i]["item_id"] != out[j]["item_id"] {
			return out[i]["item_id"].(int) < out[j]["item_id"].(int)
		}
		return out[i]["field"].(string) < out[j]["field"].(string)
	})
	w.Header().Set("Content-Disposition", `attachment; filename="techtree-costs.json"`)
	writeAdminJSON(w, map[string]any{"values": out})
}

// adminAPITechTreeImport stores an exported file's values (merged: values
// not in the file stay as they are).
func adminAPITechTreeImport(w http.ResponseWriter, r *http.Request) {
	adminAPITechTreeSetValues(w, r)
}
