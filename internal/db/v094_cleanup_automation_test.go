package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestV094PersistsCleanupAutomation(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.SaveAutomation(ctx, Automation{
		Name:              "Night cleanup",
		Cron:              "0 3 * * *",
		TargetType:        "all",
		Kind:              "cleanup",
		CleanupImages:     Bool(true),
		CleanupBuildCache: Bool(true),
		CleanupVolumes:    Bool(true),
		Enabled:           Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Automation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "cleanup" || !bool(got.CleanupImages) || !bool(got.CleanupBuildCache) || !bool(got.CleanupVolumes) || bool(got.CleanupNetworks) {
		t.Fatalf("cleanup automation fields were not persisted: %+v", got)
	}
}
