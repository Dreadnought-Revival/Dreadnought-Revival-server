package matchmaker

import (
	"os"
	"strings"
	"sync"
	"time"
)

// Live overrides of the queue switches, set from the admin dashboard
// (mmogbrain's mode_settings.go, issue #18). A nil override leaves the
// environment variable in charge, as before: DN_ESCALATION_QUEUE,
// DN_HAVOC, DN_CUSTOM_GAME_TILE.
var switches struct {
	mu              sync.RWMutex
	escalationQueue *string
	havoc           *bool
	customGame      *bool
}

// SetEscalationQueueOverride sets the queue whose matches run as Escalation
// ("" = none); nil goes back to DN_ESCALATION_QUEUE.
func SetEscalationQueueOverride(q *string) {
	switches.mu.Lock()
	defer switches.mu.Unlock()
	switches.escalationQueue = q
}

// SetHavocListedOverride lists or hides the Havoc tile; nil goes back to DN_HAVOC.
func SetHavocListedOverride(v *bool) {
	switches.mu.Lock()
	defer switches.mu.Unlock()
	switches.havoc = v
}

// SetCustomGameListedOverride lists or hides the Custom Game tile; nil goes
// back to DN_CUSTOM_GAME_TILE.
func SetCustomGameListedOverride(v *bool) {
	switches.mu.Lock()
	defer switches.mu.Unlock()
	switches.customGame = v
}

// EnvEscalationQueue, EnvHavocListed and EnvCustomGameListed are the
// environment's values, what a cleared override goes back to.
func EnvEscalationQueue() string { return strings.TrimSpace(os.Getenv("DN_ESCALATION_QUEUE")) }
func EnvHavocListed() bool       { return os.Getenv("DN_HAVOC") == "1" }
func EnvCustomGameListed() bool  { return os.Getenv("DN_CUSTOM_GAME_TILE") == "1" }

// EscalationQueue is the queue whose matches run as Escalation; "" = none.
func EscalationQueue() string {
	switches.mu.RLock()
	defer switches.mu.RUnlock()
	if switches.escalationQueue != nil {
		return *switches.escalationQueue
	}
	return EnvEscalationQueue()
}

// HavocListed reports whether the Havoc tile is listed.
func HavocListed() bool {
	switches.mu.RLock()
	defer switches.mu.RUnlock()
	if switches.havoc != nil {
		return *switches.havoc
	}
	return EnvHavocListed()
}

// CustomGameListed reports whether the Custom Game tile is listed.
func CustomGameListed() bool {
	switches.mu.RLock()
	defer switches.mu.RUnlock()
	if switches.customGame != nil {
		return *switches.customGame
	}
	return EnvCustomGameListed()
}

// RunsAs is the mode a match queued for this mode is hosted in: the
// substitution of a broken host mode (or DN_FORCE_GAME_MODE), then
// Escalation for its queue. PvE runs the event's own mode (not shown here).
func RunsAs(queued string) string {
	if IsEscalationQueue(queued) {
		return EscalationGameMode
	}
	return runnableGameMode(queued)
}

// Sizing is the match size cap (the fixed size with auto-scaling off) and
// how long an auto-scaled match waits for idle players.
func (m *Matchmaker) Sizing() (players int, maxWait time.Duration) {
	m.sizeMu.RLock()
	defer m.sizeMu.RUnlock()
	return m.PlayersPerMatch, m.MaxWait
}

// SetSizing changes them while the matchmaker runs (admin dashboard).
func (m *Matchmaker) SetSizing(players int, maxWait time.Duration) {
	m.sizeMu.Lock()
	defer m.sizeMu.Unlock()
	if players > 0 {
		m.PlayersPerMatch = players
	}
	if maxWait >= 0 {
		m.MaxWait = maxWait
	}
}

// AutoScaled reports whether the match size follows the players online
// (PlayersPerMatch is then a cap) rather than being fixed.
func (m *Matchmaker) AutoScaled() bool { return m.OnlinePlayers != nil }

// SetHold stops (true) or resumes (false) forming new matches. Players can
// still queue; running matches are not touched.
func (m *Matchmaker) SetHold(on bool) { m.hold.Store(on) }

// Held reports whether new matches are held back.
func (m *Matchmaker) Held() bool { return m.hold.Load() }
