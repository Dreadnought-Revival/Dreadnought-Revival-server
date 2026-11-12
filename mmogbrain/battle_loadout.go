package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// battleLoadoutHandler serves one player's loadout to the battle-server mod.
//
// Why it exists (verified 2026-09-25 from the exe): the only loadout message a
// client sends a battle server is ServerPlayerClickedShipLoadout(FName id); the
// server's mmog AutoLogin (0x2AABCB0) is a stub in this exe, and its fleet
// manager (UpdatePlayerFleetData, 0x35FDF0) only ever reads the host's OWN
// account. So a host that should spawn a player's custom fit has no way to
// learn it -- the original servers were a separate server build. The mod
// learns whose connection it is from the ?DNPID= option mmogbrain adds to the
// YA_Connect travel address (read back from UNetConnection+0x198 on the host)
// and asks here for the loadout the client picked.
//
// The answer is the same record the client builds its own loadout from
// (YA_PlayerFleets -> 0x34E550 -> FYShipImportLoadoutInfo): precast id, name,
// ship class, display info, 2 weapon slots, 4 ability slots, 4 perk slots --
// positional, zeros included, because the game's record is positional.
//
// GET /battle/loadout?pid=<pid>&id=<loadout id as the client sent it>
// Loopback only: the battle servers run on this host, and nothing here needs a
// secret passed through Wine. Plain "key=value" lines so the mod parses it
// without a JSON library.
func battleLoadoutHandler(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessLoopback(w, r) {
		return
	}
	pid := normalizedPlayerStatePID(r.URL.Query().Get("pid"))
	id := r.URL.Query().Get("id")
	if pid != "" && id == "" && r.URL.Query().Get("flagship") != "" {
		var ok bool
		if id, ok = battleFlagshipID(pid, r.URL.Query().Get("match")); !ok {
			http.Error(w, "no flagship for this player and match", http.StatusNotFound)
			return
		}
	}
	if pid == "" || id == "" {
		http.Error(w, "pid and id (or flagship=1) are required", http.StatusBadRequest)
		return
	}
	loadout, ok := battleLoadoutFor(pid, id)
	if !ok {
		http.Error(w, "no such loadout for this player", http.StatusNotFound)
		return
	}
	if tier, fleetType, refused := battleLoadoutOutsideFleet(pid, r.URL.Query().Get("match"), loadout); refused {
		logrus.WithFields(logrus.Fields{"player": pid, "loadout": id, "tier": tier, "fleet_type": fleetType}).
			Warn("battle loadout: refused -- the ship's tier is not allowed in this match's fleet")
		http.Error(w, "the ship's tier is not allowed in this match's fleet", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(formatBattleLoadout(id, pid, loadout)))
}

// battleLoadoutFor finds the player's loadout whose client-facing id (the
// "ID" field of the YA_PlayerFleets entry) is id -- the list the client itself
// received, so both sides resolve the same row.
func battleLoadoutFor(pid, id string) (mmogShipLoadoutSeed, bool) {
	for _, loadout := range withLoadoutVariants(pid, ownedShipLoadoutsForPlayerData(mmogPlayerStateForPID(pid), pid)) {
		if loadout.entryID() == id {
			return loadout, true
		}
	}
	return mmogShipLoadoutSeed{}, false
}

// battleLoadoutOutsideFleet reports a ship whose tier the match's fleet does
// not hold (FleetEligibility.AllowedTiers: Recruit I-II, Veteran II-III,
// Legendary IV-V). A match is made per fleet type and the client offers only
// that fleet's ships, but the host is told just an id
// (ServerPlayerClickedShipLoadout) and until 2026-11-10 any ship the player
// owned was handed out: a changed client could bring a tier V ship into a
// Recruit match. Evidence that the stock client never does: every ship
// picked in the 7 days of logs before the change (1,285 line ships and 194
// heroes over 8 game modes, checked against battle_results.fleet_type) was
// of a tier its match's fleet holds.
//
// The answer is 403, not 404: the mod (from the 2026-11-10 build) spawns
// nothing for a 403, where a 404 falls back to that ship's default precast.
// An older mod treats both the same, so the ship flies with the default fit
// -- what it did before -- and nothing breaks in either order of deploy.
//
// Unknown is allowed: no match row (a host started by hand), a fleet type
// with no eligibility row, or a ship whose tier cannot be derived.
func battleLoadoutOutsideFleet(pid, matchID string, loadout mmogShipLoadoutSeed) (tier, fleetType int32, refused bool) {
	fleetType, ok := battleMatchFleetType(pid, matchID)
	if !ok {
		return 0, 0, false
	}
	eligibility, ok := dreadFleetEligibilityByType(fleetType)
	if !ok || len(eligibility.AllowedTiers) == 0 {
		return 0, fleetType, false
	}
	tier, derived := shipTierForIDChecked(loadout.loadoutID())
	if !derived {
		return 0, fleetType, false
	}
	return tier, fleetType, !slices.Contains(eligibility.AllowedTiers, tier)
}

// battleMatchFleetType is the fleet type a match is played with
// (matches.fleet_type): the match the host names (its -MatchID, which is the
// battle_match_id, or the row id for a match made before that column), else
// the active match the player holds a slot in -- a mod older than 2026-11-10
// sends no match with a picked ship.
func battleMatchFleetType(pid, matchID string) (int32, bool) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return 0, false
	}
	var fleetType int32
	var err error
	if matchID != "" {
		// Not for "": 114 older matches have an empty battle_match_id, and
		// one of those would answer for every host that sent no id.
		err = database.QueryRow(`SELECT fleet_type FROM matches WHERE battle_match_id=? OR id=? LIMIT 1`,
			matchID, matchID).Scan(&fleetType)
	} else {
		err = database.QueryRow(`SELECT m.fleet_type FROM match_slots s JOIN matches m ON m.id=s.match_id
			WHERE s.user_id=? AND m.status='active' ORDER BY s.joined_at DESC LIMIT 1`, pid).Scan(&fleetType)
	}
	if err != nil || fleetType == 0 {
		return 0, false
	}
	return fleetType, true
}

// battleFlagshipID is the client-facing id of the player's flagship in the
// fleet the match is played with -- what the host spawns when the player
// picked no ship before the drop (GET /battle/loadout?pid=&flagship=1&match=
// <the host's -MatchID>). The game's own fallback does exactly that: its
// spawn-loadout lookup 0x370970 (callers SpawnDefaultPawn's 0x3827D0 and
// 0x382870), finding no active loadout, takes the flagship id from the
// player's fleet object (*(PC+0x958): [+0x40]+0xAB0 -> +0x2B8, else +0x118),
// looks it up in that fleet's ship list (0x346240) and activates it
// (UYLoadoutManager::ActivateLoadout 0x336C90). A battle server never logs
// the player in, so that list is empty there and the drop failed with
// "SpawnDefaultPawn: Active Loadout not found" (operator 2026-11-08: a
// player got a black screen; "normally the flagship should be auto picked").
// The match's fleet is matches.fleet_type (by battle_match_id); the
// flagship's own loadout slot picks LOADOUT A or B.
func battleFlagshipID(pid, matchID string) (string, bool) {
	if matchID == "" {
		return "", false
	}
	fleetType, ok := battleMatchFleetType(pid, matchID)
	if !ok {
		return "", false
	}
	var flagship mmogShipLoadoutSeed
	var slot int32
	found := false
	for _, fleet := range mmogPlayerStateForPID(pid).fleets {
		if fleet.fleetType == fleetType && len(fleet.shipLoadouts) > 0 {
			idx := fleet.flagshipIndex()
			if idx < 0 || int(idx) >= len(fleet.shipLoadouts) {
				idx = 0
			}
			flagship, slot, found = fleet.shipLoadouts[idx], fleet.flagshipShipLoadoutSlot(), true
			break
		}
	}
	if !found {
		return "", false
	}
	var fallback string
	for _, loadout := range withLoadoutVariants(pid, ownedShipLoadoutsForPlayerData(mmogPlayerStateForPID(pid), pid)) {
		if loadout.loadoutID() != flagship.loadoutID() {
			continue
		}
		if loadout.variant == slot {
			return loadout.entryID(), true
		}
		if loadout.variant == 0 {
			fallback = loadout.entryID()
		}
	}
	return fallback, fallback != ""
}

func formatBattleLoadout(id, pid string, loadout mmogShipLoadoutSeed) string {
	ints := func(vs ...int32) string {
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, ",")
	}
	// One value per line, and the mod takes the FIRST line of each key
	// (FieldValue, dn_host_loadout.cpp), so no text value may hold a line
	// break: a loadout name that did wrote its own class/display/weapons/
	// abilities/perks lines ahead of the real ones. Names and display strings
	// are cleaned when saved (sanitizedLoadoutName, validDisplayInfo); this is
	// for rows stored before that, and for the id, which is the request's.
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\n", battleLineValue(id))
	fmt.Fprintf(&b, "pid=%s\n", pid)
	fmt.Fprintf(&b, "precast=%d\n", loadout.precastLoadoutID)
	fmt.Fprintf(&b, "name=%s\n", sanitizedLoadoutName(loadout.loadoutName))
	fmt.Fprintf(&b, "class=%d\n", loadoutEYShipClass(loadout))
	fmt.Fprintf(&b, "display=%s\n", battleLineValue(loadout.displayInfo()))
	fmt.Fprintf(&b, "weapons=%s\n", ints(loadout.weaponPrimaryItemID(), loadout.weaponSecondaryItemID()))
	fmt.Fprintf(&b, "abilities=%s\n", ints(loadout.abilityItemID(0), loadout.abilityItemID(1), loadout.abilityItemID(2), loadout.abilityItemID(3)))
	fmt.Fprintf(&b, "perks=%s\n", ints(loadout.perkItemID(0), loadout.perkItemID(1), loadout.perkItemID(2), loadout.perkItemID(3)))
	return b.String()
}

// battleLineValue is a text value as one line of a "key=value" reply to the
// battle-server mod: control characters (line breaks above all) removed.
func battleLineValue(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, value)
}

// battlePlayerHandler answers GET /battle/player?pid=[&match=] with what
// battle-server-mod needs to know about a joining player, one "key=value" a
// line, each only when there is one: name, captain, fleet. 404 when there is
// neither a name nor a fleet.
//
// name=<display name> is the name the host gives the player. Added 2026-09-28:
// the host can only name players from their join URL, whose Name= the client
// empties (UYGameEngine::Browse) and whose PlayerName= (dn-launcher's start
// token) survives only the first match after the game starts -- a later
// 3-player proving ground joined without it and everyone was a number again.
// Loopback only, like /battle/loadout.
func battlePlayerHandler(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessLoopback(w, r) {
		return
	}
	pid := protocol.NormalizePlayerPID(r.URL.Query().Get("pid"))
	if pid == "" {
		http.Error(w, "pid is required", http.StatusBadRequest)
		return
	}
	state := mmogPlayerStateForPID(pid)
	name := strings.TrimSpace(battleLineValue(state.displayName))
	if name == "Local" {
		name = ""
	}
	// fleet= is the id of the fleet the player fields in this match (match =
	// the host's -MatchID; without it, the player's running match), in the
	// form their client keys it by. The host sends it on as
	// ClientSetActiveFleetId, which the original battle server did itself
	// (match_fleet.go) -- ours has no fleet data, so each client fielded
	// whatever fleet was selected in its hangar. Added 2026-11-11.
	fleet, haveFleet := battleMatchFleetFID(pid, strings.TrimSpace(r.URL.Query().Get("match")))
	if name == "" && !haveFleet {
		http.Error(w, "no name for this player", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if name != "" {
		_, _ = fmt.Fprintf(w, "name=%s\n", name)
	}
	if haveFleet {
		_, _ = fmt.Fprintf(w, "fleet=%s\n", fleet)
	}
	// captain= is the saved captain appearance (YA_SavePlayerDisplayInformation
	// "disp", the same string YA_GetPlayersInformation returns as DisplayInfo).
	// The host copies it into AYPlayerReplicationInfo::m_captainDisplayInformation
	// (replicated FString +0x758, RepNotify), which a login-less battle server
	// otherwise leaves empty, so every other player is drawn with the default
	// captain. From PR #80.
	if captain := strings.TrimSpace(state.displayInfo); captain != "" && !strings.ContainsAny(captain, "\r\n") {
		_, _ = fmt.Fprintf(w, "captain=%s\n", captain)
	}
}
