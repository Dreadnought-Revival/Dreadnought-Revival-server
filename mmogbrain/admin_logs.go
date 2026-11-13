package main

// Log viewing for the dashboard: only the files adminLogPath allows.

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type battleLogFile struct {
	path string
	name string
	mod  time.Time
	size int64
}

func newestBattleLogs(n int) []battleLogFile {
	entries, err := os.ReadDir(filepath.Join("run", "battle-logs"))
	if err != nil {
		return nil
	}
	var files []battleLogFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, battleLogFile{path: filepath.Join("run", "battle-logs", e.Name()), name: e.Name(), mod: info.ModTime(), size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > n {
		files = files[:n]
	}
	return files
}

func adminAPIBattleLogs(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		Name     string `json:"name"`
		Modified string `json:"modified"`
		Size     int64  `json:"size"`
		Crash    string `json:"crash,omitempty"`
		Mode     string `json:"mode,omitempty"`
	}
	out := []row{}
	for _, f := range newestBattleLogs(60) {
		tail := tailFile(f.path, 96*1024)
		r := row{Name: f.name, Modified: f.mod.UTC().Format("2006-01-02 15:04:05"), Size: f.size}
		switch {
		case strings.Contains(tail, "EXCEPTION_STACK_OVERFLOW"):
			r.Crash = "stack overflow"
		case strings.Contains(tail, "Unhandled Exception"), strings.Contains(tail, "Unhandled page fault"):
			r.Crash = "access violation"
		}
		if head := headFile(f.path, 32*1024); head != "" {
			if i := strings.Index(head, "?game="); i >= 0 {
				mode := head[i+6:]
				if j := strings.IndexAny(mode, "?& \"\n"); j > 0 {
					mode = mode[:j]
				}
				r.Mode = mode
			}
		}
		out = append(out, r)
	}
	writeAdminJSON(w, map[string]any{"logs": out})
}

// adminLogPath maps a log source name to a file. Only these files can be
// read: nothing from the request becomes a path except a battle-log NAME,
// which must be a plain file name inside run/battle-logs.
func adminLogPath(src, battle string) (string, bool) {
	gameDir := filepath.Dir(os.Getenv("GAME_BINARY"))
	switch src {
	case "mmogbrain":
		return filepath.Join("run", "mmogbrain.log"), true
	case "firmament":
		return "mmogbrain.log", true
	case "dn-dedicated":
		return filepath.Join("run", "dn-dedicated.log"), true
	case "gateway", "auth-server", "legacy-api", "master-server":
		return filepath.Join("run", src+".log"), true
	case "mod":
		if gameDir == "." {
			return "", false
		}
		return filepath.Join(gameDir, "dn_host_loadout.log"), true
	case "crashprobe":
		if gameDir == "." {
			return "", false
		}
		return filepath.Join(gameDir, "dn_host_stackprobe.log"), true
	case "battle":
		if battle == "" || battle != filepath.Base(battle) || !strings.HasPrefix(battle, "battle-") || !strings.HasSuffix(battle, ".log") {
			return "", false
		}
		return filepath.Join("run", "battle-logs", battle), true
	}
	return "", false
}

// adminLogMaxGrep bounds the text filter; adminLogReadBytes bounds how much
// of the file's end is read for one request.
const (
	adminLogMaxGrep   = 200
	adminLogReadBytes = 2 * 1024 * 1024
)

// adminAPILogs returns the last `lines` (default 300, 1..2000) lines of one
// allowed log. Filters: grep (or q; case-insensitive text, at most 200
// characters, 400 otherwise); noise=1 keeps the lines the viewer hides by
// default. Only the last 2 MB of the file is read; "truncated" says the file
// is longer, "matched" how many lines passed the filter before the last
// `lines` were cut, and size/modified describe the file.
func adminAPILogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path, ok := adminLogPath(q.Get("src"), q.Get("name"))
	if !ok {
		http.Error(w, "unknown log", http.StatusBadRequest)
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	if lines <= 0 || lines > 2000 {
		lines = 300
	}
	grep := q.Get("grep")
	if grep == "" {
		grep = q.Get("q")
	}
	grep = strings.TrimSpace(grep)
	if len(grep) > adminLogMaxGrep {
		writeAdminError(w, http.StatusBadRequest, "filter too long (200 characters at most)")
		return
	}
	filter := strings.ToLower(grep)
	hideNoise := q.Get("noise") != "1"
	text := tailFile(path, adminLogReadBytes)
	var picked []string
	matched := 0
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if hideNoise && (strings.Contains(line, "NtQueryInformationProcess") || strings.Contains(line, "dispatch_exception L\"") ||
			strings.Contains(line, "OutputDebugStringW") || strings.Contains(line, "raw bytes received") || strings.Contains(line, "decrypted application bytes")) {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			continue
		}
		matched++
		if len(line) > 600 {
			line = line[:600] + " …"
		}
		picked = append(picked, line)
	}
	if len(picked) > lines {
		picked = picked[len(picked)-lines:]
	}
	if picked == nil {
		picked = []string{}
	}
	out := map[string]any{"file": filepath.Base(path), "lines": picked, "matched": matched}
	if info, err := os.Stat(path); err == nil {
		out["size"] = info.Size()
		out["modified"] = info.ModTime().UTC().Format("2006-01-02 15:04:05")
		out["truncated"] = info.Size() > adminLogReadBytes
		out["exists"] = true
	} else {
		out["exists"] = false
	}
	writeAdminJSON(w, out)
}

func tailFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, max))
	s := string(b)
	if start > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s
}

func headFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, max))
	return string(b)
}
