package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// ownTestShip makes pid own one base ship of a tier, and returns its id.
func ownTestShip(t *testing.T, pid string, tier int32) int32 {
	t.Helper()
	mmogPlayerStateForPID(pid) // seeds the account
	var id int32
	for _, hull := range baseShipLoadouts {
		if hull.tier == tier {
			id = hull.loadoutID
			break
		}
	}
	if id == 0 {
		t.Fatalf("no base ship of tier %d", tier)
	}
	tx, err := currentMmogPlayerStateDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := grantUnlockedShipLoadout(tx, pid, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

// fleetTestShip puts an owned ship in pid's fleet of a type, the way the
// client does (YA_AddToFleet).
func fleetTestShip(t *testing.T, pid string, fleetType int32, shipID int32) {
	t.Helper()
	if err := persistMmogPlayerMutation(pid, "YA_AddToFleet", auditRequest("YA_AddToFleet",
		"FleetType", strconv.Itoa(int(fleetType)), "shipId", strconv.Itoa(int(shipID)))); err != nil {
		t.Fatal(err)
	}
	if fleet, ok := clientFleetOfType(pid, fleetType); !ok || len(fleet.shipLoadouts) == 0 {
		t.Fatalf("test setup: fleet type %d of %s holds no ship", fleetType, pid[:4])
	}
}

// The leader picks the squad's fleet type, and until 2026-11-11 every member
// was queued with it unchecked. A member whose client was never sent a fleet
// of that type does nothing when the squad starts searching (State 1 lookup
// 0x142A0ECA0 finds none) and keeps the fleet it had; live, such a member
// played a Veteran match on Recruit ships and the rewards went to an empty
// fleet. The client has the refusal for it: result "invalid_fleet_type" ->
// "The selected fleet is not available for all squad members".
func TestSquadQueueNeedsEveryMembersFleet(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	h, a, b := formTestSquad(t)
	legendary := protocol.AppendStringField(protocol.AppendInt32Field(nil, "FleetType", 3), "GameType", "TDM")
	queued := func() (n int) {
		_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries`).Scan(&n)
		return
	}
	refused := func(why string) {
		t.Helper()
		if r := h.enterSquadMatchmaking(a, legendary); !squadField(r, "result", "invalid_fleet_type") {
			t.Fatalf("%s: reply %q, want result=invalid_fleet_type", why, r)
		}
		if n := queued(); n != 0 {
			t.Fatalf("%s: %d queue entries after a refused queue, want 0", why, n)
		}
		if got := len(h.drainPushes(a)) + len(h.drainPushes(b)); got != 0 {
			t.Fatalf("%s: %d squad pushes after a refused queue, want 0 (nobody is pulled in)", why, got)
		}
	}

	// The leader fields a Legendary fleet, the member's is locked.
	fleetTestShip(t, a, 3, ownTestShip(t, a, 5))
	mmogPlayerStateForPID(b)
	if _, ok := clientFleetOfType(b, 3); ok {
		t.Fatal("test setup: an account without a tier IV/V ship is sent a Legendary fleet")
	}
	refused("member's fleet locked")

	// Unlocked (the member owns a tier V ship) but empty: nothing to pick.
	ship := ownTestShip(t, b, 5)
	if fleet, ok := clientFleetOfType(b, 3); !ok || len(fleet.shipLoadouts) != 0 {
		t.Fatalf("test setup: want an unlocked, empty Legendary fleet (sent %v, %d ships)", ok, len(fleet.shipLoadouts))
	}
	refused("member's fleet empty")

	// With a ship in it the squad queues, every member with that fleet type.
	fleetTestShip(t, b, 3, ship)
	if r := h.enterSquadMatchmaking(a, legendary); !squadField(r, "result", "ok") {
		t.Fatalf("both members field the fleet: reply %q, want result=ok", r)
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries WHERE status='waiting' AND fleet_type=3`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d Legendary queue entries, want 2", n)
	}
}

// The leader's own fleet is checked like any member's, and a Recruit queue
// needs nothing: every account is sent a Recruit fleet with its starter ships.
func TestSquadQueueChecksTheLeaderToo(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	h, a, b := formTestSquad(t)
	fleetTestShip(t, b, 3, ownTestShip(t, b, 5))
	mmogPlayerStateForPID(a)
	req := func(fleetType int32) []byte {
		return protocol.AppendStringField(protocol.AppendInt32Field(nil, "FleetType", fleetType), "GameType", "TDM")
	}
	if r := h.enterSquadMatchmaking(a, req(3)); !squadField(r, "result", "invalid_fleet_type") {
		t.Fatalf("leader without the fleet: reply %q, want result=invalid_fleet_type", r)
	}
	if r := h.enterSquadMatchmaking(a, req(1)); !squadField(r, "result", "ok") {
		t.Fatalf("Recruit queue: reply %q, want result=ok", r)
	}
}

// A solo queue names the fleet (FleetID). The stock client offers only a
// fleet it was sent that holds a ship, so these refusals are for requests it
// would not send; both tokens are in its own table (0x2A497C0), "empty_fleet"
// with the text "Unable to enter matchmaking with an empty fleet.".
func TestSoloQueueNeedsAFleetWithAShip(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = auditPID
	mmogPlayerStateForPID(pid)
	queue := func(fleetID int32) []byte {
		return buildMmogEnterMatchmakingPayload("YA_EnterMatchmaking", pid, auditRequest("YA_EnterMatchmaking",
			"FleetID", fleetFID(pid, fleetID), "GameType", "TDM"))
	}
	waiting := func() (n int, fleetType int32) {
		_ = database.QueryRow(`SELECT COUNT(*), COALESCE(MAX(fleet_type),0) FROM queue_entries WHERE user_id=? AND status='waiting'`, pid).Scan(&n, &fleetType)
		return
	}
	// Legendary is fleet 3 of every account; this one owns no tier IV/V ship.
	if r := queue(3); !squadField(r, "result", "invalid_fleet") {
		t.Fatalf("locked fleet: reply %q, want result=invalid_fleet", r)
	}
	ship := ownTestShip(t, pid, 5)
	if r := queue(3); !squadField(r, "result", "empty_fleet") {
		t.Fatalf("empty fleet: reply %q, want result=empty_fleet", r)
	}
	if n, _ := waiting(); n != 0 {
		t.Fatalf("%d queue entries after two refused queues, want 0", n)
	}
	fleetTestShip(t, pid, 3, ship)
	if r := queue(3); squadField(r, "result", "empty_fleet") || squadField(r, "result", "invalid_fleet") {
		t.Fatalf("fleet with a ship: refused, reply %q", r)
	}
	if n, fleetType := waiting(); n != 1 || fleetType != 3 {
		t.Fatalf("%d queue entries with fleet type %d, want 1 with 3", n, fleetType)
	}
	// Recruit: the starter fleet, no purchase needed.
	if r := queue(1); squadField(r, "result", "empty_fleet") || squadField(r, "result", "invalid_fleet") {
		t.Fatalf("Recruit fleet: refused, reply %q", r)
	}
}

// /battle/player names the fleet the player fields in the match, under the
// id their client was sent for it (YA_PlayerFleets "FID"): the host passes it
// on as ClientSetActiveFleetId, and the client's fleet manager keys fleets by
// the FName of exactly that string (0x142A1D450).
func TestBattlePlayerNamesTheMatchFleet(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = auditPID
	mmogPlayerStateForPID(pid)
	if _, err := database.Exec(`UPDATE player_state SET display_name='Tester' WHERE user_id=?`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,battle_match_id,fleet_type,status) VALUES
		('m1','TDM','x','bm-recruit',1,'active'),('m2','TDM','x','bm-legend',3,'active')`); err != nil {
		t.Fatal(err)
	}
	get := func(query string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/battle/player?pid="+pid+query, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		battlePlayerHandler(rec, req)
		return rec.Code, rec.Body.String()
	}
	fleetLine := func(body string) string {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "fleet=") {
				return line
			}
		}
		return ""
	}
	sent := func(fid string) bool {
		return squadField(buildMmogPlayerFleetsPayload(pid), "FID", fid)
	}

	// Recruit: the fleet id is the player id, as the client is sent it.
	code, body := get("&match=bm-recruit")
	if code != http.StatusOK || fleetLine(body) != "fleet="+pid || !strings.HasPrefix(body, "name=Tester\n") {
		t.Fatalf("Recruit match: %d %q, want name=Tester and fleet=%s", code, body, pid)
	}
	if !sent(pid) {
		t.Fatal("YA_PlayerFleets does not carry the Recruit fleet under the player id")
	}
	// No fleet of the match's type on the client: no line (the host then
	// sends nothing), but still the name.
	if code, body := get("&match=bm-legend"); code != http.StatusOK || fleetLine(body) != "" {
		t.Fatalf("Legendary match, fleet locked: %d %q, want no fleet line", code, body)
	}
	fleetTestShip(t, pid, 3, ownTestShip(t, pid, 5))
	want := fleetFID(pid, 3)
	if code, body := get("&match=bm-legend"); code != http.StatusOK || fleetLine(body) != "fleet="+want {
		t.Fatalf("Legendary match: %d %q, want fleet=%s", code, body, want)
	}
	if len(want) != 32 || want == pid || !sent(want) {
		t.Fatalf("the Legendary fleet id %q is not the 32-character FID YA_PlayerFleets sends", want)
	}
	// Without a match id the player's running match decides; none here.
	if code, body := get(""); code != http.StatusOK || fleetLine(body) != "" {
		t.Fatalf("no match: %d %q, want no fleet line", code, body)
	}
	if _, err := database.Exec(`INSERT INTO match_slots(match_id,user_id,team) VALUES('m2',?,1)`, pid); err != nil {
		t.Fatal(err)
	}
	if _, body := get(""); fleetLine(body) != "fleet="+want {
		t.Fatalf("running Legendary match, no match id: %q, want fleet=%s", body, want)
	}
	// An unknown match names no fleet rather than a guessed one.
	if _, body := get("&match=nope"); fleetLine(body) != "" {
		t.Fatalf("unknown match: %q, want no fleet line", body)
	}
}
