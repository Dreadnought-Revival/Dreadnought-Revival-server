package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// Renaming an account (admin dashboard, 2026-11-07). The account name is
// what the player signs in with (besides the email) and what the game shows:
// mmogbrain takes it from the launcher's JWT and keeps its own copy in
// player_state.display_name, which the dashboard updates in the same step.
//
// GUESS: the name rules. Registration only refuses an empty name; the
// original game's rules are not known. A rename takes 3-20 letters, digits,
// spaces, '-', '_' and '.', starting and ending with a letter or digit: no
// '#' (the game shows names as Name#1234 and splits at it) and no '@' (a
// sign-in name must not look like an email).
var renameNamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 ._-]{1,18})[A-Za-z0-9]$`)

// ValidAccountName reports whether name is acceptable for a rename, and why not.
func ValidAccountName(name string) (bool, string) {
	if len(name) < 3 || len(name) > 20 {
		return false, "the name must be 3 to 20 characters"
	}
	if !renameNamePattern.MatchString(name) || strings.Contains(name, "  ") {
		return false, "use letters, digits, single spaces, '-', '_' and '.', starting and ending with a letter or digit"
	}
	return true, ""
}

// AdminRename handles POST /admin/rename {user_id, username}: the account's
// new name, if it is valid and no other account has it (ignoring case). A
// change of case only is allowed.
func (h *Handler) AdminRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.UserID == "" {
		writeGreyboxError(w, http.StatusBadRequest, -32602, "user_id and username required")
		return
	}
	name := strings.TrimSpace(req.Username)
	if ok, why := ValidAccountName(name); !ok {
		writeGreyboxError(w, http.StatusBadRequest, -32602, why)
		return
	}
	var old string
	userID, err := h.adminTargetUser(&old, req.UserID)
	if err == sql.ErrNoRows {
		writeGreyboxError(w, http.StatusNotFound, -32001, "user not found")
		return
	}
	if err != nil {
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "db error")
		return
	}
	var other string
	err = h.DB.QueryRow(`SELECT id FROM users WHERE (username=? COLLATE NOCASE OR email=? COLLATE NOCASE) AND id<>?`, name, name, userID).Scan(&other)
	if err == nil {
		writeGreyboxError(w, http.StatusConflict, -32002, "that name is already taken")
		return
	}
	if err != sql.ErrNoRows {
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "db error")
		return
	}
	if _, err := h.DB.Exec(`UPDATE users SET username=? WHERE id=?`, name, userID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeGreyboxError(w, http.StatusConflict, -32002, "that name is already taken")
			return
		}
		writeGreyboxError(w, http.StatusInternalServerError, -32603, "rename failed")
		return
	}
	h.Log.WithField("user_id", userID).WithField("from", old).WithField(fieldUsername, name).Warn("account renamed by admin")
	writeJSON(w, http.StatusOK, map[string]string{fieldStatus: "renamed", "from": old, fieldUsername: name})
}
