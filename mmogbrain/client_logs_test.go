package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dreadnought-Revival/Dreadnought-Revival-server/mmogbrain/protocol"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
)

// A log as the client writes it (names and token made up).
const testClientLog = "LogInit: Command line: /Game/Maps/Launch_P -noeac\r\n" +
	"[2026.11.17-07.59.31:453][  0]LogInit: Computer: DESKTOP-TEST01\r\n" +
	"[2026.11.17-07.59.31:453][  0]LogInit: User: testwinuser\r\n" +
	"[2026.11.17-07.59.31:500][  0]LogInit: Base directory: C:\\Users\\testwinuser\\Games\\Dreadnought\\\r\n" +
	"LogPaths: C:/Users/Test Win User/AppData/Local/DreadGame/Saved/\r\n" +
	"[2026.11.17-07.59.42:735][ 51]LogWebServicesPlugin: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl\r\n" +
	"[2026.11.17-07.59.42:736][ 51]LogWebServicesPlugin: cut eyJhbGciOiJIUzI1\r\n" +
	"[2026.11.17-07.59.43:487][ 96]LogWebServicesPlugin: Authorization: Session a0c2e098-0000-0000-0000-000000000000, TestPilot\r\n"

func TestRedactClientLog(t *testing.T) {
	got := string(redactClientLog([]byte(testClientLog)))
	for _, gone := range []string{"DESKTOP-TEST01", "testwinuser", "Test Win User", "eyJ", "c2lnbmF0dXJl"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is still in the log:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"LogInit: Computer: <removed>\r\n", "LogInit: User: <removed>\r\n",
		`C:\Users\<user>\Games\Dreadnought\`, "C:/Users/<user>/AppData/Local/DreadGame/Saved/",
		"Authorization: Bearer <token removed>\r\n", "Session a0c2e098-0000-0000-0000-000000000000, TestPilot", "-noeac"} {
		if !strings.Contains(got, kept) {
			t.Errorf("want %q in the log:\n%s", kept, got)
		}
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

// Upload, retry, flood, junk, and the admin download.
func TestLauncherLogUpload(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	oldDir := clientLogDir
	clientLogDir = t.TempDir()
	t.Cleanup(func() { clientLogDir = oldDir })
	clientLogMu.Lock()
	clientLogLast = map[string]time.Time{}
	clientLogMu.Unlock()
	oldSecret := mmogJWTSecret()
	setMmogJWTSecret([]byte("test-secret"))
	t.Cleanup(func() { setMmogJWTSecret(oldSecret) })
	const user = "4d1f0c2a-6b7e-4c55-9a10-3f2b8e9d7c61"
	pid := protocol.NormalizePlayerPID(user)
	token := mustSignTestJWT(t, jwt.MapClaims{"sub": user, "user_id": user, "iss": protocol.GatewayJWTIssuer,
		"aud": "launcher", "realm": "dreadnought.pc-us", "exp": time.Now().Add(time.Hour).Unix()})

	post := func(query, tok string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/launcher/log?"+query, bytes.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		launcherLogHandler(rec, req)
		return rec
	}

	if rec := post("modified=1", "", gzipped(t, testClientLog)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", rec.Code)
	}
	if rec := post("modified=1", "junk", gzipped(t, testClientLog)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d, want 401", rec.Code)
	}
	if rec := post("modified=1", token, []byte("not gzip")); rec.Code != http.StatusBadRequest {
		t.Fatalf("plain body: %d, want 400", rec.Code)
	}
	clientLogMu.Lock()
	clientLogLast = map[string]time.Time{}
	clientLogMu.Unlock()

	rec := post("modified=1763366371&exit=0&seconds=754&version=1.0.12", token, gzipped(t, testClientLog))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	// The same log again (the launcher retrying): done, not stored twice.
	rec = post("modified=1763366371", token, gzipped(t, testClientLog))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	// Another log straight after: too soon.
	if rec := post("modified=1763366999", token, gzipped(t, testClientLog)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second upload within %s: %d, want 429", clientLogMinGap, rec.Code)
	}

	logs := playerClientLogs(database, pid, 10)
	if len(logs) != 1 || logs[0].Seconds != 754 || logs[0].ExitCode != 0 || logs[0].Crashed || logs[0].Version != "1.0.12" {
		t.Fatalf("rows: %+v", logs)
	}

	r := mux.NewRouter()
	registerAdminClientLogs(r)
	dl := httptest.NewRecorder()
	r.ServeHTTP(dl, httptest.NewRequest(http.MethodGet, "/client-logs/1", nil))
	if dl.Code != http.StatusOK {
		t.Fatalf("download: %d %s", dl.Code, dl.Body.String())
	}
	if body := dl.Body.String(); strings.Contains(body, "eyJ") || strings.Contains(body, "DESKTOP-TEST01") || !strings.Contains(body, "TestPilot") {
		t.Fatalf("stored log not redacted as intended:\n%s", body)
	}
	if cd := dl.Header().Get("Content-Disposition"); !strings.Contains(cd, "DreadGame-"+pid[:8]) {
		t.Errorf("Content-Disposition %q", cd)
	}
}

// Old logs are deleted, files included (the size budgets: see
// TestPruneClientLogsDiskBudget).
func TestPruneClientLogs(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	oldDir := clientLogDir
	clientLogDir = t.TempDir()
	t.Cleanup(func() { clientLogDir = oldDir })
	for i, at := range []string{"2000-01-01 00:00:00", "now", "now"} {
		rel := "p/" + string(rune('a'+i)) + ".log.gz"
		if err := writeClientLogFile(clientLogDir+"/"+rel, []byte("x")); err != nil {
			t.Fatal(err)
		}
		q := `INSERT INTO client_logs(user_id, uploaded_at, bytes, path) VALUES('p', ?, 100, ?)`
		if at == "now" {
			q = `INSERT INTO client_logs(user_id, uploaded_at, bytes, path) VALUES('p', datetime('now', ?), 100, ?)`
			at = "-" + string(rune('0'+i)) + " minutes"
		}
		if _, err := database.Exec(q, at, rel); err != nil {
			t.Fatal(err)
		}
	}
	pruneClientLogs(database)
	left := clientLogRows(database, `ORDER BY id`)
	if len(left) != 2 || left[0].ID != 2 || left[1].ID != 3 {
		t.Fatalf("left %+v, want ids 2 and 3 (1 too old)", left)
	}
	if _, err := os.Stat(clientLogDir + "/p/a.log.gz"); err == nil {
		t.Errorf("the file of the old log is still on disk")
	}
}
