package main

// Player lists for the dashboard: who is online and the account search.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

// adminPlayerName is a player's display name, or "?". It only reads: until
// 2026-11-12 it went through mmogPlayerStateForPID, which SEEDS a whole
// account (state, fleets) for an unknown id and maps a malformed one to the
// default player -- so viewing an audit row or a battle server's player list
// could create accounts. It still queries: never call it with rows open.
func adminPlayerName(pid string) string {
	database := currentMmogPlayerStateDB()
	n := protocol.NormalizePlayerPID(pid)
	if database == nil || n == "" {
		return "?"
	}
	var name string
	if err := database.QueryRow(`SELECT display_name FROM player_state WHERE user_id=?`, n).Scan(&name); err != nil || name == "" || name == "Local" {
		return "?"
	}
	return name
}

func adminAPIOnline(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		PID    string `json:"pid"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Squad  string `json:"squad,omitempty"`
		Match  string `json:"match,omitempty"`
		Queued string `json:"queued,omitempty"`
		// Fleet is the fleet of the player's queue, else of their running
		// match (Recruit/Veteran/Legendary); "" when neither.
		Fleet string `json:"fleet,omitempty"`
	}
	out := []row{}
	database := currentMmogPlayerStateDB()
	for _, p := range socialHubInstance.onlinePlayers() {
		r := row{PID: p.PID, Name: p.Name, Status: p.Status, Squad: squadHubInstance.squadIDOf(p.PID)}
		var queuedFleet, matchFleet int32
		if database != nil {
			_ = database.QueryRow(`SELECT m.game_mode||' / '||m.map, m.fleet_type FROM match_slots s JOIN matches m ON m.id=s.match_id
				WHERE s.user_id=? AND m.status='active' LIMIT 1`, p.PID).Scan(&r.Match, &matchFleet)
			_ = database.QueryRow(`SELECT game_mode, fleet_type FROM queue_entries WHERE user_id=? AND status='waiting' LIMIT 1`, p.PID).Scan(&r.Queued, &queuedFleet)
		}
		switch {
		case queuedFleet > 0:
			r.Fleet = fleetTypeName(queuedFleet)
		case matchFleet > 0:
			r.Fleet = fleetTypeName(matchFleet)
		}
		out = append(out, r)
	}
	writeAdminJSON(w, map[string]any{"players": out})
}

// adminPlayersSortSQL maps the sort= parameter to an ORDER BY (fixed strings,
// never request text). Names ascend, the rest show the biggest/newest first.
var adminPlayersSortSQL = map[string]string{
	"name":      `p.display_name COLLATE NOCASE ASC, p.user_id`,
	"rank":      `p.current_rank DESC, p.current_xp DESC, p.user_id`,
	"last_seen": `p.updated_at DESC, p.user_id`,
	"created":   `p.created_at DESC, p.user_id`,
}

// adminAPIPlayers is the account search. Query: q (name part or player id
// prefix), sort (name|rank|last_seen|created, default last_seen), online=1,
// banned=1 (an unexpired ban), offset, limit (default 100, max 200). Reply:
// players (the keys the sidebar quick search reads: pid, name, rank, online,
// banned, plus balances and counters), total (rows matching, before paging).
// last_seen is player_state.updated_at: the row changes on every login,
// reward and balance change, and the schema has no dedicated last-login time.
func adminAPIPlayers(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminJSON(w, map[string]any{"players": []any{}, "total": 0})
		return
	}
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	if len(q) > 64 {
		q = q[:64]
	}
	order, ok := adminPlayersSortSQL[query.Get("sort")]
	if !ok {
		order = adminPlayersSortSQL["last_seen"]
	}
	limit, offset := 100, 0
	if n, err := strconv.Atoi(query.Get("limit")); err == nil && n > 0 {
		limit = min(n, 200)
	}
	if n, err := strconv.Atoi(query.Get("offset")); err == nil && n > 0 {
		offset = n
	}
	// Before the query: onlinePlayers looks names up, and the store has ONE
	// connection -- a lookup inside an open result set deadlocks it.
	online := map[string]bool{}
	for _, p := range socialHubInstance.onlinePlayers() {
		online[p.PID] = true
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	where := `WHERE (?='' OR p.display_name LIKE '%'||?||'%' ESCAPE '\' OR p.user_id LIKE ?||'%' ESCAPE '\')`
	args := []any{q, adminLikeEscape(q), adminLikeEscape(strings.ToLower(strings.ReplaceAll(q, "-", "")))}
	if query.Get("banned") == "1" {
		where += ` AND EXISTS (SELECT 1 FROM admin_bans a WHERE a.user_id=p.user_id AND (a.expires_at='' OR a.expires_at>?))`
		args = append(args, now)
	}
	if query.Get("online") == "1" {
		if len(online) == 0 {
			writeAdminJSON(w, map[string]any{"players": []any{}, "total": 0})
			return
		}
		marks := make([]string, 0, len(online))
		for pid := range online {
			marks = append(marks, "?")
			args = append(args, pid)
		}
		where += ` AND p.user_id IN (` + strings.Join(marks, ",") + `)`
	}
	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM player_state p `+where, args...).Scan(&total); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := database.Query(`SELECT p.user_id, COALESCE(p.display_name,''), p.current_rank, p.current_xp, p.free_xp,
			p.soft_currency, p.premium_currency, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM player_ship_loadouts l WHERE l.user_id=p.user_id),
			(SELECT COUNT(*) FROM battle_results b WHERE b.user_id=p.user_id),
			(SELECT COUNT(*) FROM battle_results b WHERE b.user_id=p.user_id AND b.outcome='win'),
			(SELECT COALESCE(SUM(kills),0) FROM battle_results b WHERE b.user_id=p.user_id),
			-- the fleet of the player's current queue, else of their last match
			-- (match_slots are deleted when a match ends, so the last match's
			-- fleet comes from the player's result)
			COALESCE((SELECT q.fleet_type FROM queue_entries q WHERE q.user_id=p.user_id AND q.status='waiting' LIMIT 1),
				(SELECT b.fleet_type FROM battle_results b WHERE b.user_id=p.user_id AND b.fleet_type>0
					ORDER BY b.created_at DESC LIMIT 1), 0),
			-- an expired timed ban no longer counts (expires_at '' = permanent)
			EXISTS (SELECT 1 FROM admin_bans a WHERE a.user_id=p.user_id AND (a.expires_at='' OR a.expires_at>?)),
			EXISTS (SELECT 1 FROM admin_chat_mutes m WHERE m.user_id=p.user_id AND (m.expires_at='' OR m.expires_at>?))
		FROM player_state p `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(append([]any{now, now}, args...), limit, offset)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		PID      string `json:"pid"`
		Name     string `json:"name"`
		Rank     int    `json:"rank"`
		XP       int64  `json:"xp"`
		FreeXP   int64  `json:"free_xp"`
		Credits  int64  `json:"credits"`
		Premium  int64  `json:"premium"`
		Created  string `json:"created"`
		Updated  string `json:"updated"`
		LastSeen string `json:"last_seen"`
		Ships    int    `json:"ships"`
		Matches  int    `json:"matches"`
		Wins     int    `json:"wins"`
		Kills    int    `json:"kills"`
		IsOnline bool   `json:"online"`
		Banned   bool   `json:"banned"`
		Muted    bool   `json:"muted"`
		Fleet    string `json:"fleet,omitempty"` // queued, else last match; "" = never queued
	}
	out := []row{}
	for rows.Next() {
		var x row
		var fleetType int32
		if rows.Scan(&x.PID, &x.Name, &x.Rank, &x.XP, &x.FreeXP, &x.Credits, &x.Premium, &x.Created, &x.Updated,
			&x.Ships, &x.Matches, &x.Wins, &x.Kills, &fleetType, &x.Banned, &x.Muted) == nil {
			x.IsOnline = online[x.PID]
			x.LastSeen = x.Updated
			if fleetType > 0 {
				x.Fleet = fleetTypeName(fleetType)
			}
			out = append(out, x)
		}
	}
	writeAdminJSON(w, map[string]any{"players": out, "total": total, "offset": offset, "limit": limit})
}
