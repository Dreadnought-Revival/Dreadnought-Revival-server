package main

// Game logs from the launcher. After a session the launcher uploads the
// player's DreadGame.log (POST /launcher/log, through the public gateway like
// /launcher/clock, with the player's token as "Authorization: Bearer"), so a
// bug report no longer depends on the player finding and sending the file.
//
// The body is the log, gzip-compressed. The launcher has already removed the
// private parts; this side removes them again (redactClientLog), so a launcher
// with an older or broken filter cannot store them here:
//
//   - the sign-in token: the client prints its own JWT
//     ("LogWebServicesPlugin: Authorization: Bearer eyJ...");
//   - the Windows computer and user names ("LogInit: Computer: ...",
//     "LogInit: User: ...") and the user folder in paths (C:\Users\<name>\...).
//
// The session id and the account name in "Authorization: Session <uuid>,
// <name>" stay: the operator wants them, and neither signs anyone in.
//
// Files live in <DB folder>/client-logs/<player>/; the client_logs table
// (db/db.go) indexes them. Admins list and download them on the player page.
//
// Two upload forms, both accepted:
//
//   - one POST of the whole gzip-compressed log (launchers up to v1.0.10);
//     capped at clientLogMaxUpload compressed / clientLogMaxRaw raw, because the
//     body is read into memory and the HTTP server gives a request 15 s;
//   - chunked (client_logs_chunked.go): the launcher sends the log in
//     line-aligned parts, then commits. A whole session's log (tens to
//     hundreds of MB raw, the game no longer keeps only its last 2 MiB) gets
//     through slow or flaky connections, and nothing here holds a whole log in
//     memory.

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

const (
	// clientLogMaxUpload is the largest compressed body of the one-POST form.
	clientLogMaxUpload = 8 << 20
	// clientLogMaxRaw is the largest log of the one-POST form once
	// decompressed (launchers up to v1.0.10 cut anything past 40 MB down to
	// its start and its end). The chunked form has its own, larger limits.
	clientLogMaxRaw = 48 << 20
	// clientLogMinGap and clientLogPerDay limit how often one player can
	// upload: a session is minutes long, so this only stops a loop.
	clientLogMinGap = 15 * time.Second
	clientLogPerDay = 30
)

var (
	// clientLogDir is where the files go; main sets it beside the database.
	clientLogDir = "client-logs"
	// clientLogKeepDays, clientLogMaxBytes and clientLogPlayerMaxBytes bound
	// what is kept: older logs, and the oldest logs past a size, are deleted
	// after each upload. The sizes are DISK bytes (the stored .gz files), in
	// total and per player. CLIENT_LOG_DAYS / CLIENT_LOG_MAX_MB /
	// CLIENT_LOG_PLAYER_MAX_MB override them.
	clientLogKeepDays             = 30
	clientLogMaxBytes       int64 = 2 << 30
	clientLogPlayerMaxBytes int64 = 256 << 20

	clientLogMu      sync.Mutex // guards clientLogLast
	clientLogLast    = map[string]time.Time{}
	clientLogPruneMu sync.Mutex
)

func configureClientLogs(dbPath string) {
	clientLogDir = filepath.Join(filepath.Dir(dbPath), "client-logs")
	if n, err := strconv.Atoi(os.Getenv("CLIENT_LOG_DAYS")); err == nil && n > 0 {
		clientLogKeepDays = n
	}
	if n, err := strconv.ParseInt(os.Getenv("CLIENT_LOG_MAX_MB"), 10, 64); err == nil && n > 0 {
		clientLogMaxBytes = n << 20
	}
	if n, err := strconv.ParseInt(os.Getenv("CLIENT_LOG_PLAYER_MAX_MB"), 10, 64); err == nil && n > 0 {
		clientLogPlayerMaxBytes = n << 20
	}
}

var (
	// A JWT: base64url JSON header, always starting "eyJ" ({"). A cut-off one
	// (no dots) is removed as well.
	clientLogJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}(?:\.[A-Za-z0-9_-]*){0,2}`)
	// UE4's start-up lines naming the machine and the Windows account.
	clientLogMachine = regexp.MustCompile(`(LogInit: (?:Computer|User): )[^\r\n]*`)
	// The Windows user folder in a path, either slash.
	clientLogUserDir = regexp.MustCompile(`(?i)([A-Z]:[\\/]+Users[\\/]+)[^\\/\r\n"'<>|?*]+`)
)

// redactClientLog removes the sign-in token and the Windows computer and user
// names from a game log. The launcher does the same before it uploads.
func redactClientLog(b []byte) []byte {
	b = clientLogJWT.ReplaceAll(b, []byte("<token removed>"))
	b = clientLogMachine.ReplaceAll(b, []byte("${1}<removed>"))
	return clientLogUserDir.ReplaceAll(b, []byte("${1}<user>"))
}

type clientLogMeta struct {
	modified, exitCode, seconds int64
	crashed, late               bool
	version                     string
}

func parseClientLogMeta(r *http.Request) clientLogMeta {
	q := r.URL.Query()
	num := func(k string, def int64) int64 {
		if n, err := strconv.ParseInt(q.Get(k), 10, 64); err == nil {
			return n
		}
		return def
	}
	m := clientLogMeta{modified: num("modified", 0), exitCode: num("exit", -1), seconds: num("seconds", 0),
		crashed: q.Get("crashed") == "1", late: q.Get("late") == "1", version: q.Get("version")}
	if m.seconds < 0 || m.seconds > 7*24*3600 {
		m.seconds = 0
	}
	if len(m.version) > 32 || !validClockText(m.version, 32) {
		m.version = ""
	}
	return m
}

// launcherLogAuth is the signed-in player of an upload request and the
// database, or answers the request itself and says so.
func launcherLogAuth(w http.ResponseWriter, r *http.Request) (string, *sql.DB, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	pid, err := protocol.ExtractVerifiedPlayerPIDFromJWT(token, mmogJWTSecret(), "launcher", "dreadnought")
	pid = protocol.NormalizePlayerPID(pid)
	if err != nil || !adminPIDPattern.MatchString(pid) {
		http.Error(w, `{"error":"sign in first"}`, http.StatusUnauthorized)
		return "", nil, false
	}
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return "", nil, false
	}
	return pid, database, true
}

// launcherLogHandler: POST /launcher/log?modified=&exit=&crashed=&seconds=&late=&version=
// with the gzip-compressed log as the body (the one-POST form).
func launcherLogHandler(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := launcherLogAuth(w, r)
	if !ok {
		return
	}
	meta := parseClientLogMeta(r)

	// A retry of a log already stored is answered as done.
	if id, ok := clientLogStored(database, pid, meta.modified); ok {
		writeLauncherLogReply(w, id, true)
		return
	}
	if !clientLogAllowUpload(w, database, pid, true) {
		return
	}

	raw, status, err := readClientLogBody(w, r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), status)
		return
	}
	raw = redactClientLog(raw)

	rel := newClientLogRel(pid)
	if err := writeClientLogFile(filepath.Join(clientLogDir, rel), raw); err != nil {
		logrus.WithError(err).Warn("launcher log: could not save the file")
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	id, err := insertClientLog(database, pid, meta, int64(len(raw)), rel)
	if err != nil {
		_ = os.Remove(filepath.Join(clientLogDir, rel))
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	pruneClientLogs(database)
	writeLauncherLogReply(w, id, false)
}

// clientLogStored is the row of a log this player already uploaded (same
// session, by the log file's modified time): a launcher retry.
func clientLogStored(database *sql.DB, pid string, modified int64) (int64, bool) {
	if modified <= 0 {
		return 0, false
	}
	var id int64
	if database.QueryRow(`SELECT id FROM client_logs WHERE user_id=? AND log_modified=?`, pid, modified).Scan(&id) != nil {
		return 0, false
	}
	return id, true
}

// clientLogAllowUpload applies the per-day limit and, when gap is set, the
// minimum gap between two uploads of one player; it answers 429 itself. The
// chunked form checks the day limit on its first part and both on its commit.
func clientLogAllowUpload(w http.ResponseWriter, database *sql.DB, pid string, gap bool) bool {
	var today int
	_ = database.QueryRow(`SELECT COUNT(*) FROM client_logs WHERE user_id=? AND uploaded_at > datetime('now','-1 day')`, pid).Scan(&today)
	if today >= clientLogPerDay {
		http.Error(w, `{"error":"too many uploads today"}`, http.StatusTooManyRequests)
		return false
	}
	if !gap {
		return true
	}
	clientLogMu.Lock()
	defer clientLogMu.Unlock()
	if last, ok := clientLogLast[pid]; ok && time.Since(last) < clientLogMinGap {
		http.Error(w, `{"error":"too many uploads, try again later"}`, http.StatusTooManyRequests)
		return false
	}
	clientLogLast[pid] = time.Now()
	return true
}

// newClientLogRel is a fresh file name (relative to clientLogDir) for a log.
func newClientLogRel(pid string) string {
	now := time.Now().UTC()
	return filepath.Join(pid, fmt.Sprintf("%s-%d.log.gz", now.Format("20060102-150405"), now.UnixNano()%1e6))
}

// insertClientLog indexes a stored log; bytes is its size once decompressed.
func insertClientLog(database *sql.DB, pid string, meta clientLogMeta, bytes int64, rel string) (int64, error) {
	res, err := database.Exec(`INSERT INTO client_logs(user_id, log_modified, exit_code, crashed, seconds, late, launcher_version, bytes, path)
		VALUES(?,?,?,?,?,?,?,?,?)`, pid, meta.modified, meta.exitCode, meta.crashed, meta.seconds, meta.late, meta.version, bytes, filepath.ToSlash(rel))
	if err != nil {
		logrus.WithError(err).Warn("launcher log: could not save the row")
		return 0, err
	}
	id, _ := res.LastInsertId()
	logrus.WithFields(logrus.Fields{"id": id, "bytes": bytes, "exit_code": meta.exitCode, "crashed": meta.crashed,
		"late": meta.late, "launcher": meta.version}).Info("launcher log: saved")
	return id, nil
}

func writeLauncherLogReply(w http.ResponseWriter, id int64, duplicate bool) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"ok":true,"id":%d,"duplicate":%t}`, id, duplicate)
}

// readClientLogBody is the decompressed log, or the HTTP status to refuse it with.
func readClientLogBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, clientLogMaxUpload))
	if err != nil {
		return nil, http.StatusRequestEntityTooLarge, errors.New("upload too large")
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("send the log gzip-compressed")
	}
	raw, err := io.ReadAll(io.LimitReader(zr, clientLogMaxRaw+1))
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("broken gzip data")
	}
	if len(raw) > clientLogMaxRaw {
		return nil, http.StatusRequestEntityTooLarge, errors.New("log too large")
	}
	if len(raw) == 0 {
		return nil, http.StatusBadRequest, errors.New("empty log")
	}
	return raw, 0, nil
}

func writeClientLogFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	_, err = zw.Write(raw)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

type clientLogRow struct {
	ID       int64  `json:"id"`
	At       string `json:"uploaded_at"`
	ExitCode int64  `json:"exit_code"`
	Crashed  bool   `json:"crashed"`
	Seconds  int64  `json:"seconds"`
	Late     bool   `json:"late"`
	Version  string `json:"launcher_version"`
	Bytes    int64  `json:"bytes"`
	// Stored is the size of the .gz file on disk; Bytes is the log's own.
	Stored int64 `json:"stored_bytes"`
	path   string
	user   string
}

// clientLogRows runs a client_logs query and closes the result set before it
// returns (single DB connection: see dreadnought-single-connection-db).
func clientLogRows(database *sql.DB, where string, args ...any) []clientLogRow {
	out := []clientLogRow{}
	rows, err := database.Query(`SELECT id, uploaded_at, exit_code, crashed, seconds, late, launcher_version, bytes, path, user_id FROM client_logs `+where, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c clientLogRow
		if rows.Scan(&c.ID, &c.At, &c.ExitCode, &c.Crashed, &c.Seconds, &c.Late, &c.Version, &c.Bytes, &c.path, &c.user) == nil {
			if p, ok := clientLogPath(c.path); ok {
				if st, err := os.Stat(p); err == nil {
					c.Stored = st.Size()
				}
			}
			out = append(out, c)
		}
	}
	return out
}

func playerClientLogs(database *sql.DB, pid string, limit int) []clientLogRow {
	return clientLogRows(database, `WHERE user_id=? ORDER BY id DESC LIMIT ?`, pid, limit)
}

// pruneClientLogs deletes logs older than clientLogKeepDays, then the oldest
// ones past clientLogPlayerMaxBytes for their player and past clientLogMaxBytes
// in total (disk bytes of the stored files), and uploads that were never
// committed.
func pruneClientLogs(database *sql.DB) {
	clientLogPruneMu.Lock()
	defer clientLogPruneMu.Unlock()
	pruneClientLogIncoming(time.Now())
	all := clientLogRows(database, `ORDER BY id DESC`)
	cutoff := time.Now().UTC().AddDate(0, 0, -clientLogKeepDays).Format("2006-01-02 15:04:05")
	var total int64
	perPlayer := map[string]int64{}
	for _, c := range all {
		if c.At >= cutoff && total+c.Stored <= clientLogMaxBytes && perPlayer[c.user]+c.Stored <= clientLogPlayerMaxBytes {
			total += c.Stored
			perPlayer[c.user] += c.Stored
			continue
		}
		if p, ok := clientLogPath(c.path); ok {
			_ = os.Remove(p)
		}
		_, _ = database.Exec(`DELETE FROM client_logs WHERE id=?`, c.ID)
	}
}

// clientLogPath is the file of a row's path, refusing anything that would
// leave the client-logs folder.
func clientLogPath(rel string) (string, bool) {
	p := filepath.Join(clientLogDir, filepath.FromSlash(rel))
	r, err := filepath.Rel(clientLogDir, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

func init() { adminRegisterAPI(registerAdminClientLogs) }

func registerAdminClientLogs(api *mux.Router) {
	api.HandleFunc("/players/{pid}/client-logs", adminAPIPlayerClientLogs).Methods(http.MethodGet)
	api.HandleFunc("/client-logs/{id}", adminAPIClientLogDownload).Methods(http.MethodGet)
}

func adminAPIPlayerClientLogs(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := adminPlayerPID(w, r)
	if !ok {
		return
	}
	writeAdminJSON(w, map[string]any{"logs": playerClientLogs(database, pid, 200)})
}

// adminAPIClientLogDownload sends one log as a text file.
func adminAPIClientLogDownload(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"log id must be a number"}`, http.StatusBadRequest)
		return
	}
	var pid, rel, at string
	if err := database.QueryRow(`SELECT user_id, path, uploaded_at FROM client_logs WHERE id=?`, id).Scan(&pid, &rel, &at); err != nil {
		http.Error(w, `{"error":"no such log"}`, http.StatusNotFound)
		return
	}
	p, ok := clientLogPath(rel)
	if !ok {
		http.Error(w, `{"error":"no such log"}`, http.StatusNotFound)
		return
	}
	//nolint:gosec // p is checked to stay inside clientLogDir.
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, `{"error":"the file is gone"}`, http.StatusNotFound)
		return
	}
	defer f.Close()
	// A whole session's log can be hundreds of MB: more than the server's
	// 15 s write timeout gives (and ?gzip=1 sends the stored file as it is,
	// about a tenth of that).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	name := fmt.Sprintf("DreadGame-%s-%s.log", pid[:8], strings.NewReplacer(" ", "_", ":", "-").Replace(at))
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("gzip") == "1" {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.gz"`)
		_, _ = io.Copy(w, f)
		return
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		http.Error(w, `{"error":"broken file"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.Copy(w, zr)
}
