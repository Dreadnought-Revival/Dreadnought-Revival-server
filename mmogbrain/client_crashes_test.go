package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gorilla/mux"
)

func wide(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// The scrub keeps every byte where it is (a minidump is offsets into itself)
// and removes tokens, user folders and machine names in both encodings.
func TestScrubCrashData(t *testing.T) {
	tok := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	var in []byte
	in = append(in, "\x01\x02 -token="+tok+" C:\\Users\\Jane Doe\\AppData\\x \x00"...)
	in = append(in, wide("Z:/users/Jane Doe/Saved\x00 Bearer "+tok+"\x00")...)
	in = append(in, "LogInit: Computer: DESKTOP-ABC\r\nLogInit: User: jane\r\n"...)
	in = append(in, wide("USERNAME=jane\x00COMPUTERNAME=DESKTOP-ABC\x00PATH=C:\\Windows\x00")...)
	in = append(in, wide("G:\\Users\\Jörg\\x\x00")...)
	in = append(in, "<MachineId>0123456789ABCDEF0123456789ABCDEF</MachineId><EpicAccountId>abcdef0123456789abcdef0123456789</EpicAccountId>"...)
	in = append(in, wide("<LoginId>9f8e7d6c5b4a</LoginId>")...)
	in = append(in, "eyJshort keep \xff\xfe"...)
	out := append([]byte(nil), in...)
	scrubCrashData(out, nil)

	if len(out) != len(in) {
		t.Fatalf("length %d -> %d", len(in), len(out))
	}
	for _, gone := range [][]byte{[]byte(tok), wide(tok), []byte("Jane Doe"), wide("Jane Doe"), []byte("DESKTOP-ABC"),
		wide("DESKTOP-ABC"), []byte("User: jane"), wide("=jane"), wide("Jörg"), []byte("0123456789ABCDEF"), []byte("abcdef0123"),
		wide("9f8e7d6c5b4a")} {
		if bytes.Contains(out, gone) {
			t.Errorf("still holds %q", gone)
		}
	}
	for _, kept := range [][]byte{[]byte("C:\\Users\\********\\AppData"), wide("Z:/users/********/Saved"), wide("PATH=C:\\Windows"),
		[]byte("eyJshort keep \xff\xfe"), []byte("LogInit: Computer: ***********\r\n"), wide("G:\\Users\\****\\x"),
		[]byte("<MachineId>********************************</MachineId>"), wide("<LoginId>************</LoginId>")} {
		if !bytes.Contains(out, kept) {
			t.Errorf("lost %q", kept)
		}
	}
	// Extra needles (the launcher's own names), case-insensitive, both widths.
	b := append([]byte("my pc is Gamer-PC, "), wide("GAMER-pc")...)
	scrubCrashData(b, []string{"gamer-pc"})
	if bytes.Contains(bytes.ToLower(b), []byte("gamer-pc")) || bytes.Contains(b, wide("GAMER-pc")) {
		t.Errorf("needle left: %q", b)
	}
}

func crashZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *chunkedLogEnv) crashPart(id string, n int, data []byte, report string) *httptest.ResponseRecorder {
	return e.do(launcherCrashPartHandler, "/launcher/crash/part", fmt.Sprintf("id=%s&n=%d&report=%s", id, n, report), e.token, gzipped(e.t, string(data)))
}

func (e *chunkedLogEnv) crashCommit(id string, parts int, report string) *httptest.ResponseRecorder {
	return e.do(launcherCrashCommitHandler, "/launcher/crash/commit",
		fmt.Sprintf("id=%s&parts=%d&report=%s&modified=1763900000&exit=-1073741819&seconds=600&version=1.0.13", id, parts, report), e.token, nil)
}

// A report in two parts is joined, scrubbed again here, stored once, and
// downloads as a zip with the same files.
func TestLauncherCrashUpload(t *testing.T) {
	e := newChunkedLogEnv(t)
	oldDir := clientCrashDir
	clientCrashDir = t.TempDir()
	t.Cleanup(func() { clientCrashDir = oldDir })

	dump := append([]byte("MDMP\x93\xa7"), wide("C:\\Users\\Jane\\DreadGame\x00")...)
	dump = append(dump, bytes.Repeat([]byte{0xAB}, 3000)...)
	report := "AppCrash_UE4-DreadGame_1a2b3c_4d5e6f"
	z := crashZip(t, map[string][]byte{"UE4Minidump.dmp": dump, "Report.wer": wide("AppPath=C:\\Users\\Jane\\x.exe\r\n")})
	half := len(z) / 2

	if rec := e.crashPart(chunkedTestID, 1, z[half:], report); rec.Code != http.StatusOK {
		t.Fatalf("part 1: %d %s", rec.Code, rec.Body)
	}
	if rec := e.crashCommit(chunkedTestID, 2, report); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"missing":[0]`) {
		t.Fatalf("commit without part 0: %d %s", rec.Code, rec.Body)
	}
	if rec := e.crashPart(chunkedTestID, 0, z[:half], report); rec.Code != http.StatusOK {
		t.Fatalf("part 0: %d %s", rec.Code, rec.Body)
	}
	rec := e.crashCommit(chunkedTestID, 2, report)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}
	if rec := e.crashCommit(chunkedTestID, 2, report); !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Errorf("second commit: %s", rec.Body)
	}
	if rec := e.crashPart("fedcba9876543210fedcba9876543210", 0, z, report); !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Errorf("same report again: %s", rec.Body)
	}

	rows := playerClientCrashes(currentMmogPlayerStateDB(), e.pid, 10)
	if len(rows) != 1 || rows[0].Files != 2 || rows[0].Report != report || rows[0].ExitCode != -1073741819 || rows[0].Seconds != 600 {
		t.Fatalf("rows %+v", rows)
	}

	r := mux.NewRouter()
	registerAdminClientCrashes(r)
	dl := httptest.NewRecorder()
	r.ServeHTTP(dl, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/client-crashes/%d", rows[0].ID), nil))
	if dl.Code != http.StatusOK || dl.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", dl.Code, dl.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(dl.Body.Bytes()), int64(dl.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		if bytes.Contains(data, wide("Jane")) {
			t.Errorf("%s still names the user", f.Name)
		}
		if f.Name == "UE4Minidump.dmp" && len(data) != len(dump) {
			t.Errorf("dump %d bytes, sent %d", len(data), len(dump))
		}
	}
}

// Junk is refused: a bad report name, a zip with a path in a file name, not a zip.
func TestLauncherCrashUploadRefusesJunk(t *testing.T) {
	e := newChunkedLogEnv(t)
	oldDir := clientCrashDir
	clientCrashDir = t.TempDir()
	t.Cleanup(func() { clientCrashDir = oldDir })

	if rec := e.crashPart(chunkedTestID, 0, []byte("x"), "..%2Fx"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad report name: %d", rec.Code)
	}
	for i, body := range [][]byte{crashZip(t, map[string][]byte{"../evil.dmp": []byte("x")}), []byte("not a zip at all")} {
		id := fmt.Sprintf("%031x%d", 0, i)
		name := fmt.Sprintf("AppCrash_UE4-DreadGame_%d", i)
		if rec := e.crashPart(id, 0, body, name); rec.Code != http.StatusOK {
			t.Fatalf("part: %d %s", rec.Code, rec.Body)
		}
		if rec := e.crashCommit(id, 1, name); rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: commit %d %s", i, rec.Code, rec.Body)
		}
	}
	if n := len(playerClientCrashes(currentMmogPlayerStateDB(), e.pid, 10)); n != 0 {
		t.Errorf("%d rows stored", n)
	}
}
