package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// Safe restart from the admin dashboard (issue #20): wait until no battle
// server is running, then restart mmogbrain the way it is done by hand --
// stop it, and let scripts/start-services.sh start it again (which reads
// run/secrets.env afresh and starts only what is missing). Nothing
// supervises mmogbrain, so a detached helper does the second half: it
// waits for this process to exit and then runs the script. mmogbrain then
// shuts down through its normal SIGTERM path.
//
// While it waits, new matches can be held back (players can still queue),
// or a busy server might never get to zero. Players online are disconnected
// by the restart, as by a manual one; an optional Server chat line warns
// them when the wait starts.

const (
	restartCheckEvery = 10 * time.Second
	restartMaxWait    = 6 * time.Hour
	restartScript     = "scripts/start-services.sh"
)

// Swapped by tests.
var (
	restartRunningMatches = func() (int, error) {
		list, err := controlPlaneInstances()
		return len(list), err
	}
	restartLaunchHelper = launchRestartHelper
	restartExitSelf     = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }
	restartClock        = time.Now
)

var restartState struct {
	mu          sync.Mutex
	pending     bool
	stage       string // "waiting", "restarting"
	since       time.Time
	holdMatches bool
	announce    string
	lastCheck   time.Time
	running     int
	lastErr     string
	cancel      chan struct{}
}

// restartHelperDir is the repository root (mmogbrain runs there, see
// start-services.sh), or "" when the script is not there.
func restartHelperDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if st, err := os.Stat(filepath.Join(wd, restartScript)); err != nil || st.IsDir() {
		return ""
	}
	return wd
}

// launchRestartHelper starts the detached helper: wait for this process to
// exit, then run start-services.sh; its output goes to run/restart.log.
func launchRestartHelper() error {
	dir := restartHelperDir()
	if dir == "" {
		return errors.New(restartScript + " not found beside mmogbrain")
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "run", "restart.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	_, _ = fmt.Fprintf(logFile, "%s restart requested from the admin dashboard; waiting for mmogbrain %d to exit\n",
		time.Now().Format(time.RFC3339), os.Getpid())
	cmd := restartHelperCmd(dir, os.Getpid())
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// restartHelperCmd waits for process pid to exit, then runs the start
// script in dir, in a session of its own so it outlives mmogbrain.
func restartHelperCmd(dir string, pid int) *exec.Cmd {
	cmd := exec.Command("bash", "-c",
		`while kill -0 "$0" 2>/dev/null; do sleep 1; done; exec bash `+restartScript, strconv.Itoa(pid))
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// startRestartWait begins waiting for the battle servers to finish.
func startRestartWait(r *http.Request, hold bool, announce string) error {
	restartState.mu.Lock()
	if restartState.pending {
		restartState.mu.Unlock()
		return errors.New("a restart is already waiting")
	}
	cancel := make(chan struct{})
	restartState.pending, restartState.stage = true, "waiting"
	restartState.since, restartState.holdMatches, restartState.announce = restartClock(), hold, announce
	restartState.lastCheck, restartState.running, restartState.lastErr = time.Time{}, -1, ""
	restartState.cancel = cancel
	restartState.mu.Unlock()

	if hold && activeMatchmaker != nil {
		activeMatchmaker.SetHold(true)
	}
	if announce != "" {
		socialHubInstance.serverLine(announce)
	}
	adminAudit(currentMmogPlayerStateDB(), r, "restart requested", "", map[string]any{"hold_matches": hold, "announce": announce})
	go restartWaitLoop(cancel)
	return nil
}

func restartWaitLoop(cancel chan struct{}) {
	ticker := time.NewTicker(restartCheckEvery)
	defer ticker.Stop()
	for {
		if restartCheck() {
			return
		}
		select {
		case <-cancel:
			return
		case <-ticker.C:
		}
	}
}

// restartCheck looks once; it reports whether the wait is over.
func restartCheck() bool {
	n, err := restartRunningMatches()
	restartState.mu.Lock()
	if !restartState.pending || restartState.stage != "waiting" {
		restartState.mu.Unlock()
		return true
	}
	restartState.lastCheck = restartClock()
	restartState.running = n
	restartState.lastErr = ""
	if err != nil {
		restartState.lastErr = "control plane: " + err.Error()
		restartState.running = -1
	}
	waited := restartState.lastCheck.Sub(restartState.since)
	if err == nil && n == 0 {
		restartState.stage = "restarting"
		restartState.mu.Unlock()
		restartNow(waited)
		return true
	}
	if waited > restartMaxWait {
		restartState.pending, restartState.stage = false, ""
		restartState.mu.Unlock()
		if activeMatchmaker != nil {
			activeMatchmaker.SetHold(false)
		}
		adminAuditSystem("restart gave up", map[string]any{"waited": waited.Round(time.Second).String(), "running": n})
		return true
	}
	restartState.mu.Unlock()
	return false
}

func restartNow(waited time.Duration) {
	if err := restartLaunchHelper(); err != nil {
		restartState.mu.Lock()
		restartState.pending, restartState.stage, restartState.lastErr = false, "", "restart failed: "+err.Error()
		restartState.mu.Unlock()
		if activeMatchmaker != nil {
			activeMatchmaker.SetHold(false)
		}
		adminAuditSystem("restart failed", map[string]any{"error": err.Error()})
		return
	}
	adminAuditSystem("restart", map[string]any{"waited": waited.Round(time.Second).String()})
	logrus.WithField("waited", waited.Round(time.Second)).Warn("restart: no battle server running; mmogbrain stops now and start-services.sh starts it again")
	restartExitSelf()
}

// adminAuditSystem records an action the server took on an admin's behalf.
func adminAuditSystem(action string, details map[string]any) {
	text := ""
	if b, err := json.Marshal(details); err == nil {
		text = string(b)
	}
	logrus.WithFields(logrus.Fields{"action": action, "details": text}).Warn("admin: action")
	if database := currentMmogPlayerStateDB(); database != nil {
		_, _ = database.Exec(`INSERT INTO admin_audit(actor,action,target,details) VALUES('server',?,'',?)`, action, text)
	}
}

func cancelRestartWait() bool {
	restartState.mu.Lock()
	if !restartState.pending || restartState.stage != "waiting" {
		restartState.mu.Unlock()
		return false
	}
	restartState.pending, restartState.stage = false, ""
	close(restartState.cancel)
	hold := restartState.holdMatches
	restartState.mu.Unlock()
	if hold && activeMatchmaker != nil {
		activeMatchmaker.SetHold(false)
	}
	return true
}

func registerAdminRestart(api *mux.Router) {
	api.HandleFunc("/restart", adminAPIRestartStatus).Methods(http.MethodGet)
	api.HandleFunc("/restart", adminAPIRestart).Methods(http.MethodPost)
	api.HandleFunc("/restart/cancel", adminAPIRestartCancel).Methods(http.MethodPost)
}

func restartStatusJSON() map[string]any {
	restartState.mu.Lock()
	defer restartState.mu.Unlock()
	out := map[string]any{
		"available": restartHelperDir() != "",
		"pending":   restartState.pending,
		"stage":     restartState.stage,
		"error":     restartState.lastErr,
		"online":    len(socialHubInstance.onlinePlayerIDs()),
	}
	if restartState.pending {
		out["since"] = restartState.since.UTC().Format("2006-01-02 15:04:05")
		out["waited_s"] = int(restartClock().Sub(restartState.since) / time.Second)
		out["hold_matches"] = restartState.holdMatches
		out["running"] = restartState.running
		if !restartState.lastCheck.IsZero() {
			out["last_check"] = restartState.lastCheck.UTC().Format("2006-01-02 15:04:05")
		}
	}
	return out
}

func adminAPIRestartStatus(w http.ResponseWriter, _ *http.Request) {
	writeAdminJSON(w, restartStatusJSON())
}

func adminAPIRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HoldMatches bool   `json:"hold_matches"`
		Announce    string `json:"announce"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid body")
		return
	}
	announce, why := cleanServerMessage(req.Announce)
	if why != "" {
		writeAdminError(w, http.StatusBadRequest, why)
		return
	}
	if restartHelperDir() == "" {
		writeAdminError(w, http.StatusServiceUnavailable, restartScript+" is not beside mmogbrain; restart it by hand")
		return
	}
	if err := startRestartWait(r, req.HoldMatches, announce); err != nil {
		writeAdminError(w, http.StatusConflict, err.Error())
		return
	}
	writeAdminJSON(w, restartStatusJSON())
}

func adminAPIRestartCancel(w http.ResponseWriter, r *http.Request) {
	if !cancelRestartWait() {
		writeAdminError(w, http.StatusConflict, "no restart is waiting")
		return
	}
	adminAudit(currentMmogPlayerStateDB(), r, "restart cancelled", "", nil)
	writeAdminJSON(w, restartStatusJSON())
}
