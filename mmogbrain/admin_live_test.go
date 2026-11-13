package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

const (
	liveTestKey = "test-admin-key"
	liveTestA   = "a1000000000000000000000000000001"
	liveTestB   = "a1000000000000000000000000000002"
)

func liveSeed(t *testing.T) {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	for i, pid := range []string{liveTestA, liveTestB} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE player_state SET display_name=? WHERE user_id=?`, []string{"Pilot-01", "Pilot-02"}[i], pid); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	rfc := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO matches(id,game_mode,map,server_port,status,created_at,started_at,ended_at,battle_match_id,fleet_type) VALUES('m-tdm','TDM','Gorge',7900,'ended',?,?,?,'bm-tdm',2)`, []any{rfc(3 * time.Hour), rfc(3 * time.Hour), rfc(2 * time.Hour)}},
		{`INSERT INTO matches(id,game_mode,map,server_port,status,created_at,battle_match_id) VALUES('m-ter','TER','Skybridge',7901,'ended',?,'bm-ter')`, []any{rfc(5 * time.Hour)}},
		{`INSERT INTO matches(id,game_mode,map,status,created_at) VALUES('m-forming','TDM','Gorge','forming',?)`, []any{rfc(time.Minute)}},
		{`INSERT INTO match_slots(match_id,user_id,team) VALUES('m-forming',?,1)`, []any{liveTestA}},
		{`INSERT INTO battle_results(match_id,user_id,team,outcome,kills,deaths,credits,xp) VALUES('bm-tdm',?,1,'win',5,1,900,1500)`, []any{liveTestA}},
		{`INSERT INTO battle_results(match_id,user_id,team,outcome,kills,deaths,credits,xp) VALUES('bm-tdm',?,2,'loss',2,4,500,800)`, []any{liveTestB}},
		{`INSERT INTO battle_results(match_id,user_id,team,outcome,kills) VALUES('bm-tdm-r2',?,1,'win',1)`, []any{liveTestA}},
		{`INSERT INTO battle_results(match_id,user_id,team,outcome,kills) VALUES('orphan-battle',?,1,'draw',0)`, []any{liveTestA}},
		{`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES('bm-tdm',?,'score',1234)`, []any{liveTestA}},
		{`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES('bm-tdm',?,'check_flag_speed',1)`, []any{liveTestA}},
		{`INSERT INTO fair_play_reviews(match_id,user_id,status,note) VALUES('bm-tdm',?,'watch','n')`, []any{liveTestA}},
		{`INSERT INTO havoc_runs(match_id,user_id,score,waves) VALUES('bm-tdm',?,5000,7)`, []any{liveTestA}},
		{`INSERT INTO queue_entries(id,user_id,game_mode,queued_at,status,fleet_type,party_id) VALUES('q1',?,'TDM',?,'waiting',1,'p1')`, []any{liveTestA, rfc(30 * time.Second)}},
		{`INSERT INTO queue_entries(id,user_id,game_mode,queued_at,status,fleet_type,party_id) VALUES('q2',?,'TDM',?,'waiting',1,'p1')`, []any{liveTestB, rfc(20 * time.Second)}},
	} {
		if _, err := database.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

// liveGet answers within 3 s (the single-connection store would hang forever
// on a query inside an open result set) and decodes the JSON reply.
func liveGet(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	return liveGetOn(t, adminTestRouter(), path)
}

func liveGetOn(t *testing.T, r http.Handler, path string) (int, map[string]any) {
	t.Helper()
	type reply struct {
		code int
		body string
	}
	done := make(chan reply, 1)
	go func() {
		rec := adminGet(r, path, liveTestKey)
		done <- reply{rec.Code, rec.Body.String()}
	}()
	select {
	case r := <-done:
		var out map[string]any
		_ = json.Unmarshal([]byte(r.body), &out)
		return r.code, out
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not answer in 3 s: the single-connection store is deadlocked", path)
	}
	return 0, nil
}

func TestAdminLiveRoutesNeedTheKey(t *testing.T) {
	liveSeed(t)
	r := adminTestRouter()
	for _, p := range []string{"/admin/api/queue", "/admin/api/matches", "/admin/api/matches/m-tdm", "/admin/api/instances"} {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(r, p, key); rec.Code != http.StatusForbidden {
				t.Errorf("%s with key %q: %d", p, key, rec.Code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/instances/abc", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("stop without key: %d", rec.Code)
	}
}

func TestAdminLiveMatchesFiltersAndPaging(t *testing.T) {
	liveSeed(t)
	count := func(path string) (int, map[string]any) {
		code, out := liveGet(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
		ms, _ := out["matches"].([]any)
		return len(ms), out
	}
	if n, out := count("/admin/api/matches"); n != 3 {
		t.Errorf("all: %d", n)
	} else if first := out["matches"].([]any)[0].(map[string]any); first["id"] != "m-forming" {
		t.Errorf("newest first, got %v", first["id"])
	}
	if n, _ := count("/admin/api/matches?mode=TER"); n != 1 {
		t.Errorf("mode: %d", n)
	}
	if n, _ := count("/admin/api/matches?status=forming"); n != 1 {
		t.Errorf("status: %d", n)
	}
	if n, _ := count("/admin/api/matches?q=Pilot-02"); n != 1 {
		t.Errorf("q by name: %d", n)
	}
	if n, _ := count("/admin/api/matches?q=pilot-0"); n != 2 { // a name, case-insensitive: the ended match and the forming slot
		t.Errorf("q by name part: %d", n)
	}
	if n, _ := count("/admin/api/matches?q=" + liveTestB); n != 1 {
		t.Errorf("q by pid prefix: %d", n)
	}
	if n, _ := count("/admin/api/matches?q=bm-t"); n != 2 { // battle ids bm-tdm, bm-ter
		t.Errorf("q by battle id: %d", n)
	}
	if n, _ := count("/admin/api/matches?q=%25"); n != 0 { // wildcard is literal
		t.Errorf("q wildcard: %d", n)
	}
	n, out := count("/admin/api/matches?limit=2")
	if n != 2 || out["more"] != true || out["next_before"] == "" {
		t.Fatalf("page 1: %d %v", n, out)
	}
	n, out = count("/admin/api/matches?limit=2&before=" + strings.ReplaceAll(out["next_before"].(string), "+", "%2B"))
	if n != 1 || out["more"] != false {
		t.Errorf("page 2: %d %v", n, out["more"])
	}
	if code, _ := liveGet(t, "/admin/api/matches?status=bogus"); code != http.StatusBadRequest {
		t.Errorf("bad status: %d", code)
	}
	// Old keys and the results of a match (round 2 counts with its host's match).
	_, out = count("/admin/api/matches?mode=TDM&status=ended")
	m := out["matches"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "mode", "map", "status", "started", "ended", "fleet_type", "results"} {
		if _, ok := m[k]; !ok {
			t.Errorf("old key %q missing", k)
		}
	}
	if rs := m["results"].([]any); len(rs) != 3 || m["winner_team"] != float64(1) || m["kills"] != float64(8) {
		t.Errorf("results %v winner %v kills %v", len(rs), m["winner_team"], m["kills"])
	}
	// Forming match: its head count comes from its slots.
	_, out = count("/admin/api/matches?status=forming")
	if p := out["matches"].([]any)[0].(map[string]any)["players"]; p != float64(1) {
		t.Errorf("forming players %v", p)
	}
}

func TestAdminLiveMatchDetail(t *testing.T) {
	liveSeed(t)
	for _, id := range []string{"m-tdm", "bm-tdm"} { // matches.id or the battle server's id
		code, out := liveGet(t, "/admin/api/matches/"+id)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", id, code)
		}
		if out["match"].(map[string]any)["id"] != "m-tdm" {
			t.Errorf("%s: wrong match %v", id, out["match"])
		}
		rs := out["results"].([]any)
		if len(rs) != 3 {
			t.Fatalf("%s: %d results", id, len(rs))
		}
		var a map[string]any
		for _, x := range rs {
			if p := x.(map[string]any); p["pid"] == liveTestA && p["round"] == nil {
				a = p
			}
		}
		if a == nil || a["name"] != "Pilot-01" || a["review"] != "watch" || a["checked"] != true {
			t.Errorf("%s: player row %v", id, a)
		}
		if a["stats"].(map[string]any)["score"] != float64(1234) || len(a["flags"].([]any)) != 1 || out["flagged"] != float64(1) {
			t.Errorf("%s: stats/flags %v %v", id, a["stats"], a["flags"])
		}
		if len(out["havoc"].([]any)) != 1 {
			t.Errorf("%s: havoc %v", id, out["havoc"])
		}
	}
	if code, out := liveGet(t, "/admin/api/matches/m-forming"); code != http.StatusOK || len(out["slots"].([]any)) != 1 {
		t.Errorf("forming: %d %v", code, out["slots"])
	}
	if code, out := liveGet(t, "/admin/api/matches/orphan-battle"); code != http.StatusOK || len(out["results"].([]any)) != 1 {
		t.Errorf("battle id without a match row: %d %v", code, out)
	}
	if code, _ := liveGet(t, "/admin/api/matches/nope"); code != http.StatusNotFound {
		t.Errorf("unknown id: %d", code)
	}
}

func TestAdminLiveQueue(t *testing.T) {
	liveSeed(t)
	code, out := liveGet(t, "/admin/api/queue")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	es := out["entries"].([]any)
	if len(es) != 2 || out["waiting"] != float64(2) {
		t.Fatalf("entries %v waiting %v", len(es), out["waiting"])
	}
	e := es[0].(map[string]any)
	if e["name"] != "Pilot-01" || e["party_size"] != float64(2) || e["mode"] != "TDM" {
		t.Errorf("entry %v", e)
	}
	if tot := out["totals"].([]any); len(tot) != 1 || tot[0].(map[string]any)["waiting"] != float64(2) {
		t.Errorf("totals %v", tot)
	}
	f := out["forming"].([]any)
	if len(f) != 1 || len(f[0].(map[string]any)["players"].([]any)) != 1 {
		t.Errorf("forming %v", f)
	}
}

func TestAdminLiveInstancesControlPlaneDown(t *testing.T) {
	liveSeed(t)
	code, out := liveGet(t, "/admin/api/instances")
	if code != http.StatusOK || !strings.Contains(out["error"].(string), "control plane unreachable") {
		t.Errorf("%d %v", code, out)
	}
	// The stop route validates the id before it reaches the control plane URL.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/instances/a%20b", nil)
	req.Header.Set("X-Admin-Key", liveTestKey)
	adminTestRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
}

func TestAdminLiveInstancesListAndStop(t *testing.T) {
	liveSeed(t)
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"instances":[{"id":"inst-1","port":7900,"game_mode":"TDM","map":"Gorge","match_id":"bm-tdm","players":["` + liveTestA + `"],"ready":true,"running":true,"started_at":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer cp.Close()
	r := mux.NewRouter()
	registerAdminDashboard(r, liveTestKey, cp.URL, "")
	code, out := liveGetOn(t, r, "/admin/api/instances")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	i := out["instances"].([]any)[0].(map[string]any)
	if i["match_ref"] != "m-tdm" || i["player_names"].([]any)[0] != "Pilot-01" {
		t.Errorf("instance %v", i)
	}
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/instances/inst-1", nil)
	req.Header.Set("X-Admin-Key", liveTestKey)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = currentMmogPlayerStateDB().QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='stop instance' AND target='inst-1'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows: %d", n)
	}
}
