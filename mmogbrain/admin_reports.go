package main

// Client bug reports and telemetry for the dashboard.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// adminFilterPID normalizes a pid used as a filter. Unlike
// normalizedPlayerStatePID it never falls back to the default player: junk
// matches nobody.
func adminFilterPID(v string) string {
	if n := protocol.NormalizePlayerPID(v); n != "" {
		return n
	}
	return "invalid"
}

// adminPageLimit reads ?limit=, default def, at most 500 (the brief's bound
// for every admin list).
func adminPageLimit(r *http.Request, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		if n > 500 {
			return 500
		}
		return n
	}
	return def
}

// adminBeforeID reads ?before=<id> (0 = from the newest).
func adminBeforeID(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("before"))
	if n < 0 {
		return 0
	}
	return n
}

// adminAPIReports lists the clients' bug reports (client_reports, newest
// first). Filters: q (text in name or details), type, pid (exact player);
// paging: before=<id> (rows older than that id) and limit (default 150, max
// 500). "groups" counts the same filtered set per type+name (not paged), so
// the most frequent reports stay visible while paging the list. "more" says
// an older page exists.
func adminAPIReports(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	type row struct {
		ID      int    `json:"id"`
		When    string `json:"when"`
		Player  string `json:"player"`
		PID     string `json:"pid"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Details string `json:"details"`
	}
	type group struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Count  int    `json:"count"`
		Newest string `json:"newest"`
	}
	out := []row{}
	groups := []group{}
	types := []string{}
	more := false
	total := 0
	if database != nil {
		qv := r.URL.Query()
		where := []string{"1=1"}
		var args []any
		if v := strings.TrimSpace(qv.Get("q")); v != "" {
			if len(v) > 200 {
				v = v[:200]
			}
			like := "%" + adminLikeEscape(v) + "%"
			where = append(where, `(c.name LIKE ? ESCAPE '\' OR c.desc LIKE ? ESCAPE '\')`)
			args = append(args, like, like)
		}
		if v := strings.TrimSpace(qv.Get("type")); v != "" {
			where = append(where, `c.type=?`)
			args = append(args, v)
		}
		if v := strings.TrimSpace(qv.Get("pid")); v != "" {
			where = append(where, `c.user_id=?`)
			args = append(args, adminFilterPID(v))
		}
		filter := strings.Join(where, " AND ")
		limit := adminPageLimit(r, 150)
		pageArgs := append([]any{}, args...)
		pageWhere := filter
		if b := adminBeforeID(r); b > 0 {
			pageWhere += ` AND c.id<?`
			pageArgs = append(pageArgs, b)
		}
		// Names come from the same query (LEFT JOIN): the store has one
		// connection, so no lookups while rows are open.
		if rows, err := database.Query(`SELECT c.id, c.created_at, c.user_id, COALESCE(ps.display_name,''), c.type, c.name, c.desc
			FROM client_reports c LEFT JOIN player_state ps ON ps.user_id=c.user_id
			WHERE `+pageWhere+` ORDER BY c.id DESC LIMIT ?`, append(pageArgs, limit+1)...); err == nil {
			for rows.Next() {
				var x row
				if rows.Scan(&x.ID, &x.When, &x.PID, &x.Player, &x.Type, &x.Name, &x.Details) == nil {
					if x.Player == "Local" {
						x.Player = ""
					}
					out = append(out, x)
				}
			}
			_ = rows.Close()
		}
		if len(out) > limit {
			out, more = out[:limit], true
		}
		_ = database.QueryRow(`SELECT COUNT(*) FROM client_reports c WHERE `+filter, args...).Scan(&total)
		if rows, err := database.Query(`SELECT c.type, c.name, COUNT(*) n, MAX(c.created_at) FROM client_reports c WHERE `+filter+`
			GROUP BY c.type, c.name ORDER BY n DESC, MAX(c.id) DESC LIMIT 100`, args...); err == nil {
			for rows.Next() {
				var g group
				if rows.Scan(&g.Type, &g.Name, &g.Count, &g.Newest) == nil {
					groups = append(groups, g)
				}
			}
			_ = rows.Close()
		}
		if rows, err := database.Query(`SELECT DISTINCT type FROM client_reports ORDER BY type`); err == nil {
			for rows.Next() {
				var t string
				if rows.Scan(&t) == nil {
					types = append(types, t)
				}
			}
			_ = rows.Close()
		}
	}
	writeAdminJSON(w, map[string]any{"reports": out, "groups": groups, "types": types, "total": total, "more": more})
}

// adminAPITelemetry lists the client's telemetry (telemetry.go), newest
// first. Filters: rt (request type), pid (exact player) or q (name or id
// prefix), paging before=<id> and limit (default 300, max 500). "counts" is
// the number of events per rt under the player filter (not the rt filter, so
// the type picker shows what each choice would give). A reward event shows,
// beside the client's breakdown, what the server paid for the same battle
// (battle_results).
func adminAPITelemetry(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	type row struct {
		ID         int    `json:"id"`
		When       string `json:"when"`
		Player     string `json:"player"`
		PID        string `json:"pid"`
		RT         string `json:"rt"`
		Summary    string `json:"summary"`
		Fields     string `json:"fields"`
		BattleID   string `json:"battle_id,omitempty"`
		ServerPaid string `json:"server_paid,omitempty"`
	}
	out := []row{}
	types := []string{}
	counts := map[string]int{}
	if database == nil {
		writeAdminJSON(w, map[string]any{"events": out, "types": types, "counts": counts, "more": false})
		return
	}
	qv := r.URL.Query()
	rt := strings.TrimSpace(qv.Get("rt"))
	pidFilter := ""
	if v := strings.TrimSpace(qv.Get("pid")); v != "" {
		pidFilter = adminFilterPID(v)
	} else if q := strings.TrimSpace(qv.Get("q")); q != "" {
		pidFilter = strings.ToLower(strings.ReplaceAll(q, "-", ""))
		var byName string
		if database.QueryRow(`SELECT user_id FROM player_state WHERE display_name=? LIMIT 1`, q).Scan(&byName) == nil {
			pidFilter = byName
		}
	}
	// Exact for pid=, prefix for q= (the old tab's behaviour).
	pidSQL, pidArg := `(?='' OR t.user_id LIKE ?||'%' ESCAPE '\')`, adminLikeEscape(pidFilter)
	if strings.TrimSpace(qv.Get("pid")) != "" {
		pidSQL, pidArg = `(?='' OR t.user_id=?)`, pidFilter
	}
	limit := adminPageLimit(r, 300)
	where := pidSQL + ` AND (?='' OR t.rt=?)`
	args := []any{pidFilter, pidArg, rt, rt}
	if b := adminBeforeID(r); b > 0 {
		where += ` AND t.id<?`
		args = append(args, b)
	}
	rows, err := database.Query(`SELECT t.id, t.created_at, t.user_id, COALESCE(ps.display_name,''), t.rt, t.summary, t.fields, t.battle_id,
		(SELECT br.credits||' credits, '||br.xp||' XP' FROM battle_results br WHERE t.battle_id<>'' AND br.match_id=t.battle_id AND br.user_id=t.user_id LIMIT 1)
		FROM client_telemetry t LEFT JOIN player_state ps ON ps.user_id=t.user_id
		WHERE `+where+` ORDER BY t.id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		var x row
		var paid *string
		if rows.Scan(&x.ID, &x.When, &x.PID, &x.Player, &x.RT, &x.Summary, &x.Fields, &x.BattleID, &paid) == nil {
			if x.Player == "Local" {
				x.Player = ""
			}
			if x.BattleID != "" {
				x.ServerPaid = "no result recorded"
				if paid != nil {
					x.ServerPaid = *paid
				}
			}
			out = append(out, x)
		}
	}
	_ = rows.Close()
	more := false
	if len(out) > limit {
		out, more = out[:limit], true
	}
	if trows, err := database.Query(`SELECT t.rt, COUNT(*) FROM client_telemetry t WHERE `+pidSQL+` GROUP BY t.rt ORDER BY t.rt`, pidFilter, pidArg); err == nil {
		for trows.Next() {
			var t string
			var n int
			if trows.Scan(&t, &n) == nil {
				types = append(types, t)
				counts[t] = n
			}
		}
		_ = trows.Close()
	}
	writeAdminJSON(w, map[string]any{"events": out, "types": types, "counts": counts, "more": more})
}
