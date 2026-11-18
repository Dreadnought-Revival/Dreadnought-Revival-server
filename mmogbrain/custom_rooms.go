package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Custom rooms (2026-11-04, exploration). With DN_CUSTOM_GAME_TILE=1 the
// client's Custom Game tile is listed (matchmaker.customGameListed); it opens
// the custom match lobby (UI_Screen_CustomMatchLobby). The first try got the
// generic room success and the client logged "YA_CustomRoomCreateResponse -
// Creating custom room!" then "Could not find the custom room user data for
// the local user": an empty room, empty mode and map lists.
//
// What the client reads, verified in the exe:
//
//	dispatcher  YA_CustomRoomCreateResponse (0x142A2AF87..): the ROOT node
//	            "roomInfo" -> parser 0x142A69BB0 into the mmog room record
//	            (+0x3A68, 0xC8 bytes), in-room flag +0x3B08 set; then the
//	            local player is the host (room hostPid == own PID, mmog
//	            +0x3898) or must be found among the room's users (vtable
//	            +0x90), else the error above.
//	roomInfo    roomId, hostPid (strings, 0x142A1D450), teams (at most 2
//	            arrays of users; more fails the parse), gameMode, mapName,
//	            settings (array of {id 0-3, value string}; id 3 is parsed as
//	            a JSON object, 0x142A6AEE0), supportedModes (array of
//	            {gameMode, supportedMaps: [map names]} -> the mode/map
//	            choices), chatRoomId.
//	user        0x142A6B0D0: userPid, DisplayName, team (0 or 1), slot,
//	            info {UnlockedFleetType 0-3, Rank, DisplayInfo}, state (0 or
//	            1), validFleets (each 1-3); out of range fails the user.
//
// Numbers in a user go as numeric strings (the client's scalar reader
// converts them). Every room request is still logged with its readable text.
// Rooms live in memory (lost on a restart); a player is in at most one room
// and leaves it when their last connection closes. The lobby flow and the
// multi-player messages below were built 2026-11-18; the user "state" field's
// meaning is still a GUESS (always 0).

// The lobby flow, verified in the exe 2026-11-18 (dispatcher arms in
// 0x2A236C2-0x2A31A32, request builders in 0x2A19540 / 0x2A4A9D0 /
// 0x2A4AB70, countdown tick 0x324450):
//
//	settings      EYCustomMatchSettingID: 0 TeamSize (lobby slider 1-5),
//	              1 Team1FillWithAI, 2 Team2FillWithAI (No/Yes), 3 Metadata
//	              (YCustomMatchSettingsDataAsset). Sent as {ID, Value} with
//	              Value a float string ("8.000000").
//	fleet select  host: YA_CustomRoomEnterFleetSelect{RoomID} -> reply ROOT
//	              result (int, 0 = EYCustomMatchError::None) and bestFleet
//	              (int); success fires mmog+0x2190.
//	countdown     host: YA_CustomRoomStartMatchCountdown{RoomID, FleetType}
//	              -> reply ROOT result, countdownSeconds (int),
//	              countdownEndTimestamp (Unix seconds, 0x320C50 adds them to
//	              1970-01-01), fleet (int); fires mmog+0x20B0.
//	start         when the countdown reaches 0 the host's client itself sends
//	              YA_CustomRoomStartMatch{RoomID} (0x324450 -> mmog vtable
//	              +0x88); reply ROOT result. The match then reaches the players
//	              the normal way: YA_ServerStarting + YA_Connect.
//
// Every room reply's ROOT "result" is read as an int and must be 0..29
// (0x2A28C8F, field-name global 0x40E9648 = "result"); the older replies
// here send an object there, which reads as 0.

// customRoomCountdown is how long the lobby counts down before the match.
// GUESS: the original's value is not known.
const customRoomCountdown = 10

// customRoomErrStartingGame is EYCustomMatchError::StartingGame. GUESS: the
// enum's names lie in .rdata in this order and StartingGame is the 22nd
// (index 21); its numeric value was not read from the enum table.
const customRoomErrStartingGame = 21

const (
	customRoomPreparation = iota
	customRoomFleetSelection
	customRoomCountdownState
	customRoomMatchStarting
)

// Error codes: EYCustomMatchError. GUESS: the values follow the order its
// names lie in .rdata (None 0, CreatingRoom 1, EnterFleetSelect 2,
// ChangeSettings 3, JoinRoomRoomFull 4, ... TeamSwitchTeamFull 12, ...,
// ChangingHost 19, Rejoin 20, StartingGame 21); the enum table itself was not
// read. Any non-zero result fails the request on the client.
const (
	customRoomErrJoinRoomFull     = 4
	customRoomErrJoinUnknown      = 7
	customRoomErrTeamSwitchFull   = 12
	customRoomErrTeamSwitchFailed = 14
	customRoomErrChangingHost     = 19
)

// More than one player, verified in the exe 2026-11-18 (request builders and
// dispatcher arms):
//
//	invite        YA_CustomRoomInvite{InviteeId, RoomId}; the invitee gets
//	              YA_CustomRoomInvitePush: ROOT result, userInfo (the inviter),
//	              roomInfo (0x2A28A64 -> 0x2A39270, the invite list).
//	join          YA_CustomRoomUserJoin{RoomID} -> YA_CustomRoomUserJoinResponse:
//	              ROOT result, userInfo (the user who joined, 0x2A6B0D0) and
//	              roomInfo (the room, 0x2A69BB0). The room's other members get
//	              the same message.
//	leave / kick  YA_CustomRoomUserLeave{RoomID, Team, Slot}, host:
//	              YA_CustomRoomUserRemove{RoomID, UserID, Team, Slot} ->
//	              YA_CustomRoomUserLeaveResponse: ROOT result, userPid, team,
//	              slot ("has left" / "has been removed").
//	team switch   YA_CustomRoomUserSwitchTeam{RoomId, UserID, SourceTeam,
//	              SourceSlot, DesiredTeam[, UserToSwap]} ->
//	              YA_CustomRoomUserSwitchTeamResponse: ROOT result, users
//	              [{userPid, team, slot}] (every user that moved).
//	host          YA_CustomRoomChangeHost{RoomID, NewHostID} ->
//	              YA_CustomRoomChangeHostResponse: ROOT result, newHostPid.
//
// Ids travel as 16-byte GUIDs; protocol.Scalars reads them as the 32 hex
// digits the server uses for player ids.

type customRoomUser struct {
	pid  string
	team int
	slot int
}

type customRoom struct {
	id, host      string
	mode, mapName string
	settings      map[int]string // EYCustomMatchSettingID -> value as sent
	users         []customRoomUser
	state         int
	fleet         int
	matchID       string
}

var customRooms = struct {
	sync.Mutex
	byPlayer map[string]*customRoom
	byID     map[string]*customRoom
}{byPlayer: map[string]*customRoom{}, byID: map[string]*customRoom{}}

// customRoomResult starts a room reply with its ROOT "result" as an int.
func customRoomResult(responseName string, code int32) []byte {
	b := protocol.AppendStringField(nil, "RT", responseName)
	return protocol.AppendInt32Field(b, "result", code)
}

// customRoomIDField is the first field of that name as a player or room id
// (32 lowercase hex digits), or "".
func customRoomIDField(payload []byte, names ...string) string {
	for _, s := range protocol.Scalars(payload) {
		for _, n := range names {
			if s.Name == n && s.IsStr {
				return protocol.NormalizePlayerPID(s.Str)
			}
		}
	}
	return ""
}

// customRoomIntField is the first int field of that name, or def.
func customRoomIntField(payload []byte, def int, names ...string) int {
	for _, s := range protocol.Scalars(payload) {
		for _, n := range names {
			if s.Name == n {
				if s.IsStr {
					if v, err := strconv.Atoi(s.Str); err == nil {
						return v
					}
					continue
				}
				return int(s.Num)
			}
		}
	}
	return def
}

// teams is how many teams the room's mode has; teamSize how many players a
// team takes (the lobby's slider, 1-5).
func (r *customRoom) teams() int {
	if r.mode == matchmaker.EscalationGameMode {
		return 2
	}
	return matchmaker.TeamsIn(r.mode)
}

func (r *customRoom) teamSize() int {
	n := customRoomSettingInt(r, 0, 5)
	if n < 1 {
		n = 1
	}
	if n > 5 {
		n = 5
	}
	return n
}

func (r *customRoom) user(pid string) (int, bool) {
	for i, u := range r.users {
		if u.pid == pid {
			return i, true
		}
	}
	return -1, false
}

// freeSlot is the lowest free slot of a team, or -1 when it is full.
func (r *customRoom) freeSlot(team int) int {
	taken := map[int]bool{}
	for _, u := range r.users {
		if u.team == team {
			taken[u.slot] = true
		}
	}
	for s := 0; s < r.teamSize(); s++ {
		if !taken[s] {
			return s
		}
	}
	return -1
}

// addUser puts a player on the team with fewer players (team 0 in co-op).
func (r *customRoom) addUser(pid string) (customRoomUser, bool) {
	count := [2]int{}
	for _, u := range r.users {
		if u.team == 0 || u.team == 1 {
			count[u.team]++
		}
	}
	order := []int{0}
	if r.teams() == 2 {
		order = []int{0, 1}
		if count[1] < count[0] {
			order = []int{1, 0}
		}
	}
	for _, team := range order {
		if slot := r.freeSlot(team); slot >= 0 {
			u := customRoomUser{pid: pid, team: team, slot: slot}
			r.users = append(r.users, u)
			return u, true
		}
	}
	return customRoomUser{}, false
}

// others is every member but pid.
func (r *customRoom) others(pid string) []string {
	var out []string
	for _, u := range r.users {
		if u.pid != pid {
			out = append(out, u.pid)
		}
	}
	return out
}

// pushCustomRoom sends a room message to members' connections (squads.go
// pushes: each connection writes them as unsolicited frames).
func pushCustomRoom(pids []string, payload []byte) {
	for _, pid := range pids {
		squadHubInstance.push(pid, payload)
	}
}

// removeCustomRoomUserLocked takes a player out of their room: the others
// are told (YA_CustomRoomUserLeaveResponse), a new host is named when the
// host left, and an empty room is dropped. kicked also tells the player who
// was removed. Callers hold customRooms.
func removeCustomRoomUserLocked(pid string, kicked bool) {
	room := customRooms.byPlayer[pid]
	if room == nil {
		return
	}
	delete(customRooms.byPlayer, pid)
	i, ok := room.user(pid)
	if !ok {
		return
	}
	u := room.users[i]
	room.users = append(room.users[:i], room.users[i+1:]...)
	notify := room.others(pid)
	if kicked {
		notify = append(notify, pid)
	}
	pushCustomRoom(notify, buildCustomRoomLeavePayload(u))
	logrus.WithFields(logrus.Fields{"room": room.id, "pid": pid, "kicked": kicked, "left": len(room.users)}).Info("custom room: user left")
	if len(room.users) == 0 {
		delete(customRooms.byID, room.id)
		return
	}
	if room.host == pid {
		room.host = room.users[0].pid
		pushCustomRoom(room.others(""), buildCustomRoomHostPayload(room.host))
	}
}

// customRoomDisconnected takes a player who went offline out of their room.
func customRoomDisconnected(pid string) {
	customRooms.Lock()
	defer customRooms.Unlock()
	removeCustomRoomUserLocked(normalizedPlayerStatePID(pid), false)
}

// handleCustomRoomRequest answers the room requests it implements; ok false
// leaves the rest to the generic reply.
func handleCustomRoomRequest(requestName, playerPID string, payload []byte) ([]byte, bool) {
	pid := normalizedPlayerStatePID(playerPID)
	if requestName == "YA_CustomRoomStartMatch" {
		return startCustomRoomMatch(pid), true
	}
	customRooms.Lock()
	defer customRooms.Unlock()
	room := customRooms.byPlayer[pid]
	responseName := mmogRoomResponseName(requestName)
	switch requestName {
	case "YA_CustomRoomCreate":
		if room != nil {
			removeCustomRoomUserLocked(pid, false)
		}
		room = &customRoom{
			id:       strings.ReplaceAll(uuid.New().String(), "-", ""),
			host:     pid,
			mode:     customRoomMode(protocol.ExtractStringField(payload, "GameMode")),
			settings: protocol.IDValuePairs(payload, "ID", "Value"),
			users:    []customRoomUser{{pid: pid}},
		}
		room.mapName = customRoomMap(room.mode, protocol.ExtractStringField(payload, "MapName"))
		customRooms.byPlayer[pid] = room
		customRooms.byID[room.id] = room
		logrus.WithFields(logrus.Fields{"room": room.id, "pid": pid, "mode": room.mode, "map": room.mapName,
			"settings": room.settings}).Info("custom room: created")
		return buildCustomRoomPayload(responseName, room), true
	case "YA_CustomRoomInvite":
		invitee := customRoomIDField(payload, "InviteeId")
		if room == nil || invitee == "" || invitee == pid {
			return nil, false
		}
		pushCustomRoom([]string{invitee}, buildCustomRoomInvitePayload(room, pid))
		logrus.WithFields(logrus.Fields{"room": room.id, "from": pid, "to": invitee}).Info("custom room: invite")
		return nil, false
	case "YA_CustomRoomUserJoin":
		target := customRooms.byID[customRoomIDField(payload, "RoomID", "RoomId")]
		if target == nil {
			return customRoomResult(responseName, customRoomErrJoinUnknown), true
		}
		if room == target {
			if i, ok := target.user(pid); ok {
				return buildCustomRoomJoinPayload(target, target.users[i]), true
			}
		}
		if target.state != customRoomPreparation {
			return customRoomResult(responseName, customRoomErrJoinUnknown), true
		}
		if room != nil {
			removeCustomRoomUserLocked(pid, false)
		}
		u, ok := target.addUser(pid)
		if !ok {
			return customRoomResult(responseName, customRoomErrJoinRoomFull), true
		}
		customRooms.byPlayer[pid] = target
		reply := buildCustomRoomJoinPayload(target, u)
		pushCustomRoom(target.others(pid), reply)
		logrus.WithFields(logrus.Fields{"room": target.id, "pid": pid, "team": u.team, "slot": u.slot}).Info("custom room: user joined")
		return reply, true
	case "YA_CustomRoomUserReturn":
		// Back from the room's match: the room is as it was, in preparation.
		if room == nil {
			return nil, false
		}
		room.state, room.matchID = customRoomPreparation, ""
		return buildCustomRoomPayload(responseName, room), true
	case "YA_CustomRoomChangeSettings", "YA_CustomRoomUpdate":
		if room == nil {
			return nil, false
		}
		if mode := protocol.ExtractStringField(payload, "GameMode"); mode != "" {
			room.mode = customRoomMode(mode)
		}
		if m := protocol.ExtractStringField(payload, "MapName"); m != "" || room.mapName == "" {
			room.mapName = customRoomMap(room.mode, m)
		}
		for id, v := range protocol.IDValuePairs(payload, "ID", "Value") {
			if room.settings == nil {
				room.settings = map[int]string{}
			}
			room.settings[id] = v
		}
		logrus.WithFields(logrus.Fields{"room": room.id, "mode": room.mode, "map": room.mapName,
			"settings": room.settings}).Info("custom room: settings")
		if requestName == "YA_CustomRoomChangeSettings" {
			reply := buildCustomRoomSettingsPayload(responseName, room)
			pushCustomRoom(room.others(pid), reply)
			return reply, true
		}
		return buildCustomRoomPayload(responseName, room), true
	case "YA_CustomRoomUserSwitchTeam":
		if room == nil {
			return customRoomResult(responseName, customRoomErrTeamSwitchFailed), true
		}
		mover := customRoomIDField(payload, "UserID")
		if mover == "" {
			mover = pid
		}
		if mover != pid && room.host != pid {
			return customRoomResult(responseName, customRoomErrTeamSwitchFailed), true
		}
		i, ok := room.user(mover)
		if !ok {
			return customRoomResult(responseName, customRoomErrTeamSwitchFailed), true
		}
		var moved []customRoomUser
		if other := customRoomIDField(payload, "UserToSwap"); other != "" && other != mover {
			j, ok := room.user(other)
			if !ok {
				return customRoomResult(responseName, customRoomErrTeamSwitchFailed), true
			}
			room.users[i].team, room.users[j].team = room.users[j].team, room.users[i].team
			room.users[i].slot, room.users[j].slot = room.users[j].slot, room.users[i].slot
			moved = []customRoomUser{room.users[i], room.users[j]}
		} else {
			desired := customRoomIntField(payload, 1-room.users[i].team, "DesiredTeam")
			if desired < 0 || desired >= room.teams() || desired == room.users[i].team {
				return customRoomResult(responseName, customRoomErrTeamSwitchFailed), true
			}
			slot := room.freeSlot(desired)
			if slot < 0 {
				return customRoomResult(responseName, customRoomErrTeamSwitchFull), true
			}
			room.users[i].team, room.users[i].slot = desired, slot
			moved = []customRoomUser{room.users[i]}
		}
		reply := buildCustomRoomSwitchPayload(moved)
		pushCustomRoom(room.others(pid), reply)
		return reply, true
	case "YA_CustomRoomChangeHost":
		newHost := customRoomIDField(payload, "NewHostID")
		if room == nil || room.host != pid || newHost == "" {
			return customRoomResult(responseName, customRoomErrChangingHost), true
		}
		if _, ok := room.user(newHost); !ok {
			return customRoomResult(responseName, customRoomErrChangingHost), true
		}
		room.host = newHost
		reply := buildCustomRoomHostPayload(newHost)
		pushCustomRoom(room.others(pid), reply)
		return reply, true
	case "YA_CustomRoomUserRemove":
		target := customRoomIDField(payload, "UserID")
		if room != nil && room.host == pid && target != "" && target != pid {
			if _, ok := room.user(target); ok {
				removeCustomRoomUserLocked(target, true)
			}
		}
		return nil, false
	case "YA_CustomRoomEnterFleetSelect":
		if room == nil || room.host != pid {
			return nil, false
		}
		room.state = customRoomFleetSelection
		for _, other := range room.others(pid) {
			pushCustomRoom([]string{other}, buildCustomRoomFleetSelectPayload(responseName, other))
		}
		return buildCustomRoomFleetSelectPayload(responseName, pid), true
	case "YA_CustomRoomExitFleetSelect":
		if room == nil {
			return nil, false
		}
		room.state = customRoomPreparation
		reply := customRoomResult(responseName, 0)
		if room.host == pid {
			pushCustomRoom(room.others(pid), reply)
		}
		return reply, true
	case "YA_CustomRoomStartMatchCountdown":
		if room == nil || room.host != pid {
			return nil, false
		}
		// The room plays the fleet the host picked, at most the lowest best
		// fleet among its members (everyone must have one of that type).
		best := 3
		for _, u := range room.users {
			if f := customRoomFleetType(mmogPlayerStateForPID(u.pid), u.pid); f < best {
				best = f
			}
		}
		fleet := customRoomIntField(payload, best, "FleetType")
		if fleet < 1 || fleet > best {
			fleet = best
		}
		room.state, room.fleet = customRoomCountdownState, fleet
		logrus.WithFields(logrus.Fields{"room": room.id, "fleet": fleet}).Info("custom room: countdown")
		b := customRoomResult(responseName, 0)
		b = protocol.AppendInt32Field(b, "countdownSeconds", customRoomCountdown)
		b = protocol.AppendStringField(b, "countdownEndTimestamp", strconv.FormatInt(time.Now().Unix()+customRoomCountdown, 10))
		b = protocol.AppendInt32Field(b, "fleet", int32(fleet))
		pushCustomRoom(room.others(pid), b)
		return b, true
	case "YA_CustomRoomCancelMatchCountdown":
		if room == nil {
			return nil, false
		}
		room.state = customRoomFleetSelection
		reply := customRoomResult(responseName, 0)
		pushCustomRoom(room.others(pid), reply)
		return reply, true
	case "YA_CustomRoomUserLeave":
		removeCustomRoomUserLocked(pid, false)
	}
	return nil, false
}

// startCustomRoomMatch is the host's YA_CustomRoomStartMatch: it asks the
// matchmaker for the room's battle server (outside the rooms lock: a spawn
// takes seconds) and answers only once that worked, so a failure reaches the
// lobby as an error instead of a wait for a server that never comes. The
// room's players are then armed, so their connections push the match, and
// the other members get the same success.
func startCustomRoomMatch(pid string) []byte {
	responseName := mmogRoomResponseName("YA_CustomRoomStartMatch")
	customRooms.Lock()
	room := customRooms.byPlayer[pid]
	if room == nil || room.host != pid {
		customRooms.Unlock()
		return customRoomResult(responseName, customRoomErrStartingGame)
	}
	if room.state == customRoomMatchStarting && room.matchID != "" {
		customRooms.Unlock()
		return customRoomResult(responseName, 0) // a repeat: already started
	}
	room.state = customRoomMatchStarting
	match := matchmaker.CustomMatch{Mode: room.mode, Map: room.mapName, FleetType: room.fleet,
		TeamSize: room.teamSize(), TeamAI: [2]bool{customRoomSettingInt(room, 1, 0) != 0, customRoomSettingInt(room, 2, 0) != 0}}
	for _, u := range room.users {
		match.Players = append(match.Players, matchmaker.CustomMatchPlayer{PID: u.pid, Team: u.team})
	}
	others := room.others(pid)
	customRooms.Unlock()

	mm := activeMatchmaker
	if mm == nil {
		return customRoomResult(responseName, customRoomErrStartingGame)
	}
	matchID, err := mm.StartCustomMatch(match)
	customRooms.Lock()
	if room := customRooms.byPlayer[pid]; room != nil {
		if err != nil {
			room.state = customRoomCountdownState
		} else {
			room.matchID = matchID
		}
	}
	customRooms.Unlock()
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{"mode": match.Mode, "map": match.Map}).Warn("custom room: match not started")
		reply := customRoomResult(responseName, customRoomErrStartingGame)
		pushCustomRoom(others, reply)
		return reply
	}
	for _, p := range match.Players {
		squadHubInstance.armForMatch(p.PID)
	}
	reply := customRoomResult(responseName, 0)
	pushCustomRoom(others, reply)
	return reply
}

// buildCustomRoomFleetSelectPayload is one member's fleet select message:
// their own best fleet.
func buildCustomRoomFleetSelectPayload(responseName, pid string) []byte {
	b := customRoomResult(responseName, 0)
	return protocol.AppendInt32Field(b, "bestFleet", int32(customRoomFleetType(mmogPlayerStateForPID(pid), pid)))
}

// buildCustomRoomJoinPayload: a user joined (userInfo) and the room as it is now.
func buildCustomRoomJoinPayload(room *customRoom, u customRoomUser) []byte {
	b := customRoomResult(mmogRoomResponseName("YA_CustomRoomUserJoin"), 0)
	var stack []int
	b, stack = protocol.AppendObjectStart(b, stack, "userInfo")
	b, stack = appendCustomRoomUserFields(b, stack, u)
	b, stack = protocol.AppendObjectEnd(b, stack)
	b, _ = appendCustomRoomInfo(b, stack, room)
	return b
}

// buildCustomRoomInvitePayload is the push the invitee gets.
func buildCustomRoomInvitePayload(room *customRoom, inviter string) []byte {
	b := customRoomResult("YA_CustomRoomInvitePush", 0)
	var stack []int
	b, stack = protocol.AppendObjectStart(b, stack, "userInfo")
	if i, ok := room.user(inviter); ok {
		b, stack = appendCustomRoomUserFields(b, stack, room.users[i])
	} else {
		b, stack = appendCustomRoomUserFields(b, stack, customRoomUser{pid: inviter})
	}
	b, stack = protocol.AppendObjectEnd(b, stack)
	b, _ = appendCustomRoomInfo(b, stack, room)
	return b
}

func buildCustomRoomLeavePayload(u customRoomUser) []byte {
	b := customRoomResult(mmogRoomResponseName("YA_CustomRoomUserLeave"), 0)
	b = protocol.AppendStringField(b, "userPid", u.pid)
	b = protocol.AppendInt32Field(b, "team", int32(u.team))
	return protocol.AppendInt32Field(b, "slot", int32(u.slot))
}

func buildCustomRoomSwitchPayload(moved []customRoomUser) []byte {
	b := customRoomResult(mmogRoomResponseName("YA_CustomRoomUserSwitchTeam"), 0)
	var stack []int
	b, stack = protocol.AppendArrayStart(b, stack, "users")
	for _, u := range moved {
		b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		b = protocol.AppendStringField(b, "userPid", u.pid)
		b = protocol.AppendInt32Field(b, "team", int32(u.team))
		b = protocol.AppendInt32Field(b, "slot", int32(u.slot))
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	b, _ = protocol.AppendObjectEnd(b, stack)
	return b
}

func buildCustomRoomHostPayload(newHost string) []byte {
	b := customRoomResult(mmogRoomResponseName("YA_CustomRoomChangeHost"), 0)
	return protocol.AppendStringField(b, "newHostPid", newHost)
}

// customRoomSettingInt is a room setting as an integer (values come as
// float strings, "8.000000"), or def when it is not set or not a number.
func customRoomSettingInt(room *customRoom, id, def int) int {
	v, ok := room.settings[id]
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return int(f)
}

// customRoomMode is a mode the room offers: the one asked for, else TDM.
func customRoomMode(mode string) string {
	for _, m := range matchmaker.CustomRoomModes() {
		if strings.EqualFold(m.Mode, mode) {
			return m.Mode
		}
	}
	return "TDM"
}

// customRoomMap is a map the mode offers: the one asked for, else its first.
func customRoomMap(mode, mapName string) string {
	for _, m := range matchmaker.CustomRoomModes() {
		if m.Mode != mode || len(m.Maps) == 0 {
			continue
		}
		for _, name := range m.Maps {
			if strings.EqualFold(name, mapName) {
				return name
			}
		}
		return m.Maps[0]
	}
	return mapName
}

// buildCustomRoomSettingsPayload is the settings change reply. Its handler
// (YA_CustomRoomSettingsUpdateResponse, dispatcher 0x142A294B7..) reads ROOT
// "gameMode" and "mapName" into the room record (+0x10, +0x20) and the ROOT
// "settings" array (0x142A6AEE0 each) into +0x40, then broadcasts the update
// (0x142AB0B10). Sent inside roomInfo (first try, 2026-11-04 18:48) they read
// empty, the room's mode and map were cleared, and the lobby logged
// "GetSelectableMaps: Game mode not yet selected!" (0x140323720: maps are the
// selected mode's supportedMaps) and "GetSelectableGameModes: Map not yet
// selected!".
func buildCustomRoomSettingsPayload(responseName string, room *customRoom) []byte {
	var b []byte
	var stack []int
	b = protocol.AppendStringField(b, "RT", responseName)
	b, stack = protocol.AppendObjectStart(b, stack, "result")
	b = protocol.AppendStringField(b, fieldStatus, "ok")
	b = protocol.AppendInt32Field(b, "Code", 0)
	b, stack = protocol.AppendObjectEnd(b, stack)
	b = protocol.AppendStringField(b, "gameMode", room.mode)
	b = protocol.AppendStringField(b, "mapName", room.mapName)
	b, _ = appendCustomRoomSettings(b, stack, room)
	return b
}

func appendCustomRoomSettings(b []byte, stack []int, room *customRoom) ([]byte, []int) {
	b, stack = protocol.AppendArrayStart(b, stack, "settings")
	for id := 0; id <= 2; id++ { // id 3 (Metadata) is a JSON object, not sent
		v, ok := room.settings[id]
		if !ok {
			continue
		}
		b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		b = protocol.AppendStringField(b, "id", strconv.Itoa(id))
		b = protocol.AppendStringField(b, "value", v)
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	return protocol.AppendObjectEnd(b, stack)
}

func buildCustomRoomPayload(responseName string, room *customRoom) []byte {
	var b []byte
	var stack []int
	b = protocol.AppendStringField(b, "RT", responseName)
	b, stack = protocol.AppendObjectStart(b, stack, "result")
	b = protocol.AppendStringField(b, fieldStatus, "ok")
	b = protocol.AppendInt32Field(b, "Code", 0)
	b, stack = protocol.AppendObjectEnd(b, stack)
	b, _ = appendCustomRoomInfo(b, stack, room)
	return b
}

// appendCustomRoomInfo is the ROOT "roomInfo" node (parser 0x142A69BB0).
func appendCustomRoomInfo(b []byte, stack []int, room *customRoom) ([]byte, []int) {
	b, stack = protocol.AppendObjectStart(b, stack, "roomInfo")
	b = protocol.AppendStringField(b, "roomId", room.id)
	b = protocol.AppendStringField(b, "hostPid", room.host)
	b, stack = protocol.AppendArrayStart(b, stack, "teams")
	for team := 0; team < 2; team++ {
		b, stack = protocol.AppendUnnamedArrayStart(b, stack)
		for _, u := range room.users {
			if u.team == team {
				b, stack = appendCustomRoomUser(b, stack, u)
			}
		}
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	b, stack = protocol.AppendObjectEnd(b, stack)
	b = protocol.AppendStringField(b, "gameMode", room.mode)
	b = protocol.AppendStringField(b, "mapName", room.mapName)
	b, stack = appendCustomRoomSettings(b, stack, room)
	b, stack = protocol.AppendArrayStart(b, stack, "supportedModes")
	for _, m := range matchmaker.CustomRoomModes() {
		b, stack = protocol.AppendUnnamedObjectStart(b, stack)
		b = protocol.AppendStringField(b, "gameMode", m.Mode)
		b, stack = protocol.AppendStringArrayField(b, stack, "supportedMaps", m.Maps)
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	b, stack = protocol.AppendObjectEnd(b, stack)
	b = protocol.AppendStringField(b, "chatRoomId", "")
	return protocol.AppendObjectEnd(b, stack)
}

func appendCustomRoomUser(b []byte, stack []int, u customRoomUser) ([]byte, []int) {
	b, stack = protocol.AppendUnnamedObjectStart(b, stack)
	b, stack = appendCustomRoomUserFields(b, stack, u)
	return protocol.AppendObjectEnd(b, stack)
}

// appendCustomRoomUserFields is one user's fields (parser 0x142A6B0D0), inside
// an object the caller opened.
func appendCustomRoomUserFields(b []byte, stack []int, u customRoomUser) ([]byte, []int) {
	state := mmogPlayerStateForPID(u.pid)
	b = protocol.AppendStringField(b, "userPid", u.pid)
	b = protocol.AppendStringField(b, "DisplayName", state.displayName)
	b = protocol.AppendStringField(b, "team", strconv.Itoa(u.team))
	b = protocol.AppendStringField(b, "slot", strconv.Itoa(u.slot))
	b, stack = protocol.AppendObjectStart(b, stack, "info")
	b = protocol.AppendStringField(b, "UnlockedFleetType", strconv.Itoa(customRoomFleetType(state, u.pid)))
	b = protocol.AppendStringField(b, "Rank", strconv.Itoa(int(state.currentRank)))
	b = protocol.AppendStringField(b, "DisplayInfo", state.displayInfo)
	b, stack = protocol.AppendObjectEnd(b, stack)
	b = protocol.AppendStringField(b, "state", "0")
	var fleets []string
	for _, f := range unlockedFleets(state, u.pid, state.fleets) {
		if f.fleetType >= 1 && f.fleetType <= 3 {
			fleets = append(fleets, strconv.Itoa(int(f.fleetType)))
		}
	}
	return protocol.AppendStringArrayField(b, stack, "validFleets", fleets)
}

// customRoomFleetType is the player's highest unlocked fleet (EYFleetType 1-3).
func customRoomFleetType(state mmogPlayerState, pid string) int {
	best := 1
	for _, f := range unlockedFleets(state, pid, state.fleets) {
		if int(f.fleetType) > best && f.fleetType <= 3 {
			best = int(f.fleetType)
		}
	}
	return best
}

// logCustomRoomRequest logs a room request and the readable strings in it.
func logCustomRoomRequest(requestName, playerPID string, payload []byte) {
	logrus.WithFields(logrus.Fields{
		"request": requestName,
		"pid":     playerPID,
		"bytes":   len(payload),
		"text":    payloadText(payload, 40),
	}).Info("custom room: request")
}

// payloadText is the runs of 3+ printable ASCII bytes in a binary payload
// (field names and string values), at most max of them, joined by "|".
func payloadText(payload []byte, max int) string {
	var runs []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end-start >= 3 && len(runs) < max {
			runs = append(runs, string(payload[start:end]))
		}
		start = -1
	}
	for i, b := range payload {
		if b >= 0x20 && b < 0x7f {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(payload))
	return strings.Join(runs, "|")
}
