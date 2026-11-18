package main

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// customRoomSettingsPayload is a create/settings request's settings array as
// the client sends it: Setting objects, ID a 1-byte int, Value a float string.
func customRoomSettingsPayload(b []byte, values map[int]string) []byte {
	var stack []int
	b, stack = protocol.AppendArrayStart(b, stack, "InitialSettings")
	for id := 0; id <= 2; id++ {
		v, ok := values[id]
		if !ok {
			continue
		}
		b, stack = protocol.AppendObjectStart(b, stack, "Setting")
		b = append(b, 2, 'I', 'D', 0x26, byte(id))
		b = protocol.AppendStringField(b, "Value", v)
		b, stack = protocol.AppendObjectEnd(b, stack)
	}
	b, _ = protocol.AppendObjectEnd(b, stack)
	return b
}

// The lobby flow end to end: settings, fleet select, countdown, start. The
// start reaches the battle server with the room's map, team size and AI
// fill, records a custom match with the player's slot, and arms their
// connection; its results pay nothing.
func TestCustomRoomLobbyFlow(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	const pid = "c0570000000000000000000000000009"
	if err := seedMmogPlayerState(database, pid); err != nil {
		t.Fatal(err)
	}
	var spawn map[string]interface{}
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &spawn)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ip":"127.0.0.1","port":7799,"instance_id":"inst-c","match_id":"bm-c"}`))
	}))
	defer gm.Close()
	log := logrus.New()
	log.SetOutput(io.Discard)
	old := activeMatchmaker
	activeMatchmaker = matchmaker.New(database, log, gm.URL, "k", 2)
	t.Cleanup(func() { activeMatchmaker = old })
	defer handleCustomRoomRequest("YA_CustomRoomUserLeave", pid, nil)

	create := protocol.AppendStringField(protocol.AppendStringField(nil, "GameMode", "TDM"), "MapName", "Gorge")
	create = customRoomSettingsPayload(create, map[int]string{0: "8.000000", 1: "0.000000", 2: "0.000000"})
	if _, ok := handleCustomRoomRequest("YA_CustomRoomCreate", pid, create); !ok {
		t.Fatal("create not handled")
	}
	change := customRoomSettingsPayload(protocol.AppendStringField(nil, "MapName", "Skybridge"),
		map[int]string{0: "3.000000", 1: "1.000000"})
	if reply, ok := handleCustomRoomRequest("YA_CustomRoomChangeSettings", pid, change); !ok ||
		protocol.ExtractStringField(reply, "mapName") != "Skybridge" {
		t.Fatalf("settings: %v %q", ok, protocol.ExtractStringField(reply, "mapName"))
	}

	reply, ok := handleCustomRoomRequest("YA_CustomRoomEnterFleetSelect", pid, nil)
	if !ok || protocol.ExtractStringField(reply, "RT") != "YA_CustomRoomEnterFleetSelectResponse" {
		t.Fatalf("fleet select: %v %q", ok, protocol.ExtractStringField(reply, "RT"))
	}
	if r, _ := protocol.ExtractInt32Field(reply, "result"); r != 0 {
		t.Fatalf("fleet select result %d", r)
	}
	if f, ok := protocol.ExtractInt32Field(reply, "bestFleet"); !ok || f < 1 || f > 3 {
		t.Fatalf("bestFleet %d %v", f, ok)
	}

	countdown := protocol.AppendInt32Field(nil, "FleetType", 1)
	reply, ok = handleCustomRoomRequest("YA_CustomRoomStartMatchCountdown", pid, countdown)
	if !ok || protocol.ExtractStringField(reply, "RT") != "YA_CustomRoomStartMatchCountdownResponse" {
		t.Fatalf("countdown: %v %q", ok, protocol.ExtractStringField(reply, "RT"))
	}
	secs, _ := protocol.ExtractInt32Field(reply, "countdownSeconds")
	end, _ := strconv.ParseInt(protocol.ExtractStringField(reply, "countdownEndTimestamp"), 10, 64)
	if secs != customRoomCountdown || end < time.Now().Unix() || end > time.Now().Unix()+customRoomCountdown+1 {
		t.Fatalf("countdown %d s, end %d", secs, end)
	}
	if f, _ := protocol.ExtractInt32Field(reply, "fleet"); f != 1 {
		t.Fatalf("fleet %d", f)
	}

	reply, ok = handleCustomRoomRequest("YA_CustomRoomStartMatch", pid, nil)
	if r, _ := protocol.ExtractInt32Field(reply, "result"); !ok || r != 0 {
		t.Fatalf("start: %v result %d", ok, r)
	}
	if spawn["map"] != "Skybridge" || spawn["team_ai"] != "10" || spawn["team_size"] != float64(3) || spawn["max_players"] != float64(6) {
		t.Fatalf("spawn request %v", spawn)
	}
	var custom, team int
	if err := database.QueryRow(`SELECT m.custom, s.team FROM matches m JOIN match_slots s ON s.match_id=m.id WHERE s.user_id=?`, pid).Scan(&custom, &team); err != nil {
		t.Fatal(err)
	}
	if custom != 1 || team != 1 {
		t.Fatalf("custom %d team %d", custom, team)
	}
	if squadHubInstance.takeArm(pid) != 1 {
		t.Fatal("the player's connection was not armed for the match")
	}
	if !matchIsCustom(database, "bm-c") || !matchIsCustom(database, "bm-c-r2") || matchIsCustom(database, "other") {
		t.Fatal("matchIsCustom")
	}
	// A repeat start (the host's client) is answered as done, nothing new.
	spawn = nil
	if reply, _ := handleCustomRoomRequest("YA_CustomRoomStartMatch", pid, nil); spawn != nil {
		t.Fatalf("second start spawned again: %v", spawn)
	} else if r, _ := protocol.ExtractInt32Field(reply, "result"); r != 0 {
		t.Fatalf("repeat start result %d", r)
	}
}

// Without a battle server the start fails as an error the lobby can show.
func TestCustomRoomStartMatchFailureIsAnError(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const pid = "c057000000000000000000000000000a"
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer gm.Close()
	log := logrus.New()
	log.SetOutput(io.Discard)
	old := activeMatchmaker
	activeMatchmaker = matchmaker.New(currentMmogPlayerStateDB(), log, gm.URL, "k", 2)
	t.Cleanup(func() { activeMatchmaker = old })
	defer handleCustomRoomRequest("YA_CustomRoomUserLeave", pid, nil)
	handleCustomRoomRequest("YA_CustomRoomCreate", pid, protocol.AppendStringField(nil, "GameMode", "TDM"))
	reply, _ := handleCustomRoomRequest("YA_CustomRoomStartMatch", pid, nil)
	if r, _ := protocol.ExtractInt32Field(reply, "result"); r == 0 {
		t.Fatal("a failed start answered success")
	}
	if squadHubInstance.takeArm(pid) != 0 {
		t.Fatal("armed although nothing started")
	}
}

// guidField is a 16-byte GUID field (tag 0x02) as the client sends ids.
func guidField(b []byte, name, hexID string) []byte {
	raw, _ := hex.DecodeString(hexID)
	b = append(b, byte(len(name)))
	b = append(b, name...)
	b = append(b, 0x02)
	return append(b, raw...)
}

func drainRT(pid string) []string {
	var out []string
	for _, p := range squadHubInstance.drainPushes(pid) {
		out = append(out, protocol.ExtractStringField(p, "RT"))
	}
	return out
}

// Two players: invite, join (the host is told), team switch, host change,
// the host leaving (a new host is named), a kick, and a disconnect.
func TestCustomRoomTwoPlayers(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const host, guest = "c057000000000000000000000000000b", "c057000000000000000000000000000c"
	for _, p := range []string{host, guest} {
		squadHubInstance.connected(p)
		defer squadHubInstance.disconnected(p)
		defer handleCustomRoomRequest("YA_CustomRoomUserLeave", p, nil)
	}
	handleCustomRoomRequest("YA_CustomRoomCreate", host, customRoomSettingsPayload(
		protocol.AppendStringField(nil, "GameMode", "TDM"), map[int]string{0: "2.000000"}))
	customRooms.Lock()
	roomID := customRooms.byPlayer[host].id
	customRooms.Unlock()

	handleCustomRoomRequest("YA_CustomRoomInvite", host, guidField(guidField(nil, "InviteeId", guest), "RoomId", roomID))
	if got := drainRT(guest); len(got) != 1 || got[0] != "YA_CustomRoomInvitePush" {
		t.Fatalf("invitee pushes %v", got)
	}

	reply, ok := handleCustomRoomRequest("YA_CustomRoomUserJoin", guest, guidField(nil, "RoomID", roomID))
	if r, _ := protocol.ExtractInt32Field(reply, "result"); !ok || r != 0 ||
		protocol.ExtractStringField(reply, "RT") != "YA_CustomRoomUserJoinResponse" {
		t.Fatalf("join: %v result %d", ok, r)
	}
	if protocol.ExtractStringField(reply, "roomId") != roomID {
		t.Fatal("join reply has no roomInfo")
	}
	if got := drainRT(host); len(got) != 1 || got[0] != "YA_CustomRoomUserJoinResponse" {
		t.Fatalf("host pushes after join %v", got)
	}
	customRooms.Lock()
	room := customRooms.byPlayer[guest]
	i, _ := room.user(guest)
	guestTeam := room.users[i].team
	customRooms.Unlock()
	if guestTeam != 1 {
		t.Fatalf("guest on team %d, want the emptier team 1", guestTeam)
	}

	// The guest moves to team 0 (room for 2 a team).
	sw := protocol.AppendInt32Field(guidField(nil, "UserID", guest), "DesiredTeam", 0)
	reply, _ = handleCustomRoomRequest("YA_CustomRoomUserSwitchTeam", guest, sw)
	if r, _ := protocol.ExtractInt32Field(reply, "result"); r != 0 {
		t.Fatalf("switch result %d", r)
	}
	if got := drainRT(host); len(got) != 1 || got[0] != "YA_CustomRoomUserSwitchTeamResponse" {
		t.Fatalf("host pushes after switch %v", got)
	}

	// The host hands over, then leaves: the guest stays host of the room.
	reply, _ = handleCustomRoomRequest("YA_CustomRoomChangeHost", host, guidField(nil, "NewHostID", guest))
	if r, _ := protocol.ExtractInt32Field(reply, "result"); r != 0 || protocol.ExtractStringField(reply, "newHostPid") != guest {
		t.Fatalf("change host result %d", r)
	}
	drainRT(guest)
	handleCustomRoomRequest("YA_CustomRoomUserLeave", host, nil)
	if got := drainRT(guest); len(got) != 1 || got[0] != "YA_CustomRoomUserLeaveResponse" {
		t.Fatalf("guest pushes after the host left %v", got)
	}
	customRooms.Lock()
	if r := customRooms.byPlayer[guest]; r == nil || r.host != guest || len(r.users) != 1 {
		t.Fatalf("room after the host left: %+v", r)
	}
	customRooms.Unlock()

	// The old host joins again and is kicked by the new one.
	handleCustomRoomRequest("YA_CustomRoomUserJoin", host, guidField(nil, "RoomID", roomID))
	drainRT(guest)
	handleCustomRoomRequest("YA_CustomRoomUserRemove", guest, guidField(nil, "UserID", host))
	if got := drainRT(host); len(got) != 1 || got[0] != "YA_CustomRoomUserLeaveResponse" {
		t.Fatalf("kicked player pushes %v", got)
	}
	customRooms.Lock()
	kicked := customRooms.byPlayer[host]
	customRooms.Unlock()
	if kicked != nil {
		t.Fatal("kicked player still in the room")
	}

	// The last member goes offline: the room is gone.
	customRoomDisconnected(guest)
	customRooms.Lock()
	gone := customRooms.byID[roomID] == nil
	customRooms.Unlock()
	if !gone {
		t.Fatal("empty room kept")
	}
}

// A full room refuses a joiner.
func TestCustomRoomJoinFull(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	const host, a, b = "c057000000000000000000000000000d", "c057000000000000000000000000000e", "c057000000000000000000000000000f"
	for _, p := range []string{host, a, b} {
		defer handleCustomRoomRequest("YA_CustomRoomUserLeave", p, nil)
	}
	handleCustomRoomRequest("YA_CustomRoomCreate", host, customRoomSettingsPayload(
		protocol.AppendStringField(nil, "GameMode", "TDM"), map[int]string{0: "1.000000"}))
	customRooms.Lock()
	roomID := customRooms.byPlayer[host].id
	customRooms.Unlock()
	if reply, _ := handleCustomRoomRequest("YA_CustomRoomUserJoin", a, guidField(nil, "RoomID", roomID)); func() int32 {
		r, _ := protocol.ExtractInt32Field(reply, "result")
		return r
	}() != 0 {
		t.Fatal("second player refused in a 1v1 room")
	}
	reply, _ := handleCustomRoomRequest("YA_CustomRoomUserJoin", b, guidField(nil, "RoomID", roomID))
	if r, _ := protocol.ExtractInt32Field(reply, "result"); r != customRoomErrJoinRoomFull {
		t.Fatalf("third player in a 1v1 room: result %d", r)
	}
}
