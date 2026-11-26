package main

// Two-factor sign-in for staff (2026-11-25; operator: "for staff account we
// need ... totp as 2fa"). Every staff dashboard sign-in is password (checked
// by the auth server, staff.go) AND a 6-digit code from an authenticator app
// (RFC 6238: HMAC-SHA1, 30 s steps, 6 digits; Google Authenticator, Microsoft
// Authenticator, Authy, 1Password ... all read it).
//
//	POST /admin/login {username, password}
//	  -> {"totp": "setup", "login_token", "secret", "otpauth", "qr"}  first time:
//	     scan the QR (or type the secret) into the app
//	  -> {"totp": "code", "login_token"}                             afterwards
//	POST /admin/login/totp {login_token, code}
//	  -> {"token", "role", "name", "expires_at"}                     signed in
//
// A login token is single-use, lives 5 minutes (10 while setting up) and
// allows staffTOTPTries codes. A setup is saved only when its first code is
// right. A code is accepted once (staff.totp_step), one step either side of
// now (clock drift). A global admin can reset someone's 2FA (Staff page); they
// set it up again at their next sign-in. The admin key needs no code: it is
// the break-glass login, kept in run/secrets.env.
//
// In-game staff commands need no code: the player is already signed in to
// the game with their password, and those commands are limited to
// moderation.
//
// The secret is stored as plain base32 in mmog.db (like the auth server's
// password hashes in auth.db, the database is the server's own); the session
// token is stored only as a hash.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	staffTOTPIssuer = "Dreadnought Revival"
	staffTOTPStep   = 30 // seconds
	staffTOTPDigits = 6
	staffTOTPTries  = 5
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpCode is the RFC 6238 code for a time step.
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", staffTOTPDigits, v%1000000)
}

// totpMatch returns the time step code is valid for (now, one before or one
// after), or 0.
func totpMatch(secretB32, code string, now time.Time) int64 {
	secret, err := totpEncoding.DecodeString(strings.ToUpper(secretB32))
	if err != nil || len(code) != staffTOTPDigits {
		return 0
	}
	cur := now.Unix() / staffTOTPStep
	for _, step := range []int64{cur, cur - 1, cur + 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			return step
		}
	}
	return 0
}

func newTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(b), nil
}

// --- pending sign-ins --------------------------------------------------------

type staffPendingLogin struct {
	pid     string
	secret  string // set: a first-time setup with this secret
	expires time.Time
	tries   int
}

var staffPending = struct {
	mu sync.Mutex
	m  map[string]*staffPendingLogin // token -> pending
}{m: map[string]*staffPendingLogin{}}

func staffNewPending(pid, setupSecret string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	ttl := 5 * time.Minute
	if setupSecret != "" {
		ttl = 10 * time.Minute
	}
	staffPending.mu.Lock()
	defer staffPending.mu.Unlock()
	for k, p := range staffPending.m {
		if time.Now().After(p.expires) {
			delete(staffPending.m, k)
		}
	}
	staffPending.m[token] = &staffPendingLogin{pid: pid, secret: setupSecret, expires: time.Now().Add(ttl)}
	return token, nil
}

// staffTOTPOf is a staff member's secret and last accepted step.
func staffTOTPOf(database *sql.DB, pid string) (secret string, step int64) {
	_ = database.QueryRow(`SELECT totp_secret, totp_step FROM staff WHERE user_id=?`, pid).Scan(&secret, &step)
	return
}

// staffLoginSecondStep answers a correct password: set up 2FA, or ask for
// the code. Called by staffAPILogin.
func staffLoginSecondStep(w http.ResponseWriter, database *sql.DB, pid string) {
	secret, _ := staffTOTPOf(database, pid)
	if secret != "" {
		token, err := staffNewPending(pid, "")
		if err != nil {
			http.Error(w, `{"error":"could not start the sign-in"}`, http.StatusInternalServerError)
			return
		}
		writeAdminJSON(w, map[string]any{"totp": "code", "login_token": token})
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		http.Error(w, `{"error":"could not start the sign-in"}`, http.StatusInternalServerError)
		return
	}
	token, err := staffNewPending(pid, secret)
	if err != nil {
		http.Error(w, `{"error":"could not start the sign-in"}`, http.StatusInternalServerError)
		return
	}
	label := url.PathEscape(staffTOTPIssuer + ":" + adminPlayerName(pid))
	uri := "otpauth://totp/" + label + "?" + url.Values{"secret": {secret}, "issuer": {staffTOTPIssuer},
		"digits": {"6"}, "period": {"30"}, "algorithm": {"SHA1"}}.Encode()
	png, err := qrcode.Encode(uri, qrcode.Medium, 220)
	qr := ""
	if err == nil {
		qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	writeAdminJSON(w, map[string]any{"totp": "setup", "login_token": token, "secret": secret, "otpauth": uri, "qr": qr})
}

// staffAPILoginTOTP finishes a sign-in with the authenticator code.
func staffAPILoginTOTP(w http.ResponseWriter, r *http.Request) {
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
		Token string `json:"login_token"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		fail(http.StatusBadRequest, "invalid body")
		return
	}
	code := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, req.Code)
	database := currentMmogPlayerStateDB()
	if database == nil {
		fail(http.StatusServiceUnavailable, "database not ready")
		return
	}
	staffPending.mu.Lock()
	p, ok := staffPending.m[req.Token]
	if ok && time.Now().After(p.expires) {
		delete(staffPending.m, req.Token)
		ok = false
	}
	var pending staffPendingLogin
	if ok {
		p.tries++
		if p.tries > staffTOTPTries {
			delete(staffPending.m, req.Token)
			ok = false
		} else {
			pending = *p
		}
	}
	staffPending.mu.Unlock()
	if !ok {
		fail(http.StatusUnauthorized, "this sign-in expired; start again")
		return
	}
	role := staffRoleOf(pending.pid)
	if role == "" || adminPlayerBanned(pending.pid) {
		fail(http.StatusForbidden, "this account is not staff")
		return
	}
	secret, lastStep := pending.secret, int64(0)
	if secret == "" {
		secret, lastStep = staffTOTPOf(database, pending.pid)
	}
	step := totpMatch(secret, code, time.Now())
	name := adminPlayerName(pending.pid)
	if step == 0 || step <= lastStep {
		adminAuditActor(database, ip, "staff sign-in refused", pending.pid, map[string]any{"reason": "wrong 2FA code"})
		fail(http.StatusUnauthorized, "wrong code; check the time on your phone")
		return
	}
	staffPending.mu.Lock()
	delete(staffPending.m, req.Token)
	staffPending.mu.Unlock()
	if pending.secret != "" {
		if _, err := database.Exec(`UPDATE staff SET totp_secret=?, totp_step=? WHERE user_id=?`, pending.secret, step, pending.pid); err != nil {
			fail(http.StatusInternalServerError, "could not save the 2FA setup")
			return
		}
		adminAuditActor(database, name+" ("+staffRoleName(role)+")", "staff 2fa set up", pending.pid, map[string]any{"ip": ip})
	} else if _, err := database.Exec(`UPDATE staff SET totp_step=? WHERE user_id=?`, step, pending.pid); err != nil {
		fail(http.StatusInternalServerError, "could not sign in")
		return
	}
	token, expires, err := staffNewSession(database, pending.pid, ip)
	if err != nil {
		fail(http.StatusInternalServerError, "could not start a session")
		return
	}
	adminAuditActor(database, name+" ("+staffRoleName(role)+")", "staff sign-in", pending.pid, map[string]any{"ip": ip})
	writeAdminJSON(w, map[string]any{"token": token, "expires_at": expires.Format(time.RFC3339), "name": name, "role": role})
}

// adminAPIStaffTOTPReset clears a staff member's 2FA (lost phone); they set
// it up again at their next sign-in. Their sessions end.
func adminAPIStaffTOTPReset(w http.ResponseWriter, r *http.Request) {
	pid := normalizedPlayerStatePID(mux.Vars(r)["pid"])
	database := currentMmogPlayerStateDB()
	if database == nil || staffRoleOf(pid) == "" {
		http.Error(w, `{"error":"not staff"}`, http.StatusNotFound)
		return
	}
	if _, err := database.Exec(`UPDATE staff SET totp_secret='', totp_step=0 WHERE user_id=?`, pid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	staffEndSessionsOf(database, pid)
	adminAudit(database, r, "staff 2fa reset", pid, nil)
	writeAdminJSON(w, map[string]any{"reset": true})
}

// staffEndSessionsOf signs a staff member out everywhere.
func staffEndSessionsOf(database *sql.DB, pid string) {
	_, _ = database.Exec(`DELETE FROM staff_sessions WHERE user_id=?`, pid)
	staffSessions.mu.Lock()
	for h, s := range staffSessions.byHash {
		if s.pid == pid {
			delete(staffSessions.byHash, h)
		}
	}
	staffSessions.mu.Unlock()
}
