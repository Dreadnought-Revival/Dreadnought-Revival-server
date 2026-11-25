package main

import (
	"database/sql"
	"strings"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/sirupsen/logrus"
)

// How a player queues, picked with chat commands (2026-11-25; operator: "if
// someone types /pvp-only the game will queue to play against other players
// so no bots will join, /pvp is vs player with bots ... give the players the
// ability to pick how they want to queue").
//
//	/pvp-only  queue against players only: a separate pool, no bots (the
//	           matchmaker's pvp_only.go has the rules)
//	/pvp       queue as before: bots fill the teams (the default)
//	/queue     show the current choice
//	/help      list the commands
//
// The choice is stored on the account (player_queue_prefs) and written on
// every queue entry the player makes in a mode it applies to
// (matchmaker.PvPOnlyEligible); a squad queues with its LEADER's choice
// (operator's picks, 2026-11-25).
//
// The client sends a chat line that starts with "/" to the server like any
// other: /help, /gp and /global are in chat_messages (verified 2026-11-25);
// no /pvp line has been seen yet. A command line is answered with a Server
// line to the sender only, in Global, and is neither stored nor shown to
// anyone else. Any other "/" line is ordinary chat, as before.

// queuePrefKey is the preference table's player key: undashed, lower case
// (the matchmaker's normalizeUserID).
func queuePrefKey(pid string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pid), "-", ""))
}

// playerQueuePvPOnly is the player's stored choice (false when unset).
func playerQueuePvPOnly(database *sql.DB, pid string) bool {
	if database == nil {
		return false
	}
	var on int
	if err := database.QueryRow(`SELECT pvp_only FROM player_queue_prefs WHERE user_id=?`, queuePrefKey(pid)).Scan(&on); err != nil {
		return false
	}
	return on == 1
}

func setPlayerQueuePvPOnly(database *sql.DB, pid string, on bool) error {
	_, err := database.Exec(`INSERT INTO player_queue_prefs(user_id,pvp_only,updated_at) VALUES(?,?,datetime('now'))
		ON CONFLICT(user_id) DO UPDATE SET pvp_only=excluded.pvp_only, updated_at=excluded.updated_at`,
		queuePrefKey(pid), on)
	return err
}

// queueEntryPvPOnly is the pvp_only of a queue entry pid makes for gameMode:
// their choice, in a mode it applies to.
func queueEntryPvPOnly(database *sql.DB, pid, gameMode string) bool {
	return matchmaker.PvPOnlyEligible(gameMode) && playerQueuePvPOnly(database, pid)
}

// pvpOnlyModeList names the modes the choice applies to, for the replies.
const pvpOnlyModeList = "Team Deathmatch, Turbo TDM, Team Elimination, Conquest, Ceres Awakens and Onslaught"

// chatQueueCommand answers a queue command typed in chat. It returns false
// for a line that is not one (it is then ordinary chat).
func chatQueueCommand(peer *socialPeer, body string) bool {
	line := strings.TrimSpace(body)
	if !strings.HasPrefix(line, "/") {
		return false
	}
	cmd := strings.ToLower(strings.Fields(line)[0])
	switch cmd {
	case "/pvp-only", "/pvponly", "/pvp_only", "/pvp", "/queue", "/help":
	default:
		return false
	}
	database := currentMmogPlayerStateDB()
	reply := func(text string) {
		_ = peer.sendServerProfile(false)
		_ = peer.send(chatMessageNotice("chat.channel.message", defaultChatChannels[0], serverChatEntry(),
			chatTimeStamp(peer.playerID, time.Now())+text))
	}
	if database == nil {
		reply("Queue settings are not available right now.")
		return true
	}
	switch cmd {
	case "/help":
		reply("Commands: /pvp-only - queue against players only, no bots. /pvp - queue with bots filling the teams (default). /queue - show your choice. They apply to " + pvpOnlyModeList + ".")
		return true
	case "/queue":
		if playerQueuePvPOnly(database, peer.playerID) {
			reply("You queue PvP only: you are matched with other PvP-only players and no bots. Type /pvp to queue with bots.")
		} else {
			reply("You queue with bots: bots fill the teams when there are not enough players. Type /pvp-only to play against players only.")
		}
		return true
	}
	on := cmd != "/pvp"
	if err := setPlayerQueuePvPOnly(database, peer.playerID, on); err != nil {
		logrus.WithError(err).WithField("player", peer.playerID).Warn("queue prefs: save failed")
		reply("Your queue choice could not be saved. Try again in a moment.")
		return true
	}
	logrus.WithFields(logrus.Fields{"player": peer.playerID, "pvp_only": on}).Info("queue prefs: set by chat command")
	var text string
	if on {
		text = "PvP only is on: in " + pvpOnlyModeList + " you are matched only with other PvP-only players, with no bots. A match starts once there is a player for each team. Type /pvp to queue with bots again."
	} else {
		text = "PvP with bots is on: bots fill the teams when there are not enough players. Type /pvp-only to play against players only."
	}
	if leader, inSquad := squadHubInstance.leaderOf(peer.playerID); inSquad && leader != squadPID(peer.playerID) {
		text += " In a squad, the squad leader's choice decides how the squad queues."
	} else if moved := requeuePvPOnly(database, peer.playerID, on); moved {
		text += " Your current queue was switched too."
	}
	reply(text)
	return true
}

// requeuePvPOnly moves a player's waiting solo queue entry to the pool they
// just picked, when the choice applies to its mode. A squad entry keeps its
// leader's choice until the squad queues again.
func requeuePvPOnly(database *sql.DB, pid string, on bool) bool {
	var id, mode, party string
	var cur bool
	// user_id spellings differ between the mmog and social connections
	// (dashed or not), so it is compared normalised.
	if err := database.QueryRow(`SELECT id, game_mode, party_id, pvp_only FROM queue_entries
		WHERE lower(replace(user_id,'-',''))=? AND status='waiting'`,
		queuePrefKey(pid)).Scan(&id, &mode, &party, &cur); err != nil {
		return false
	}
	if party != "" || cur == on || !matchmaker.PvPOnlyEligible(mode) {
		return false
	}
	res, err := database.Exec(`UPDATE queue_entries SET pvp_only=? WHERE id=? AND status='waiting'`, on, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// leaderOf is the leader of pid's squad, and whether pid is in one.
func (h *squadHub) leaderOf(pid string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sq := h.squadOf(squadPID(pid))
	if sq == nil {
		return "", false
	}
	return sq.leader, true
}
