package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireSQLite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
}

func TestFreshV2SchemaDoesNotCreateLegacySchedules(t *testing.T) {
	requireSQLite(t)
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	exists, err := s.tableExists(ctx, "schedules")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("fresh V2 databases must not create the obsolete V0.1 schedules table")
	}

	// Host deletion must not depend on the optional legacy table being present.
	hostID, err := s.CreateHost(ctx, "local", "unix:///var/run/docker.sock", "token", Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost(ctx, hostID); err != nil {
		t.Fatalf("delete host on fresh schema: %v", err)
	}
}

func TestLegacySchedulesRemainUpgradeCompatibleAndAreCleanedWithHost(t *testing.T) {
	requireSQLite(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vibewatch.db")
	legacy := `CREATE TABLE schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron TEXT NOT NULL, action TEXT NOT NULL, host_id INTEGER NOT NULL, containers TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT NOT NULL DEFAULT '');
INSERT INTO schedules(name,cron,action,host_id) VALUES('Legacy nightly','0 4 * * *','update',1);`
	if out, err := exec.Command("sqlite3", path, legacy).CombinedOutput(); err != nil {
		t.Fatalf("create legacy schedule table: %v: %s", err, out)
	}

	s := New(path)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	exists, err := s.tableExists(ctx, "schedules")
	if err != nil || !exists {
		t.Fatalf("legacy schedules table must be retained on upgrade: exists=%v err=%v", exists, err)
	}
	count, err := s.scalarInt(ctx, `SELECT COUNT(*) FROM schedules WHERE host_id=1;`)
	if err != nil || count != 1 {
		t.Fatalf("legacy schedule row was not preserved: count=%d err=%v", count, err)
	}

	// Create a host with id 1 in the newly initialized host table, then ensure
	// removing that host also removes its dormant legacy schedule rows.
	hostID, err := s.CreateHost(ctx, "legacy-host", "tcp://192.0.2.10:2375", "token", Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	if hostID != 1 {
		t.Fatalf("expected first host id 1, got %d", hostID)
	}
	if err := s.DeleteHost(ctx, hostID); err != nil {
		t.Fatal(err)
	}
	count, err = s.scalarInt(ctx, `SELECT COUNT(*) FROM schedules WHERE host_id=1;`)
	if err != nil || count != 0 {
		t.Fatalf("legacy schedules were not cleaned with host: count=%d err=%v", count, err)
	}
}

func TestSchemaInitIsIdempotentWithoutDuplicateColumnErrors(t *testing.T) {
	requireSQLite(t)
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init must be a no-op-compatible schema check: %v", err)
	}
}

func TestUpdateHostEndpointPreservesHostIdentity(t *testing.T) {
	requireSQLite(t)
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateHost(ctx, "remote", "tcp://192.0.2.20:2375", "worker-token", Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHostEndpoint(ctx, id, "tls://192.0.2.20:2376"); err != nil {
		t.Fatal(err)
	}
	h, err := s.Host(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Endpoint != "tls://192.0.2.20:2376" || h.WorkerToken != "worker-token" || h.Name != "remote" {
		t.Fatalf("unexpected host after endpoint migration: %+v", h)
	}
}
