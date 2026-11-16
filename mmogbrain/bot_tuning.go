package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/sirupsen/logrus"
)

// Bot tuning per hosted game mode (operator, 2026-11-16: "make the ai smarter
// in battles ... per game mode").
//
// The game has no bot skill level: Recruit / Veteran / Legendary only pick a
// bot's ship tier (GameInfo_*_BP m_aiSpawnTierRules), and Feats_AI_difficulty
// is a placeholder no code reads. What a bot does is the numbers in its
// controller Blueprint (YNPCAIController): how far off it aims, how long it
// takes to react, how readily it fires an ability. These three settings scale
// those numbers on the battle server, in percent of the game's own value:
//
//	aim        the aim error: m_shootBaseOffset + distance * m_shootDistOffset
//	           (read at exe 0x2B23B0), and the same pair for the secondary
//	           weapon. 50 = half the miss radius.
//	reaction   m_perceptReactTimeBase/Rand and m_reacquireTargetBaseTime/
//	           RandomTime. 50 = reacts and picks a new target twice as fast.
//	           GUESS: what the four values do is read from their names.
//	abilities  m_chancePerCheck of each ability rule (m_fireAbilities), capped
//	           at 100. GUESS: a percent chance per check, read from the name
//	           and the cooked values (5 to 20).
//
// 100/100/100 is the original game and is what a mode has until the operator
// changes it. The values are NOT original data; they are an operator setting,
// stored in server_flags as bot_tune:<mode> = "aim,reaction,abilities". The
// battle-server mod asks for its mode's values once per match (GET
// /battle/bots?mode=<its -GameMode=>, loopback only).

type botTuning struct {
	Aim       int `json:"aim"`
	Reaction  int `json:"reaction"`
	Abilities int `json:"abilities"`
}

var originalBotTuning = botTuning{Aim: 100, Reaction: 100, Abilities: 100}

const (
	botTuneFlagPrefix = "bot_tune:"

	botAimMin, botAimMax             = 0, 300
	botReactionMin, botReactionMax   = 10, 300
	botAbilitiesMin, botAbilitiesMax = 10, 500
)

func (b botTuning) valid() bool {
	return b.Aim >= botAimMin && b.Aim <= botAimMax &&
		b.Reaction >= botReactionMin && b.Reaction <= botReactionMax &&
		b.Abilities >= botAbilitiesMin && b.Abilities <= botAbilitiesMax
}

func (b botTuning) String() string { return fmt.Sprintf("%d,%d,%d", b.Aim, b.Reaction, b.Abilities) }

func parseBotTuning(s string) (botTuning, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return botTuning{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return botTuning{}, false
		}
		n[i] = v
	}
	b := botTuning{Aim: n[0], Reaction: n[1], Abilities: n[2]}
	return b, b.valid()
}

// botTuningModes are the hosted modes with bots a queue can lead to: what the
// battle server is started with (-GameMode=), so the key the mod asks by.
func botTuningModes() map[string]bool {
	out := map[string]bool{}
	for _, mode := range matchmaker.GameModeConfigs() {
		if mode.Name == matchmaker.CustomGameClientName {
			continue
		}
		queued := matchmaker.NormalizeGameMode(mode.Name)
		if queued == matchmaker.PvEGameMode {
			continue // an episode is hosted as Horde or Escort, never as "PVE"
		}
		out[matchmaker.RunsAs(queued)] = true
	}
	return out
}

// storedBotTuning is every mode's stored setting (one query, rows closed
// before returning).
func storedBotTuning(database *sql.DB) map[string]botTuning {
	out := map[string]botTuning{}
	if database == nil {
		return out
	}
	rows, err := database.Query(`SELECT name, value FROM server_flags WHERE name LIKE 'bot\_tune:%' ESCAPE '\'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		if b, ok := parseBotTuning(v); ok {
			out[strings.TrimPrefix(k, botTuneFlagPrefix)] = b
		}
	}
	return out
}

// botTuningFor is the setting of one hosted mode and whether it was set on
// the dashboard.
func botTuningFor(database *sql.DB, mode string) (botTuning, bool) {
	if database == nil || mode == "" {
		return originalBotTuning, false
	}
	var v string
	if err := database.QueryRow(`SELECT value FROM server_flags WHERE name=?`, botTuneFlagPrefix+mode).Scan(&v); err != nil {
		return originalBotTuning, false
	}
	if b, ok := parseBotTuning(v); ok {
		return b, true
	}
	return originalBotTuning, false
}

// saveBotTuning stores a mode's setting; nil or the original values remove it.
func saveBotTuning(database *sql.DB, mode string, b *botTuning) error {
	if database == nil {
		return nil
	}
	if b == nil || *b == originalBotTuning {
		_, err := database.Exec(`DELETE FROM server_flags WHERE name=?`, botTuneFlagPrefix+mode)
		return err
	}
	_, err := database.Exec(`INSERT OR REPLACE INTO server_flags(name,value) VALUES(?,?)`, botTuneFlagPrefix+mode, b.String())
	return err
}

// battleBotsHandler answers the battle-server mod: "aim reaction abilities",
// in percent, for the mode the host runs. An unknown mode gets the original
// values.
func battleBotsHandler(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessLoopback(w, r) {
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	b, set := botTuningFor(currentMmogPlayerStateDB(), mode)
	logrus.WithFields(logrus.Fields{"mode": mode, "aim": b.Aim, "reaction": b.Reaction, "abilities": b.Abilities, "dashboard": set}).
		Info("battle bots: tuning served")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "%d %d %d\n", b.Aim, b.Reaction, b.Abilities)
}
