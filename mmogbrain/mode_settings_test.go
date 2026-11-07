package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/sirupsen/logrus"
)

func clearModeOverrides() {
	matchmaker.SetEscalationQueueOverride(nil)
	matchmaker.SetHavocListedOverride(nil)
	matchmaker.SetCustomGameListedOverride(nil)
}

func listsMode(name string) bool {
	for _, m := range matchmaker.GameModeConfigs() {
		if m.Name == name {
			return true
		}
	}
	return false
}

// The Game modes tab changes the queue switches live, stores them, reads
// them back at startup, and hands them back to the environment on reset.
func TestAdminModeSettings(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	t.Setenv("DN_HAVOC", "1")
	t.Setenv("DN_ESCALATION_QUEUE", "")
	clearModeOverrides()
	t.Cleanup(clearModeOverrides)

	log := logrus.New()
	mm := matchmaker.New(database, log, "http://127.0.0.1:1", "", 2)
	mm.SetSizing(10, 60*time.Second)
	old := activeMatchmaker
	activeMatchmaker = mm
	t.Cleanup(func() { activeMatchmaker = old })
	applyModeSettings(database, mm, log) // nothing stored: environment values

	r := adminTestRouter()
	if rec := adminPost(r, "/admin/api/modes", "", `{"havoc":false}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}
	if !listsMode("HAVOC") || matchmaker.IsEscalationQueue("BC") {
		t.Fatal("environment defaults not in force")
	}

	for body, want := range map[string]int{
		`{"escalation_queue":"Nope"}`: http.StatusBadRequest,
		`{"max_players":121}`:          http.StatusBadRequest, // two teams of MaxTeamSize
		`{"max_players":0}`:           http.StatusBadRequest,
		`{"max_wait_s":601}`:          http.StatusBadRequest,
		`{"reset":["everything"]}`:    http.StatusBadRequest,
	} {
		if rec := adminPost(r, "/admin/api/modes", "test-admin-key", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}

	rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"escalation_queue":"BC","havoc":false,"max_players":6,"max_wait_s":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Escalation adminSwitch      `json:"escalation_queue"`
		Queues     []map[string]any `json:"queues"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Escalation.Value != "BC" || !got.Escalation.Dashboard {
		t.Errorf("escalation in reply: %+v", got.Escalation)
	}
	if !matchmaker.IsEscalationQueue("BC") || matchmaker.RunsAs("BC") != matchmaker.EscalationGameMode {
		t.Error("BC does not run as Escalation")
	}
	if listsMode("HAVOC") {
		t.Error("Havoc still listed")
	}
	if p, w := mm.Sizing(); p != 6 || w != 30*time.Second {
		t.Errorf("sizing %d / %v, want 6 / 30s", p, w)
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='game modes'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d audit rows, want 1", audits)
	}

	// A restart reads them back.
	clearModeOverrides()
	mm.SetSizing(10, 60*time.Second)
	applyModeSettings(database, mm, log)
	if !matchmaker.IsEscalationQueue("BC") || listsMode("HAVOC") {
		t.Error("stored switches not applied at startup")
	}
	if p, w := mm.Sizing(); p != 6 || w != 30*time.Second {
		t.Errorf("sizing after restart %d / %v, want 6 / 30s", p, w)
	}

	// Back to the environment.
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"reset":["escalation_queue","havoc","max_players","max_wait_s"]}`); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if matchmaker.IsEscalationQueue("BC") || !listsMode("HAVOC") {
		t.Error("reset did not restore the environment values")
	}
	if p, w := mm.Sizing(); p != 10 || w != 60*time.Second {
		t.Errorf("sizing after reset %d / %v, want 10 / 60s", p, w)
	}
	var left int
	_ = database.QueryRow(`SELECT COUNT(*) FROM server_flags WHERE name LIKE 'mode_%' OR name LIKE 'match_%'`).Scan(&left)
	if left != 0 {
		t.Errorf("%d switches still stored after the reset", left)
	}

	// "" turns Escalation off even when the environment names a queue.
	t.Setenv("DN_ESCALATION_QUEUE", "BC")
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"escalation_queue":""}`); rec.Code != http.StatusOK {
		t.Fatalf("off: %d", rec.Code)
	}
	if matchmaker.IsEscalationQueue("BC") {
		t.Error("Escalation still on BC after switching it off")
	}
}

// Team sizes per queue: today's limits by default, changed per mode, capped
// by the overall players-per-match, stored and read back, reset.
func TestAdminTeamSizes(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	log := logrus.New()
	mm := matchmaker.New(database, log, "http://127.0.0.1:1", "", 2)
	mm.SetSizing(10, 60*time.Second)
	old := activeMatchmaker
	activeMatchmaker = mm
	t.Cleanup(func() { activeMatchmaker = old })
	for _, q := range []string{"TDM", "BC", "Havoc"} {
		matchmaker.SetTeamSizeOverride(q, nil)
	}
	t.Cleanup(func() {
		for _, q := range []string{"TDM", "BC", "Havoc"} {
			matchmaker.SetTeamSizeOverride(q, nil)
		}
	})

	// The defaults are what the queues took before (10-player cap; PvE and
	// Havoc one team of 5).
	for q, want := range map[string]int{"TDM": 10, "TE": 10, "BC": 10, "Onslaught": 10, "Havoc": 5, "PVE": 5} {
		if got := mm.MaxPlayersFor(q); got != want {
			t.Errorf("default max players for %s = %d, want %d", q, got, want)
		}
	}
	if matchmaker.TeamsIn("TDM") != 2 || matchmaker.TeamsIn("BC") != 1 {
		t.Fatal("team counts wrong")
	}

	r := adminTestRouter()
	for body, want := range map[string]int{
		`{"team_sizes":{"TDM":61}}`: http.StatusBadRequest,
		`{"team_sizes":{"TDM":0}}`:  http.StatusBadRequest,
		`{"team_sizes":{"Nope":5}}`: http.StatusBadRequest,
		`{"max_players":121}`:        http.StatusBadRequest,
	} {
		if rec := adminPost(r, "/admin/api/modes", "test-admin-key", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"team_sizes":{"TDM":8,"BC":3,"Havoc":4}}`); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	// 8 a side is 16, but the overall cap is still 10.
	if got := mm.MaxPlayersFor("TDM"); got != 10 {
		t.Errorf("TDM 8 a side under a cap of 10: %d, want 10", got)
	}
	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"max_players":20}`); rec.Code != http.StatusOK {
		t.Fatalf("cap 20: %d", rec.Code)
	}
	for q, want := range map[string]int{"TDM": 16, "BC": 3, "Havoc": 4, "TE": 10} {
		if got := mm.MaxPlayersFor(q); got != want {
			t.Errorf("max players for %s = %d, want %d", q, got, want)
		}
	}

	// A restart reads them back.
	for _, q := range []string{"TDM", "BC", "Havoc"} {
		matchmaker.SetTeamSizeOverride(q, nil)
	}
	applyModeSettings(database, mm, log)
	if n, set := matchmaker.TeamSizeFor("TDM"); n != 8 || !set {
		t.Errorf("after a restart TDM team size %d (set %v), want 8", n, set)
	}

	if rec := adminPost(r, "/admin/api/modes", "test-admin-key", `{"team_sizes":{"TDM":null}}`); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d", rec.Code)
	}
	if n, set := matchmaker.TeamSizeFor("TDM"); n != 5 || set {
		t.Errorf("after reset TDM team size %d (set %v), want default 5", n, set)
	}
}
