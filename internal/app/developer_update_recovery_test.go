package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/m9rph/vibewatch/internal/devupdate"
)

func writeDevSourceFixture(t *testing.T, root, version string) {
	t.Helper()
	files := map[string]string{
		"go.mod":                           "module github.com/m9rph/vibewatch\n",
		"Dockerfile":                       "FROM scratch\n",
		"docker-compose.yml":               "services: {}\n",
		"docker-compose.build.yml":         "services: {}\n",
		"web/package.json":                 "{}\n",
		"cmd/devupdater/main.go":           "package main\nfunc main(){}\n",
		"web/public/developer-update.html": "<!doctype html>\n",
		"VERSION":                          version + "\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcileOrphanedSourceRollbackRepairsMissingGoMod(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	writeDevSourceFixture(t, workspace, "1.0.15")
	id := "20260819-diskfull"
	backup := filepath.Join(devupdate.PathsFor(dataDir).Backups, id, "source")
	writeDevSourceFixture(t, backup, "1.0.15")
	// Reproduce the v1.0.16 ENOSPC symptom: rollback got most files back, but
	// go.mod disappeared before the helper exited.
	if err := os.Remove(filepath.Join(workspace, "go.mod")); err != nil {
		t.Fatal(err)
	}
	st := devupdate.Status{ID: id, Filename: "vibewatch-v1.0.16.zip", Version: "1.0.16", State: "rolling_back", Stage: "Restoring previous source", Percent: 70}
	if err := devupdate.WriteStatus(dataDir, st); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: Config{DataDir: dataDir, ProjectDir: workspace, Version: "1.0.15", DeveloperUpdates: true}}
	if err := a.reconcileOrphanedDeveloperUpdate(st); err != nil {
		t.Fatal(err)
	}
	if err := devupdate.ValidateSourceTree(workspace); err != nil {
		t.Fatalf("workspace was not repaired: %v", err)
	}
	version, err := devupdate.SourceTreeVersion(workspace)
	if err != nil || version != "1.0.15" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	got, err := devupdate.ReadStatus(dataDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" || got.Percent != 100 {
		t.Fatalf("unexpected reconciled state: %+v", got)
	}
}

func TestReconcileRecoveryRequiredCanRetrySourceRestore(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	id := "20260819-retryrec"
	writeDevSourceFixture(t, workspace, "1.0.15")
	backup := filepath.Join(devupdate.PathsFor(dataDir).Backups, id, "source")
	writeDevSourceFixture(t, backup, "1.0.15")
	if err := os.Remove(filepath.Join(workspace, "go.mod")); err != nil {
		t.Fatal(err)
	}
	st := devupdate.Status{ID: id, Version: "1.0.16", State: "recovery_required", Stage: "Manual source recovery required", Percent: 100}
	a := &App{Cfg: Config{DataDir: dataDir, ProjectDir: workspace, Version: "1.0.15", DeveloperUpdates: true}}
	if err := a.reconcileOrphanedDeveloperUpdate(st); err != nil {
		t.Fatal(err)
	}
	got, _ := devupdate.ReadStatus(dataDir, id)
	if got.State != "failed" {
		t.Fatalf("retry should resolve source-only recovery to failed terminal state, got %+v", got)
	}
}
