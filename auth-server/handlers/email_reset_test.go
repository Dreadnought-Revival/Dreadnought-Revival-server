package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeMail records what would have been mailed.
type fakeMail struct{ sent chan [3]string }

func (f *fakeMail) Send(to, subject, body string) error {
	f.sent <- [3]string{to, subject, body}
	return nil
}

func withMail(t *testing.T) (*Handler, *fakeMail) {
	t.Helper()
	h := newTestHandler(t)
	m := &fakeMail{sent: make(chan [3]string, 8)}
	h.Mail = m
	return h, m
}

func forgot(h *Handler, email string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ForgotPassword(rec, httptest.NewRequest(http.MethodPost, "/auth/password/forgot", strings.NewReader(`{"email":"`+email+`"}`)))
	return rec
}

func resetWith(h *Handler, email, code, password string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ResetPassword(rec, httptest.NewRequest(http.MethodPost, "/auth/password/reset",
		strings.NewReader(`{"email":"`+email+`","code":"`+code+`","password":"`+password+`"}`)))
	return rec
}

var mailedCode = regexp.MustCompile(`\b(\d{8})\b`)

func waitMail(t *testing.T, m *fakeMail) [3]string {
	t.Helper()
	select {
	case got := <-m.sent:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no mail sent")
	}
	return [3]string{}
}

func noMail(t *testing.T, m *fakeMail) {
	t.Helper()
	select {
	case got := <-m.sent:
		t.Fatalf("unexpected mail to %s", got[0])
	case <-time.After(200 * time.Millisecond):
	}
}

// The whole flow: the code arrives at the account's address, a wrong code is
// refused, the right one sets the password, signs the account out, and works
// only once.
func TestEmailResetFlow(t *testing.T) {
	h, m := withMail(t)
	register(t, h, "Pilot", "pilot@example.com", "oldpass1")
	if rec := forgot(h, "PILOT@example.com"); rec.Code != http.StatusOK {
		t.Fatalf("forgot: %d", rec.Code)
	}
	mail := waitMail(t, m)
	if mail[0] != "pilot@example.com" || !strings.Contains(mail[2], "Hello Pilot") {
		t.Fatalf("mail %q", mail)
	}
	code := mailedCode.FindStringSubmatch(mail[2])[1]
	if rec := resetWith(h, "pilot@example.com", "00000000", "newpass1"); rec.Code != http.StatusBadRequest && code != "00000000" {
		t.Errorf("wrong code accepted: %d", rec.Code)
	}
	if rec := resetWith(h, "pilot@example.com", code, "short"); rec.Code != http.StatusBadRequest {
		t.Errorf("short password accepted: %d", rec.Code)
	}
	if rec := resetWith(h, "pilot@example.com", code[:4]+" "+code[4:], "newpass1"); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := passwordLogin(t, h, "Pilot", "newpass1"); rec.Code != http.StatusOK {
		t.Errorf("new password does not sign in: %d", rec.Code)
	}
	if rec := passwordLogin(t, h, "Pilot", "oldpass1"); rec.Code == http.StatusOK {
		t.Error("old password still signs in")
	}
	var sessions int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.username='Pilot'`).Scan(&sessions)
	if sessions != 1 { // only the sign-in just made with the new password
		t.Errorf("%d sessions after the reset, want only the new sign-in", sessions)
	}
	if rec := resetWith(h, "pilot@example.com", code, "another1"); rec.Code != http.StatusBadRequest {
		t.Errorf("a code worked twice: %d", rec.Code)
	}
}

// Nobody learns whether an address has an account: the same answer, no mail.
func TestEmailResetUnknownAddressLooksTheSame(t *testing.T) {
	h, m := withMail(t)
	register(t, h, "Pilot", "pilot@example.com", "oldpass1")
	known, unknown := forgot(h, "pilot@example.com"), forgot(h, "nobody@example.com")
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Errorf("answers differ: %d %q / %d %q", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
	waitMail(t, m)
	noMail(t, m)
}

// Five wrong codes spend it; mails are limited to one a minute.
func TestEmailResetLimits(t *testing.T) {
	h, m := withMail(t)
	register(t, h, "Pilot", "pilot@example.com", "oldpass1")
	forgot(h, "pilot@example.com")
	code := mailedCode.FindStringSubmatch(waitMail(t, m)[2])[1]
	forgot(h, "pilot@example.com") // within the minute
	noMail(t, m)
	wrong := "11111111"
	if wrong == code {
		wrong = "22222222"
	}
	for i := 0; i < resetMaxAttempts; i++ {
		resetWith(h, "pilot@example.com", wrong, "newpass1")
	}
	if rec := resetWith(h, "pilot@example.com", code, "newpass1"); rec.Code != http.StatusBadRequest {
		t.Errorf("the right code worked after %d wrong ones: %d", resetMaxAttempts, rec.Code)
	}
	// An expired code.
	_, _ = h.DB.Exec(`UPDATE password_resets SET sent_at='2000-01-01 00:00:00'`)
	forgot(h, "pilot@example.com")
	code = mailedCode.FindStringSubmatch(waitMail(t, m)[2])[1]
	_, _ = h.DB.Exec(`UPDATE password_resets SET expires_at='2000-01-01 00:00:00'`)
	if rec := resetWith(h, "pilot@example.com", code, "newpass1"); rec.Code != http.StatusBadRequest {
		t.Errorf("an expired code worked: %d", rec.Code)
	}
}

func TestEmailResetOffWithoutSMTP(t *testing.T) {
	h := newTestHandler(t)
	if rec := forgot(h, "pilot@example.com"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("forgot without SMTP: %d", rec.Code)
	}
}
