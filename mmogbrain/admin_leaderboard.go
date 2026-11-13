package main

// The Leaderboards screen as the admin sees it (page "leaderboard"): the same
// ranking the client gets from YA_GetLeaderboardData (leaderboardRows /
// leaderboardWindow in leaderboard.go, called read-only), so what the page
// shows is what players see. The GUESSes noted there apply: SID is Name#1234,
// the score is the sum of the player's reported match scores, the weekly
// board resets Monday 00:00 UTC.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminLeaderboard) }

func registerAdminLeaderboard(api *mux.Router) {
	api.HandleFunc("/leaderboard", adminAPILeaderboard).Methods(http.MethodGet)
}

var adminLeaderboardSpans = []string{"weekly", "lastweek", "lifetime"}

// GET /admin/api/leaderboard?board=<mode>|<fleet 1-3>|<weekly|lastweek|lifetime>
// Without board: the boards that have scores. With it: that board's ranking
// (top leaderboardSize, like the client gets).
func adminAPILeaderboard(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	type boardInfo struct {
		Mode    string `json:"mode"`
		Fleet   int    `json:"fleet"`
		Players int    `json:"players"`
	}
	boards := []boardInfo{}
	if database == nil {
		writeAdminJSON(w, map[string]any{"boards": boards, "spans": adminLeaderboardSpans})
		return
	}
	// Mode and fleet pairs with at least one score row (same joins as
	// leaderboardRows, so the list only offers boards that can be non-empty).
	rows, err := database.Query(`SELECT m.game_mode, r.fleet_type, COUNT(DISTINCT r.user_id)
		FROM battle_result_stats s
		JOIN battle_results r ON r.match_id = s.match_id AND r.user_id = s.user_id
		JOIN matches m ON m.battle_match_id = r.match_id
		WHERE s.stat = 'score' AND s.value > 0 AND r.fleet_type BETWEEN 1 AND 3
		GROUP BY m.game_mode, r.fleet_type ORDER BY m.game_mode, r.fleet_type`)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var b boardInfo
		if rows.Scan(&b.Mode, &b.Fleet, &b.Players) == nil {
			boards = append(boards, b)
		}
	}
	_ = rows.Close()
	sort.SliceStable(boards, func(i, j int) bool { return boards[i].Mode < boards[j].Mode })

	out := map[string]any{"boards": boards, "spans": adminLeaderboardSpans}
	board := r.URL.Query().Get("board")
	if board == "" {
		writeAdminJSON(w, out)
		return
	}
	parts := strings.Split(board, "|")
	fleet, ferr := 0, error(nil)
	if len(parts) == 3 {
		fleet, ferr = strconv.Atoi(parts[1])
	}
	span := ""
	if len(parts) == 3 {
		span = parts[2]
	}
	okSpan := false
	for _, s := range adminLeaderboardSpans {
		okSpan = okSpan || s == span
	}
	if len(parts) != 3 || parts[0] == "" || ferr != nil || fleet < 1 || fleet > 3 || !okSpan {
		writeAdminError(w, http.StatusBadRequest, "board must be <mode>|<fleet 1-3>|<weekly|lastweek|lifetime>")
		return
	}
	timespan := "Lifetime"
	if span != "lifetime" {
		timespan = "Weekly"
	}
	from, to := leaderboardWindow(timespan, span == "lastweek", time.Now())
	ranked := leaderboardRows(parts[0], int32(fleet), from, to)
	if len(ranked) > leaderboardSize {
		ranked = ranked[:leaderboardSize]
	}
	type entry struct {
		Rank  int    `json:"rank"`
		PID   string `json:"pid"`
		Name  string `json:"name"`
		SID   string `json:"sid"`
		Score int64  `json:"score"`
	}
	entries := make([]entry, 0, len(ranked))
	names := map[string]string{}
	if len(ranked) > 0 {
		marks := make([]string, len(ranked))
		args := make([]any, len(ranked))
		for i, e := range ranked {
			marks[i], args[i] = "?", e.pid
		}
		// leaderboardRows has closed its rows; one JOIN-less lookup for all names.
		if nr, err := database.Query(`SELECT user_id, display_name FROM player_state WHERE user_id IN (`+strings.Join(marks, ",")+`)`, args...); err == nil {
			for nr.Next() {
				var id, n string
				if nr.Scan(&id, &n) == nil && n != "Local" {
					names[id] = n
				}
			}
			_ = nr.Close()
		}
	}
	for i, e := range ranked {
		entries = append(entries, entry{Rank: i + 1, PID: e.pid, Name: names[e.pid], SID: gatewayUsernameTag(names[e.pid], e.pid), Score: e.score})
	}
	out["entries"] = entries
	out["from"], out["to"] = from, to
	writeAdminJSON(w, out)
}
