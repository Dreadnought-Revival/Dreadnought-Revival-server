package main

// One match in detail for the dashboard's Match page, and the lookup of the
// battle log a match was hosted under (shared with the Battle servers page).

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminMatchDetail) }

func registerAdminMatchDetail(api *mux.Router) {
	api.HandleFunc("/matches/{id}", adminAPIMatchDetail).Methods(http.MethodGet)
}

// battleLogNameRE is the name dn-dedicated gives a host's log:
// battle-<local start time>-port<port>.log (see battleLogCrash).
var battleLogNameRE = regexp.MustCompile(`^battle-(\d{8}-\d{6})-port(\d+)\.log$`)

// adminBattleLogFor finds the battle log of a match: logs of its port started
// up to 5 minutes after the match row was made; the newest one whose head
// names the battle id wins, else (ids missing from older logs) the newest of
// them. The match row is written after the host started, so a log AFTER it is
// never the match's. Returns "" when none is found. GUESS: a port reused
// within minutes with no id in the log head can pick the wrong log; the head
// check covers every log the current dn-dedicated writes (-MatchID= is in its
// command line, verified only by reading logs, not by a test against live).
func adminBattleLogFor(port int, created, battleID string) string {
	ct, err := time.Parse(time.RFC3339, created)
	if err != nil || port <= 0 {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join("run", "battle-logs"))
	if err != nil {
		return ""
	}
	type cand struct {
		name string
		at   time.Time
	}
	var cands []cand
	for _, e := range entries {
		m := battleLogNameRE.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil || m[2] != strconv.Itoa(port) {
			continue
		}
		at, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil || at.After(ct.Add(5*time.Minute)) || at.Before(ct.Add(-12*time.Hour)) {
			continue
		}
		cands = append(cands, cand{e.Name(), at})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at.After(cands[j].at) })
	if len(cands) > 6 {
		cands = cands[:6]
	}
	if battleID != "" {
		for _, c := range cands {
			if strings.Contains(headFile(filepath.Join("run", "battle-logs", c.name), 32*1024), battleID) {
				return c.name
			}
		}
	}
	if len(cands) > 0 {
		return cands[0].name
	}
	return ""
}

type adminMatchPlayer struct {
	PID        string           `json:"pid"`
	Name       string           `json:"name"`
	Team       int              `json:"team"`
	Round      string           `json:"round,omitempty"` // battle_results.match_id when it differs from the battle id ("-r2")
	Outcome    string           `json:"outcome"`
	Kills      int              `json:"kills"`
	Deaths     int              `json:"deaths"`
	Assists    int              `json:"assists"`
	Damage     int              `json:"damage"`
	Credits    int              `json:"credits"`
	XP         int              `json:"xp"`
	Fleet      int              `json:"fleet_type"`
	At         string           `json:"reported"`
	Stats      map[string]int64 `json:"stats"`
	Flags      []string         `json:"flags"`            // check_flag_* the fair-play checks raised
	Checked    bool             `json:"checked"`          // the checks ran for this result
	Review     string           `json:"review,omitempty"` // fair_play_reviews.status
	ReviewNote string           `json:"review_note,omitempty"`
}

// adminAPIMatchDetail: GET /matches/{id}; id is matches.id or the battle
// server's id (matches.battle_match_id, also what battle_results use). A
// battle id with results but no match row (a host started by hand) answers
// with a match that has only its battle id.
func adminAPIMatchDetail(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" || len(id) > 128 {
		writeAdminError(w, http.StatusBadRequest, "bad match id")
		return
	}
	m := &adminMatchRow{Results: []adminMatchResult{}}
	var instance string
	err := database.QueryRow(`SELECT id, COALESCE(battle_match_id,''), game_mode, map, status, created_at, COALESCE(started_at,''),
		COALESCE(ended_at,''), COALESCE(fleet_type,0), COALESCE(server_port,0), COALESCE(instance_id,'')
		FROM matches WHERE id=? OR (battle_match_id<>'' AND battle_match_id=?) ORDER BY id=? DESC LIMIT 1`, id, id, id).
		Scan(&m.ID, &m.BattleID, &m.Mode, &m.Map, &m.Status, &m.Created, &m.Started, &m.Ended, &m.FleetType, &m.Port, &instance)
	battle := id
	switch {
	case err == nil:
		m.Map = adminMapName(m.Map)
		battle = m.BattleID
	case err == sql.ErrNoRows:
		var n int
		_ = database.QueryRow(`SELECT COUNT(*) FROM battle_results WHERE match_id=? OR match_id LIKE ?||'-r%' ESCAPE '\'`, id, adminLikeEscape(id)).Scan(&n)
		if n == 0 {
			writeAdminError(w, http.StatusNotFound, "no such match")
			return
		}
		m = &adminMatchRow{BattleID: id, Status: "unknown", Results: []adminMatchResult{}}
	default:
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}

	players := []*adminMatchPlayer{}
	byKey := map[[2]string]*adminMatchPlayer{}
	slots := []map[string]any{}
	havoc := []map[string]any{}
	if battle != "" {
		// One query: results + names + this player's fair-play review.
		// match_id LIKE uses the raw id: '_' in it only over-matches, and the
		// base-id check below drops the strays.
		rows, err := database.Query(`SELECT b.match_id, b.user_id, COALESCE(p.display_name,''), b.team, b.outcome, b.kills, b.deaths,
			b.assists, b.damage, b.credits, b.xp, b.fleet_type, b.created_at, COALESCE(rv.status,''), COALESCE(rv.note,'')
			FROM battle_results b
			LEFT JOIN player_state p ON p.user_id=b.user_id
			LEFT JOIN fair_play_reviews rv ON rv.match_id=b.match_id AND rv.user_id=b.user_id
			WHERE b.match_id=? OR b.match_id LIKE ?||'-r%' ESCAPE '\' ORDER BY b.match_id, b.team, b.kills DESC, b.user_id`, battle, adminLikeEscape(battle))
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for rows.Next() {
			x := &adminMatchPlayer{Stats: map[string]int64{}, Flags: []string{}}
			var mid string
			if rows.Scan(&mid, &x.PID, &x.Name, &x.Team, &x.Outcome, &x.Kills, &x.Deaths, &x.Assists, &x.Damage, &x.Credits, &x.XP,
				&x.Fleet, &x.At, &x.Review, &x.ReviewNote) != nil || adminBaseBattleID(mid) != battle {
				continue
			}
			if mid != battle {
				x.Round = mid
			}
			x.Name = adminShownName(x.Name)
			players = append(players, x)
			byKey[[2]string{mid, x.PID}] = x
		}
		_ = rows.Close()

		srows, err := database.Query(`SELECT match_id, user_id, stat, value FROM battle_result_stats WHERE match_id=? OR match_id LIKE ?||'-r%' ESCAPE '\'`, battle, adminLikeEscape(battle))
		if err == nil {
			for srows.Next() {
				var mid, pid, stat string
				var v int64
				if srows.Scan(&mid, &pid, &stat, &v) != nil {
					continue
				}
				x := byKey[[2]string{mid, pid}]
				if x == nil {
					continue
				}
				switch {
				case strings.HasPrefix(stat, "check_flag_"):
					x.Checked = true
					if v > 0 {
						x.Flags = append(x.Flags, strings.TrimPrefix(stat, "check_flag_"))
					}
				case strings.HasPrefix(stat, "check_"):
					x.Checked = true // the figures themselves live on the Fair play page
				default:
					x.Stats[stat] = v
				}
			}
			_ = srows.Close()
		}
		for _, x := range players {
			sort.Strings(x.Flags)
		}

		if hrows, err := database.Query(`SELECT h.user_id, COALESCE(p.display_name,''), h.score, h.waves, h.created_at FROM havoc_runs h
			LEFT JOIN player_state p ON p.user_id=h.user_id WHERE h.match_id=? OR h.match_id LIKE ?||'-r%' ESCAPE '\' ORDER BY h.score DESC, h.user_id`, battle, adminLikeEscape(battle)); err == nil {
			for hrows.Next() {
				var pid, name, at string
				var score, waves int
				if hrows.Scan(&pid, &name, &score, &waves, &at) == nil {
					havoc = append(havoc, map[string]any{"pid": pid, "name": adminShownName(name), "score": score, "waves": waves, "at": at})
				}
			}
			_ = hrows.Close()
		}
	}
	if m.ID != "" {
		if srows, err := database.Query(`SELECT s.user_id, COALESCE(p.display_name,''), s.team, s.joined_at FROM match_slots s
			LEFT JOIN player_state p ON p.user_id=s.user_id WHERE s.match_id=? ORDER BY s.team, s.joined_at LIMIT 200`, m.ID); err == nil {
			for srows.Next() {
				var pid, name, at string
				var team int
				if srows.Scan(&pid, &name, &team, &at) == nil {
					slots = append(slots, map[string]any{"pid": pid, "name": adminShownName(name), "team": team, "joined": at})
				}
			}
			_ = srows.Close()
		}
	}

	// Summary figures, the same as a list row.
	m.Players = len(players)
	for _, x := range players {
		m.Kills += x.Kills
		if x.Outcome == "win" && m.Winner == 0 {
			m.Winner = x.Team
		}
	}
	if m.Players == 0 {
		m.Players = len(slots)
	}
	switch {
	case len(players) == 0:
	case m.Winner > 0:
		m.Outcome = "win"
	default:
		m.Outcome = "unknown"
		for _, x := range players {
			if x.Outcome == "draw" {
				m.Outcome = "draw"
			}
		}
	}
	flagged := 0
	for _, x := range players {
		if len(x.Flags) > 0 {
			flagged++
		}
	}
	log := ""
	if m.Port > 0 && m.Created != "" {
		log = adminBattleLogFor(m.Port, m.Created, battle)
	}
	writeAdminJSON(w, map[string]any{
		"match": m, "instance_id": instance, "results": players, "slots": slots, "havoc": havoc, "log": log,
		"flagged": flagged,
		// player_ribbons keeps running totals per player, not per match, so a match's
		// ribbons are not stored anywhere; the stats above are what is per match.
		"ribbons_stored": false,
	})
}
