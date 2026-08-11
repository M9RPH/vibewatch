package db

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerEventsUseBoundedJSONLAndFilterExecNoise(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	dir := t.TempDir()
	s := New(filepath.Join(dir, "vibewatch.db"))
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	normal := `{"Type":"container","Action":"start","Actor":{"Attributes":{"name":"paperless"}}}`
	noisy := `{"Type":"container","Action":"exec_die","Actor":{"Attributes":{"name":"paperless","exitCode":"0"}}}`
	if err := s.AddDockerEvent(context.Background(), 1, normal); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDockerEvent(context.Background(), 1, noisy); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDockerEvent(context.Background(), 2, `{"Type":"container","Action":"stop"}`); err != nil {
		t.Fatal(err)
	}
	all, err := s.DockerEvents(context.Background(), 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 retained events, got %d", len(all))
	}
	host1, err := s.DockerEvents(context.Background(), 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(host1) != 1 || !strings.Contains(host1[0].RawJSON, "paperless") {
		t.Fatalf("unexpected host-filtered events: %#v", host1)
	}
	b, err := os.ReadFile(s.EventPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e DockerEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("invalid JSONL record: %v", err)
		}
	}
}

func TestRepairDockerEventCorruptionPreservesCoreTables(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "vibewatch.db")
	s := New(path)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.exec(ctx, `INSERT INTO hosts(name,endpoint,enabled,worker_token,created_at) VALUES('Pi5','tcp://192.168.1.5:2375',1,'secret','2026-08-11T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "sentinel", "preserve-me"); err != nil {
		t.Fatal(err)
	}
	if err := s.exec(ctx, `CREATE TABLE docker_events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, host_id INTEGER NOT NULL, raw_json TEXT NOT NULL); CREATE INDEX idx_events_host ON docker_events(host_id,id DESC);`); err != nil {
		t.Fatal(err)
	}
	if err := s.exec(ctx, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<400) INSERT INTO docker_events(ts,host_id,raw_json) SELECT '2026-08-11T00:00:00Z',1,'{"Action":"start","pad":"abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789"}' FROM n;`); err != nil {
		t.Fatal(err)
	}
	if err := s.exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatal(err)
	}
	root, err := s.scalarInt(ctx, `SELECT rootpage FROM sqlite_master WHERE name='docker_events';`)
	if err != nil {
		t.Fatal(err)
	}
	pageSize, err := s.scalarInt(ctx, `PRAGMA page_size;`)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, 32), (root-1)*pageSize); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	result, integrityErr := s.IntegrityCheck(ctx)
	if integrityErr == nil && strings.TrimSpace(result) == "ok" {
		t.Fatal("expected synthetic docker_events corruption")
	}
	repaired, backup, err := s.RepairDockerEventCorruption(ctx, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("repair failed: %v\nintegrity=%s", err, result)
	}
	if !repaired || backup == "" {
		t.Fatalf("expected repair and preserved backup, got repaired=%v backup=%q", repaired, backup)
	}
	check, err := s.IntegrityCheck(ctx)
	if err != nil || strings.TrimSpace(check) != "ok" {
		t.Fatalf("recovered DB is not healthy: %q err=%v", check, err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Name != "Pi5" {
		t.Fatalf("host state not preserved: %#v err=%v", hosts, err)
	}
	if got := s.Setting(ctx, "sentinel", ""); got != "preserve-me" {
		t.Fatalf("setting not preserved: %q", got)
	}
}
