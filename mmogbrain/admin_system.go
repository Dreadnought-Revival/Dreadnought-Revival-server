package main

// System page backend (GET /admin/api/system, POST /admin/api/system/backup).
// Every part is optional and fails soft: a part that cannot be read comes
// back with an "error" text instead of failing the whole reply. Nothing here
// reads a key file, prints an address, or exposes a secret.

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

func init() { adminRegisterAPI(registerAdminSystem) }

func registerAdminSystem(api *mux.Router) {
	api.HandleFunc("/system", adminAPISystem).Methods(http.MethodGet)
	api.HandleFunc("/system/backup", adminAPISystemBackup).Methods(http.MethodPost)
}

const (
	systemBackupScript  = "scripts/backup.sh" // written by scripts/backup.sh itself: backups/<UTC stamp>.tar.gz + backups/backup.log (cron)
	systemBackupTimeout = 5 * time.Minute
)

// Swapped by tests.
var (
	// systemRootDir is the repository root (where mmogbrain runs), or "" when
	// scripts/backup.sh is not beside the binary -- found the way restart.go
	// finds start-services.sh.
	systemRootDir = func() string {
		wd, err := os.Getwd()
		if err != nil {
			return ""
		}
		if st, err := os.Stat(filepath.Join(wd, systemBackupScript)); err != nil || st.IsDir() {
			return ""
		}
		return wd
	}
	// systemWorkDir is the root used for relative paths (certs/, run/, backups/).
	systemWorkDir = func() string {
		wd, _ := os.Getwd()
		return wd
	}
	systemBackupMu sync.Mutex
)

// systemSecretName reports whether an environment variable name could hold a
// secret; such names are never read.
func systemSecretName(name string) bool {
	u := strings.ToUpper(name)
	for _, bad := range []string{"KEY", "SECRET", "PASSWORD", "TOKEN", "JWT", "PASS"} {
		if strings.Contains(u, bad) {
			return true
		}
	}
	return false
}

// systemConfigValues are shown verbatim: numbers, durations, on/off switches
// and queue/mode names -- none holds an address, domain or credential.
var systemConfigValues = []string{
	"PLAYERS_PER_MATCH",        // players that fill a match (start-services.sh)
	"DN_MATCH_MAX_PLAYERS",     // autoscale cap, a number
	"DN_MATCH_MAX_WAIT",        // autoscale wait, a duration
	"DN_MATCH_AUTOSCALE",       // "0" = fixed size
	"DN_MATCH_BACKFILL_WINDOW", // a duration
	"DN_ESCALATION_QUEUE",      // a queue/mode code such as BC
	"DN_TLS_SHA384",            // off / all
	"DN_CHAT_FILTER",           // "0" = off
	"DN_DAILY_CONTRACTS",       // "0" = off
	"DN_LOADOUT_OWNERSHIP",     // warn / enforce
	"DN_LOADOUT_B",             // on/off switch
	"DN_MARKET_CAMPAIGNS",      // on/off switch
	"DN_CATALOG_GZIP",          // on/off switch
	"DN_SERVER_CHAT",           // on/off switch
	"DN_REWARD_EVENT_CREDITS",  // a number
	"DN_REWARD_EVENT_XP",       // a number
	"DN_REWARD_RIBBON_CREDITS", // a number
}

// systemConfigPresence only say "set" / "not set": addresses and domains.
var systemConfigPresence = []string{"PUBLIC_HOST", "SERVER_IP", "AUTH_URL", "GAME_MGR_URL", "FIRMAMENT_CERT", "GATEWAY_CERT", "DN_TLS_SHA384_FILE", "DN_CHAT_PROFANITY_FILE", "DB_PATH"}

type systemConfigEntry struct {
	Name  string `json:"name"`
	Set   bool   `json:"set"`
	Value string `json:"value,omitempty"`
}

func systemConfig() []systemConfigEntry {
	out := []systemConfigEntry{}
	for _, n := range systemConfigValues {
		if systemSecretName(n) {
			continue
		}
		v := os.Getenv(n)
		if len(v) > 40 {
			v = v[:40]
		}
		out = append(out, systemConfigEntry{Name: n, Set: v != "", Value: v})
	}
	for _, n := range systemConfigPresence {
		if systemSecretName(n) {
			continue
		}
		e := systemConfigEntry{Name: n, Set: os.Getenv(n) != ""}
		// The control plane URL is shown only when it is a loopback host:port
		// (the default http://127.0.0.1:8085); anything else stays "set".
		if n == "GAME_MGR_URL" && e.Set {
			if u, err := url.Parse(os.Getenv(n)); err == nil {
				if h := u.Hostname(); h == "localhost" || (net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()) {
					e.Value = u.Host
				}
			}
		}
		out = append(out, e)
	}
	return out
}

func systemReadFile(path string) (string, bool) {
	b, err := os.ReadFile(path) // /proc files only: tiny
	return string(b), err == nil
}

// systemHost reads /proc and statfs; empty on other systems.
func systemHost(dbFile string) map[string]any {
	out := map[string]any{"os": runtime.GOOS, "cpus": runtime.NumCPU()}
	if runtime.GOOS != "linux" {
		return out
	}
	if s, ok := systemReadFile("/proc/loadavg"); ok {
		if f := strings.Fields(s); len(f) >= 3 {
			var l []float64
			for _, x := range f[:3] {
				v, _ := strconv.ParseFloat(x, 64)
				l = append(l, v)
			}
			out["load"] = l
		}
	}
	if s, ok := systemReadFile("/proc/uptime"); ok {
		if f := strings.Fields(s); len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			out["uptime_s"] = int64(v)
		}
	}
	if s, ok := systemReadFile("/proc/meminfo"); ok {
		var total, avail int64
		for _, line := range strings.Split(s, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = kb * 1024
			case "MemAvailable:":
				avail = kb * 1024
			}
		}
		out["mem_total"], out["mem_available"] = total, avail
	}
	disks := []map[string]any{}
	add := func(label, path string) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			disks = append(disks, map[string]any{"label": label, "error": "not readable"})
			return
		}
		disks = append(disks, map[string]any{"label": label, "total": int64(st.Blocks) * int64(st.Bsize), "free": int64(st.Bavail) * int64(st.Bsize)})
	}
	if dbFile != "" {
		add("Database disk", filepath.Dir(dbFile))
	}
	add("run/ disk", filepath.Join(systemWorkDir(), "run"))
	out["disks"] = disks
	return out
}

func systemProcess() map[string]any {
	out := map[string]any{
		"uptime_s":   int64(time.Since(adminDash.startedAt).Seconds()),
		"go_version": runtime.Version(),
		"goroutines": runtime.NumGoroutine(),
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out["heap_inuse"], out["gc_count"] = int64(m.HeapInuse), m.NumGC
	if bi, ok := debug.ReadBuildInfo(); ok {
		b := map[string]string{}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision", "vcs.time", "vcs.modified":
				b[strings.TrimPrefix(s.Key, "vcs.")] = s.Value
			}
		}
		out["build"] = b
	}
	if runtime.GOOS == "linux" {
		if ents, err := os.ReadDir("/proc/self/fd"); err == nil {
			out["open_fds"] = len(ents)
		}
	}
	return out
}

// systemCountTables is the fixed allow-list of tables counted (names are
// never taken from a request).
var systemCountTables = []string{"player_state", "matches", "battle_results", "battle_result_stats", "queue_entries",
	"chat_messages", "client_reports", "client_telemetry", "player_purchases", "player_ribbons", "fair_play_reviews",
	"admin_audit", "admin_bans", "admin_metrics_samples", "havoc_runs"}

type systemTableCount struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
	Error string `json:"error,omitempty"`
}

// systemDatabase: sequential single-row queries, no rows open (one connection).
func systemDatabase(database *sql.DB) (map[string]any, string) {
	out := map[string]any{}
	if database == nil {
		out["error"] = "no database open"
		return out, ""
	}
	file := ""
	var seq int
	var name string
	if err := database.QueryRow(`SELECT seq, name, file FROM pragma_database_list WHERE name='main'`).Scan(&seq, &name, &file); err != nil {
		out["error"] = "database path unknown: " + err.Error()
	}
	if file != "" {
		out["file"] = filepath.Base(file)
		files := map[string]int64{}
		for k, suffix := range map[string]string{"db": "", "wal": "-wal", "shm": "-shm"} {
			if st, err := os.Stat(file + suffix); err == nil {
				files[k] = st.Size()
			}
		}
		out["sizes"] = files
	}
	var v sql.NullInt64
	if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&v); err == nil && v.Valid {
		out["schema_version"] = v.Int64
	}
	counts := []systemTableCount{}
	for _, t := range systemCountTables {
		c := systemTableCount{Table: t}
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + t).Scan(&c.Rows); err != nil { // t from the fixed list above
			c.Error = "unreadable"
		}
		counts = append(counts, c)
	}
	out["tables"] = counts
	return out, file
}

func systemBackupDir(root string) string {
	if d := os.Getenv("BACKUP_DIR"); d != "" { // same variable scripts/backup.sh honours
		return d
	}
	return filepath.Join(root, "backups")
}

func systemBackups() map[string]any {
	out := map[string]any{}
	root := systemRootDir()
	out["script"] = root != ""
	if root == "" {
		root = systemWorkDir()
	}
	dir := systemBackupDir(root)
	type file struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified string `json:"modified"`
	}
	files := []file{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		out["error"] = "no backup directory yet"
		out["files"] = files
		return out
	}
	type fm struct {
		f file
		t time.Time
	}
	var all []fm
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		if info, err := e.Info(); err == nil {
			all = append(all, fm{file{e.Name(), info.Size(), info.ModTime().UTC().Format(time.RFC3339)}, info.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
	out["count"] = len(all)
	for i, a := range all {
		if i >= 10 {
			break
		}
		files = append(files, a.f)
	}
	out["files"] = files
	out["log_tail"] = tailFile(filepath.Join(dir, "backup.log"), 2048)
	return out
}

func systemCerts() []map[string]any {
	now := time.Now()
	one := func(label, path string) map[string]any {
		e := map[string]any{"label": label}
		raw, err := os.ReadFile(path) // a .crt: public; key files are never opened
		if err != nil {
			e["error"] = "not found"
			return e
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			e["error"] = "not a PEM certificate"
			return e
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			e["error"] = "unreadable certificate"
			return e
		}
		e["cn"] = c.Subject.CommonName
		e["not_after"] = c.NotAfter.UTC().Format(time.RFC3339)
		e["days_left"] = int(c.NotAfter.Sub(now).Hours() / 24)
		return e
	}
	pick := func(env, def string) string {
		if p := os.Getenv(env); p != "" && strings.HasSuffix(p, ".crt") {
			return p
		}
		return filepath.Join(systemWorkDir(), def)
	}
	return []map[string]any{
		one("Gateway (server.crt)", pick("GATEWAY_CERT", "certs/server.crt")),
		one("Firmament (firmament.crt)", pick("FIRMAMENT_CERT", "certs/firmament.crt")),
	}
}

// systemTLSFallback counts the remembered clients (24 h memory), never the addresses.
func systemTLSFallback() map[string]any {
	badMACMu.Lock()
	defer badMACMu.Unlock()
	loadBadMACClientsLocked()
	n := 0
	for _, at := range badMACClients {
		if time.Since(at) <= badMACMemory {
			n++
		}
	}
	return map[string]any{"clients": n, "mode": os.Getenv("DN_TLS_SHA384")}
}

func systemLogs() map[string]any {
	out := map[string]any{}
	wd := systemWorkDir()
	sum := func(dir string) (n int, size int64) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return 0, 0
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			if info, err := e.Info(); err == nil {
				n++
				size += info.Size()
			}
		}
		return
	}
	n, s := sum(filepath.Join(wd, "run"))
	out["run_files"], out["run_bytes"] = n, s
	n, s = sum(filepath.Join(wd, "run", "battle-logs"))
	out["battle_files"], out["battle_bytes"] = n, s
	return out
}

func adminAPISystem(w http.ResponseWriter, _ *http.Request) {
	db, dbFile := systemDatabase(currentMmogPlayerStateDB())
	writeAdminJSON(w, map[string]any{
		"host":      systemHost(dbFile),
		"process":   systemProcess(),
		"database":  db,
		"backups":   systemBackups(),
		"certs":     systemCerts(),
		"tls":       systemTLSFallback(),
		"logs":      systemLogs(),
		"config":    systemConfig(),
		"generated": time.Now().UTC().Format(time.RFC3339),
	})
}

// adminAPISystemBackup runs scripts/backup.sh once at a time.
func adminAPISystemBackup(w http.ResponseWriter, r *http.Request) {
	root := systemRootDir()
	if root == "" {
		writeAdminError(w, http.StatusServiceUnavailable, systemBackupScript+" is not beside mmogbrain; run the backup by hand")
		return
	}
	if !systemBackupMu.TryLock() {
		writeAdminError(w, http.StatusConflict, "a backup is already running")
		return
	}
	defer systemBackupMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), systemBackupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", systemBackupScript)
	cmd.Dir = root
	start := time.Now()
	raw, err := cmd.CombinedOutput()
	tail := string(raw)
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	ok := err == nil
	detail := map[string]any{"ok": ok, "seconds": int(time.Since(start).Seconds())}
	if err != nil {
		detail["error"] = err.Error()
	}
	adminAudit(currentMmogPlayerStateDB(), r, "backup run", "", detail)
	resp := map[string]any{"ok": ok, "output": tail}
	if err != nil {
		resp["error"] = err.Error()
		if ctx.Err() != nil {
			resp["error"] = "backup timed out"
		}
	}
	writeAdminJSON(w, resp)
}
