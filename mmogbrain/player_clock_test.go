package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/golang-jwt/jwt/v5"
)

func useCleanPlayerClocks(t *testing.T) {
	t.Helper()
	playerClocks.once = sync.Once{}
	t.Cleanup(func() { playerClocks.once = sync.Once{} })
}

// The Windows short-time patterns players have, in their own clock.
func TestFormatWindowsTime(t *testing.T) {
	at := time.Date(2026, 11, 7, 14, 5, 9, 0, time.UTC)
	morning := time.Date(2026, 11, 7, 0, 7, 0, 0, time.UTC)
	for _, c := range []struct {
		at    time.Time
		clock playerClock
		want  string
	}{
		{at, playerClock{pattern: "HH:mm"}, "14:05"},
		{at, playerClock{pattern: "H:mm"}, "14:05"},
		{morning, playerClock{pattern: "H:mm"}, "0:07"},
		{morning, playerClock{pattern: "HH:mm"}, "00:07"},
		{at, playerClock{pattern: "h:mm tt", am: "AM", pm: "PM"}, "2:05 PM"},
		{morning, playerClock{pattern: "h:mm tt", am: "AM", pm: "PM"}, "12:07 AM"},
		{at, playerClock{pattern: "tt hh:mm", am: "오전", pm: "오후"}, "오후 02:05"},
		{at, playerClock{pattern: "h:mm t"}, "2:05 P"},
		{at, playerClock{pattern: "HH'h'mm"}, "14h05"},
		{at, playerClock{pattern: "HH.mm.ss"}, "14.05.09"},
		{at, playerClock{}, "14:05"},
	} {
		if got := formatWindowsTime(c.at, c.clock); got != c.want {
			t.Errorf("%q at %s: %q, want %q", c.clock.pattern, c.at.Format("15:04"), got, c.want)
		}
	}
}

// The launcher reports the clock with the player's token; the stamp then
// uses it, and without one it is UTC, marked so.
func TestLauncherClock(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanPlayerClocks(t)
	oldSecret := mmogJWTSecret()
	setMmogJWTSecret([]byte("test-secret"))
	t.Cleanup(func() { setMmogJWTSecret(oldSecret) })
	const user = "4d1f0c2a-6b7e-4c55-9a10-3f2b8e9d7c61"
	pid := protocol.NormalizePlayerPID(user)
	token := mustSignTestJWT(t, jwt.MapClaims{"sub": user, "user_id": user, "iss": protocol.GatewayJWTIssuer,
		"aud": "launcher", "realm": "dreadnought.pc-us", "exp": time.Now().Add(time.Hour).Unix()})

	now := time.Date(2026, 11, 7, 22, 9, 0, 0, time.UTC)
	if got := chatTimeStamp(pid, now); got != "[22:09 UTC] " {
		t.Fatalf("no clock yet: %q", got)
	}
	post := func(auth, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/launcher/clock", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		launcherClockHandler(rec, req)
		return rec.Code
	}
	if c := post("", `{"utc_offset":60}`); c != http.StatusUnauthorized {
		t.Errorf("without a token: %d", c)
	}
	if c := post("not-a-token", `{"utc_offset":60}`); c != http.StatusUnauthorized {
		t.Errorf("bad token: %d", c)
	}
	for _, body := range []string{`{}`, `{"utc_offset":900}`, `{"utc_offset":0,"pattern":"HH:mm\n"}`,
		`{"utc_offset":0,"pattern":"` + strings.Repeat("H", 33) + `"}`} {
		if c := post(token, body); c != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, c)
		}
	}
	if c := post(token, `{"utc_offset":-480,"pattern":"h:mm tt","am":"AM","pm":"PM"}`); c != http.StatusOK {
		t.Fatalf("save: %d", c)
	}
	if got := chatTimeStamp(pid, now); got != "[2:09 PM] " {
		t.Errorf("Pacific player: %q, want [2:09 PM] ", got)
	}
	// Read back after a restart.
	playerClocks.once = sync.Once{}
	if got := chatTimeStamp(strings.ToUpper(pid), now); got != "[2:09 PM] " {
		t.Errorf("after a restart: %q", got)
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM player_clock`).Scan(&n)
	if n != 1 {
		t.Errorf("%d rows", n)
	}
}

// Each player's copy of an automatic line carries their own time.
func TestAnnouncementIsStampedPerPlayer(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	useCleanPlayerClocks(t)
	loadPlayerClocks()
	playerClocks.mu.Lock()
	playerClocks.by["0123456789abcdef0123456789abcdef"] = playerClock{offset: 60, pattern: "HH:mm"}
	playerClocks.mu.Unlock()
	hub := socialTestHub(t)
	_, readA := socialTestPeer(t, hub, "0123456789abcdef0123456789abcdef")
	_, readB := socialTestPeer(t, hub, "fedcba9876543210fedcba9876543210")
	before := time.Now().UTC()
	go hub.announce("3 players online")
	chatLine := func(r *bufio.Reader) map[string]any {
		for i := 0; i < 10; i++ {
			if method, data := readNotice(t, r); method == "chat.channel.message" {
				return data
			}
		}
		t.Fatal("no chat line")
		return nil
	}
	a, b := chatLine(readA), chatLine(readB)
	wantA := "[" + before.Add(time.Hour).Format("15:04") + "] 3 players online"
	wantB := "[" + before.Format("15:04") + " UTC] 3 players online"
	if a["text"] != wantA {
		t.Errorf("player with a clock got %q, want %q", a["text"], wantA)
	}
	if b["text"] != wantB {
		t.Errorf("player without one got %q, want %q", b["text"], wantB)
	}
}
