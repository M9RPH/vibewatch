package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCachePersistsFirstDetectedAndSnooze(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not installed")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	want := Cache{HostID: 7, ContainerName: "demo", Image: "demo:latest", ImageID: "sha256:old", CurrentDigest: "sha256:old", LatestDigest: "sha256:new", UpdateAvailable: false, FirstDetectedAt: "2026-08-11T10:00:00Z", SnoozedDigest: "sha256:new", SnoozedAt: "2026-08-11T10:05:00Z", LastCheckedAt: "2026-08-11T10:06:00Z"}
	if err := s.SaveCache(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Cache(ctx, 7, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstDetectedAt != want.FirstDetectedAt || got.SnoozedDigest != want.SnoozedDigest || got.SnoozedAt != want.SnoozedAt {
		t.Fatalf("tracking fields did not round-trip: got=%+v want=%+v", got, want)
	}
}
