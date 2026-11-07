package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/sirupsen/logrus"
)

// The player's own clock, for the time stamp on the automatic Server chat
// lines (the online count and "a player is searching", server_chat.go;
// operator 2026-11-07: "no date, just the player's local time format").
//
// The game cannot show a time itself: a chat row (UI_Chat_Row) has no time
// widget -- nothing time-like among its names -- so chatMessageNotice's
// "timestamp" is never displayed, and the time has to be part of the text.
// And the game never tells the server its time zone: ClientTime/ServerTime
// in YA_PlayerGet go server -> client, the telemetry carries no zone, and
// the exe's only "timezone" strings are ICU locale data. Our launcher knows
// both, so it reports them on Play: POST /launcher/clock with the player's
// token, {"utc_offset": minutes east of UTC, "pattern": the Windows short-time
// pattern (LOCALE_SSHORTTIME, e.g. "HH:mm" or "h:mm tt"), "am"/"pm": the
// AM/PM designators}. A player whose launcher has not reported one (an older
// launcher) sees the time in UTC, marked "UTC".

type playerClock struct {
	offset  int // minutes east of UTC
	pattern string
	am, pm  string
}

var playerClocks struct {
	once sync.Once
	mu   sync.RWMutex
	by   map[string]playerClock // normalized pid -> clock
}

// loadPlayerClocks reads the stored clocks once (at startup: the lines are
// stamped inside a send loop, which must not touch the database).
func loadPlayerClocks() {
	playerClocks.once.Do(func() {
		m := map[string]playerClock{}
		if database := currentMmogPlayerStateDB(); database != nil {
			if rows, err := database.Query(`SELECT user_id, utc_offset, pattern, am, pm FROM player_clock`); err == nil {
				for rows.Next() {
					var pid string
					var c playerClock
					if rows.Scan(&pid, &c.offset, &c.pattern, &c.am, &c.pm) == nil {
						m[pid] = c
					}
				}
				_ = rows.Close()
			}
		}
		playerClocks.mu.Lock()
		playerClocks.by = m
		playerClocks.mu.Unlock()
	})
}

func playerClockFor(pid string) (playerClock, bool) {
	loadPlayerClocks()
	playerClocks.mu.RLock()
	defer playerClocks.mu.RUnlock()
	c, ok := playerClocks.by[protocol.NormalizePlayerPID(pid)]
	return c, ok
}

// chatTimeStamp is the "[14:05] " a Server line starts with for this player.
func chatTimeStamp(pid string, now time.Time) string {
	c, ok := playerClockFor(pid)
	if !ok {
		return "[" + now.UTC().Format("15:04") + " UTC] "
	}
	return "[" + formatWindowsTime(now.UTC().Add(time.Duration(c.offset)*time.Minute), c) + "] "
}

// formatWindowsTime writes t (already in the player's zone) the way a
// Windows time pattern asks: h/hh 12-hour, H/HH 24-hour, m/mm, s/ss,
// t/tt the AM/PM designator (first letter / whole), 'quoted' text as is.
func formatWindowsTime(t time.Time, c playerClock) string {
	p := c.pattern
	if p == "" {
		p = "HH:mm"
	}
	designator := c.am
	if t.Hour() >= 12 {
		designator = c.pm
	}
	if designator == "" {
		designator = "AM"
		if t.Hour() >= 12 {
			designator = "PM"
		}
	}
	h12 := t.Hour() % 12
	if h12 == 0 {
		h12 = 12
	}
	var b strings.Builder
	for i := 0; i < len(p); {
		ch := p[i]
		if ch == '\'' { // literal text up to the closing quote
			j := strings.IndexByte(p[i+1:], '\'')
			if j < 0 {
				b.WriteString(p[i+1:])
				break
			}
			b.WriteString(p[i+1 : i+1+j])
			i += j + 2
			continue
		}
		n := 1
		for i+n < len(p) && p[i+n] == ch {
			n++
		}
		num := func(v int) {
			if n >= 2 {
				fmt.Fprintf(&b, "%02d", v)
			} else {
				fmt.Fprintf(&b, "%d", v)
			}
		}
		switch ch {
		case 'h':
			num(h12)
		case 'H':
			num(t.Hour())
		case 'm':
			num(t.Minute())
		case 's':
			num(t.Second())
		case 't':
			if n >= 2 {
				b.WriteString(designator)
			} else {
				r := []rune(designator)
				b.WriteString(string(r[:1]))
			}
		default:
			b.WriteString(p[i : i+n])
		}
		i += n
	}
	return b.String()
}

// validClockText: a pattern or designator from the launcher; short, one
// line, no control characters (it is written into chat lines).
func validClockText(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// launcherClockHandler: POST /launcher/clock from the launcher, through
// the public gateway (Host mmog.greybox.sixfoot.live), with the player's
// token as "Authorization: Bearer".
func launcherClockHandler(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	pid, err := protocol.ExtractVerifiedPlayerPIDFromJWT(token, mmogJWTSecret(), "launcher", "dreadnought")
	pid = protocol.NormalizePlayerPID(pid)
	if err != nil || pid == "" {
		http.Error(w, `{"error":"sign in first"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		Offset  *int   `json:"utc_offset"`
		Pattern string `json:"pattern"`
		AM      string `json:"am"`
		PM      string `json:"pm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Offset == nil ||
		*req.Offset < -14*60 || *req.Offset > 14*60 ||
		!validClockText(req.Pattern, 32) || !validClockText(req.AM, 16) || !validClockText(req.PM, 16) {
		http.Error(w, `{"error":"send utc_offset (minutes, -840..840), pattern, am, pm"}`, http.StatusBadRequest)
		return
	}
	c := playerClock{offset: *req.Offset, pattern: req.Pattern, am: req.AM, pm: req.PM}
	if database := currentMmogPlayerStateDB(); database != nil {
		if _, err := database.Exec(`INSERT OR REPLACE INTO player_clock(user_id, utc_offset, pattern, am, pm, updated_at)
			VALUES(?,?,?,?,?,datetime('now'))`, pid, c.offset, c.pattern, c.am, c.pm); err != nil {
			logrus.WithError(err).Warn("launcher clock: could not save")
		}
	}
	loadPlayerClocks()
	playerClocks.mu.Lock()
	playerClocks.by[pid] = c
	playerClocks.mu.Unlock()
	logrus.WithFields(logrus.Fields{"utc_offset": c.offset, "pattern": c.pattern}).Info("launcher clock: saved")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
