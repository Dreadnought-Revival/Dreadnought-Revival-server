package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Renaming a player from the admin dashboard (issue #14, 2026-11-07).
//
// The name lives in two places: the auth server's account (what the player
// signs in with, and what the launcher's JWT carries as "username") and
// player_state.display_name here (chat, friends, the dashboard). A rename
// changes both in one step and stamps player_state.name_changed_at.
//
// The game shows the name the gateway login sends ("Username", Name#1234),
// which came from the JWT -- a launcher token stays valid for 24 h, so a
// renamed player kept the old name until they signed out of the launcher
// and in again. Now a token issued before the rename gets the new name
// (accountNameAtLogin). The chat takes display_name when the player
// connects, so a player who is online sees the new name after their next
// login; the dashboard can sign them out to make that now.

// accountNameAtLogin is the name the gateway sends at login: the JWT's,
// unless the account was renamed after the token was issued.
func accountNameAtLogin(userID, tokenName string, claims jwt.MapClaims) string {
	database := currentMmogPlayerStateDB()
	if database == nil || userID == "" {
		return tokenName
	}
	var name string
	var changedAt int64
	if database.QueryRow(`SELECT display_name, name_changed_at FROM player_state WHERE user_id=?`,
		normalizedPlayerStatePID(userID)).Scan(&name, &changedAt) != nil || changedAt == 0 || name == "" {
		return tokenName
	}
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil || iat.Unix() < changedAt {
		return name
	}
	return tokenName
}

// adminAPIPlayerRename renames the account on the auth server (which checks
// the name and that it is free) and here. signout drops the player's
// connections so the game picks up the name now.
func adminAPIPlayerRename(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    string `json:"name"`
		SignOut bool   `json:"signout"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, `{"error":"a new name is required"}`, http.StatusBadRequest)
		return
	}
	reply, err := adminAuthPost("/admin/rename", map[string]string{"user_id": pid, "username": strings.TrimSpace(req.Name)})
	var out struct {
		From     string `json:"from"`
		Username string `json:"username"`
		Message  string `json:"message"`
	}
	_ = json.Unmarshal(reply, &out)
	if err != nil {
		msg := out.Message
		if msg == "" {
			msg = err.Error()
		}
		adminAudit(database, r, "rename failed", pid, map[string]any{"to": strings.TrimSpace(req.Name), "error": msg})
		writeAdminError(w, http.StatusBadRequest, msg)
		return
	}
	if out.Username == "" {
		writeAdminError(w, http.StatusBadGateway, "auth server sent no name")
		return
	}
	if _, err := database.Exec(`UPDATE player_state SET display_name=?, name_changed_at=?, updated_at=datetime('now') WHERE user_id=?`,
		out.Username, time.Now().Unix(), pid); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "renamed on the auth server, but not here: "+err.Error())
		return
	}
	signedOut := false
	if req.SignOut {
		signedOut = adminDisconnect(pid)
	}
	adminAudit(database, r, "rename", pid, map[string]any{"from": out.From, "to": out.Username, "signed_out": signedOut})
	adminAPIPlayerDetail(w, r)
}

func writeAdminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
