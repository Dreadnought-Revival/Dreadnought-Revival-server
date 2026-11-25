package matchmaker

// PvP-only pool (2026-11-25; operator: "if someone types /pvp-only the game
// will queue to play against other players so no bots will join, /pvp is vs
// player with bots ... give the players the ability to pick how they want to
// queue"). The player's choice is stored by mmogbrain (queue_prefs.go) and
// written on each queue entry as pvp_only.
//
// Rules (operator's choices, 2026-11-25):
//   - a separate pool: PvP-only entries are grouped, sized, backfilled and
//     hosted apart from everyone else, so a /pvp player never loses the bots
//     and a /pvp-only player never meets one;
//   - a PvP-only match needs humans on BOTH teams (at least 2 players; a
//     squad alone waits for an opponent), and never starts a lone player;
//   - its battle server gets team_ai "00" (dn-dedicated -> ?DNTeamAI=00), the
//     custom rooms' "no AI on either team": the mod keeps both sides at 0
//     bots. Verified on three custom-room hosts 2026-11-19/20 ("bot census:
//     ... alive T1 0 T2 0" for the whole match);
//   - every two-team queue whose teams are filled with bot captains: TDM,
//     Turbo TDM, Team Elimination, Conquest, Ceres Awakens and Onslaught.
//     In Onslaught only the bot CAPTAINS go (operator: "no ai assault/command
//     ships should stay"): DNTeamAI only sets the captain bot targets
//     (SetTeamSizeAI over m_npcPlayers, gm+0x968); the command ships, assault
//     ships and fighters are the mode's own spawns (GameInfo_Onslaught_BP
//     m_bigCreepClass ..., the mod's "AI targets" census), not that list.
//     The co-op queues (Proving Grounds, Training, PvE, Havoc) are bots by
//     design; there the choice is ignored and the player queues as before.

// pvpOnlyTeamAI is the custom-room AI choice meaning "no bots on either team".
const pvpOnlyTeamAI = "00"

// minPvPOnlyMatch is the fewest humans a PvP-only match starts with: one a side.
const minPvPOnlyMatch = 2

// PvPOnlyEligible reports whether the PvP-only choice applies to a queued mode.
func PvPOnlyEligible(queued string) bool {
	queued = NormalizeGameMode(queued)
	if TeamsIn(queued) != 2 || IsEscalationQueue(queued) {
		return false
	}
	return true
}

// pvpOnlyMatchSize adjusts the auto-scaled size for the PvP-only pool: never
// fewer than minPvPOnlyMatch (0, "not yet", stays 0).
func pvpOnlyMatchSize(size int) int {
	if size > 0 && size < minPvPOnlyMatch {
		return minPvPOnlyMatch
	}
	return size
}

// availablePvPOnlyPlayers counts who could still join a PvP-only match: the
// players waiting in that pool, plus online players whose stored choice is
// PvP only, minus anyone in an active match. Without the preferences table
// (tests) only the waiting players count.
func (m *Matchmaker) availablePvPOnlyPlayers() int {
	set := map[string]bool{}
	if rows, err := m.DB.Query(`SELECT user_id FROM queue_entries WHERE status='waiting' AND pvp_only=1`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && normalizeUserID(id) != "" {
				set[normalizeUserID(id)] = true
			}
		}
		_ = rows.Close()
	}
	if m.OnlinePlayers != nil {
		chose := map[string]bool{}
		if rows, err := m.DB.Query(`SELECT user_id FROM player_queue_prefs WHERE pvp_only=1`); err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					chose[normalizeUserID(id)] = true
				}
			}
			_ = rows.Close()
		}
		for _, id := range m.OnlinePlayers() {
			if id = normalizeUserID(id); chose[id] {
				set[id] = true
			}
		}
	}
	if rows, err := m.DB.Query(`SELECT s.user_id FROM match_slots s JOIN matches m ON m.id = s.match_id WHERE m.status='active'`); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				delete(set, normalizeUserID(id))
			}
		}
		_ = rows.Close()
	}
	return len(set)
}
