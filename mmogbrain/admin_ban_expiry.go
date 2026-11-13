package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Timed bans and chat mutes. admin_bans.expires_at / admin_chat_mutes.expires_at
// are '' for permanent, else UTC "YYYY-MM-DD HH:MM:SS" (the format
// datetime('now') writes, so they compare as text). An expired ban counts as
// lifted immediately (adminBanActiveLocked); a sweeper then removes the row
// through the same path as a manual unban and records it as a system action.

const (
	adminExpiryLayout   = "2006-01-02 15:04:05"
	adminMaxSanctionHrs = 365 * 24
	adminSweepEvery     = time.Minute
)

// adminParseExpiry reads a stored expiry; ok is false for ” (permanent) or junk.
func adminParseExpiry(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(adminExpiryLayout, strings.TrimSpace(s), time.UTC)
	return t, err == nil
}

// adminExpiryAfter is the stored expiry for a sanction of hours (0 = permanent = ”).
func adminExpiryAfter(hours int) string {
	if hours <= 0 {
		return ""
	}
	return time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format(adminExpiryLayout)
}

// adminReasonHours reads {reason, hours} from a ban or mute request. hours 0 or
// absent = permanent; negative or over a year is refused.
func adminReasonHours(w http.ResponseWriter, r *http.Request, reasonRequired bool) (string, int, bool) {
	var req struct {
		Reason string `json:"reason"`
		Hours  int    `json:"hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return "", 0, false
	}
	req.Reason = strings.TrimSpace(req.Reason)
	switch {
	case len(req.Reason) > 200:
		http.Error(w, `{"error":"reason is limited to 200 characters"}`, http.StatusBadRequest)
	case reasonRequired && req.Reason == "":
		http.Error(w, `{"error":"a reason is required"}`, http.StatusBadRequest)
	case req.Hours < 0 || req.Hours > adminMaxSanctionHrs:
		http.Error(w, `{"error":"hours must be 0 (permanent) to 8760 (one year)"}`, http.StatusBadRequest)
	default:
		return req.Reason, req.Hours, true
	}
	return "", 0, false
}

var adminSweeperOnce sync.Once

// adminStartSweeper starts the once-a-minute expiry sweep (first call only).
func adminStartSweeper() {
	adminSweeperOnce.Do(func() {
		go func() {
			for range time.Tick(adminSweepEvery) {
				adminSweepExpired(time.Now())
			}
		}()
	})
}

// adminSweepExpired lifts every ban and mute whose end is before now. Rows are
// read into slices and closed BEFORE any follow-up call (single DB connection).
func adminSweepExpired(now time.Time) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return
	}
	stamp := now.UTC().Format(adminExpiryLayout)
	type item struct{ pid, reason, expires string }
	read := func(table string) []item {
		var list []item
		rows, err := database.Query(`SELECT user_id, reason, expires_at FROM `+table+` WHERE expires_at<>'' AND expires_at<=?`, stamp)
		if err != nil {
			return nil
		}
		for rows.Next() {
			var it item
			if rows.Scan(&it.pid, &it.reason, &it.expires) == nil {
				list = append(list, it)
			}
		}
		_ = rows.Close()
		return list
	}
	for _, it := range read("admin_bans") {
		// The auth server first: its ban keeps the launcher shut. If it does
		// not answer, the row stays and the next sweep tries again -- deleting
		// it first would leave the launcher ban in place with nothing left to
		// lift it. (mmogbrain already ignores an expired ban: adminPlayerBanned.)
		auth := "not configured"
		if adminDash.authURL != "" {
			if err := adminAuthCall("/admin/unban", it.pid, ""); err != nil {
				logrus.WithError(err).WithField("pid", it.pid).Warn("admin: ban expiry: auth server unban failed; retrying next sweep")
				continue
			}
			auth = "ok"
		}
		if err := adminUnbanLocal(database, it.pid); err != nil {
			logrus.WithError(err).WithField("pid", it.pid).Warn("admin: ban expiry could not unban")
			continue
		}
		adminAuditSystemTarget("ban expired", it.pid, map[string]any{"reason": it.reason, "expired_at": it.expires, "auth": auth})
	}
	for _, it := range read("admin_chat_mutes") {
		if err := adminUnmutePlayer(database, it.pid); err != nil {
			continue
		}
		adminAuditSystemTarget("mute expired", it.pid, map[string]any{"reason": it.reason, "expired_at": it.expires})
	}
}

// adminAuditSystemTarget is adminAuditSystem with the player in the target
// column, so the entry shows under the player's own audit list.
func adminAuditSystemTarget(action, target string, details map[string]any) {
	adminAuditSystem(action, details)
	if database := currentMmogPlayerStateDB(); database != nil {
		// adminAuditSystem leaves target empty; fill it on the row it just wrote.
		_, _ = database.Exec(`UPDATE admin_audit SET target=? WHERE id=(SELECT MAX(id) FROM admin_audit) AND target='' AND actor='server' AND action=?`, target, action)
	}
}
