package handlers

// Password reset by email (2026-11-25; operator: "implement the email system
// for players to reset their account password"). The launcher's "Forgot
// password?" calls:
//
//	POST /auth/password/forgot {"email"}                      -> 200 always
//	POST /auth/password/reset  {"email", "code", "password"}  -> 200 or 400
//
// forgot mails an 8-digit code to the account's address (the email, or the
// captain name, given). It answers the same whether or not an account uses
// that address, so it cannot be used to find out who plays here; the mail
// goes out in the background for the same reason. A code lives
// resetCodeTTL, allows resetMaxAttempts wrong tries, and only its hash is
// stored. At most one mail a minute and resetMaxPerDay a day per account.
// reset sets the new password and signs the account out everywhere, like
// the admin reset (password_reset.go).
//
// Staff accounts are player accounts: they reset the same way (their
// dashboard 2FA is separate and stays; mmogbrain staff_totp.go).
//
// The mail is sent through SMTP (smtp_mail.go, SMTP_* in run/secrets.env);
// without SMTP_HOST both routes answer 503 and the launcher says the server
// cannot send email.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	resetCodeTTL     = 15 * time.Minute
	resetMaxAttempts = 5
	resetMaxPerDay   = 5
	resetMinInterval = time.Minute
	resetTimeLayout  = "2006-01-02 15:04:05"
)

// Mailer sends one plain-text email.
type Mailer interface {
	Send(to, subject, body string) error
}

func resetCodeHash(userID, code string) string {
	sum := sha256.Sum256([]byte(userID + ":" + code))
	return hex.EncodeToString(sum[:])
}

func newResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}

// resetAccount finds the account an email (or captain name) belongs to.
func (h *Handler) resetAccount(identifier string) (id, username, email string, err error) {
	err = h.DB.QueryRow(`SELECT id, username, email FROM users
		WHERE (email=? COLLATE NOCASE OR username=? COLLATE NOCASE) AND banned_at IS NULL
		ORDER BY (email=? OR username=?) DESC LIMIT 1`, identifier, identifier, identifier, identifier).Scan(&id, &username, &email)
	return
}

// ForgotPassword handles POST /auth/password/forgot.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if h.Mail == nil {
		writeGreyboxError(w, http.StatusServiceUnavailable, -32010, "This server cannot send email yet. Ask a moderator to reset your password.")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeGreyboxError(w, http.StatusBadRequest, -32602, "Enter the email address of your account.")
		return
	}
	// The same answer whatever happens below.
	defer writeJSON(w, http.StatusOK, map[string]any{"ok": true,
		"message": "If an account uses that address, a code is on its way. It works for 15 minutes."})

	id, username, email, err := h.resetAccount(strings.TrimSpace(req.Email))
	if err != nil || email == "" {
		return
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	var sentAt, day string
	var count int
	if h.DB.QueryRow(`SELECT sent_at, day, day_count FROM password_resets WHERE user_id=?`, id).Scan(&sentAt, &day, &count) == nil {
		if t, err := time.Parse(resetTimeLayout, sentAt); err == nil && now.Sub(t) < resetMinInterval {
			return
		}
		if day == today && count >= resetMaxPerDay {
			h.Log.WithField(fieldUserID, id).Warn("password reset: daily mail limit reached")
			return
		}
		if day != today {
			count = 0
		}
	}
	code, err := newResetCode()
	if err != nil {
		h.Log.WithError(err).Error("password reset: code")
		return
	}
	if _, err := h.DB.Exec(`INSERT INTO password_resets(user_id,code_hash,expires_at,attempts,sent_at,day,day_count) VALUES(?,?,?,0,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET code_hash=excluded.code_hash, expires_at=excluded.expires_at, attempts=0,
		sent_at=excluded.sent_at, day=excluded.day, day_count=excluded.day_count`,
		id, resetCodeHash(id, code), now.Add(resetCodeTTL).Format(resetTimeLayout), now.Format(resetTimeLayout), today, count+1); err != nil {
		h.Log.WithError(err).Error("password reset: store")
		return
	}
	body := fmt.Sprintf(`Hello %s,

your code to reset your Dreadnought Revival password is:

    %s

Enter it in the launcher (Sign in > Forgot password?) within 15 minutes.

If you did not ask for this, ignore this email: your password stays as it is.

-- Dreadnought Revival
`, username, code)
	mail := h.Mail
	go func() {
		if err := mail.Send(email, "Your Dreadnought Revival password reset code", body); err != nil {
			// Never the address in the log: it is private.
			h.Log.WithError(err).WithField(fieldUserID, id).Warn("password reset: mail not sent")
			return
		}
		h.Log.WithField(fieldUserID, id).Info("password reset: code mailed")
	}()
}

// ResetPassword handles POST /auth/password/reset.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if h.Mail == nil {
		writeGreyboxError(w, http.StatusServiceUnavailable, -32010, "This server cannot send email yet. Ask a moderator to reset your password.")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeGreyboxError(w, http.StatusBadRequest, -32602, "invalid request")
		return
	}
	if len(req.Password) < 6 || len(req.Password) > 256 {
		writeGreyboxError(w, http.StatusBadRequest, -32602, "Use 6 to 256 characters for the password.")
		return
	}
	code := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, req.Code)
	bad := func() {
		writeGreyboxError(w, http.StatusBadRequest, -32011, "That code is wrong or has expired. Ask for a new one.")
	}
	id, _, _, err := h.resetAccount(strings.TrimSpace(req.Email))
	if err != nil {
		bad()
		return
	}
	var hash, expires string
	var attempts int
	if err := h.DB.QueryRow(`SELECT code_hash, expires_at, attempts FROM password_resets WHERE user_id=?`, id).Scan(&hash, &expires, &attempts); err != nil {
		bad()
		return
	}
	end, err := time.Parse(resetTimeLayout, expires)
	if err != nil || time.Now().UTC().After(end) || attempts >= resetMaxAttempts || hash == "" {
		bad()
		return
	}
	if len(code) != 8 || subtle.ConstantTimeCompare([]byte(resetCodeHash(id, code)), []byte(hash)) != 1 {
		_, _ = h.DB.Exec(`UPDATE password_resets SET attempts=attempts+1 WHERE user_id=?`, id)
		if attempts+1 >= resetMaxAttempts {
			// Spent: the code can no longer be used (the row keeps the mail limit).
			_, _ = h.DB.Exec(`UPDATE password_resets SET code_hash='' WHERE user_id=?`, id)
		}
		bad()
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "password hashing failed")
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "db error")
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE users SET password_hash=? WHERE id=?`, []any{string(newHash), id}},
		// Signed out everywhere, as after an admin reset.
		{`DELETE FROM sessions WHERE user_id=?`, []any{id}},
		// The code is used up; the row stays for the daily mail limit.
		{`UPDATE password_resets SET code_hash='', attempts=0 WHERE user_id=?`, []any{id}},
	} {
		if _, err := tx.Exec(q.sql, q.args...); err != nil {
			writeGreyboxError(w, http.StatusInternalServerError, -32603, "reset failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "reset failed")
		return
	}
	h.Log.WithField(fieldUserID, id).Warn("password reset by email code")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Your password is changed. Sign in with the new one."})
}
