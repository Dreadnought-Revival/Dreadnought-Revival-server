package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// serverLineText waits for the next Server chat line among a peer's pushes
// (skipping profiles and room notices) and returns its text.
func serverLineText(t *testing.T, ch <-chan map[string]any) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatal("connection closed")
			}
			data, _ := msg["data"].(map[string]any)
			if msg["type"] == "chat.channel.message" && data["sender"] == serverChatGUID {
				text, _ := data["text"].(string)
				return text
			}
		case <-deadline:
			t.Fatal("no Server line arrived")
			return ""
		}
	}
}

// The dashboard's messages: a notice to everyone online (also with the
// automatic lines off), and a message of the day to each player who comes
// online.
func TestAdminServerMessages(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	t.Setenv("DN_SERVER_CHAT", "0") // turns off only the automatic lines
	hub := socialTestHub(t)
	saved := socialHubInstance
	socialHubInstance = hub
	t.Cleanup(func() { socialHubInstance = saved })
	savedDelay := motdDelay
	motdDelay = 10 * time.Millisecond
	t.Cleanup(func() { motdDelay = savedDelay })
	t.Cleanup(func() { _ = setMessageOfTheDay(nil, "") })

	_, readA := socialTestPeer(t, hub, "player-a")
	_, readB := socialTestPeer(t, hub, "player-b")
	a, b := pushLines(readA), pushLines(readB)
	r := adminTestRouter()

	if rec := adminPost(r, "/admin/api/messages/send", "", `{"text":"hi"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}
	long := make([]byte, 301)
	for i := range long {
		long[i] = 'x'
	}
	for body, want := range map[string]int{`{"text":"   "}`: http.StatusBadRequest, `{"text":"` + string(long) + `"}`: http.StatusBadRequest} {
		if rec := adminPost(r, "/admin/api/messages/send", "test-admin-key", body); rec.Code != want {
			t.Errorf("send %.20s: %d, want %d", body, rec.Code, want)
		}
	}

	rec := adminPost(r, "/admin/api/messages/send", "test-admin-key", `{"text":"  Restart in\n10 minutes "}`)
	var sent struct{ Recipients int }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &sent) != nil || sent.Recipients != 2 {
		t.Fatalf("send: %d %s, want 2 recipients", rec.Code, rec.Body.String())
	}
	for name, ch := range map[string]<-chan map[string]any{"a": a, "b": b} {
		if got := serverLineText(t, ch); got != "Restart in 10 minutes" {
			t.Errorf("player %s got %q", name, got)
		}
	}

	// Message of the day.
	if rec := adminPost(r, "/admin/api/messages/motd", "test-admin-key", `{"text":"Welcome back, Captain!"}`); rec.Code != http.StatusOK {
		t.Fatalf("motd: %d %s", rec.Code, rec.Body.String())
	}
	var stored string
	_ = database.QueryRow(`SELECT value FROM server_flags WHERE name='motd'`).Scan(&stored)
	if stored != "Welcome back, Captain!" {
		t.Errorf("stored motd %q", stored)
	}
	peerC, readC := socialTestPeer(t, hub, "player-c")
	c := pushLines(readC)
	sendMessageOfTheDay(peerC)
	if got := serverLineText(t, c); got != "Welcome back, Captain!" {
		t.Errorf("motd line %q", got)
	}

	if rec := adminPost(r, "/admin/api/messages/motd", "test-admin-key", `{"text":""}`); rec.Code != http.StatusOK {
		t.Fatalf("motd off: %d", rec.Code)
	}
	peerD, readD := socialTestPeer(t, hub, "player-d")
	d := pushLines(readD)
	sendMessageOfTheDay(peerD)
	deadline := time.After(200 * time.Millisecond)
	for done := false; !done; {
		select {
		case msg := <-d:
			if data, _ := msg["data"].(map[string]any); msg["type"] == "chat.channel.message" && data["sender"] == serverChatGUID {
				t.Fatal("a message of the day went out while it was off")
			}
		case <-deadline:
			done = true
		}
	}

	var listed struct {
		Motd   string `json:"motd"`
		Recent []struct {
			Action string `json:"action"`
		} `json:"recent"`
	}
	rec = adminGet(r, "/admin/api/messages", "test-admin-key")
	if json.Unmarshal(rec.Body.Bytes(), &listed) != nil || listed.Motd != "" || len(listed.Recent) != 3 {
		t.Errorf("messages: %s, want no motd and 3 recent (send, set, off)", rec.Body.String())
	}
}
