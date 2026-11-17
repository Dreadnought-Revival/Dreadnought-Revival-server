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

// The ticket is not verified, so an unknown one must not create an account
// (it did, for any bytes, until 2026-11-17). An account the route made
// earlier still signs in.
func TestSteamTicketSignsInExistingAccountsOnly(t *testing.T) {
	h := newTestHandler(t)
	var before int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&before)

	rec := steamTicketLogin(t, h, "unknown-ticket")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown ticket: status %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	var after int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&after)
	if after != before {
		t.Fatalf("an unknown ticket created an account (%d -> %d users)", before, after)
	}

	sum := sha256.Sum256([]byte("known-ticket"))
	if _, err := h.DB.Exec(`INSERT INTO users(id, username, email, password_hash, steam_id) VALUES('u1','player_old','player_old@steam.private','x',?)`,
		fmt.Sprintf("%x", sum[:8])); err != nil {
		t.Fatal(err)
	}
	rec = steamTicketLogin(t, h, "known-ticket")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "player_old") {
		t.Fatalf("existing steam account: status %d body %s", rec.Code, rec.Body.String())
	}
}
