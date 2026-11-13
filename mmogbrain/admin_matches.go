package main

// Match history for the dashboard: the filtered, paged list (this file) and
// one match in detail (admin_match_detail.go).

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

// adminShownName is a player_state.display_name for display: 'Local' (the
// default seed) and ” mean the player never named themselves.
func adminShownName(n string) string {
	if n == "Local" {
		return ""
	}
	return n
}

// adminBaseBattleID strips the "-r<n>" the mod appends for a second round on
// the same host (see matchFleetType), giving matches.battle_match_id.
func adminBaseBattleID(id string) string {
	if i := strings.LastIndex(id, "-r"); i > 0 {
		if _, err := strconv.Atoi(id[i+2:]); err == nil {
			return id[:i]
		}
	}
	return id
}

type adminMatchResult struct {
	PID     string `json:"pid"`
	Name    string `json:"name"`
	Team    int    `json:"team"`
	Outcome string `json:"outcome"`
	Kills   int    `json:"kills"`
	Deaths  int    `json:"deaths"`
	Credits int    `json:"credits"`
	XP      int    `json:"xp"`
}

type adminMatchRow struct {
	ID        string             `json:"id"`
	BattleID  string             `json:"battle_id"`
	Mode      string             `json:"mode"`
	Map       string             `json:"map"`
	Status    string             `json:"status"`
	Created   string             `json:"created"`
	Started   string             `json:"started"`
	Ended     string             `json:"ended"`
	FleetType int                `json:"fleet_type"`
	Port      int                `json:"port"`
	Players   int                `json:"players"`
	Kills     int                `json:"kills"`
	Winner    int                `json:"winner_team"` // team of the winners; 0 = none/draw/unknown
	Outcome   string             `json:"outcome"`     // "win", "draw", "unknown" summarised over the results; "" = no results
	Results   []adminMatchResult `json:"results"`
}

// adminAPIMatches lists matches, newest first. Filters: mode= (exact
// game_mode), status= (forming/active/ended), q= (a match or battle id prefix,
// a player's pid prefix, or part of a player's name). Paging: limit= (default
// 50, max 200) and before=<created_at of the last row seen>; the reply's
// "more" says whether a next page exists and "next_before" is its cursor.
// Every key the first dashboard read (id, mode, map, status, started, ended,
// fleet_type, results[]) is kept.
func adminAPIMatches(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	out := []*adminMatchRow{}
	if database == nil {
		writeAdminJSON(w, map[string]any{"matches": out, "more": false})
		return
	}
	q := r.URL.Query()
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > 200 {
		limit = 200
	}
	var where []string
	var args []any
	if v := strings.TrimSpace(q.Get("mode")); v != "" {
		where = append(where, `m.game_mode=?`)
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("status")); v != "" {
		if v != "forming" && v != "active" && v != "ended" {
			writeAdminError(w, http.StatusBadRequest, "status must be forming, active or ended")
			return
		}
		where = append(where, `m.status=?`)
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("before")); v != "" {
		where = append(where, `m.created_at < ?`)
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		if len(v) > 64 {
			v = v[:64]
		}
		esc := adminLikeEscape(v)
		// A player is in a match through its results (battle_results.match_id is
		// matches.battle_match_id, plus "-r<n>" for later rounds) or, while it
		// is not over, its slots (rows are deleted when the match ends).
		where = append(where, `(m.id LIKE ?||'%' ESCAPE '\' OR (m.battle_match_id<>'' AND m.battle_match_id LIKE ?||'%' ESCAPE '\')
			OR EXISTS (SELECT 1 FROM battle_results b LEFT JOIN player_state p ON p.user_id=b.user_id
				WHERE m.battle_match_id<>'' AND (b.match_id=m.battle_match_id OR b.match_id LIKE m.battle_match_id||'-r%')
				AND (b.user_id LIKE ?||'%' ESCAPE '\' OR p.display_name LIKE '%'||?||'%' ESCAPE '\'))
			OR EXISTS (SELECT 1 FROM match_slots s LEFT JOIN player_state p ON p.user_id=s.user_id
				WHERE s.match_id=m.id AND (s.user_id LIKE ?||'%' ESCAPE '\' OR p.display_name LIKE '%'||?||'%' ESCAPE '\')))`)
		args = append(args, esc, esc, esc, esc, esc, esc)
	}
	sqlWhere := ""
	if len(where) > 0 {
		sqlWhere = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit+1)
	rows, err := database.Query(`SELECT m.id, COALESCE(m.battle_match_id,''), m.game_mode, m.map, m.status, m.created_at,
		COALESCE(m.started_at,''), COALESCE(m.ended_at,''), COALESCE(m.fleet_type,0), COALESCE(m.server_port,0)
		FROM matches m `+sqlWhere+` ORDER BY m.created_at DESC, m.id LIMIT ?`, args...)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for rows.Next() {
		m := &adminMatchRow{Results: []adminMatchResult{}}
		if rows.Scan(&m.ID, &m.BattleID, &m.Mode, &m.Map, &m.Status, &m.Created, &m.Started, &m.Ended, &m.FleetType, &m.Port) == nil {
			m.Map = adminMapName(m.Map)
			out = append(out, m)
		}
	}
	_ = rows.Close()
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	next := ""
	if len(out) > 0 {
		next = out[len(out)-1].Created
	}
	adminFillMatchResults(database, out)
	writeAdminJSON(w, map[string]any{"matches": out, "more": more, "next_before": next})
}

// adminFillMatchResults adds results, player counts and the outcome summary
// to a page of matches with TWO queries (results with names joined, slots
// counted), both after the match rows are closed: the store has ONE
// connection (MaxOpenConns=1), and a query inside an open result set waits for
// itself forever -- which deadlocked the whole server's database the first
// time the dashboard was opened (2026-09-29).
func adminFillMatchResults(database *sql.DB, ms []*adminMatchRow) {
	byBattle := map[string]*adminMatchRow{}
	byID := map[string]*adminMatchRow{}
	var ids []any
	var conds []string
	for _, m := range ms {
		byID[m.ID] = m
		if m.BattleID != "" {
			byBattle[m.BattleID] = m
			ids = append(ids, m.BattleID, m.BattleID)
			conds = append(conds, `(b.match_id=? OR b.match_id LIKE ?||'-r%')`)
		}
	}
	if len(conds) > 0 {
		rs, err := database.Query(`SELECT b.match_id, b.user_id, COALESCE(p.display_name,''), b.team, b.outcome, b.kills, b.deaths, b.credits, b.xp
			FROM battle_results b LEFT JOIN player_state p ON p.user_id=b.user_id
			WHERE `+strings.Join(conds, " OR ")+` ORDER BY b.team, b.kills DESC, b.user_id`, ids...)
		if err == nil {
			for rs.Next() {
				var mid string
				var x adminMatchResult
				if rs.Scan(&mid, &x.PID, &x.Name, &x.Team, &x.Outcome, &x.Kills, &x.Deaths, &x.Credits, &x.XP) != nil {
					continue
				}
				if m := byBattle[adminBaseBattleID(mid)]; m != nil {
					x.Name = adminShownName(x.Name)
					m.Results = append(m.Results, x)
				}
			}
			_ = rs.Close()
		}
	}
	for _, m := range ms {
		m.Players = len(m.Results)
		for _, x := range m.Results {
			m.Kills += x.Kills
			if x.Outcome == "win" && m.Winner == 0 {
				m.Winner = x.Team
			}
		}
		switch {
		case len(m.Results) == 0:
		case m.Winner > 0:
			m.Outcome = "win"
		default:
			m.Outcome = "unknown"
			for _, x := range m.Results {
				if x.Outcome == "draw" {
					m.Outcome = "draw"
				}
			}
		}
	}
	// Slots exist only until the match ends: they give the head count of a
	// forming or running match that has no results yet.
	var pending []any
	for _, m := range ms {
		if m.Players == 0 && m.Status != "ended" {
			pending = append(pending, m.ID)
		}
	}
	if len(pending) == 0 {
		return
	}
	rs, err := database.Query(`SELECT match_id, COUNT(*) FROM match_slots WHERE match_id IN (?`+strings.Repeat(",?", len(pending)-1)+`) GROUP BY match_id`, pending...)
	if err != nil {
		return
	}
	for rs.Next() {
		var id string
		var n int
		if rs.Scan(&id, &n) == nil && byID[id] != nil {
			byID[id].Players = n
		}
	}
	_ = rs.Close()
}
