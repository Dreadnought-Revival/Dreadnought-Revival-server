package main

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Metrics for the admin dashboard. Two sources:
//
//   - admin_metrics_samples: the server's load (online, in a match, queued,
//     battle servers) sampled every adminMetricsInterval -- the only history
//     of who was online, which nothing else records.
//   - the game tables themselves (matches, match_slots, battle_results,
//     player_state) and the battle logs, aggregated per UTC day on request.

const (
	adminMetricsInterval  = 5 * time.Minute
	adminMetricsRetention = 90 * 24 * time.Hour
	adminSampleTimeFormat = "2006-01-02 15:04:05"
)

func startAdminMetricsSampler(log *logrus.Logger) {
	go func() {
		// First sample soon after a restart, so a restart leaves no hole.
		timer := time.NewTimer(time.Minute)
		for range timer.C {
			adminTakeMetricsSample(log)
			timer.Reset(adminMetricsInterval)
		}
	}()
}

func adminTakeMetricsSample(log *logrus.Logger) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return
	}
	online := len(socialHubInstance.onlinePlayers())
	var inMatch, queued int
	_ = database.QueryRow(`SELECT COUNT(DISTINCT s.user_id) FROM match_slots s JOIN matches m ON m.id=s.match_id
		WHERE m.status='active'`).Scan(&inMatch)
	_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries WHERE status='waiting'`).Scan(&queued)
	// In a match: the players the battle servers report; match_slots is the
	// fallback (a player who leaves early loses their slot).
	instances := 0
	if list, err := controlPlaneInstances(); err == nil {
		instances = len(list)
		players := 0
		for _, i := range list {
			players += len(i.Players)
		}
		if players > inMatch {
			inMatch = players
		}
	}
	now := time.Now().UTC()
	if _, err := database.Exec(`INSERT OR REPLACE INTO admin_metrics_samples(ts,online,in_match,queued,instances) VALUES(?,?,?,?,?)`,
		now.Format(adminSampleTimeFormat), online, inMatch, queued, instances); err != nil && log != nil {
		log.WithError(err).Warn("admin metrics: sample not stored")
	}
	_, _ = database.Exec(`DELETE FROM admin_metrics_samples WHERE ts < ?`, now.Add(-adminMetricsRetention).Format(adminSampleTimeFormat))
}

type adminMetricsSample struct {
	TS        string `json:"ts"`
	Online    int    `json:"online"`
	InMatch   int    `json:"in_match"`
	Queued    int    `json:"queued"`
	Instances int    `json:"instances"`
}

type adminMetricsDay struct {
	Day         string `json:"day"`
	Matches     int    `json:"matches"`
	Players     int    `json:"players"` // distinct players with a result
	Results     int    `json:"results"`
	NewAccounts int    `json:"new_accounts"`
	Credits     int64  `json:"credits"`
	XP          int64  `json:"xp"`
	Kills       int    `json:"kills"`
	PeakOnline  int    `json:"peak_online"`
	Crashes     int    `json:"crashes"`
	HostLogs    int    `json:"host_logs"`
}

type adminMetricsMode struct {
	Mode       string  `json:"mode"`
	Name       string  `json:"name"` // player-facing, "" when the mode has none
	Matches    int     `json:"matches"`
	AvgMinutes float64 `json:"avg_minutes"`
	AvgPlayers float64 `json:"avg_players"`
}

type adminMetricsTopPlayer struct {
	PID     string `json:"pid"`
	Name    string `json:"name"`
	Matches int    `json:"matches"`
	Wins    int    `json:"wins"`
	Kills   int    `json:"kills"`
}

type adminMetricsMap struct {
	Map     string `json:"map"` // in-game name (adminMapName)
	Matches int    `json:"matches"`
}

type adminMetricsShipClass struct {
	Class   string `json:"class"` // the Statistics ship type, e.g. "Destroyer"
	Seconds int64  `json:"seconds"`
	Players int    `json:"players"`
}

type adminMetricsBalance struct {
	Mode      string `json:"mode"`
	Name      string `json:"name"`
	Team0Wins int    `json:"team0_wins"`
	Team1Wins int    `json:"team1_wins"`
}

// adminRetention: accounts created inside the window (player_state.created_at
// from `first` on) form the cohort. An account "returned on day N" when it has
// at least one battle_results row on the UTC calendar day N days after its
// creation day; battle results are the only per-account activity history (a
// login stores only the latest date). An account counts toward dayN_eligible
// only when that day has completely passed (before today UTC), so recent
// signups do not drag the share down. Shares are returned / eligible, 0 when
// nobody is eligible.
func adminRetention(database *sql.DB, first, now time.Time) map[string]any {
	since := first.Format(adminSampleTimeFormat)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	out := map[string]any{}
	var cohort int
	_ = database.QueryRow(`SELECT COUNT(*) FROM player_state WHERE created_at >= ?`, since).Scan(&cohort)
	out["cohort"] = cohort
	for _, n := range []int{1, 7} {
		mod := "+" + strconv.Itoa(n) + " day"
		var eligible, returned int
		_ = database.QueryRow(`SELECT COUNT(*) FROM player_state p WHERE p.created_at >= ? AND date(p.created_at, ?) < ?`,
			since, mod, today).Scan(&eligible)
		_ = database.QueryRow(`SELECT COUNT(*) FROM player_state p WHERE p.created_at >= ? AND date(p.created_at, ?) < ?
			AND EXISTS (SELECT 1 FROM battle_results b WHERE b.user_id=p.user_id AND date(b.created_at)=date(p.created_at, ?))`,
			since, mod, today, mod).Scan(&returned)
		share := 0.0
		if eligible > 0 {
			share = float64(returned) / float64(eligible)
		}
		k := "day" + strconv.Itoa(n)
		out[k+"_eligible"], out[k+"_returned"], out[k+"_share"] = eligible, returned, share
	}
	return out
}

// GET /admin/api/metrics?days=N (1..90, default 7).
func adminAPIMetrics(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"database unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	writeAdminJSON(w, adminCollectMetrics(database, days, time.Now().UTC()))
}

func adminCollectMetrics(database *sql.DB, days int, now time.Time) map[string]any {
	// Whole UTC days: today plus the days-1 before it.
	first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	since := first.Format(adminSampleTimeFormat) // datetime('now') columns
	sinceRFC := first.Format(time.RFC3339)       // matches.* (RFC 3339)

	daily := make([]adminMetricsDay, days)
	byDay := map[string]*adminMetricsDay{}
	for i := range daily {
		daily[i].Day = first.AddDate(0, 0, i).Format("2006-01-02")
		byDay[daily[i].Day] = &daily[i]
	}
	// Each query is read to the end and closed before the next one: one
	// database connection (a query inside a rows loop deadlocks).
	each := func(q string, scan func(rows *sql.Rows) error, args ...any) {
		rows, err := database.Query(q, args...)
		if err != nil {
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			_ = scan(rows)
		}
	}
	each(`SELECT substr(created_at,1,10), COUNT(*) FROM matches WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var d string
		var n int
		err := rows.Scan(&d, &n)
		if x := byDay[d]; err == nil && x != nil {
			x.Matches = n
		}
		return err
	}, sinceRFC)
	each(`SELECT substr(created_at,1,10), COUNT(DISTINCT user_id), COUNT(*), COALESCE(SUM(credits),0), COALESCE(SUM(xp),0),
		COALESCE(SUM(kills),0) FROM battle_results WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var d string
		var p, n, k int
		var c, xp int64
		err := rows.Scan(&d, &p, &n, &c, &xp, &k)
		if x := byDay[d]; err == nil && x != nil {
			x.Players, x.Results, x.Credits, x.XP, x.Kills = p, n, c, xp, k
		}
		return err
	}, since)
	each(`SELECT substr(created_at,1,10), COUNT(*) FROM player_state WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var d string
		var n int
		err := rows.Scan(&d, &n)
		if x := byDay[d]; err == nil && x != nil {
			x.NewAccounts = n
		}
		return err
	}, since)
	each(`SELECT substr(ts,1,10), MAX(online) FROM admin_metrics_samples WHERE ts >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var d string
		var n int
		err := rows.Scan(&d, &n)
		if x := byDay[d]; err == nil && x != nil {
			x.PeakOnline = n
		}
		return err
	}, since)
	for _, f := range adminBattleLogCrashes(first) {
		if x := byDay[f.day]; x != nil {
			x.HostLogs++
			if f.crash != "" {
				x.Crashes++
			}
		}
	}

	// Load samples: raw up to two days, else the hourly peak.
	samples := []adminMetricsSample{}
	sampleQuery := `SELECT ts, online, in_match, queued, instances FROM admin_metrics_samples WHERE ts >= ? ORDER BY ts`
	if days > 2 {
		sampleQuery = `SELECT substr(ts,1,13)||':00:00', MAX(online), MAX(in_match), MAX(queued), MAX(instances)
			FROM admin_metrics_samples WHERE ts >= ? GROUP BY substr(ts,1,13) ORDER BY 1`
	}
	each(sampleQuery, func(rows *sql.Rows) error {
		var s adminMetricsSample
		err := rows.Scan(&s.TS, &s.Online, &s.InMatch, &s.Queued, &s.Instances)
		if err == nil {
			samples = append(samples, s)
		}
		return err
	}, since)

	modes := []adminMetricsMode{}
	each(`SELECT m.game_mode, COUNT(*),
		COALESCE(AVG(CASE WHEN m.ended_at IS NOT NULL AND m.started_at IS NOT NULL
			THEN (julianday(m.ended_at)-julianday(m.started_at))*1440 END),0),
		-- players = results reported for the match (match_slots are deleted at the end)
		COALESCE(AVG(CASE WHEN m.battle_match_id<>'' THEN
			(SELECT COUNT(*) FROM battle_results b WHERE b.match_id=m.battle_match_id) END),0)
		FROM matches m WHERE m.created_at >= ? GROUP BY m.game_mode ORDER BY 2 DESC`, func(rows *sql.Rows) error {
		var m adminMetricsMode
		err := rows.Scan(&m.Mode, &m.Matches, &m.AvgMinutes, &m.AvgPlayers)
		if err == nil {
			m.AvgMinutes = float64(int(m.AvgMinutes*10+0.5)) / 10
			m.AvgPlayers = float64(int(m.AvgPlayers*10+0.5)) / 10
			m.Name = serverChatModeNames[m.Mode]
			modes = append(modes, m)
		}
		return err
	}, sinceRFC)

	hours := make([]int, 24) // matches started per UTC hour of day
	each(`SELECT CAST(substr(created_at,12,2) AS INTEGER), COUNT(*) FROM matches WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var h, n int
		err := rows.Scan(&h, &n)
		if err == nil && h >= 0 && h < 24 {
			hours[h] = n
		}
		return err
	}, sinceRFC)

	fleets := map[string]int{}
	each(`SELECT fleet_type, COUNT(*) FROM matches WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var f int32
		var n int
		err := rows.Scan(&f, &n)
		if err == nil {
			fleets[fleetTypeName(f)] += n
		}
		return err
	}, sinceRFC)

	outcomes := map[string]int{}
	each(`SELECT outcome, COUNT(*) FROM battle_results WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var o string
		var n int
		err := rows.Scan(&o, &n)
		if err == nil {
			outcomes[o] = n
		}
		return err
	}, since)

	top := []adminMetricsTopPlayer{}
	// Names by JOIN: adminPlayerName would also create a player_state row for
	// an id that has results but no account.
	each(`SELECT b.user_id, COALESCE(ps.display_name,''), COUNT(*), COALESCE(SUM(b.outcome='win'),0), COALESCE(SUM(b.kills),0)
		FROM battle_results b LEFT JOIN player_state ps ON ps.user_id=b.user_id
		WHERE b.created_at >= ? GROUP BY b.user_id ORDER BY 3 DESC, 5 DESC, b.user_id LIMIT 15`, func(rows *sql.Rows) error {
		var p adminMetricsTopPlayer
		err := rows.Scan(&p.PID, &p.Name, &p.Matches, &p.Wins, &p.Kills)
		if err == nil {
			if p.Name == "" || p.Name == "Local" {
				p.Name = "?"
			}
			top = append(top, p)
		}
		return err
	}, since)

	// Active players in the window, and how many of them had played before it.
	var active, returning int
	_ = database.QueryRow(`SELECT COUNT(DISTINCT user_id) FROM battle_results WHERE created_at >= ?`, since).Scan(&active)
	_ = database.QueryRow(`SELECT COUNT(DISTINCT b.user_id) FROM battle_results b WHERE b.created_at >= ?
		AND EXISTS (SELECT 1 FROM battle_results o WHERE o.user_id=b.user_id AND o.created_at < ?)`, since, since).Scan(&returning)

	// Map popularity: matches per in-game map name (the map path's folder).
	mapCounts := map[string]int{}
	each(`SELECT map, COUNT(*) FROM matches WHERE created_at >= ? GROUP BY 1`, func(rows *sql.Rows) error {
		var p string
		var n int
		err := rows.Scan(&p, &n)
		if err == nil {
			mapCounts[adminMapName(p)] += n
		}
		return err
	}, sinceRFC)
	maps := make([]adminMetricsMap, 0, len(mapCounts))
	for name, n := range mapCounts {
		maps = append(maps, adminMetricsMap{Map: name, Matches: n})
	}
	sort.Slice(maps, func(i, j int) bool {
		if maps[i].Matches != maps[j].Matches {
			return maps[i].Matches > maps[j].Matches
		}
		return maps[i].Map < maps[j].Map
	})

	// Ship class usage: the seconds players spent alive per ship type, from
	// battle_result_stats rows time_<Type> (match_statistics.go: reported by the
	// mod as ct=, or an even split over the ships flown for an older mod -- so
	// the split part is an estimate). Counted for results inside the window.
	shipClasses := []adminMetricsShipClass{}
	each(`SELECT s.stat, COALESCE(SUM(s.value),0), COUNT(DISTINCT s.user_id) FROM battle_result_stats s
		JOIN battle_results b ON b.match_id=s.match_id AND b.user_id=s.user_id
		WHERE b.created_at >= ? AND s.stat LIKE 'time\_%' ESCAPE '\' AND s.value > 0 GROUP BY s.stat ORDER BY 2 DESC, 1`, func(rows *sql.Rows) error {
		var c adminMetricsShipClass
		var stat string
		err := rows.Scan(&stat, &c.Seconds, &c.Players)
		if err == nil {
			c.Class = strings.TrimPrefix(stat, statTimePrefix)
			shipClasses = append(shipClasses, c)
		}
		return err
	}, since)

	// Team win balance: per mode, the matches whose winning side was team 0
	// and team 1. A match counts for a team when that team has a result with
	// outcome 'win'. Draws and one-sided modes (every player on team 0, e.g.
	// Onslaught) simply show up as team 0 wins or are absent.
	balance := []adminMetricsBalance{}
	balIdx := map[string]int{}
	each(`SELECT m.game_mode, b.team, COUNT(DISTINCT b.match_id) FROM battle_results b
		JOIN matches m ON m.battle_match_id=b.match_id WHERE b.created_at >= ? AND b.outcome='win' AND b.team IN (0,1)
		GROUP BY 1,2 ORDER BY 1,2`, func(rows *sql.Rows) error {
		var mode string
		var team, n int
		err := rows.Scan(&mode, &team, &n)
		if err != nil {
			return err
		}
		i, ok := balIdx[mode]
		if !ok {
			i = len(balance)
			balIdx[mode] = i
			balance = append(balance, adminMetricsBalance{Mode: mode, Name: serverChatModeNames[mode]})
		}
		if team == 0 {
			balance[i].Team0Wins = n
		} else {
			balance[i].Team1Wins = n
		}
		return nil
	}, since)

	retention := adminRetention(database, first, now)

	totals := adminMetricsDay{Day: "total"}
	for _, d := range daily {
		totals.Matches += d.Matches
		totals.Results += d.Results
		totals.NewAccounts += d.NewAccounts
		totals.Credits += d.Credits
		totals.XP += d.XP
		totals.Kills += d.Kills
		totals.Crashes += d.Crashes
		totals.HostLogs += d.HostLogs
		if d.PeakOnline > totals.PeakOnline {
			totals.PeakOnline = d.PeakOnline
		}
	}
	totals.Players = active

	return map[string]any{
		"days":              days,
		"since":             first.Format(time.RFC3339),
		"sample_minutes":    int(adminMetricsInterval / time.Minute),
		"samples_hourly":    days > 2,
		"samples":           samples,
		"daily":             daily,
		"totals":            totals,
		"returning_players": returning,
		"modes":             modes,
		"hours_utc":         hours,
		"fleets":            fleets,
		"outcomes":          outcomes,
		"top_players":       top,
		"maps":              maps,
		"ship_classes":      shipClasses,
		"team_balance":      balance,
		"retention":         retention,
	}
}

// --- battle-log crash scan ----------------------------------------------------

type adminLogCrash struct {
	day   string // UTC day of the log's last write
	crash string // "" = ended cleanly
}

// adminCrashCache remembers each battle log's verdict by name and size, so
// the scan reads only logs that are new or still growing.
var adminCrashCache = struct {
	mu    sync.Mutex
	files map[string]struct {
		size  int64
		crash string
	}
}{files: map[string]struct {
	size  int64
	crash string
}{}}

func adminBattleLogCrashes(since time.Time) []adminLogCrash {
	out := []adminLogCrash{}
	adminCrashCache.mu.Lock()
	defer adminCrashCache.mu.Unlock()
	for _, f := range newestBattleLogs(1 << 20) {
		if f.mod.Before(since) {
			break // newest first
		}
		c, ok := adminCrashCache.files[f.name]
		if !ok || c.size != f.size {
			c.size, c.crash = f.size, battleLogTailCrash(tailFile(f.path, 96*1024))
			adminCrashCache.files[f.name] = c
		}
		out = append(out, adminLogCrash{day: f.mod.UTC().Format("2006-01-02"), crash: c.crash})
	}
	return out
}

// battleLogTailCrash classifies a battle log's tail like the battle-logs tab.
func battleLogTailCrash(tail string) string {
	switch {
	case strings.Contains(tail, "EXCEPTION_STACK_OVERFLOW"):
		return "stack overflow"
	case strings.Contains(tail, "Unhandled Exception"), strings.Contains(tail, "Unhandled page fault"):
		return "access violation"
	}
	return ""
}
