package matchmaker

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

// Backfill: prefer a match with other players over a private one.
//
// Matches are auto-sized by who is idle (requiredMatchSize), so a player who
// queues while everyone else is in a battle starts a match alone, against
// bots -- and the next player to queue a minute later started ANOTHER one.
// Measured 2026-10-02: 77 of 124 matches in 24 h had one human.
//
// So before a new match is formed, waiting players join a match that is
// already running when one fits: same game mode and fleet type, its host has
// reported ready, it started less than BackfillWindow ago, it has at least one
// player in it and room for the whole party (PlayersPerMatch). The party goes
// on the side with fewer players (all on team 1 in co-op modes). The battle
// server takes late joiners: it is not told who may join, and the mod's bot
// balance gives a bot's place to each human who arrives.
//
// Which modes: only those in BackfillModes (main.go: TDM, Onslaught and BC by
// default). A player who joins a running match gets no orbit and no ship
// selection: the client reports ServerReadyForJoining with bFirstOrbitSpawn =
// 0 and the host spawns the active loadout at once. The host had none, so the
// first backfilled players (2026-10-03, 0 of 11) never got a ship. The
// battle-server mod's flagship drop-in (2026-11-08) fills that: all 10
// mid-match reconnects of 2026-11-11..15 spawned in their flagship, could
// switch ships at the next respawn and got a result (seen in TDM, Onslaught
// and BC). Not seen, so not listed: the round and wave modes (TE, Havoc) and
// Conquest.
//
// A match that has reported a result is over even while its host is still
// up (a player was sent into one two minutes after its end, 2026-11-13); it
// takes nobody.
//
// GUESS: the window. The original game's join-in-progress rules are not
// known; 3 minutes keeps a joiner well inside a ~10 minute match.

// backfillMatch is a running match a waiting party could join.
type backfillMatch struct {
	id    string
	teams map[int]int // players per team
	total int
}

// backfill places the waiting parties of one queue bucket into running
// matches. It returns how many players it placed.
func (m *Matchmaker) backfill(gameMode string, tierMin, fleetType int) (int, error) {
	runMode := runnableGameMode(gameMode)
	if m.BackfillWindow <= 0 || !m.BackfillModes[runMode] {
		return 0, nil
	}
	groups, err := m.waitingGroups(gameMode, tierMin, fleetType)
	if err != nil || len(groups) == 0 {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-m.BackfillWindow).Format(time.RFC3339)
	rows, err := m.DB.Query(`SELECT id, battle_match_id FROM matches
		WHERE status='active' AND custom=0 AND game_mode=? AND fleet_type=? AND server_ready_at IS NOT NULL
		  AND datetime(created_at) >= datetime(?)
		ORDER BY created_at DESC`, runMode, fleetType, cutoff)
	if err != nil {
		return 0, err
	}
	var ids, battleIDs []string
	for rows.Next() {
		var id, battleID string
		if rows.Scan(&id, &battleID) == nil {
			ids = append(ids, id)
			battleIDs = append(battleIDs, battleID)
		}
	}
	_ = rows.Close()
	// Results and team counts AFTER the rows are closed: one database
	// connection.
	var matches []*backfillMatch
	for i, id := range ids {
		if battleIDs[i] != "" {
			// The mod reports a later round on the same host as "<id>-r<n>".
			var results int
			if err := m.DB.QueryRow(`SELECT COUNT(*) FROM battle_results WHERE match_id=? OR match_id LIKE ?||'-r%'`,
				battleIDs[i], battleIDs[i]).Scan(&results); err != nil {
				return 0, err
			}
			if results > 0 {
				continue
			}
		}
		bm := &backfillMatch{id: id, teams: map[int]int{}}
		trows, err := m.DB.Query(`SELECT team, COUNT(*) FROM match_slots WHERE match_id=? GROUP BY team`, id)
		if err != nil {
			return 0, err
		}
		for trows.Next() {
			var team, n int
			if trows.Scan(&team, &n) == nil {
				bm.teams[team] = n
				bm.total += n
			}
		}
		_ = trows.Close()
		if bm.total > 0 { // nobody left in it: the host is about to be reaped
			matches = append(matches, bm)
		}
	}
	if len(matches) == 0 {
		return 0, nil
	}

	pvp := matchTeam(runMode, 1) == 2
	maxPlayers := m.MaxPlayersFor(gameMode) // per queue (switches.go)
	placed := 0
	for _, g := range groups {
		var target *backfillMatch
		for _, bm := range matches {
			if bm.total+len(g) <= maxPlayers {
				target = bm
				break
			}
		}
		if target == nil {
			continue
		}
		team := 1
		if pvp && target.teams[2] < target.teams[1] {
			team = 2
		}
		for _, e := range g {
			if _, err := m.DB.Exec(`INSERT OR REPLACE INTO match_slots(match_id,user_id,team) VALUES(?,?,?)`, target.id, e.UserID, team); err != nil {
				return placed, fmt.Errorf("backfill slot for %s: %w", e.UserID, err)
			}
			if _, err := m.DB.Exec(`DELETE FROM queue_entries WHERE id=?`, e.ID); err != nil {
				return placed, fmt.Errorf("delete backfilled queue entry %s: %w", e.ID, err)
			}
		}
		target.teams[team] += len(g)
		target.total += len(g)
		placed += len(g)
		m.Log.WithFields(logrus.Fields{
			"match_id":   target.id,
			"game_mode":  runMode,
			"fleet_type": fleetType,
			"players":    len(g),
			"team":       team,
			"in_match":   target.total,
		}).Info("matchmaker: backfilled a running match instead of starting a new one")
	}
	return placed, nil
}
