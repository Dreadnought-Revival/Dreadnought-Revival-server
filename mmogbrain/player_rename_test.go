package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A dashboard rename goes through the auth server, which refuses a taken
// name; on success the player's name here changes, and a launcher token
// issued before the rename logs in under the new name.
func TestAdminRenamePlayer(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name='OldName' WHERE user_id=?`, adminTestPID); err != nil {
		t.Fatal(err)
	}
	var gotBody string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path != "/admin/rename" {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(gotBody, "Taken") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"that name is already taken","code":-32002,"message":"that name is already taken"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"renamed","from":"OldName","username":"NewName"}`))
	}))
	defer auth.Close()
	r := adminTestRouter() // resets adminDash: set the auth URL after it
	oldURL := adminDash.authURL
	adminDash.authURL = auth.URL
	defer func() { adminDash.authURL = oldURL }()
	base := "/admin/api/players/" + adminTestPID + "/rename"

	if rec := adminPost(r, base, "", `{"name":"NewName"}`); rec.Code != http.StatusForbidden {
		t.Errorf("without the key: %d", rec.Code)
	}
	if rec := adminPost(r, base, "test-admin-key", `{"name":"  "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty name: %d", rec.Code)
	}
	rec := adminPost(r, base, "test-admin-key", `{"name":"Taken"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already taken") {
		t.Errorf("taken name: %d %s, want the auth server's message", rec.Code, rec.Body.String())
	}
	if n := mmogPlayerStateForPID(adminTestPID).displayName; n != "OldName" {
		t.Fatalf("a refused rename changed the name to %q", n)
	}

	issuedBefore := time.Now().Add(-time.Hour)
	rec = adminPost(r, base, "test-admin-key", `{"name":"NewName"}`)
	if rec.Code != http.StatusOK || !strings.Contains(gotBody, adminTestPID) {
		t.Fatalf("rename: %d %s (auth got %s)", rec.Code, rec.Body.String(), gotBody)
	}
	var detail struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.Name != "NewName" {
		t.Errorf("detail after rename: %s", rec.Body.String())
	}
	if n := mmogPlayerStateForPID(adminTestPID).displayName; n != "NewName" {
		t.Errorf("display_name %q, want NewName", n)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='rename' AND target=?`, adminTestPID).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d rename audit rows, want 1", audits)
	}

	// The token issued before the rename still says OldName: the login uses
	// the new name. A token issued after it is taken as it is.
	old := jwt.MapClaims{"user_id": adminTestPID, "username": "OldName", "iat": float64(issuedBefore.Unix())}
	if got := accountNameAtLogin(adminTestPID, "OldName", old); got != "NewName" {
		t.Errorf("old token logs in as %q, want NewName", got)
	}
	fresh := jwt.MapClaims{"user_id": adminTestPID, "username": "NewName", "iat": float64(time.Now().Add(time.Minute).Unix())}
	if got := accountNameAtLogin(adminTestPID, "NewName", fresh); got != "NewName" {
		t.Errorf("fresh token logs in as %q", got)
	}
	// Never renamed: the token's name.
	const other = "0a0b0c0d0e0f40108011aabbccddeeff"
	if err := seedMmogPlayerState(database, other); err != nil {
		t.Fatal(err)
	}
	if got := accountNameAtLogin(other, "Someone", old); got != "Someone" {
		t.Errorf("unrenamed player logs in as %q, want the token's name", got)
	}
}
