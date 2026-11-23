package main

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// The CREDIT PACKS tab lists the virtual currency catalog's entities: each
// pack is there as a GP-priced "item" entity whose "currency" array holds
// the credits it grants (credit_packs.go).
func TestCreditPacksInTheVirtualCurrencyCatalog(t *testing.T) {
	catalog := gatewayRequestedCatalogCollection("", "currency_catalog_virtual", true)
	entities, _ := catalog["entities"].([]any)
	found := map[string]map[string]any{}
	for _, e := range entities {
		entity := e.(map[string]any)
		if id, _ := entity["entity_id"].(string); id != "" {
			found[id] = entity
		}
	}
	if len(creditPacks) != 8 {
		t.Fatalf("%d packs, want the 8 of the original GP to CR bucket", len(creditPacks))
	}
	for _, p := range creditPacks {
		entity, ok := found[strconv.Itoa(int(p.id))]
		if !ok {
			t.Errorf("pack %d not in the virtual currency catalog", p.id)
			continue
		}
		if entity["entity_type"] != "item" {
			t.Errorf("pack %d entity_type %v, want item", p.id, entity["entity_type"])
		}
		if entity["currency_id"] != "SP_regular" || fmt.Sprint(entity["Price"]) != strconv.Itoa(int(p.gp)) {
			t.Errorf("pack %d priced %v %v, want %d SP_regular", p.id, entity["Price"], entity["currency_id"], p.gp)
		}
		cur, _ := entity["currency"].([]any)
		if len(cur) != 1 || fmt.Sprint(cur[0].(map[string]any)["amount"]) != strconv.Itoa(int(p.gp*gatewayGpToCreditsRate)) {
			t.Errorf("pack %d currency %v, want [{amount %d}]", p.id, cur, p.gp*gatewayGpToCreditsRate)
		}
	}
	// The documented rate: 500 GP = 52,500 credits.
	if p, _ := creditPackByID(970005); p.gp != 500 || p.credits() != 52500 {
		t.Errorf("500 GP pack = %d GP / %d credits", p.gp, p.credits())
	}
}

// Buying a pack charges its GP, adds its credits, records nothing as owned,
// can be repeated, and is refused without enough GP. Both id forms (item id
// and "999" SKU) buy the same pack.
func TestCreditPackPurchase(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	if _, err := database.Exec(`UPDATE player_state SET soft_currency=1000, premium_currency=600 WHERE user_id=?`, auditPID); err != nil {
		t.Fatal(err)
	}
	balances := func() (credits, gp int64) {
		_ = database.QueryRow(`SELECT soft_currency, premium_currency FROM player_state WHERE user_id=?`, auditPID).Scan(&credits, &gp)
		return
	}
	buy := func(offer string) (string, string, string) {
		reply := buildMmogPurchasePayload("YA_PurchaseItem", auditPID, auditRequest("YA_PurchaseItem", "offer", offer, "quantity", "7"))
		return protocol.ExtractStringField(reply, "result"), protocol.ExtractStringField(reply, "detailedResult"),
			protocol.ExtractStringField(reply, "currency")
	}

	if result, detail, currency := buy("970005"); result != "bought" || currency != "SP_regular" {
		t.Fatalf("500 GP pack: %q / %q / %q", result, detail, currency)
	}
	if c, gp := balances(); c != 1000+52500 || gp != 100 {
		t.Errorf("after one pack: %d credits %d GP, want 53500 / 100", c, gp)
	}
	if result, _, _ := buy("999970003"); result != "bought" { // 100 GP, by SKU
		t.Fatalf("100 GP pack by SKU: %q", result)
	}
	if c, gp := balances(); c != 1000+52500+10500 || gp != 0 {
		t.Errorf("after two packs: %d credits %d GP, want 64000 / 0 (quantity is always 1)", c, gp)
	}
	if result, detail, _ := buy("970002"); result != "failed" || detail != "insufficient premium currency" {
		t.Errorf("with 0 GP: %q / %q, want failed / insufficient premium currency", result, detail)
	}
	if c, gp := balances(); c != 64000 || gp != 0 {
		t.Errorf("a refused pack changed the balance: %d credits %d GP", c, gp)
	}
	if n := auditPurchaseRows(t, auditPID, 970005); n != 0 {
		t.Errorf("%d purchase rows for a credit pack, want 0", n)
	}
}

// No entity of the virtual currency catalog -- the CREDIT PACKS tab lists
// them all -- has an empty "currency" array: the tile list copies element 0
// unchecked and an empty array crashed the client (minidumps 2026-11-23,
// DreadGame-Win64-Shipping.exe+0x332CD6 called from +0xAB300B).
func TestNoEmptyCurrencyArrayInTheCreditPacksTab(t *testing.T) {
	catalog := gatewayRequestedCatalogCollection("", "currency_catalog_virtual", true)
	entities, _ := catalog["entities"].([]any)
	if len(entities) == 0 {
		t.Fatal("empty catalog")
	}
	for _, e := range entities {
		entity := e.(map[string]any)
		if cur, isArray := entity["currency"].([]any); isArray {
			if len(cur) == 0 {
				t.Errorf("entity %v (%v) has an empty currency array", entity["entity_id"], entity["entity_type"])
				continue
			}
			if _, ok := cur[0].(map[string]any); !ok {
				t.Errorf("entity %v: currency[0] is %T, want an object", entity["entity_id"], cur[0])
			}
		}
	}
}
