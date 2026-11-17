package handlers

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func steamTicketLogin(t *testing.T, h *Handler, ticket string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"id":"t","jsonrpc":"2.0","method":"jwt.get.by_steam_ticket","params":{"ticket":"` + ticket + `","audience":"launcher"}}`
	req := httptest.NewRequest(http.MethodPost, "/auth/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	return rec
}

// Steam-ticket sign-in is gone: an unknown ticket creates nothing, and a
// ticket of an account the route made earlier no longer signs it in.
func TestSteamTicketSignInRemoved(t *testing.T) {
	h := newTestHandler(t)
	sum := sha256.Sum256([]byte("known-ticket"))
	if _, err := h.DB.Exec(`INSERT INTO users(id, username, email, password_hash, steam_id) VALUES('u1','player_old','player_old@steam.private','x',?)`,
		fmt.Sprintf("%x", sum[:8])); err != nil {
		t.Fatal(err)
	}
	for _, ticket := range []string{"unknown-ticket", "known-ticket"} {
		rec := steamTicketLogin(t, h, ticket)
		if rec.Code != http.StatusGone || strings.Contains(rec.Body.String(), "eyJ") {
			t.Fatalf("%s: status %d, want 410 and no token; body %s", ticket, rec.Code, rec.Body.String())
		}
	}
	var users int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Fatalf("%d users, want 1 (nothing created)", users)
	}
}
