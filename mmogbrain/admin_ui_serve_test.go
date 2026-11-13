package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/shared/dreadgameconfig"
)

// TestAdminUIServe is not a test: it puts the dashboard on a local port so a
// page can be looked at in a browser while it is being written, against a
// temporary database of made-up players. It does nothing unless asked:
//
//	ADMIN_UI_SERVE=127.0.0.1:18090 go test -run TestAdminUIServe -count=1 -timeout 30m .
//
//	ADMIN_UI_SERVE_SECONDS  how long to stay up (default 600)
//	ADMIN_UI_SEED_SQL       a file of extra SQL to run after the seed
//
// The admin key is "test-admin-key". GET /shot?page=<id>[&arg=..][&theme=dark]
// signs in and opens that page, for a headless screenshot.
//
// Nothing here touches the live stack: the control plane and the auth server
// point nowhere, and the working directory moves to an empty temp folder with
// two invented log files, so no real log or player ever reaches a screenshot.
func TestAdminUIServe(t *testing.T) {
	addr := os.Getenv("ADMIN_UI_SERVE")
	if addr == "" {
		t.Skip("set ADMIN_UI_SERVE=127.0.0.1:<port> to serve the dashboard")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("ADMIN_UI_SERVE must be a 127.0.0.1 address, got %q", addr)
	}
	if abs, err := filepath.Abs(dreadgameconfig.DataDir()); err == nil {
		t.Setenv("DATA_DIR", abs)
	}
	seedSQL := ""
	if p := os.Getenv("ADMIN_UI_SEED_SQL"); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ADMIN_UI_SEED_SQL: %v", err)
		}
		seedSQL = string(raw)
	}
	work := t.TempDir()
	adminUIServeFakeLogs(t, work)
	t.Chdir(work)

	database := useTempMmogPlayerStateDB(t)
	adminUIServeSeed(t, database)
	if seedSQL != "" {
		if _, err := database.Exec(seedSQL); err != nil {
			t.Fatalf("ADMIN_UI_SEED_SQL: %v", err)
		}
	}

	r := adminTestRouter()
	r.HandleFunc("/shot", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		hash := "#/" + q.Get("page")
		if a := q.Get("arg"); a != "" {
			hash += "/" + a
		}
		theme := "light"
		if q.Get("theme") == "dark" {
			theme = "dark"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><script>sessionStorage.setItem('adminKey','test-admin-key');localStorage.setItem('adminTheme',%s);location.replace('/admin/dashboard'+%s);</script>`,
			strconv.Quote(theme), strconv.Quote(hash))
	})
	srv := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	defer func() { _ = srv.Close() }()

	seconds, _ := strconv.Atoi(os.Getenv("ADMIN_UI_SERVE_SECONDS"))
	if seconds <= 0 {
		seconds = 600
	}
	t.Logf("dashboard on http://%s/shot?page=overview for %d s", addr, seconds)
	time.Sleep(time.Duration(seconds) * time.Second)
}

// adminUIServePIDs are the made-up accounts ("Pilot-01" …).
func adminUIServePIDs() []string {
	pids := make([]string, 14)
	for i := range pids {
		pids[i] = fmt.Sprintf("a1%030d", i+1)
	}
	return pids
}

func adminUIServeFakeLogs(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "run", "battle-logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, `{"level":"info","msg":"sample log line %d","time":"2026-01-01T00:%02d:00Z"}`+"\n", i, i)
	}
	_ = os.WriteFile(filepath.Join(dir, "run", "mmogbrain.log"), []byte(b.String()), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "run", "battle-logs", "battle-sample-port7900.log"),
		[]byte("LogInit: sample battle server log\nLogWorld: Bringing World up for play\nLogExit: Exiting.\n"), 0o600)
}

func adminUIServeSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	now := time.Now().UTC()
	sqlTime := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02 15:04:05") }
	rfcTime := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	pids := adminUIServePIDs()
	for i, pid := range pids {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatalf("seed player: %v", err)
		}
		exec(`UPDATE player_state SET display_name=?, soft_currency=?, premium_currency=?, free_xp=?, current_rank=?, current_xp=?, created_at=?, login_streak=? WHERE user_id=?`,
			fmt.Sprintf("Pilot-%02d", i+1), 12000+i*7300, 400+i*650, 900*i, 1+i*3, 4000*i, sqlTime(time.Duration(i*20)*time.Hour), i%6, pid)
	}

	modes := []struct{ mode, m string }{{"TDM", "Gorge"}, {"TDM", "Space"}, {"TER", "Skybridge"}, {"Onslaught", "Amirani"}, {"TE", "Glacier"},
		{"BC", "Derelict"}, {"PodTDM", "Space02"}, {"TurboTDM", "Glacier"}, {"TDM", "Highlands"}, {"Havoc", "Elysium"}}
	for m := 0; m < 36; m++ {
		mode := modes[m%len(modes)]
		age := time.Duration(m*4+1) * time.Hour
		id, bid := fmt.Sprintf("m1%030d", m), fmt.Sprintf("b1%030d", m)
		status, ended := "ended", any(rfcTime(age-14*time.Minute))
		if m == 0 {
			status, ended = "active", nil
			age = 6 * time.Minute
		}
		exec(`INSERT INTO matches (id, game_mode, map, server_ip, server_port, status, created_at, started_at, ended_at, instance_id, battle_match_id, fleet_type)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, mode.mode, mode.m, "127.0.0.1", 7900+m%4, status, rfcTime(age), rfcTime(age-time.Minute), ended, fmt.Sprintf("inst-%d", m), bid, 1+m%3)
		if m == 0 {
			continue
		}
		for s := 0; s < 4+m%5; s++ {
			pid := pids[(m+s*3)%len(pids)]
			team := s % 2
			outcome := "loss"
			if team == m%2 {
				outcome = "win"
			}
			exec(`INSERT OR IGNORE INTO battle_results (match_id, user_id, team, outcome, kills, deaths, assists, damage, credits, xp, created_at, fleet_type)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, bid, pid, team, outcome, (m+s)%9, (m*s)%6, (m+2*s)%7, 20000+3100*s+900*m, 800+90*s, 1500+120*s, sqlTime(age-14*time.Minute), 1+m%3)
			for _, st := range []struct {
				name string
				v    int
			}{{"score", 900 + 140*s + 30*m}, {"double_kills", s % 3}, {"healing", 400 * (s % 2)}, {"time_Destroyer", 300 + 20*s}, {"time_Dreadnought", 200 + 10*m}} {
				exec(`INSERT OR IGNORE INTO battle_result_stats (match_id, user_id, stat, value) VALUES (?,?,?,?)`, bid, pid, st.name, st.v)
			}
			if mode.mode == "Havoc" {
				exec(`INSERT OR IGNORE INTO havoc_runs (match_id, user_id, score, waves, created_at) VALUES (?,?,?,?,?)`, bid, pid, 5200+310*s+40*m, 6+s, sqlTime(age))
			}
		}
	}
	for i := 0; i < 48; i++ {
		ch := "dreadnought.global"
		if i%7 == 3 {
			ch = "english.language"
		}
		exec(`INSERT INTO chat_messages (id, channel, sender_id, content, sent_at) VALUES (?,?,?,?,?)`,
			fmt.Sprintf("c%d", i), ch, pids[i%len(pids)], fmt.Sprintf("sample chat line %d: anyone up for a match?", i), sqlTime(time.Duration(i*17)*time.Minute))
	}
	for i := 0; i < 14; i++ {
		typ, name := "GameplayLogicFailure", "Ship did not spawn"
		if i%3 == 0 {
			typ, name = "NetworkFailureClient", "ConnectionLost"
		}
		exec(`INSERT INTO client_reports (user_id, type, name, desc, created_at) VALUES (?,?,?,?,?)`, pids[i%len(pids)], typ, name,
			fmt.Sprintf("sample report text %d", i), sqlTime(time.Duration(i*3)*time.Hour))
	}
	for i := 0; i < 30; i++ {
		rt := []string{"YA_AnalyticsEvent", "YA_GameModeEvent", "YA_AnalyticsReceiveCreditsEvent", "YA_AnalyticsInvestEvent"}[i%4]
		exec(`INSERT INTO client_telemetry (user_id, rt, battle_id, summary, fields, created_at) VALUES (?,?,?,?,?,?)`, pids[i%len(pids)], rt,
			fmt.Sprintf("b1%030d", i%9+1), fmt.Sprintf("sample event %d", i), `[{"n":"Amount","v":"120"}]`, sqlTime(time.Duration(i*40)*time.Minute))
	}
	for i, pid := range pids {
		exec(`INSERT OR IGNORE INTO player_ribbons (user_id, ribbon_type, count) VALUES (?,?,?)`, pid, "Dominator", 3+i)
		exec(`INSERT OR IGNORE INTO player_ribbons (user_id, ribbon_type, count) VALUES (?,?,?)`, pid, "FirstStrike", 1+i%4)
		exec(`INSERT OR IGNORE INTO player_stats_counters (user_id, counter_id, counter_sub_id, value) VALUES (?,?,?,?)`, pid, "Kills", "", 40+11*i)
		exec(`INSERT OR IGNORE INTO player_career_claims (user_id, goal_id, claimed_stages) VALUES (?,?,?)`, pid, "SampleGoal", 1+i%3)
		exec(`INSERT INTO player_daily_contracts (user_id, slot, quest_id, difficulty, target, progress, reward, state, assigned_at, completed_at, paid, gp_paid)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, pid, 0, "MPQ_Sample", 1, 10, 10, 2000, "acknowledged", now.Add(-30*time.Hour).Unix(), now.Add(-20*time.Hour).Unix(), 1, 200)
		exec(`INSERT INTO player_daily_contracts (user_id, slot, quest_id, difficulty, target, progress, reward, state, assigned_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, pid, 1, "MPQ_Sample", 2, 25, 4+i, 3500, "active", now.Add(-5*time.Hour).Unix())
		if i > 0 {
			exec(`INSERT OR IGNORE INTO player_friends (pid_a, pid_b, requester_id, state) VALUES (?,?,?,?)`, pids[0], pid, pids[0], map[bool]string{true: "accepted", false: "pending"}[i%2 == 0])
		}
	}
	for i := 0; i < 3; i++ {
		exec(`INSERT INTO queue_entries (id, user_id, game_mode, queued_at, status, fleet_type, party_id) VALUES (?,?,?,?,?,?,?)`,
			fmt.Sprintf("q%d", i), pids[10+i], []string{"TDM", "TDM", "Onslaught"}[i], rfcTime(time.Duration(40+70*i)*time.Second), "waiting", 1+i%2, map[bool]string{true: "party-1", false: ""}[i < 2])
	}
	for i := 0; i < 600; i++ {
		online := 6 + (i*7)%9 + (i/40)%5
		exec(`INSERT OR IGNORE INTO admin_metrics_samples (ts, online, in_match, queued, instances) VALUES (?,?,?,?,?)`,
			now.Add(-time.Duration(i*5)*time.Minute).Truncate(5*time.Minute).Format("2006-01-02 15:04:05"), online, online/2, i%3, 1+i%3)
	}
	exec(`INSERT INTO admin_bans (user_id, reason, created_at, expires_at) VALUES (?,?,?,?)`, pids[13], "sample ban", sqlTime(3*time.Hour), sqlTime(-45*time.Hour))
	exec(`INSERT INTO admin_chat_mutes (user_id, reason, created_at, expires_at) VALUES (?,?,?,?)`, pids[12], "sample mute", sqlTime(time.Hour), sqlTime(-2*time.Hour))
	exec(`INSERT INTO admin_player_notes (user_id, note, actor, created_at) VALUES (?,?,?,?)`, pids[0], "Sample note: asked about a missing ship.", "127.0.0.1", sqlTime(26*time.Hour))
	for i, a := range []struct{ action, target, details string }{
		{"balances", pids[0], `{"credits":{"from":12000,"to":15000}}`},
		{"ban", pids[13], `{"reason":"sample ban"}`},
		{"setting", "contract_gp_reward", `{"from":200,"to":250}`},
		{"stop instance", "inst-3", `{}`},
		{"game modes", "", `{"TDM":{"from":"on","to":"off"}}`},
	} {
		exec(`INSERT INTO admin_audit (created_at, actor, action, target, details) VALUES (?,?,?,?,?)`, sqlTime(time.Duration(i*5+1)*time.Hour), "127.0.0.1", a.action, a.target, a.details)
	}
}
