package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// A player the matchmaker puts into a running match (backfill) is only in
// match_slots; the control plane still lists the players the match was formed
// with. The Battle servers page shows both, each once.
func TestAdminInstancesListBackfilledPlayers(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const (
		formed   = "650dd79476a1484b8adcd01ac2f17354"
		joinedA  = "4d1f0c2a6b7e4c559a103f2b8e9d7c61"
		joinedB  = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"
		matchRef = "m-backfill"
	)
	for _, q := range []string{
		`INSERT INTO matches(id,game_mode,map,server_ip,server_port,status,created_at,instance_id,battle_match_id) VALUES('` + matchRef + `','TDM','Gorge','127.0.0.1',7901,'active','2026-11-17T19:39:04Z','inst-1','bm-1')`,
		`INSERT INTO match_slots(match_id,user_id,team,joined_at) VALUES('` + matchRef + `','` + formed + `',1,'2026-11-17 19:39:04')`,
		`INSERT INTO match_slots(match_id,user_id,team,joined_at) VALUES('` + matchRef + `','` + joinedA + `',2,'2026-11-17 19:39:48')`,
		`INSERT INTO match_slots(match_id,user_id,team,joined_at) VALUES('` + matchRef + `','` + joinedB + `',1,'2026-11-17 19:40:48')`,
		`INSERT INTO match_slots(match_id,user_id,team) VALUES('other-match','` + joinedB + `',1)`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":1,"instances":[{"id":"inst-1","port":7901,"game_mode":"TDM","match_id":"` + matchRef + `","players":["` + formed + `"],"ready":true,"running":true}]}`))
	}))
	defer cp.Close()
	old := adminDash
	t.Cleanup(func() { adminDash = old })
	r := mux.NewRouter()
	registerAdminDashboard(r, "test-admin-key", cp.URL, "")

	rec := adminGet(r, "/admin/api/instances", "test-admin-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("instances: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Instances []adminInstance `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Instances) != 1 {
		t.Fatalf("decode: %v %s", err, rec.Body.String())
	}
	got := body.Instances[0]
	want := []string{formed, joinedA, joinedB}
	if len(got.Players) != len(want) || len(got.PlayerNames) != len(want) {
		t.Fatalf("players %v names %v, want %v", got.Players, got.PlayerNames, want)
	}
	for k := range want {
		if got.Players[k] != want[k] {
			t.Fatalf("players %v, want %v", got.Players, want)
		}
	}
	if got.MatchRef != matchRef {
		t.Errorf("match_ref %q", got.MatchRef)
	}
}
