package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adminLogsWorkdir runs the test in a temp dir holding run/mmogbrain.log and
// two battle logs (the allow-list is relative to the working directory).
func adminLogsWorkdir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join("run", "battle-logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		if i%100 == 0 {
			b.WriteString(" ERROR needle")
		}
		b.WriteString("\n")
	}
	b.WriteString("raw bytes received noise\n")
	_ = os.WriteFile(filepath.Join("run", "mmogbrain.log"), []byte(b.String()), 0o600)
	_ = os.WriteFile(filepath.Join("run", "battle-logs", "battle-a-port7900.log"), []byte("start ?game=TDM?x\nEXCEPTION_STACK_OVERFLOW\n"), 0o600)
	_ = os.WriteFile(filepath.Join("run", "battle-logs", "battle-b-port7901.log"), []byte("start ?game=Onslaught\nfine\n"), 0o600)
	_ = os.WriteFile(filepath.Join("run", "battle-logs", "notes.txt"), []byte("x"), 0o600)
}

type adminLogsResp struct {
	File      string   `json:"file"`
	Lines     []string `json:"lines"`
	Matched   int      `json:"matched"`
	Size      int64    `json:"size"`
	Modified  string   `json:"modified"`
	Exists    bool     `json:"exists"`
	Truncated bool     `json:"truncated"`
}

func adminLogsGet(t *testing.T, query string, want int) adminLogsResp {
	t.Helper()
	rec := adminGet(adminTestRouter(), "/admin/api/logs?"+query, "test-admin-key")
	if rec.Code != want {
		t.Fatalf("%s: %d %s, want %d", query, rec.Code, rec.Body.String(), want)
	}
	var out adminLogsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestAdminLogsGrepAndBounds(t *testing.T) {
	adminLogsWorkdir(t)
	all := adminLogsGet(t, "src=mmogbrain", http.StatusOK)
	if len(all.Lines) != 300 || !all.Exists || all.Size == 0 || all.Modified == "" || all.File != "mmogbrain.log" {
		t.Fatalf("default: %d lines, %+v", len(all.Lines), all)
	}
	for _, l := range all.Lines {
		if strings.Contains(l, "raw bytes received") {
			t.Fatal("noise must be hidden by default")
		}
	}
	if n := len(adminLogsGet(t, "src=mmogbrain&noise=1&lines=5", http.StatusOK).Lines); n != 5 {
		t.Errorf("lines=5: %d", n)
	}
	for _, q := range []string{"lines=999999", "lines=-3", "lines=abc"} {
		if n := len(adminLogsGet(t, "src=mmogbrain&"+q, http.StatusOK).Lines); n != 300 {
			t.Errorf("%s: %d lines, want the 300 default", q, n)
		}
	}
	g := adminLogsGet(t, "src=mmogbrain&grep=NEEDLE&lines=2000", http.StatusOK)
	if len(g.Lines) != 30 || g.Matched != 30 {
		t.Errorf("grep: %d lines, matched %d, want 30", len(g.Lines), g.Matched)
	}
	g = adminLogsGet(t, "src=mmogbrain&grep=needle&lines=10", http.StatusOK)
	if len(g.Lines) != 10 || g.Matched != 30 {
		t.Errorf("grep with a cut: %d lines, matched %d", len(g.Lines), g.Matched)
	}
	if g := adminLogsGet(t, "src=mmogbrain&q=needle&lines=10", http.StatusOK); len(g.Lines) != 10 {
		t.Errorf("the old q= still filters: %d", len(g.Lines))
	}
	// A filter of exactly 200 characters is fine, 201 is refused.
	adminLogsGet(t, "src=mmogbrain&grep="+strings.Repeat("a", 200), http.StatusOK)
	adminLogsGet(t, "src=mmogbrain&grep="+strings.Repeat("a", 201), http.StatusBadRequest)
	// A missing file answers with exists=false, not an error.
	if m := adminLogsGet(t, "src=gateway", http.StatusOK); m.Exists || len(m.Lines) != 0 {
		t.Errorf("missing file: %+v", m)
	}
}

// The new parameters never reach the file system: only a plain battle-*.log
// name inside run/battle-logs is readable.
func TestAdminLogsAllowListStaysStrict(t *testing.T) {
	adminLogsWorkdir(t)
	for _, q := range []string{"src=../x", "src=mmogbrain/../../etc/passwd", "src=", "src=battle", "src=battle&name=notes.txt",
		"src=battle&name=battle-a-port7900.log/../../mmogbrain.log", "src=battle&name=..%2Fbattle-a.log", "src=battle&name=%2Fetc%2Fpasswd",
		"src=battle&name=battle-.log%00"} {
		adminLogsGet(t, q, http.StatusBadRequest)
	}
	// A name given for a fixed source is ignored, not used as a path.
	if g := adminLogsGet(t, "src=mmogbrain&name=../../etc/passwd", http.StatusOK); g.File != "mmogbrain.log" {
		t.Errorf("name with a fixed source: %+v", g)
	}
	// grep/lines/noise never change which file is read.
	if g := adminLogsGet(t, "src=battle&name=battle-a-port7900.log&grep=EXCEPTION&lines=5&noise=1", http.StatusOK); g.File != "battle-a-port7900.log" || len(g.Lines) != 1 {
		t.Errorf("battle log: %+v", g)
	}
}

func TestAdminBattleLogsList(t *testing.T) {
	adminLogsWorkdir(t)
	rec := adminGet(adminTestRouter(), "/admin/api/battle-logs", "test-admin-key")
	var out struct {
		Logs []struct {
			Name, Modified, Crash, Mode string
			Size                        int64
		} `json:"logs"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Logs) != 2 {
		t.Fatalf("battle-logs: %d %s", rec.Code, rec.Body.String())
	}
	for _, l := range out.Logs {
		switch l.Name {
		case "battle-a-port7900.log":
			if l.Crash != "stack overflow" || l.Mode != "TDM" || l.Size == 0 {
				t.Errorf("a: %+v", l)
			}
		case "battle-b-port7901.log":
			if l.Crash != "" || l.Mode != "Onslaught" {
				t.Errorf("b: %+v", l)
			}
		default:
			t.Errorf("unexpected %q", l.Name)
		}
		if len(l.Modified) != 19 {
			t.Errorf("modified %q is not YYYY-MM-DD HH:MM:SS", l.Modified)
		}
	}
}

func TestAdminLogsNeedTheKey(t *testing.T) {
	adminLogsWorkdir(t)
	for _, p := range []string{"/admin/api/logs?src=mmogbrain&grep=x&lines=5", "/admin/api/battle-logs"} {
		for _, key := range []string{"", "wrong"} {
			if rec := adminGet(adminTestRouter(), p, key); rec.Code != http.StatusForbidden {
				t.Errorf("%s with %q: %d", p, key, rec.Code)
			}
		}
	}
}
