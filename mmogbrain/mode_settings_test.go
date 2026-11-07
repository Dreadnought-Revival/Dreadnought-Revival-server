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
		`{"max_players":11}`:          http.StatusBadRequest,
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
