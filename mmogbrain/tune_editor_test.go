package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func useCleanTuneEditor(t *testing.T) {
	t.Helper()
	reset := func() {
		tuneEditor.once = sync.Once{}
		setLiveTuneVersion(0, map[string]tuneChange{})
		tuneEditor.once.Do(func() {})
	}
	reset()
	t.Cleanup(func() { reset(); tuneEditor.once = sync.Once{} })
}

type tuneStatus struct {
	Live    string        `json:"live"`
	LiveID  int64         `json:"live_id"`
	Pending int           `json:"pending"`
	Diff    []tuneDiffRow `json:"diff"`
}

func tunePost(t *testing.T, r http.Handler, path, body string, want int) tuneStatus {
	t.Helper()
	rec := adminPost(r, "/admin/api/tune"+path, "test-admin-key", body)
	if rec.Code != want {
		t.Fatalf("%s %s: %d, want %d (%s)", path, body, rec.Code, want, rec.Body.String())
	}
	var st tuneStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	return st
}

// replyDamage is m_damageHigh of a weapon row in the reply an account gets.
func replyDamage(t *testing.T, pid, row string) (version, damage string) {
	t.Helper()
	ret := tuneDocumentOf(t, buildMmogTuneReplyFor(pid))
	version = ret.child("MetaData").child("Version").str
	for _, r := range ret.child("WeaponsTune").children {
		if r.child("Name").str == row {
			return version, r.child("m_damageHigh").str
		}
	}
	t.Fatalf("no row %s in the reply", row)
	return
}

// Edit a weapon, publish, see it in the apply account's reply under a new
// version; discard, restore and the game's own values; survive a restart.
func TestAdminTuneEditor(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	useCleanTuneEditor(t)
	t.Setenv("DN_TUNE_SEND", "")
	if err := seedMmogPlayerState(database, adminTestPID); err != nil {
		t.Fatal(err)
	}
	loadTuneTestAccounts()
	tuneTestAccounts.mu.Lock()
	tuneTestAccounts.mode = map[string]string{adminTestPID: "apply"}
	tuneTestAccounts.mu.Unlock()
	t.Cleanup(func() {
		tuneTestAccounts.mu.Lock()
		tuneTestAccounts.mode = map[string]string{}
		tuneTestAccounts.mu.Unlock()
	})
	r := adminTestRouter()
	const weapon = "WP_DreadnoughtMPri01_weapon01_T1_BP"
	orig := tuneOriginal()["WeaponsTune"][weapon]["m_damageHigh"]
	if v, d := replyDamage(t, adminTestPID, weapon); v != "1.0.0" || d != orig.(json.Number).String() {
		t.Fatalf("before any edit: version %s damage %s, want 1.0.0 and the game's %v", v, d, orig)
	}

	if rec := adminPost(r, "/admin/api/tune/draft", "", `{"changes":[]}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}
	for _, body := range []string{
		`{"changes":[{"table":"AbilitiesTune","row":"x","field":"m_coolDown","value":1}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"nope","field":"m_damageHigh","value":1}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"` + weapon + `","field":"m_nope","value":1}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"` + weapon + `","field":"m_damageHigh","value":"lots"}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"` + weapon + `","field":"m_damageHigh","value":1.5}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"` + weapon + `","field":"m_ignoreShields","value":1}]}`,
		`{"changes":[{"table":"WeaponsTune","row":"` + weapon + `","field":"Name","value":"x"}]}`,
	} {
		tunePost(t, r, "/draft", body, http.StatusBadRequest)
	}

	st := tunePost(t, r, "/draft", `{"changes":[{"table":"WeaponsTune","row":"`+weapon+`","field":"m_damageHigh","value":999},
		{"table":"WeaponsTune","row":"`+weapon+`","field":"m_ignoreShields","value":true}]}`, http.StatusOK)
	if st.Pending != 2 || len(st.Diff) != 2 {
		t.Fatalf("draft: %+v", st)
	}
	if _, d := replyDamage(t, adminTestPID, weapon); d != orig.(json.Number).String() {
		t.Fatalf("a draft reached the reply: damage %s", d)
	}

	st = tunePost(t, r, "/publish", `{"note":"test buff"}`, http.StatusOK)
	if st.Live != "1.0.1" || st.Pending != 0 {
		t.Fatalf("after publish: %+v", st)
	}
	if v, d := replyDamage(t, adminTestPID, weapon); v != "1.0.1" || d != "999" {
		t.Errorf("apply reply: version %s damage %s, want 1.0.1 and 999", v, d)
	}
	ret := tuneDocumentOf(t, buildMmogTuneReplyFor(adminTestPID))
	for _, row := range ret.child("WeaponsTune").children {
		if row.child("Name").str == weapon && row.child("m_ignoreShields").str != "True" {
			t.Errorf("bool change sent as %q, want True", row.child("m_ignoreShields").str)
		}
	}
	tunePost(t, r, "/publish", `{}`, http.StatusConflict)

	// A draft change, discarded.
	tunePost(t, r, "/draft", `{"changes":[{"table":"WeaponsTune","row":"`+weapon+`","field":"m_damageHigh","value":5}]}`, http.StatusOK)
	if st = tunePost(t, r, "/discard", `{}`, http.StatusOK); st.Pending != 0 {
		t.Errorf("after discard %d pending", st.Pending)
	}

	// Back to the game's own values, then version 1 again.
	tunePost(t, r, "/restore", `{"id":0}`, http.StatusOK)
	if st = tunePost(t, r, "/publish", `{"note":"defaults"}`, http.StatusOK); st.Live != "1.0.2" {
		t.Fatalf("defaults published as %s", st.Live)
	}
	if v, d := replyDamage(t, adminTestPID, weapon); v != "1.0.2" || d != orig.(json.Number).String() {
		t.Errorf("after restoring defaults: version %s damage %s", v, d)
	}
	tunePost(t, r, "/restore", `{"id":1}`, http.StatusOK)
	tunePost(t, r, "/publish", `{"note":"rollback to 1"}`, http.StatusOK)
	tunePost(t, r, "/restore", `{"id":42}`, http.StatusNotFound)

	// A restart reads the live version back.
	setLiveTuneVersion(0, map[string]tuneChange{})
	tuneEditor.once = sync.Once{}
	if v, d := replyDamage(t, adminTestPID, weapon); v != "1.0.3" || d != "999" {
		t.Errorf("after a restart: version %s damage %s, want 1.0.3 and 999", v, d)
	}

	// Only apply accounts get it: a check account still gets 0.0.0.
	tuneTestAccounts.mu.Lock()
	tuneTestAccounts.mode[adminTestPID] = "check"
	tuneTestAccounts.mu.Unlock()
	if v, _ := replyDamage(t, adminTestPID, weapon); v != tuneCheckVersion {
		t.Errorf("check account got version %s", v)
	}

	var audits int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action LIKE 'tune %'`).Scan(&audits)
	if audits != 6 { // 3 publishes, 1 discard, 2 restores (the 404 one is not recorded)
		t.Errorf("%d tune audit rows, want 6", audits)
	}

	// The table browser.
	rec := adminGet(r, "/admin/api/tune/table/WeaponsTune?q=DreadnoughtMPri01_weapon01_T1", "test-admin-key")
	var tb struct {
		Total int `json:"total"`
		Rows  []struct {
			Name   string `json:"name"`
			Fields []struct {
				Field     string `json:"field"`
				Published any    `json:"published"`
			} `json:"fields"`
		} `json:"rows"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &tb) != nil || tb.Total == 0 {
		t.Fatalf("table browser: %s", rec.Body.String())
	}
	found := false
	for _, row := range tb.Rows {
		for _, f := range row.Fields {
			if row.Name == weapon && f.Field == "m_damageHigh" && f.Published != nil {
				found = true
			}
		}
	}
	if !found {
		t.Error("the browser does not show the published value")
	}
}
