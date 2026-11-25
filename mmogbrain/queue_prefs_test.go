package main

import (
	"strings"
	"testing"
	"time"
)

const (
	prefTestPID   = "0123456789abcdef0123456789abcdef"
	prefTestOther = "fedcba9876543210fedcba9876543210"
)

// /pvp-only stores the choice and answers the sender with a Server line;
// /pvp turns it off; the choice applies to the two-team modes, Onslaught
// included, and never to the co-op ones.
func TestPvPOnlyCommandStoresTheChoice(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	hub := socialTestHub(t)
	peer, read := socialTestPeer(t, hub, prefTestPID)
	pushes := pushLines(read)

	if !chatQueueCommand(peer, "/pvp-only") {
		t.Fatal("/pvp-only not taken as a command")
	}
	nextPush(t, pushes) // the Server profile
	_, data := nextPush(t, pushes)
	if text := unstamped(t, data["text"]); !strings.HasPrefix(text, "PvP only is on") {
		t.Errorf("reply %q", text)
	}
	if !playerQueuePvPOnly(database, prefTestPID) {
		t.Fatal("choice not stored")
	}
	for mode, want := range map[string]bool{"TDM": true, "TurboTDM": true, "TE": true, "TER": true, "PodTDM": true,
		"Onslaught": true, "BC": false, "TM": false, "Havoc": false} {
		if got := queueEntryPvPOnly(database, prefTestPID, mode); got != want {
			t.Errorf("%s: queue entry pvp_only %v, want %v", mode, got, want)
		}
	}
	// Dashed spelling of the same player reads the same choice.
	dashed := prefTestPID[:8] + "-" + prefTestPID[8:12] + "-" + prefTestPID[12:16] + "-" + prefTestPID[16:20] + "-" + prefTestPID[20:]
	if !playerQueuePvPOnly(database, dashed) {
		t.Error("the dashed id does not find the choice")
	}

	if !chatQueueCommand(peer, "  /PVP ") {
		t.Fatal("/pvp not taken as a command")
	}
	_, data = nextPush(t, pushes)
	if text := unstamped(t, data["text"]); !strings.HasPrefix(text, "PvP with bots is on") {
		t.Errorf("reply %q", text)
	}
	if playerQueuePvPOnly(database, prefTestPID) {
		t.Error("/pvp did not turn PvP only off")
	}
}

func TestOnlyQueueCommandsAreTaken(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	hub := socialTestHub(t)
	peer, _ := socialTestPeer(t, hub, prefTestPID)
	for _, line := range []string{"hello", "/gp", "/sadf", "hi /pvp", "/pvpx", "!pvp"} {
		if chatQueueCommand(peer, line) {
			t.Errorf("%q was taken as a queue command", line)
		}
	}
}

// Through the chat handler: the command line is answered to its sender only,
// never relayed to the channel and never stored.
func TestQueueCommandIsNotRelayedOrStored(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	hub := socialTestHub(t)
	sender, _ := socialTestPeer(t, hub, prefTestPID)
	_, otherRead := socialTestPeer(t, hub, prefTestOther)
	others := pushLines(otherRead)

	res := handleChatMethod(socialRequest{method: "chat.channel.message", hub: hub, peer: sender,
		params: map[string]any{"channel": defaultChatChannels[0], "message": "/pvp-only"}})
	if res == nil {
		t.Fatal("no reply")
	}
	if !noPush(others, 200*time.Millisecond) {
		t.Error("the command reached another player")
	}
	var stored int
	_ = database.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE content LIKE '/pvp%'`).Scan(&stored)
	if stored != 0 {
		t.Errorf("the command line was stored %d time(s)", stored)
	}
}

// A waiting solo queue entry moves to the pool just picked; a squad entry
// keeps its leader's choice; a co-op mode is never PvP only.
func TestPvPOnlyCommandMovesTheWaitingSoloEntry(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	insert := func(id, user, mode, party string) {
		if _, err := database.Exec(`INSERT INTO queue_entries(id,user_id,game_mode,tier_min,tier_max,fleet_type,status,party_id)
			VALUES(?,?,?,1,5,2,'waiting',?)`, id, user, mode, party); err != nil {
			t.Fatal(err)
		}
	}
	pvpOnlyOf := func(id string) (v int) {
		_ = database.QueryRow(`SELECT pvp_only FROM queue_entries WHERE id=?`, id).Scan(&v)
		return
	}
	insert("solo", prefTestPID, "TDM", "")
	if !requeuePvPOnly(database, prefTestPID, true) || pvpOnlyOf("solo") != 1 {
		t.Error("the waiting solo TDM entry did not move to the PvP-only pool")
	}
	if requeuePvPOnly(database, prefTestPID, true) {
		t.Error("moved an entry that was already in the pool")
	}
	insert("sq", prefTestOther, "TDM", "squad1")
	if requeuePvPOnly(database, prefTestOther, true) || pvpOnlyOf("sq") != 0 {
		t.Error("a squad member's entry was moved by their own choice")
	}
	_, _ = database.Exec(`DELETE FROM queue_entries`)
	insert("bc", prefTestPID, "BC", "")
	if requeuePvPOnly(database, prefTestPID, true) || pvpOnlyOf("bc") != 0 {
		t.Error("a Proving Grounds entry was made PvP only")
	}
}

func TestPvPOnlyQueueAnnouncement(t *testing.T) {
	want := "A squad of 2 is searching for a PvP-only Legendary Onslaught match (no bots). Type /pvp-only and queue with your Legendary fleet to play against them!"
	if got := serverChatQueueLine("Onslaught", 3, 2, true); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
