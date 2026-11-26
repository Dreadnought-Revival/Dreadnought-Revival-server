package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/gorilla/mux"
)

const (
	staffTestMod    = "11111111111111111111111111111111"
	staffTestBal    = "22222222222222222222222222222222"
	staffTestPlayer = "33333333333333333333333333333333"
	staffTestAdmin  = "44444444444444444444444444444444"
)

func resetStaff(t *testing.T) {
	t.Helper()
	reset := func() {
		staffList.mu.Lock()
		staffList.loaded = false
		staffList.role = map[string]string{}
		staffList.mu.Unlock()
		staffSessions.mu.Lock()
		staffSessions.byHash = map[string]staffSession{}
		staffSessions.mu.Unlock()
		staffLoginTries.mu.Lock()
		staffLoginTries.at = map[string][]time.Time{}
		staffLoginTries.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// staffTestSetup: a temp store with a moderator, a balancer, a global admin
// and a plain player, named so chat commands can find them.
func staffTestSetup(t *testing.T) {
	t.Helper()
	database := useTempMmogPlayerStateDB(t)
	resetStaff(t)
	resetAdminModeration(t)
	for pid, name := range map[string]string{staffTestMod: "Mod One", staffTestBal: "Bal", staffTestPlayer: "Some Player", staffTestAdmin: "Boss"} {
		if err := seedMmogPlayerState(database, pid); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE player_state SET display_name=? WHERE user_id=?`, name, pid); err != nil {
			t.Fatal(err)
		}
	}
	for pid, role := range map[string]string{staffTestMod: staffRoleModerator, staffTestBal: staffRoleBalancer, staffTestAdmin: staffRoleAdmin} {
		if err := staffSet(database, pid, role, "test"); err != nil {
			t.Fatal(err)
		}
	}
}

// adminAPIRouteTemplates: every /admin/api route as "METHOD /template"
// (without the prefix), the form staffRoutePerms uses.
func adminAPIRouteTemplates(t *testing.T, r *mux.Router) []string {
	t.Helper()
	var out []string
	_ = r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tpl, err := route.GetPathTemplate()
		if err != nil || !strings.HasPrefix(tpl, "/admin/api/") {
			return nil
		}
		methods, _ := route.GetMethods()
		for _, m := range methods {
			out = append(out, m+" "+strings.TrimPrefix(tpl, "/admin/api"))
		}
		return nil
	})
	if len(out) < 40 {
		t.Fatalf("only %d admin routes found", len(out))
	}
	return out
}

func staffSessionFor(t *testing.T, pid string) string {
	t.Helper()
	token, _, err := staffNewSession(currentMmogPlayerStateDB(), pid, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func staffRequest(r http.Handler, method, path, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Admin-Session", session)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// Every permission names a real route: a typo would silently lock a page.
func TestStaffPermissionsNameRealRoutes(t *testing.T) {
	r := adminTestRouter()
	templates := map[string]bool{}
	for _, rt := range adminAPIRouteTemplates(t, r) {
		templates[rt] = true
	}
	for role, perms := range staffRoutePerms {
		for p := range perms {
			if !templates[p] {
				t.Errorf("%s: permission %q is not a dashboard route", role, p)
			}
		}
	}
}

// Moderators and balancers get exactly their lists; everything else -- every
// route anyone adds later -- answers 403 with the role's reason, not the
// "forbidden" that locks the dashboard.
func TestStaffRolesAreLimitedToTheirRoutes(t *testing.T) {
	staffTestSetup(t)
	r := adminTestRouter()
	for _, who := range []struct{ pid, role string }{{staffTestMod, staffRoleModerator}, {staffTestBal, staffRoleBalancer}} {
		session := staffSessionFor(t, who.pid)
		for _, tpl := range adminAPIRouteTemplates(t, r) {
			method, path, _ := strings.Cut(tpl, " ")
			if staffRoutePerms[who.role][tpl] {
				continue // allowed: its own tests cover the behaviour
			}
			path = strings.NewReplacer("{pid}", staffTestPlayer, "{id}", "1", "{table}", "x", "{name}", "x").Replace(path)
			rec := staffRequest(r, method, "/admin/api"+path, session, `{}`)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cannot do this") {
				t.Errorf("%s: %s answered %d %s, want 403 'cannot do this'", who.role, tpl, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
	if staffRoutePerms[staffRoleModerator]["GET /staff"] || staffRoutePerms[staffRoleBalancer]["POST /staff"] {
		t.Error("the Staff page must stay global admin only")
	}
}

func TestStaffWhoamiAndAllowedCalls(t *testing.T) {
	staffTestSetup(t)
	r := adminTestRouter()
	mod := staffSessionFor(t, staffTestMod)
	rec := staffRequest(r, http.MethodGet, "/admin/api/whoami", mod, "")
	var who struct {
		Name, Role  string
		Pages       []string
		MaxBanHours int `json:"max_ban_hours"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &who) != nil {
		t.Fatalf("whoami %d %s", rec.Code, rec.Body.String())
	}
	if who.Name != "Mod One" || who.Role != staffRoleModerator || who.MaxBanHours != 168 || len(who.Pages) == 0 {
		t.Errorf("whoami %+v", who)
	}
	if rec := staffRequest(r, http.MethodGet, "/admin/api/players?q=Some", mod, ""); rec.Code != http.StatusOK {
		t.Errorf("moderator player search: %d", rec.Code)
	}
	if rec := staffRequest(r, http.MethodPost, "/admin/api/players/"+staffTestPlayer+"/currency", mod, `{"mode":"add","credits":5}`); rec.Code != http.StatusForbidden {
		t.Errorf("moderator granted currency: %d", rec.Code)
	}
	// Admin key still works for everything, the Staff page included.
	if rec := adminGet(r, "/admin/api/staff", "test-admin-key"); rec.Code != http.StatusOK {
		t.Errorf("admin key on /staff: %d", rec.Code)
	}
	// Removing a moderator ends their session at once.
	if err := staffSet(currentMmogPlayerStateDB(), staffTestMod, "", ""); err != nil {
		t.Fatal(err)
	}
	if rec := staffRequest(r, http.MethodGet, "/admin/api/whoami", mod, ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"forbidden"`) {
		t.Errorf("removed moderator still signed in: %d %s", rec.Code, rec.Body.String())
	}
}

// A moderator bans for 1..168 hours on the dashboard and lifts only timed
// bans; the audit names them.
func TestModeratorBanLimitsOnTheDashboard(t *testing.T) {
	staffTestSetup(t)
	r := adminTestRouter()
	mod := staffSessionFor(t, staffTestMod)
	ban := func(hours string) int {
		return staffRequest(r, http.MethodPost, "/admin/api/players/"+staffTestPlayer+"/ban", mod, `{"reason":"spam","hours":`+hours+`}`).Code
	}
	if c := ban("0"); c != http.StatusForbidden {
		t.Errorf("moderator permanent ban: %d", c)
	}
	if c := ban("720"); c != http.StatusForbidden {
		t.Errorf("moderator 30-day ban: %d", c)
	}
	if c := ban("24"); c != http.StatusOK {
		t.Fatalf("moderator 24 h ban: %d", c)
	}
	var actor string
	_ = currentMmogPlayerStateDB().QueryRow(`SELECT actor FROM admin_audit WHERE action='ban' ORDER BY id DESC LIMIT 1`).Scan(&actor)
	if actor != "Mod One (moderator)" {
		t.Errorf("audit actor %q", actor)
	}
	if c := staffRequest(r, http.MethodPost, "/admin/api/players/"+staffTestPlayer+"/unban", mod, `{}`).Code; c != http.StatusOK {
		t.Errorf("moderator lifting a timed ban: %d", c)
	}
	// A permanent ban by the admin key: the moderator cannot lift it.
	if c := adminPost(r, "/admin/api/players/"+staffTestPlayer+"/ban", "test-admin-key", `{"reason":"cheat","hours":0}`).Code; c != http.StatusOK {
		t.Fatalf("admin permanent ban: %d", c)
	}
	if c := staffRequest(r, http.MethodPost, "/admin/api/players/"+staffTestPlayer+"/unban", mod, `{}`).Code; c != http.StatusForbidden {
		t.Errorf("moderator lifted a permanent ban: %d", c)
	}
}

// Sign-in: the auth server's answer (stubbed) decides who the caller is; only
// staff get a session, and it works for the API.
func TestStaffSignIn(t *testing.T) {
	staffTestSetup(t)
	saved := staffCheckPassword
	t.Cleanup(func() { staffCheckPassword = saved })
	staffCheckPassword = func(user, pass string) (string, error) {
		switch {
		case user == "Bal" && pass == "right":
			return staffTestBal, nil
		case user == "Some Player" && pass == "right":
			return staffTestPlayer, nil
		}
		return "", errStaffBadLogin
	}
	r := adminTestRouter()
	login := func(user, pass string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := login("Bal", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	if rec := login("Some Player", "right"); rec.Code != http.StatusForbidden {
		t.Errorf("non-staff sign-in: %d", rec.Code)
	}
	// First sign-in: the password is right, so 2FA is set up (no session yet).
	rec := login("Bal", "right")
	var setup struct {
		TOTP       string `json:"totp"`
		LoginToken string `json:"login_token"`
		Secret     string `json:"secret"`
		QR         string `json:"qr"`
		Token      string `json:"token"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &setup) != nil || setup.TOTP != "setup" ||
		setup.Secret == "" || setup.Token != "" || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Fatalf("first sign-in: %d %s", rec.Code, rec.Body.String())
	}
	code := func(secret string, at time.Time) string {
		raw, _ := totpEncoding.DecodeString(secret)
		return totpCode(raw, at.Unix()/staffTOTPStep)
	}
	totp := func(token, c string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/login/totp", strings.NewReader(`{"login_token":"`+token+`","code":"`+c+`"}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := totp(setup.LoginToken, "000000"); rec.Code != http.StatusUnauthorized && code(setup.Secret, time.Now()) != "000000" {
		t.Errorf("wrong setup code accepted: %d", rec.Code)
	}
	if s, _ := staffTOTPOf(currentMmogPlayerStateDB(), staffTestBal); s != "" {
		t.Fatal("2FA saved before a right code")
	}
	rec = totp(setup.LoginToken, code(setup.Secret, time.Now()))
	var out struct{ Token, Role string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" || out.Role != staffRoleBalancer {
		t.Fatalf("balancer sign-in: %d %s", rec.Code, rec.Body.String())
	}
	if s, _ := staffTOTPOf(currentMmogPlayerStateDB(), staffTestBal); s != setup.Secret {
		t.Fatal("2FA not saved")
	}
	if rec := totp(setup.LoginToken, code(setup.Secret, time.Now())); rec.Code != http.StatusUnauthorized {
		t.Errorf("a login token worked twice: %d", rec.Code)
	}
	// Next sign-in asks for a code; the one just used is refused (replay),
	// the next step's is accepted.
	rec = login("Bal", "right")
	var again struct {
		TOTP       string `json:"totp"`
		LoginToken string `json:"login_token"`
		Secret     string `json:"secret"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &again) != nil || again.TOTP != "code" || again.Secret != "" {
		t.Fatalf("second sign-in: %s", rec.Body.String())
	}
	if rec := totp(again.LoginToken, code(setup.Secret, time.Now())); rec.Code != http.StatusUnauthorized {
		t.Errorf("a used code was accepted again: %d", rec.Code)
	}
	if rec := totp(again.LoginToken, code(setup.Secret, time.Now().Add(staffTOTPStep*time.Second))); rec.Code != http.StatusOK {
		t.Errorf("the next code was refused: %d %s", rec.Code, rec.Body.String())
	}
	// A global admin resets it: the next sign-in sets it up again.
	if rec := adminPost(r, "/admin/api/staff/"+staffTestBal+"/totp-reset", "test-admin-key", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("2FA reset: %d", rec.Code)
	}
	if c := staffRequest(r, http.MethodGet, "/admin/api/whoami", out.Token, "").Code; c != http.StatusForbidden {
		t.Errorf("session survived a 2FA reset: %d", c)
	}
	rec = login("Bal", "right")
	if !strings.Contains(rec.Body.String(), `"totp":"setup"`) {
		t.Errorf("after a reset: %s", rec.Body.String())
	}
	out.Token, _, _ = staffNewSession(currentMmogPlayerStateDB(), staffTestBal, "127.0.0.1")
	if c := staffRequest(r, http.MethodGet, "/admin/api/tune", out.Token, "").Code; c == http.StatusForbidden {
		t.Errorf("balancer refused the balancing page")
	}
	// Signed out: the token is dead.
	req := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	req.Header.Set("X-Admin-Session", out.Token)
	r.ServeHTTP(httptest.NewRecorder(), req)
	if c := staffRequest(r, http.MethodGet, "/admin/api/whoami", out.Token, "").Code; c != http.StatusForbidden {
		t.Errorf("token works after sign-out: %d", c)
	}
	// Too many attempts from one address.
	for i := 0; i < staffLoginLimit; i++ {
		login("Bal", "wrong")
	}
	if rec := login("Bal", "right"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("no sign-in limit: %d", rec.Code)
	}
}

// In game: staff commands through the MMOG chat path the client uses.
func TestStaffChatCommands(t *testing.T) {
	staffTestSetup(t)
	hub := socialTestHub(t)
	saved := socialHubInstance
	socialHubInstance = hub
	t.Cleanup(func() { socialHubInstance = saved })
	_, modRead := socialTestPeer(t, hub, staffTestMod)
	modPush := pushLines(modRead)
	_, plRead := socialTestPeer(t, hub, staffTestPlayer)
	plPush := pushLines(plRead)
	database := currentMmogPlayerStateDB()

	say := func(pid, line string) {
		_ = buildMmogChatPayload("YA_GlobalChat", pid, protocol.AppendStringField(nil, "message", line))
	}
	next := func(ch <-chan map[string]any) string {
		t.Helper()
		for {
			typ, data := nextPush(t, ch)
			if typ == "chat.channel.message" {
				return unstamped2(data["text"])
			}
		}
	}

	say(staffTestMod, "/mute Some Player 2 flooding chat")
	if got := next(modPush); got != "Muted Some Player for 2 hour(s)." {
		t.Errorf("mute reply %q", got)
	}
	if got := next(plPush); !strings.HasPrefix(got, "You were muted for 2 hour(s): flooding chat") {
		t.Errorf("muted player told %q", got)
	}
	if !adminChatMuted(staffTestPlayer) {
		t.Error("not muted")
	}
	say(staffTestMod, `/warn "Some Player" mind your language`)
	if got := next(plPush); got != "Warning from moderator Mod One: mind your language" {
		t.Errorf("warning %q", got)
	}
	next(modPush)
	say(staffTestMod, "/ban Some Player 0 cheating")
	if got := next(modPush); !strings.Contains(got, "permanent ban needs a global admin") {
		t.Errorf("moderator permanent ban reply %q", got)
	}
	say(staffTestMod, "/ban Boss 24 nope")
	if got := next(modPush); got != "You cannot use /ban on a global admin." {
		t.Errorf("ban of an admin %q", got)
	}
	say(staffTestMod, "/help")
	next(modPush)
	if got := next(modPush); !strings.HasPrefix(got, "Staff commands (moderator): /who") {
		t.Errorf("staff help %q", got)
	}
	// A plain player cannot use them, and the line is not stored as chat.
	say(staffTestPlayer, "/ban Mod One 24 revenge")
	if got := next(plPush); got != "That command is for staff only." {
		t.Errorf("player /ban %q", got)
	}
	if adminPlayerBanned(staffTestMod) {
		t.Error("a player banned a moderator")
	}
	var actor string
	_ = database.QueryRow(`SELECT actor FROM admin_audit WHERE action='mute' ORDER BY id DESC LIMIT 1`).Scan(&actor)
	if actor != "Mod One (moderator)" {
		t.Errorf("mute audit actor %q", actor)
	}
	// A balancer has /who only.
	_, balRead := socialTestPeer(t, hub, staffTestBal)
	balPush := pushLines(balRead)
	say(staffTestBal, "/kick Some Player")
	if got := next(balPush); got != "Your role (balancer) cannot use /kick." {
		t.Errorf("balancer kick %q", got)
	}
	say(staffTestBal, "/who")
	if got := next(balPush); !strings.Contains(got, "online:") || !strings.Contains(got, "Some Player") {
		t.Errorf("who %q", got)
	}
}

// RFC 6238 appendix B (SHA-1, 8 digits there; the last 6 here).
func TestTOTPMatchesTheRFCVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, unix/staffTOTPStep); got != want {
			t.Errorf("t=%d: %s, want %s", unix, got, want)
		}
	}
	b32 := totpEncoding.EncodeToString(secret)
	if step := totpMatch(b32, "287082", time.Unix(59, 0)); step != 1 {
		t.Errorf("match step %d", step)
	}
	if step := totpMatch(b32, "287082", time.Unix(59+90, 0)); step != 0 {
		t.Errorf("a code three steps old matched (step %d)", step)
	}
}
