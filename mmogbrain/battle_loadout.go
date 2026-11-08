package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
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
	database := currentMmogPlayerStateDB()
	if database == nil {
		return "", false
	}
	var fleetType int32
	if err := database.QueryRow(`SELECT fleet_type FROM matches WHERE battle_match_id=? OR id=? LIMIT 1`,
		matchID, matchID).Scan(&fleetType); err != nil || fleetType == 0 {
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
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\n", id)
	fmt.Fprintf(&b, "pid=%s\n", pid)
	fmt.Fprintf(&b, "precast=%d\n", loadout.precastLoadoutID)
	fmt.Fprintf(&b, "name=%s\n", loadout.loadoutName)
	fmt.Fprintf(&b, "class=%d\n", loadoutEYShipClass(loadout))
	fmt.Fprintf(&b, "display=%s\n", loadout.displayInfo())
	fmt.Fprintf(&b, "weapons=%s\n", ints(loadout.weaponPrimaryItemID(), loadout.weaponSecondaryItemID()))
	fmt.Fprintf(&b, "abilities=%s\n", ints(loadout.abilityItemID(0), loadout.abilityItemID(1), loadout.abilityItemID(2), loadout.abilityItemID(3)))
	fmt.Fprintf(&b, "perks=%s\n", ints(loadout.perkItemID(0), loadout.perkItemID(1), loadout.perkItemID(2), loadout.perkItemID(3)))
	return b.String()
}

// battlePlayerHandler answers GET /battle/player?pid= with "name=<display
// name>": the name battle-server-mod gives a joining player. Added 2026-09-28:
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
	name := strings.TrimSpace(state.displayName)
	if name == "" || name == "Local" {
		http.Error(w, "no name for this player", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "name=%s\n", name)
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
