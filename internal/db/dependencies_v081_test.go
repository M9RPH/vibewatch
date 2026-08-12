package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDependencyMetadataRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	rpID, err := s.AddRestorePoint(ctx, RestorePoint{HostID: 1, ContainerName: "gluetun", SnapshotID: "snap", Status: "ready", DependencyCount: 2, DependenciesJSON: `[{"type":"network_namespace","source_container":"xteve"}]`})
	if err != nil {
		t.Fatal(err)
	}
	rp, err := s.RestorePoint(ctx, rpID)
	if err != nil {
		t.Fatal(err)
	}
	if rp.DependencyCount != 2 || rp.DependenciesJSON == "" {
		t.Fatalf("restore point dependency metadata lost: %#v", rp)
	}
	hID, err := s.AddUpdateHistory(ctx, UpdateHistory{HostID: 1, ContainerName: "gluetun", Status: "success", DependencyCount: 2, DependencyStatus: "success", DependencyDetails: "qbittorrent, xteve"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.UpdateHistoryEntry(ctx, hID)
	if err != nil {
		t.Fatal(err)
	}
	if h.DependencyCount != 2 || h.DependencyStatus != "success" || h.DependencyDetails != "qbittorrent, xteve" {
		t.Fatalf("history dependency metadata lost: %#v", h)
	}
}
