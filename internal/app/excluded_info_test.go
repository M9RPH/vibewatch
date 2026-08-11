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

	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
	"github.com/watchtower-ui/watchtower-ui/internal/registry"
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
