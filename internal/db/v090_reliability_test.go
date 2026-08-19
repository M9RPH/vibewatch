package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestV090TransactionLeaseAndVerificationHistoryRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateJob(ctx, "update", "manual", 4, "app", "running")
	if err != nil {
		t.Fatal(err)
	}
	txID, err := s.CreateUpdateTransaction(ctx, UpdateTransaction{JobID: jobID, HostID: 4, ContainerName: "app", Trigger: "manual", Actor: "owner", State: "queued", Status: "running", TargetDigest: "sha256:manifest", TargetImageID: "sha256:config"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.UpdateTransaction(ctx, txID)
	if err != nil || tx.TargetDigest != "sha256:manifest" || tx.TargetImageID != "sha256:config" {
		t.Fatalf("transaction target identities lost: %#v err=%v", tx, err)
	}
	if err := s.TransitionUpdateTransaction(ctx, txID, "queued", "preflight", "running", "start", 12); err != nil {
		t.Fatal(err)
	}
	events, err := s.UpdateTransactionEvents(ctx, txID)
	if err != nil || len(events) != 1 || events[0].ToState != "preflight" {
		t.Fatalf("events: %#v err=%v", events, err)
	}
	ok, err := s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "container:4:app", HostID: 4, ContainerName: "app", Owner: "job:1", OperationType: "update", JobID: jobID, TransactionID: txID}, time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease acquire ok=%v err=%v", ok, err)
	}
	ok, err = s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "container:4:app", HostID: 4, ContainerName: "app", Owner: "job:2", OperationType: "rollback", JobID: 999}, time.Minute)
	if err != nil || ok {
		t.Fatalf("conflicting lease must fail ok=%v err=%v", ok, err)
	}
	if err := s.ReleaseOperationLease(ctx, "container:4:app", "job:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddVerificationHistory(ctx, VerificationHistory{HostID: 4, ContainerName: "app", Trigger: "manual", Actor: "owner", JobID: jobID, TransactionID: txID, Status: "verified", ScopeType: "container", ScopeKey: "app", DurationMS: 123, DetailsJSON: `[{"status":"verified"}]`}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.VerificationHistory(ctx, 4, "app", 10)
	if err != nil || len(hist) != 1 || hist[0].DurationMS != 123 || hist[0].TransactionID != txID {
		t.Fatalf("verification history: %#v err=%v", hist, err)
	}
}

func TestV090HierarchicalHostAndContainerLeases(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err := s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "container:7:web", HostID: 7, ContainerName: "web", Owner: "job:1", OperationType: "update"}, time.Minute)
	if err != nil || !ok {
		t.Fatalf("container lease acquire ok=%v err=%v", ok, err)
	}
	ok, err = s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "host:7", HostID: 7, Owner: "job:2", OperationType: "cleanup"}, time.Minute)
	if err != nil || ok {
		t.Fatalf("host lease must conflict with active container lease ok=%v err=%v", ok, err)
	}
	if err := s.ReleaseOperationLease(ctx, "container:7:web", "job:1"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "host:7", HostID: 7, Owner: "job:2", OperationType: "cleanup"}, time.Minute)
	if err != nil || !ok {
		t.Fatalf("host lease acquire ok=%v err=%v", ok, err)
	}
	ok, err = s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "container:7:db", HostID: 7, ContainerName: "db", Owner: "job:3", OperationType: "rollback"}, time.Minute)
	if err != nil || ok {
		t.Fatalf("container lease must conflict with active host lease ok=%v err=%v", ok, err)
	}
	ok, err = s.AcquireOperationLease(ctx, OperationLease{ResourceKey: "container:8:other", HostID: 8, ContainerName: "other", Owner: "job:4", OperationType: "update"}, time.Minute)
	if err != nil || !ok {
		t.Fatalf("different host must remain independent ok=%v err=%v", ok, err)
	}
}

func TestV090LegacyMigrationAddsReliabilityTablesAndIntegrityColumns(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vibewatch.db")
	sql := `CREATE TABLE restore_points (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, snapshot_id TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', trigger TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'ready', image_ref TEXT NOT NULL DEFAULT '', image_id TEXT NOT NULL DEFAULT '', original_image_ref TEXT NOT NULL DEFAULT '', original_image_id TEXT NOT NULL DEFAULT '', target_digest TEXT NOT NULL DEFAULT '', from_version TEXT NOT NULL DEFAULT '', unit_kind TEXT NOT NULL DEFAULT '', unit_key TEXT NOT NULL DEFAULT '', stack_type TEXT NOT NULL DEFAULT '', writable_layer INTEGER NOT NULL DEFAULT 0, config_protected INTEGER NOT NULL DEFAULT 1, volume_data_protected INTEGER NOT NULL DEFAULT 0, volume_count INTEGER NOT NULL DEFAULT 0, bind_count INTEGER NOT NULL DEFAULT 0, restore_count INTEGER NOT NULL DEFAULT 0, last_restored_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', dependency_count INTEGER NOT NULL DEFAULT 0, dependencies_json TEXT NOT NULL DEFAULT '[]'); INSERT INTO restore_points(created_at,updated_at,host_id,container_name,snapshot_id) VALUES('now','now',1,'legacy','s1');`
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("legacy schema: %v %s", err, out)
	}
	s := New(path)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.RestorePoints(ctx, 10, 1, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("points %#v err=%v", rows, err)
	}
	if rows[0].IntegrityStatus != "not_checked" {
		t.Fatalf("integrity default lost: %#v", rows[0])
	}
	jobID, _ := s.CreateJob(ctx, "update", "manual", 1, "legacy", "running")
	if _, err := s.CreateUpdateTransaction(ctx, UpdateTransaction{JobID: jobID, HostID: 1, ContainerName: "legacy"}); err != nil {
		t.Fatalf("new table unavailable: %v", err)
	}
}
