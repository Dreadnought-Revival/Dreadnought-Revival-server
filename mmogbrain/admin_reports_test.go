package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

const adminReportsOtherPID = "0a0b0c0d0e0f40108011aabbccddeeff"

// adminReportsSeed: 5 reports of "Ship did not spawn" + 2 "ConnectionLost" by
// the test player, 1 "Ship did not spawn" by another; telemetry for both.
func adminReportsSeed(t *testing.T) {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	for _, pid := range []string{adminTestPID, adminReportsOtherPID} {
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
	for i := 0; i < 5; i++ {
		exec(`INSERT INTO client_reports(user_id,type,name,desc) VALUES(?,?,?,?)`, adminTestPID, "GameplayLogicFailure", "Ship did not spawn", fmt.Sprintf("detail %d with 50%%_odd", i))
	}
	for i := 0; i < 2; i++ {
		exec(`INSERT INTO client_reports(user_id,type,name,desc) VALUES(?,?,?,?)`, adminTestPID, "NetworkFailureClient", "ConnectionLost", "timeout")
	}
	exec(`INSERT INTO client_reports(user_id,type,name,desc) VALUES(?,?,?,?)`, adminReportsOtherPID, "GameplayLogicFailure", "Ship did not spawn", "other")
	exec(`INSERT INTO matches(id,game_mode,map,battle_match_id) VALUES('m1','TDM','Gorge','bm1')`)
	exec(`INSERT INTO battle_results(match_id,user_id,outcome,credits,xp) VALUES('bm1',?, 'win', 700, 90)`, adminTestPID)
	for i := 0; i < 6; i++ {
		exec(`INSERT INTO client_telemetry(user_id,rt,battle_id,summary) VALUES(?,?,?,?)`, adminTestPID, "YA_AnalyticsReceiveCreditsEvent", "bm1", "reward")
	}
	for i := 0; i < 3; i++ {
		exec(`INSERT INTO client_telemetry(user_id,rt,summary) VALUES(?,?,?)`, adminReportsOtherPID, "YA_GameModeEvent", "mode")
	}
	exec(`INSERT INTO client_telemetry(user_id,rt,battle_id,summary) VALUES(?,?,?,?)`, adminReportsOtherPID, "YA_AnalyticsReceiveCreditsEvent", "gone", "reward")
}

func adminReportsGetJSON(t *testing.T, path string, into any) {
	t.Helper()
	rec := adminGet(adminTestRouter(), path, "test-admin-key")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), into) != nil {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
}

type adminReportsResp struct {
	Reports []struct {
		ID      int    `json:"id"`
		Player  string `json:"player"`
		PID     string `json:"pid"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Details string `json:"details"`
	} `json:"reports"`
	Groups []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"groups"`
	Types []string `json:"types"`
	Total int      `json:"total"`
	More  bool     `json:"more"`
}

func TestAdminReportsFiltersGroupsAndPaging(t *testing.T) {
	adminReportsSeed(t)
	var all adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports", &all)
	if len(all.Reports) != 8 || all.Total != 8 || all.More || len(all.Types) != 2 {
		t.Fatalf("all: %d reports, total %d, more %v, types %v", len(all.Reports), all.Total, all.More, all.Types)
	}
	if len(all.Groups) != 2 || all.Groups[0].Name != "Ship did not spawn" || all.Groups[0].Count != 6 || all.Groups[1].Count != 2 {
		t.Errorf("groups: %+v", all.Groups)
	}
	if all.Reports[0].ID < all.Reports[1].ID || all.Reports[0].PID == "" {
		t.Errorf("order/pid: %+v", all.Reports[0])
	}

	var typed adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?type=NetworkFailureClient", &typed)
	if len(typed.Reports) != 2 || len(typed.Groups) != 1 {
		t.Errorf("type filter: %d reports %d groups", len(typed.Reports), len(typed.Groups))
	}
	var mine adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?pid="+adminReportsOtherPID, &mine)
	if len(mine.Reports) != 1 || mine.Reports[0].Details != "other" {
		t.Errorf("pid filter: %+v", mine.Reports)
	}
	var junk adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?pid=not-a-pid", &junk)
	if len(junk.Reports) != 0 {
		t.Errorf("a junk pid must match nobody, got %d", len(junk.Reports))
	}
	// LIKE wildcards in the text are literal: "%" matches only the one report with "%" in it.
	var like adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?q=50%25_odd", &like)
	if len(like.Reports) != 5 {
		t.Errorf("literal %%/_ search: %d, want 5", len(like.Reports))
	}
	var wild adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?q=%25", &wild)
	if len(wild.Reports) != 5 {
		t.Errorf("a lone %% must not match everything: %d", len(wild.Reports))
	}
	var name adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?q=connectionlost", &name)
	if len(name.Reports) != 2 {
		t.Errorf("name search: %d", len(name.Reports))
	}

	// Paging: limit 3, then before=<last id>.
	var p1, p2, p3 adminReportsResp
	adminReportsGetJSON(t, "/admin/api/reports?limit=3", &p1)
	if len(p1.Reports) != 3 || !p1.More || p1.Total != 8 || len(p1.Groups) != 2 {
		t.Fatalf("page 1: %d more %v total %d groups %d", len(p1.Reports), p1.More, p1.Total, len(p1.Groups))
	}
	adminReportsGetJSON(t, fmt.Sprintf("/admin/api/reports?limit=3&before=%d", p1.Reports[2].ID), &p2)
	adminReportsGetJSON(t, fmt.Sprintf("/admin/api/reports?limit=3&before=%d", p2.Reports[2].ID), &p3)
	if len(p2.Reports) != 3 || !p2.More || len(p3.Reports) != 2 || p3.More || p2.Reports[0].ID >= p1.Reports[2].ID {
		t.Errorf("paging: %d/%v %d/%v", len(p2.Reports), p2.More, len(p3.Reports), p3.More)
	}
	// limit is capped at 500 (and junk falls back to the default): both answer.
	for _, q := range []string{"limit=100000", "limit=-5", "limit=abc", "before=abc"} {
		var x adminReportsResp
		adminReportsGetJSON(t, "/admin/api/reports?"+q, &x)
		if len(x.Reports) != 8 {
			t.Errorf("%s: %d reports", q, len(x.Reports))
		}
	}
}

type adminTelemetryResp struct {
	Events []struct {
		ID         int    `json:"id"`
		PID        string `json:"pid"`
		RT         string `json:"rt"`
		BattleID   string `json:"battle_id"`
		ServerPaid string `json:"server_paid"`
	} `json:"events"`
	Types  []string       `json:"types"`
	Counts map[string]int `json:"counts"`
	More   bool           `json:"more"`
}

func TestAdminTelemetryFiltersCountsAndPaging(t *testing.T) {
	adminReportsSeed(t)
	var all adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry", &all)
	if len(all.Events) != 10 || all.Counts["YA_AnalyticsReceiveCreditsEvent"] != 7 || all.Counts["YA_GameModeEvent"] != 3 || len(all.Types) != 2 {
		t.Fatalf("all: %d events, counts %v", len(all.Events), all.Counts)
	}
	paid, missing := 0, 0
	for _, e := range all.Events {
		switch {
		case e.BattleID == "bm1" && e.ServerPaid == "700 credits, 90 XP":
			paid++
		case e.BattleID == "gone" && e.ServerPaid == "no result recorded":
			missing++
		}
	}
	if paid != 6 || missing != 1 {
		t.Errorf("server paid: %d matched, %d missing", paid, missing)
	}
	var mine adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry?pid="+adminReportsOtherPID, &mine)
	if len(mine.Events) != 4 || mine.Counts["YA_GameModeEvent"] != 3 {
		t.Errorf("pid: %d events %v", len(mine.Events), mine.Counts)
	}
	// counts ignore the rt filter, events obey it.
	var rt adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry?rt=YA_GameModeEvent&pid="+adminReportsOtherPID, &rt)
	if len(rt.Events) != 3 || rt.Counts["YA_AnalyticsReceiveCreditsEvent"] != 1 {
		t.Errorf("rt: %d events %v", len(rt.Events), rt.Counts)
	}
	var junk adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry?pid=zzz", &junk)
	if len(junk.Events) != 0 {
		t.Errorf("junk pid: %d events", len(junk.Events))
	}
	var p1, p2 adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry?limit=4", &p1)
	adminReportsGetJSON(t, fmt.Sprintf("/admin/api/telemetry?limit=4&before=%d", p1.Events[3].ID), &p2)
	if len(p1.Events) != 4 || !p1.More || len(p2.Events) != 4 || !p2.More || p2.Events[0].ID >= p1.Events[3].ID {
		t.Errorf("paging: %d/%v %d/%v", len(p1.Events), p1.More, len(p2.Events), p2.More)
	}
	// The old q= (name or id prefix) still works.
	var q adminTelemetryResp
	adminReportsGetJSON(t, "/admin/api/telemetry?q="+adminTestPID[:8], &q)
	if len(q.Events) != 6 {
		t.Errorf("q prefix: %d events", len(q.Events))
	}
}

func TestAdminReportsRoutesNeedTheKeyAndDoNotDeadlock(t *testing.T) {
	adminReportsSeed(t)
	r := adminTestRouter()
	paths := []string{"/admin/api/reports?q=x&type=T&pid=" + adminTestPID + "&before=5&limit=10", "/admin/api/telemetry?pid=" + adminTestPID + "&before=5&limit=10&rt=YA_GameModeEvent",
		"/admin/api/fairplay?verdict=none&q=Pilot&limit=5"}
	for _, path := range paths {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(r, path, key); rec.Code != http.StatusForbidden {
				t.Errorf("%s with key %q: %d, want 403", path, key, rec.Code)
			}
		}
		done := make(chan int, 1)
		go func() { done <- adminGet(r, path, "test-admin-key").Code }()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: %d", path, code)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer in 3 s: the single-connection store is deadlocked", path)
		}
	}
}
