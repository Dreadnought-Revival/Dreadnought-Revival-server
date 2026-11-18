package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// Server announcements in chat: how many players are online, and when a
// matchmaking queue starts, so players know when to queue to play together.
//
// They go to the GLOBAL channel, from a sender named "Server". A channel of
// their own is not possible with the stock client: UYMmogChat keeps ONE
// channel-name slot per room type (Global, language, match all/team, squad,
// custom room -- see chatJoinNotice) and creates only its Global and English
// rooms at startup, so a further "server" room has no tab to appear in, and
// joining one of an existing type would replace that type's channel.
//
// The sender is a fixed GUID that is no player's. The client names a chat
// line's sender from its user.profile cache (GetUsername) or asks user.whois;
// both are answered for this GUID with the name "Server" (serverChatProfile,
// and handleUserMethod's whois).
//
// DN_SERVER_CHAT=0 turns it off. DN_SERVER_CHAT_QUEUE_DELAY (default 8s)
// holds a "queue started" line back so a queue filled at once is not posted. DN_SERVER_CHAT_ONLINE_EVERY (default 10m)
// is the least time between two online counts; one is posted only when the
// count changed. DN_SERVER_CHAT_QUEUE_COOLDOWN (default 2m) is the least time
// between two "queue started" lines for the same mode and fleet.

const (
	serverChatGUID = "5e7e7e70-0000-4000-8000-000000000001"
	serverChatName = "Server"
)

func serverChatEnabled() bool { return os.Getenv("DN_SERVER_CHAT") != "0" }

func serverChatDuration(env string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(env)); err == nil && d >= 0 {
		return d
	}
	return def
}

// isServerChatID reports whether a GUID (any form) is the Server sender.
func isServerChatID(id string) bool {
	return protocol.NormalizePlayerPID(id) == protocol.NormalizePlayerPID(serverChatGUID)
}

// serverChatEntry is the Server sender as a presence/whois record.
func serverChatEntry() map[string]any {
	return map[string]any{
		"guid":              serverChatGUID,
		"pid":               serverChatGUID,
		"PID":               serverChatGUID,
		"peer_id":           "",
		"name":              serverChatName,
		"display_name":      serverChatName,
		"full_display_name": serverChatName,
		"number":            "0",
		"status":            "online",
		"message":           "",
		"online":            true,
	}
}

// serverChatProfile fills the client's name cache for the Server sender, the
// same push userProfileEvent sends for a player.
func serverChatProfile() map[string]any {
	return firmamentEvent("user.profile", map[string]any{
		"guid":              serverChatGUID,
		"profile":           serverChatGUID,
		"public_id":         serverChatGUID,
		"number":            "0",
		"display_name":      serverChatName,
		"full_display_name": serverChatName,
		"status_message":    "",
		"is_online":         true,
		"is_idle":           false,
		"is_away":           false,
		"is_private":        false,
		"is_admin":          true,
		"status":            1,
		"peer_id":           "",
	})
}

// sendServerProfile gives this connection the Server sender's user.profile,
// which fills the client's name cache so the Server's chat lines show their
// name.
//
// Every user.profile whose guid is not the receiving player's own makes the
// stock client log "LogYFirmament:Error: Failed to get current user profile"
// (YFirmamentClient.cpp:0x406). Verified in the binary, 2026-11-18: the
// "user.profile" callback (0x142AA1290) receives data.guid -- NOT data.profile,
// as the comment on selfUserProfileEvent used to claim -- and 0x142AA9B60 logs
// unless that guid equals the client's own id. The same guid is the key the
// dispatcher (0x142A8DFA0) files the record under, so a profile cannot be both
// cached for someone else and accepted by the callback; the error is the price
// of every name cached for another player, and the log line is harmless (the
// callback just returns).
//
// What can be avoided is repeating it. The cache lives as long as the
// connection, so the profile goes out once per connection, not before every
// automatic line (the online count alone is one every few minutes per player).
// force re-sends it -- the answer to a user.whois for the Server GUID, which is
// the client saying its cache has no entry. The flag is set only after the push
// was queued, so a dropped one is retried with the next line.
func (p *socialPeer) sendServerProfile(force bool) error {
	p.mu.Lock()
	sent := p.serverProfileSent
	p.mu.Unlock()
	if sent && !force {
		return nil
	}
	if err := p.send(serverChatProfile()); err != nil {
		return err
	}
	p.mu.Lock()
	p.serverProfileSent = true
	p.mu.Unlock()
	return nil
}

// announce posts one automatic Server line (online count, open queues) to
// everyone in the Global channel, unless DN_SERVER_CHAT=0. Each player's copy
// starts with the time in their own clock (chatTimeStamp, player_clock.go;
// operator 2026-11-07).
func (h *socialHub) announce(text string) {
	if !serverChatEnabled() {
		return
	}
	channel := defaultChatChannels[0]
	now := time.Now()
	sent := 0
	for _, peer := range h.peersIn(channel) {
		_ = peer.sendServerProfile(false)
		_ = peer.send(chatMessageNotice("chat.channel.message", channel, serverChatEntry(), chatTimeStamp(peer.playerID, now)+text))
		sent++
	}
	logrus.WithField("recipients", sent).Info("server chat: " + text)
}

// serverLine posts one Server line to everyone in the Global channel and
// returns how many it went to. The admin dashboard's messages use it
// directly: DN_SERVER_CHAT turns off only the automatic lines.
func (h *socialHub) serverLine(text string) int {
	channel := defaultChatChannels[0]
	msg := chatMessageNotice("chat.channel.message", channel, serverChatEntry(), text)
	sent := 0
	for _, peer := range h.peersIn(channel) {
		_ = peer.sendServerProfile(false)
		_ = peer.send(msg)
		sent++
	}
	logrus.WithField("recipients", sent).Info("server chat: " + text)
	return sent
}

// serverLineTo posts one Server line to one player only (the message of
// the day).
func serverLineTo(peer *socialPeer, text string) error {
	if err := peer.sendServerProfile(false); err != nil {
		return err
	}
	return peer.send(chatMessageNotice("chat.channel.message", defaultChatChannels[0], serverChatEntry(), text))
}

// --- online count ----------------------------------------------------------------

// serverChatOnlineLine is the online-count announcement.
func serverChatOnlineLine(online, inBattle, searching int, openQueues string) string {
	players := "players"
	if online == 1 {
		players = "player"
	}
	line := fmt.Sprintf("%d %s online -- %d in battle, %d searching for a match.", online, players, inBattle, searching)
	if openQueues != "" {
		line += " Open queues: " + openQueues + "."
	}
	return line
}

// serverChatCounts counts players online, in a running match, and queued.
func serverChatCounts() (online, inBattle, searching int) {
	ids := socialHubInstance.onlinePlayerIDs()
	online = len(ids)
	database := currentMmogPlayerStateDB()
	if database == nil {
		return online, 0, 0
	}
	_ = database.QueryRow(`SELECT COUNT(DISTINCT s.user_id) FROM match_slots s JOIN matches m ON m.id=s.match_id WHERE m.status='active'`).Scan(&inBattle)
	_ = database.QueryRow(`SELECT COUNT(DISTINCT user_id) FROM queue_entries WHERE status='waiting'`).Scan(&searching)
	return online, inBattle, searching
}

// startServerChatOnlineCount posts the online count every interval, when it
// changed since the last post.
func startServerChatOnlineCount() {
	every := serverChatDuration("DN_SERVER_CHAT_ONLINE_EVERY", 10*time.Minute)
	if !serverChatEnabled() || every == 0 {
		return
	}
	go func() {
		last := -1
		var lastAt time.Time
		for range time.Tick(time.Minute) {
			if time.Since(lastAt) < every {
				continue
			}
			online, inBattle, searching := serverChatCounts()
			if online == 0 || online == last {
				continue
			}
			socialHubInstance.announce(serverChatOnlineLine(online, inBattle, searching, serverChatOpenQueues()))
			last, lastAt = online, time.Now()
		}
	}()
}

// --- queue started ---------------------------------------------------------------

// serverChatModeNames are the player-facing names of the queueable modes.
var serverChatModeNames = map[string]string{
	"TDM": "Team Deathmatch",
	// Ceres Awakens: the client's own name for PodTDM (GlobalUI
	// UI_GameMode_CeresWakes; played on Space02 "Ryugu Haven", buff pods).
	"PodTDM":    "Ceres Awakens",
	"TurboTDM":  "Team Deathmatch",
	"TE":        "Team Elimination",
	"TER":       "Conquest",
	"Territory": "Conquest",
	// Onslaught is its own mode (host IVN: command ships, assault ships,
	// fighters -- "Kill the enemy command ship in Onslaught"); Havoc is the
	// separate PvE wave mode (YGameState_Havoc). Both names are the client's
	// own (SID_MATCHMAKINGTYPE_ONSLAUGHT / _HAVOC). It said "Havoc" until
	// 2026-10-07 (operator report).
	"Onslaught": "Onslaught",
	"Havoc":     "Havoc",
}

var (
	serverChatQueueMu   sync.Mutex
	serverChatQueueLast = map[string]time.Time{}
)

// serverChatQueueLine is the "queue started" announcement, or "" for a mode
// nobody else can join (solo tutorial modes).
func serverChatQueueLine(gameMode string, fleetType int32, partySize int) string {
	mode, ok := serverChatModeNames[gameMode]
	if !ok {
		return ""
	}
	fleet := fleetTypeName(fleetType)
	who := "A player is"
	if partySize > 1 {
		who = fmt.Sprintf("A squad of %d is", partySize)
	}
	return fmt.Sprintf("%s searching for a %s %s match. Queue with your %s fleet now to play together!",
		who, fleet, mode, fleet)
}

// announceQueueStarted posts a "queue started" line when a player or squad
// just queued into an EMPTY queue for its mode and fleet -- the moment others
// can join them. Called after the queue entries are written.
//
// Held back serverChatQueueDelay: a queue the matchmaker fills at once (no
// other idle player online, so the match starts against bots) is not
// announced, because nobody can join it any more. Of the first 44 lines
// posted, most were such queues, and chat lines cannot be deleted -- the
// client's chat protocol has no delete or edit (chat.channel.notice/info/
// message and chat.user.message only) -- so they buried the ones that were
// still open (operator, 2026-10-03).
func announceQueueStarted(gameMode string, fleetType int32, partySize int) {
	if !serverChatEnabled() {
		return
	}
	line := serverChatQueueLine(gameMode, fleetType, partySize)
	if line == "" {
		return
	}
	if serverChatQueueWaiting(gameMode, fleetType) > partySize {
		return // others were already waiting: the queue had started before
	}
	delay := serverChatDuration("DN_SERVER_CHAT_QUEUE_DELAY", 8*time.Second)
	go func() {
		time.Sleep(delay)
		if serverChatQueueWaiting(gameMode, fleetType) == 0 {
			return // the match started already: nothing to join
		}
		key := gameMode + "/" + strings.ToLower(fleetTypeName(fleetType))
		cooldown := serverChatDuration("DN_SERVER_CHAT_QUEUE_COOLDOWN", 2*time.Minute)
		serverChatQueueMu.Lock()
		if last, ok := serverChatQueueLast[key]; ok && time.Since(last) < cooldown {
			serverChatQueueMu.Unlock()
			return
		}
		serverChatQueueLast[key] = time.Now()
		serverChatQueueMu.Unlock()
		socialHubInstance.announce(line)
	}()
}

// serverChatQueueWaiting counts the players waiting in one queue.
func serverChatQueueWaiting(gameMode string, fleetType int32) int {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return 0
	}
	var waiting int
	_ = database.QueryRow(`SELECT COUNT(*) FROM queue_entries WHERE status='waiting' AND game_mode=? AND fleet_type=?`,
		gameMode, fleetType).Scan(&waiting)
	return waiting
}

// serverChatOpenQueues describes the queues players are waiting in, e.g.
// "Veteran Team Deathmatch (2 waiting)"; "" when there are none.
func serverChatOpenQueues() string {
	database := currentMmogPlayerStateDB()
	if database == nil {
		return ""
	}
	rows, err := database.Query(`SELECT game_mode, fleet_type, COUNT(*) FROM queue_entries WHERE status='waiting'
		GROUP BY game_mode, fleet_type ORDER BY fleet_type, game_mode`)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	var parts []string
	for rows.Next() {
		var mode string
		var fleet int32
		var n int
		if rows.Scan(&mode, &fleet, &n) != nil {
			continue
		}
		name, ok := serverChatModeNames[mode]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s (%d waiting)", fleetTypeName(fleet), name, n))
	}
	return strings.Join(parts, ", ")
}
