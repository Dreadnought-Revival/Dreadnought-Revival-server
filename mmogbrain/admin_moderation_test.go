package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

func resetAdminModeration(t *testing.T) {
	t.Helper()
	reset := func() {
		adminModeration.mu.Lock()
		adminModeration.loaded = false
		adminModeration.banned = map[string]bool{}
		adminModeration.until = map[string]time.Time{}
		adminModeration.kicked = map[string]time.Time{}
		adminModeration.mu.Unlock()
		adminMutes.mu.Lock()
		adminMutes.loaded = false
		adminMutes.until = map[string]time.Time{}
		adminMutes.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

type adminBanDetail struct {
	Banned    bool   `json:"banned"`
	BanReason string `json:"ban_reason"`
}

// A ban is enforced by mmogbrain itself (the launcher JWT outlives it), is
// recorded in the audit log, and an unban lifts it.
func TestAdminBanAndUnban(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	resetAdminModeration(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	r := adminTestRouter()
	base := "/admin/api/players/" + adminTestPID

	if rec := adminPost(r, base+"/ban", "test-admin-key", `{"reason":""}`); rec.Code != 400 {
		t.Errorf("ban without a reason: %d, want 400", rec.Code)
	}
	if rec := adminPost(r, base+"/ban", "wrong", `{"reason":"x"}`); rec.Code != 403 {
		t.Errorf("ban with a wrong key: %d, want 403", rec.Code)
	}
	rec := adminPost(r, base+"/ban", "test-admin-key", `{"reason":"cheating"}`)
	var d adminBanDetail
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || !d.Banned || d.BanReason != "cheating" {
		t.Fatalf("ban: %d %s", rec.Code, rec.Body.String())
	}
	// Dashed form too: the gateway sees the account id with dashes.
	dashed := adminTestPID[:8] + "-" + adminTestPID[8:12] + "-" + adminTestPID[12:16] + "-" + adminTestPID[16:20] + "-" + adminTestPID[20:]
	if !adminPlayerBanned(adminTestPID) || !adminPlayerBanned(dashed) {
		t.Fatal("banned player not refused")
	}
	if !adminConnectionRevoked(adminTestPID, time.Now()) {
		t.Fatal("a banned player's game connection is not dropped")
	}
	// The ban survives a restart (loaded from admin_bans).
	adminModeration.mu.Lock()
	adminModeration.loaded, adminModeration.banned = false, map[string]bool{}
	adminModeration.mu.Unlock()
	if !adminPlayerBanned(adminTestPID) {
		t.Fatal("ban lost after reloading")
	}

	rec = adminPost(r, base+"/unban", "test-admin-key", `{}`)
	d = adminBanDetail{}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || d.Banned {
		t.Fatalf("unban: %d %s", rec.Code, rec.Body.String())
	}
	if adminPlayerBanned(adminTestPID) {
		t.Fatal("still banned after unban")
	}

	var audit struct {
		Audit []adminAuditRow `json:"audit"`
	}
	rec = adminGet(r, base+"/audit", "test-admin-key")
	if err := json.Unmarshal(rec.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Audit) != 2 || audit.Audit[0].Action != "unban" || audit.Audit[1].Action != "ban" ||
		!strings.Contains(audit.Audit[1].Details, "cheating") {
		t.Fatalf("audit: %+v", audit.Audit)
	}
}

// A kick closes the social socket now and drops game connections that were
// open before it, but not one opened afterwards (a kick is not a ban).
func TestAdminKick(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	resetAdminModeration(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	peer := newSocialPeer(adminTestPID, "peer", server)
	socialHubInstance.mu.Lock()
	socialHubInstance.peers[adminTestPID] = peer
	socialHubInstance.mu.Unlock()
	t.Cleanup(func() {
		socialHubInstance.mu.Lock()
		delete(socialHubInstance.peers, adminTestPID)
		socialHubInstance.mu.Unlock()
	})
	opened := time.Now().Add(-time.Minute)

	rec := adminPost(adminTestRouter(), "/admin/api/players/"+adminTestPID+"/kick", "test-admin-key", `{"reason":"afk"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"was_online":true`) {
		t.Fatalf("kick: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Error("the social socket is still open")
	}
	socialHubInstance.mu.RLock()
	_, still := socialHubInstance.peers[adminTestPID]
	socialHubInstance.mu.RUnlock()
	if still {
		t.Error("the player is still listed online")
	}
	if !adminConnectionRevoked(adminTestPID, opened) {
		t.Error("a game connection from before the kick is not dropped")
	}
	if adminConnectionRevoked(adminTestPID, time.Now().Add(time.Second)) {
		t.Error("a reconnect after the kick is dropped too")
	}
	if adminPlayerBanned(adminTestPID) {
		t.Error("a kick banned the player")
	}
}

func TestAdminPlayerMatchHistory(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO matches(id,battle_match_id,game_mode,map,status,fleet_type) VALUES('m1','bm1','TDM','/Game/Maps/MP/Amirani/MP_Amirani_P','ended',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO battle_results(match_id,user_id,outcome,kills,credits,fleet_type) VALUES('bm1',?,'win',7,1234,2)`, adminTestPID); err != nil {
		t.Fatal(err)
	}
	rec := adminGet(adminTestRouter(), "/admin/api/players/"+adminTestPID+"/matches", "test-admin-key")
	var body struct {
		Matches []struct {
			Mode, Map, Fleet, Outcome string
			Kills                     int
			Credits                   int64
		} `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Matches) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	m := body.Matches[0]
	if m.Mode != "TDM" || m.Map != "Amirani" || m.Fleet != "Veteran" || m.Outcome != "win" || m.Kills != 7 || m.Credits != 1234 {
		t.Fatalf("history row: %+v", m)
	}
}

func adminModSeed(t *testing.T) (*sql.DB, http.Handler) {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	resetAdminModeration(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	return database, adminTestRouter()
}

func adminAuditCount(t *testing.T, database *sql.DB, action string) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action=?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A timed ban stores its end, lapses by itself even before the sweeper runs,
// and the sweeper then removes it through the manual-unban path with a system
// audit row.
func TestAdminTimedBanExpires(t *testing.T) {
	database, r := adminModSeed(t)
	base := "/admin/api/players/" + adminTestPID
	if rec := adminPost(r, base+"/ban", "test-admin-key", `{"reason":"x","hours":9000}`); rec.Code != 400 {
		t.Errorf("9000 h ban: %d, want 400", rec.Code)
	}
	if rec := adminPost(r, base+"/ban", "test-admin-key", `{"reason":"x","hours":-1}`); rec.Code != 400 {
		t.Errorf("negative hours: %d, want 400", rec.Code)
	}
	if rec := adminPost(r, base+"/ban", "test-admin-key", `{"reason":"spam","hours":2}`); rec.Code != 200 {
		t.Fatalf("timed ban: %d %s", rec.Code, rec.Body.String())
	}
	var exp, details string
	_ = database.QueryRow(`SELECT expires_at FROM admin_bans WHERE user_id=?`, adminTestPID).Scan(&exp)
	end, ok := adminParseExpiry(exp)
	if !ok || end.Before(time.Now().Add(119*time.Minute)) || end.After(time.Now().Add(121*time.Minute)) {
		t.Fatalf("expires_at = %q, want about now+2h", exp)
	}
	_ = database.QueryRow(`SELECT details FROM admin_audit WHERE action='ban'`).Scan(&details)
	if !strings.Contains(details, `"hours":2`) || !strings.Contains(details, "expires_at") {
		t.Errorf("ban audit lacks hours/expires_at: %s", details)
	}
	if !adminPlayerBanned(adminTestPID) {
		t.Fatal("timed ban not enforced")
	}
	rec := adminGet(r, "/admin/api/bans", "test-admin-key")
	if !strings.Contains(rec.Body.String(), exp) {
		t.Errorf("bans list lacks expires_at: %s", rec.Body.String())
	}

	// Past its end: not banned at once, the row waits for the sweeper.
	past := time.Now().UTC().Add(-time.Minute).Format(adminExpiryLayout)
	if _, err := database.Exec(`UPDATE admin_bans SET expires_at=?`, past); err != nil {
		t.Fatal(err)
	}
	resetAdminModeration(t) // reload from the database
	if adminPlayerBanned(adminTestPID) || adminConnectionRevoked(adminTestPID, time.Now()) {
		t.Fatal("an expired ban still holds")
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_bans`).Scan(&n)
	if n != 1 {
		t.Fatal("the row should wait for the sweeper")
	}
	adminSweepExpired(time.Now())
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_bans`).Scan(&n)
	if n != 0 {
		t.Fatal("the sweeper did not remove the expired ban")
	}
	var actor, target string
	if err := database.QueryRow(`SELECT actor, target FROM admin_audit WHERE action='ban expired'`).Scan(&actor, &target); err != nil || actor != "server" || target != adminTestPID {
		t.Errorf("system audit row: actor=%q target=%q err=%v", actor, target, err)
	}

	// A permanent ban is never swept.
	adminPost(r, base+"/ban", "test-admin-key", `{"reason":"cheat"}`)
	adminSweepExpired(time.Now().Add(1000 * time.Hour))
	if !adminPlayerBanned(adminTestPID) {
		t.Error("a permanent ban was swept")
	}
}

// An expired ban is lifted at the auth server FIRST: while it does not
// answer, the row stays and the next sweep retries (deleting it first would
// leave the launcher ban with nothing to lift it).
func TestAdminBanExpiryRetriesTheAuthServer(t *testing.T) {
	database, r := adminModSeed(t)
	authUp := false
	unbans := 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/admin/unban" {
			unbans++
		}
		if !authUp {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer auth.Close()
	old := adminDash.authURL
	adminDash.authURL = auth.URL
	defer func() { adminDash.authURL = old }()

	authUp = true // the ban itself goes through
	if rec := adminPost(r, "/admin/api/players/"+adminTestPID+"/ban", "test-admin-key", `{"reason":"spam","hours":1}`); rec.Code != 200 {
		t.Fatalf("ban: %d %s", rec.Code, rec.Body.String())
	}
	authUp = false
	past := time.Now().UTC().Add(-time.Minute).Format(adminExpiryLayout)
	if _, err := database.Exec(`UPDATE admin_bans SET expires_at=?`, past); err != nil {
		t.Fatal(err)
	}
	count := func() (n int) { _ = database.QueryRow(`SELECT COUNT(*) FROM admin_bans`).Scan(&n); return }
	adminSweepExpired(time.Now())
	if count() != 1 || unbans != 1 {
		t.Fatalf("auth down: rows=%d unban calls=%d, want the row kept after one try", count(), unbans)
	}
	if adminAuditCount(t, database, "ban expired") != 0 {
		t.Error("audited an expiry that did not happen")
	}
	authUp = true
	adminSweepExpired(time.Now())
	if count() != 0 || unbans != 2 {
		t.Fatalf("auth back: rows=%d unban calls=%d, want the row gone after the retry", count(), unbans)
	}
	if adminAuditCount(t, database, "ban expired") != 1 {
		t.Error("no system audit row for the expiry")
	}
}

func TestAdminMuteRoutesAndGuard(t *testing.T) {
	database, r := adminModSeed(t)
	base := "/admin/api/players/" + adminTestPID
	for _, path := range []string{base + "/mute", base + "/unmute"} {
		if rec := adminPost(r, path, "", `{"reason":"x"}`); rec.Code != 403 {
			t.Errorf("%s without key: %d", path, rec.Code)
		}
		if rec := adminPost(r, path, "wrong", `{"reason":"x"}`); rec.Code != 403 {
			t.Errorf("%s wrong key: %d", path, rec.Code)
		}
	}
	if rec := adminGet(r, "/admin/api/mutes", "wrong"); rec.Code != 403 {
		t.Errorf("mutes wrong key: %d", rec.Code)
	}
	if rec := adminPost(r, base+"/mute", "test-admin-key", `{"reason":""}`); rec.Code != 400 {
		t.Errorf("mute without reason: %d", rec.Code)
	}
	if rec := adminPost(r, base+"/mute", "test-admin-key", `{"reason":"x","hours":-5}`); rec.Code != 400 {
		t.Errorf("negative hours: %d", rec.Code)
	}
	if adminChatMuted(adminTestPID) {
		t.Fatal("muted before any mute")
	}
	peer := &socialPeer{playerID: adminTestPID}
	if why := chatLineRefused(peer, "hello"); why != "" {
		t.Fatalf("unmuted line refused: %q", why)
	}
	if rec := adminPost(r, base+"/mute", "test-admin-key", `{"reason":"spam","hours":1}`); rec.Code != 200 {
		t.Fatalf("mute: %d %s", rec.Code, rec.Body.String())
	}
	if why := chatLineRefused(peer, "hello"); why != "muted" {
		t.Errorf("muted line = %q, want muted", why)
	}
	// The binary MMOG chat path relays by storing: a muted line is not stored.
	line := protocol.AppendStringField(protocol.AppendStringField(nil, "channelName", "global"), "message", "muted words")
	buildMmogChatPayload("YA_Chat", adminTestPID, line)
	var stored int
	if err := database.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE content='muted words'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("binary chat path stored %d muted line(s)", stored)
	}
	dashed := adminTestPID[:8] + "-" + adminTestPID[8:12] + "-" + adminTestPID[12:16] + "-" + adminTestPID[16:20] + "-" + adminTestPID[20:]
	if !adminChatMuted(dashed) {
		t.Error("dashed id form not muted")
	}
	if rec := adminGet(r, "/admin/api/mutes", "test-admin-key"); rec.Code != 200 || !strings.Contains(rec.Body.String(), adminTestPID) {
		t.Errorf("mutes list: %d %s", rec.Code, rec.Body.String())
	}
	// Survives a reload; expires on read, then the sweeper clears it.
	resetAdminModeration(t)
	if !adminChatMuted(adminTestPID) {
		t.Fatal("mute lost after reload")
	}
	past := time.Now().UTC().Add(-time.Minute).Format(adminExpiryLayout)
	if _, err := database.Exec(`UPDATE admin_chat_mutes SET expires_at=?`, past); err != nil {
		t.Fatal(err)
	}
	resetAdminModeration(t)
	if adminChatMuted(adminTestPID) {
		t.Fatal("expired mute still holds")
	}
	adminSweepExpired(time.Now())
	if adminAuditCount(t, database, "mute expired") != 1 {
		t.Error("no system audit row for the expired mute")
	}
	// Manual unmute.
	adminPost(r, base+"/mute", "test-admin-key", `{"reason":"again"}`)
	if !adminChatMuted(adminTestPID) {
		t.Fatal("permanent mute not applied")
	}
	if rec := adminPost(r, base+"/unmute", "test-admin-key", `{"reason":"sorry"}`); rec.Code != 200 {
		t.Fatalf("unmute: %d", rec.Code)
	}
	if adminChatMuted(adminTestPID) {
		t.Error("still muted after unmute")
	}
	if adminAuditCount(t, database, "mute") != 2 || adminAuditCount(t, database, "unmute") != 1 {
		t.Error("mute/unmute audit rows missing")
	}
	if rec := adminPost(r, "/admin/api/players/"+strings.Repeat("0", 32)+"/mute", "test-admin-key", `{"reason":"x"}`); rec.Code != 404 {
		t.Errorf("mute of an unknown player: %d", rec.Code)
	}
}

func TestAdminAuditFilters(t *testing.T) {
	database, r := adminModSeed(t)
	_, _ = database.Exec(`UPDATE player_state SET display_name='Pilot-Test' WHERE user_id=?`, adminTestPID)
	for i := 0; i < 6; i++ {
		target := adminTestPID
		if i%2 == 1 {
			target = ""
		}
		at := fmt.Sprintf("2026-10-0%d 12:00:00", i+1)
		if _, err := database.Exec(`INSERT INTO admin_audit(created_at,actor,action,target,details) VALUES(?,?,?,?,?)`,
			at, fmt.Sprintf("10.0.0.%d", i), []string{"kick", "ban", "mute"}[i%3], target, fmt.Sprintf(`{"reason":"r%d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	get := func(q string) (rows []adminAuditRow, more bool) {
		t.Helper()
		var out struct {
			Audit   []adminAuditRow `json:"audit"`
			More    bool            `json:"more"`
			Actions []string        `json:"actions"`
		}
		rec := adminGet(r, "/admin/api/audit"+q, "test-admin-key")
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("audit%s: %d %s", q, rec.Code, rec.Body.String())
		}
		return out.Audit, out.More
	}
	if rows, _ := get(""); len(rows) != 6 {
		t.Errorf("all = %d", len(rows))
	}
	if rows, _ := get("?action=BAN"); len(rows) != 2 {
		t.Errorf("action=BAN = %d, want 2", len(rows))
	}
	if rows, _ := get("?q=10.0.0.4"); len(rows) != 1 || rows[0].Actor != "10.0.0.4" {
		t.Errorf("q by actor = %v", rows)
	}
	if rows, _ := get("?q=r3"); len(rows) != 1 {
		t.Errorf("q by details = %d", len(rows))
	}
	if rows, _ := get("?target=" + adminTestPID); len(rows) != 3 || rows[0].Name == "" {
		t.Errorf("target = %d", len(rows))
	}
	if rows, _ := get("?since=2026-10-02&until=2026-10-04"); len(rows) != 2 {
		t.Errorf("date range = %d, want 2", len(rows))
	}
	rows, more := get("?limit=4")
	if len(rows) != 4 || !more {
		t.Fatalf("page 1: %d more=%v", len(rows), more)
	}
	rows2, more2 := get(fmt.Sprintf("?limit=4&before=%d", rows[3].ID))
	if len(rows2) != 2 || more2 {
		t.Errorf("page 2: %d more=%v", len(rows2), more2)
	}
	for _, bad := range []string{"?limit=9999", "?before=x", "?target=zz", "?since=yesterday"} {
		if rec := adminGet(r, "/admin/api/audit"+bad, "test-admin-key"); rec.Code != 400 {
			t.Errorf("audit%s: %d, want 400", bad, rec.Code)
		}
	}
}

func TestAdminModerationRoutesDoNotDeadlock(t *testing.T) {
	database, r := adminModSeed(t)
	_, _ = database.Exec(`INSERT INTO admin_bans(user_id,reason,expires_at) VALUES(?,?,?)`, adminTestPID, "r", "")
	_, _ = database.Exec(`INSERT INTO admin_chat_mutes(user_id,reason) VALUES(?,?)`, adminTestPID, "r")
	for _, path := range []string{"/admin/api/bans", "/admin/api/mutes", "/admin/api/audit?q=a&limit=5"} {
		done := make(chan int, 1)
		go func() { done <- adminGet(r, path, "test-admin-key").Code }()
		select {
		case code := <-done:
			if code != http.StatusOK {
				t.Errorf("%s: %d", path, code)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not answer within 3 s", path)
		}
	}
	done := make(chan struct{})
	go func() { adminSweepExpired(time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweeper deadlocked")
	}
}
