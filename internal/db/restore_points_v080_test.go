package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRestorePointRoundTripAndLatestPrefersUsableFullPoint(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	readyID, err := s.AddRestorePoint(ctx, RestorePoint{
		HostID: 9, ContainerName: "db", SnapshotID: "snap-ready", Reason: "before-update", Trigger: "manual", Status: "ready",
		ImageRef: "vibewatch-restore/host-9/db:snap-ready", ImageID: "sha256:restore", OriginalImageRef: "example/db:latest", OriginalImageID: "sha256:old",
		TargetDigest: "sha256:new", FromVersion: "1.0", UnitKind: "service", UnitKey: "db", StackType: "compose",
		WritableLayer: Bool(true), ConfigProtected: Bool(true), VolumeDataProtected: Bool(false), VolumeCount: 1, BindCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRestorePoint(ctx, RestorePoint{
		HostID: 9, ContainerName: "db", SnapshotID: "snap-degraded", Status: "degraded", ConfigProtected: Bool(true), LastError: "commit failed",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := s.RestorePoint(ctx, readyID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ImageRef == "" || !bool(row.WritableLayer) || row.VolumeCount != 1 || row.BindCount != 2 || row.TargetDigest != "sha256:new" {
		t.Fatalf("restore point did not persist: %#v", row)
	}
	latest, err := s.LatestRestorePointsForHost(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	if got := latest["db"]; got.ID != readyID {
		t.Fatalf("latest usable full restore point was hidden by degraded row: got=%#v ready_id=%d", got, readyID)
	}
}

func TestUpdateHistoryPersistsRestorePointLink(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddUpdateHistory(ctx, UpdateHistory{HostID: 2, ContainerName: "app", Action: "update", Trigger: "manual", Actor: "owner", Status: "success", SnapshotID: "snap-2", RestorePointID: 77})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.UpdateHistoryEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.RestorePointID != 77 {
		t.Fatalf("restore point link lost in update history: %#v", row)
	}
}
