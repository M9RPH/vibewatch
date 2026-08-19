package devupdate

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func addRequiredSourceFiles(dst map[string]string, version string) {
	if version == "" {
		version = "1.0.0"
	}
	defaults := map[string]string{
		"go.mod":                           "module github.com/m9rph/vibewatch\n",
		"Dockerfile":                       "FROM scratch\n",
		"docker-compose.yml":               "services: {}\n",
		"docker-compose.build.yml":         "services: {}\n",
		"web/package.json":                 "{}\n",
		"cmd/devupdater/main.go":           "package main\nfunc main(){}\n",
		"web/public/developer-update.html": "<!doctype html>\n",
		"VERSION":                          version + "\n",
	}
	for _, rel := range RequiredPackageFiles {
		if _, ok := defaults[rel]; !ok {
			defaults[rel] = "release skeleton\n"
		}
	}
	for k, v := range defaults {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
}

func makeFixtureZIP(t *testing.T, root string, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{}
	addRequiredSourceFiles(files, "1.0.0")
	for k, v := range extra {
		files[k] = v
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(root + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStageArchiveAcceptsNestedProjectRoot(t *testing.T) {
	dataDir := t.TempDir()
	payload := makeFixtureZIP(t, "vibewatch-patch/", map[string]string{"internal/example.go": "package internal\n"})
	st, err := StageArchive(dataDir, "patch.zip", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "1.0.0" || st.State != "uploaded" || st.SHA256 == "" {
		t.Fatalf("unexpected staged status: %#v", st)
	}
	p := PathsFor(dataDir)
	if _, err := os.Stat(filepath.Join(p.Staged, st.ID, "source", "internal", "example.go")); err != nil {
		t.Fatalf("expected extracted project file: %v", err)
	}
}

func TestStageArchiveRejectsMissingRepositorySkeleton(t *testing.T) {
	files := map[string]string{}
	addRequiredSourceFiles(files, "1.0.0")
	delete(files, "data/backups/bundles/.gitkeep")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := StageArchive(t.TempDir(), "missing-skeleton.zip", bytes.NewReader(buf.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "data/backups/bundles/.gitkeep") {
		t.Fatalf("expected release-skeleton validation error, got %v", err)
	}
}

func TestStageArchiveRequiresIntegratedUpdater(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"go.mod": "module x\n", "Dockerfile": "FROM scratch\n", "docker-compose.yml": "services: {}\n",
		"docker-compose.build.yml": "services: {}\n", "web/package.json": "{}\n", "VERSION": "1.0.0\n",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	_, err := StageArchive(t.TempDir(), "old.zip", bytes.NewReader(buf.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "development updater") {
		t.Fatalf("expected integrated-updater validation error, got %v", err)
	}
}

func TestApplySourcePreservesEnvironmentDataAndGit(t *testing.T) {
	workspace := t.TempDir()
	mustWrite := func(path, body string) {
		t.Helper()
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("docker-compose.yml", "old compose")
	mustWrite("stale.go", "stale")
	mustWrite(".env", "SECRET=keep")
	mustWrite("scripts/.env", "SCRIPT_SECRET=keep")
	mustWrite("data/vibewatch.db", "database")
	mustWrite(".git/config", "git")
	requiredOld := map[string]string{}
	addRequiredSourceFiles(requiredOld, "1.0.0")
	for path, body := range requiredOld {
		mustWrite(path, body)
	}

	source := t.TempDir()
	newFiles := map[string]string{"docker-compose.yml": "new compose", "docker-compose.build.yml": "build", "fresh.go": "fresh", "scripts/tool.sh": "new tool", ".env": "SECRET=replace", "data/evil": "nope"}
	addRequiredSourceFiles(newFiles, "1.0.1")
	for path, body := range newFiles {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplySource(source, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "stale.go")); !os.IsNotExist(err) {
		t.Fatalf("stale source was not removed: %v", err)
	}
	checks := map[string]string{
		"docker-compose.yml": "new compose", "fresh.go": "fresh", ".env": "SECRET=keep", "scripts/.env": "SCRIPT_SECRET=keep", "data/vibewatch.db": "database", ".git/config": "git",
	}
	for path, want := range checks {
		b, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q err=%v, want %q", path, string(b), err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "data", "evil")); !os.IsNotExist(err) {
		t.Fatalf("staged runtime data must not be copied into persistent data: %v", err)
	}
	for _, rel := range releaseDataSkeleton {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("release skeleton marker %s missing after apply: %v", rel, err)
		}
	}
}

func TestApplySourceNeverCopiesStagedScriptEnvironment(t *testing.T) {
	workspace := t.TempDir()
	oldFiles := map[string]string{"docker-compose.yml": "old compose", "scripts/.env": "OWNER_SECRET=keep", "scripts/old.sh": "old"}
	addRequiredSourceFiles(oldFiles, "1.0.0")
	for path, body := range oldFiles {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := t.TempDir()
	newFiles := map[string]string{"docker-compose.yml": "new compose", "scripts/.env": "OWNER_SECRET=replace-me", "scripts/new.sh": "new"}
	addRequiredSourceFiles(newFiles, "1.0.1")
	for path, body := range newFiles {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplySource(source, workspace); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(workspace, "scripts", ".env"))
	if err != nil || string(b) != "OWNER_SECRET=keep" {
		t.Fatalf("scripts/.env changed to %q err=%v", string(b), err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "scripts", "old.sh")); !os.IsNotExist(err) {
		t.Fatalf("stale script was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "scripts", "new.sh")); err != nil {
		t.Fatalf("new script missing: %v", err)
	}
}

func TestSnapshotSourceDoesNotCopyEnvironmentSecrets(t *testing.T) {
	workspace := t.TempDir()
	files := map[string]string{"docker-compose.yml": "services: {}", ".env": "ROOT_SECRET=yes", "scripts/.env": "SCRIPT_SECRET=yes", "scripts/tool.sh": "#!/bin/sh\n"}
	addRequiredSourceFiles(files, "1.0.0")
	for path, body := range files {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup := filepath.Join(t.TempDir(), "source")
	if err := SnapshotSource(workspace, backup); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{".env", "scripts/.env"} {
		if _, err := os.Stat(filepath.Join(backup, filepath.FromSlash(secret))); !os.IsNotExist(err) {
			t.Fatalf("secret %s must not be duplicated into source rollback backup: %v", secret, err)
		}
	}
	if _, err := os.Stat(filepath.Join(backup, "scripts", "tool.sh")); err != nil {
		t.Fatalf("non-secret source should be backed up: %v", err)
	}
}

func TestRestoreDatabaseBackupReplacesDatabaseAndClearsSidecars(t *testing.T) {
	dataDir := t.TempDir()
	backup := filepath.Join(t.TempDir(), "before.db")
	if err := os.WriteFile(backup, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(backup+".registry-key", key, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"vibewatch.db": "after", "vibewatch.db-wal": "wal", "vibewatch.db-shm": "shm"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RestoreDatabaseBackup(backup, dataDir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "vibewatch.db"))
	if err != nil || string(b) != "before" {
		t.Fatalf("database restore = %q err=%v", string(b), err)
	}
	for _, sidecar := range []string{"vibewatch.db-wal", "vibewatch.db-shm"} {
		if _, err := os.Stat(filepath.Join(dataDir, sidecar)); !os.IsNotExist(err) {
			t.Fatalf("SQLite sidecar %s must be removed: %v", sidecar, err)
		}
	}
	gotKey, err := os.ReadFile(filepath.Join(dataDir, "registry-credentials.key"))
	if err != nil || !bytes.Equal(gotKey, key) {
		t.Fatalf("registry key restore failed: %v", err)
	}
}

func TestApplySourceFailureDoesNotDeleteExistingSource(t *testing.T) {
	workspace := t.TempDir()
	old := map[string]string{"keep-stale.txt": "must survive failed apply", "block": "existing file"}
	addRequiredSourceFiles(old, "1.0.0")
	for path, body := range old {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source := t.TempDir()
	newFiles := map[string]string{"block/child.txt": "forces mkdir over file"}
	addRequiredSourceFiles(newFiles, "1.0.1")
	for path, body := range newFiles {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplySource(source, workspace); err == nil {
		t.Fatal("expected apply failure")
	}
	if b, err := os.ReadFile(filepath.Join(workspace, "keep-stale.txt")); err != nil || string(b) != "must survive failed apply" {
		t.Fatalf("failed apply deleted preexisting source: %q err=%v", string(b), err)
	}
	if err := ValidateSourceTree(workspace); err != nil {
		t.Fatalf("failed apply must leave a structurally complete source tree: %v", err)
	}
}

func TestWriteStatusCreatesEmergencyReserveAndCancelMarker(t *testing.T) {
	dataDir := t.TempDir()
	st := Status{ID: "20260819-test123", State: "building", Stage: "Building", Percent: 38}
	if err := WriteStatus(dataDir, st); err != nil {
		t.Fatal(err)
	}
	reserve := filepath.Join(PathsFor(dataDir).Root, ".status-reserve")
	if info, err := os.Stat(reserve); err != nil || info.Size() < statusReserveBytes {
		t.Fatalf("status emergency reserve missing: info=%v err=%v", info, err)
	}
	if err := RequestCancel(dataDir, st.ID); err != nil {
		t.Fatal(err)
	}
	if !CancelRequested(dataDir, st.ID) {
		t.Fatal("cancel marker not visible")
	}
	ClearCancel(dataDir, st.ID)
	if CancelRequested(dataDir, st.ID) {
		t.Fatal("cancel marker not cleared")
	}
}

func TestActiveStatusDoesNotExpireByAge(t *testing.T) {
	dataDir := t.TempDir()
	st := Status{ID: "20260819-oldactive", State: "rolling_back", Stage: "Restoring previous source", Percent: 70}
	if err := WriteStatus(dataDir, st); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(PathsFor(dataDir).States, st.ID+".json")
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	got, ok := ActiveStatus(dataDir)
	if !ok {
		t.Fatal("aged active development update must remain active until reconciled")
	}
	if got.ID != st.ID || got.State != "rolling_back" {
		t.Fatalf("active status = %#v", got)
	}
}
