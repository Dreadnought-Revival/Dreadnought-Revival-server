package matchmaker

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

// pvpOnlyMatchmaker is pvpMatchmaker with a control plane that keeps every
// spawn request's body.
func pvpOnlyMatchmaker(t *testing.T, database *sql.DB) (*Matchmaker, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var spawns []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			spawns = append(spawns, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ip":"10.0.0.73","port":7777,"instance_id":"inst-1"}`))
	}))
	t.Cleanup(srv.Close)
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	m := &Matchmaker{DB: database, Log: log, GameMgrURL: srv.URL, InternalKey: "k", PlayersPerMatch: 10}
	return m, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), spawns...)
	}
}

func queuePool(t *testing.T, database *sql.DB, id, user, mode, party string, pvpOnly bool, queuedAt string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO queue_entries(id,user_id,game_mode,tier_min,status,queued_at,party_id,pvp_only) VALUES(?,?,?,1,'waiting',?,?,?)`,
		id, user, mode, queuedAt, party, pvpOnly); err != nil {
		t.Fatalf("queue %s: %v", user, err)
	}
}

func TestPvPOnlyEligibleModes(t *testing.T) {
	for _, mode := range []string{"TDM", "TurboTDM", "TE", "TER", "PodTDM", "Onslaught"} {
		if !PvPOnlyEligible(mode) {
			t.Errorf("%s: PvP only should apply", mode)
		}
	}
	for _, mode := range []string{"BC", "TM", PvEGameMode, HavocGameMode} {
		if PvPOnlyEligible(mode) {
			t.Errorf("%s: co-op mode, PvP only must not apply", mode)
		}
	}
}

func TestPvPOnlyMatchSizeNeverOne(t *testing.T) {
	for in, want := range map[int]int{0: 0, 1: 2, 2: 2, 5: 5} {
		if got := pvpOnlyMatchSize(in); got != want {
			t.Errorf("pvpOnlyMatchSize(%d) = %d, want %d", in, got, want)
		}
	}
}

// Two PvP-only players and one /pvp player in the same TDM queue: the two
// PvP-only players get a match together with no bots (team_ai "00"), one a
// side; the /pvp player is not in it, and their match gets no team_ai.
func TestPvPOnlyPoolIsSeparateAndHasNoBots(t *testing.T) {
	database := pvpTestDB(t)
	m, spawns := pvpOnlyMatchmaker(t, database)
	queuePool(t, database, "q1", "alice", "TDM", "", true, "2026-11-25T10:00:00Z")
	queuePool(t, database, "q2", "bob", "TDM", "", false, "2026-11-25T10:00:01Z")
	queuePool(t, database, "q3", "carol", "TDM", "", true, "2026-11-25T10:00:02Z")

	if err := m.formMatchOfSize("TDM", 1, 1, 2, true); err != nil {
		t.Fatal(err)
	}
	got := spawns()
	if len(got) != 1 || got[0]["team_ai"] != pvpOnlyTeamAI {
		t.Fatalf("spawns %v, want one with team_ai %q", got, pvpOnlyTeamAI)
	}
	var pvpOnly int
	if err := database.QueryRow(`SELECT pvp_only FROM matches`).Scan(&pvpOnly); err != nil || pvpOnly != 1 {
		t.Errorf("match pvp_only = %d (%v), want 1", pvpOnly, err)
	}
	teams := map[string]int{}
	rows, _ := database.Query(`SELECT user_id, team FROM match_slots`)
	for rows.Next() {
		var u string
		var team int
		_ = rows.Scan(&u, &team)
		teams[u] = team
	}
	_ = rows.Close()
	if len(teams) != 2 || teams["bob"] != 0 || teams["alice"] == teams["carol"] {
		t.Errorf("PvP-only match slots %v, want alice and carol on opposite teams, bob not in it", teams)
	}

	// The /pvp player's own match: bots as before.
	if err := m.formMatchOfSize("TDM", 1, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	got = spawns()
	if len(got) != 2 {
		t.Fatalf("%d spawns, want 2", len(got))
	}
	if _, ok := got[1]["team_ai"]; ok {
		t.Errorf("a /pvp match was sent team_ai %v", got[1]["team_ai"])
	}
}

// A PvP-only squad alone has nobody to fight: it waits.
func TestPvPOnlySquadAloneWaits(t *testing.T) {
	database := pvpTestDB(t)
	m, spawns := pvpOnlyMatchmaker(t, database)
	queuePool(t, database, "q1", "alice", "TDM", "sq1", true, "2026-11-25T10:00:00Z")
	queuePool(t, database, "q2", "bob", "TDM", "sq1", true, "2026-11-25T10:00:00Z")
	if err := m.formMatchOfSize("TDM", 1, 1, 2, true); err != nil {
		t.Fatal(err)
	}
	if n := len(spawns()); n != 0 {
		t.Fatalf("a lone PvP-only squad got a match (%d spawns)", n)
	}
	var waiting int
	_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries WHERE status='waiting'`).Scan(&waiting)
	if waiting != 2 {
		t.Errorf("%d still waiting, want the squad's 2", waiting)
	}
	// An opponent arrives: squad on one side, the opponent on the other.
	queuePool(t, database, "q3", "carol", "TDM", "", true, "2026-11-25T10:01:00Z")
	if err := m.formMatchOfSize("TDM", 1, 1, 3, true); err != nil {
		t.Fatal(err)
	}
	if n := len(spawns()); n != 1 {
		t.Fatalf("%d spawns after an opponent queued, want 1", n)
	}
}

// The tick never starts a lone PvP-only player, even after the longest wait,
// while a lone /pvp player still gets a match with bots at once.
func TestTickNeverStartsALonePvPOnlyPlayer(t *testing.T) {
	database := pvpTestDB(t)
	m, spawns := pvpOnlyMatchmaker(t, database)
	m.OnlinePlayers = func() []string { return []string{"alice", "bob"} }
	queuePool(t, database, "q1", "alice", "TDM", "", true, "2026-11-25T08:00:00Z")
	if err := m.tick(); err != nil {
		t.Fatal(err)
	}
	if n := len(spawns()); n != 0 {
		t.Fatalf("a lone PvP-only player got a match (%d spawns)", n)
	}
	queuePool(t, database, "q2", "bob", "TDM", "", false, "2026-11-25T08:00:01Z")
	if err := m.tick(); err != nil {
		t.Fatal(err)
	}
	got := spawns()
	if len(got) != 1 {
		t.Fatalf("%d spawns, want bob's match with bots", len(got))
	}
	if _, ok := got[0]["team_ai"]; ok {
		t.Errorf("bob's match was sent team_ai %v", got[0]["team_ai"])
	}
	var aliceWaiting int
	_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries WHERE user_id='alice' AND status='waiting'`).Scan(&aliceWaiting)
	if aliceWaiting != 1 {
		t.Error("the PvP-only player was taken into the /pvp match")
	}
}

// Backfill keeps the pools apart: a PvP-only player never joins a running
// match with bots, nor a /pvp player a PvP-only one.
func TestBackfillKeepsThePoolsApart(t *testing.T) {
	database := pvpTestDB(t)
	m, _ := pvpOnlyMatchmaker(t, database)
	m.BackfillWindow = 3 * 60 * 1e9
	m.BackfillModes = map[string]bool{"TDM": true}
	now := "2099-01-01T00:00:00Z"
	for _, row := range []struct {
		id      string
		pvpOnly bool
	}{{"withbots", false}, {"nobots", true}} {
		if _, err := database.Exec(`INSERT INTO matches(id,game_mode,map,status,created_at,server_ready_at,fleet_type,pvp_only)
			VALUES(?, 'TDM', 'm', 'active', ?, ?, 1, ?)`, row.id, now, now, row.pvpOnly); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO match_slots(match_id,user_id,team) VALUES(?, ?, 1)`, row.id, "in-"+row.id); err != nil {
			t.Fatal(err)
		}
	}
	queuePool(t, database, "q1", "alice", "TDM", "", true, now)
	queuePool(t, database, "q2", "bob", "TDM", "", false, now)
	if _, err := m.backfill("TDM", 1, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.backfill("TDM", 1, 1, false); err != nil {
		t.Fatal(err)
	}
	where := map[string]string{}
	rows, _ := database.Query(`SELECT user_id, match_id FROM match_slots`)
	for rows.Next() {
		var u, id string
		_ = rows.Scan(&u, &id)
		where[u] = id
	}
	_ = rows.Close()
	if where["alice"] != "nobots" || where["bob"] != "withbots" {
		t.Errorf("backfilled into %v, want alice -> nobots, bob -> withbots", where)
	}
}
