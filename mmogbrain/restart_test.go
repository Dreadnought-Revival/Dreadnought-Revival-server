package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/matchmaker"
	"github.com/sirupsen/logrus"
)

// fakeRestart replaces the control plane, the helper and the exit for one
// test, and runs it from a directory that has scripts/start-services.sh.
func fakeRestart(t *testing.T, running *atomic.Int32, helperErr error) (launched, exited *atomic.Bool) {
	t.Helper()
	launched, exited = new(atomic.Bool), new(atomic.Bool)
	savedRunning, savedLaunch, savedExit := restartRunningMatches, restartLaunchHelper, restartExitSelf
	restartRunningMatches = func() (int, error) { return int(running.Load()), nil }
	restartLaunchHelper = func() error { launched.Store(true); return helperErr }
	restartExitSelf = func() { exited.Store(true) }
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, restartScript), []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
		restartRunningMatches, restartLaunchHelper, restartExitSelf = savedRunning, savedLaunch, savedExit
		cancelRestartWait()
		restartState.mu.Lock()
		restartState.pending, restartState.stage = false, ""
		restartState.mu.Unlock()
	})
	return launched, exited
}

// The restart waits while matches run (holding new ones back), then starts
// the helper and stops mmogbrain; a cancel releases the hold.
func TestAdminSafeRestart(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	hub := socialTestHub(t)
	saved := socialHubInstance
	socialHubInstance = hub
	t.Cleanup(func() { socialHubInstance = saved })
	mm := matchmaker.New(database, logrus.New(), "http://127.0.0.1:1", "", 2)
	savedMM := activeMatchmaker
	activeMatchmaker = mm
	t.Cleanup(func() { activeMatchmaker = savedMM })

	var running atomic.Int32
	running.Store(2)
	launched, exited := fakeRestart(t, &running, nil)
	r := adminTestRouter()

	if rec := adminPost(r, "/admin/api/restart", "", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("without the key: %d", rec.Code)
	}
	rec := adminPost(r, "/admin/api/restart", "test-admin-key", `{"hold_matches":true,"announce":"Restart after the current matches."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	if !mm.Held() {
		t.Error("new matches not held")
	}
	if rec := adminPost(r, "/admin/api/restart", "test-admin-key", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("second restart request: %d, want 409", rec.Code)
	}

	// Matches still running: nothing happens.
	if restartCheck() || launched.Load() || exited.Load() {
		t.Fatal("restarted while matches ran")
	}
	var st struct {
		Pending bool   `json:"pending"`
		Stage   string `json:"stage"`
		Running int    `json:"running"`
	}
	_ = json.Unmarshal(adminGet(r, "/admin/api/restart", "test-admin-key").Body.Bytes(), &st)
	if !st.Pending || st.Stage != "waiting" || st.Running != 2 {
		t.Errorf("status while waiting: %+v", st)
	}

	// The last match ends: the helper starts and mmogbrain stops.
	running.Store(0)
	restartCheck() // the wait loop may have got there first
	if !launched.Load() || !exited.Load() {
		t.Fatalf("idle: helper %v, exit %v", launched.Load(), exited.Load())
	}
	var actions []string
	rows, _ := database.Query(`SELECT action FROM admin_audit ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	_ = rows.Close()
	if len(actions) != 2 || actions[0] != "restart requested" || actions[1] != "restart" {
		t.Errorf("audit %v, want restart requested, restart", actions)
	}
}

func TestAdminSafeRestartCancelAndFailure(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	_ = database
	mm := matchmaker.New(database, logrus.New(), "http://127.0.0.1:1", "", 2)
	savedMM := activeMatchmaker
	activeMatchmaker = mm
	t.Cleanup(func() { activeMatchmaker = savedMM })
	var running atomic.Int32
	running.Store(1)
	launched, exited := fakeRestart(t, &running, errors.New("no bash"))
	r := adminTestRouter()

	if rec := adminPost(r, "/admin/api/restart", "test-admin-key", `{"hold_matches":true}`); rec.Code != http.StatusOK {
		t.Fatalf("restart: %d", rec.Code)
	}
	if rec := adminPost(r, "/admin/api/restart/cancel", "test-admin-key", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d", rec.Code)
	}
	if mm.Held() {
		t.Error("cancel left new matches held")
	}
	if rec := adminPost(r, "/admin/api/restart/cancel", "test-admin-key", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("cancel with nothing waiting: %d, want 409", rec.Code)
	}

	// The helper cannot start: mmogbrain keeps running, matches resume.
	if rec := adminPost(r, "/admin/api/restart", "test-admin-key", `{"hold_matches":true}`); rec.Code != http.StatusOK {
		t.Fatalf("restart again: %d", rec.Code)
	}
	running.Store(0)
	restartCheck()
	if !launched.Load() || exited.Load() || mm.Held() {
		t.Errorf("failed helper: launched %v, exited %v, held %v; want true/false/false", launched.Load(), exited.Load(), mm.Held())
	}
	if st := restartStatusJSON(); st["pending"] != false || st["error"] == "" {
		t.Errorf("status after a failed restart: %v", st)
	}
}

// The helper itself: it waits for the process to exit, then runs the start
// script (here a stand-in that leaves a marker).
func TestRestartHelperWaitsThenStarts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, restartScript), []byte("echo started > marker\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := exec.Command("sleep", "0.5")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = victim.Wait() }()
	helper := restartHelperCmd(dir, victim.Process.Pid)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker")
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the script ran before the process exited")
	}
	_ = helper.Wait()
	if b, err := os.ReadFile(marker); err != nil || string(b) != "started\n" {
		t.Fatalf("script did not run after the exit: %q %v", b, err)
	}
}
