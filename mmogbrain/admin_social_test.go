package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

const socialPID1 = "a1000000000000000000000000000001"
const socialPID2 = "a1000000000000000000000000000002"

func socialSeed(t *testing.T) {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	for _, pid := range []string{socialPID1, socialPID2} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name='Pilot-01' WHERE user_id=?`, socialPID1); err != nil {
		t.Fatal(err)
	}
	week := havocWeekStart(time.Now()).Format(havocTimeLayout)
	stmts := []string{
		`INSERT INTO matches(id,game_mode,map,status,created_at,battle_match_id,fleet_type) VALUES('mm1','Havoc','Glacier','ended','2026-09-29T20:00:00Z','bm1',1)`,
		`INSERT INTO battle_results(match_id,user_id,outcome,fleet_type) VALUES('bm1','` + socialPID1 + `','win',1)`,
		`INSERT INTO battle_result_stats(match_id,user_id,stat,value) VALUES('bm1','` + socialPID1 + `','score',900)`,
		`INSERT INTO havoc_runs(match_id,user_id,score,waves,created_at) VALUES('bm1','` + socialPID1 + `',7000,8,'` + week + `')`,
		`INSERT INTO havoc_runs(match_id,user_id,score,waves,created_at) VALUES('bm2','` + socialPID2 + `',3000,5,'` + week + `')`,
		`INSERT INTO havoc_reward_claims(user_id,week_start,reward,match_id) VALUES('` + socialPID1 + `','` + week + `','Reward1','bm1')`,
		`INSERT INTO admin_chat_mutes(user_id,reason,expires_at) VALUES('` + socialPID2 + `','x','')`,
	}
	for i := 0; i < 30; i++ {
		ch := "dreadnought.global"
		if i%3 == 0 {
			ch = "mm1.all"
		}
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO chat_messages(id,channel,sender_id,content) VALUES('c%d','%s','%s','line %d 50%% <b>x</b>')`, i, ch, []string{socialPID1, socialPID2}[i%2], i))
	}
	for _, q := range stmts {
		if _, err := database.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

var socialRoutes = []string{"/admin/api/chat", "/admin/api/leaderboard", "/admin/api/leaderboard?board=Havoc%7C1%7Clifetime", "/admin/api/havoc", "/admin/api/havoc?weeks=26"}

func TestAdminSocialRoutesNeedTheKey(t *testing.T) {
	r := adminTestRouter()
	for _, p := range socialRoutes {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(r, p, key); rec.Code != http.StatusForbidden {
				t.Errorf("%s key %q: %d", p, key, rec.Code)
			}
		}
	}
}

func TestAdminSocialRoutesDoNotDeadlock(t *testing.T) {
	socialSeed(t)
	r := adminTestRouter()
	for _, p := range socialRoutes {
		done := make(chan int, 1)
		go func() { done <- adminGet(r, p, "test-admin-key").Code }()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: %d", p, code)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer in 3 s: deadlocked store", p)
		}
	}
}

func socialJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	rec := adminGet(adminTestRouter(), path, "test-admin-key")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdminChatFiltersAndPaging(t *testing.T) {
	socialSeed(t)
	d := socialJSON(t, "/admin/api/chat?limit=10")
	msgs := d["messages"].([]any)
	if len(msgs) != 10 || d["more"] != true {
		t.Fatalf("limit 10: %d messages, more=%v", len(msgs), d["more"])
	}
	first := msgs[0].(map[string]any)
	if first["text"] != "line 29 50% <b>x</b>" {
		t.Errorf("newest first, text kept raw: %v", first["text"])
	}
	// paging: the next page continues below the last seq, no overlap
	last := msgs[9].(map[string]any)["seq"].(float64)
	d2 := socialJSON(t, fmt.Sprintf("/admin/api/chat?limit=10&before=%d", int64(last)))
	if got := d2["messages"].([]any)[0].(map[string]any)["text"]; got != "line 19 50% <b>x</b>" {
		t.Errorf("page 2 starts at %v", got)
	}
	if n := len(socialJSON(t, "/admin/api/chat?channel=mm1.all&limit=500")["messages"].([]any)); n != 10 {
		t.Errorf("channel filter: %d, want 10", n)
	}
	if n := len(socialJSON(t, "/admin/api/chat?pid="+socialPID2+"&limit=500")["messages"].([]any)); n != 15 {
		t.Errorf("pid filter: %d, want 15", n)
	}
	// LIKE wildcards in the search are literal: "50%" matches, "5_" does not
	if n := len(socialJSON(t, "/admin/api/chat?q=50%25&limit=500")["messages"].([]any)); n != 30 {
		t.Errorf("q=50%%: %d, want 30", n)
	}
	if n := len(socialJSON(t, "/admin/api/chat?q=5_&limit=500")["messages"].([]any)); n != 0 {
		t.Errorf("q=5_: %d, want 0 (underscore literal)", n)
	}
	if n := len(socialJSON(t, "/admin/api/chat?limit=9999")["messages"].([]any)); n != 30 {
		t.Errorf("limit clamp: %d", n)
	}
	m1 := first["name"]
	if m1 != "" && first["pid"] == socialPID2 {
		t.Errorf("unnamed player should have an empty name, got %v", m1)
	}
	if first["pid"] == socialPID2 && first["muted"] != true {
		t.Errorf("muted flag missing")
	}
	for _, bad := range []string{"limit=0", "limit=x", "before=abc", "before=0"} {
		if rec := adminGet(adminTestRouter(), "/admin/api/chat?"+bad, "test-admin-key"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	chans := d["channels"].([]any)
	if len(chans) != 2 {
		t.Fatalf("channels: %v", chans)
	}
}

func TestAdminChatChannelNames(t *testing.T) {
	for ch, want := range map[string][2]string{
		"dreadnought.global": {"Global", ""},
		"global":             {"Global", ""},
		"english.language":   {"English", ""},
		"abc-def.all":        {"Match (all)", "abc-def"},
		"abc-def-2.team":     {"Match team 2", "abc-def"},
		"sq1.squad":          {"Squad", ""},
		"weird":              {"weird", ""},
	} {
		if l, m := adminChatChannel(ch); l != want[0] || m != want[1] {
			t.Errorf("%s: %q %q, want %v", ch, l, m, want)
		}
	}
}

func TestAdminLeaderboardAndHavocFixture(t *testing.T) {
	socialSeed(t)
	d := socialJSON(t, "/admin/api/leaderboard")
	if b := d["boards"].([]any); len(b) != 1 || b[0].(map[string]any)["mode"] != "Havoc" {
		t.Fatalf("boards: %v", d["boards"])
	}
	e := socialJSON(t, "/admin/api/leaderboard?board=Havoc%7C1%7Clifetime")["entries"].([]any)
	if len(e) != 1 || e[0].(map[string]any)["score"] != float64(900) || e[0].(map[string]any)["name"] != "Pilot-01" {
		t.Errorf("entries: %v", e)
	}
	for _, bad := range []string{"x", "Havoc%7C9%7Clifetime", "Havoc%7C1%7Cforever"} {
		if rec := adminGet(adminTestRouter(), "/admin/api/leaderboard?board="+bad, "test-admin-key"); rec.Code != http.StatusBadRequest {
			t.Errorf("board %s: %d, want 400", bad, rec.Code)
		}
	}
	h := socialJSON(t, "/admin/api/havoc?weeks=2")
	tot := h["totals"].(map[string]any)
	if tot["runs"] != float64(2) || tot["best"] != float64(7000) || tot["claims"] != float64(1) || tot["players"] != float64(2) {
		t.Errorf("totals: %v", tot)
	}
	if st := h["stages"].(map[string]any); st["8"] != float64(1) || st["5"] != float64(1) {
		t.Errorf("stages: %v", st)
	}
	top := h["weekly_top"].([]any)
	if len(top) != 2 || top[0].(map[string]any)["score"] != float64(7000) {
		t.Errorf("weekly top: %v", top)
	}
	if rec := adminGet(adminTestRouter(), "/admin/api/havoc?weeks=99", "test-admin-key"); rec.Code != http.StatusBadRequest {
		t.Errorf("weeks=99: %d", rec.Code)
	}
}
