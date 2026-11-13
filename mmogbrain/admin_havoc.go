package main

// Havoc overview for the dashboard (page "havoc"): runs (havoc_runs, written by
// battleHavocHandler at Game Over), the weekly best per player and the
// milestone rewards paid (havoc_reward_claims, payHavocMilestones). Weeks use
// havocWeekStart (GUESS marked in havoc.go: Monday 00:00 UTC).

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminHavoc) }

func registerAdminHavoc(api *mux.Router) {
	api.HandleFunc("/havoc", adminAPIHavoc).Methods(http.MethodGet)
}

// GET /admin/api/havoc?weeks=N (default 4, 1-26).
func adminAPIHavoc(w http.ResponseWriter, r *http.Request) {
	weeks := 4
	if v := r.URL.Query().Get("weeks"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 26 {
			writeAdminError(w, http.StatusBadRequest, "weeks must be 1 to 26")
			return
		}
		weeks = n
	}
	now := time.Now()
	thisWeek := havocWeekStart(now)
	since := thisWeek.AddDate(0, 0, -7*(weeks-1)).Format(havocTimeLayout)

	type run struct {
		Match    string `json:"match"`
		PID      string `json:"pid"`
		Name     string `json:"name"`
		Score    int64  `json:"score"`
		Waves    int    `json:"waves"`
		Created  string `json:"created"`
		Seconds  int64  `json:"seconds"` // match start to end, 0 when unknown
		MatchRef string `json:"match_ref"`
	}
	type top struct {
		Week  string `json:"week"`
		Rank  int    `json:"rank"`
		PID   string `json:"pid"`
		Name  string `json:"name"`
		Score int64  `json:"score"`
		Waves int    `json:"waves"`
		Runs  int    `json:"runs"`
	}
	type claim struct {
		PID     string `json:"pid"`
		Name    string `json:"name"`
		Week    string `json:"week"`
		Reward  string `json:"reward"`
		Title   string `json:"title"`
		Type    string `json:"type"`
		Value   int32  `json:"value"`
		Score   int32  `json:"threshold"`
		Match   string `json:"match"`
		Created string `json:"created"`
	}
	out := map[string]any{"week_start": thisWeek.Format(havocTimeLayout), "reset": thisWeek.AddDate(0, 0, 7).Format(havocTimeLayout), "weeks": weeks}
	runs, tops, claims := []run{}, []top{}, []claim{}
	stages := map[string]int{}
	database := currentMmogPlayerStateDB()
	if database == nil {
		out["runs"], out["weekly_top"], out["claims"], out["stages"] = runs, tops, claims, stages
		out["totals"] = map[string]any{}
		writeAdminJSON(w, out)
		return
	}
	const nameCol = `COALESCE(NULLIF(ps.display_name,'Local'),'')`

	// Recent runs, with the match's duration when its row is still there.
	rows, err := database.Query(`SELECT h.match_id, h.user_id, `+nameCol+`, h.score, h.waves, h.created_at,
		COALESCE(CAST(strftime('%s', m.ended_at) AS INTEGER) - CAST(strftime('%s', COALESCE(m.started_at, m.created_at)) AS INTEGER), 0)
		FROM havoc_runs h LEFT JOIN player_state ps ON ps.user_id = h.user_id
		LEFT JOIN matches m ON m.battle_match_id = h.match_id AND m.battle_match_id <> ''
		WHERE h.created_at >= ? ORDER BY h.created_at DESC, h.rowid DESC LIMIT 100`, since)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var x run
		if rows.Scan(&x.Match, &x.PID, &x.Name, &x.Score, &x.Waves, &x.Created, &x.Seconds) == nil {
			if x.Seconds < 0 || x.Seconds > 6*3600 {
				x.Seconds = 0
			}
			x.MatchRef = x.Match
			runs = append(runs, x)
		}
	}
	_ = rows.Close()

	// Weekly best per player, top 10 of each week in the window.
	rows, err = database.Query(`SELECT h.user_id, `+nameCol+`, h.score, h.waves, h.created_at,
		(SELECT COUNT(*) FROM havoc_runs h2 WHERE h2.user_id = h.user_id AND h2.created_at >= ? AND h2.created_at < ?)
		FROM havoc_runs h LEFT JOIN player_state ps ON ps.user_id = h.user_id
		WHERE h.created_at >= ? ORDER BY h.score DESC, h.created_at ASC LIMIT 5000`, since, thisWeek.AddDate(0, 0, 7).Format(havocTimeLayout), since)
	if err == nil {
		seen := map[string]bool{}
		perWeek := map[string]int{}
		for rows.Next() {
			var pid, name, created string
			var score int64
			var waves, n int
			if rows.Scan(&pid, &name, &score, &waves, &created, &n) != nil {
				continue
			}
			t, perr := time.Parse(havocTimeLayout, created)
			if perr != nil {
				continue
			}
			week := havocWeekStart(t).Format(havocTimeLayout)
			if seen[week+pid] || perWeek[week] >= 10 {
				continue
			}
			seen[week+pid] = true
			perWeek[week]++
			// n counts the player's runs over the whole window, not just this week.
			tops = append(tops, top{Week: week, Rank: perWeek[week], PID: pid, Name: name, Score: score, Waves: waves, Runs: n})
		}
		_ = rows.Close()
	}

	sort.SliceStable(tops, func(i, j int) bool {
		if tops[i].Week != tops[j].Week {
			return tops[i].Week > tops[j].Week
		}
		return tops[i].Rank < tops[j].Rank
	})

	// Runs per wave reached, over the window.
	if sr, err := database.Query(`SELECT waves, COUNT(*) FROM havoc_runs WHERE created_at >= ? GROUP BY waves`, since); err == nil {
		for sr.Next() {
			var wv, n int
			if sr.Scan(&wv, &n) == nil {
				stages[strconv.Itoa(wv)] = n
			}
		}
		_ = sr.Close()
	}

	// Milestone rewards paid; title/type/value come from DN_HavocRewards_DT.
	milestones := map[string]claim{}
	for _, m := range havocMilestones() {
		milestones[m.RowName] = claim{Title: m.Title, Type: havocRewardKind(m.RewardType), Value: m.Value, Score: m.Highscore}
	}
	if cr, err := database.Query(`SELECT c.user_id, `+nameCol+`, c.week_start, c.reward, c.match_id, c.created_at
		FROM havoc_reward_claims c LEFT JOIN player_state ps ON ps.user_id = c.user_id
		WHERE c.week_start >= ? ORDER BY c.created_at DESC LIMIT 100`, since); err == nil {
		for cr.Next() {
			var c claim
			if cr.Scan(&c.PID, &c.Name, &c.Week, &c.Reward, &c.Match, &c.Created) == nil {
				m := milestones[c.Reward]
				c.Title, c.Type, c.Value, c.Score = m.Title, m.Type, m.Value, m.Score
				claims = append(claims, c)
			}
		}
		_ = cr.Close()
	}

	totals := map[string]any{}
	var runsN, players, best, waveSum, claimsN, runsAll int64
	_ = database.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT user_id), COALESCE(MAX(score),0), COALESCE(SUM(waves),0)
		FROM havoc_runs WHERE created_at >= ?`, since).Scan(&runsN, &players, &best, &waveSum)
	_ = database.QueryRow(`SELECT COUNT(*) FROM havoc_reward_claims WHERE week_start >= ?`, since).Scan(&claimsN)
	_ = database.QueryRow(`SELECT COUNT(*) FROM havoc_runs`).Scan(&runsAll)
	var weekRuns, weekPlayers int64
	_ = database.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT user_id) FROM havoc_runs WHERE created_at >= ?`, thisWeek.Format(havocTimeLayout)).Scan(&weekRuns, &weekPlayers)
	totals["runs"], totals["players"], totals["best"], totals["claims"], totals["runs_all_time"] = runsN, players, best, claimsN, runsAll
	totals["week_runs"], totals["week_players"] = weekRuns, weekPlayers
	if runsN > 0 {
		totals["avg_waves"] = float64(waveSum) / float64(runsN)
	}
	out["runs"], out["weekly_top"], out["claims"], out["stages"], out["totals"] = runs, tops, claims, stages, totals
	writeAdminJSON(w, out)
}

// havocRewardKind: "EYHavocRewardType::YHRT_FREEXP" -> "FREEXP".
func havocRewardKind(t string) string {
	if i := strings.LastIndex(t, "YHRT_"); i >= 0 {
		return t[i+len("YHRT_"):]
	}
	return t
}
