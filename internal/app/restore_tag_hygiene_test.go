package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/m9rph/vibewatch/internal/dockercli"
)

func TestRestoreRuntimeRefRemovesLegacyRestoreContaminatedMutableTag(t *testing.T) {
	dir := t.TempDir()
	tagged := filepath.Join(dir, "tagged")
	removed := filepath.Join(dir, "removed")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect example/app:latest --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:restore-old","Os":"linux","Architecture":"amd64"}'
    exit 0 ;;
  *"image inspect sha256:restore-old --format {{json .Config.Labels}}"*)
    printf '%s\n' '{"io.vibewatch.restore-point":"legacy-snapshot"}'
    exit 0 ;;
  *"image tag sha256:restore-new example/app:latest"*)
    : > "` + tagged + `"
    exit 0 ;;
  *"image rm example/app:latest"*)
    : > "` + removed + `"
    exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	a := &App{Docker: docker}
	ref, cleanup := a.prepareRuntimeRestoreRefPreservingTag(context.Background(), "tcp://docker:2375", "sha256:restore-new", "example/app:latest")
	if ref != "example/app:latest" {
		t.Fatalf("expected readable temporary ref, got %q", ref)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup contaminated mutable ref: %v", err)
	}
	if _, err := os.Stat(tagged); err != nil {
		t.Fatalf("expected temporary restore tag: %v", err)
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatalf("expected contaminated app tag to be removed: %v", err)
	}
}

func TestRestoreRuntimeRefPreservesNormalPreRollbackTargetTag(t *testing.T) {
	dir := t.TempDir()
	restored := filepath.Join(dir, "restored")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect example/app:latest --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:target","Os":"linux","Architecture":"amd64"}'
    exit 0 ;;
  *"image inspect sha256:target --format {{json .Config.Labels}}"*)
    printf '%s\n' '{}'
    exit 0 ;;
  *"image tag sha256:restore example/app:latest"*)
    exit 0 ;;
  "--host tcp://docker:2375 image inspect sha256:target"|"image inspect sha256:target")
    exit 0 ;;
  *"image tag sha256:target example/app:latest"*)
    : > "` + restored + `"
    exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	a := &App{Docker: docker}
	ref, cleanup := a.prepareRuntimeRestoreRefPreservingTag(context.Background(), "tcp://docker:2375", "sha256:restore", "example/app:latest")
	if ref != "example/app:latest" {
		t.Fatalf("expected readable temporary ref, got %q", ref)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("restore pre-rollback target tag: %v", err)
	}
	if _, err := os.Stat(restored); err != nil {
		t.Fatalf("expected original target tag to be restored: %v", err)
	}
}
