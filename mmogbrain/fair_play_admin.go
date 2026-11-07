package main

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// The admin dashboard's Fair play tab (issue #12): the figures the
// battle-server mod reports with every match result (fair_play_checks.go,
// stored as battle_result_stats "check_..."), the flags it raised, and an
// admin's review of a flag. Nothing here acts on a player: a review is a note
// for the admins, recorded in the player's audit log.

// fairPlayThresholds are the mod's flag rules (battle-server-mod
// dn_host_loadout.cpp, SPEED_* and the dealt-damage check). The mod does not
// report them, so they are copied here; keep them in step.
const fairPlayMinBotSamples = 20

var fairPlayThresholds = map[string]any{
	"speed_bot_factor":      1.5,                   // SPEED_BOT_FACTOR: sustained speed over this many times the bots' of the same class
	"speed_min_bot_samples": fairPlayMinBotSamples, // SPEED_MIN_BOT_SAMPLES: bot seconds needed before the bot rule applies
	"speed_flag_ups":        2000,                  // SPEED_FLAG_UPS: sustained speed flagged outright (units/s)
	"speed_jump_ups":        3000,                  // SPEED_JUMP_UPS: a one-second move this far is a jump (teleport/respawn), not speed
	"dealt_ratio_min":       0.8,                   // dealt: reported vs measured outside 0.8..1.25x ...
	"dealt_ratio_max":       1.25,
	"dealt_min":             10000, // ... once either side passes this
}

var fairPlayReviewStatuses = map[string]bool{"false_positive": true, "watch": true, "confirmed": true}

// shipClassLabel names an EYShipClass the way the game does.
func shipClassLabel(c int) string {
	for key, id := range eyShipClassByKey {
		if int(id) != c {
			continue
		}
		for _, size := range []string{"Light", "Medium", "Heavy"} {
			if line, ok := strings.CutSuffix(key, size); ok {
				name := map[string]string{"Dreadnought": "Dreadnought", "Scout": "Corvette", "Sniper": "Artillery Cruiser",
					"Support": "Tactical Cruiser", "Assault": "Destroyer"}[line]
				return name + " (" + size + ")"
			}
		}
	}
	return "class " + strconv.Itoa(c)
}

type fairPlaySpeed struct {
	Class      int     `json:"class"`
	ClassName  string  `json:"class_name"`
	Sustained  int64   `json:"sustained"`
	Fastest    int64   `json:"fastest"`
	Bots       int64   `json:"bots"`
	BotSamples int64   `json:"bot_samples"`
	Ratio      float64 `json:"ratio"` // sustained / bots; 0 without a bot yardstick
}

type fairPlayReview struct {
	Status string `json:"status"`
	Note   string `json:"note"`
	Actor  string `json:"actor"`
	At     string `json:"at"`
}

type fairPlayResult struct {
	MatchID          string          `json:"match_id"`
	PID              string          `json:"pid"`
	Name             string          `json:"name"`
	At               string          `json:"at"`
	Mode             string          `json:"mode"`
	Map              string          `json:"map"`
	Fleet            string          `json:"fleet"`
	Outcome          string          `json:"outcome"`
	Flags            []string        `json:"flags"`
	Speed            []fairPlaySpeed `json:"speed"`
	Jumps            int64           `json:"jumps"`
	DamageReported   int64           `json:"damage_reported"`
	DamageMeasured   int64           `json:"damage_measured"`
	DamageHits       int64           `json:"damage_hits"`
	Received         int64           `json:"received"`
	ReceivedRaw      int64           `json:"received_raw"`
	ReceivedShield   int64           `json:"received_shield"`
	ReceivedMeasured int64           `json:"received_measured"`
	ReceivedHits     int64           `json:"received_hits"`
	Review           *fairPlayReview `json:"review"`
}

func registerAdminFairPlay(api *mux.Router) {
	api.HandleFunc("/fairplay", adminAPIFairPlay).Methods(http.MethodGet)
	api.HandleFunc("/fairplay/review", adminAPIFairPlayReview).Methods(http.MethodPost)
}

// adminAPIFairPlay lists checked match results, newest first. Filters:
// flagged=1, unreviewed=1 (flagged and not yet reviewed), mode, class
// (EYShipClass flown), days, pid (one player), match (one match, every
// player).
func adminAPIFairPlay(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"no database"}`, http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	where := []string{`EXISTS (SELECT 1 FROM battle_result_stats s WHERE s.match_id=b.match_id AND s.user_id=b.user_id AND s.stat LIKE 'check\_%' ESCAPE '\')`}
	var args []any
	flaggedSQL := `EXISTS (SELECT 1 FROM battle_result_stats s WHERE s.match_id=b.match_id AND s.user_id=b.user_id AND s.stat LIKE 'check\_flag\_%' ESCAPE '\' AND s.value>0)`
	if q.Get("flagged") == "1" || q.Get("unreviewed") == "1" {
		where = append(where, flaggedSQL)
	}
	if q.Get("unreviewed") == "1" {
		where = append(where, `rv.status IS NULL`)
	}
	if v := strings.TrimSpace(q.Get("mode")); v != "" {
		where = append(where, `m.game_mode=?`)
		args = append(args, v)
	}
	if v, err := strconv.Atoi(q.Get("class")); err == nil && v > 0 && v <= 20 {
		where = append(where, `EXISTS (SELECT 1 FROM battle_result_stats s WHERE s.match_id=b.match_id AND s.user_id=b.user_id AND s.stat=?)`)
		args = append(args, "check_speed_c"+strconv.Itoa(v))
	}
	if v, err := strconv.Atoi(q.Get("days")); err == nil && v > 0 {
		where = append(where, `b.created_at >= ?`)
		args = append(args, time.Now().UTC().Add(-time.Duration(v)*24*time.Hour).Format("2006-01-02 15:04:05"))
	}
	if v := strings.TrimSpace(q.Get("pid")); v != "" {
		where = append(where, `b.user_id=?`)
		args = append(args, normalizedPlayerStatePID(v))
	}
	if v := strings.TrimSpace(q.Get("match")); v != "" {
		where = append(where, `b.match_id=?`)
		args = append(args, v)
	}
	rows, err := database.Query(`SELECT b.match_id, b.user_id, b.created_at, COALESCE(m.game_mode,''), COALESCE(m.map,''),
		b.fleet_type, b.outcome, b.damage, rv.status, COALESCE(rv.note,''), COALESCE(rv.actor,''), COALESCE(rv.updated_at,'')
		FROM battle_results b
		-- battle_results.match_id is the battle server's id (matches.battle_match_id)
		LEFT JOIN matches m ON m.battle_match_id=b.match_id AND b.match_id<>''
		LEFT JOIN fair_play_reviews rv ON rv.match_id=b.match_id AND rv.user_id=b.user_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY b.created_at DESC, b.user_id LIMIT 200`, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results := []*fairPlayResult{}
	byKey := map[[2]string]*fairPlayResult{}
	for rows.Next() {
		x := &fairPlayResult{Flags: []string{}, Speed: []fairPlaySpeed{}}
		var fleet int32
		var status sql.NullString
		var rv fairPlayReview
		if rows.Scan(&x.MatchID, &x.PID, &x.At, &x.Mode, &x.Map, &fleet, &x.Outcome, &x.DamageReported,
			&status, &rv.Note, &rv.Actor, &rv.At) != nil {
			continue
		}
		x.Map = adminMapName(x.Map)
		x.Fleet = fleetTypeName(fleet)
		if status.Valid {
			rv.Status = status.String
			x.Review = &rv
		}
		results = append(results, x)
		byKey[[2]string{x.MatchID, x.PID}] = x
	}
	_ = rows.Close()

	// The figures, per match of the page (one query per match, after the
	// rows above are closed: the database has one connection).
	matches := map[string]bool{}
	for _, x := range results {
		matches[x.MatchID] = true
	}
	for match := range matches {
		srows, err := database.Query(`SELECT user_id, stat, value FROM battle_result_stats
			WHERE match_id=? AND stat LIKE 'check\_%' ESCAPE '\'`, match)
		if err != nil {
			continue
		}
		speed := map[*fairPlayResult]map[int]*fairPlaySpeed{}
		for srows.Next() {
			var pid, stat string
			var v int64
			if srows.Scan(&pid, &stat, &v) != nil {
				continue
			}
			x := byKey[[2]string{match, pid}]
			if x == nil {
				continue
			}
			applyFairPlayStat(x, stat, v, speed)
		}
		_ = srows.Close()
		for x, classes := range speed {
			for _, s := range classes {
				if s.Bots > 0 && s.BotSamples >= fairPlayMinBotSamples {
					s.Ratio = float64(s.Sustained) / float64(s.Bots)
				}
				x.Speed = append(x.Speed, *s)
			}
			sort.Slice(x.Speed, func(i, j int) bool { return x.Speed[i].Class < x.Speed[j].Class })
		}
	}
	names := fairPlayNames(database, results)
	for _, x := range results {
		sort.Strings(x.Flags)
		x.Name = names[x.PID]
		if x.Name == "" || x.Name == "Local" {
			x.Name = "?"
		}
	}

	modes := []string{}
	if mrows, err := database.Query(`SELECT DISTINCT game_mode FROM matches WHERE game_mode<>'' ORDER BY game_mode`); err == nil {
		for mrows.Next() {
			var m string
			if mrows.Scan(&m) == nil {
				modes = append(modes, m)
			}
		}
		_ = mrows.Close()
	}
	out := map[string]any{"results": results, "thresholds": fairPlayThresholds, "modes": modes}
	if pid := strings.TrimSpace(q.Get("pid")); pid != "" {
		var checked, flagged, reviewed int
		_ = database.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(EXISTS (SELECT 1 FROM battle_result_stats s WHERE s.match_id=b.match_id AND s.user_id=b.user_id AND s.stat LIKE 'check\_flag\_%' ESCAPE '\' AND s.value>0)),0),
			COALESCE(SUM(EXISTS (SELECT 1 FROM fair_play_reviews rv WHERE rv.match_id=b.match_id AND rv.user_id=b.user_id)),0)
			FROM battle_results b WHERE b.user_id=? AND EXISTS (SELECT 1 FROM battle_result_stats s WHERE s.match_id=b.match_id AND s.user_id=b.user_id AND s.stat LIKE 'check\_%' ESCAPE '\')`,
			normalizedPlayerStatePID(pid)).Scan(&checked, &flagged, &reviewed)
		out["player"] = map[string]any{"pid": normalizedPlayerStatePID(pid), "name": adminPlayerName(pid),
			"checked": checked, "flagged": flagged, "reviewed": reviewed}
	}
	writeAdminJSON(w, out)
}

// applyFairPlayStat files one "check_..." stat into the result.
func applyFairPlayStat(x *fairPlayResult, stat string, v int64, speed map[*fairPlayResult]map[int]*fairPlaySpeed) {
	switch stat {
	case "check_jumps":
		x.Jumps = v
	case "check_damage_measured":
		x.DamageMeasured = v
	case "check_damage_hits":
		x.DamageHits = v
	case "check_received":
		x.Received = v
	case "check_received_raw":
		x.ReceivedRaw = v
	case "check_received_shield":
		x.ReceivedShield = v
	case "check_received_measured":
		x.ReceivedMeasured = v
	case "check_received_hits":
		x.ReceivedHits = v
	}
	if flag, ok := strings.CutPrefix(stat, "check_flag_"); ok && v > 0 {
		x.Flags = append(x.Flags, flag)
		return
	}
	rest, ok := strings.CutPrefix(stat, "check_speed_c")
	if !ok {
		return
	}
	num, field, _ := strings.Cut(rest, "_")
	c, err := strconv.Atoi(num)
	if err != nil {
		return
	}
	if speed[x] == nil {
		speed[x] = map[int]*fairPlaySpeed{}
	}
	s := speed[x][c]
	if s == nil {
		s = &fairPlaySpeed{Class: c, ClassName: shipClassLabel(c)}
		speed[x][c] = s
	}
	switch field {
	case "":
		s.Sustained = v
	case "fastest":
		s.Fastest = v
	case "bots":
		s.Bots = v
	case "bot_samples":
		s.BotSamples = v
	}
}

// adminAPIFairPlayReview records (or, with status "", clears) an admin's
// review of one checked match result. It is a note, recorded in the
// player's audit log; it changes nothing for the player.
func adminAPIFairPlayReview(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"no database"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		MatchID string `json:"match_id"`
		PID     string `json:"pid"`
		Status  string `json:"status"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Status != "" && !fairPlayReviewStatuses[req.Status] {
		http.Error(w, `{"error":"status must be false_positive, watch or confirmed"}`, http.StatusBadRequest)
		return
	}
	if len(req.Note) > 500 {
		http.Error(w, `{"error":"note too long (500 characters at most)"}`, http.StatusBadRequest)
		return
	}
	pid := normalizedPlayerStatePID(req.PID)
	var n int
	if strings.TrimSpace(req.MatchID) == "" || strings.TrimSpace(req.PID) == "" ||
		database.QueryRow(`SELECT COUNT(*) FROM battle_results WHERE match_id=? AND user_id=?`, req.MatchID, pid).Scan(&n) != nil || n == 0 {
		http.Error(w, `{"error":"no such match result"}`, http.StatusNotFound)
		return
	}
	actor := r.RemoteAddr
	if host, _, err := net.SplitHostPort(actor); err == nil {
		actor = host
	}
	var err error
	if req.Status == "" {
		_, err = database.Exec(`DELETE FROM fair_play_reviews WHERE match_id=? AND user_id=?`, req.MatchID, pid)
	} else {
		_, err = database.Exec(`INSERT INTO fair_play_reviews(match_id,user_id,status,note,actor) VALUES(?,?,?,?,?)
			ON CONFLICT(match_id,user_id) DO UPDATE SET status=excluded.status, note=excluded.note, actor=excluded.actor, updated_at=datetime('now')`,
			req.MatchID, pid, req.Status, req.Note, actor)
	}
	if err != nil {
		http.Error(w, `{"error":"could not save the review"}`, http.StatusInternalServerError)
		return
	}
	adminAudit(database, r, "fair play review", pid, map[string]any{"match": req.MatchID, "status": req.Status, "note": req.Note})
	writeAdminJSON(w, map[string]any{"ok": true})
}

// fairPlayNames reads the players' names in one query (adminPlayerName loads
// a whole player state, which for a page of 200 results held the database's
// one connection for most of a second).
func fairPlayNames(database *sql.DB, results []*fairPlayResult) map[string]string {
	names := map[string]string{}
	if len(results) == 0 {
		return names
	}
	seen := map[string]bool{}
	var args []any
	for _, x := range results {
		if !seen[x.PID] {
			seen[x.PID] = true
			args = append(args, x.PID)
		}
	}
	rows, err := database.Query(`SELECT user_id, display_name FROM player_state WHERE user_id IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return names
	}
	for rows.Next() {
		var pid, name string
		if rows.Scan(&pid, &name) == nil {
			names[pid] = name
		}
	}
	_ = rows.Close()
	return names
}
