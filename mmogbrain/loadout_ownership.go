package main

import (
	"os"
	"strconv"
	"strings"

	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
	"github.com/sirupsen/logrus"
)

// Loadout edits were stored without checking ownership (audit 2026-09-26):
// persistUpdateShipLoadout wrote whatever weapon/module/perk ids and appearance
// string the client sent for its own loadout. The stock client only offers what
// the player owns, but a modified client or a hand-built request could fit
// anything -- and the battle server spawns the stored fit (/battle/loadout), so
// that is unowned weapons in a match and unpaid cosmetics.
//
// DN_LOADOUT_OWNERSHIP:
//
//	enforce (default)  skip the unowned slot / appearance change, and log it
//	warn               store as sent, log every item the player does not own
//	off                no check
//
// It ran as warn first (2026-09-26 to 2026-11-10), because a FALSE positive
// in enforce mode silently loses a real player's change -- ownership has
// corners (per-ship ids, hero hulls, fitted defaults). Enforce is the default
// since 2026-11-10 on this evidence:
//   - seven days of live logs: 10,058 YA_UpdateShipLoadout, no "loadout:
//     unowned" line;
//   - the check run over every STORED fit of a copy of the live database:
//     3,092 fits (A and B), 0 unowned appearances, 11 unowned items -- all on
//     one account's three ships, last saved 2026-09-24, before the check
//     existed.
//
// A refused slot keeps what is stored; nothing already stored is removed.
// DN_LOADOUT_OWNERSHIP=warn puts the old behaviour back without a build.
func loadoutOwnershipMode() string {
	switch os.Getenv("DN_LOADOUT_OWNERSHIP") {
	case "warn":
		return "warn"
	case "off":
		return "off"
	default:
		return "enforce"
	}
}

// ownedItemSet is what the player owns for fitting purposes: purchases
// (cosmetics included) plus every owned ship's fitted defaults, the same set
// the client is told (clientOwnedItemIDs), plus every owned ship's own look.
//
// Weapons, abilities and perks are keyed by their SHARED form. Real stored fits
// use both forms -- per-ship ids (middle byte = EYShipClass, see inflatedItemID)
// and shared ones (middle byte 0xFF) -- even within one account, while the
// owned list holds per-ship ids for fitted defaults and shared ids for the
// starter kit. Comparing shared forms means owning a weapon on one hull
// permits it on another: slightly lenient, but it still catches the abuse
// that matters (a weapon never obtained at all) without flagging honest fits.
func ownedItemSet(playerPID string) map[int32]bool {
	set := map[int32]bool{}
	add := func(id int32) {
		set[id] = true
		set[sharedGearID(id)] = true
	}
	for _, id := range clientOwnedItemIDs(playerPID) {
		add(id)
	}
	for _, item := range starterOwnedInventorySeeds() {
		add(item.itemID)
	}
	// An owned hero's own fit, which clientOwnedItemIDs leaves out while its
	// base hull is not owned (see ownedBaseHulls there). It is still fittable
	// on the hero itself.
	for _, loadout := range ownedShipLoadoutsForPlayerData(mmogPlayerStateForPID(playerPID), playerPID) {
		if h, ok := heroByID(loadout.precastLoadoutID); ok {
			for _, id := range append(append([]int32{h.primary, h.secondary}, h.abilities[:]...), h.perks[:]...) {
				if id > 0 {
					add(id)
				}
			}
		}
	}
	// Cosmetics: bought, free, and every owned ship's own look as its
	// blueprint names it -- from tier III up a base ship's parts are its own
	// (Onager wears the Kore parts), not the hull line's generic default
	// (2026-10-03). See vanity_ownership.go.
	for _, id := range ownedVanityItemIDs(playerPID) {
		add(id)
	}
	return set
}

// sharedGearID maps a per-ship id to its shared form (middle byte 0xFF):
// weapons, abilities and perks (4-6), and SHIP cosmetics (20-24) -- the client
// stores a ship's appearance with per-ship ids too (e.g. 336461851 =
// VAN_H_AssaultM_Hull_Default_DA 352256027 with class byte 0x0E, from a real
// saved fit). Captain items and everything else are returned unchanged.
func sharedGearID(id int32) int32 {
	switch (id >> 24) & 0xff {
	case 4, 5, 6, 20, 21, 22, 23, 24:
		return id | 0x00FF0000
	}
	return id
}

// unownedAppearanceItems lists the cosmetic ids in a ship's display info
// ("m#m#m#m;emblem;paint;pattern;decal") that the player neither owns nor has
// by default. -1 and 0 are empty slots.
func unownedAppearanceItems(owned map[int32]bool, displayInfo string) []int32 {
	defaults := dreadconfig.AllDefaultShipVanityItemIDs()
	var out []int32
	for _, group := range strings.Split(displayInfo, ";") {
		for _, field := range strings.Split(group, "#") {
			v, err := strconv.ParseInt(strings.TrimSpace(field), 10, 32)
			if err != nil || v <= 0 {
				continue
			}
			id := int32(v)
			if !owned[id] && !owned[sharedGearID(id)] && !defaults[id] && !defaults[sharedGearID(id)] {
				out = append(out, id)
			}
		}
	}
	return out
}

// allowLoadoutItem reports whether a slot may be set to itemID, logging when
// the player does not own it.
func allowLoadoutItem(mode string, owned map[int32]bool, playerPID string, loadoutID int32, slot string, itemID int32) bool {
	if mode == "off" || itemID <= 0 || owned[itemID] || owned[sharedGearID(itemID)] {
		return true
	}
	logrus.WithFields(logrus.Fields{"player": playerPID, "loadout": loadoutID, "slot": slot,
		"item_id": itemID, "mode": mode}).Warn("loadout: unowned item fitted")
	return mode != "enforce"
}

// allowAppearance is the same check for the ship's display info.
func allowAppearance(mode string, owned map[int32]bool, playerPID string, loadoutID int32, displayInfo string) bool {
	if mode == "off" {
		return true
	}
	// A ship's own appearance comes with the ship, hero or not (its own hull
	// is no longer an owned item; see ownedVanityItemIDs).
	a, ok := dreadconfig.HeroShipAppearance(loadoutID)
	if !ok {
		a, ok = dreadconfig.ShipBlueprintAppearance(loadoutID)
	}
	if ok {
		withHero := make(map[int32]bool, len(owned)+8)
		for id, v := range owned {
			withHero[id] = v
		}
		for _, id := range a.Items() {
			withHero[id] = true
		}
		owned = withHero
	}
	unowned := unownedAppearanceItems(owned, displayInfo)
	if len(unowned) == 0 {
		return true
	}
	logrus.WithFields(logrus.Fields{"player": playerPID, "loadout": loadoutID,
		"unowned": unowned, "mode": mode}).Warn("loadout: unowned cosmetic in ship appearance")
	return mode != "enforce"
}

// unownedCaptainItems lists the item ids in a captain appearance
// ("GENDER_x;#iiS=id#iiH=id#...#bIam=0") that the player neither owns nor is
// given by repairedCaptainDisplayInfo. -1 and 0 are empty slots; a field
// whose value is not an item id (bIam) is not a cosmetic.
func unownedCaptainItems(owned map[int32]bool, displayInfo string) []int32 {
	_, rest, _ := strings.Cut(displayInfo, ";")
	var out []int32
	for _, field := range strings.Split(rest, "#") {
		key, value, ok := strings.Cut(field, "=")
		if !ok || !strings.HasPrefix(key, "ii") {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
		if err != nil || v <= 0 {
			continue
		}
		if id := int32(v); !owned[id] && !captainDefaultItems()[id] {
			out = append(out, id)
		}
	}
	return out
}

// ownedCaptainDisplayInfo is the captain appearance a save may store: the one
// sent, with every slot holding an item the player does not own put back
// (DN_LOADOUT_OWNERSHIP, as for a ship: warn stores it as sent, off does not
// look). A captain is shown to everyone -- YA_GetPlayersInformation, and the
// battle server replicates it -- and until 2026-11-10 any sold outfit or
// headwear id in the string was stored.
//
// "Put back" is the slot's stored item when the gender is the same, so a
// captain saved before the check keeps what it wears, and otherwise the slot
// default of that gender (captainSlotDefaults; -1 for a slot without one) --
// never the refusal of the whole save, which would lose the other slots.
//
// Evidence that a stock client only saves what it owns: the 45 captain saves
// of 8 players in the request log of 2026-11-10 (two of them switched
// gender, 42 distinct slot values) held no unowned item. Of the 168 stored
// captains 5 do, all on accounts last changed on or before 2026-10-27 --
// before the captain screen fix of 2026-11-02, and one before cosmetics were
// tracked at all; none of the 107 accounts active since 2026-11-03. A small
// sample: if "captain: unowned item" appears for stock clients, read the
// line before blaming the player.
func ownedCaptainDisplayInfo(playerPID, stored, displayInfo string) string {
	mode := loadoutOwnershipMode()
	if mode == "off" {
		return displayInfo
	}
	owned := map[int32]bool{}
	for _, id := range ownedVanityItemIDs(playerPID) {
		owned[id] = true
	}
	unowned := unownedCaptainItems(owned, displayInfo)
	if len(unowned) == 0 {
		return displayInfo
	}
	logrus.WithFields(logrus.Fields{"player": playerPID, "unowned": unowned, "mode": mode}).
		Warn("captain: unowned item in the appearance")
	if mode != "enforce" {
		return displayInfo
	}
	refused := map[string]bool{}
	for _, id := range unowned {
		refused[strconv.Itoa(int(id))] = true
	}
	gender, rest, _ := strings.Cut(displayInfo, ";")
	kept := map[string]string{}
	if storedGender, storedRest, ok := strings.Cut(stored, ";"); ok && storedGender == gender {
		for _, field := range strings.Split(storedRest, "#") {
			if key, value, ok := strings.Cut(field, "="); ok {
				kept[key] = value
			}
		}
	}
	fields := strings.Split(rest, "#")
	for i, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || !strings.HasPrefix(key, "ii") || !refused[strings.TrimSpace(value)] {
			continue
		}
		back, ok := kept[key]
		if !ok {
			if back, ok = captainSlotDefaults[gender][key]; !ok {
				back = "-1"
			}
		}
		fields[i] = key + "=" + back
	}
	return gender + ";" + strings.Join(fields, "#")
}

// captainDefaultItems is every item the server itself puts on a captain: the
// slot defaults of both genders (captainSlotDefaults).
func captainDefaultItems() map[int32]bool {
	out := map[int32]bool{}
	for _, slots := range captainSlotDefaults {
		for _, value := range slots {
			if v, err := strconv.ParseInt(value, 10, 32); err == nil {
				out[int32(v)] = true
			}
		}
	}
	return out
}
