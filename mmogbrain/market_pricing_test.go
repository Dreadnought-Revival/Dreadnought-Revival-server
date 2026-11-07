package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// useCleanMarketPricing starts a test with no dashboard prices or sales.
func useCleanMarketPricing(t *testing.T) {
	t.Helper()
	loadMarketPricing()
	reset := func() {
		marketOverrides.mu.Lock()
		marketOverrides.m = map[string]int32{}
		marketOverrides.mu.Unlock()
		marketSales.mu.Lock()
		marketSales.sales, marketSales.ended = nil, map[string]time.Time{}
		marketSales.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func soldCosmetic(t *testing.T, category int32) int32 {
	t.Helper()
	for _, v := range dreadconfig.VanityItems() {
		if v.Category() != category || halloweenSaleItems[v.ItemID] {
			continue
		}
		if _, sold, _ := vanityOffer(v.ItemID); sold {
			return v.ItemID
		}
	}
	t.Fatalf("no sold cosmetic in category %d", category)
	return 0
}

// Dashboard prices reach the store and the till alike, go back to the
// code's value on "default", and refuse nonsense.
func TestAdminMarketPrices(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanMarketPricing(t)
	r := adminTestRouter()
	coating, decal := soldCosmetic(t, 22), soldCosmetic(t, 23)
	savedClock := marketClock
	marketClock = func() time.Time { return time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC) } // no built-in sale
	t.Cleanup(func() { marketClock = savedClock })

	if rec := adminPost(r, "/admin/api/market/prices", "", `{"vanity":150}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}
	for _, body := range []string{`{"nope":5}`, `{"vanity":0}`, `{"vanity":10000001}`, `{}`} {
		if rec := adminPost(r, "/admin/api/market/prices", "test-admin-key", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}

	rec := adminPost(r, "/admin/api/market/prices", "test-admin-key", `{"vanity":150,"coating":400,"hero_t3":3000,"ship_t2":60000,"module_t1":4000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set prices: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := purchasePriceForItemChecked(decal); p != 150 {
		t.Errorf("decal charged %d, want 150", p)
	}
	if p, _ := purchasePriceForItemChecked(coating); p != 400 {
		t.Errorf("coating charged %d, want 400", p)
	}
	if heroPriceGP(3) != 3000 || heroPriceGP(1) != 1500 {
		t.Errorf("hero prices %d / %d, want 3000 / 1500", heroPriceGP(3), heroPriceGP(1))
	}
	if gatewayMarketCreditPrice("ship", 2) != 60000 || gatewayMarketCreditPrice("ship", 3) != 100000 ||
		gatewayMarketCreditPrice("weapon", 1) != 4000 || gatewayMarketCreditPrice("module", 2) != 10000 {
		t.Error("credit curve overrides not applied per type and tier")
	}
	var stored int
	_ = database.QueryRow(`SELECT COUNT(*) FROM market_price_overrides`).Scan(&stored)
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='market prices'`).Scan(&audits)
	if stored != 5 || audits != 1 {
		t.Errorf("%d stored overrides, %d audit rows; want 5 and 1", stored, audits)
	}

	// A restart reads them back.
	marketOverrides.mu.Lock()
	marketOverrides.m = map[string]int32{}
	marketOverrides.mu.Unlock()
	marketOverrides.once = sync.Once{}
	if p, _ := purchasePriceForItemChecked(decal); p != 150 {
		t.Errorf("after a reload the decal costs %d, want 150", p)
	}

	if rec := adminPost(r, "/admin/api/market/prices", "test-admin-key", `{"vanity":null}`); rec.Code != http.StatusOK {
		t.Fatalf("default: %d", rec.Code)
	}
	if p, _ := purchasePriceForItemChecked(decal); p != vanityPrice {
		t.Errorf("decal back at default costs %d, want %d", p, vanityPrice)
	}
	var listed struct {
		Prices []struct {
			Key        string `json:"key"`
			Value      int32  `json:"value"`
			Overridden bool   `json:"overridden"`
		} `json:"prices"`
	}
	_ = json.Unmarshal(adminGet(r, "/admin/api/market", "test-admin-key").Body.Bytes(), &listed)
	for _, p := range listed.Prices {
		if (p.Key == "vanity" && (p.Overridden || p.Value != vanityPrice)) || (p.Key == "coating" && (!p.Overridden || p.Value != 400)) {
			t.Errorf("listed %+v", p)
		}
	}
}

// A dashboard sale lowers its cosmetics' price (shelf and till) while it
// runs; "End now" ends it, also for a built-in campaign.
func TestAdminMarketSales(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanMarketPricing(t)
	r := adminTestRouter()
	coating, decal := soldCosmetic(t, 22), soldCosmetic(t, 23)
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	savedClock := marketClock
	marketClock = func() time.Time { return now }
	t.Cleanup(func() { marketClock = savedClock })

	for _, body := range []string{
		`{"name":"","percent":20,"scope":"coatings","end":"2026-12-20T00:00:00Z"}`,
		`{"name":"x","percent":95,"scope":"coatings","end":"2026-12-20T00:00:00Z"}`,
		`{"name":"x","percent":20,"scope":"ships","end":"2026-12-20T00:00:00Z"}`,
		`{"name":"x","percent":20,"scope":"coatings","end":"2026-12-01T00:00:00Z"}`,
	} {
		if rec := adminPost(r, "/admin/api/market/sales", "test-admin-key", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	rec := adminPost(r, "/admin/api/market/sales", "test-admin-key", `{"name":"Winter Sale","percent":20,"scope":"coatings","end":"2026-12-20T00:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := purchasePriceForItemChecked(coating); p != coatingPrice*80/100 {
		t.Errorf("coating in the sale costs %d, want %d", p, coatingPrice*80/100)
	}
	if p, _ := purchasePriceForItemChecked(decal); p != vanityPrice {
		t.Errorf("a decal outside the sale costs %d, want %d", p, vanityPrice)
	}
	seed := gatewayCatalogEntitySeed{itemID: coating, itemType: "vanity", priceAmount: coatingPrice}
	if d := campaignDiscounts(seed); len(d) != 1 || campaignShelfPrice(seed) != coatingPrice*80/100 {
		t.Errorf("the shelf does not show the sale: %v, %d", d, campaignShelfPrice(seed))
	}

	// Stored: a restart still has it.
	reloadMarketSales()
	if _, ok := campaignForItem(coating, now); !ok {
		t.Error("sale lost on reload")
	}

	// End it now.
	var sale struct {
		Sales []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Items int    `json:"items"`
		} `json:"sales"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sale)
	var id string
	for _, s := range sale.Sales {
		if s.State == "running" {
			id = s.ID
			if s.Items == 0 {
				t.Error("the sale lists no items")
			}
		}
	}
	if id == "" {
		t.Fatalf("no running sale listed: %s", rec.Body.String())
	}
	now = now.Add(time.Hour)
	if rec := adminPost(r, "/admin/api/market/sales/"+id+"/end", "test-admin-key", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := purchasePriceForItemChecked(coating); p != coatingPrice {
		t.Errorf("after the end the coating costs %d, want %d", p, coatingPrice)
	}
	if rec := adminPost(r, "/admin/api/market/sales/"+id+"/end", "test-admin-key", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("ending it twice: %d, want 409", rec.Code)
	}

	// The built-in Halloween campaign, ended early.
	now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	var halloween int32
	for id := range halloweenSaleItems {
		halloween = id
		break
	}
	if _, ok := campaignForItem(halloween, now); !ok {
		t.Fatal("Halloween sale not running on 10 November")
	}
	if rec := adminPost(r, "/admin/api/market/sales/halloween_2026/end", "test-admin-key", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("end Halloween: %d %s", rec.Code, rec.Body.String())
	}
	now = now.Add(time.Minute)
	if _, ok := campaignForItem(halloween, now); ok {
		t.Error("Halloween still running after End now")
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action IN ('market sale','market sale ended')`).Scan(&audits)
	if audits != 3 {
		t.Errorf("%d sale audit rows, want 3", audits)
	}
}
