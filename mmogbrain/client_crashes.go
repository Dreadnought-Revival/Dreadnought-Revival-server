package main

// Crash reports from the launcher. When the game crashes, the launcher sends
// the crash report the game wrote (a zip of its files, the minidump first of
// all), so a crash can be read in a debugger instead of guessed from the log.
//
// Where the game puts a crash report, verified in the exe (2026-11-23):
//
//   - The crash handler (0x140CC1EF0-0x140CC324D) files a Windows Error Report:
//     WerReportCreate, then WerReportSetParameter (P0 "Application Name" =
//     "UE4-DreadGame", from "UE4-%s"; version, time stamp, fault module ...),
//     five WerReportAddFile calls -- one of type 2 (WerFileTypeMinidump; the
//     exe writes UE4Minidump.dmp with dbghelp MiniDumpWriteDump), four of
//     type 5 (other files, one named CrashVideo.avi) -- and WerReportSubmit
//     with consent 4 and flags 0x804 = WER_SUBMIT_QUEUE |
//     WER_SUBMIT_BYPASS_DATA_THROTTLING: the report is QUEUED on the PC, not
//     sent. Windows copies the added files into the queued report's folder.
//   - Then it starts CrashReportClient.exe -Unattended -AppName=UE4-DreadGame
//     -CrashGUID=WindowsWERFailureNoGUID, which looks for that report under
//     "Microsoft/Windows/WER/ReportQueue" (literal in CrashReportClient.exe;
//     the folder whose name holds "UE4-" and that has a Report.wer) and would
//     upload it to bugreports.greybox.com:57005 -- gone. Under Wine there is
//     no WER, and its log says "No error report found" (observed, 2026-10-26).
//   - The exe has no "Crashes" literal, so the Saved\Crashes folder of later
//     UE4 versions is not used; "%s/UE4CC*" is only its cleanup of old crash
//     context files in the log folder.
//
// So the launcher looks in %LOCALAPPDATA%\Microsoft\Windows\WER\ReportQueue
// (and ReportArchive, and the same under %ProgramData% -- GUESS: where
// Windows keeps a report it has already handled or queued machine-wide) for
// folders named after UE4-DreadGame or the exe, made during the session.
//
// Upload: the launcher zips the folder's files (no heap dumps or videos),
// scrubs them in place (client_crash_scrub.go has the rules), cuts the zip
// into gzip parts and sends them like a game log:
//
//	POST /launcher/crash/part?id=<32 hex>&n=<index>&report=<folder name>   body: gzip(part)
//	POST /launcher/crash/commit?id=&parts=&report=&modified=&exit=&on_exit=&seconds=&late=&version=
//
// The commit joins the parts, checks the zip, scrubs every file again and
// stores a fresh zip in <DB folder>/crash-reports/<player>/; the
// client_crashes table indexes it, and admins download it on the player page.
// A report already stored (same player, same folder name) is answered as
// done, so a retry or a second launcher never stores it twice.

import (
	"archive/zip"
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
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

const (
	// clientCrashMaxZip is the largest zip the launcher may send (its own cap
	// on what it packs is lower).
	clientCrashMaxZip = 96 << 20
	// clientCrashMaxFiles / clientCrashMaxFile / clientCrashMaxUnpacked bound
	// what is inside it.
	clientCrashMaxFiles    = 32
	clientCrashMaxFile     = 128 << 20
	clientCrashMaxUnpacked = 256 << 20
	// clientCrashPerDay: one session crashes once; this only stops a loop.
	clientCrashPerDay = 10
)

var (
	// clientCrashDir is where the zips go; configureClientLogs sets it beside
	// the database. Kept: clientLogKeepDays, and clientCrashMaxBytes /
	// clientCrashPlayerMaxBytes of zips (CLIENT_CRASH_MAX_MB,
	// CLIENT_CRASH_PLAYER_MAX_MB).
	clientCrashDir                  = "crash-reports"
	clientCrashMaxBytes       int64 = 2 << 30
	clientCrashPlayerMaxBytes int64 = 256 << 20

	// clientCrashReportName: a WER report folder name (AppCrash_..., Critical_...,
	// with hex ids) or a UE4 crash folder name.
	clientCrashReportName = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,160}$`)
	// clientCrashFileName: a file inside the zip, no folders.
	clientCrashFileName = regexp.MustCompile(`^[A-Za-z0-9._ ()-]{1,120}$`)
)

func configureClientCrashes(dbPath string) {
	clientCrashDir = filepath.Join(filepath.Dir(dbPath), "crash-reports")
	if n, err := strconv.ParseInt(os.Getenv("CLIENT_CRASH_MAX_MB"), 10, 64); err == nil && n > 0 {
		clientCrashMaxBytes = n << 20
	}
	if n, err := strconv.ParseInt(os.Getenv("CLIENT_CRASH_PLAYER_MAX_MB"), 10, 64); err == nil && n > 0 {
		clientCrashPlayerMaxBytes = n << 20
	}
}

type clientCrashMeta struct {
	report                      string
	modified, exitCode, seconds int64
	onExit, late                bool
	version                     string
}

func parseClientCrashMeta(r *http.Request) (clientCrashMeta, bool) {
	q := r.URL.Query()
	lm := parseClientLogMeta(r)
	m := clientCrashMeta{report: q.Get("report"), modified: lm.modified, exitCode: lm.exitCode, seconds: lm.seconds,
		onExit: q.Get("on_exit") == "1", late: lm.late, version: lm.version}
	return m, clientCrashReportName.MatchString(m.report) && !strings.Contains(m.report, "..")
}

// clientCrashStored is the row of a report this player already sent.
func clientCrashStored(database *sql.DB, pid, report string) (int64, bool) {
	var id int64
	if database.QueryRow(`SELECT id FROM client_crashes WHERE user_id=? AND report_name=?`, pid, report).Scan(&id) != nil {
		return 0, false
	}
	return id, true
}

func clientCrashAllowUpload(w http.ResponseWriter, database *sql.DB, pid string) bool {
	var today int
	_ = database.QueryRow(`SELECT COUNT(*) FROM client_crashes WHERE user_id=? AND uploaded_at > datetime('now','-1 day')`, pid).Scan(&today)
	if today >= clientCrashPerDay {
		http.Error(w, `{"error":"too many crash reports today"}`, http.StatusTooManyRequests)
		return false
	}
	return true
}

// clientCrashIncomingDir shares the game logs' incoming folder, so the
// unfinished-upload limits and the sweep cover both.
func clientCrashIncomingDir(pid, id string) string {
	return clientLogIncomingDir(pid, "crash-"+id)
}

// launcherCrashPartHandler: POST /launcher/crash/part?id=&n=&report=
func launcherCrashPartHandler(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := launcherLogAuth(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	n, err := strconv.Atoi(q.Get("n"))
	meta, okName := parseClientCrashMeta(r)
	if !clientLogUploadID.MatchString(id) || err != nil || n < 0 || n >= clientLogMaxParts || !okName {
		http.Error(w, `{"error":"bad upload id, part number or report name"}`, http.StatusBadRequest)
		return
	}
	if stored, ok := clientCrashStored(database, pid, meta.report); ok {
		writeLauncherLogReply(w, stored, true)
		return
	}
	dir := clientCrashIncomingDir(pid, id)
	if _, err := os.Stat(dir); err != nil {
		if !clientCrashAllowUpload(w, database, pid) {
			return
		}
		if uploads, _ := clientLogIncomingUsage(pid); uploads >= clientLogMaxIncoming {
			http.Error(w, `{"error":"too many unfinished uploads"}`, http.StatusTooManyRequests)
			return
		}
	}
	if _, used := clientLogIncomingUsage(pid); used > clientLogIncomingMax {
		http.Error(w, `{"error":"too much unfinished upload data"}`, http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, clientLogPartMax))
	if err != nil {
		http.Error(w, `{"error":"part too large or cut off"}`, http.StatusRequestEntityTooLarge)
		return
	}
	raw, status, err := clientLogUnzipPart(body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), status)
		return
	}
	// Binary: no log redaction here (it would shift bytes); the whole zip is
	// scrubbed at the commit.
	if err := storeClientLogPart(dir, n, raw); err != nil {
		logrus.WithError(err).Warn("launcher crash: could not save a part")
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	go sweepClientLogIncoming()
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true, "part": n})
}

// launcherCrashCommitHandler: POST /launcher/crash/commit?id=&parts=&report=&...
func launcherCrashCommitHandler(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := launcherLogAuth(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	count, err := strconv.Atoi(q.Get("parts"))
	meta, okName := parseClientCrashMeta(r)
	if !clientLogUploadID.MatchString(id) || err != nil || count < 1 || count > clientLogMaxParts || !okName {
		http.Error(w, `{"error":"bad upload id, part count or report name"}`, http.StatusBadRequest)
		return
	}
	dir := clientCrashIncomingDir(pid, id)
	if stored, ok := clientCrashStored(database, pid, meta.report); ok {
		_ = os.RemoveAll(dir)
		writeLauncherLogReply(w, stored, true)
		return
	}
	clientLogCommitMu.Lock()
	defer clientLogCommitMu.Unlock()

	files, sizes := clientLogParts(dir)
	var missing []int
	var raw int64
	for i := 0; i < count; i++ {
		if _, ok := files[i]; !ok {
			if len(missing) < 64 {
				missing = append(missing, i)
			}
			continue
		}
		raw += sizes[i]
	}
	if len(missing) > 0 {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "parts missing", "missing": missing})
		return
	}
	if raw > clientCrashMaxZip {
		_ = os.RemoveAll(dir)
		http.Error(w, `{"error":"crash report too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	// Scrubbing a large dump takes a few seconds.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))

	joined := filepath.Join(dir, "joined.zip")
	if err := joinClientCrashParts(joined, files, count); err != nil {
		logrus.WithError(err).Warn("launcher crash: could not join the parts")
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	rel := filepath.Join(pid, fmt.Sprintf("%s-%d.zip", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()%1e6))
	nFiles, unpacked, err := rewriteClientCrashZip(joined, filepath.Join(clientCrashDir, rel))
	if err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	res, err := database.Exec(`INSERT INTO client_crashes(user_id, report_name, report_time, exit_code, on_exit, seconds, late, launcher_version, files, bytes, path)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, pid, meta.report, meta.modified, meta.exitCode, meta.onExit, meta.seconds, meta.late, meta.version,
		nFiles, unpacked, filepath.ToSlash(rel))
	if err != nil {
		logrus.WithError(err).Warn("launcher crash: could not save the row")
		_ = os.Remove(filepath.Join(clientCrashDir, rel))
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	rowID, _ := res.LastInsertId()
	_ = os.RemoveAll(dir)
	logrus.WithFields(logrus.Fields{"id": rowID, "files": nFiles, "bytes": unpacked, "exit_code": meta.exitCode,
		"on_exit": meta.onExit, "late": meta.late, "launcher": meta.version}).Info("launcher crash: saved")
	pruneClientCrashes(database)
	writeLauncherLogReply(w, rowID, false)
}

// joinClientCrashParts writes the parts' contents (each stored as its own
// gzip member) one after the other: the zip the launcher sent.
func joinClientCrashParts(path string, files map[int]string, count int) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	for i := 0; i < count && err == nil; i++ {
		var in *os.File
		//nolint:gosec // files holds names this package made inside the incoming folder.
		if in, err = os.Open(files[i]); err != nil {
			break
		}
		var zr *gzip.Reader
		if zr, err = gzip.NewReader(in); err == nil {
			_, err = io.Copy(out, io.LimitReader(zr, clientLogPartRaw+1))
		}
		_ = in.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// rewriteClientCrashZip checks the launcher's zip and writes a new one with
// every file scrubbed. It returns the number of files and their total size.
func rewriteClientCrashZip(src, dst string) (int, int64, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return 0, 0, errors.New("not a zip file")
	}
	defer zr.Close()
	if len(zr.File) == 0 || len(zr.File) > clientCrashMaxFiles {
		return 0, 0, errors.New("a crash report holds 1 to 32 files")
	}
	var total uint64
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if !clientCrashFileName.MatchString(name) || name == "." || name == ".." || seen[strings.ToLower(name)] {
			return 0, 0, fmt.Errorf("bad file name in the report")
		}
		seen[strings.ToLower(name)] = true
		if f.UncompressedSize64 > clientCrashMaxFile {
			return 0, 0, errors.New("a file in the report is too large")
		}
		total += f.UncompressedSize64
	}
	if total > clientCrashMaxUnpacked {
		return 0, 0, errors.New("crash report too large")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, 0, err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
	zw := zip.NewWriter(out)
	var written int64
	err = func() error {
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				return errors.New("broken zip data")
			}
			// The header's size is the launcher's word; the limit holds anyway.
			data, err := io.ReadAll(io.LimitReader(rc, clientCrashMaxFile+1))
			_ = rc.Close()
			if err != nil || len(data) > clientCrashMaxFile {
				return errors.New("broken zip data")
			}
			scrubCrashData(data, nil)
			hdr := &zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified}
			fw, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			if _, err := fw.Write(data); err != nil {
				return err
			}
			written += int64(len(data))
		}
		return zw.Close()
	}()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, 0, err
	}
	return len(zr.File), written, nil
}

type clientCrashRow struct {
	ID       int64  `json:"id"`
	At       string `json:"uploaded_at"`
	Report   string `json:"report_name"`
	ExitCode int64  `json:"exit_code"`
	OnExit   bool   `json:"on_exit"`
	Seconds  int64  `json:"seconds"`
	Late     bool   `json:"late"`
	Version  string `json:"launcher_version"`
	Files    int64  `json:"files"`
	Bytes    int64  `json:"bytes"`
	Stored   int64  `json:"stored_bytes"`
	path     string
	user     string
}

// clientCrashRows closes its result set before it returns (single DB
// connection: see dreadnought-single-connection-db).
func clientCrashRows(database *sql.DB, where string, args ...any) []clientCrashRow {
	out := []clientCrashRow{}
	rows, err := database.Query(`SELECT id, uploaded_at, report_name, exit_code, on_exit, seconds, late, launcher_version, files, bytes, path, user_id
		FROM client_crashes `+where, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c clientCrashRow
		if rows.Scan(&c.ID, &c.At, &c.Report, &c.ExitCode, &c.OnExit, &c.Seconds, &c.Late, &c.Version, &c.Files, &c.Bytes, &c.path, &c.user) == nil {
			if p, ok := clientCrashPath(c.path); ok {
				if st, err := os.Stat(p); err == nil {
					c.Stored = st.Size()
				}
			}
			out = append(out, c)
		}
	}
	return out
}

func playerClientCrashes(database *sql.DB, pid string, limit int) []clientCrashRow {
	return clientCrashRows(database, `WHERE user_id=? ORDER BY id DESC LIMIT ?`, pid, limit)
}

// pruneClientCrashes deletes reports older than clientLogKeepDays and the
// oldest past the size limits, per player and in total.
func pruneClientCrashes(database *sql.DB) {
	clientLogPruneMu.Lock()
	defer clientLogPruneMu.Unlock()
	all := clientCrashRows(database, `ORDER BY id DESC`)
	cutoff := time.Now().UTC().AddDate(0, 0, -clientLogKeepDays).Format("2006-01-02 15:04:05")
	var total int64
	perPlayer := map[string]int64{}
	for _, c := range all {
		if c.At >= cutoff && total+c.Stored <= clientCrashMaxBytes && perPlayer[c.user]+c.Stored <= clientCrashPlayerMaxBytes {
			total += c.Stored
			perPlayer[c.user] += c.Stored
			continue
		}
		if p, ok := clientCrashPath(c.path); ok {
			_ = os.Remove(p)
		}
		_, _ = database.Exec(`DELETE FROM client_crashes WHERE id=?`, c.ID)
	}
}

// clientCrashPath is the file of a row's path, refusing anything that would
// leave the crash-reports folder.
func clientCrashPath(rel string) (string, bool) {
	p := filepath.Join(clientCrashDir, filepath.FromSlash(rel))
	r, err := filepath.Rel(clientCrashDir, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

func init() { adminRegisterAPI(registerAdminClientCrashes) }

func registerAdminClientCrashes(api *mux.Router) {
	api.HandleFunc("/client-crashes/{id}", adminAPIClientCrashDownload).Methods(http.MethodGet)
}

// adminAPIClientCrashDownload sends one crash report zip.
func adminAPIClientCrashDownload(w http.ResponseWriter, r *http.Request) {
	database := currentMmogPlayerStateDB()
	if database == nil {
		http.Error(w, `{"error":"not ready"}`, http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"crash id must be a number"}`, http.StatusBadRequest)
		return
	}
	var pid, rel, at string
	if err := database.QueryRow(`SELECT user_id, path, uploaded_at FROM client_crashes WHERE id=?`, id).Scan(&pid, &rel, &at); err != nil {
		http.Error(w, `{"error":"no such crash report"}`, http.StatusNotFound)
		return
	}
	p, ok := clientCrashPath(rel)
	if !ok {
		http.Error(w, `{"error":"no such crash report"}`, http.StatusNotFound)
		return
	}
	//nolint:gosec // p is checked to stay inside clientCrashDir.
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, `{"error":"the file is gone"}`, http.StatusNotFound)
		return
	}
	defer f.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	name := fmt.Sprintf("DreadGame-crash-%s-%s.zip", pid[:8], strings.NewReplacer(" ", "_", ":", "-").Replace(at))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.Copy(w, f)
}
