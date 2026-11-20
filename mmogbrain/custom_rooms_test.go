package main

import (
	"bytes"
	"testing"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
)

func TestPayloadText(t *testing.T) {
	got := payloadText([]byte("\x01\x02RT\x00\x05YA_CustomRoomCreate\x00\x0aMapName\x00ab\x00Highlands"), 10)
	if got != "YA_CustomRoomCreate|MapName|Highlands" {
		t.Fatalf("payloadText = %q", got)
	}
}

// A created room names its host (the client's own PID) and has it in team 0,
// with the mode and map lists the lobby's dropdowns are filled from.
func TestCustomRoomCreateReply(t *testing.T) {
	pid := "c0570000000000000000000000000001"
	req := protocol.AppendStringField(protocol.AppendStringField(nil, "GameMode", "Onslaught"), "MapName", "ANY")
	reply, ok := handleCustomRoomRequest("YA_CustomRoomCreate", pid, req)
	if !ok {
		t.Fatal("create not handled")
	}
	for _, want := range []string{"YA_CustomRoomCreateResponse", "roomInfo", "hostPid", pid, "teams", "userPid",
		"supportedModes", "supportedMaps", "chatRoomId", "validFleets"} {
		if !bytes.Contains(reply, []byte(want)) {
			t.Errorf("reply lacks %q", want)
		}
	}
	// Escalation has no GlobalUI label, so the lobby is not told its name
	// (see customRoomLabelledModes); it travels under customRoomEscalationKey.
	if bytes.Contains(reply, []byte("Escalation")) {
		t.Error("the lobby is offered an unlabelled mode name")
	}
	if !bytes.Contains(reply, []byte(customRoomEscalationKey)) {
		t.Errorf("Escalation not offered under %q", customRoomEscalationKey)
	}
	if _, ok := handleCustomRoomRequest("YA_CustomRoomUserLeave", pid, nil); ok {
		t.Error("leave should keep the generic reply")
	}
	if customRooms.byPlayer[pid] != nil {
		t.Error("room kept after leave")
	}
}

// The settings reply carries gameMode / mapName / settings at the ROOT.
func TestCustomRoomSettingsReplyAtRoot(t *testing.T) {
	pid := "c0570000000000000000000000000002"
	handleCustomRoomRequest("YA_CustomRoomCreate", pid, protocol.AppendStringField(nil, "GameMode", "TDM"))
	defer handleCustomRoomRequest("YA_CustomRoomUserLeave", pid, nil)
	reply, ok := handleCustomRoomRequest("YA_CustomRoomChangeSettings", pid,
		protocol.AppendStringField(protocol.AppendStringField(nil, "GameMode", customRoomEscalationKey), "MapName", "Gorge"))
	if !ok {
		t.Fatal("settings change not handled")
	}
	if bytes.Contains(reply, []byte("roomInfo")) {
		t.Error("settings reply nests the room; the client reads root fields")
	}
	if got := protocol.ExtractStringField(reply, "gameMode"); got != customRoomEscalationKey {
		t.Errorf("gameMode = %q", got)
	}
	if got := customRooms.byPlayer[pid].mode; got != matchmaker.EscalationGameMode {
		t.Errorf("room mode = %q, want Escalation behind its lobby key", got)
	}
	if got := protocol.ExtractStringField(reply, "mapName"); got != "Gorge" {
		t.Errorf("mapName = %q", got)
	}
}

// globalUIMapNames are the m_mapName values of the client's GlobalUI
// m_multiplayerMaps (22 entries, dumped from Generic/UI/GlobalUI.uasset with
// tools/bpdump --props, 2026-11-20). A map name outside this list has no
// label in the lobby's map dropdown.
var globalUIMapNames = map[string]bool{
	"ANY": true, "Highlands": true, "Highlands_Night": true, "Glacier": true, "Glacier_Night": true,
	"Gorge": true, "Gorge_Night": true, "Space": true, "Space_Night": true, "Skybridge": true,
	"Skybridge_Night": true, "Space02": true, "Space02_Night": true, "Thebe": true, "Elysium": true,
	"Elysium_Night": true, "Tutorial": true, "Juno": true, "Space04": true, "Amirani": true,
	"Amirani_Night": true, "Derelict": true,
}

// Every option the lobby's two dropdowns are built from has a label in the
// client's GlobalUI. An option without one is blank, and a blank option is
// what UI_Dropdown_Generic's ClearOptions -> OnSelectionChanged("") picks
// (observed: ChangeSettings{Escalation} first in every uploaded lobby log).
// The client also forces the first listed mode and that mode's first map when
// the room appears (HandleDataReady -> RefreshDropdowns(0, 0)), so those are
// the lobby's defaults: Team Deathmatch on Highlands.
func TestCustomRoomLobbyOffersOnlyLabelledOptions(t *testing.T) {
	modes := customRoomLobbyModes()
	if len(modes) == 0 {
		t.Fatal("no modes offered")
	}
	if modes[0].Mode != "TDM" || len(modes[0].Maps) == 0 || modes[0].Maps[0] != "Highlands" {
		t.Errorf("first option is %q on %v; the client defaults to it", modes[0].Mode, modes[0].Maps)
	}
	seen := map[string]bool{}
	for _, m := range modes {
		if !customRoomLabelledModes[m.Mode] {
			t.Errorf("mode %q has no GlobalUI label", m.Mode)
		}
		if seen[m.Mode] {
			t.Errorf("mode %q offered twice", m.Mode)
		}
		seen[m.Mode] = true
		if len(m.Maps) == 0 {
			t.Errorf("mode %q has no maps", m.Mode)
		}
		for _, name := range m.Maps {
			if !globalUIMapNames[name] {
				t.Errorf("mode %q map %q has no GlobalUI label", m.Mode, name)
			}
		}
	}
	if !seen[customRoomEscalationKey] {
		t.Errorf("Escalation is not offered under %q", customRoomEscalationKey)
	}
}

// The "Fill Team 1/2" checkboxes are settings 1 and 2. The client shows a
// team as filled only for the exact value "1" (0x322B70), so the settings
// reply sends the canonical form whatever float string the client sent; the
// battle server reads the same flag.
func TestCustomRoomTeamAIFlagsAreCanonical(t *testing.T) {
	pid := "c0570000000000000000000000000003"
	handleCustomRoomRequest("YA_CustomRoomCreate", pid, protocol.AppendStringField(nil, "GameMode", "TDM"))
	defer handleCustomRoomRequest("YA_CustomRoomUserLeave", pid, nil)
	req := customRoomSettingsPayload(nil, map[int]string{0: "5.000000", 1: "1.000000", 2: "0.000000"})
	reply, ok := handleCustomRoomRequest("YA_CustomRoomChangeSettings", pid, req)
	if !ok {
		t.Fatal("settings change not handled")
	}
	got := protocol.IDValuePairs(reply, "id", "value")
	if got[1] != "1" || got[2] != "0" || got[0] != "5.000000" {
		t.Errorf("settings reply %v", got)
	}
	if ai := [2]bool{customRoomSettingInt(customRooms.byPlayer[pid], 1, 0) != 0, customRoomSettingInt(customRooms.byPlayer[pid], 2, 0) != 0}; ai != [2]bool{true, false} {
		t.Errorf("room AI flags %v", ai)
	}
}
