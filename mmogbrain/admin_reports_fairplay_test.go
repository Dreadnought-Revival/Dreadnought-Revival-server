package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The Fair play page's additions: verdict and player filters, the summary of
// the listed results, and limit.
func TestAdminFairPlayVerdictPlayerAndSummary(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const other = "0a0b0c0d0e0f40108011aabbccddeeff"
	for _, pid := range []string{adminTestPID, other} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE player_state SET display_name='Pilot-Alpha' WHERE user_id=?`, adminTestPID)
	exec(`UPDATE player_state SET display_name='Pilot-Beta' WHERE user_id=?`, other)
	exec(`INSERT INTO matches(id,game_mode,map,battle_match_id) VALUES('m1','TDM','Gorge','b1')`)
	for _, c := range []struct{ match, pid, flag string }{{"b1", adminTestPID, "check_flag_speed"}, {"b1", other, "check_flag_damage"}, {"b2", adminTestPID, "check_jumps"}} {
		exec(`INSERT INTO battle_results(match_id,user_id,outcome) VALUES(?,?,'win')`, c.match, c.pid)
		v := 1
		if c.flag == "check_jumps" {
			v = 0
		}
		exec(`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES(?,?,?,?)`, c.match, c.pid, c.flag, v)
	}
	r := adminTestRouter()
	get := func(q string) map[string]any {
		t.Helper()
		rec := adminGet(r, "/admin/api/fairplay?"+q, "test-admin-key")
		var out map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
		return out
	}
	n := func(m map[string]any) int { return len(m["results"].([]any)) }
	sum := get("")["summary"].(map[string]any)
	byFlag := sum["by_flag"].(map[string]any)
	if sum["listed"] != 3.0 || sum["flagged"] != 2.0 || sum["open"] != 2.0 || sum["reviewed"] != 0.0 || byFlag["speed"] != 1.0 || byFlag["damage"] != 1.0 {
		t.Errorf("summary: %v", sum)
	}
	if rec := adminPost(r, "/admin/api/fairplay/review", "test-admin-key", `{"match_id":"b1","pid":"`+adminTestPID+`","status":"confirmed","note":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("review: %d", rec.Code)
	}
	sum = get("")["summary"].(map[string]any)
	if sum["open"] != 1.0 || sum["reviewed"] != 1.0 || sum["by_verdict"].(map[string]any)["confirmed"] != 1.0 {
		t.Errorf("summary after review: %v", sum)
	}
	if got := n(get("verdict=confirmed")); got != 1 {
		t.Errorf("verdict=confirmed: %d", got)
	}
	if got := n(get("verdict=none")); got != 2 {
		t.Errorf("verdict=none: %d", got)
	}
	if got := n(get("verdict=bogus")); got != 3 {
		t.Errorf("an unknown verdict is ignored: %d", got)
	}
	if got := n(get("q=pilot-be")); got != 1 {
		t.Errorf("q by name: %d", got)
	}
	if got := n(get("q=" + adminTestPID[:6])); got != 2 {
		t.Errorf("q by pid prefix: %d", got)
	}
	if got := n(get("q=%25")); got != 0 {
		t.Errorf("a lone %% is literal: %d", got)
	}
	if got := n(get("limit=1")); got != 1 {
		t.Errorf("limit=1: %d", got)
	}
}
