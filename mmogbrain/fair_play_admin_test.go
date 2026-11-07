package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

type fairPlayResp struct {
	Results []fairPlayResult `json:"results"`
	Modes   []string         `json:"modes"`
	Player  struct {
		Checked  int `json:"checked"`
		Flagged  int `json:"flagged"`
		Reviewed int `json:"reviewed"`
	} `json:"player"`
}

func fairPlayGet(t *testing.T, r http.Handler, query string) fairPlayResp {
	t.Helper()
	rec := adminGet(r, "/admin/api/fairplay?"+query, "test-admin-key")
	var out fairPlayResp
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("fairplay?%s: %d %s", query, rec.Code, rec.Body.String())
	}
	return out
}

// The Fair play tab lists what the mod reported, filters it, and keeps an
// admin's review; a review changes nothing for the player.
func TestAdminFairPlay(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const other = "0a0b0c0d0e0f40108011aabbccddeeff"
	for _, pid := range []string{adminTestPID, other} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO matches(id,game_mode,map,battle_match_id) VALUES('m1','TDM','/Game/Maps/MP/Gorge/MP_Gorge_P','b1'),('m2','Onslaught','/Game/Maps/MP/Sky/MP_Sky_P','b2')`)
	result := func(match, pid string, damage int, params string) {
		t.Helper()
		mustExec(`INSERT INTO battle_results(match_id,user_id,outcome,damage) VALUES(?,?,'win',?)`, match, pid, damage)
		q, err := url.ParseQuery(params)
		if err != nil {
			t.Fatal(err)
		}
		stats, _ := fairPlayCheckStats(q)
		for stat, v := range stats {
			mustExec(`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES(?,?,?,?)`, match, pid, stat, v)
		}
	}
	// b1: the test player flagged for speed in a Heavy Destroyer (1500 vs bots 900);
	// the other player clean. b2: the test player clean.
	result("b1", adminTestPID, 50000, "chk_flags=speed&chk_spd=15.1500.1700.900.40&chk_jumps=2&chk_dmg_measured=49990&chk_dmg_hits=120")
	result("b1", other, 30000, "chk_flags=&chk_spd=7.400.450.380.60&chk_dmg_measured=30000")
	result("b2", adminTestPID, 20000, "chk_flags=&chk_spd=15.800.900.0.0")
	mustExec(`INSERT INTO battle_results(match_id,user_id,outcome) VALUES('b0',?,'loss')`, other) // before the checks: not listed

	r := adminTestRouter()
	if rec := adminGet(r, "/admin/api/fairplay", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}

	all := fairPlayGet(t, r, "")
	if len(all.Results) != 3 {
		t.Fatalf("%d checked results, want 3 (the unchecked one left out)", len(all.Results))
	}
	if len(all.Modes) != 2 {
		t.Errorf("modes %v, want TDM and Onslaught", all.Modes)
	}

	flagged := fairPlayGet(t, r, "flagged=1")
	if len(flagged.Results) != 1 {
		t.Fatalf("%d flagged, want 1", len(flagged.Results))
	}
	x := flagged.Results[0]
	if x.MatchID != "b1" || x.PID != adminTestPID || x.Mode != "TDM" || x.Map != "Gorge" || len(x.Flags) != 1 || x.Flags[0] != "speed" {
		t.Errorf("flagged result: %+v", x)
	}
	if len(x.Speed) != 1 || x.Speed[0].ClassName != "Destroyer (Heavy)" || x.Speed[0].Sustained != 1500 || x.Speed[0].Fastest != 1700 ||
		x.Speed[0].Bots != 900 || x.Speed[0].Ratio < 1.66 || x.Speed[0].Ratio > 1.67 {
		t.Errorf("speed: %+v", x.Speed)
	}
	if x.Jumps != 2 || x.DamageReported != 50000 || x.DamageMeasured != 49990 || x.DamageHits != 120 {
		t.Errorf("figures: %+v", x)
	}

	// No bot yardstick: no ratio.
	if got := fairPlayGet(t, r, "match=b2"); len(got.Results) != 1 || got.Results[0].Speed[0].Ratio != 0 {
		t.Errorf("match b2: %+v", got.Results)
	}
	if got := fairPlayGet(t, r, "match=b1"); len(got.Results) != 2 {
		t.Errorf("match b1 lists %d players, want both", len(got.Results))
	}
	if got := fairPlayGet(t, r, "mode=Onslaught"); len(got.Results) != 1 || got.Results[0].MatchID != "b2" {
		t.Errorf("mode filter: %+v", got.Results)
	}
	if got := fairPlayGet(t, r, "class=7"); len(got.Results) != 1 || got.Results[0].PID != other {
		t.Errorf("class filter: %+v", got.Results)
	}
	p := fairPlayGet(t, r, "pid="+adminTestPID)
	if len(p.Results) != 2 || p.Player.Checked != 2 || p.Player.Flagged != 1 || p.Player.Reviewed != 0 {
		t.Errorf("player view: %d results, summary %+v", len(p.Results), p.Player)
	}

	// Reviews.
	for body, want := range map[string]int{
		`{"match_id":"b1","pid":"` + adminTestPID + `","status":"guilty"}`:  http.StatusBadRequest,
		`{"match_id":"nope","pid":"` + adminTestPID + `","status":"watch"}`: http.StatusNotFound,
		`{"match_id":"b1","pid":"` + adminTestPID + `","status":"watch"}`:   http.StatusForbidden, // no key, below
	} {
		key := "test-admin-key"
		if want == http.StatusForbidden {
			key = ""
		}
		if rec := adminPost(r, "/admin/api/fairplay/review", key, body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
	before := mmogPlayerStateForPID(adminTestPID)
	if rec := adminPost(r, "/admin/api/fairplay/review", "test-admin-key",
		`{"match_id":"b1","pid":"`+adminTestPID+`","status":"false_positive","note":"bot-heavy TDM"}`); rec.Code != http.StatusOK {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	got := fairPlayGet(t, r, "flagged=1").Results[0]
	if got.Review == nil || got.Review.Status != "false_positive" || got.Review.Note != "bot-heavy TDM" {
		t.Errorf("review not shown: %+v", got.Review)
	}
	if n := len(fairPlayGet(t, r, "unreviewed=1").Results); n != 0 {
		t.Errorf("%d unreviewed after the review, want 0", n)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='fair play review' AND target=?`, adminTestPID).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d audit rows, want 1", audits)
	}
	after := mmogPlayerStateForPID(adminTestPID)
	if after.softCurrency != before.softCurrency || after.premiumCurrency != before.premiumCurrency || adminPlayerBanned(adminTestPID) {
		t.Error("a review changed something for the player")
	}

	// Removing it.
	if rec := adminPost(r, "/admin/api/fairplay/review", "test-admin-key", `{"match_id":"b1","pid":"`+adminTestPID+`","status":""}`); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d", rec.Code)
	}
	if n := len(fairPlayGet(t, r, "unreviewed=1").Results); n != 1 {
		t.Errorf("%d unreviewed after removing the review, want 1", n)
	}
}

func TestShipClassLabel(t *testing.T) {
	for c, want := range map[int]string{1: "Dreadnought (Light)", 9: "Corvette (Heavy)", 10: "Artillery Cruiser (Medium)",
		13: "Tactical Cruiser (Heavy)", 15: "Destroyer (Heavy)", 19: "class 19"} {
		if got := shipClassLabel(c); got != want {
			t.Errorf("shipClassLabel(%d) = %q, want %q", c, got, want)
		}
	}
}
