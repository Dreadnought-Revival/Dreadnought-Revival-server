package main

import (
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// Chat mutes: a muted player stays online and can play, but their chat lines
// (channel messages and direct messages) are not relayed or stored. The list is
// kept in memory (loaded lazily from admin_chat_mutes, like the ban list) so the
// chat path never queries the database per line; expiry is checked on read.
//
// Enforcement point: chatLineRefused (social_handlers.go), which both the
// channel-message and the direct-message handlers call. It answers the sender
// with the same socialError reply as the length and flood limits ("muted").
// The binary MMOG chat path (buildMmogChatPayload) relays by storing, so a
// muted line is simply not stored there. THEORY: how the real client shows
// the "muted" error, and whether it still uses the binary path, is unverified.

func init() { adminRegisterAPI(registerAdminMutes) }

func registerAdminMutes(api *mux.Router) {
	adminStartSweeper() // once; also covers the ban expiry sweep
	api.HandleFunc("/players/{pid}/mute", adminAPIPlayerMute).Methods(http.MethodPost)
	api.HandleFunc("/players/{pid}/unmute", adminAPIPlayerUnmute).Methods(http.MethodPost)
	api.HandleFunc("/mutes", adminAPIMutes).Methods(http.MethodGet)
}

var adminMutes = struct {
	mu     sync.Mutex
	loaded bool
	until  map[string]time.Time // pid -> end; the zero time = permanent
}{until: map[string]time.Time{}}

func adminLoadMutes() {
	adminMutes.mu.Lock()
	loaded := adminMutes.loaded
	adminMutes.mu.Unlock()
	database := currentMmogPlayerStateDB()
	if loaded || database == nil {
		return
	}
	set := map[string]time.Time{}
	rows, err := database.Query(`SELECT user_id, expires_at FROM admin_chat_mutes`)
	if err != nil {
		return
	}
	for rows.Next() {
		var pid, exp string
		if rows.Scan(&pid, &exp) == nil {
			end, _ := adminParseExpiry(exp) // '' -> zero = permanent
			set[pid] = end
		}
	}
	_ = rows.Close()
	adminMutes.mu.Lock()
	if !adminMutes.loaded {
		adminMutes.until, adminMutes.loaded = set, true
	}
	adminMutes.mu.Unlock()
}

// adminChatMuted reports whether the player (any id form) may not chat now.
func adminChatMuted(pid string) bool {
	adminLoadMutes()
	pid = normalizedPlayerStatePID(pid)
	adminMutes.mu.Lock()
	defer adminMutes.mu.Unlock()
	end, ok := adminMutes.until[pid]
	if !ok {
		return false
	}
	return end.IsZero() || time.Now().Before(end)
}

// adminUnmutePlayer removes the mute from the database and the memory list.
func adminUnmutePlayer(database *sql.DB, pid string) error {
	adminLoadMutes()
	if _, err := database.Exec(`DELETE FROM admin_chat_mutes WHERE user_id=?`, pid); err != nil {
		return err
	}
	adminMutes.mu.Lock()
	delete(adminMutes.until, pid)
	adminMutes.mu.Unlock()
	return nil
}

func adminAPIPlayerMute(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	reason, hours, ok := adminReasonHours(w, r, true)
	if !ok {
		return
	}
	expires, err := adminMutePlayer(database, pid, reason, hours)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminAudit(database, r, "mute", pid, map[string]any{"reason": reason, "hours": hours, "expires_at": expires})
	writeAdminJSON(w, map[string]any{"muted": true, "expires_at": expires})
}

// adminMutePlayer mutes pid for hours (0 = permanent): the row and the
// in-memory list. Shared by the dashboard and the in-game /mute.
func adminMutePlayer(database *sql.DB, pid, reason string, hours int) (string, error) {
	adminLoadMutes()
	expires := adminExpiryAfter(hours)
	if _, err := database.Exec(`INSERT INTO admin_chat_mutes(user_id,reason,expires_at) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET reason=excluded.reason, expires_at=excluded.expires_at`, pid, reason, expires); err != nil {
		return "", err
	}
	end, _ := adminParseExpiry(expires)
	adminMutes.mu.Lock()
	adminMutes.until[pid] = end
	adminMutes.mu.Unlock()
	return expires, nil
}

func adminAPIPlayerUnmute(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	reason, ok := adminReason(w, r, false)
	if !ok {
		return
	}
	if err := adminUnmutePlayer(database, pid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminAudit(database, r, "unmute", pid, map[string]any{"reason": reason})
	writeAdminJSON(w, map[string]any{"muted": false})
}

func adminAPIMutes(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	type row struct {
		PID       string `json:"pid"`
		Name      string `json:"name"`
		Reason    string `json:"reason"`
		At        string `json:"at"`
		ExpiresAt string `json:"expires_at"`
	}
	out := []row{}
	rows, err := database.Query(`SELECT m.user_id, COALESCE(NULLIF(ps.display_name,'Local'),'?'), m.reason, m.created_at, m.expires_at
		FROM admin_chat_mutes m LEFT JOIN player_state ps ON ps.user_id=m.user_id
		WHERE m.expires_at='' OR m.expires_at>datetime('now') ORDER BY m.created_at DESC`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var m row
		if rows.Scan(&m.PID, &m.Name, &m.Reason, &m.At, &m.ExpiresAt) == nil {
			out = append(out, m)
		}
	}
	_ = rows.Close()
	writeAdminJSON(w, map[string]any{"mutes": out})
}
