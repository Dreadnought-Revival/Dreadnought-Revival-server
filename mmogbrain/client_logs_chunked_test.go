package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
)

// chunkedLogEnv is a signed-in test player and a temporary log folder.
type chunkedLogEnv struct {
	t     *testing.T
	pid   string
	token string
}

func newChunkedLogEnv(t *testing.T) *chunkedLogEnv {
	t.Helper()
	useTempMmogPlayerStateDB(t)
	oldDir := clientLogDir
	clientLogDir = t.TempDir()
	t.Cleanup(func() { clientLogDir = oldDir })
	resetLogGap := func() {
		clientLogMu.Lock()
		clientLogLast = map[string]time.Time{}
		clientLogMu.Unlock()
	}
	resetLogGap()
	t.Cleanup(resetLogGap)
	oldSecret := mmogJWTSecret()
	setMmogJWTSecret([]byte("test-secret"))
	t.Cleanup(func() { setMmogJWTSecret(oldSecret) })
	const user = "4d1f0c2a-6b7e-4c55-9a10-3f2b8e9d7c61"
	return &chunkedLogEnv{t: t, pid: protocol.NormalizePlayerPID(user),
		token: mustSignTestJWT(t, jwt.MapClaims{"sub": user, "user_id": user, "iss": protocol.GatewayJWTIssuer,
			"aud": "launcher", "realm": "dreadnought.pc-us", "exp": time.Now().Add(time.Hour).Unix()})}
}

func (e *chunkedLogEnv) do(h http.HandlerFunc, path, query, tok string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path+"?"+query, bytes.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func (e *chunkedLogEnv) part(id string, n int, text string) *httptest.ResponseRecorder {
	return e.do(launcherLogPartHandler, "/launcher/log/part", fmt.Sprintf("id=%s&n=%d&modified=1763366371", id, n), e.token, gzipped(e.t, text))
}

func (e *chunkedLogEnv) commit(id string, parts int) *httptest.ResponseRecorder {
	return e.do(launcherLogCommitHandler, "/launcher/log/commit",
		fmt.Sprintf("id=%s&parts=%d&modified=1763366371&exit=0&seconds=900&version=1.0.11", id, parts), e.token, nil)
}

const chunkedTestID = "0123456789abcdef0123456789abcdef"

// A log in three parts, sent out of order with one part sent twice, joins into
// one log; the commit can be repeated; the admin download reads it as one
// stream, plain or as the stored .gz.
func TestLauncherLogChunked(t *testing.T) {
	e := newChunkedLogEnv(t)
	database := currentMmogPlayerStateDB()
	p0 := "Log file open, 11/17/26 22:44:55\r\n" + testClientLog
	p1 := "[2026.11.17-08.00.00:000][  1]LogNet: second part\r\n[2026.11.17-08.00.01:000][  1]LogWebServicesPlugin: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl\r\n"
	p2 := "[2026.11.17-08.00.02:000][  1]LogInit: User: testwinuser\r\n[2026.11.17-08.00.03:000][  1]Log file closed, 11/17/26 23:00:00\r\n"

	if rec := e.do(launcherLogPartHandler, "/launcher/log/part", "id="+chunkedTestID+"&n=0", "", gzipped(t, p0)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", rec.Code)
	}
	for _, q := range []string{"id=short&n=0", "id=" + chunkedTestID + "&n=-1", "id=" + chunkedTestID + "&n=999999", "id=" + chunkedTestID} {
		if rec := e.do(launcherLogPartHandler, "/launcher/log/part", q, e.token, gzipped(t, p0)); rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: %d, want 400", q, rec.Code)
		}
	}
	if rec := e.do(launcherLogPartHandler, "/launcher/log/part", "id="+chunkedTestID+"&n=0", e.token, []byte("plain")); rec.Code != http.StatusBadRequest {
		t.Fatalf("plain part: %d, want 400", rec.Code)
	}
	if rec := e.part(chunkedTestID, 2, p2); rec.Code != http.StatusOK {
		t.Fatalf("part 2: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.part(chunkedTestID, 0, "stale"); rec.Code != http.StatusOK {
		t.Fatalf("part 0: %d %s", rec.Code, rec.Body.String())
	}
	// Part 1 is missing: told which, and the gap is not used up.
	rec := e.commit(chunkedTestID, 3)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"missing":[1]`) {
		t.Fatalf("commit with a part missing: %d %s", rec.Code, rec.Body.String())
	}
	e.part(chunkedTestID, 1, p1)
	e.part(chunkedTestID, 0, p0) // replaces "stale"
	rec = e.commit(chunkedTestID, 3)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	// The same commit again (the launcher did not see the answer): done.
	if rec := e.commit(chunkedTestID, 3); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("repeated commit: %d %s", rec.Code, rec.Body.String())
	}
	// And a late part of it: done, not stored.
	if rec := e.part(chunkedTestID, 1, p1); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("late part: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(clientLogIncomingDir(e.pid, chunkedTestID)); err == nil {
		t.Errorf("the incoming folder is still there after the commit")
	}

	logs := playerClientLogs(database, e.pid, 10)
	want := redactClientLog([]byte(p0 + p1 + p2))
	if len(logs) != 1 || logs[0].Bytes != int64(len(want)) || logs[0].Seconds != 900 || logs[0].Version != "1.0.11" || logs[0].Stored <= 0 {
		t.Fatalf("rows: %+v (want %d bytes)", logs, len(want))
	}

	r := mux.NewRouter()
	registerAdminClientLogs(r)
	dl := httptest.NewRecorder()
	r.ServeHTTP(dl, httptest.NewRequest(http.MethodGet, "/client-logs/1", nil))
	body := dl.Body.String()
	if dl.Code != http.StatusOK || body != string(want) {
		t.Fatalf("download (%d):\n%q\nwant\n%q", dl.Code, body, want)
	}
	for _, gone := range []string{"eyJ", "DESKTOP-TEST01", "testwinuser", "stale"} {
		if strings.Contains(body, gone) {
			t.Errorf("%q is in the stored log", gone)
		}
	}
	gz := httptest.NewRecorder()
	r.ServeHTTP(gz, httptest.NewRequest(http.MethodGet, "/client-logs/1?gzip=1", nil))
	if gz.Code != http.StatusOK || gz.Header().Get("Content-Type") != "application/gzip" || !strings.HasSuffix(gz.Header().Get("Content-Disposition"), `.log.gz"`) {
		t.Fatalf("gzip download: %d %v", gz.Code, gz.Header())
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != string(want) {
		t.Fatalf("the stored file is not one stream of the whole log")
	}
}

// Limits: a part that expands too far, too many unfinished uploads, uploads
// left behind, a part sent for a log that is already stored.
func TestLauncherLogChunkedLimits(t *testing.T) {
	e := newChunkedLogEnv(t)

	bomb := gzipped(t, strings.Repeat("A", clientLogPartRaw+1))
	if rec := e.do(launcherLogPartHandler, "/launcher/log/part", "id="+chunkedTestID+"&n=0", e.token, bomb); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("part past %d bytes: %d, want 413", clientLogPartRaw, rec.Code)
	}
	if rec := e.do(launcherLogPartHandler, "/launcher/log/part", "id="+chunkedTestID+"&n=0", e.token, gzipped(t, "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty part: %d, want 400", rec.Code)
	}

	for i := 0; i < clientLogMaxIncoming; i++ {
		id := fmt.Sprintf("%032x", i+1)
		if rec := e.part(id, 0, "x\n"); rec.Code != http.StatusOK {
			t.Fatalf("upload %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := e.part(fmt.Sprintf("%032x", 99), 0, "x\n"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("one unfinished upload too many: %d, want 429", rec.Code)
	}
	// Another part of an unfinished upload is not a new one.
	if rec := e.part(fmt.Sprintf("%032x", 1), 1, "y\n"); rec.Code != http.StatusOK {
		t.Fatalf("second part of an upload: %d", rec.Code)
	}

	// Uploads nobody finished are removed after a day; a young one stays.
	old := clientLogIncomingDir(e.pid, fmt.Sprintf("%032x", 2))
	stale := time.Now().Add(-2 * clientLogIncomingKeep)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	pruneClientLogIncoming(time.Now())
	if _, err := os.Stat(old); err == nil {
		t.Errorf("a two-day-old unfinished upload was kept")
	}
	if _, err := os.Stat(clientLogIncomingDir(e.pid, fmt.Sprintf("%032x", 3))); err != nil {
		t.Errorf("a fresh unfinished upload was removed: %v", err)
	}
}

// The budget is disk bytes, in total and per player, newest kept.
func TestPruneClientLogsDiskBudget(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	oldDir, oldMax, oldPlayer := clientLogDir, clientLogMaxBytes, clientLogPlayerMaxBytes
	clientLogDir = t.TempDir()
	t.Cleanup(func() { clientLogDir, clientLogMaxBytes, clientLogPlayerMaxBytes = oldDir, oldMax, oldPlayer })
	// Each stored file is a gzip of 200 bytes that do not compress: ~230 on disk.
	noise := make([]byte, 200)
	for i := range noise {
		noise[i] = byte(i*131 + i*i*7)
	}
	add := func(user, name string, ageMinutes int) {
		rel := user + "/" + name + ".log.gz"
		if err := writeClientLogFile(filepath.Join(clientLogDir, rel), noise); err != nil {
			t.Fatal(err)
		}
		// bytes (the log's own size) is huge and must not count.
		if _, err := database.Exec(`INSERT INTO client_logs(user_id, uploaded_at, bytes, path) VALUES(?, datetime('now', ?), 999999999, ?)`,
			user, fmt.Sprintf("-%d minutes", ageMinutes), rel); err != nil {
			t.Fatal(err)
		}
	}
	add("a", "a1", 5)
	add("a", "a2", 4)
	add("a", "a3", 3)
	add("b", "b1", 2)
	add("b", "b2", 1)
	clientLogPlayerMaxBytes = 500 // two files of ~230 per player
	clientLogMaxBytes = 1 << 30
	pruneClientLogs(database)
	left := clientLogRows(database, `ORDER BY id`)
	if len(left) != 4 || left[0].path != "a/a2.log.gz" || left[1].path != "a/a3.log.gz" {
		t.Fatalf("per player: %+v, want a1 gone, a2 a3 b1 b2 left", left)
	}
	clientLogPlayerMaxBytes = 1 << 30
	clientLogMaxBytes = 500
	pruneClientLogs(database)
	left = clientLogRows(database, `ORDER BY id`)
	if len(left) != 2 || left[0].path != "b/b1.log.gz" || left[1].path != "b/b2.log.gz" {
		t.Fatalf("in total: %+v, want only the newest two (b1 b2)", left)
	}
	if _, err := os.Stat(filepath.Join(clientLogDir, "a/a3.log.gz")); err == nil {
		t.Errorf("the file of a pruned log is still on disk")
	}
}

// Old launchers keep working: the one-POST upload (and the new launcher's
// fallback for a server without parts, a body of several gzip members).
func TestLauncherLogOnePostStillWorks(t *testing.T) {
	e := newChunkedLogEnv(t)
	body := append(gzipped(t, "Log file open\r\n"+testClientLog), gzipped(t, "[launcher: 5 bytes of the log cut here]\r\nLog file closed\r\n")...)
	rec := e.do(launcherLogHandler, "/launcher/log", "modified=42&exit=0&version=1.0.10", e.token, body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("one POST: %d %s", rec.Code, rec.Body.String())
	}
	logs := playerClientLogs(currentMmogPlayerStateDB(), e.pid, 5)
	want := string(redactClientLog([]byte("Log file open\r\n" + testClientLog + "[launcher: 5 bytes of the log cut here]\r\nLog file closed\r\n")))
	if len(logs) != 1 || logs[0].Bytes != int64(len(want)) {
		t.Fatalf("rows %+v, want %d bytes", logs, len(want))
	}
	r := mux.NewRouter()
	registerAdminClientLogs(r)
	dl := httptest.NewRecorder()
	r.ServeHTTP(dl, httptest.NewRequest(http.MethodGet, "/client-logs/1", nil))
	if dl.Body.String() != want {
		t.Fatalf("download differs:\n%q", dl.Body.String())
	}
}
