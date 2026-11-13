package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// Moderation for the admin dashboard: kick, ban/unban, a player's match
// history, and the audit log of every admin action.
//
// Bans are enforced HERE, not only by the auth server. The auth server's ban
// stops new launcher sign-ins, but mmogbrain checks a launcher JWT on its own
// (stateless, valid 24 h), so a player who signed in before the ban could keep
// playing and reconnect all day. mmogbrain keeps its own list (admin_bans),
// refuses banned players at the gateway login and the Firmament handshake,
// and drops their live connections. The auth server is told as well, so the
// launcher refuses them too.

var errAdminDisconnected = errors.New("disconnected by an admin")

// adminModeration is the in-memory side of admin_bans plus recent kicks. The
// handshake paths read it without touching the database: mmogbrain's SQLite
// has ONE connection, and a lookup from inside someone else's open rows loop
// would deadlock the server.
var adminModeration = struct {
	mu     sync.Mutex
	loaded bool
	banned map[string]bool
	// until holds the end of a TIMED ban (admin_bans.expires_at); a banned
	// player without an entry is banned for good. An expired entry counts as
	// not banned at once; the sweeper (admin_ban_expiry.go) then removes the row.
	until  map[string]time.Time
	kicked map[string]time.Time // pid -> when; drops connections older than this
}{banned: map[string]bool{}, until: map[string]time.Time{}, kicked: map[string]time.Time{}}

// adminBanActiveLocked: banned and not past a timed ban's end. mu must be held.
func adminBanActiveLocked(pid string) bool {
	if !adminModeration.banned[pid] {
		return false
	}
	if end, ok := adminModeration.until[pid]; ok && !time.Now().Before(end) {
		return false
	}
	return true
}

func adminLoadBans() {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return
	}
	adminModeration.mu.Lock()
	loaded := adminModeration.loaded
	adminModeration.mu.Unlock()
	if loaded {
		return
	}
	set := map[string]bool{}
	until := map[string]time.Time{}
	rows, err := database.Query(`SELECT user_id, expires_at FROM admin_bans`)
	if err != nil {
		return
	}
	for rows.Next() {
		var pid, exp string
		if rows.Scan(&pid, &exp) == nil {
			set[pid] = true
			if end, ok := adminParseExpiry(exp); ok {
				until[pid] = end
			}
		}
	}
	_ = rows.Close()
	adminModeration.mu.Lock()
	if !adminModeration.loaded {
		adminModeration.banned = set
		adminModeration.until = until
		adminModeration.loaded = true
	}
	adminModeration.mu.Unlock()
}

// adminPlayerBanned reports whether a player (any id form) is banned.
func adminPlayerBanned(pid string) bool {
	adminLoadBans()
	pid = normalizedPlayerStatePID(pid)
	adminModeration.mu.Lock()
	defer adminModeration.mu.Unlock()
	return adminBanActiveLocked(pid)
}

// adminConnectionRevoked reports whether a game connection that registered at
// since must be dropped: the player is banned, or was kicked after it opened.
func adminConnectionRevoked(pid string, since time.Time) bool {
	adminLoadBans()
	pid = normalizedPlayerStatePID(pid)
	adminModeration.mu.Lock()
	defer adminModeration.mu.Unlock()
	if adminBanActiveLocked(pid) {
		return true
	}
	at, ok := adminModeration.kicked[pid]
	return ok && !at.Before(since)
}

// adminDisconnect drops every connection the player holds: the Firmament
// (social) socket now, the game socket on its next push pass (within one
// client ping, ~5 s), and the gateway session.
func adminDisconnect(pid string) (wasOnline bool) {
	adminModeration.mu.Lock()
	adminModeration.kicked[pid] = time.Now()
	for p, at := range adminModeration.kicked { // keep the map small
		if time.Since(at) > time.Hour {
			delete(adminModeration.kicked, p)
		}
	}
	adminModeration.mu.Unlock()

	socialHubInstance.mu.RLock()
	peer := socialHubInstance.peers[pid]
	socialHubInstance.mu.RUnlock()
	if peer != nil {
		wasOnline = true
		_ = peer.conn.Close()
		socialHubInstance.leave(peer)
	}
	sessionsMu.Lock()
	for id, sess := range sessions {
		if normalizedPlayerStatePID(sess.UserID) == pid {
			delete(sessions, id)
		}
	}
	sessionsMu.Unlock()
	return wasOnline
}

// adminAudit records one admin action. actor is the caller's address.
func adminAudit(database *sql.DB, r *http.Request, action, target string, details any) {
	actor := r.RemoteAddr
	if host, _, err := net.SplitHostPort(actor); err == nil {
		actor = host
	}
	text := ""
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			text = string(b)
		}
	}
	logrus.WithFields(logrus.Fields{"actor": actor, "action": action, "target": target, "details": text}).Warn("admin: action")
	if database != nil {
		_, _ = database.Exec(`INSERT INTO admin_audit(actor,action,target,details) VALUES(?,?,?,?)`, actor, action, target, text)
	}
}

func registerAdminModeration(api *mux.Router) {
	api.HandleFunc("/players/{pid}/kick", adminAPIPlayerKick).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/ban", adminAPIPlayerBan).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/unban", adminAPIPlayerUnban).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/password", adminAPIPlayerResetPassword).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/matches", adminAPIPlayerMatches).Methods(http.MethodGet)
	api.HandleFunc("/players/{pid}/audit", adminAPIPlayerAudit).Methods(http.MethodGet)
	api.HandleFunc("/bans", adminAPIBans).Methods(http.MethodGet)
	api.HandleFunc("/audit", adminAPIAudit).Methods(http.MethodGet) // admin_audit_query.go
}

func adminReason(w http.ResponseWriter, r *http.Request, required bool) (string, bool) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return "", false
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > 200 {
		http.Error(w, `{"error":"reason is limited to 200 characters"}`, http.StatusBadRequest)
		return "", false
	}
	if required && req.Reason == "" {
		http.Error(w, `{"error":"a reason is required"}`, http.StatusBadRequest)
		return "", false
	}
	return req.Reason, true
}

func adminAPIPlayerKick(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	reason, ok := adminReason(w, r, false)
	if !ok {
		return
	}
	online := adminDisconnect(pid)
	adminAudit(database, r, "kick", pid, map[string]any{"reason": reason, "was_online": online})
	writeAdminJSON(w, map[string]any{"kicked": true, "was_online": online})
}

func adminAPIPlayerBan(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	reason, hours, ok := adminReasonHours(w, r, true)
	if !ok {
		return
	}
	adminLoadBans()
	expires := adminExpiryAfter(hours)
	if _, err := database.Exec(`INSERT INTO admin_bans(user_id,reason,expires_at) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET reason=excluded.reason, expires_at=excluded.expires_at`, pid, reason, expires); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminModeration.mu.Lock()
	adminModeration.banned[pid] = true
	if end, ok := adminParseExpiry(expires); ok {
		adminModeration.until[pid] = end
	} else {
		delete(adminModeration.until, pid)
	}
	adminModeration.mu.Unlock()
	online := adminDisconnect(pid)
	authErr := adminAuthCall("/admin/ban", pid, reason)
	details := map[string]any{"reason": reason, "was_online": online, "auth": "ok", "hours": hours, "expires_at": expires}
	if authErr != nil {
		details["auth"] = authErr.Error()
	}
	adminAudit(database, r, "ban", pid, details)
	adminAPIPlayerDetail(w, r)
}

func adminAPIPlayerUnban(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	authErr, err := adminUnbanPlayer(database, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	details := map[string]any{"auth": "ok"}
	if authErr != nil {
		details["auth"] = authErr.Error()
	}
	adminAudit(database, r, "unban", pid, details)
	adminAPIPlayerDetail(w, r)
}

// adminUnbanPlayer lifts a ban: the database row, the in-memory list, and the
// auth server (best effort, returned as authErr). The one path for a manual
// unban and for the expiry sweeper.
func adminUnbanPlayer(database *sql.DB, pid string) (authErr, err error) {
	if err := adminUnbanLocal(database, pid); err != nil {
		return nil, err
	}
	return adminAuthCall("/admin/unban", pid, ""), nil
}

// adminUnbanLocal lifts mmogbrain's own ban (row and memory list) only.
func adminUnbanLocal(database *sql.DB, pid string) error {
	adminLoadBans()
	if _, err := database.Exec(`DELETE FROM admin_bans WHERE user_id=?`, pid); err != nil {
		return err
	}
	adminModeration.mu.Lock()
	delete(adminModeration.banned, pid)
	delete(adminModeration.until, pid)
	adminModeration.mu.Unlock()
	return nil
}

// adminAuthCall forwards a ban/unban to the auth server so the launcher
// refuses the account too. Best effort: mmogbrain's own ban already holds.
func adminAuthCall(path, pid, reason string) error {
	_, err := adminAuthPost(path, map[string]string{"user_id": pid, "reason": reason})
	return err
}

// adminAuthPost sends an admin request to the auth server and returns its
// reply body.
func adminAuthPost(path string, payload map[string]string) ([]byte, error) {
	if adminDash.authURL == "" {
		return nil, errors.New("auth server not configured")
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(adminDash.authURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", adminDash.adminKey)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return reply, fmt.Errorf("auth server answered %d", resp.StatusCode)
	}
	return reply, nil
}

// adminAPIPlayerResetPassword gives the account a new random password (the
// auth server makes and stores it, and signs the account out everywhere) and
// returns it ONCE, for the admin to pass on. The audit records the reset, never
// the password.
func adminAPIPlayerResetPassword(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	reply, err := adminAuthPost("/admin/password", map[string]string{"user_id": pid})
	if err != nil {
		adminAudit(database, r, "password reset failed", pid, map[string]any{"error": err.Error()})
		http.Error(w, `{"error":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}`, http.StatusBadGateway)
		return
	}
	var out struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(reply, &out); err != nil || out.Password == "" {
		http.Error(w, `{"error":"auth server sent no password"}`, http.StatusBadGateway)
		return
	}
	adminAudit(database, r, "password reset", pid, map[string]any{"username": out.Username})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func adminAPIPlayerMatches(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	type row struct {
		MatchID string `json:"match_id"`
		At      string `json:"at"`
		Mode    string `json:"mode"`
		Map     string `json:"map"`
		Fleet   string `json:"fleet"`
		Outcome string `json:"outcome"`
		Kills   int    `json:"kills"`
		Deaths  int    `json:"deaths"`
		Assists int    `json:"assists"`
		Damage  int64  `json:"damage"`
		Credits int64  `json:"credits"`
		XP      int64  `json:"xp"`
	}
	out := []row{}
	rows, err := database.Query(`SELECT b.match_id, b.created_at, COALESCE(m.game_mode,''), COALESCE(m.map,''),
		b.fleet_type, b.outcome, b.kills, b.deaths, b.assists, b.damage, b.credits, b.xp
		FROM battle_results b
		-- battle_results.match_id is the battle server's id (matches.battle_match_id)
		LEFT JOIN matches m ON m.battle_match_id=b.match_id AND b.match_id<>''
		WHERE b.user_id=? ORDER BY b.created_at DESC LIMIT 50`, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var x row
		var fleet int32
		if rows.Scan(&x.MatchID, &x.At, &x.Mode, &x.Map, &fleet, &x.Outcome, &x.Kills, &x.Deaths, &x.Assists,
			&x.Damage, &x.Credits, &x.XP) == nil {
			x.Map = adminMapName(x.Map)
			x.Fleet = fleetTypeName(fleet)
			out = append(out, x)
		}
	}
	_ = rows.Close()
	writeAdminJSON(w, map[string]any{"matches": out})
}

// adminMapName shortens a map path (/Game/Maps/MP/X/MP_X_P) to its folder.
func adminMapName(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return path
}

type adminAuditRow struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Target  string `json:"target"`
	Name    string `json:"name,omitempty"`
	Details string `json:"details"`
}

func adminAuditRows(database *sql.DB, where string, args ...any) ([]adminAuditRow, error) {
	out := []adminAuditRow{}
	rows, err := database.Query(`SELECT id, created_at, actor, action, target, details FROM admin_audit `+where+
		` ORDER BY id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a adminAuditRow
		if rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Details) == nil {
			out = append(out, a)
		}
	}
	_ = rows.Close()
	// Names after the rows are closed: the lookup uses the same (single)
	// database connection.
	for i := range out {
		if adminPIDPattern.MatchString(out[i].Target) {
			out[i].Name = adminPlayerName(out[i].Target)
		}
	}
	return out, nil
}

func adminAPIPlayerAudit(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	rows, err := adminAuditRows(database, `WHERE target=?`, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"audit": rows})
}

func adminAPIBans(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	type row struct {
		PID       string `json:"pid"`
		Name      string `json:"name"`
		Reason    string `json:"reason"`
		At        string `json:"at"`
		ExpiresAt string `json:"expires_at"` // "" = permanent, else UTC "YYYY-MM-DD HH:MM:SS"
	}
	out := []row{}
	// Names joined in the one query: the store has ONE connection. A ban past
	// its end is not listed (it no longer holds; the sweeper removes the row).
	rows, err := database.Query(`SELECT b.user_id, COALESCE(NULLIF(ps.display_name,'Local'),'?'), b.reason, b.created_at, b.expires_at
		FROM admin_bans b LEFT JOIN player_state ps ON ps.user_id=b.user_id
		WHERE b.expires_at='' OR b.expires_at>datetime('now') ORDER BY b.created_at DESC`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var b row
		if rows.Scan(&b.PID, &b.Name, &b.Reason, &b.At, &b.ExpiresAt) == nil {
			out = append(out, b)
		}
	}
	_ = rows.Close()
	writeAdminJSON(w, map[string]any{"bans": out})
}

// adminAPIAudit lists admin actions, newest first, with filters:
//
//	action=  contains (case-insensitive) in the action text
//	q=       contains in action, actor (address) or details
//	target=  a player id (dashed or not) or the start of one
//	since=, until=  UTC "YYYY-MM-DD" or "YYYY-MM-DD HH:MM:SS" (until is exclusive)
//	before=  an audit id: only older rows (paging)
//	limit=   1..500, default 200
//
// Reply: {audit:[...], more:bool, next:<id to pass as before>, actions:[distinct action words]}.
func adminAPIAudit(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	where := []string{}
	args := []any{}
	contains := func(col, val string) {
		where = append(where, `instr(lower(`+col+`), lower(?))>0`)
		args = append(args, val)
	}
	if v := strings.TrimSpace(q.Get("action")); v != "" {
		contains("a.action", v)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		where = append(where, `(instr(lower(a.action), lower(?))>0 OR instr(lower(a.actor), lower(?))>0 OR instr(lower(a.details), lower(?))>0)`)
		args = append(args, v, v, v)
	}
	if v := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(q.Get("target")), "-", "")); v != "" {
		if len(v) > 32 || strings.Trim(v, "0123456789abcdef") != "" {
			http.Error(w, `{"error":"target must be a player id (hex)"}`, http.StatusBadRequest)
			return
		}
		where = append(where, `a.target LIKE ?||'%'`)
		args = append(args, v)
	}
	for _, f := range []struct{ key, op string }{{"since", ">="}, {"until", "<"}} {
		if v := strings.TrimSpace(q.Get(f.key)); v != "" {
			t, ok := adminParseExpiry(v)
			if !ok {
				if d, err := time.Parse("2006-01-02", v); err == nil {
					t, ok = d, true
				}
			}
			if !ok {
				http.Error(w, `{"error":"`+f.key+` must be YYYY-MM-DD"}`, http.StatusBadRequest)
				return
			}
			where = append(where, `a.created_at `+f.op+` ?`)
			args = append(args, t.UTC().Format("2006-01-02 15:04:05"))
		}
	}
	if v := strings.TrimSpace(q.Get("before")); v != "" {
		var id int64
		if _, err := fmt.Sscanf(v, "%d", &id); err != nil || id < 1 {
			http.Error(w, `{"error":"before must be an audit id"}`, http.StatusBadRequest)
			return
		}
		where = append(where, `a.id<?`)
		args = append(args, id)
	}
	limit := 200
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit < 1 || limit > 500 {
			http.Error(w, `{"error":"limit must be 1..500"}`, http.StatusBadRequest)
			return
		}
	}
	clause := ""
	if len(where) > 0 {
		clause = ` WHERE ` + strings.Join(where, ` AND `)
	}
	// One extra row tells whether there is a next page. Names are joined here:
	// the store has ONE connection (no lookups while rows are open).
	rows, err := database.Query(`SELECT a.id, a.created_at, a.actor, a.action, a.target, a.details,
		COALESCE(NULLIF(ps.display_name,'Local'),'')
		FROM admin_audit a LEFT JOIN player_state ps ON ps.user_id=a.target`+clause+
		` ORDER BY a.id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []adminAuditRow{}
	for rows.Next() {
		var a adminAuditRow
		if rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Details, &a.Name) == nil {
			out = append(out, a)
		}
	}
	_ = rows.Close()
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	actions := []string{}
	if ar, err := database.Query(`SELECT DISTINCT action FROM admin_audit ORDER BY action LIMIT 200`); err == nil {
		for ar.Next() {
			var s string
			if ar.Scan(&s) == nil {
				actions = append(actions, s)
			}
		}
		_ = ar.Close()
	}
	next := int64(0)
	if len(out) > 0 {
		next = out[len(out)-1].ID
	}
	writeAdminJSON(w, map[string]any{"audit": out, "more": more, "next": next, "actions": actions})
}
