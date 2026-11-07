package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /battle/tune hands the battle-server mod the live version's changes, in
// the host's table names, or "none".
func TestBattleTuneText(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	useCleanTuneEditor(t)
	tuneBattleServers.loaded.Store(true)
	tuneBattleServers.off.Store(false)
	t.Cleanup(func() { tuneBattleServers.loaded.Store(false); tuneBattleServers.off.Store(false) })

	if got := battleTuneText(); got != "version\tnone\n" {
		t.Fatalf("nothing published: %q", got)
	}
	setLiveTuneVersion(3, map[string]tuneChange{
		"a": {Table: "WeaponsTune", Row: "WP_A", Field: "m_damageHigh", Value: json.Number("999")},
		"b": {Table: "WeaponsTune", Row: "WP_A", Field: "m_ignoreShields", Value: true},
		"c": {Table: "ProjectilesTune", Row: "PR_B", Field: "m_gravityScale", Value: json.Number("0.25")},
		"d": {Table: "OfficersTune", Row: "OF_C", Field: "m_isPerkFeat", Value: false},
	})
	got := battleTuneText()
	for _, want := range []string{
		"version\t1.0.3\n",
		"DN_Weapons_OTS_DT\tWP_A\tm_damageHigh\tn\t999\n",
		"DN_Weapons_OTS_DT\tWP_A\tm_ignoreShields\tb\ttrue\n",
		"DN_Projectile_OTS_DT\tPR_B\tm_gravityScale\tn\t0.25\n",
		"DN_Officers_OTS_DT\tOF_C\tm_isPerkFeat\tb\tfalse\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "version\t") {
		t.Errorf("first line is not the version: %q", got)
	}

	// Switched off on the dashboard: the game's values.
	tuneBattleServers.off.Store(true)
	if got := battleTuneText(); got != "version\tnone\n" {
		t.Errorf("switched off: %q", got)
	}
	tuneBattleServers.off.Store(false)

	// Loopback only.
	req := httptest.NewRequest(http.MethodGet, "/battle/tune", nil)
	req.RemoteAddr = "203.0.113.5:4000"
	rec := httptest.NewRecorder()
	battleTuneHandler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("from outside: %d, want 403", rec.Code)
	}
	req.RemoteAddr = "127.0.0.1:4000"
	rec = httptest.NewRecorder()
	battleTuneHandler(rec, req)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "version\t1.0.3") {
		t.Errorf("loopback: %d %q", rec.Code, rec.Body.String())
	}
}

// The dashboard switch is saved and audited.
func TestAdminTuneBattleServersSwitch(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanTuneEditor(t)
	t.Cleanup(func() { tuneBattleServers.loaded.Store(false); tuneBattleServers.off.Store(false) })
	r := adminTestRouter()
	if rec := adminPost(r, "/admin/api/tune/battle", "test-admin-key", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no value: %d", rec.Code)
	}
	if rec := adminPost(r, "/admin/api/tune/battle", "test-admin-key", `{"on":false}`); rec.Code != http.StatusOK {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	if tuneOnBattleServers() {
		t.Error("still on")
	}
	tuneBattleServers.loaded.Store(false) // a restart reads it back
	if tuneOnBattleServers() {
		t.Error("on again after a reload")
	}
	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='tune battle servers'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d audit rows", audits)
	}
}
