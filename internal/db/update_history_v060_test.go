package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdateHistoryRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddUpdateHistory(ctx, UpdateHistory{HostID: 7, ContainerName: "app", Action: "update", Trigger: "manual", Actor: "admin", Status: "success", FromVersion: "1.0", ToVersion: "1.1", FromDigest: "sha256:old", ToDigest: "sha256:new", SnapshotID: "snap-1", DurationMS: 1234})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("invalid history id %d", id)
	}
	row, err := s.UpdateHistoryEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.HostID != 7 || row.ContainerName != "app" || row.Actor != "admin" || row.SnapshotID != "snap-1" || row.ToVersion != "1.1" {
		t.Fatalf("unexpected row: %#v", row)
	}
	rows, err := s.UpdateHistory(ctx, 20, 7, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("unexpected filtered history: %#v", rows)
	}
}

func TestConfigDriftBaselinePersistsAcrossStoreRead(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	want := ConfigDriftState{HostID: 7, ContainerName: "app", Status: "matches", DetailsJSON: "[]", BaselineAt: "2026-08-11T09:00:00Z", BaselineJSON: `{"image_reference":"example/app:latest"}`, BaselineSource: "post-update", CheckedAt: "2026-08-11T09:00:01Z"}
	if err := s.SaveConfigDrift(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigDrift(ctx, 7, "app")
	if err != nil {
		t.Fatal(err)
	}
	if got.BaselineJSON != want.BaselineJSON || got.BaselineSource != want.BaselineSource {
		t.Fatalf("baseline did not persist: got=%#v want=%#v", got, want)
	}
}
