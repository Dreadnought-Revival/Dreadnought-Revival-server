package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const adminProfileOtherPID = "b2000000000000000000000000000002"

// adminDelete sends an authenticated DELETE.
func adminDelete(r http.Handler, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if key != "" {
		req.Header.Set("X-Admin-Key", key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// adminProfileSeed gives a player a row in every table the profile reads.
func adminProfileSeed(t *testing.T) *sql.DB {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	for _, pid := range []string{adminTestPID, adminProfileOtherPID} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	sqlTime := func(d time.Duration) string { return now.Add(d).Format("2006-01-02 15:04:05") }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE player_state SET display_name='Pilot-Seed' WHERE user_id=?`, []any{adminTestPID}},
		{`UPDATE player_state SET display_name='Pilot-Friend' WHERE user_id=?`, []any{adminProfileOtherPID}},
		{`INSERT INTO matches(id,game_mode,map,server_ip,server_port,status,created_at,started_at,battle_match_id) VALUES('m1','TDM','Glacier','127.0.0.1',7777,'ended','2026-09-29T20:00:00Z','2026-09-29T20:00:00Z','bm1')`, nil},
		{`INSERT INTO battle_results(match_id,user_id,outcome,kills) VALUES('bm1',?,'win',5)`, []any{adminTestPID}},
		{`INSERT INTO client_reports(user_id,type,name,desc) VALUES(?,'T','N','D')`, []any{adminTestPID}},
		{`INSERT INTO client_telemetry(user_id,rt,battle_id,summary) VALUES(?,'YA_AnalyticsEvent','bm1','s')`, []any{adminTestPID}},
		{`INSERT INTO player_ribbons(user_id,ribbon_type,count) VALUES(?,'Dominator',4)`, []any{adminTestPID}},
		{`INSERT INTO player_stats_counters(user_id,counter_id,counter_sub_id,value) VALUES(?,'Kills','',9)`, []any{adminTestPID}},
		{`INSERT INTO player_career_claims(user_id,goal_id,claimed_stages) VALUES(?,'G1',2)`, []any{adminTestPID}},
		{`INSERT INTO player_daily_contracts(user_id,slot,quest_id,target,progress,reward,state,assigned_at) VALUES(?,0,'YMPQ_Kills',10,3,500,'active',?)`, []any{adminTestPID, now.Unix()}},
		{`INSERT INTO player_friends(pid_a,pid_b,requester_id,state) VALUES(?,?,?,'accepted')`, []any{adminTestPID, adminProfileOtherPID, adminTestPID}},
		{`INSERT INTO player_ignores(pid,ignored_id) VALUES(?,?)`, []any{adminProfileOtherPID, adminTestPID}},
		{`INSERT INTO chat_messages(id,channel,sender_id,content) VALUES('c1','dreadnought.global',?,'hello there')`, []any{adminTestPID}},
		{`INSERT INTO player_purchases(user_id,item_id,item_type,price_paid,currency) VALUES(?,?,'loadout',100,'CR')`, []any{adminTestPID, heroShipLoadouts[0].loadoutID}},
		{`INSERT INTO admin_bans(user_id,reason,expires_at) VALUES(?,'sample',?)`, []any{adminTestPID, sqlTime(48 * time.Hour)}},
		{`INSERT INTO admin_chat_mutes(user_id,reason,expires_at) VALUES(?,'sample',?)`, []any{adminTestPID, sqlTime(-time.Hour)}},
		{`INSERT INTO admin_audit(actor,action,target,details) VALUES('127.0.0.1','balances',?,'{"credits":5}')`, []any{adminTestPID}},
		{`INSERT INTO admin_player_notes(user_id,note,actor) VALUES(?,'seeded note','127.0.0.1')`, []any{adminTestPID}},
	} {
		if _, err := database.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	return database
}

func TestAdminPlayerProfileRequiresTheKey(t *testing.T) {
	r := adminTestRouter()
	base := "/admin/api/players/" + adminTestPID
	for _, key := range []string{"", "wrong"} {
		if rec := adminGet(r, base+"/profile", key); rec.Code != http.StatusForbidden {
			t.Errorf("profile with key %q: %d", key, rec.Code)
		}
		if rec := adminGet(r, base+"/notes", key); rec.Code != http.StatusForbidden {
			t.Errorf("notes list with key %q: %d", key, rec.Code)
		}
		if rec := adminPost(r, base+"/notes", key, `{"note":"x"}`); rec.Code != http.StatusForbidden {
			t.Errorf("note add with key %q: %d", key, rec.Code)
		}
		if rec := adminDelete(r, base+"/notes/1", key); rec.Code != http.StatusForbidden {
			t.Errorf("note delete with key %q: %d", key, rec.Code)
		}
		if rec := adminPost(r, base+"/shipxp", key, `{"ship_id":1,"amount":1,"mode":"add"}`); rec.Code != http.StatusForbidden {
			t.Errorf("shipxp with key %q: %d", key, rec.Code)
		}
	}
}

func TestAdminPlayerProfileDoesNotDeadlockAndIsFull(t *testing.T) {
	adminProfileSeed(t)
	r := adminTestRouter()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- adminGet(r, "/admin/api/players/"+adminTestPID+"/profile", "test-admin-key") }()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("profile did not answer in 3 s: the single-connection store is deadlocked")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"identity", "balances", "ban", "mute", "membership", "factions", "purchases", "ships", "ship_xp", "fleets",
		"career_claims", "ribbons", "counters", "results", "contracts", "contract_state", "contracts_legacy", "matches", "friends",
		"ignores", "chat", "reports", "telemetry", "notes", "owned", "audit"} {
		if _, ok := p[k]; !ok {
			t.Errorf("profile has no %q", k)
		}
	}
	var got struct {
		Identity struct {
			Name string `json:"name"`
		} `json:"identity"`
		Ban    struct{ Active bool } `json:"ban"`
		Mute   struct{ Active bool } `json:"mute"`
		Ships  []struct{}            `json:"ships"`
		Fleets []struct {
			Ships []struct{} `json:"ships"`
		} `json:"fleets"`
		Purchases []struct{ Name string } `json:"purchases"`
		Matches   []struct {
			MatchID string `json:"match_id"`
			Mode    string
		} `json:"matches"`
		Friends []struct{ Name string } `json:"friends"`
		Chat    []struct{ Text string } `json:"chat"`
		Notes   []struct{}              `json:"notes"`
		Audit   []struct{}              `json:"audit"`
		Owned   []struct{}              `json:"owned"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Identity.Name != "Pilot-Seed" || !got.Ban.Active || got.Mute.Active {
		t.Errorf("identity/ban/mute: %+v %+v %+v (the mute expired an hour ago)", got.Identity, got.Ban, got.Mute)
	}
	if len(got.Ships) == 0 || len(got.Fleets) == 0 || len(got.Fleets[0].Ships) == 0 || len(got.Owned) == 0 {
		t.Errorf("ships %d, fleets %d, owned %d: the seeded starter data is missing", len(got.Ships), len(got.Fleets), len(got.Owned))
	}
	if len(got.Purchases) != 1 || got.Purchases[0].Name == "" || len(got.Matches) != 1 || got.Matches[0].Mode != "TDM" ||
		len(got.Friends) != 1 || got.Friends[0].Name != "Pilot-Friend" || len(got.Chat) != 1 || len(got.Notes) != 1 || len(got.Audit) != 1 {
		t.Errorf("sections: %s", rec.Body.String())
	}
	if rec := adminGet(r, "/admin/api/players/ffffffffffffffffffffffffffffffff/profile", "test-admin-key"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown player: %d, want 404", rec.Code)
	}
}

func TestAdminPlayerNotesAddListDeleteAndAudit(t *testing.T) {
	database := adminProfileSeed(t)
	r := adminTestRouter()
	base := "/admin/api/players/" + adminTestPID + "/notes"
	rec := adminPost(r, base, "test-admin-key", `{"note":"  asked about a ship  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	var added struct {
		ID    int64 `json:"id"`
		Notes []struct {
			ID   int64
			Note string
		} `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || added.ID == 0 || len(added.Notes) != 2 || added.Notes[0].Note != "asked about a ship" {
		t.Fatalf("add reply: %v %s", err, rec.Body.String())
	}
	if rec := adminGet(r, base, "test-admin-key"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "asked about a ship") {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`{"note":"   "}`, `{"note":"` + strings.Repeat("x", adminNoteMaxLen+1) + `"}`, `nope`} {
		if rec := adminPost(r, base, "test-admin-key", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("bad note %.20q: %d, want 400", bad, rec.Code)
		}
	}
	// A note of another player cannot be removed through this player.
	var otherNote int64
	if _, err := database.Exec(`INSERT INTO admin_player_notes(user_id,note) VALUES(?,'theirs')`, adminProfileOtherPID); err != nil {
		t.Fatal(err)
	}
	_ = database.QueryRow(`SELECT id FROM admin_player_notes WHERE user_id=?`, adminProfileOtherPID).Scan(&otherNote)
	if rec := adminDelete(r, fmt.Sprintf("%s/%d", base, otherNote), "test-admin-key"); rec.Code != http.StatusNotFound {
		t.Errorf("deleting another player's note: %d, want 404", rec.Code)
	}
	if rec := adminDelete(r, base+"/abc", "test-admin-key"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id: %d, want 400", rec.Code)
	}
	if rec := adminDelete(r, fmt.Sprintf("%s/%d", base, added.ID), "test-admin-key"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	var left int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_player_notes WHERE id=?`, added.ID).Scan(&left)
	if left != 0 {
		t.Error("the note is still stored")
	}
	for _, action := range []string{"player note", "player note deleted"} {
		var n int
		_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action=? AND target=?`, action, adminTestPID).Scan(&n)
		if n != 1 {
			t.Errorf("audit rows %q = %d, want 1", action, n)
		}
	}
}

func TestAdminPlayerShipXPAddSetAndValidation(t *testing.T) {
	database := adminProfileSeed(t)
	var pawn int32
	var precast int32
	if err := database.QueryRow(`SELECT ship_id, precast_loadout_id FROM player_ship_loadouts WHERE user_id=? AND ship_id>0 LIMIT 1`, adminTestPID).Scan(&pawn, &precast); err != nil {
		t.Fatal(err)
	}
	squadHubInstance.connected(adminTestPID)
	t.Cleanup(func() { squadHubInstance.disconnected(adminTestPID) })
	_ = squadHubInstance.drainPushes(adminTestPID)
	r := adminTestRouter()
	path := "/admin/api/players/" + adminTestPID + "/shipxp"
	xp := func() int64 {
		var v int64
		_ = database.QueryRow(`SELECT xp FROM player_ship_xp WHERE user_id=? AND ship_id=?`, adminTestPID, pawn).Scan(&v)
		return v
	}
	post := func(body string) *httptest.ResponseRecorder { return adminPost(r, path, "test-admin-key", body) }

	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":500,"mode":"add"}`, pawn)); rec.Code != http.StatusOK || xp() != 500 {
		t.Fatalf("add by pawn id: %d %s, xp %d", rec.Code, rec.Body.String(), xp())
	}
	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":250,"mode":"add"}`, precast)); rec.Code != http.StatusOK || xp() != 750 {
		t.Fatalf("add by loadout id: %d %s, xp %d (the loadout id must resolve to the same pawn row)", rec.Code, rec.Body.String(), xp())
	}
	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":-9999,"mode":"add"}`, pawn)); rec.Code != http.StatusOK || xp() != 0 {
		t.Fatalf("subtract past zero: %d, xp %d, want 0", rec.Code, xp())
	}
	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":1000000000,"mode":"set"}`, pawn)); rec.Code != http.StatusOK || xp() != 1000000000 {
		t.Fatalf("set: %d, xp %d", rec.Code, xp())
	}
	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":1000000000,"mode":"add"}`, pawn)); rec.Code != http.StatusOK || xp() != 2000000000 {
		t.Fatalf("add near the limit: %d, xp %d", rec.Code, xp())
	}
	if rec := post(fmt.Sprintf(`{"ship_id":%d,"amount":1000000000,"mode":"add"}`, pawn)); rec.Code != http.StatusOK || xp() != 2147483647 {
		t.Fatalf("add past int32: %d, xp %d, want clamped to 2147483647", rec.Code, xp())
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"ship_id":%d,"amount":5,"mode":"steal"}`, pawn),
		fmt.Sprintf(`{"ship_id":%d,"amount":-5,"mode":"set"}`, pawn),
		fmt.Sprintf(`{"ship_id":%d,"amount":0,"mode":"add"}`, pawn),
		fmt.Sprintf(`{"ship_id":%d,"amount":10000000000,"mode":"add"}`, pawn),
		`{"ship_id":12345,"amount":5,"mode":"add"}`,
		fmt.Sprintf(`{"ship_id":%d,"amount":5,"mode":"add"}`, int32(10<<24|0x00ffff)),
		`not json`,
	} {
		if rec := post(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%.60s: %d, want 400", bad, rec.Code)
		}
	}
	if rec := adminPost(r, "/admin/api/players/ffffffffffffffffffffffffffffffff/shipxp", "test-admin-key", `{"ship_id":1,"amount":1,"mode":"add"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown player: %d, want 404", rec.Code)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='ship xp' AND target=?`, adminTestPID).Scan(&audits)
	if audits != 6 {
		t.Errorf("ship xp audit rows = %d, want 6 (one per accepted change, none for refusals)", audits)
	}
	saw := false
	for _, push := range squadHubInstance.drainPushes(adminTestPID) {
		saw = saw || strings.Contains(string(push), "YA_ConvertShipXP")
	}
	if !saw {
		t.Error("an online player got no YA_ConvertShipXP push")
	}
}

func TestAdminPlayersListFiltersAndPaging(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	now := time.Now().UTC()
	sqlTime := func(d time.Duration) string { return now.Add(d).Format("2006-01-02 15:04:05") }
	pids := make([]string, 6)
	for i := range pids {
		pids[i] = fmt.Sprintf("a1%030d", i+1)
		if err := seedMmogPlayerState(database, pids[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE player_state SET display_name=?, current_rank=?, created_at=?, updated_at=? WHERE user_id=?`,
			fmt.Sprintf("Pilot-%02d", i+1), 10-i, sqlTime(-time.Duration(i)*time.Hour), sqlTime(-time.Duration(5-i)*time.Hour), pids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name='Odd_100%' WHERE user_id=?`, pids[5]); err != nil {
		t.Fatal(err)
	}
	// 0: permanent ban, 1: expired ban, 2: muted.
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO admin_bans(user_id,reason,expires_at) VALUES('%s','x','')`, pids[0]),
		fmt.Sprintf(`INSERT INTO admin_bans(user_id,reason,expires_at) VALUES('%s','x','%s')`, pids[1], sqlTime(-time.Hour)),
		fmt.Sprintf(`INSERT INTO admin_chat_mutes(user_id,reason,expires_at) VALUES('%s','x','')`, pids[2]),
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r := adminTestRouter()
	type list struct {
		Total   int `json:"total"`
		Players []struct {
			PID    string `json:"pid"`
			Name   string `json:"name"`
			Banned bool   `json:"banned"`
			Muted  bool   `json:"muted"`
			Online bool   `json:"online"`
		} `json:"players"`
	}
	get := func(query string) list {
		t.Helper()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- adminGet(r, "/admin/api/players"+query, "test-admin-key") }()
		var rec *httptest.ResponseRecorder
		select {
		case rec = <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer in 3 s: deadlock", query)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var l list
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := get(""); l.Total != 6 || len(l.Players) != 6 || l.Players[0].Name != "Pilot-06" && l.Players[0].Name != "Odd_100%" {
		t.Errorf("default (last seen, newest first): %+v", l)
	}
	if l := get("?sort=name"); l.Players[0].Name != "Odd_100%" || l.Players[1].Name != "Pilot-01" {
		t.Errorf("sort=name: %+v", l.Players)
	}
	if l := get("?sort=rank"); l.Players[0].Name != "Pilot-01" {
		t.Errorf("sort=rank: %+v", l.Players)
	}
	if l := get("?sort=created"); l.Players[0].Name != "Pilot-01" {
		t.Errorf("sort=created: %+v", l.Players)
	}
	if l := get("?q=pilot-0"); l.Total != 5 {
		t.Errorf("q by name part: %+v", l)
	}
	if l := get("?q=100%25"); l.Total != 1 || l.Players[0].Name != "Odd_100%" {
		t.Errorf("a LIKE wildcard in q must be literal: %+v", l)
	}
	if l := get("?q=a10000000000000000000000000000000"); l.Total != 0 {
		t.Errorf("q by id prefix that matches nothing: %+v", l)
	}
	if l := get("?q=a100000"); l.Total != 6 {
		t.Errorf("q by id prefix: %+v", l)
	}
	if l := get("?banned=1"); l.Total != 1 || !l.Players[0].Banned {
		t.Errorf("banned=1 must list only the unexpired ban: %+v", l)
	}
	if l := get("?online=1"); l.Total != 0 || len(l.Players) != 0 {
		t.Errorf("online=1 with nobody online: %+v", l)
	}
	if l := get(""); !l.Players[0].Muted && !l.Players[1].Muted && !l.Players[2].Muted && !l.Players[3].Muted && !l.Players[4].Muted && !l.Players[5].Muted {
		t.Error("no row carries muted")
	}
	page := get("?sort=name&limit=2&offset=2")
	if page.Total != 6 || len(page.Players) != 2 || page.Players[0].Name != "Pilot-02" {
		t.Errorf("paging: %+v", page)
	}
	if l := get("?limit=100000"); len(l.Players) != 6 {
		t.Errorf("limit above the cap: %+v", l)
	}
	if rec := adminGet(r, "/admin/api/players?sort=name;drop", "test-admin-key"); rec.Code != http.StatusOK {
		t.Errorf("unknown sort falls back: %d", rec.Code)
	}
}
