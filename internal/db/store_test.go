package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostWorkerTokenDecodesFromSQLiteJSONButNeverSerializes(t *testing.T) {
	const token = "super-secret-worker-token"
	raw := `[{"id":2,"name":"Pi4","endpoint":"tcp://192.168.1.250:2375","enabled":1,"worker_token":"` + token + `","created_at":"2026-08-10T08:42:23Z"}]`
	var hosts []Host
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].WorkerToken != token {
		t.Fatalf("worker token was not restored from sqlite3 JSON: %+v", hosts)
	}

	encoded, err := json.Marshal(hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "worker_token") {
		t.Fatalf("worker token leaked through JSON serialization: %s", encoded)
	}
}

func TestBackupUsesSQLiteConsistentVacuumInto(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sqlite.log")
	script := filepath.Join(dir, "sqlite3")
	body := `#!/bin/sh
cat >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	dbPath := filepath.Join(dir, "watchtower-ui.db")
	if err := os.WriteFile(dbPath, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backups", "snapshot.db")
	s := New(dbPath)
	if err := s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "VACUUM INTO") || !strings.Contains(got, backup) {
		t.Fatalf("backup did not use VACUUM INTO for destination: %s", got)
	}
}
