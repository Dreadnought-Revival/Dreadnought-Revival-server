package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// Regression tests for the profile and inventory review of 2026-11-10. Each
// request below is one a stock client does not send: they are what a
// hand-built client could send, and each of them worked before that day.

const auditPID = "650dd79476a1484b8adcd01ac2f17354"

func auditRequest(name string, fields ...string) []byte {
	b := protocol.AppendStringField(nil, "RT", name)
	for i := 0; i+1 < len(fields); i += 2 {
		b = protocol.AppendStringField(b, fields[i], fields[i+1])
	}
	return b
}

func auditPurchaseRows(t *testing.T, pid string, itemID int32) int {
	t.Helper()
	var n int
	if err := currentMmogPlayerStateDB().QueryRow(`SELECT COUNT(*) FROM player_purchases WHERE user_id=? AND item_id=?`,
		pid, itemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sold cosmetic that is not free and not a coating, and a coating.
func auditSoldCosmetics(t *testing.T) (cosmetic, coating int32) {
	t.Helper()
	for _, v := range dreadconfig.VanityItems() {
		if isVanity, sold, _ := vanityOffer(v.ItemID); !isVanity || !sold || isFreeVanityItem(v.ItemID) {
			continue
		}
		if v.Category() == 22 && coating == 0 {
			coating = v.ItemID
		} else if v.Category() != 22 && cosmetic == 0 {
			cosmetic = v.ItemID
		}
	}
	if cosmetic == 0 || coating == 0 {
		t.Fatal("no sold cosmetic / coating in the vanity table")
	}
	return cosmetic, coating
}

// YA_UnlockItem is research, and research is for tech tree nodes. An id
// outside the tree skipped the cost check and was written as an owned
// purchase: a hero ship (with its loadout), a cosmetic and a coating for 0 XP.
func TestUnlockRefusesItemsOutsideTheTechTree(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	cosmetic, coating := auditSoldCosmetics(t)
	hero := heroShipLoadouts[0].loadoutID

	for _, id := range []int32{hero, cosmetic, coating, 0x7f000001} {
		if _, inTree := techTreeResearchCost(id); inTree {
			t.Fatalf("item %d is a tech tree node; the test needs one that is not", id)
		}
		req := auditRequest("YA_UnlockItem", "ItemID", strconv.Itoa(int(id)), "ShipXp", "0", "FreeXp", "0")
		if err := persistMmogPlayerMutation(auditPID, "YA_UnlockItem", req); err != nil {
			t.Fatal(err)
		}
		if n := auditPurchaseRows(t, auditPID, id); n != 0 {
			t.Errorf("unlock of %d (not in the tech tree) recorded %d purchase rows, want 0", id, n)
		}
		var loadouts int
		_ = database.QueryRow(`SELECT COUNT(*) FROM player_ship_loadouts WHERE user_id=? AND loadout_id=?`, auditPID, id).Scan(&loadouts)
		if loadouts != 0 {
			t.Errorf("unlock of %d granted a ship loadout", id)
		}
		for _, owned := range clientOwnedItemIDs(auditPID) {
			if owned == id {
				t.Errorf("unlock of %d made it an owned item", id)
			}
		}
	}
}

// The store sells bundles, researched weapons/modules, hero ships and sold
// cosmetics. Everything else used to be sold for credits at the price
// fallback: the shared form of a tier V module with no research (which then
// passed the fitting check for the per-ship item), a briefing, a pawn id, an
// id that is no item.
func TestPurchaseSellsOnlyWhatTheStoreOffers(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	const funds = 50_000_000
	if _, err := database.Exec(`UPDATE player_state SET soft_currency=?, premium_currency=? WHERE user_id=?`, funds, funds, auditPID); err != nil {
		t.Fatal(err)
	}
	var perShipT5 int32
	for _, item := range techTreeBaseItems() {
		if row, ok := perShipResearchRow(item.id); ok && row.Tier == 5 {
			perShipT5 = item.id
			break
		}
	}
	if perShipT5 == 0 {
		t.Fatal("no tier V per-ship research item in the tech tree")
	}
	buy := func(id int32) (result, detail string) {
		reply := buildMmogPurchasePayload("YA_PurchaseItem", auditPID, auditRequest("YA_PurchaseItem", "ItemID", strconv.Itoa(int(id))))
		return protocol.ExtractStringField(reply, "result"), protocol.ExtractStringField(reply, "detailedResult")
	}
	balances := func() (credits, premium int64) {
		_ = database.QueryRow(`SELECT soft_currency, premium_currency FROM player_state WHERE user_id=?`, auditPID).Scan(&credits, &premium)
		return
	}

	refused := map[string]int32{
		"shared form of a tier V module": sharedGearID(perShipT5),
		"officer briefing":               officerBriefings()[0],
		"pawn id":                        10<<24 | 0xff0001,
		"no item at all":                 0x7f000001,
	}
	for what, id := range refused {
		result, detail := buy(id)
		if result != "failed" || detail != "not for sale" {
			t.Errorf("%s (%d): result %q / %q, want failed / not for sale", what, id, result, detail)
		}
		if n := auditPurchaseRows(t, auditPID, id); n != 0 {
			t.Errorf("%s (%d): %d purchase rows, want 0", what, id, n)
		}
		if ownedItemSet(auditPID)[id] {
			t.Errorf("%s (%d) counts as owned for fitting", what, id)
		}
	}
	if credits, premium := balances(); credits != funds || premium != funds {
		t.Errorf("refused purchases changed the balances: credits %d, premium %d, want %d each", credits, premium, funds)
	}
	// The research rule itself is unchanged: not researched, not sold.
	if result, detail := buy(perShipT5); result != "failed" || detail != "not researched" {
		t.Errorf("unresearched per-ship module: result %q / %q, want failed / not researched", result, detail)
	}

	// What the store does sell still sells: a hero ship and a cosmetic, for GP.
	var hero int32
	for _, h := range heroShipLoadouts {
		if havocUnlockRefusal(auditPID, h.loadoutID) == "" {
			hero = h.loadoutID
			break
		}
	}
	cosmetic, _ := auditSoldCosmetics(t)
	for what, id := range map[string]int32{"hero ship": hero, "cosmetic": cosmetic} {
		if result, detail := buy(id); result != "bought" {
			t.Errorf("%s (%d): result %q / %q, want bought", what, id, result, detail)
		}
		if n := auditPurchaseRows(t, auditPID, id); n != 1 {
			t.Errorf("%s (%d): %d purchase rows, want 1", what, id, n)
		}
	}
	if credits, premium := balances(); credits != funds || premium >= funds {
		t.Errorf("after a hero and a cosmetic: credits %d (want %d), premium %d (want less than %d)", credits, funds, premium, funds)
	}
}

// YA_SavePlayerDisplayInformation saves the captain's look. A "DisplayName"
// field in it used to be stored as the player's name.
func TestSaveDisplayInformationDoesNotRename(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	if _, err := database.Exec(`UPDATE player_state SET display_name='Pilot' WHERE user_id=?`, auditPID); err != nil {
		t.Fatal(err)
	}
	stored := func() (name, disp string) {
		if err := database.QueryRow(`SELECT display_name, display_info FROM player_state WHERE user_id=?`, auditPID).Scan(&name, &disp); err != nil {
			t.Fatal(err)
		}
		return
	}
	save := func(fields ...string) {
		t.Helper()
		if err := persistMmogPlayerMutation(auditPID, "YA_SavePlayerDisplayInformation",
			auditRequest("YA_SavePlayerDisplayInformation", fields...)); err != nil {
			t.Fatal(err)
		}
	}

	// A request as a stock client sent it on 2026-11-10 (request log).
	// CHANGED 2026-11-10: this string had iiEw=872349700, a made-up value
	// for the part the captured request in persistSavePlayerDisplayInformation
	// elides; the client always exports iiEw as -1, and since the captain
	// ownership check an item the account does not own is not stored.
	const look = "GENDER_FEMALE;#iiS=872349703#iiH=872349769#iiHw=872349736#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0"
	save("disp", look, "DisplayName", "Server Admin")
	name, disp := stored()
	if name != "Pilot" {
		t.Errorf("display name is %q after a save carrying DisplayName, want it unchanged (Pilot)", name)
	}
	if disp != look {
		t.Errorf("captain look not saved: %q", disp)
	}

	save("DisplayName", "Server Admin")
	if name, _ := stored(); name != "Pilot" {
		t.Errorf("display name is %q after a name-only save, want Pilot", name)
	}

	// Not a captain string: kept out, the stored look stays.
	for _, bad := range []string{
		"GENDER_MALE;#iiS=1\ncaptain=x",
		"GENDER_MALE;#iiS=<b>1</b>",
		"GENDER_MALE;" + strings.Repeat("#iiS=872349703", 30),
	} {
		save("disp", bad)
		if _, disp := stored(); disp != look {
			t.Errorf("a display string that is not the client's format was stored: %.60q", disp)
		}
	}
}

// The battle server's loadout record is "key=value" lines and the mod takes
// the first line of each key. A loadout name with line breaks wrote its own
// fit ahead of the real one.
func TestLoadoutNameCannotInjectBattleLoadoutLines(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	loadout := mmogPlayerStateForPID(auditPID).shipLoadouts()[0]
	shipID := strconv.Itoa(int(loadout.loadoutID()))
	const injected = "X\nclass=9\r\ndisplay=1#2#3#4;5;6;7;8\nweapons=111,222\nabilities=1,2,3,4\nperks=5,6,7,8"

	record := func() string {
		t.Helper()
		got, ok := battleLoadoutFor(auditPID, loadout.entryID())
		if !ok {
			t.Fatal("the player's own loadout is not found")
		}
		return formatBattleLoadout(loadout.entryID(), auditPID, got)
	}
	want := record() // before anything is renamed
	oneLinePerKey := func(reply string) {
		t.Helper()
		seen := map[string]int{}
		for _, line := range strings.Split(strings.TrimSuffix(reply, "\n"), "\n") {
			key, _, _ := strings.Cut(line, "=")
			seen[key]++
		}
		for _, key := range []string{"id", "pid", "precast", "name", "class", "display", "weapons", "abilities", "perks"} {
			if seen[key] != 1 {
				t.Errorf("key %q appears %d times, want once:\n%s", key, seen[key], reply)
			}
		}
		if len(seen) != 9 {
			t.Errorf("reply has %d different keys, want 9:\n%s", len(seen), reply)
		}
	}
	fitLines := func(reply string) string {
		var keep []string
		for _, line := range strings.Split(reply, "\n") {
			if !strings.HasPrefix(line, "name=") {
				keep = append(keep, line)
			}
		}
		return strings.Join(keep, "\n")
	}

	for _, request := range []struct{ name, idField string }{
		{"YA_UpdateShipLoadout", "ShipID"},
		{"YA_RenameShipLoadout", "LoadoutID"},
	} {
		if err := persistMmogPlayerMutation(auditPID, request.name, auditRequest(request.name, request.idField, shipID, "Name", injected)); err != nil {
			t.Fatal(err)
		}
		var stored string
		_ = database.QueryRow(`SELECT loadout_name FROM player_ship_loadouts WHERE user_id=? AND loadout_id=?`, auditPID, loadout.loadoutID()).Scan(&stored)
		if strings.ContainsAny(stored, "\r\n") || stored == "" {
			t.Errorf("%s stored the name %q", request.name, stored)
		}
		got := record()
		oneLinePerKey(got)
		if fitLines(got) != fitLines(want) {
			t.Errorf("%s: a loadout name changed the fit sent to the battle server:\n%s\nwant\n%s", request.name, got, want)
		}
	}

	// A row stored before names were cleaned is cleaned on the way out.
	if _, err := database.Exec(`UPDATE player_ship_loadouts SET loadout_name=? WHERE user_id=? AND loadout_id=?`,
		injected, auditPID, loadout.loadoutID()); err != nil {
		t.Fatal(err)
	}
	got := record()
	oneLinePerKey(got)
	if fitLines(got) != fitLines(want) {
		t.Errorf("a stored name with line breaks changed the fit sent to the battle server:\n%s", got)
	}

	// Length: a name is capped, an appearance that is not the client's format
	// is not stored, and a real one still is.
	var before string
	_ = database.QueryRow(`SELECT display_info FROM player_ship_loadouts WHERE user_id=? AND loadout_id=?`, auditPID, loadout.loadoutID()).Scan(&before)
	for _, bad := range []string{strings.Repeat("9", 5000), "1#2#3#4;5;6;7;8\nweapons=1,2", "<img>"} {
		if err := persistMmogPlayerMutation(auditPID, "YA_UpdateShipLoadout", auditRequest("YA_UpdateShipLoadout",
			"ShipID", shipID, "Name", strings.Repeat("A", 5000), "DisplayInfo", bad)); err != nil {
			t.Fatal(err)
		}
		var name, display string
		_ = database.QueryRow(`SELECT loadout_name, display_info FROM player_ship_loadouts WHERE user_id=? AND loadout_id=?`,
			auditPID, loadout.loadoutID()).Scan(&name, &display)
		if len(name) != maxLoadoutNameRunes {
			t.Errorf("a 5000-character name was stored with %d characters, want %d", len(name), maxLoadoutNameRunes)
		}
		if display != before {
			t.Errorf("an appearance that is not a ship display string was stored (%d bytes)", len(display))
		}
	}
	if got := sanitizedLoadoutName("  Штурмовик №1\t "); got != "Штурмовик №1" {
		t.Errorf("a non-Latin name came out as %q", got)
	}
	if !validDisplayInfo(loadout.displayInfo(), shipDisplayInfoChars) {
		t.Errorf("the starter ship's own appearance %q is refused", loadout.displayInfo())
	}
}

// YA_AddToFleet was stored as sent: any tier in any fleet, the same ship any
// number of times, and an unowned id stopped only by the foreign key.
func TestAddToFleetChecksOwnershipTierAndDuplicates(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	var t5 int32
	for _, hull := range baseShipLoadouts {
		if hull.tier == 5 {
			t5 = hull.loadoutID
			break
		}
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := grantUnlockedShipLoadout(tx, auditPID, t5); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	const recruit, legendary = 1, 3
	for _, fleetType := range []int{recruit, legendary} {
		var n int
		_ = database.QueryRow(`SELECT COUNT(*) FROM player_fleets WHERE user_id=? AND fleet_id=? AND fleet_type=?`, auditPID, fleetType, fleetType).Scan(&n)
		if n != 1 {
			t.Fatalf("the account has %d fleets of type %d, the test needs 1", n, fleetType)
		}
	}
	places := func(fleetID int, loadoutID int32) (n int) {
		_ = database.QueryRow(`SELECT COUNT(*) FROM player_fleet_loadouts WHERE user_id=? AND fleet_id=? AND loadout_id=?`,
			auditPID, fleetID, loadoutID).Scan(&n)
		return
	}
	add := func(fleetID int, field string, id int32) {
		t.Helper()
		if err := persistMmogPlayerMutation(auditPID, "YA_AddToFleet", auditRequest("YA_AddToFleet",
			"FleetType", strconv.Itoa(fleetID), field, strconv.Itoa(int(id)))); err != nil {
			t.Errorf("YA_AddToFleet(%s=%d, fleet %d): %v", field, id, fleetID, err)
		}
	}

	add(recruit, "shipId", t5)
	if n := places(recruit, t5); n != 0 {
		t.Errorf("a tier V ship has %d places in the Recruit fleet, want 0", n)
	}
	add(legendary, "shipId", t5)
	add(legendary, "shipId", t5)
	add(legendary, "LoadoutID", t5)
	if n := places(legendary, t5); n != 1 {
		t.Errorf("a tier V ship added three times has %d places in the Legendary fleet, want 1", n)
	}
	hero := heroShipLoadouts[0].loadoutID // not owned
	add(legendary, "LoadoutID", hero)
	add(recruit, "LoadoutID", hero)
	if n := places(legendary, hero) + places(recruit, hero); n != 0 {
		t.Errorf("an unowned ship has %d fleet places, want 0", n)
	}

	// A second row from before the check is served once.
	if _, err := database.Exec(`INSERT INTO player_fleet_loadouts(user_id,fleet_id,position,loadout_id)
		SELECT user_id, fleet_id, MAX(position)+1, ? FROM player_fleet_loadouts WHERE user_id=? AND fleet_id=?`,
		t5, auditPID, legendary); err != nil {
		t.Fatal(err)
	}
	if n := places(legendary, t5); n != 2 {
		t.Fatalf("test setup: %d rows, want 2", n)
	}
	for _, fleet := range mmogPlayerStateForPID(auditPID).fleets {
		seen := map[int32]int{}
		for i, loadout := range fleet.shipLoadouts {
			seen[loadout.loadoutID()]++
			if loadout.position != int32(i) {
				t.Errorf("fleet type %d: place %d carries position %d", fleet.fleetType, i, loadout.position)
			}
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("fleet type %d lists ship %d %d times", fleet.fleetType, id, n)
			}
		}
		if fleet.fleetType == legendary && seen[t5] != 1 {
			t.Errorf("the Legendary fleet does not list the tier V ship")
		}
	}
}

// A captain wears only what the player owns. Until 2026-11-10 any sold
// outfit or headwear id in "disp" was stored, and the appearance is shown to
// every other player. A slot with an unowned item is put back -- to its
// stored item, or the gender's default after a gender switch -- and the rest
// of the save is kept (ownedCaptainDisplayInfo has the evidence that a stock
// client never sends one).
func TestCaptainSaveKeepsUnownedItemsOut(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	// Sold captain items the new account does not own: Body_Female_02, and
	// the headwear CH_Attachment_Eyepatch02.
	const outfit, eyepatch = 872349704, 872349739
	owned := map[int32]bool{}
	for _, id := range ownedVanityItemIDs(auditPID) {
		owned[id] = true
	}
	if owned[outfit] || owned[eyepatch] {
		t.Fatalf("the test needs two captain items a new account does not own (outfit owned %v, eyepatch owned %v)", owned[outfit], owned[eyepatch])
	}
	captain := func(gender, suit, headwear string) string {
		head := map[string]string{"GENDER_MALE": "872349830", "GENDER_FEMALE": "872349769"}[gender]
		return gender + ";#iiS=" + suit + "#iiH=" + head + "#iiHw=" + headwear +
			"#iiEw=-1#iiMs=855572488#iiMt=855572501#iiMe=855572482#iiMn=855572504#bIam=0"
	}
	save := func(disp string) string {
		t.Helper()
		if err := persistMmogPlayerMutation(auditPID, "YA_SavePlayerDisplayInformation",
			auditRequest("YA_SavePlayerDisplayInformation", "disp", disp)); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := database.QueryRow(`SELECT display_info FROM player_state WHERE user_id=?`, auditPID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	outfitText, eyepatchText := strconv.Itoa(outfit), strconv.Itoa(eyepatch)

	// A gender switch with an unowned outfit: the switch is kept, the outfit
	// is the female default (the stored one is a male suit).
	if got, want := save(captain("GENDER_FEMALE", outfitText, "872349736")), captain("GENDER_FEMALE", "872349703", "872349736"); got != want {
		t.Errorf("gender switch with an unowned outfit:\n got %s\nwant %s", got, want)
	}
	// Same gender: both unowned slots keep what is stored.
	if got, want := save(captain("GENDER_FEMALE", outfitText, eyepatchText)), captain("GENDER_FEMALE", "872349703", "872349736"); got != want {
		t.Errorf("unowned outfit and headwear:\n got %s\nwant %s", got, want)
	}
	// Bought, it is worn; the headwear still is not.
	if _, err := database.Exec(`INSERT INTO player_purchases(user_id,item_id,item_type,price_paid,currency) VALUES(?,?,'vanity',350,'gp')`, auditPID, outfit); err != nil {
		t.Fatal(err)
	}
	if got, want := save(captain("GENDER_FEMALE", outfitText, eyepatchText)), captain("GENDER_FEMALE", outfitText, "872349736"); got != want {
		t.Errorf("bought outfit, unowned headwear:\n got %s\nwant %s", got, want)
	}
	// A captain stored before the check keeps the item it already wears (5 of
	// the 168 live captains on 2026-11-10), also through a later save.
	if _, err := database.Exec(`UPDATE player_state SET display_info=? WHERE user_id=?`, captain("GENDER_FEMALE", "872349703", eyepatchText), auditPID); err != nil {
		t.Fatal(err)
	}
	if got, want := save(captain("GENDER_FEMALE", outfitText, eyepatchText)), captain("GENDER_FEMALE", outfitText, eyepatchText); got != want {
		t.Errorf("an item worn before the check:\n got %s\nwant %s", got, want)
	}
	// warn: stored as sent.
	t.Setenv("DN_LOADOUT_OWNERSHIP", "warn")
	if got, want := save(captain("GENDER_MALE", "872349708", eyepatchText)), captain("GENDER_MALE", "872349708", eyepatchText); got != want {
		t.Errorf("warn mode:\n got %s\nwant %s", got, want)
	}
}

// A balance is added to in SQL ("free_xp = free_xp + ?") in a 64-bit column,
// while the wire and the Go side hold an int32. One value past 2147483647
// failed the Scan of the whole row, and the player was then served a default
// account at every login. The database now stops a balance at int32 (db.go,
// the *_int32_* triggers), for a row changed and for a row created.
func TestBalancePastInt32StopsAtInt32(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	mmogPlayerStateForPID(auditPID)
	const maxInt32 = 2147483647
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE player_state SET display_name='Pilot', soft_currency=2147483000, free_xp=5 WHERE user_id=?`, auditPID)

	// An earning the way battle results and claims add it.
	exec(`UPDATE player_state SET soft_currency = soft_currency + ?, free_xp = free_xp + ? WHERE user_id=?`, 5000, 7, auditPID)
	state, err := loadMmogPlayerState(auditPID)
	if err != nil {
		t.Fatalf("the player state no longer loads: %v", err)
	}
	if state.softCurrency != maxInt32 || state.freeXP != 12 || state.displayName != "Pilot" {
		t.Errorf("after an earning past int32: credits %d (want %d), free XP %d (want 12), name %q (want Pilot)",
			state.softCurrency, maxInt32, state.freeXP, state.displayName)
	}

	// Every summed column, set outright.
	for _, column := range []string{"soft_currency", "premium_currency", "free_xp", "current_xp", "rank_xp"} {
		exec(`UPDATE player_state SET `+column+`=3000000000 WHERE user_id=?`, auditPID)
		var got int64
		if err := database.QueryRow(`SELECT `+column+` FROM player_state WHERE user_id=?`, auditPID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != maxInt32 {
			t.Errorf("%s stored as %d, want %d", column, got, maxInt32)
		}
	}
	if _, err := loadMmogPlayerState(auditPID); err != nil {
		t.Errorf("the player state no longer loads: %v", err)
	}

	// A row created past int32 (an admin grant, a provisioned account).
	const other = "0123456789abcdef0123456789abcdef"
	exec(`INSERT INTO player_state(user_id,soft_currency,premium_currency,free_xp,current_xp,current_rank,rank_xp) VALUES(?,?,?,?,?,?,?)`,
		other, int64(3000000000), 1, 2, 3, 4, 5)
	var credits, gp int64
	if err := database.QueryRow(`SELECT soft_currency, premium_currency FROM player_state WHERE user_id=?`, other).Scan(&credits, &gp); err != nil {
		t.Fatal(err)
	}
	if credits != maxInt32 || gp != 1 {
		t.Errorf("created row: credits %d (want %d), GP %d (want 1)", credits, maxInt32, gp)
	}

	// Ship XP: created, then added to the way a battle result does.
	const ship = 12345
	exec(`INSERT INTO player_ship_xp(user_id,ship_id,xp) VALUES(?,?,?)`, auditPID, ship, int64(3000000000))
	if got := persistedPlayerShipXP(auditPID, ship); got != maxInt32 {
		t.Errorf("ship XP created past int32 reads %d, want %d", got, maxInt32)
	}
	exec(`UPDATE player_ship_xp SET xp=100 WHERE user_id=? AND ship_id=?`, auditPID, ship)
	exec(`INSERT INTO player_ship_xp(user_id,ship_id,xp) VALUES(?,?,?)
		ON CONFLICT(user_id,ship_id) DO UPDATE SET xp=xp+excluded.xp`, auditPID, ship, int64(2147483647))
	if got := persistedPlayerShipXP(auditPID, ship); got != maxInt32 {
		t.Errorf("ship XP added past int32 reads %d, want %d", got, maxInt32)
	}
}
