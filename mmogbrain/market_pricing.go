package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
	"github.com/gorilla/mux"
)

// Market pricing and sales on the admin dashboard (issue #13). The prices
// were constants in code (vanityPrice, coatingPrice, heroPriceGP, bundle
// priceEUR, gatewayMarketCreditPrice's curve) and the sales a list in
// market_campaigns.go. The code values stay the defaults; an override is a
// row of market_price_overrides, a sale a row of market_sales.
//
// One price per item, for the shelf and the till: the catalog and
// purchasePriceForItemChecked read the same functions, which read these
// overrides. The catalog is built on every fetch, and the client fetches it
// at login (no in-session re-fetch exists, dreadnought-in-session-refresh),
// so a player online during a change sees the old price until they log in
// again -- and is charged the new one if they buy before that.

type marketPriceKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Currency string `json:"currency"` // "GP" or "credits"
	Default  int32  `json:"default"`
}

const maxMarketPrice = 10000000

// marketPriceKeys lists every price the dashboard can set, with the code's
// value as the default.
func marketPriceKeys() []marketPriceKey {
	keys := []marketPriceKey{
		{"vanity", "Every sold cosmetic (except coatings)", "Cosmetics", "GP", vanityPrice},
		{"coating", "Ship coating", "Cosmetics", "GP", coatingPrice},
		{"hero_t12", "Hero ship, tier I-II", "Hero ships", "GP", 1500},
		{"hero_t3", "Hero ship, tier III", "Hero ships", "GP", 2500},
		{"hero_t45", "Hero ship, tier IV-V", "Hero ships", "GP", 3500},
	}
	for t := int32(1); t <= 5; t++ {
		keys = append(keys, marketPriceKey{"ship_t" + strconv.Itoa(int(t)), "Ship, tier " + romanTier(t), "Ships and modules (credits)", "credits", 25000 << uint(t-1)})
	}
	for t := int32(1); t <= 5; t++ {
		keys = append(keys, marketPriceKey{"module_t" + strconv.Itoa(int(t)), "Weapon / module, tier " + romanTier(t), "Ships and modules (credits)", "credits", 5000 << uint(t-1)})
	}
	for _, b := range marketBundles {
		keys = append(keys, marketPriceKey{"bundle_" + strconv.Itoa(int(b.id)), b.name, "Bundles", "GP", int32(b.priceEUR*100 + 0.5)})
	}
	return keys
}

func romanTier(t int32) string {
	return [...]string{"", "I", "II", "III", "IV", "V"}[t]
}

// loadMarketPricing reads the overrides and sales at startup (main), so the
// price functions never query the database themselves: they run while the
// catalog is built, and a query inside someone's open rows loop would
// deadlock the one connection (dreadnought-single-connection-db). The lazy
// loads below are the fallback for tests.
func loadMarketPricing() {
	loadMarketOverrides()
	loadMarketSales()
}

var marketOverrides struct {
	once sync.Once
	mu   sync.RWMutex
	m    map[string]int32
}

func loadMarketOverrides() {
	marketOverrides.once.Do(func() {
		m := map[string]int32{}
		if database := currentMmogPlayerStateDB(); database != nil {
			if rows, err := database.Query(`SELECT key, amount FROM market_price_overrides`); err == nil {
				for rows.Next() {
					var k string
					var v int32
					if rows.Scan(&k, &v) == nil && v > 0 {
						m[k] = v
					}
				}
				_ = rows.Close()
			}
		}
		marketOverrides.mu.Lock()
		marketOverrides.m = m
		marketOverrides.mu.Unlock()
	})
}

// marketPrice is the price under key: the dashboard's override, or def.
func marketPrice(key string, def int32) int32 {
	loadMarketOverrides()
	marketOverrides.mu.RLock()
	defer marketOverrides.mu.RUnlock()
	if v, ok := marketOverrides.m[key]; ok {
		return v
	}
	return def
}

// --- sales -----------------------------------------------------------------------

// saleScopes are what a dashboard sale can cover. Sales apply to cosmetics
// only: that is where the client shows a campaign price (the customizer and
// the captain screen, market_campaigns.go); hero ships and bundles are not
// wired to campaigns.
var saleScopes = map[string]struct {
	label string
	item  func(int32) bool
}{
	"all":       {"Every sold cosmetic", func(int32) bool { return true }},
	"ship":      {"Ship cosmetics", func(id int32) bool { c := (id >> 24) & 0xff; return c >= 20 && c <= 24 }},
	"coatings":  {"Ship coatings", func(id int32) bool { return (id>>24)&0xff == 22 }},
	"captain":   {"Captain cosmetics", func(id int32) bool { c := (id >> 24) & 0xff; return c >= 50 && c <= 55 }},
	"halloween": {"Earlier Halloween items", func(id int32) bool { return halloweenSaleItems[id] }},
}

type marketSale struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Percent int32     `json:"percent"`
	Scope   string    `json:"scope"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

var marketSales struct {
	once  sync.Once
	mu    sync.RWMutex
	sales []marketSale
	ended map[string]time.Time // built-in campaigns ended early, by id
}

func loadMarketSales() {
	marketSales.once.Do(reloadMarketSales)
}

func reloadMarketSales() {
	var sales []marketSale
	ended := map[string]time.Time{}
	if database := currentMmogPlayerStateDB(); database != nil {
		if rows, err := database.Query(`SELECT id, name, percent, scope, start_at, end_at FROM market_sales ORDER BY id`); err == nil {
			for rows.Next() {
				var s marketSale
				var start, end int64
				if rows.Scan(&s.ID, &s.Name, &s.Percent, &s.Scope, &start, &end) == nil {
					s.Start, s.End = time.Unix(start, 0), time.Unix(end, 0)
					sales = append(sales, s)
				}
			}
			_ = rows.Close()
		}
		if rows, err := database.Query(`SELECT name, value FROM server_flags WHERE name LIKE 'campaign\_ended\_%' ESCAPE '\'`); err == nil {
			for rows.Next() {
				var k, v string
				if rows.Scan(&k, &v) == nil {
					if n, err := strconv.ParseInt(v, 10, 64); err == nil {
						ended[strings.TrimPrefix(k, "campaign_ended_")] = time.Unix(n, 0)
					}
				}
			}
			_ = rows.Close()
		}
	}
	marketSales.mu.Lock()
	marketSales.sales, marketSales.ended = sales, ended
	marketSales.mu.Unlock()
}

// allMarketCampaigns is the code's campaigns (cut short where the dashboard
// ended them) and the dashboard's sales. The first running one that covers
// an item is its price.
func allMarketCampaigns() []marketCampaign {
	loadMarketSales()
	marketSales.mu.RLock()
	defer marketSales.mu.RUnlock()
	out := make([]marketCampaign, 0, len(marketCampaigns)+len(marketSales.sales))
	for _, c := range marketCampaigns {
		if at, ok := marketSales.ended[c.id]; ok && at.Before(c.end) {
			c.end = at
		}
		out = append(out, c)
	}
	for _, s := range marketSales.sales {
		scope, ok := saleScopes[s.Scope]
		if !ok {
			continue
		}
		out = append(out, marketCampaign{id: "sale_" + strconv.FormatInt(s.ID, 10), name: s.Name,
			start: s.Start, end: s.End, percent: s.Percent, item: scope.item})
	}
	return out
}

// saleItemCount is how many sold cosmetics a scope covers (the preview).
func saleItemCount(scope string) int {
	sc, ok := saleScopes[scope]
	if !ok {
		return 0
	}
	n := 0
	for _, v := range dreadconfig.VanityItems() {
		if _, sold, _ := vanityOffer(v.ItemID); sold && sc.item(v.ItemID) {
			n++
		}
	}
	return n
}

// --- admin API ---------------------------------------------------------------------

func registerAdminMarketPricing(api *mux.Router) {
	api.HandleFunc("/market", adminAPIMarket).Methods(http.MethodGet)
	api.HandleFunc("/market/prices", adminAPISetMarketPrices).Methods(http.MethodPost)
	api.HandleFunc("/market/sales", adminAPICreateSale).Methods(http.MethodPost)
	api.HandleFunc("/market/sales/{id}/end", adminAPIEndSale).Methods(http.MethodPost)
}

func adminMarketJSON() map[string]any {
	loadMarketOverrides()
	type price struct {
		marketPriceKey
		Value      int32 `json:"value"`
		Overridden bool  `json:"overridden"`
	}
	prices := []price{}
	marketOverrides.mu.RLock()
	for _, k := range marketPriceKeys() {
		v, ok := marketOverrides.m[k.Key]
		if !ok {
			v = k.Default
		}
		prices = append(prices, price{k, v, ok})
	}
	marketOverrides.mu.RUnlock()

	now := marketClock()
	type sale struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Percent  int32  `json:"percent"`
		Scope    string `json:"scope"`
		Start    string `json:"start"`
		End      string `json:"end"`
		State    string `json:"state"` // upcoming, running, ended
		BuiltIn  bool   `json:"built_in"`
		Items    int    `json:"items"`
		ScopeTag string `json:"scope_label"`
	}
	sales := []sale{}
	builtIn := map[string]bool{}
	for _, c := range marketCampaigns {
		builtIn[c.id] = true
	}
	for _, c := range allMarketCampaigns() {
		x := sale{ID: c.id, Name: c.name, Percent: c.percent, Start: c.start.UTC().Format(time.RFC3339),
			End: c.end.UTC().Format(time.RFC3339), BuiltIn: builtIn[c.id]}
		switch {
		case now.Before(c.start):
			x.State = "upcoming"
		case c.active(now):
			x.State = "running"
		default:
			x.State = "ended"
		}
		if x.BuiltIn {
			x.Scope, x.ScopeTag = "halloween", "Earlier Halloween items (built in)"
		} else {
			marketSales.mu.RLock()
			for _, s := range marketSales.sales {
				if "sale_"+strconv.FormatInt(s.ID, 10) == c.id {
					x.Scope, x.ScopeTag = s.Scope, saleScopes[s.Scope].label
				}
			}
			marketSales.mu.RUnlock()
		}
		x.Items = saleItemCount(x.Scope)
		sales = append(sales, x)
	}
	scopes := []map[string]any{}
	for _, k := range []string{"all", "ship", "coatings", "captain", "halloween"} {
		scopes = append(scopes, map[string]any{"scope": k, "label": saleScopes[k].label, "items": saleItemCount(k)})
	}
	return map[string]any{"prices": prices, "sales": sales, "scopes": scopes, "max_price": maxMarketPrice}
}

func adminAPIMarket(w http.ResponseWriter, _ *http.Request) { writeAdminJSON(w, adminMarketJSON()) }

// adminAPISetMarketPrices sets prices {key: amount}; null puts a price back
// to the code's default.
func adminAPISetMarketPrices(w http.ResponseWriter, r *http.Request) {
	var req map[string]*int64
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil || len(req) == 0 {
		writeAdminError(w, http.StatusBadRequest, "send {key: amount} (null = default)")
		return
	}
	known := map[string]marketPriceKey{}
	for _, k := range marketPriceKeys() {
		known[k.Key] = k
	}
	for k, v := range req {
		if _, ok := known[k]; !ok {
			writeAdminError(w, http.StatusBadRequest, "unknown price "+k)
			return
		}
		if v != nil && (*v < 1 || *v > maxMarketPrice) {
			writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("%s: a price must be 1 to %d", known[k].Label, maxMarketPrice))
			return
		}
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	changes := map[string]any{}
	for k, v := range req {
		from := marketPrice(k, known[k].Default)
		var err error
		if v == nil {
			_, err = database.Exec(`DELETE FROM market_price_overrides WHERE key=?`, k)
		} else {
			_, err = database.Exec(`INSERT INTO market_price_overrides(key, amount) VALUES(?,?)
				ON CONFLICT(key) DO UPDATE SET amount=excluded.amount, updated_at=datetime('now')`, k, *v)
		}
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "could not save "+k)
			return
		}
		marketOverrides.mu.Lock()
		if v == nil {
			delete(marketOverrides.m, k)
		} else {
			marketOverrides.m[k] = int32(*v)
		}
		marketOverrides.mu.Unlock()
		to := any("default")
		if v != nil {
			to = *v
		}
		changes[k] = map[string]any{"from": from, "to": to}
	}
	adminAudit(database, r, "market prices", "", changes)
	writeAdminJSON(w, adminMarketJSON())
}

func adminAPICreateSale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Percent int32  `json:"percent"`
		Scope   string `json:"scope"`
		Start   string `json:"start"` // RFC 3339; "" = now
		End     string `json:"end"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.Join(strings.Fields(req.Name), " ")
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 60 {
		writeAdminError(w, http.StatusBadRequest, "the sale needs a name of at most 60 characters")
		return
	}
	if req.Percent < 1 || req.Percent > 90 {
		writeAdminError(w, http.StatusBadRequest, "the discount must be 1 to 90 percent")
		return
	}
	if _, ok := saleScopes[req.Scope]; !ok {
		writeAdminError(w, http.StatusBadRequest, "unknown scope "+req.Scope)
		return
	}
	start := marketClock()
	if req.Start != "" {
		t, err := time.Parse(time.RFC3339, req.Start)
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, "start: use a date and time")
			return
		}
		start = t
	}
	end, err := time.Parse(time.RFC3339, req.End)
	if err != nil || !end.After(start) || !end.After(marketClock()) {
		writeAdminError(w, http.StatusBadRequest, "the end must be in the future and after the start")
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	res, err := database.Exec(`INSERT INTO market_sales(name, percent, scope, start_at, end_at) VALUES(?,?,?,?,?)`,
		req.Name, req.Percent, req.Scope, start.Unix(), end.Unix())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not save the sale")
		return
	}
	id, _ := res.LastInsertId()
	reloadMarketSales()
	adminAudit(database, r, "market sale", "sale_"+strconv.FormatInt(id, 10), map[string]any{"name": req.Name,
		"percent": req.Percent, "scope": req.Scope, "start": start.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339),
		"items": saleItemCount(req.Scope)})
	writeAdminJSON(w, adminMarketJSON())
}

// adminAPIEndSale ends a sale now (a dashboard sale, or a built-in one).
func adminAPIEndSale(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	now := marketClock()
	var found *marketCampaign
	for _, c := range allMarketCampaigns() {
		if c.id == id {
			c := c
			found = &c
		}
	}
	if found == nil {
		writeAdminError(w, http.StatusNotFound, "no such sale")
		return
	}
	if !now.Before(found.end) {
		writeAdminError(w, http.StatusConflict, "the sale has already ended")
		return
	}
	var err error
	if n, ok := strings.CutPrefix(id, "sale_"); ok {
		_, err = database.Exec(`UPDATE market_sales SET end_at=? WHERE id=?`, now.Unix(), n)
	} else {
		_, err = database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES(?,?)`, "campaign_ended_"+id, strconv.FormatInt(now.Unix(), 10))
	}
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "could not end the sale")
		return
	}
	reloadMarketSales()
	adminAudit(database, r, "market sale ended", id, map[string]any{"name": found.name})
	writeAdminJSON(w, adminMarketJSON())
}
