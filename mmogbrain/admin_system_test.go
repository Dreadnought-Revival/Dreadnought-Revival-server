package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminSystemRequiresTheKey(t *testing.T) {
	r := adminTestRouter()
	for _, key := range []string{"", "wrong"} {
		if rec := adminGet(r, "/admin/api/system", key); rec.Code != http.StatusForbidden {
			t.Errorf("system with key %q: %d", key, rec.Code)
		}
		if rec := adminPost(r, "/admin/api/system/backup", key, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("backup with key %q: %d", key, rec.Code)
		}
	}
}

func TestAdminSystemAnswersWithoutDeadlock(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	r := adminTestRouter()
	done := make(chan int, 1)
	var body string
	go func() {
		rec := adminGet(r, "/admin/api/system", "test-admin-key")
		body = rec.Body.String()
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("system: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("system did not answer within 3 s")
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"host", "process", "database", "backups", "certs", "tls", "logs", "config"} {
		if _, ok := d[k]; !ok {
			t.Errorf("missing part %s", k)
		}
	}
	db := d["database"].(map[string]any)
	if db["file"] != "mmog.db" {
		t.Errorf("database file = %v, want the bare name", db["file"])
	}
	if strings.Contains(body, os.TempDir()+"/") && strings.Contains(body, "TestAdminSystem") {
		t.Error("a full path leaked")
	}
}

func TestAdminSystemConfigNeverLeaksSecrets(t *testing.T) {
	useTempMmogPlayerStateDB(t)
	t.Setenv("FOO_SECRET", "s3cr3t-value-aaa")
	t.Setenv("X_KEY", "k3y-value-bbb")
	t.Setenv("DN_JWT_THING", "jwt-value-ccc")
	t.Setenv("PLAYERS_PER_MATCH", "4")
	t.Setenv("PUBLIC_HOST", "example-host.invalid")
	t.Setenv("SERVER_IP", "203.0.113.9")
	t.Setenv("GAME_MGR_URL", "http://198.51.100.7:8085")
	body := adminGet(adminTestRouter(), "/admin/api/system", "test-admin-key").Body.String()
	for _, bad := range []string{"s3cr3t-value-aaa", "k3y-value-bbb", "jwt-value-ccc", "FOO_SECRET", "X_KEY", "example-host.invalid", "203.0.113.9", "198.51.100.7"} {
		if strings.Contains(body, bad) {
			t.Errorf("reply contains %q", bad)
		}
	}
	if !strings.Contains(body, `"PLAYERS_PER_MATCH"`) || !strings.Contains(body, `"value":"4"`) {
		t.Error("allow-listed setting missing")
	}
	for _, n := range append(append([]string{}, systemConfigValues...), systemConfigPresence...) {
		if systemSecretName(n) {
			t.Errorf("allow-list holds secret-looking name %s", n)
		}
	}
	t.Setenv("GAME_MGR_URL", "http://127.0.0.1:8085")
	if body := adminGet(adminTestRouter(), "/admin/api/system", "test-admin-key").Body.String(); !strings.Contains(body, "127.0.0.1:8085") {
		t.Error("loopback control plane should show host:port")
	}
}

func fakeBackupRoot(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "backup.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := systemRootDir
	systemRootDir = func() string { return root }
	t.Cleanup(func() { systemRootDir = old })
	return root
}

func TestAdminSystemBackupRunsOnceAndAudits(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	root := fakeBackupRoot(t, "#!/bin/bash\nsleep 1\necho fake backup done\nmkdir -p backups; echo x > backups/20260101T000000Z.tar.gz\n")
	t.Setenv("BACKUP_DIR", "")
	r := adminTestRouter()
	first := make(chan string, 1)
	go func() { first <- adminPost(r, "/admin/api/system/backup", "test-admin-key", `{}`).Body.String() }()
	time.Sleep(300 * time.Millisecond)
	if rec := adminPost(r, "/admin/api/system/backup", "test-admin-key", `{}`); rec.Code != http.StatusConflict {
		t.Errorf("concurrent backup: %d, want 409", rec.Code)
	}
	body := <-first
	if !strings.Contains(body, "fake backup done") || !strings.Contains(body, `"ok":true`) {
		t.Errorf("reply: %s", body)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='backup run'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d (%v), want 1", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backups", "20260101T000000Z.tar.gz")); err != nil {
		t.Error("script did not run in the root")
	}
	oldWD := systemWorkDir
	systemWorkDir = func() string { return root }
	defer func() { systemWorkDir = oldWD }()
	b := systemBackups()
	if b["count"] != 1 || b["script"] != true {
		t.Errorf("backups listing: %v", b)
	}
}

func TestAdminSystemBackupFailureAndMissingScript(t *testing.T) {
	database := useTempMmogPlayerStateDB(t)
	fakeBackupRoot(t, "#!/bin/bash\necho broken >&2\nexit 3\n")
	rec := adminPost(adminTestRouter(), "/admin/api/system/backup", "test-admin-key", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "broken") {
		t.Errorf("failed script: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action='backup run'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows %d", n)
	}
	old := systemRootDir
	systemRootDir = func() string { return "" }
	defer func() { systemRootDir = old }()
	rec = adminPost(adminTestRouter(), "/admin/api/system/backup", "test-admin-key", `{}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "backup.sh") {
		t.Errorf("missing script: %d %s", rec.Code, rec.Body.String())
	}
}
