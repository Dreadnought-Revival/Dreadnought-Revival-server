package main

// Admin dashboard: GET /admin/dashboard serves the shell in admin_ui/
// (admin_ui.go); every piece of data it shows comes from /admin/api/*,
// behind the same X-Admin-Key middleware as the other /admin routes
// (ADMIN_KEY, which requireAdminKey refuses to leave empty). mmogbrain's HTTP port is not forwarded to the
// internet, so the page is reachable only from the machine or the LAN.
//
// Actions (stopping a battle server, player management and moderation) are
// recorded in the admin_audit table with the caller's address.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// adminDashboardConfig is what the dashboard needs from main.
type adminDashboardConfig struct {
	controlPlaneURL string // GAME_MGR_URL (dn-dedicated)
	internalKey     string // X-Internal-Key for the control plane
	adminKey        string // forwarded to the auth server's /admin routes
	authURL         string // auth server; "" = bans stay local (tests)
	startedAt       time.Time
}

var adminDash adminDashboardConfig

// constantTimeKeyMiddleware is the admin gate for the dashboard API: the
// same key as adminKeyMiddleware, compared in constant time.
func constantTimeKeyMiddleware(key string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-Admin-Key")
			if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func registerAdminDashboard(r *mux.Router, adminKey, controlPlaneURL, internalKey string) {
	adminDash = adminDashboardConfig{controlPlaneURL: controlPlaneURL, internalKey: internalKey, adminKey: adminKey, startedAt: time.Now()}
	registerAdminUI(r)

	api := r.PathPrefix("/admin/api").Subrouter()
	api.Use(constantTimeKeyMiddleware(adminKey))
	api.HandleFunc("/overview", adminAPIOverview).Methods(http.MethodGet)
	api.HandleFunc("/instances", adminAPIInstances).Methods(http.MethodGet)
	api.HandleFunc("/instances/{id}", adminAPIStopInstance).Methods(http.MethodDelete)
	api.HandleFunc("/online", adminAPIOnline).Methods(http.MethodGet)
	api.HandleFunc("/matches", adminAPIMatches).Methods(http.MethodGet)
	api.HandleFunc("/players", adminAPIPlayers).Methods(http.MethodGet)
	api.HandleFunc("/reports", adminAPIReports).Methods(http.MethodGet)
	api.HandleFunc("/telemetry", adminAPITelemetry).Methods(http.MethodGet)
	api.HandleFunc("/logs", adminAPILogs).Methods(http.MethodGet)
	api.HandleFunc("/battle-logs", adminAPIBattleLogs).Methods(http.MethodGet)
	api.HandleFunc("/metrics", adminAPIMetrics).Methods(http.MethodGet)
	registerAdminPlayerManagement(api)
	registerAdminModeration(api)
	registerAdminEconomySettings(api)
	registerAdminFairPlay(api)
	registerAdminModeSettings(api)
	registerAdminServerMessages(api)
	registerAdminRestart(api)
	registerAdminMarketPricing(api)
	registerAdminTechTree(api)
	registerAdminTuneEditor(api)
	for _, register := range adminAPIExtra {
		register(api)
	}
}

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// adminLikeEscape makes request text safe inside a LIKE pattern written with
// ESCAPE '\' (the dashboard's searches: names, ids, report text).
func adminLikeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func adminAPIOverview(w http.ResponseWriter, _ *http.Request) {
	database := currentMmogPlayerStateDB()
	count := func(q string, args ...any) int {
		if database == nil {
			return 0
		}
		var n int
		_ = database.QueryRow(q, args...).Scan(&n)
		return n
	}
	since := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	sinceRFC := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	instances, cpErr := controlPlaneInstances()
	crashed := 0
	for _, i := range instances {
		if i.Crashed != "" {
			crashed++
		}
	}
	modes := map[string]int{}
	if database != nil {
		if rows, err := database.Query(`SELECT game_mode, COUNT(*) FROM matches WHERE created_at >= ? GROUP BY game_mode`, sinceRFC); err == nil {
			for rows.Next() {
				var m string
				var n int
				if rows.Scan(&m, &n) == nil {
					modes[m] = n
				}
			}
			_ = rows.Close()
		}
	}
	services := map[string]bool{}
	for name, port := range map[string]int{"auth-server": 8081, "legacy-api": 8082, "mmogbrain": 8083, "master-server": 8084, "dn-dedicated": 8085} {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		services[name] = err == nil && resp.StatusCode == http.StatusOK
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	ov := map[string]any{
		"uptime_seconds":      int(time.Since(adminDash.startedAt).Seconds()),
		"services":            services,
		"online":              len(socialHubInstance.onlinePlayers()),
		"accounts":            count(`SELECT COUNT(*) FROM player_state`),
		"new_accounts_24h":    count(`SELECT COUNT(*) FROM player_state WHERE created_at >= ?`, since),
		"queued":              count(`SELECT COUNT(*) FROM queue_entries WHERE status='waiting'`),
		"instances":           len(instances),
		"instances_crashed":   crashed,
		"matches_24h":         count(`SELECT COUNT(*) FROM matches WHERE created_at >= ?`, sinceRFC),
		"matches_total":       count(`SELECT COUNT(*) FROM matches`),
		"results_24h":         count(`SELECT COUNT(*) FROM battle_results WHERE created_at >= ?`, since),
		"kills_24h":           count(`SELECT COALESCE(SUM(kills),0) FROM battle_results WHERE created_at >= ?`, since),
		"credits_paid_24h":    count(`SELECT COALESCE(SUM(credits),0) FROM battle_results WHERE created_at >= ?`, since),
		"reports_24h":         count(`SELECT COUNT(*) FROM client_reports WHERE created_at >= ?`, since),
		"modes_24h":           modes,
		"host_crashes_recent": recentHostCrashes(40),
	}
	if cpErr != nil {
		ov["control_plane_error"] = cpErr.Error()
	}
	writeAdminJSON(w, ov)
}

// recentHostCrashes counts crash kinds over the newest n battle logs.
func recentHostCrashes(n int) map[string]int {
	out := map[string]int{"stack overflow": 0, "access violation": 0, "clean": 0}
	for _, f := range newestBattleLogs(n) {
		tail := tailFile(f.path, 96*1024)
		switch {
		case strings.Contains(tail, "EXCEPTION_STACK_OVERFLOW"):
			out["stack overflow"]++
		case strings.Contains(tail, "Unhandled Exception"), strings.Contains(tail, "Unhandled page fault"):
			out["access violation"]++
		default:
			out["clean"]++
		}
	}
	return out
}
