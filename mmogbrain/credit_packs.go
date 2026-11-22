package main

import (
	"database/sql"
	"strconv"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
	"github.com/sirupsen/logrus"
)

// GP to credits: the Market's Currency section, tab CREDIT PACKS.
//
// How the client builds the tab, verified in the exe (2026-11-20/22):
//
//   - The tab's offers are the entity_ids of the VIRTUAL CURRENCY catalog
//     (currency_pack_vc). The client collects them (0x2A60370) into the doc
//     key "Gp2CreditsConversion" of its own rebuild (0x2A5C800), so a scalar
//     sent under that name is overwritten and never read.
//   - The tile list (0x140AB2C50, via GetMarketItemsByCurrencyPanelID) looks
//     each id up as an int offer (0x14041F980) and lists it when the offer has
//     its item data (+0x68) and a GP price (+0x30; the CR -> GP tab reads the
//     credits price +0x34 instead). The amount a tile shows is the "amount"
//     of the first element of the raw entity's "currency" array (keyed by
//     the id string), else the item's.
//   - An offer priced exactly 1 GP is the exchange rate offer (0x14053C010);
//     it is left out of the tiles. None is sent: nothing else reads it.
//   - The entity goes through the market's "item" converter (0x142A5B960):
//     an "item" entity whose id is in the item list -- the currency catalog's
//     entities are appended to that same list -- is an ordinary item offer.
//   - Clicking a tile opens UI_Popup_GpToCreditsDetails, a plain confirmation
//     (GP cost, credits; bytecode dumped 2026-11-22), whose Confirm goes
//     OnGpToCreditsButtonSelected -> HandleItemPurchaseClick ->
//     RequestMarketItemPurchase: an ordinary YA_PurchaseItem for the offer.
//
// The original catalog's "GP to CR" bucket (CatalogIDTable.json) holds eight
// offers, SKUs 999970002-999970009; their item ids here are 970002-970009
// (top byte 0, which claims no item category). The rate is the documented one,
// 500 GP = 52,500 credits (patch 1.4.1), gatewayGpToCreditsRate.
//
// Until 2026-11-22 the currency catalog held only the bundle offers, and
// nothing converted GP to credits. The bundles are GP-priced offers with a
// "currency" array too, so the tile rule above lists them in this tab as well
// (theory; they must stay in this catalog, gatewayBundleListing).

type creditPack struct {
	id int32 // item id; the offer SKU is "999" + id
	gp int32
}

// creditPacks are the eight "GP to CR" offers.
// GUESS: the GP price of each pack. No source gives the original pack sizes;
// these follow the GP store's own steps, smallest first, and every pack
// converts at the documented rate.
var creditPacks = []creditPack{
	{id: 970002, gp: 50},
	{id: 970003, gp: 100},
	{id: 970004, gp: 250},
	{id: 970005, gp: 500},
	{id: 970006, gp: 1000},
	{id: 970007, gp: 2500},
	{id: 970008, gp: 5000},
	{id: 970009, gp: 10000},
}

func (p creditPack) credits() int32 { return p.gp * gatewayGpToCreditsRate }

// creditPackByID finds a pack by item id or by its "999"-prefixed SKU, the
// two forms an offer id can arrive in.
func creditPackByID(id int32) (creditPack, bool) {
	for _, p := range creditPacks {
		if id == p.id || id == 999000000+p.id {
			return p, true
		}
	}
	return creditPack{}, false
}

// creditPackEntities are the CREDIT PACKS offers of the virtual currency
// catalog: GP-priced "item" entities that carry the credits they grant in
// "currency":[{"amount"}] (what the tile shows) and ProvidedCredits.
func creditPackEntities(playerDataReady bool) []any {
	out := make([]any, 0, len(creditPacks))
	for _, p := range creditPacks {
		name := formatThousands(p.credits()) + " Credits"
		seed := gatewayCatalogEntitySeed{
			itemID:               p.id,
			externalID:           "999" + strconv.Itoa(int(p.id)),
			displayName:          name,
			description:          formatThousands(p.gp) + " GP converted to " + name + ".",
			entityType:           "item",
			itemType:             "currency_pack",
			priceCurrencyID:      vanityCurrency,
			priceAmount:          p.gp,
			providedCredits:      p.credits(),
			quantity:             1,
			localizedName:        dreadconfig.Localized{"en": name},
			localizedDescription: dreadconfig.Localized{"en": formatThousands(p.gp) + " GP converted to " + name + "."},
		}
		entity := gatewayMarketEntity(seed, playerDataReady)
		// The tile reads the raw entity's "currency" as an ARRAY (the parsed
		// tree's type 6) and "amount" from its first element, an object
		// (type 5) -- the bundles' own format (gatewayMarketEntity).
		entity["currency"] = []any{map[string]any{"amount": p.credits(), "currency_type": "CR"}}
		out = append(out, entity)
	}
	return out
}

// buyCreditPack charges the pack's GP and adds its credits, in one
// conditional update (the balance check and the write cannot interleave with
// another purchase). Nothing is recorded as owned: a pack can be bought again.
func buyCreditPack(database *sql.DB, pid string, p creditPack) (charged int32, reason string) {
	res, err := database.Exec(`UPDATE player_state SET premium_currency=premium_currency-?, soft_currency=soft_currency+?,
		updated_at=datetime('now') WHERE user_id=? AND premium_currency>=?`, p.gp, p.credits(), pid, p.gp)
	if err != nil {
		return 0, "currency exchange failed"
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, "insufficient premium currency"
	}
	logrus.WithFields(logrus.Fields{"player": pid, "pack": p.id, "gp": p.gp, "credits": p.credits()}).
		Info("mmog: credit pack bought")
	return p.gp, ""
}

// formatThousands writes n with comma thousands separators.
func formatThousands(n int32) string {
	s := strconv.Itoa(int(n))
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
