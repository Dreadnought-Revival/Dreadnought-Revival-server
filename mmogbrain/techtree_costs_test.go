package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func useCleanTechTreeCosts(t *testing.T) {
	t.Helper()
	loadTechTreeCosts()
	reset := func() {
		techTreeCosts.mu.Lock()
		techTreeCosts.m = map[[2]string]techTreeCost{}
		techTreeCosts.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

type ttPage struct {
	Nodes []techTreeNode `json:"nodes"`
	Done  int            `json:"done"`
	Total int            `json:"total"`
}

func ttGetPage(t *testing.T, r http.Handler, id int32) ttPage {
	t.Helper()
	rec := adminGet(r, fmt.Sprintf("/admin/api/techtree/ships/%d", id), "test-admin-key")
	var p ttPage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
		t.Fatalf("page %d: %d %s", id, rec.Code, rec.Body.String())
	}
	return p
}

// A per-node cost set on the dashboard is what the tech tree shows, what
// research charges, what the store shows and what a purchase charges; a
// value can also only be annotated; it all survives a restart.
func TestAdminTechTreeCosts(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanTechTreeCosts(t)
	r := adminTestRouter()
	const blud = 33489283 // Destroyer (Heavy) T4, after Dola T3
	page := ttGetPage(t, r, blud)
	if len(page.Nodes) < 2 || page.Nodes[0].ItemID != blud || page.Nodes[0].Kind != "hull" || page.Total == 0 || page.Done != 0 {
		t.Fatalf("Blud page: %+v", page)
	}
	var module techTreeNode
	for _, n := range page.Nodes[1:] {
		if n.Kind == "module" {
			module = n
			break
		}
	}
	if module.ItemID == 0 {
		t.Fatal("no module on the Blud page")
	}

	for _, body := range []string{
		`{"values":[{"item_id":1,"field":"xp","amount":5}]}`,
		`{"values":[{"item_id":33489283,"field":"xp","amount":-1}]}`,
		`{"values":[{"item_id":33489283,"field":"xp","amount":5,"status":"maybe"}]}`,
		`{"values":[{"item_id":33489283,"field":"xp","amount":5,"status":"sourced"}]}`, // no source
		`{"values":[]}`,
	} {
		if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}

	body := fmt.Sprintf(`{"values":[
		{"item_id":%d,"field":"xp","amount":30000,"status":"sourced","source":"wiki snapshot 2019"},
		{"item_id":%d,"field":"price","amount":90000,"status":"accepted"},
		{"item_id":%d,"field":"xp","amount":1234},
		{"item_id":%d,"field":"price","status":"accepted","source":"keep the curve"}]}`, blud, blud, module.ItemID, module.ItemID)
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", body); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}

	// Tree (display) and research charge.
	if xp, ok := techTreeResearchCost(blud); !ok || xp != 30000 {
		t.Errorf("Blud research cost %d, want 30000", xp)
	}
	if xp, _ := techTreeResearchCost(module.ItemID); xp != 1234 {
		t.Errorf("module research cost %d, want 1234", xp)
	}
	// Purchase charge.
	if p, _ := purchasePriceForItemChecked(blud); p != 90000 {
		t.Errorf("Blud charged %d, want 90000", p)
	}
	if p, _ := purchasePriceForItemChecked(module.ItemID); p != module.Price.Default {
		t.Errorf("annotated-only module price %d, want its default %d", p, module.Price.Default)
	}
	if techTreePriceOr(blud, 1) != 90000 {
		t.Error("the catalog's price for Blud is not the override")
	}

	page = ttGetPage(t, r, blud)
	hull := page.Nodes[0]
	if !hull.XP.Overridden || hull.XP.Value != 30000 || hull.XP.Default != 20000 || hull.XP.Status != "sourced" || hull.XP.Source != "wiki snapshot 2019" {
		t.Errorf("hull XP listed as %+v", hull.XP)
	}
	if page.Done != 3 {
		t.Errorf("%d values sourced or accepted, want 3", page.Done)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='tech tree costs'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d audit rows, want 1", audits)
	}

	// Over the client's 5 digits: capped for display and charge alike, and warned.
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", fmt.Sprintf(`{"values":[{"item_id":%d,"field":"xp","amount":150000}]}`, blud)); rec.Code != http.StatusOK {
		t.Fatalf("save 150000: %d", rec.Code)
	}
	if xp, _ := techTreeResearchCost(blud); xp != techTreeMaxDisplayableCost {
		t.Errorf("150000 XP charged as %d, want %d", xp, techTreeMaxDisplayableCost)
	}
	if w := ttGetPage(t, r, blud).Nodes[0].Warnings; len(w) == 0 {
		t.Error("no warning for a 6-digit research cost")
	}

	// Export, then a restart and an import into an empty table.
	rec := adminGet(r, "/admin/api/techtree/export", "test-admin-key")
	var exported struct {
		Values []map[string]any `json:"values"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &exported) != nil || len(exported.Values) != 4 {
		t.Fatalf("export: %s", rec.Body.String())
	}
	techTreeCosts.mu.Lock()
	techTreeCosts.m = map[[2]string]techTreeCost{}
	techTreeCosts.mu.Unlock()
	techTreeCosts.once = sync.Once{}
	if xp, _ := techTreeResearchCost(module.ItemID); xp != 1234 {
		t.Errorf("after a restart the module costs %d, want 1234", xp)
	}
	if _, err := database.Exec(`DELETE FROM techtree_cost_overrides`); err != nil {
		t.Fatal(err)
	}
	useCleanTechTreeCosts(t)
	if rec := adminPost(r, "/admin/api/techtree/import", "test-admin-key", rec.Body.String()); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := purchasePriceForItemChecked(blud); p != 90000 {
		t.Errorf("after the import Blud costs %d, want 90000", p)
	}

	// Back to the code's value.
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", fmt.Sprintf(`{"values":[{"item_id":%d,"field":"xp","amount":null,"status":"guess"}]}`, blud)); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if xp, _ := techTreeResearchCost(blud); xp != 20000 {
		t.Errorf("reset Blud research cost %d, want 20000", xp)
	}
}

// A hero's only value is its GP price, which the Market and the purchase share.
func TestAdminTechTreeHeroPrice(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	useCleanTechTreeCosts(t)
	r := adminTestRouter()
	var hero heroShipLoadout
	for _, h := range heroShipLoadouts {
		if _, havoc := havocUnlockThreshold(h.loadoutID); !havoc {
			hero = h // one the Market sells on its own
			break
		}
	}
	page := ttGetPage(t, r, hero.loadoutID)
	if len(page.Nodes) != 1 || page.Nodes[0].Kind != "hero" || page.Nodes[0].XP != nil || page.Nodes[0].Price.Currency != "GP" {
		t.Fatalf("hero page: %+v", page.Nodes)
	}
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", fmt.Sprintf(`{"values":[{"item_id":%d,"field":"xp","amount":5}]}`, hero.loadoutID)); rec.Code != http.StatusBadRequest {
		t.Errorf("hero research cost accepted: %d", rec.Code)
	}
	if rec := adminPost(r, "/admin/api/techtree/values", "test-admin-key", fmt.Sprintf(`{"values":[{"item_id":%d,"field":"price","amount":1999,"status":"accepted"}]}`, hero.loadoutID)); rec.Code != http.StatusOK {
		t.Fatalf("hero price: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := purchasePriceForItemChecked(hero.loadoutID); p != 1999 {
		t.Errorf("hero charged %d, want 1999", p)
	}
	found := false
	for _, seed := range heroCatalogSeeds(adminTestPID) {
		if seed.itemID == hero.loadoutID {
			found = true
			if seed.priceAmount != 1999 {
				t.Errorf("hero in the Market at %d, want 1999", seed.priceAmount)
			}
		}
	}
	if !found {
		t.Error("hero not in the Market's hero entries")
	}
}
