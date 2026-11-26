package main

// In-game staff commands (2026-11-25; staff.go has the roles). Typed in chat
// like the queue commands (queue_prefs.go): the client sends every "/" line
// to the server as an admin command, and the answer is a Server line to the
// sender only.
//
//	/who                          staff: who is online, and where
//	/warn <name> <text>           moderator, admin: a private Server line to the player
//	/mute <name> <hours> <reason> moderator, admin: 0 hours = until lifted
//	/unmute <name>                moderator, admin
//	/kick <name> [reason]         moderator, admin
//	/ban <name> <hours> <reason>  moderator (1..168 hours), admin (0 = permanent)
//	/unban <name>                 moderator (timed bans only), admin
//	/announce <text>              moderator, admin: a Server line to everyone
//
// A name with spaces may be quoted ("Some Name"); unquoted, the longest run
// of words that is a captain name is taken. Every action is written to the
// audit log under the staff member's name, like a dashboard action.

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// staffCommandRoles: which roles may run each staff command.
var staffCommandRoles = map[string][]string{
	"/who":      {staffRoleAdmin, staffRoleModerator, staffRoleBalancer},
	"/warn":     {staffRoleAdmin, staffRoleModerator},
	"/mute":     {staffRoleAdmin, staffRoleModerator},
	"/unmute":   {staffRoleAdmin, staffRoleModerator},
	"/kick":     {staffRoleAdmin, staffRoleModerator},
	"/ban":      {staffRoleAdmin, staffRoleModerator},
	"/unban":    {staffRoleAdmin, staffRoleModerator},
	"/announce": {staffRoleAdmin, staffRoleModerator},
}

var staffCommandUsage = map[string]string{
	"/who":      "/who",
	"/warn":     "/warn <name> <text>",
	"/mute":     "/mute <name> <hours> <reason>",
	"/unmute":   "/unmute <name>",
	"/kick":     "/kick <name> [reason]",
	"/ban":      "/ban <name> <hours> <reason>",
	"/unban":    "/unban <name>",
	"/announce": "/announce <text>",
}

func staffMayRun(role, cmd string) bool {
	for _, r := range staffCommandRoles[cmd] {
		if r == role {
			return true
		}
	}
	return false
}

// staffHelpLine lists the staff commands role may run ("" for none).
func staffHelpLine(role string) string {
	var cmds []string
	for _, c := range []string{"/who", "/warn", "/mute", "/unmute", "/kick", "/ban", "/unban", "/announce"} {
		if staffMayRun(role, c) {
			cmds = append(cmds, staffCommandUsage[c])
		}
	}
	if len(cmds) == 0 {
		return ""
	}
	return "Staff commands (" + staffRoleName(role) + "): " + strings.Join(cmds, ", ") + "."
}

// runStaffCommand runs one staff command for pid. It returns false when cmd
// is not a staff command (the caller handles it).
func runStaffCommand(database *sql.DB, pid, cmd, args string, reply func(string)) bool {
	if _, ok := staffCommandRoles[cmd]; !ok {
		return false
	}
	role := staffRoleOf(pid)
	if !staffMayRun(role, cmd) {
		if role == "" {
			reply("That command is for staff only.")
		} else {
			reply("Your role (" + staffRoleName(role) + ") cannot use " + cmd + ".")
		}
		return true
	}
	actor := adminPlayerName(pid) + " (" + staffRoleName(role) + ")"
	usage := func() { reply("Usage: " + staffCommandUsage[cmd]) }
	args = strings.TrimSpace(args)

	switch cmd {
	case "/who":
		reply(staffWhoLine(database))
		return true
	case "/announce":
		if args == "" {
			usage()
			return true
		}
		if len(args) > 300 {
			reply("An announcement is limited to 300 characters.")
			return true
		}
		n := socialHubInstance.serverLine(args)
		adminAuditActor(database, actor, "server message", "", map[string]any{"text": args, "recipients": n, "via": "chat"})
		reply(fmt.Sprintf("Announced to %d player(s).", n))
		return true
	}

	// Every other command names a player first.
	target, rest, err := staffTargetIn(database, args)
	if err != nil {
		if args == "" {
			usage()
		} else {
			reply(err.Error())
		}
		return true
	}
	name := adminPlayerName(target)
	if target == normalizedPlayerStatePID(pid) {
		reply("You cannot use " + cmd + " on yourself.")
		return true
	}
	if r := staffRoleOf(target); r == staffRoleAdmin && role != staffRoleAdmin {
		reply("You cannot use " + cmd + " on a global admin.")
		return true
	}

	switch cmd {
	case "/warn":
		if rest == "" {
			usage()
			return true
		}
		if !staffTellPlayer(target, "Warning from "+staffRoleName(role)+" "+adminPlayerName(pid)+": "+rest) {
			reply(name + " is not online.")
			return true
		}
		adminAuditActor(database, actor, "warn", target, map[string]any{"text": rest, "via": "chat"})
		reply("Warned " + name + ".")

	case "/mute", "/ban":
		hours, reason, ok := staffHoursReason(rest)
		if !ok {
			usage()
			return true
		}
		if cmd == "/ban" {
			if max := staffMaxBanHours(role); max > 0 && (hours == 0 || hours > max) {
				reply(fmt.Sprintf("Moderators can ban for 1 to %d hours (7 days); a permanent ban needs a global admin.", max))
				return true
			}
			details, err := adminBanPlayer(database, target, reason, hours)
			if err != nil {
				reply("The ban failed: " + err.Error())
				return true
			}
			details["via"] = "chat"
			adminAuditActor(database, actor, "ban", target, details)
			reply("Banned " + name + " " + staffForHours(hours) + ".")
			return true
		}
		expires, err := adminMutePlayer(database, target, reason, hours)
		if err != nil {
			reply("The mute failed: " + err.Error())
			return true
		}
		adminAuditActor(database, actor, "mute", target, map[string]any{"reason": reason, "hours": hours, "expires_at": expires, "via": "chat"})
		staffTellPlayer(target, "You were muted "+staffForHours(hours)+": "+reason)
		reply("Muted " + name + " " + staffForHours(hours) + ".")

	case "/unmute":
		if err := adminUnmutePlayer(database, target); err != nil {
			reply("The unmute failed: " + err.Error())
			return true
		}
		adminAuditActor(database, actor, "unmute", target, map[string]any{"via": "chat"})
		staffTellPlayer(target, "You can chat again.")
		reply("Unmuted " + name + ".")

	case "/kick":
		online := adminDisconnect(target)
		adminAuditActor(database, actor, "kick", target, map[string]any{"reason": rest, "was_online": online, "via": "chat"})
		if online {
			reply("Kicked " + name + ".")
		} else {
			reply(name + " was not online.")
		}

	case "/unban":
		if !adminPlayerBanned(target) {
			reply(name + " is not banned.")
			return true
		}
		if staffMaxBanHours(role) > 0 && adminBanPermanent(target) {
			reply("A permanent ban can be lifted only by a global admin.")
			return true
		}
		authErr, err := adminUnbanPlayer(database, target)
		if err != nil {
			reply("The unban failed: " + err.Error())
			return true
		}
		details := map[string]any{"auth": "ok", "via": "chat"}
		if authErr != nil {
			details["auth"] = authErr.Error()
		}
		adminAuditActor(database, actor, "unban", target, details)
		reply("Unbanned " + name + ".")
	}
	return true
}

// staffTargetIn takes the player named at the start of args and returns the
// rest. A quoted name is taken as written; otherwise the longest run of words
// that is a captain name.
func staffTargetIn(database *sql.DB, args string) (pid, rest string, err error) {
	if args == "" {
		return "", "", errors.New("name a player")
	}
	if args[0] == '"' {
		end := strings.IndexByte(args[1:], '"')
		if end < 0 {
			return "", "", errors.New("the name's closing quote is missing")
		}
		pid, err := staffFindPlayer(database, args[1:1+end])
		return pid, strings.TrimSpace(args[2+end:]), err
	}
	words := strings.Fields(args)
	max := len(words)
	if max > 5 {
		max = 5 // captain names are at most 20 characters
	}
	for n := max; n >= 1; n-- {
		name := strings.Join(words[:n], " ")
		pids, err := staffPlayersNamed(database, name)
		if err != nil {
			return "", "", err
		}
		switch len(pids) {
		case 0:
			continue
		case 1:
			return pids[0], strings.Join(words[n:], " "), nil
		default:
			return "", "", errors.New("several players are called " + name + "; use the dashboard")
		}
	}
	return "", "", errors.New("no player is called " + words[0])
}

// staffHoursReason splits "<hours> <reason>"; a reason is required.
func staffHoursReason(s string) (int, string, bool) {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0, "", false
	}
	hours, err := strconv.Atoi(f[0])
	if err != nil || hours < 0 || hours > adminMaxSanctionHrs {
		return 0, "", false
	}
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), f[0]))
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return hours, reason, true
}

func staffForHours(hours int) string {
	switch {
	case hours == 0:
		return "until lifted"
	case hours%24 == 0:
		return fmt.Sprintf("for %d day(s)", hours/24)
	}
	return fmt.Sprintf("for %d hour(s)", hours)
}

// staffTellPlayer sends one private Server line to a player; false when they
// have no chat connection.
func staffTellPlayer(pid, text string) bool {
	peer := socialHubInstance.peerFor(socialID(pid))
	if peer == nil {
		return false
	}
	serverReplyTo(peer, text)
	return true
}

// staffWhoLine: "N online: Name (in TDM), Name (waiting for TDM), Name".
func staffWhoLine(database *sql.DB) string {
	ids := socialHubInstance.onlinePlayerIDs()
	where := map[string]string{}
	if rows, err := database.Query(`SELECT s.user_id, m.game_mode FROM match_slots s JOIN matches m ON m.id=s.match_id WHERE m.status='active'`); err == nil {
		for rows.Next() {
			var id, mode string
			if rows.Scan(&id, &mode) == nil {
				where[normalizedPlayerStatePID(id)] = "in " + mode
			}
		}
		_ = rows.Close()
	}
	if rows, err := database.Query(`SELECT user_id, game_mode FROM queue_entries WHERE status='waiting'`); err == nil {
		for rows.Next() {
			var id, mode string
			if rows.Scan(&id, &mode) == nil {
				if _, ok := where[normalizedPlayerStatePID(id)]; !ok {
					where[normalizedPlayerStatePID(id)] = "waiting for " + mode
				}
			}
		}
		_ = rows.Close()
	}
	var parts []string
	for _, id := range ids {
		pid := normalizedPlayerStatePID(id)
		entry := adminPlayerName(pid)
		if w := where[pid]; w != "" {
			entry += " (" + w + ")"
		}
		parts = append(parts, entry)
	}
	sort.Strings(parts)
	more := ""
	if len(parts) > 40 {
		more = fmt.Sprintf(", and %d more", len(parts)-40)
		parts = parts[:40]
	}
	if len(parts) == 0 {
		return "Nobody is online."
	}
	return fmt.Sprintf("%d online: %s%s.", len(ids), strings.Join(parts, ", "), more)
}
