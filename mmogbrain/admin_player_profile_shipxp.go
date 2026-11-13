package main

// POST /admin/api/players/{pid}/shipxp {ship_id, amount, mode:"add"|"set"}.
//
// Ship XP has two id spaces (ship_xp_keys.go): STORED in player_ship_xp by
// pawn id (category 10), read by the CLIENT by hull loadout id (category 1/3).
// ship_id may be either; a loadout id is resolved to its pawn first, so both
// land in the same row the battle rewards and research use.

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	dreadconfig "github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

func adminAPIPlayerShipXP(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	var req struct {
		ShipID int32  `json:"ship_id"`
		Amount int64  `json:"amount"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Mode != "add" && req.Mode != "set" {
		writeAdminError(w, http.StatusBadRequest, "mode must be add or set")
		return
	}
	if req.Amount > adminMaxAmount || req.Amount < -adminMaxAmount || (req.Mode == "set" && req.Amount < 0) {
		writeAdminError(w, http.StatusBadRequest, "amount out of range")
		return
	}
	if req.Mode == "add" && req.Amount == 0 {
		writeAdminError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	pawn := req.ShipID
	switch category := (req.ShipID >> 24) & 0xff; category {
	case mmogItemCategoryShipLoadoutPrecast, mmogItemCategoryShipLoadoutHero:
		p, found := dreadconfig.ShipIDForPrecastLoadout(req.ShipID)
		if !found {
			writeAdminError(w, http.StatusBadRequest, "no ship pawn is known for that loadout id")
			return
		}
		pawn = p
	case mmogItemCategoryShipPawn:
		// A pawn the game data lists, one the player flies, or one that
		// already holds XP (older rows) -- never an arbitrary number.
		_, known := dreadconfig.ItemByID(pawn)
		var n int
		_ = database.QueryRow(`SELECT (SELECT COUNT(*) FROM player_ship_loadouts WHERE user_id=? AND ship_id=?)
			+ (SELECT COUNT(*) FROM player_ship_xp WHERE user_id=? AND ship_id=?)`, pid, pawn, pid, pawn).Scan(&n)
		if !known && n == 0 {
			writeAdminError(w, http.StatusBadRequest, "unknown ship id")
			return
		}
	default:
		writeAdminError(w, http.StatusBadRequest, "ship_id must be a ship loadout id (category 1 or 3) or a ship pawn id (category 10)")
		return
	}

	tx, err := database.Begin()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()
	var before int64
	_ = tx.QueryRow(`SELECT xp FROM player_ship_xp WHERE user_id=? AND ship_id=?`, pid, pawn).Scan(&before)
	after := req.Amount
	if req.Mode == "add" {
		after = before + req.Amount
	}
	after = max(0, min(math.MaxInt32, after)) // the client reads an int32
	if _, err := tx.Exec(`INSERT INTO player_ship_xp(user_id,ship_id,xp) VALUES(?,?,?)
		ON CONFLICT(user_id,ship_id) DO UPDATE SET xp=excluded.xp, updated_at=datetime('now')`, pid, pawn, after); err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name, _ := authoritativeShipName(pawn)
	adminAudit(database, r, "ship xp", pid, map[string]any{"ship_id": req.ShipID, "pawn": pawn, "name": name, "mode": req.Mode,
		"amount": req.Amount, "xp": map[string]any{"from": before, "to": after}})

	// Online: the one in-session ship-XP path the client has (YA_ConvertShipXP,
	// see buildMmogShipXPSyncPush) SUBTRACTS the ShipXp it is given, so a gain
	// goes out negative and a loss positive. Not verified on a live client.
	pushed := false
	if delta := after - before; delta != 0 {
		squadHubInstance.mu.Lock()
		pushed = squadHubInstance.online[pid] > 0
		squadHubInstance.mu.Unlock()
		if pushed {
			squadHubInstance.push(pid, adminBuildShipXPDeltaPush(pid, pawn, delta))
		}
	}
	writeAdminJSON(w, map[string]any{"ship_id": pawn, "name": name, "before": before, "after": after, "pushed": pushed,
		"note": map[bool]string{true: "pushed to the online client", false: "applied; an offline player sees it at the next login"}[pushed]})
}

// adminBuildShipXPDeltaPush is buildMmogShipXPSyncPush for a signed change:
// delta > 0 is a gain (sent as negative ShipXp), delta < 0 a loss.
func adminBuildShipXPDeltaPush(playerPID string, pawn int32, delta int64) []byte {
	var b []byte
	var stack []int
	b = protocol.AppendStringField(b, "RT", "YA_ConvertShipXP")
	b = protocol.AppendStringField(b, "result", "bought")
	b = protocol.AppendStringField(b, "FreeXp", strconv.Itoa(int(mmogPlayerStateForPID(playerPID).freeXP)))
	b, stack = protocol.AppendArrayStart(b, stack, "ShipXps")
	for _, e := range clientShipXPs([]shipXPEntry{{shipID: pawn, xp: int32(delta)}}) {
		b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		b = protocol.AppendStringField(b, "ShipID", strconv.Itoa(int(e.shipID)))
		b = protocol.AppendStringField(b, "ShipXp", strconv.Itoa(int(-e.xp)))
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	b, _ = protocol.AppendObjectEnd(b, stack)
	return b
}
