package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/dockercli"
	"github.com/m9rph/vibewatch/internal/watchtower"
)

func TestUpdateSummaryWithLocalRecreateMarksAppliedUpdate(t *testing.T) {
	raw := []byte(`{"summary":{"updated":0,"failed":0,"skipped":0}}`)
	res := watchtower.UpdateResponse{Summary: watchtower.UpdateSummary{Updated: 0, Failed: 0, Skipped: 0}}
	out, got := updateSummaryWithLocalRecreate(raw, res)
	if got.Summary.Updated != 1 {
		t.Fatalf("expected effective update count 1, got %d", got.Summary.Updated)
	}
	var payload struct {
		Summary   watchtower.UpdateSummary `json:"summary"`
		Vibewatch struct {
			TargetRecreate bool `json:"target_recreate"`
			WorkerUpdated  int  `json:"worker_updated"`
		} `json:"vibewatch"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("decode augmented summary: %v", err)
	}
	if !payload.Vibewatch.TargetRecreate || payload.Vibewatch.WorkerUpdated != 0 || payload.Summary.Updated != 1 {
		t.Fatalf("unexpected augmented summary: %+v", payload)
	}
}

func TestEnsureExpectedTargetImageLocalProvesImmutableTargetAfterPull(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "pulled")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:new --format {{json .}}"*)
    if [ -f "` + statePath + `" ]; then
      printf '%s\n' '{"Id":"sha256:new","Os":"linux","Architecture":"arm64","Variant":"v8"}'
      exit 0
    fi
    exit 1 ;;
  *"pull example/app:latest"*)
    : > "` + statePath + `"
    printf '%s\n' 'Pulled newer image'
    exit 0 ;;
  *"image inspect example/app:latest --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:new","Os":"linux","Architecture":"arm64","Variant":"v8"}'
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
	prepared := preflightPrepared{}
	prepared.TargetInspect.Config.Image = "example/app:latest"
	if err := a.ensureExpectedTargetImageLocal(context.Background(), db.Host{Endpoint: "tcp://docker:2375"}, prepared, "sha256:new"); err != nil {
		t.Fatalf("expected immutable target proof after pull, got %v", err)
	}
}

func TestDeterministicSourceDefaultsWalksRestoreCommitBackToOriginalImage(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "nas", "unix:///var/run/docker.sock", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRestorePoint(ctx, db.RestorePoint{
		HostID:           hostID,
		ContainerName:    "sabnzbdvpn",
		SnapshotID:       "snap-restore-1",
		Status:           "ready",
		ImageRef:         "vibewatch-restore/sab:snap1",
		ImageID:          "sha256:restore1",
		OriginalImageRef: "binhex/arch-sabnzbdvpn:latest",
		OriginalImageID:  "sha256:base",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddRestorePoint(ctx, db.RestorePoint{
		HostID:           hostID,
		ContainerName:    "sabnzbdvpn",
		SnapshotID:       "snap-restore-2",
		Status:           "ready",
		ImageRef:         "vibewatch-restore/sab:snap2",
		ImageID:          "sha256:restore2",
		OriginalImageRef: "binhex/arch-sabnzbdvpn:latest",
		OriginalImageID:  "sha256:restore1",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:restore2"*)
    printf '%s\n' '[{"Id":"sha256:restore2","Config":{"Env":["PATH=/old","VPN_ENABLED=yes"],"Cmd":["/old/init"],"Labels":{"io.vibewatch.restore-point":"snap-restore-2"}}}]'; exit 0 ;;
  *"image inspect sha256:restore1"*)
    printf '%s\n' '[{"Id":"sha256:restore1","Config":{"Env":["PATH=/old","VPN_ENABLED=yes"],"Cmd":["/old/init"],"Labels":{"io.vibewatch.restore-point":"snap-restore-1"}}}]'; exit 0 ;;
  *"image inspect sha256:base"*)
    printf '%s\n' '[{"Id":"sha256:base","Config":{"Env":["PATH=/old"],"Cmd":["/old/init"],"Labels":{"org.opencontainers.image.version":"old"}}}]'; exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	a := &App{Store: store, Docker: docker}
	var target inspectContainer
	target.Name = "/sabnzbdvpn"
	target.Image = "sha256:restore2"
	target.Config.Env = []string{"PATH=/old", "VPN_ENABLED=yes"}
	target.Config.Cmd = []string{"/old/init"}

	defaults, sourceID, err := a.deterministicSourceDefaults(ctx, db.Host{ID: hostID, Endpoint: "unix:///var/run/docker.sock"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "sha256:base" {
		t.Fatalf("expected original base image, got %q", sourceID)
	}
	prepared, summary := targetRuntimeOverrides(target, defaults)
	if summary.Environment != 1 || len(prepared.Config.Env) != 1 || prepared.Config.Env[0] != "VPN_ENABLED=yes" {
		t.Fatalf("restore commit must not swallow real user env overrides: summary=%+v env=%v", summary, prepared.Config.Env)
	}
	if prepared.Config.Cmd != nil {
		t.Fatalf("command inherited from original source image should be supplied by target image, got %v", prepared.Config.Cmd)
	}
}

func TestDeterministicSourceDefaultsUsesEmbeddedRestoreProvenanceWithoutDBRow(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "nas", "unix:///var/run/docker.sock", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:restore"*)
    printf '%s\n' '[{"Id":"sha256:restore","Config":{"Env":["PATH=/old","VPN_ENABLED=yes"],"Cmd":["/old/init"],"Labels":{"io.vibewatch.restore-point":"expired-snapshot","io.vibewatch.restore-original-image-id":"sha256:base"}}}]'; exit 0 ;;
  *"image inspect sha256:base"*)
    printf '%s\n' '[{"Id":"sha256:base","Config":{"Env":["PATH=/old"],"Cmd":["/old/init"],"Labels":{}}}]'; exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	a := &App{Store: store, Docker: docker}
	var target inspectContainer
	target.Name = "/sabnzbdvpn"
	target.Image = "sha256:restore"
	target.Config.Env = []string{"PATH=/old", "VPN_ENABLED=yes"}
	target.Config.Cmd = []string{"/old/init"}

	defaults, sourceID, err := a.deterministicSourceDefaults(ctx, db.Host{ID: hostID, Endpoint: "unix:///var/run/docker.sock"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "sha256:base" {
		t.Fatalf("expected embedded original base image, got %q", sourceID)
	}
	prepared, summary := targetRuntimeOverrides(target, defaults)
	if summary.Environment != 1 || len(prepared.Config.Env) != 1 || prepared.Config.Env[0] != "VPN_ENABLED=yes" {
		t.Fatalf("embedded restore provenance must preserve real user env overrides: summary=%+v env=%v", summary, prepared.Config.Env)
	}
	if prepared.Config.Cmd != nil {
		t.Fatalf("source image command must be inherited from the target image, got %v", prepared.Config.Cmd)
	}
}

func TestDeterministicSourceDefaultsRecoversExpiredLegacyRestoreByUniqueLayerAncestry(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "nas", "unix:///var/run/docker.sock", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  # Match the batch inspect before the single-image inspect. Shell case arms
  # are first-match-wins, and the batch command also contains the shorter
  # "image inspect sha256:restore" substring.
  *"image inspect sha256:restore sha256:base sha256:unrelated"*)
    printf '%s\n' '[{"Id":"sha256:restore","RootFS":{"Layers":["sha256:l1","sha256:l2","sha256:commit"]},"Config":{"Labels":{"io.vibewatch.restore-point":"expired-snapshot"}}},{"Id":"sha256:base","RootFS":{"Layers":["sha256:l1","sha256:l2"]},"Config":{"Env":["PATH=/old"],"Cmd":["/old/init"],"Labels":{}}},{"Id":"sha256:unrelated","RootFS":{"Layers":["sha256:x"]},"Config":{"Labels":{}}}]'; exit 0 ;;
  *"image inspect sha256:restore"*)
    printf '%s\n' '[{"Id":"sha256:restore","Parent":"","RootFS":{"Layers":["sha256:l1","sha256:l2","sha256:commit"]},"Config":{"Env":["PATH=/old","VPN_ENABLED=yes"],"Cmd":["/old/init"],"Labels":{"io.vibewatch.restore-point":"expired-snapshot"}}}]'; exit 0 ;;
  *"image ls -aq --no-trunc"*)
    printf '%s\n' 'sha256:restore' 'sha256:base' 'sha256:unrelated'; exit 0 ;;
  *"image inspect sha256:base"*)
    printf '%s\n' '[{"Id":"sha256:base","RootFS":{"Layers":["sha256:l1","sha256:l2"]},"Config":{"Env":["PATH=/old"],"Cmd":["/old/init"],"Labels":{}}}]'; exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	a := &App{Store: store, Docker: docker}
	var target inspectContainer
	target.Name = "/sabnzbdvpn"
	target.Image = "sha256:restore"
	target.Config.Env = []string{"PATH=/old", "VPN_ENABLED=yes"}
	target.Config.Cmd = []string{"/old/init"}

	defaults, sourceID, err := a.deterministicSourceDefaults(ctx, db.Host{ID: hostID, Endpoint: "unix:///var/run/docker.sock"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != "sha256:base" {
		t.Fatalf("expected layer-derived original base image, got %q", sourceID)
	}
	prepared, summary := targetRuntimeOverrides(target, defaults)
	if summary.Environment != 1 || len(prepared.Config.Env) != 1 || prepared.Config.Env[0] != "VPN_ENABLED=yes" {
		t.Fatalf("legacy ancestry recovery must preserve real user env overrides: summary=%+v env=%v", summary, prepared.Config.Env)
	}
}

func TestLegacyRestoreLayerAncestryFailsClosedWhenDeepestCandidateIsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image ls -aq --no-trunc"*)
    printf '%s\n' 'sha256:restore' 'sha256:base-a' 'sha256:base-b'; exit 0 ;;
  *"image inspect sha256:restore sha256:base-a sha256:base-b"*)
    printf '%s\n' '[{"Id":"sha256:restore","RootFS":{"Layers":["sha256:l1","sha256:l2","sha256:commit"]},"Config":{"Labels":{}}},{"Id":"sha256:base-a","RootFS":{"Layers":["sha256:l1","sha256:l2"]},"Config":{"Labels":{}}},{"Id":"sha256:base-b","RootFS":{"Layers":["sha256:l1","sha256:l2"]},"Config":{"Labels":{}}}]'; exit 0 ;;
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
	var restore imageRuntimeDefaults
	restore.ID = "sha256:restore"
	restore.RootFS.Layers = []string{"sha256:l1", "sha256:l2", "sha256:commit"}
	_, _, err := a.inferLegacyRestoreParent(context.Background(), "unix:///var/run/docker.sock", restore)
	if err == nil || !strings.Contains(err.Error(), "ambiguous local ancestry") {
		t.Fatalf("expected ambiguous ancestry to fail closed, got %v", err)
	}
}

func TestLegacyRestoreUsesValidatedDockerParentWhenPresent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:base"*)
    printf '%s\n' '[{"Id":"sha256:base","RootFS":{"Layers":["sha256:l1","sha256:l2"]},"Config":{"Env":["PATH=/old"],"Labels":{}}}]'; exit 0 ;;
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
	var restore imageRuntimeDefaults
	restore.ID = "sha256:restore"
	restore.Parent = "sha256:base"
	// A config-only/empty commit can legitimately have the same RootFS layer
	// sequence as its parent; an explicit Docker Parent remains useful when it is
	// present and the layer relationship validates.
	restore.RootFS.Layers = []string{"sha256:l1", "sha256:l2"}
	parent, parentID, err := a.inferLegacyRestoreParent(context.Background(), "unix:///var/run/docker.sock", restore)
	if err != nil {
		t.Fatal(err)
	}
	if parentID != "sha256:base" || parent.ID != "sha256:base" {
		t.Fatalf("expected validated docker parent, got id=%q parent=%+v", parentID, parent)
	}
}

func TestAlignMutableImageRefToTargetReplacesRestorePollutedTag(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "aligned")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:610c --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:610c","Os":"linux","Architecture":"amd64"}'
    exit 0 ;;
  *"image inspect binhex/arch-sabnzbdvpn --format {{json .}}"*)
    if [ -f "` + statePath + `" ]; then
      printf '%s\n' '{"Id":"sha256:610c","Os":"linux","Architecture":"amd64"}'
    else
      printf '%s\n' '{"Id":"sha256:44fe","Os":"linux","Architecture":"amd64"}'
    fi
    exit 0 ;;
  *"image tag sha256:610c binhex/arch-sabnzbdvpn"*)
    : > "` + statePath + `"
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
	prepared := preflightPrepared{}
	prepared.TargetInspect.Config.Image = "binhex/arch-sabnzbdvpn"
	changed, previous, err := a.alignMutableImageRefToTarget(context.Background(), db.Host{Endpoint: "tcp://docker:2375"}, prepared, "sha256:610c")
	if err != nil {
		t.Fatalf("align polluted mutable tag: %v", err)
	}
	if !changed || previous != "sha256:44fe" {
		t.Fatalf("expected polluted tag sha256:44fe to be replaced, changed=%t previous=%q", changed, previous)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected exact target retag operation: %v", err)
	}
}

func TestRestoreRuntimeDeltaPreservesSABEnvironmentAndDropsRestoreProvenance(t *testing.T) {
	var source imageRuntimeDefaults
	source.ID = "sha256:4d61"
	source.Config.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LANG=en_US.UTF-8",
		"TERM=xterm",
	}
	source.Config.Labels = map[string]string{
		"org.opencontainers.image.source": "https://github.com/binhex/arch-sabnzbdvpn",
	}
	source.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}
	source.Config.Cmd = []string{"/bin/bash", "/usr/local/bin/init.sh"}

	var restored inspectContainer
	restored.Image = "sha256:4aa0"
	restored.Name = "/sabnzbdvpn"
	restored.Config.Env = append([]string{}, source.Config.Env...)
	restored.Config.Env = append(restored.Config.Env,
		"PUID=0",
		"PGID=0",
		"UMASK=000",
		"VPN_ENABLED=yes",
		"VPN_CLIENT=wireguard",
		"VPN_PROV=custom",
		"VPN_USER=unused",
		"VPN_PASS=unused",
		"LAN_NETWORK=192.168.1.0/24",
		"NAME_SERVERS=192.168.1.250,1.1.1.1",
		"USERSPACE_WIREGUARD=no",
		"VPN_INPUT_PORTS=1234",
		"VPN_OUTPUT_PORTS=5678",
		"ENABLE_STARTUP_SCRIPTS=no",
		"ENABLE_SOCKS=yes",
		"ENABLE_PRIVOXY=yes",
		"SOCKS_USER=admin",
		"SOCKS_PASS=socks",
		"STRICT_PORT_FORWARD=yes",
		"DEBUG=false",
	)
	restored.Config.Labels = map[string]string{
		"org.opencontainers.image.source":         "https://github.com/binhex/arch-sabnzbdvpn",
		"com.docker.compose.project":              "sabnzbd",
		"com.docker.compose.service":              "sabnzbdvpn",
		"io.vibewatch.restore-point":              "20260819T095658.850092559Z-before-manual-update",
		"io.vibewatch.restore-original-image-id":  "sha256:44fe",
		"io.vibewatch.restore-original-image-ref": "binhex/arch-sabnzbdvpn",
		"io.vibewatch.restore-store":              "vibewatch_restore_points",
	}
	// Match Vibewatch's own historical rollback normalization. This must not be
	// replayed onto the new image as a user process override.
	restored.Config.Entrypoint = []string{"/usr/bin/dumb-init"}
	restored.Config.Cmd = []string{"--", "/bin/bash", "/usr/local/bin/init.sh"}

	prepared, summary := targetRuntimeOverrides(restored, source)
	if summary.Environment != 20 {
		t.Fatalf("expected 20 explicit SAB/VPN environment overrides, got %d: %v", summary.Environment, prepared.Config.Env)
	}
	env := runtimeEnvMap(prepared.Config.Env)
	for _, key := range []string{"PUID", "PGID", "VPN_ENABLED", "VPN_CLIENT", "VPN_PROV", "LAN_NETWORK", "NAME_SERVERS"} {
		if _, ok := env[key]; !ok {
			t.Fatalf("expected %s to survive restore-runtime delta, env=%v", key, prepared.Config.Env)
		}
	}
	for _, key := range []string{"PATH", "HOME", "LANG", "TERM"} {
		if _, ok := env[key]; ok {
			t.Fatalf("expected inherited image default %s to be supplied by target image", key)
		}
	}
	if prepared.Config.Labels["com.docker.compose.project"] != "sabnzbd" || prepared.Config.Labels["com.docker.compose.service"] != "sabnzbdvpn" {
		t.Fatalf("expected Compose identity labels to survive: %#v", prepared.Config.Labels)
	}
	for key := range prepared.Config.Labels {
		if restoreProvenanceLabel(key) {
			t.Fatalf("restore provenance must not be copied onto successful target: %s", key)
		}
	}
	if summary.Command || summary.Entrypoint {
		t.Fatalf("Vibewatch rollback-normalized process must not become target override: %+v", summary)
	}
}

func TestRuntimeOverrideFidelityDetectsMissingEnvironmentWithoutValues(t *testing.T) {
	var expected inspectContainer
	expected.Config.Env = []string{"VPN_PROV=custom", "VPN_CLIENT=wireguard", "PUID=0"}
	expected.Config.Labels = map[string]string{"com.docker.compose.project": "sabnzbd"}
	actual := expected
	actual.Config.Env = []string{"VPN_CLIENT=wireguard", "PUID=0"}

	mismatches := runtimeOverrideFidelityMismatches(expected, actual)
	if !slices.Contains(mismatches, "env:VPN_PROV") {
		t.Fatalf("expected missing VPN_PROV to be detected, got %v", mismatches)
	}
	for _, mismatch := range mismatches {
		if strings.Contains(mismatch, "custom") || strings.Contains(mismatch, "wireguard") {
			t.Fatalf("fidelity error must not expose environment values: %v", mismatches)
		}
	}
}
