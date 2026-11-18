package matchmaker

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// CustomMatchPlayer is one player of a custom room match: their PID and the
// room team they are on (0 or 1, as the room sends it).
type CustomMatchPlayer struct {
	PID  string
	Team int
}

// CustomMatch is what a custom room starts (custom_rooms.go): the room's
// mode and map as the lobby names them, the fleet the host picked, the
// room's team size (1-5, the lobby's slider) and whether each team is filled
// with bots.
type CustomMatch struct {
	Mode      string
	Map       string
	FleetType int
	TeamSize  int
	TeamAI    [2]bool
	Players   []CustomMatchPlayer
}

// customMapFor finds a map by its lobby name among the maps the mode can run.
func customMapFor(mode, name string) (GameMap, bool) {
	var candidates []GameMap
	if mode == EscalationGameMode {
		candidates = mapsByGameMode["TDM"]
	} else if maps, ok := mapsByGameMode[mode]; ok {
		candidates = maps
	} else {
		candidates = append(append([]GameMap{}, availableMaps...), pveMaps...)
	}
	for _, m := range candidates {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return GameMap{}, false
}

// StartCustomMatch starts a battle server for a custom room and records the
// match and its players like a formed match, flagged custom. The players'
// connections then push YA_ServerStarting and YA_Connect as for any match
// (once their connection is armed, see squadHub.armForMatch). Returns the
// match id.
//
// Team size goes to the battle server as DNTeamSize (every side has that many
// ships) and the AI choice per team as team_ai ("10" = team 1 filled with
// bots, team 2 not; battle-server-mod DNTeamAI). PvP rooms keep the room's
// teams; co-op modes put everyone on team 1.
func (m *Matchmaker) StartCustomMatch(c CustomMatch) (string, error) {
	if len(c.Players) == 0 {
		return "", fmt.Errorf("no players")
	}
	launchMode := runnableGameMode(c.Mode)
	gameMode := launchMode
	if c.Mode == EscalationGameMode {
		gameMode, launchMode = "TDM", EscalationGameMode
	}
	chosen, ok := customMapFor(c.Mode, c.Map)
	if !ok {
		chosen, ok = customMapFor(gameMode, c.Map)
	}
	if !ok {
		return "", fmt.Errorf("map %q is not one %s can run", c.Map, c.Mode)
	}
	teamSize := c.TeamSize
	if teamSize < 1 {
		teamSize = 1
	}
	if teamSize > 5 {
		teamSize = 5
	}
	teams := TeamsIn(c.Mode)
	if c.Mode == EscalationGameMode {
		teams = 2
	}
	ai := ""
	for _, on := range c.TeamAI {
		if on {
			ai += "1"
		} else {
			ai += "0"
		}
	}
	fleetType := c.FleetType
	if fleetType < 1 || fleetType > 3 {
		fleetType = 1
	}
	pids := make([]string, len(c.Players))
	for i, p := range c.Players {
		pids[i] = p.PID
	}
	serverIP, serverPort, instanceID, battleMatchID, err := m.requestGameInstance(launchMode, chosen.Name, chosen.Path, pids,
		fleetTierURLValue(fleetType), teamSize, teamSize*teams, map[string]interface{}{"team_ai": ai})
	if err != nil {
		return "", fmt.Errorf("request game instance: %w", err)
	}
	serverIP = battleServerIPv4(serverIP, m.Log)

	matchID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := m.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("record match: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO matches(id,game_mode,map,server_ip,server_port,status,created_at,started_at,instance_id,battle_match_id,fleet_type,custom) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
		matchID, gameMode, chosen.Name, serverIP, serverPort, "active", now, now, instanceID, battleMatchID, fleetType,
	); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("insert match %s: %w", matchID, err)
	}
	for _, p := range c.Players {
		team := 1
		if teams == 2 && p.Team == 1 {
			team = 2
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO match_slots(match_id,user_id,team) VALUES(?,?,?)`, matchID, p.PID, team); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("insert match slot: %w", err)
		}
		// Not in a queue any more, whatever they were waiting for.
		if _, err := tx.Exec(`DELETE FROM queue_entries WHERE user_id=?`, p.PID); err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("leave queue: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("record match %s: %w", matchID, err)
	}
	m.Log.WithFields(logrus.Fields{
		"match_id": matchID, "instance_id": instanceID, "game_mode": gameMode, "launch_mode": launchMode,
		"map": chosen.Name, "fleet_type": fleetType, "team_size": teamSize, "team_ai": ai, "players": len(c.Players),
		"server": fmt.Sprintf("%s:%d", serverIP, serverPort),
	}).Info("custom match started")
	return matchID, nil
}
