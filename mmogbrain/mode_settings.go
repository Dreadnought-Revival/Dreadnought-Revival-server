package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// Game mode and queue switches on the admin dashboard (issue #18): which
// queue runs as Escalation, whether the Havoc and Custom Game tiles are
// listed, and how big a match gets / how long it waits. They used to be
// environment variables in run/secrets.env, changed with an mmogbrain
// restart at an idle moment. The environment still gives the defaults; a
// dashboard change is stored in server_flags and applies at once to the
// matchmaker. A tile change reaches a client at its next login (the mode
// list is sent then).

const (
	flagEscalationQueue = "mode_escalation_queue"
	flagHavocListed     = "mode_havoc"
	flagCustomGame      = "mode_custom_game"
	flagMatchMaxPlayers = "match_max_players"
	flagMatchMaxWait    = "match_max_wait_s"

	maxMatchPlayers = 120 // two teams of matchmaker.MaxTeamSize
	maxMatchWait    = 600 * time.Second
)

// envMatchSizing is what configureMatchAutoscale set from the environment,
// what a cleared override goes back to.
var envMatchSizing struct {
	players int
	wait    time.Duration
}

// applyModeSettings puts the stored overrides in force at startup.
func applyModeSettings(database *sql.DB, mm *matchmaker.Matchmaker, log *logrus.Logger) {
	envMatchSizing.players, envMatchSizing.wait = mm.Sizing()
	if database == nil {
		return
	}
	flags := map[string]string{}
	rows, err := database.Query(`SELECT name, value FROM server_flags WHERE name IN (?,?,?,?,?) OR name LIKE 'team\_size:%' ESCAPE '\'`,
		flagEscalationQueue, flagHavocListed, flagCustomGame, flagMatchMaxPlayers, flagMatchMaxWait)
	if err != nil {
		return
	}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			flags[k] = v
		}
	}
	_ = rows.Close()
	if v, ok := flags[flagEscalationQueue]; ok {
		matchmaker.SetEscalationQueueOverride(&v)
	}
	for k, v := range flags {
		if mode, ok := strings.CutPrefix(k, "team_size:"); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= matchmaker.MaxTeamSize {
				matchmaker.SetTeamSizeOverride(mode, &n)
			}
		}
	}
	if v, ok := flags[flagHavocListed]; ok {
		b := v == "1"
		matchmaker.SetHavocListedOverride(&b)
	}
	if v, ok := flags[flagCustomGame]; ok {
		b := v == "1"
		matchmaker.SetCustomGameListedOverride(&b)
	}
	players, wait := envMatchSizing.players, envMatchSizing.wait
	if n, err := strconv.Atoi(flags[flagMatchMaxPlayers]); err == nil && n >= 1 && n <= maxMatchPlayers {
		players = n
	}
	if n, err := strconv.Atoi(flags[flagMatchMaxWait]); err == nil && n >= 0 && time.Duration(n)*time.Second <= maxMatchWait {
		wait = time.Duration(n) * time.Second
	}
	mm.SetSizing(players, wait)
	if len(flags) > 0 {
		log.WithField("overrides", flags).Info("matchmaker: game mode switches from the admin dashboard")
	}
}

func registerAdminModeSettings(api *mux.Router) {
	api.HandleFunc("/modes", adminAPIModes).Methods(http.MethodGet)
	api.HandleFunc("/modes", adminAPISetModes).Methods(http.MethodPost)
}

type adminSwitch struct {
	Value     any  `json:"value"`
	Env       any  `json:"env"`
	Dashboard bool `json:"dashboard"` // set on the dashboard, not by the environment
}

func storedFlags(database *sql.DB) map[string]bool {
	out := map[string]bool{}
	if database == nil {
		return out
	}
	rows, err := database.Query(`SELECT name FROM server_flags WHERE name LIKE 'mode\_%' ESCAPE '\' OR name LIKE 'match\_%' ESCAPE '\'`)
	if err != nil {
		return out
	}
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	_ = rows.Close()
	return out
}

// queueableModes are the queues a client can pick, the escalation choices.
func queueableModes() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range matchmaker.GameModeList() {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func adminModesJSON() map[string]any {
	database := currentMmogPlayerStateDB()
	set := storedFlags(database)
	out := map[string]any{
		"escalation_queue": adminSwitch{Value: matchmaker.EscalationQueue(), Env: matchmaker.EnvEscalationQueue(), Dashboard: set[flagEscalationQueue]},
		"havoc":            adminSwitch{Value: matchmaker.HavocListed(), Env: matchmaker.EnvHavocListed(), Dashboard: set[flagHavocListed]},
		"custom_game":      adminSwitch{Value: matchmaker.CustomGameListed(), Env: matchmaker.EnvCustomGameListed(), Dashboard: set[flagCustomGame]},
		"queueable":        queueableModes(),
		"max_players_max":  maxMatchPlayers,
		"max_wait_s_max":   int(maxMatchWait / time.Second),
	}
	queues := []map[string]any{}
	for _, mode := range matchmaker.GameModeConfigs() {
		queued := matchmaker.NormalizeGameMode(mode.Name)
		q := map[string]any{"queue": mode.Name, "mode": queued, "runs": matchmaker.RunsAs(queued), "client_team_size": mode.TeamSize}
		if mode.Name != matchmaker.CustomGameClientName { // the lobby, not a queue
			size, set := matchmaker.TeamSizeFor(queued)
			q["team_size"] = adminSwitch{Value: size, Env: matchmaker.DefaultTeamSize(queued), Dashboard: set}
			q["teams"] = matchmaker.TeamsIn(queued)
			if mm := activeMatchmaker; mm != nil {
				q["max_players"] = mm.MaxPlayersFor(queued)
			}
		}
		queues = append(queues, q)
	}
	out["queues"] = queues
	out["max_team_size"] = matchmaker.MaxTeamSize
	if mm := activeMatchmaker; mm != nil {
		players, wait := mm.Sizing()
		out["max_players"] = adminSwitch{Value: players, Env: envMatchSizing.players, Dashboard: set[flagMatchMaxPlayers]}
		out["max_wait_s"] = adminSwitch{Value: int(wait / time.Second), Env: int(envMatchSizing.wait / time.Second), Dashboard: set[flagMatchMaxWait]}
		out["autoscale"] = mm.AutoScaled()
		out["backfill_window_s"] = int(mm.BackfillWindow / time.Second)
	}
	return out
}

func adminAPIModes(w http.ResponseWriter, _ *http.Request) { writeAdminJSON(w, adminModesJSON()) }

// adminAPISetModes changes the switches named in the body. "reset" lists
// switches to hand back to the environment.
func adminAPISetModes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EscalationQueue *string `json:"escalation_queue"`
		Havoc           *bool   `json:"havoc"`
		CustomGame      *bool   `json:"custom_game"`
		MaxPlayers      *int    `json:"max_players"`
		MaxWaitS        *int    `json:"max_wait_s"`
		// TeamSizes sets max humans per team by queue mode; null = default.
		TeamSizes map[string]*int `json:"team_sizes"`
		Reset     []string        `json:"reset"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	mm := activeMatchmaker
	if (req.MaxPlayers != nil || req.MaxWaitS != nil) && mm == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "the matchmaker is not running")
		return
	}
	if req.EscalationQueue != nil {
		q := strings.TrimSpace(*req.EscalationQueue)
		valid := q == ""
		for _, m := range queueableModes() {
			if m == q {
				valid = true
			}
		}
		if !valid {
			writeAdminError(w, http.StatusBadRequest, "unknown queue "+q)
			return
		}
		req.EscalationQueue = &q
	}
	if req.MaxPlayers != nil && (*req.MaxPlayers < 1 || *req.MaxPlayers > maxMatchPlayers) {
		writeAdminError(w, http.StatusBadRequest, "players per match must be 1 to "+strconv.Itoa(maxMatchPlayers))
		return
	}
	if req.MaxWaitS != nil && (*req.MaxWaitS < 0 || time.Duration(*req.MaxWaitS)*time.Second > maxMatchWait) {
		writeAdminError(w, http.StatusBadRequest, "max wait must be 0 to "+strconv.Itoa(int(maxMatchWait/time.Second))+" seconds")
		return
	}
	queueModes := map[string]bool{}
	for _, mode := range matchmaker.GameModeConfigs() {
		queueModes[matchmaker.NormalizeGameMode(mode.Name)] = true
	}
	queueModes[matchmaker.HavocGameMode], queueModes[matchmaker.PvEGameMode] = true, true
	for mode, n := range req.TeamSizes {
		if !queueModes[mode] {
			writeAdminError(w, http.StatusBadRequest, "unknown queue "+mode)
			return
		}
		if n != nil && (*n < 1 || *n > matchmaker.MaxTeamSize) {
			writeAdminError(w, http.StatusBadRequest, "a team size must be 1 to "+strconv.Itoa(matchmaker.MaxTeamSize))
			return
		}
	}
	resettable := map[string]string{"escalation_queue": flagEscalationQueue, "havoc": flagHavocListed, "custom_game": flagCustomGame,
		"max_players": flagMatchMaxPlayers, "max_wait_s": flagMatchMaxWait}
	for _, k := range req.Reset {
		if resettable[k] == "" {
			writeAdminError(w, http.StatusBadRequest, "cannot reset "+k)
			return
		}
	}

	database := currentMmogPlayerStateDB()
	before := adminModesJSON()
	store := func(flag, value string) bool {
		if database == nil {
			return true
		}
		if _, err := database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES(?,?)`, flag, value); err != nil {
			writeAdminError(w, http.StatusInternalServerError, "could not save "+flag)
			return false
		}
		return true
	}
	changed := map[string]any{}
	if req.EscalationQueue != nil {
		if !store(flagEscalationQueue, *req.EscalationQueue) {
			return
		}
		matchmaker.SetEscalationQueueOverride(req.EscalationQueue)
		changed["escalation_queue"] = *req.EscalationQueue
	}
	for _, b := range []struct {
		name, flag string
		v          *bool
		set        func(*bool)
	}{{"havoc", flagHavocListed, req.Havoc, matchmaker.SetHavocListedOverride},
		{"custom_game", flagCustomGame, req.CustomGame, matchmaker.SetCustomGameListedOverride}} {
		if b.v == nil {
			continue
		}
		v := "0"
		if *b.v {
			v = "1"
		}
		if !store(b.flag, v) {
			return
		}
		b.set(b.v)
		changed[b.name] = *b.v
	}
	for mode, n := range req.TeamSizes {
		flag := "team_size:" + mode
		if n == nil {
			if database != nil {
				if _, err := database.Exec(`DELETE FROM server_flags WHERE name=?`, flag); err != nil {
					writeAdminError(w, http.StatusInternalServerError, "could not reset "+flag)
					return
				}
			}
			matchmaker.SetTeamSizeOverride(mode, nil)
			changed[flag] = "default"
			continue
		}
		if !store(flag, strconv.Itoa(*n)) {
			return
		}
		matchmaker.SetTeamSizeOverride(mode, n)
		changed[flag] = *n
	}
	if req.MaxPlayers != nil || req.MaxWaitS != nil {
		players, wait := -1, time.Duration(-1)
		if req.MaxPlayers != nil {
			if !store(flagMatchMaxPlayers, strconv.Itoa(*req.MaxPlayers)) {
				return
			}
			players = *req.MaxPlayers
			changed["max_players"] = players
		}
		if req.MaxWaitS != nil {
			if !store(flagMatchMaxWait, strconv.Itoa(*req.MaxWaitS)) {
				return
			}
			wait = time.Duration(*req.MaxWaitS) * time.Second
			changed["max_wait_s"] = *req.MaxWaitS
		}
		mm.SetSizing(players, wait)
	}
	for _, k := range req.Reset {
		if database != nil {
			if _, err := database.Exec(`DELETE FROM server_flags WHERE name=?`, resettable[k]); err != nil {
				writeAdminError(w, http.StatusInternalServerError, "could not reset "+k)
				return
			}
		}
		switch k {
		case "escalation_queue":
			matchmaker.SetEscalationQueueOverride(nil)
		case "havoc":
			matchmaker.SetHavocListedOverride(nil)
		case "custom_game":
			matchmaker.SetCustomGameListedOverride(nil)
		case "max_players":
			if mm != nil {
				mm.SetSizing(envMatchSizing.players, -1)
			}
		case "max_wait_s":
			if mm != nil {
				mm.SetSizing(0, envMatchSizing.wait)
			}
		}
		changed[k] = "environment default"
	}
	after := adminModesJSON()
	from := map[string]any{}
	for k := range changed {
		if s, ok := before[k].(adminSwitch); ok {
			from[k] = s.Value
		}
	}
	adminAudit(database, r, "game modes", "", map[string]any{"from": from, "to": changed})
	writeAdminJSON(w, after)
}
