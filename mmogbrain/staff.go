package main

// Staff accounts (2026-11-25; operator: "the moderators should be able to do
// both, so in the dashboard and in the game; the dashboard needs to be
// limited, so it needs multi role: global admin, moderator, balancer").
//
// A staff member is an ordinary player account with a role (table staff):
//
//	admin      everything, as the admin key (which still works as the
//	           break-glass login and counts as admin)
//	moderator  players, chat, moderation, fair play, reports, messages;
//	           bans at most staffModeratorMaxBanHours, and may lift only
//	           timed bans (operator's picks, 2026-11-25)
//	balancer   balancing tables, tech-tree costs, game modes and bots,
//	           market and economy settings; read-only match statistics
//
// Dashboard: a staff member signs in with their game username (or email) and
// password. mmogbrain checks them with the auth server (POST /auth/, the
// launcher's own sign-in), reads the player id from the JWT it returns
// (verified with JWT_SECRET), and issues a dashboard session token (random,
// 12 h; only its SHA-256 is stored). Every /admin/api call then carries
// X-Admin-Session or X-Admin-Key. The role is read from the in-memory staff
// list on every call, so removing someone or changing a role applies at once.
//
// Permissions are an allow-list per role keyed by method and route template
// (staffRoutePerms): a route nobody listed is admin-only, so a new dashboard
// route never opens to moderators or balancers by accident.
//
// The audit log names the staff member ("Name (moderator)") instead of the
// caller's address, which could not tell people apart under one shared key.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

const (
	staffRoleAdmin     = "admin"
	staffRoleModerator = "moderator"
	staffRoleBalancer  = "balancer"

	// staffModeratorMaxBanHours: a moderator's longest ban, 7 days.
	staffModeratorMaxBanHours = 7 * 24
	staffSessionTTL           = 12 * time.Hour
)

func staffRoleValid(role string) bool {
	return role == staffRoleAdmin || role == staffRoleModerator || role == staffRoleBalancer
}

// staffRoleName is the role as staff see it.
func staffRoleName(role string) string {
	switch role {
	case staffRoleAdmin:
		return "global admin"
	case staffRoleModerator:
		return "moderator"
	case staffRoleBalancer:
		return "balancer"
	}
	return role
}

// --- the staff list --------------------------------------------------------

var staffList = struct {
	mu     sync.Mutex
	loaded bool
	role   map[string]string // pid -> role
}{role: map[string]string{}}

func staffLoad() {
	staffList.mu.Lock()
	loaded := staffList.loaded
	staffList.mu.Unlock()
	if loaded {
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		return
	}
	roles := map[string]string{}
	rows, err := database.Query(`SELECT user_id, role FROM staff`)
	if err != nil {
		return
	}
	for rows.Next() {
		var pid, role string
		if rows.Scan(&pid, &role) == nil && staffRoleValid(role) {
			roles[pid] = role
		}
	}
	_ = rows.Close()
	staffList.mu.Lock()
	if !staffList.loaded {
		staffList.role = roles
		staffList.loaded = true
	}
	staffList.mu.Unlock()
}

// staffRoleOf is a player's staff role ("" for none). Any id spelling.
func staffRoleOf(pid string) string {
	staffLoad()
	pid = normalizedPlayerStatePID(pid)
	staffList.mu.Lock()
	defer staffList.mu.Unlock()
	return staffList.role[pid]
}

// staffSet gives pid a role ("" removes them). Their dashboard sessions are
// ended when they are removed.
func staffSet(database *sql.DB, pid, role, addedBy string) error {
	staffLoad()
	if role == "" {
		if _, err := database.Exec(`DELETE FROM staff WHERE user_id=?`, pid); err != nil {
			return err
		}
		staffEndSessionsOf(database, pid)
	} else if _, err := database.Exec(`INSERT INTO staff(user_id,role,added_by) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET role=excluded.role, added_by=excluded.added_by`, pid, role, addedBy); err != nil {
		return err
	}
	staffList.mu.Lock()
	if role == "" {
		delete(staffList.role, pid)
	} else {
		staffList.role[pid] = role
	}
	staffList.mu.Unlock()
	return nil
}

// --- who is calling ----------------------------------------------------------

// staffActor is who made an admin request or ran a staff command.
type staffActor struct {
	pid  string // "" for the admin key
	name string
	role string
	ip   string
}

// label is the audit log's actor text.
func (a staffActor) label() string {
	if a.pid == "" {
		return a.ip // the admin key: as before, its address
	}
	return a.name + " (" + staffRoleName(a.role) + ")"
}

type staffCtxKey struct{}

// staffFromRequest is the caller of an /admin/api request; the admin key
// (or no middleware, in older tests) is an admin.
func staffFromRequest(r *http.Request) staffActor {
	if a, ok := r.Context().Value(staffCtxKey{}).(staffActor); ok {
		return a
	}
	return staffActor{role: staffRoleAdmin, ip: requestIP(r)}
}

func requestIP(r *http.Request) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return ip
}

// --- dashboard sessions ----------------------------------------------------

type staffSession struct {
	pid     string
	expires time.Time
}

var staffSessions = struct {
	mu     sync.Mutex
	byHash map[string]staffSession
}{byHash: map[string]staffSession{}}

func staffTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func staffNewSession(database *sql.DB, pid, ip string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(staffSessionTTL).UTC()
	h := staffTokenHash(token)
	if _, err := database.Exec(`INSERT INTO staff_sessions(token_hash,user_id,expires_at,ip) VALUES(?,?,?,?)`,
		h, pid, expires.Format("2006-01-02 15:04:05"), ip); err != nil {
		return "", time.Time{}, err
	}
	_, _ = database.Exec(`DELETE FROM staff_sessions WHERE expires_at < ?`, time.Now().UTC().Format("2006-01-02 15:04:05"))
	staffSessions.mu.Lock()
	staffSessions.byHash[h] = staffSession{pid: pid, expires: expires}
	staffSessions.mu.Unlock()
	return token, expires, nil
}

// staffSessionPID is the account a live session token belongs to.
func staffSessionPID(token string) (string, bool) {
	if len(token) != 64 {
		return "", false
	}
	h := staffTokenHash(token)
	staffSessions.mu.Lock()
	s, ok := staffSessions.byHash[h]
	staffSessions.mu.Unlock()
	if !ok {
		// After a restart: the session is in the table only.
		database := currentMmogPlayerStateDB()
		if database == nil {
			return "", false
		}
		var pid, exp string
		if database.QueryRow(`SELECT user_id, expires_at FROM staff_sessions WHERE token_hash=?`, h).Scan(&pid, &exp) != nil {
			return "", false
		}
		end, err := time.Parse("2006-01-02 15:04:05", exp)
		if err != nil {
			return "", false
		}
		s = staffSession{pid: pid, expires: end}
		staffSessions.mu.Lock()
		staffSessions.byHash[h] = s
		staffSessions.mu.Unlock()
	}
	if !time.Now().Before(s.expires) {
		return "", false
	}
	return s.pid, true
}

func staffEndSession(database *sql.DB, token string) {
	h := staffTokenHash(token)
	staffSessions.mu.Lock()
	delete(staffSessions.byHash, h)
	staffSessions.mu.Unlock()
	if database != nil {
		_, _ = database.Exec(`DELETE FROM staff_sessions WHERE token_hash=?`, h)
	}
}

// --- permissions -------------------------------------------------------------

// staffRoutePerms: the /admin/api routes (method + template, without the
// /admin/api prefix) each role besides admin may call. Anything else is
// admin-only.
var staffRoutePerms = map[string]map[string]bool{
	staffRoleModerator: setOf(
		"GET /whoami", "GET /overview", "GET /metrics", "GET /restart", "GET /instances", "GET /online", "GET /queue",
		"GET /matches", "GET /matches/{id}", "GET /leaderboard",
		"GET /players", "GET /players/{pid}", "GET /players/{pid}/profile", "GET /players/{pid}/matches",
		"GET /players/{pid}/audit", "GET /players/{pid}/notes",
		"POST /players/{pid}/kick", "POST /players/{pid}/ban", "POST /players/{pid}/unban",
		"POST /players/{pid}/mute", "POST /players/{pid}/unmute", "POST /players/{pid}/rename",
		"POST /players/{pid}/notes", "DELETE /players/{pid}/notes/{id}",
		"GET /chat", "GET /bans", "GET /mutes", "GET /audit",
		"GET /fairplay", "POST /fairplay/review", "GET /reports",
		"GET /messages", "POST /messages/send",
	),
	staffRoleBalancer: setOf(
		"GET /whoami", "GET /overview", "GET /metrics", "GET /restart", "GET /instances", "GET /queue",
		"GET /matches", "GET /matches/{id}", "GET /leaderboard", "GET /havoc",
		"GET /tune", "GET /tune/battle", "GET /tune/table/{table}", "GET /tune/versions/{id}",
		"POST /tune/draft", "POST /tune/publish", "POST /tune/discard", "POST /tune/restore", "POST /tune/battle",
		"GET /techtree/ships", "GET /techtree/ships/{id}", "GET /techtree/costs", "GET /techtree/export",
		"POST /techtree/values", "POST /techtree/import",
		"GET /modes", "POST /modes",
		"GET /market", "POST /market/prices", "POST /market/sales", "POST /market/sales/{id}/end",
		"GET /economy", "GET /settings", "POST /settings",
	),
}

// staffPages: the dashboard pages (admin_ui/pages ids) a role sees; admin sees
// every page.
var staffPages = map[string][]string{
	staffRoleModerator: {"overview", "instances", "queue", "matches", "match", "chat", "players", "player",
		"moderation", "fairplay", "reports", "messages", "leaderboard"},
	staffRoleBalancer: {"overview", "instances", "queue", "matches", "match", "leaderboard", "havoc", "metrics",
		"modes", "tune", "techtree", "market", "economy"},
}

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// staffMayCall: whether role may call method on route template (with the
// /admin/api prefix or without).
func staffMayCall(role, method, template string) bool {
	if role == staffRoleAdmin {
		return true
	}
	template = strings.TrimPrefix(template, "/admin/api")
	return staffRoutePerms[role][method+" "+template]
}

// staffAPIMiddleware is the /admin/api gate: the admin key, or a staff
// session whose role may call this route. A missing or wrong credential is
// 403 {"error":"forbidden"} (the dashboard locks); a role that may not call
// the route is 403 with a readable reason (the dashboard shows it).
func staffAPIMiddleware(key string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := staffActor{ip: requestIP(r)}
			if got := r.Header.Get("X-Admin-Key"); got != "" {
				if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
					http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
					return
				}
				actor.role = staffRoleAdmin
			} else {
				pid, ok := staffSessionPID(r.Header.Get("X-Admin-Session"))
				role := ""
				if ok {
					role = staffRoleOf(pid)
				}
				if role == "" || adminPlayerBanned(pid) {
					http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
					return
				}
				actor.pid, actor.role, actor.name = pid, role, adminPlayerName(pid)
			}
			template := r.URL.Path
			if route := mux.CurrentRoute(r); route != nil {
				if t, err := route.GetPathTemplate(); err == nil {
					template = t
				}
			}
			if !staffMayCall(actor.role, r.Method, template) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "your role (" + staffRoleName(actor.role) + ") cannot do this"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), staffCtxKey{}, actor)))
		})
	}
}

// --- sign-in, sign-out, whoami ----------------------------------------------

// staffLoginLimit: sign-in attempts per address in staffLoginWindow.
const (
	staffLoginLimit  = 10
	staffLoginWindow = 15 * time.Minute
)

var staffLoginTries = struct {
	mu sync.Mutex
	at map[string][]time.Time
}{at: map[string][]time.Time{}}

func staffLoginAllowed(ip string) bool {
	staffLoginTries.mu.Lock()
	defer staffLoginTries.mu.Unlock()
	now := time.Now()
	var kept []time.Time
	for _, t := range staffLoginTries.at[ip] {
		if now.Sub(t) < staffLoginWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= staffLoginLimit {
		staffLoginTries.at[ip] = kept
		return false
	}
	staffLoginTries.at[ip] = append(kept, now)
	return true
}

// staffCheckPassword asks the auth server whether identifier/password sign in
// (the launcher's own POST /auth/), and returns the player id from the JWT it
// issues, verified with this server's JWT secret.
var staffCheckPassword = func(identifier, password string) (string, error) {
	if adminDash.authURL == "" {
		return "", errors.New("auth server not configured")
	}
	form := url.Values{"grant_type": {"password"}, "username": {identifier}, "password": {password}}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).PostForm(strings.TrimRight(adminDash.authURL, "/")+"/auth/", form)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", errStaffBadLogin
	}
	var out struct {
		JWT string `json:"jwt"`
	}
	if json.Unmarshal(body, &out) != nil || out.JWT == "" {
		return "", errors.New("auth server sent no token")
	}
	pid, err := protocol.ExtractVerifiedPlayerPIDFromJWT(out.JWT, mmogJWTSecret())
	if err != nil {
		return "", err
	}
	return normalizedPlayerStatePID(pid), nil
}

var errStaffBadLogin = errors.New("wrong name or password")

func registerStaffAuth(r *mux.Router) {
	r.HandleFunc("/admin/login", staffAPILogin).Methods(http.MethodPost)
	r.HandleFunc("/admin/login/totp", staffAPILoginTOTP).Methods(http.MethodPost)
	r.HandleFunc("/admin/logout", staffAPILogout).Methods(http.MethodPost)
}

func staffAPILogin(w http.ResponseWriter, r *http.Request) {
	ip := requestIP(r)
	fail := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if !staffLoginAllowed(ip) {
		fail(http.StatusTooManyRequests, "too many sign-in attempts; wait 15 minutes")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		strings.TrimSpace(req.Username) == "" || req.Password == "" {
		fail(http.StatusBadRequest, "name and password are required")
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		fail(http.StatusServiceUnavailable, "database not ready")
		return
	}
	pid, err := staffCheckPassword(strings.TrimSpace(req.Username), req.Password)
	if err != nil {
		if !errors.Is(err, errStaffBadLogin) {
			logrus.WithError(err).Warn("staff: sign-in check failed")
		}
		adminAuditActor(database, ip, "staff sign-in refused", "", map[string]any{"reason": "wrong name or password"})
		fail(http.StatusUnauthorized, "wrong name or password")
		return
	}
	role := staffRoleOf(pid)
	if role == "" || adminPlayerBanned(pid) {
		adminAuditActor(database, ip, "staff sign-in refused", pid, map[string]any{"reason": "not staff"})
		fail(http.StatusForbidden, "this account is not staff")
		return
	}
	// The password was right: now the authenticator code (staff_totp.go).
	staffLoginSecondStep(w, database, pid)
}

func staffAPILogout(w http.ResponseWriter, r *http.Request) {
	if token := r.Header.Get("X-Admin-Session"); token != "" {
		staffEndSession(currentMmogPlayerStateDB(), token)
	}
	writeAdminJSON(w, map[string]any{"signed_out": true})
}

// adminAPIWhoami tells the dashboard who is signed in and which pages to show
// (nil pages = every page).
func adminAPIWhoami(w http.ResponseWriter, r *http.Request) {
	a := staffFromRequest(r)
	name := a.name
	if a.pid == "" {
		name = "admin key"
	}
	// routes: the "METHOD /template" calls this role may make (nil = all),
	// so pages hide the controls it cannot use.
	var routes []string
	if a.role != staffRoleAdmin {
		routes = []string{}
		for r := range staffRoutePerms[a.role] {
			routes = append(routes, r)
		}
		sort.Strings(routes)
	}
	writeAdminJSON(w, map[string]any{"name": name, "role": a.role, "role_name": staffRoleName(a.role),
		"pages": staffPages[a.role], "routes": routes, "max_ban_hours": staffMaxBanHours(a.role)})
}

// staffMaxBanHours: the longest ban a role may give (0 = no cap).
func staffMaxBanHours(role string) int {
	if role == staffRoleModerator {
		return staffModeratorMaxBanHours
	}
	return 0
}

// --- the Staff page (admin only) --------------------------------------------

func init() { adminRegisterAPI(registerAdminStaff) }

func registerAdminStaff(api *mux.Router) {
	api.HandleFunc("/whoami", adminAPIWhoami).Methods(http.MethodGet)
	api.HandleFunc("/staff", adminAPIStaff).Methods(http.MethodGet)
	api.HandleFunc("/staff", adminAPIStaffSet).Methods(http.MethodPost)
	api.HandleFunc("/staff/{pid}", adminAPIStaffRemove).Methods(http.MethodDelete)
	api.HandleFunc("/staff/{pid}/totp-reset", adminAPIStaffTOTPReset).Methods(http.MethodPost)
}

func adminAPIStaff(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	type row struct {
		PID      string `json:"pid"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		AddedBy  string `json:"added_by"`
		Created  string `json:"created_at"`
		Sessions int    `json:"sessions"`
		TwoFA    bool   `json:"two_fa"`
	}
	out := []row{}
	if database != nil {
		rows, err := database.Query(`SELECT user_id, role, added_by, created_at, totp_secret<>'' FROM staff ORDER BY role, created_at`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for rows.Next() {
			var x row
			if rows.Scan(&x.PID, &x.Role, &x.AddedBy, &x.Created, &x.TwoFA) == nil {
				out = append(out, x)
			}
		}
		_ = rows.Close()
		// Names and session counts after the rows are closed (one connection).
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		for i := range out {
			out[i].Name = adminPlayerName(out[i].PID)
			_ = database.QueryRow(`SELECT COUNT(*) FROM staff_sessions WHERE user_id=? AND expires_at > ?`, out[i].PID, now).Scan(&out[i].Sessions)
		}
	}
	writeAdminJSON(w, map[string]any{"staff": out, "roles": []string{staffRoleAdmin, staffRoleModerator, staffRoleBalancer}})
}

// adminAPIStaffSet adds or changes a staff member: {"player": name or id,
// "role": admin|moderator|balancer}.
func adminAPIStaffSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Player string `json:"player"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if !staffRoleValid(req.Role) {
		http.Error(w, `{"error":"role must be admin, moderator or balancer"}`, http.StatusBadRequest)
		return
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database not ready"}`, http.StatusServiceUnavailable)
		return
	}
	pid, err := staffFindPlayer(database, req.Player)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	before := staffRoleOf(pid)
	addedBy := staffFromRequest(r).name
	if addedBy == "" {
		addedBy = "admin key"
	}
	if err := staffSet(database, pid, req.Role, addedBy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminAudit(database, r, "staff role", pid, map[string]any{"from": before, "to": req.Role})
	writeAdminJSON(w, map[string]any{"pid": pid, "name": adminPlayerName(pid), "role": req.Role})
}

func adminAPIStaffRemove(w http.ResponseWriter, r *http.Request) {
	pid := normalizedPlayerStatePID(mux.Vars(r)["pid"])
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database not ready"}`, http.StatusServiceUnavailable)
		return
	}
	before := staffRoleOf(pid)
	if before == "" {
		http.Error(w, `{"error":"not staff"}`, http.StatusNotFound)
		return
	}
	if err := staffSet(database, pid, "", ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminAudit(database, r, "staff role", pid, map[string]any{"from": before, "to": ""})
	writeAdminJSON(w, map[string]any{"removed": true})
}

// staffFindPlayer resolves a captain name (exact, any case) or a player id to
// an existing account.
func staffFindPlayer(database *sql.DB, player string) (string, error) {
	player = strings.TrimSpace(player)
	if player == "" {
		return "", errors.New("name a player")
	}
	if n := normalizedPlayerStatePID(player); len(strings.ReplaceAll(player, "-", "")) == 32 {
		var one int
		if database.QueryRow(`SELECT 1 FROM player_state WHERE user_id=?`, n).Scan(&one) == nil {
			return n, nil
		}
	}
	pids, err := staffPlayersNamed(database, player)
	if err != nil {
		return "", err
	}
	switch len(pids) {
	case 0:
		return "", errors.New("no player is called " + player)
	case 1:
		return pids[0], nil
	}
	return "", errors.New("several players are called " + player + "; use the player id")
}

// staffPlayersNamed: the accounts whose captain name is name (any case).
func staffPlayersNamed(database *sql.DB, name string) ([]string, error) {
	rows, err := database.Query(`SELECT user_id FROM player_state WHERE display_name=? COLLATE NOCASE LIMIT 3`, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var pid string
		if rows.Scan(&pid) == nil {
			out = append(out, pid)
		}
	}
	return out, rows.Err()
}
