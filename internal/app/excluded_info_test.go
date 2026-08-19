package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/dockercli"
	"github.com/m9rph/vibewatch/internal/registry"
)

type appRoundTripFunc func(*http.Request) (*http.Response, error)

func (f appRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExcludedReadOnlyRegistryCheckDetectsChangedDigest(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "test", "tcp://docker:2375", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:local --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:local","Os":"linux","Architecture":"amd64","Variant":""}'; exit 0 ;;
  *"image inspect sha256:local --format {{json .Config.Labels}}"*)
    exit 1 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	reg := registry.New()
	reg.HTTP = &http.Client{Transport: appRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:new"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:new"}}`)),
			Request:    r,
		}, nil
	})}

	a := &App{Store: store, Docker: docker, Registry: reg, Logger: slog.Default(), infoRefresh: map[string]bool{}}
	res, err := a.readOnlyRegistryCheck(ctx, hostID, dockercli.Container{Name: "app", Image: "example/app:latest", ImageID: "sha256:local", State: "running"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || len(res.Containers) != 1 || !res.Containers[0].UpdateAvailable {
		t.Fatalf("unexpected result: %#v", res)
	}
	cache, err := store.Cache(ctx, hostID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !bool(cache.UpdateAvailable) || cache.CurrentDigest != "sha256:local" || cache.LatestDigest != "sha256:new" || cache.LastError != "" {
		t.Fatalf("unexpected cache: %#v", cache)
	}
}

func TestImageStateStaleUsesShorterRetryAfterError(t *testing.T) {
	if !imageStateStale("", "") {
		t.Fatal("empty state must be stale")
	}
}

func TestRunCheckUsesRegistryIdentityAfterRollback(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID, err := store.CreateHost(ctx, "pi4", "tcp://docker:2375", "token", db.Bool(true))
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(ctx, "check", "test", hostID, "adguardhome", "running")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.StartJob(ctx, jobID)

	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"inspect adguardhome"*)
    printf '%s\n' '[{"Id":"container-id","Name":"/adguardhome","Image":"sha256:old","Config":{"Image":"adguard/adguardhome:latest","Labels":{}},"State":{"Status":"running","Running":true}}]'; exit 0 ;;
  *"image inspect sha256:old --format {{json .}}"*)
    printf '%s\n' '{"Id":"sha256:old","Os":"linux","Architecture":"arm64","Variant":"v8"}'; exit 0 ;;
  *"image inspect sha256:old --format {{json .Config.Labels}}"*)
    printf '%s\n' '{}'; exit 0 ;;
esac
printf 'unexpected docker command: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.Binary = script
	reg := registry.New()
	reg.HTTP = &http.Client{Transport: appRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Docker-Content-Digest": []string{"sha256:manifest-new"}},
			Body:       io.NopCloser(strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:new"}}`)),
			Request:    r,
		}, nil
	})}

	a := &App{Store: store, Docker: docker, Registry: reg, Logger: slog.Default(), infoRefresh: map[string]bool{}}
	res, err := a.runCheck(ctx, jobID, hostID, "adguardhome", "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || len(res.Containers) != 1 || !res.Containers[0].UpdateAvailable {
		t.Fatalf("expected registry-authoritative update after rollback, got %#v", res)
	}
	if res.Containers[0].Digest != "sha256:old" || res.Containers[0].LatestDigest != "sha256:new" {
		t.Fatalf("expected comparable config digests, got %#v", res.Containers[0])
	}
	cache, err := store.Cache(ctx, hostID, "adguardhome")
	if err != nil {
		t.Fatal(err)
	}
	if !bool(cache.UpdateAvailable) || cache.CurrentDigest != "sha256:old" || cache.LatestDigest != "sha256:new" {
		t.Fatalf("unexpected authoritative cache: %#v", cache)
	}
	job, err := store.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "success" || !strings.Contains(job.SummaryJSON, "sha256:new") {
		t.Fatalf("unexpected check job: %#v", job)
	}
}
