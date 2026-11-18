package main

// Chunked game-log upload (the launcher since v1.0.11; the one-POST form in
// client_logs.go stays for older launchers).
//
// The game no longer keeps only the first 256 KiB and the last 1.75 MiB of its
// log (the launcher starts it with -NOLOGTOMEMORY), so a session's log can be
// tens to hundreds of MB. One POST of that does not fit: mmogbrain gives a
// request 15 s, the gateway 30 s, and a flaky connection loses everything at
// once. So the launcher cuts the log (already redacted) into line-aligned
// parts of about 4 MiB, gzips each, and sends them one by one:
//
//	POST /launcher/log/part?id=<32 hex>&n=<index>&modified=<unix>   body: gzip(part)
//	POST /launcher/log/commit?id=&parts=<count>&modified=&exit=&crashed=&seconds=&late=&version=
//
// A part is stored when it arrives, after this side removes the sign-in token
// and the Windows names from it again (redactClientLog; the launcher's
// parts end at a line break, so nothing straddles a boundary), as
// <client-logs>/.incoming/<player>/<id>/<n>-<raw bytes>.gz. Sending a part
// again replaces it, so a retry after a dropped connection costs one part. The
// commit checks that every part is there (409 with the missing ones if not, so
// the launcher can send just those), then joins the parts into the final file
// by concatenating the gzip members (a valid gzip file; readers see one
// stream) and indexes it like a one-POST log. Nothing here holds a whole log in
// memory, and the commit copies a few MB of compressed data, so it is quick.
//
// Limits (all enforced here, not trusted from the launcher): a part is at most
// clientLogPartMax compressed and clientLogPartRaw decompressed; a log at most
// clientLogChunkedMaxRaw decompressed and clientLogChunkedMaxStored on disk;
// a player has at most clientLogMaxIncoming unfinished uploads and
// clientLogIncomingMax bytes of them; unfinished uploads are removed after
// clientLogIncomingKeep. The day limit counts logs, as before.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	clientLogPartMax = 8 << 20  // one part, compressed
	clientLogPartRaw = 32 << 20 // one part, decompressed
	// clientLogChunkedMaxRaw: the launcher sends at most 256 MiB (its own cut
	// keeps the start and the end of anything longer); this is the hard stop.
	clientLogChunkedMaxRaw    = 512 << 20
	clientLogChunkedMaxStored = 160 << 20
	clientLogMaxParts         = 512
	clientLogMaxIncoming      = 4
	clientLogIncomingMax      = 256 << 20
	clientLogIncomingKeep     = 24 * time.Hour
)

var (
	clientLogUploadID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	clientLogPartName = regexp.MustCompile(`^(\d{6})-(\d+)\.gz$`)
	// clientLogCommitMu: one commit at a time (they copy files), and the
	// incoming sweep never runs under a commit.
	clientLogCommitMu sync.Mutex
	clientLogSweepAt  time.Time
)

func clientLogIncomingDir(pid, id string) string {
	return filepath.Join(clientLogDir, ".incoming", pid, id)
}

// clientLogIncomingUsage is how many unfinished uploads a player has and how
// many bytes they hold.
func clientLogIncomingUsage(pid string) (uploads int, bytes int64) {
	dirs, _ := os.ReadDir(filepath.Join(clientLogDir, ".incoming", pid))
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		uploads++
		files, _ := os.ReadDir(filepath.Join(clientLogDir, ".incoming", pid, d.Name()))
		for _, f := range files {
			if info, err := f.Info(); err == nil {
				bytes += info.Size()
			}
		}
	}
	return uploads, bytes
}

// pruneClientLogIncoming removes uploads that were never committed.
func pruneClientLogIncoming(now time.Time) {
	players, _ := os.ReadDir(filepath.Join(clientLogDir, ".incoming"))
	for _, p := range players {
		base := filepath.Join(clientLogDir, ".incoming", p.Name())
		dirs, _ := os.ReadDir(base)
		left := len(dirs)
		for _, d := range dirs {
			if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > clientLogIncomingKeep {
				_ = os.RemoveAll(filepath.Join(base, d.Name()))
				left--
			}
		}
		if left == 0 {
			_ = os.Remove(base)
		}
	}
}

// sweepClientLogIncoming runs the sweep at most every ten minutes, from the
// part handler (an abandoned upload is otherwise only removed by the next
// finished one).
func sweepClientLogIncoming() {
	clientLogCommitMu.Lock()
	defer clientLogCommitMu.Unlock()
	if time.Since(clientLogSweepAt) < 10*time.Minute {
		return
	}
	clientLogSweepAt = time.Now()
	pruneClientLogIncoming(time.Now())
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// launcherLogPartHandler: POST /launcher/log/part?id=&n=&modified= with one
// gzip-compressed part as the body.
func launcherLogPartHandler(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := launcherLogAuth(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	n, err := strconv.Atoi(q.Get("n"))
	if !clientLogUploadID.MatchString(id) || err != nil || n < 0 || n >= clientLogMaxParts {
		http.Error(w, `{"error":"bad upload id or part number"}`, http.StatusBadRequest)
		return
	}
	// Already stored (a retry after the commit went through): tell the
	// launcher it is done so it stops sending.
	meta := parseClientLogMeta(r)
	if stored, ok := clientLogStored(database, pid, meta.modified); ok {
		writeLauncherLogReply(w, stored, true)
		return
	}
	dir := clientLogIncomingDir(pid, id)
	if _, err := os.Stat(dir); err != nil {
		// A new upload: the day limit, the number of unfinished ones.
		if !clientLogAllowUpload(w, database, pid, false) {
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
	raw = redactClientLog(raw)
	if err := storeClientLogPart(dir, n, raw); err != nil {
		logrus.WithError(err).Warn("launcher log: could not save a part")
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	go sweepClientLogIncoming()
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true, "part": n})
}

// clientLogUnzipPart is a part's text, or the HTTP status to refuse it with.
func clientLogUnzipPart(body []byte) ([]byte, int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("send each part gzip-compressed")
	}
	raw, err := io.ReadAll(io.LimitReader(zr, clientLogPartRaw+1))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("broken gzip data")
	}
	if len(raw) > clientLogPartRaw {
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("part too large")
	}
	if len(raw) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("empty part")
	}
	return raw, 0, nil
}

// storeClientLogPart writes part n (already redacted) as its own gzip member,
// replacing an earlier copy of the same part.
func storeClientLogPart(dir string, n int, raw []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	prefix := fmt.Sprintf("%06d-", n)
	final := filepath.Join(dir, fmt.Sprintf("%s%d.gz", prefix, len(raw)))
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(tmp)
	_, err = zw.Write(raw)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	// An earlier copy of this part may have another raw size in its name.
	if old, _ := filepath.Glob(filepath.Join(dir, prefix+"*.gz")); len(old) > 0 {
		for _, o := range old {
			_ = os.Remove(o)
		}
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// clientLogParts lists an upload's stored parts: file by index and the raw
// sizes.
func clientLogParts(dir string) (files map[int]string, sizes map[int]int64) {
	files, sizes = map[int]string{}, map[int]int64{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		m := clientLogPartName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		sz, _ := strconv.ParseInt(m[2], 10, 64)
		files[i], sizes[i] = filepath.Join(dir, e.Name()), sz
	}
	return files, sizes
}

// launcherLogCommitHandler: POST /launcher/log/commit?id=&parts=&modified=&exit=&crashed=&seconds=&late=&version=
func launcherLogCommitHandler(w http.ResponseWriter, r *http.Request) {
	pid, database, ok := launcherLogAuth(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	count, err := strconv.Atoi(q.Get("parts"))
	if !clientLogUploadID.MatchString(id) || err != nil || count < 1 || count > clientLogMaxParts {
		http.Error(w, `{"error":"bad upload id or part count"}`, http.StatusBadRequest)
		return
	}
	meta := parseClientLogMeta(r)
	if stored, ok := clientLogStored(database, pid, meta.modified); ok {
		_ = os.RemoveAll(clientLogIncomingDir(pid, id))
		writeLauncherLogReply(w, stored, true)
		return
	}
	clientLogCommitMu.Lock()
	defer clientLogCommitMu.Unlock()

	dir := clientLogIncomingDir(pid, id)
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
	if raw > clientLogChunkedMaxRaw {
		_ = os.RemoveAll(dir)
		http.Error(w, `{"error":"log too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	var stored int64
	for i := 0; i < count; i++ {
		if st, err := os.Stat(files[i]); err == nil {
			stored += st.Size()
		}
	}
	if stored > clientLogChunkedMaxStored {
		_ = os.RemoveAll(dir)
		http.Error(w, `{"error":"log too large"}`, http.StatusRequestEntityTooLarge)
		return
	}

	// After the parts check: a 409 must not use up the gap, the launcher
	// sends the missing parts and commits again at once.
	if !clientLogAllowUpload(w, database, pid, true) {
		return
	}
	rel := newClientLogRel(pid)
	if err := joinClientLogParts(filepath.Join(clientLogDir, rel), files, count); err != nil {
		logrus.WithError(err).Warn("launcher log: could not join the parts")
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	rowID, err := insertClientLog(database, pid, meta, raw, rel)
	if err != nil {
		_ = os.Remove(filepath.Join(clientLogDir, rel))
		http.Error(w, `{"error":"could not save"}`, http.StatusInternalServerError)
		return
	}
	_ = os.RemoveAll(dir)
	pruneClientLogs(database)
	writeLauncherLogReply(w, rowID, false)
}

// joinClientLogParts writes the final log: the parts' gzip members one after
// the other.
func joinClientLogParts(path string, files map[int]string, count int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		//nolint:gosec // files holds names this package made inside the incoming folder.
		in, oerr := os.Open(files[i])
		if oerr != nil {
			err = oerr
			break
		}
		_, err = io.Copy(out, in)
		_ = in.Close()
		if err != nil {
			break
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
