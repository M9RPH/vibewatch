package dockercli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func TestEnsureWorkerUsesValidHTTPAPIEndpoints(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  "network inspect "*) exit 0 ;;
  "inspect -f "*) exit 1 ;;
  "rm -f "*) exit 0 ;;
  "run "*) echo fake-container-id; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	c := New(slog.Default())
	c.Binary = script
	c.WorkerVersion = "0.4.5"
	_, err := c.EnsureWorker(context.Background(), db.Host{ID: 7, Endpoint: "tcp://192.168.1.250:2375", WorkerToken: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	valid := "WATCHTOWER_HTTP_API_ENDPOINTS=health,check,update,history,containers,config,images,metrics"
	if !strings.Contains(got, valid) {
		t.Fatalf("worker did not receive expected endpoint list:\n%s", got)
	}
	if strings.Contains(got, "ENDPOINTS=health,check,update,history,containers,status") {
		t.Fatalf("invalid status endpoint is still configured:\n%s", got)
	}
	if !strings.Contains(got, "DOCKER_HOST=tcp://192.168.1.250:2375") {
		t.Fatalf("remote Docker host was not passed to worker:\n%s", got)
	}
	if !strings.Contains(got, "WATCHTOWER_HTTP_API_TOKEN=secret-token") {
		t.Fatalf("worker API token was not passed to worker:\n%s", got)
	}
	if !strings.Contains(got, "WATCHTOWER_HTTP_API_PERIODIC_POLLS=false") || !strings.Contains(got, "WATCHTOWER_UPDATE_ON_START=false") {
		t.Fatalf("worker autonomous polling/start update safety flags missing:\n%s", got)
	}
	if !strings.Contains(got, "com.centurylinklabs.watchtower.scope=vibewatch-worker-7") {
		t.Fatalf("remote worker isolation scope label missing:\n%s", got)
	}
	if strings.Contains(got, "WATCHTOWER_SCOPE=") {
		t.Fatalf("remote worker must not enable a scope filter on target containers:\n%s", got)
	}
	if !strings.Contains(got, "rm -f watchtower-ui-worker-7") {
		t.Fatalf("legacy worker was not removed before runtime-name migration:\n%s", got)
	}
	if !strings.Contains(got, "--name vibewatch-worker-7") {
		t.Fatalf("new Vibewatch worker name missing:\n%s", got)
	}
	if !strings.Contains(got, "io.vibewatch.worker-version=0.4.5") {
		t.Fatalf("worker version label missing:\n%s", got)
	}
}

func TestEnsureWorkerRejectsEmptyToken(t *testing.T) {
	c := New(slog.Default())
	_, err := c.EnsureWorker(context.Background(), db.Host{ID: 7, Endpoint: "tcp://192.168.1.250:2375"})
	if err == nil || !strings.Contains(err.Error(), "worker API token is empty") {
		t.Fatalf("expected explicit empty-token error, got %v", err)
	}
}

func TestInstalledVersionPrefersOCILabel(t *testing.T) {
	v, source := InstalledVersion("netbirdio/netbird:latest", map[string]string{"org.opencontainers.image.version": "v0.59.3"})
	if v != "0.59.3" || source != "org.opencontainers.image.version" {
		t.Fatalf("got %q %q", v, source)
	}
}
func TestInstalledVersionUsesPinnedTag(t *testing.T) {
	v, source := InstalledVersion("example/app:1.2.3", nil)
	if v != "1.2.3" || source != "image-tag" {
		t.Fatalf("got %q %q", v, source)
	}
}
func TestInstalledVersionDoesNotPretendLatestIsVersion(t *testing.T) {
	v, _ := InstalledVersion("example/app:latest", nil)
	if v != "" {
		t.Fatalf("expected unknown, got %q", v)
	}
}

func TestLaunchSelfUpdateTargetsOnlyController(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	c.WorkerImage = "nickfedor/watchtower:latest"
	if err := c.LaunchSelfUpdate(context.Background(), "vibewatch"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "--run-once --cleanup vibewatch") {
		t.Fatalf("self updater does not target controller only:\n%s", got)
	}
	if strings.Contains(got, "DOCKER_HOST=") {
		t.Fatalf("self updater must use controller local socket only:\n%s", got)
	}
}

func TestStackMetadataCompose(t *testing.T) {
	name, service, typ := StackMetadata(map[string]string{
		"com.docker.compose.project": "paperless",
		"com.docker.compose.service": "webserver",
	})
	if name != "paperless" || service != "webserver" || typ != "compose" {
		t.Fatalf("got %q %q %q", name, service, typ)
	}
}

func TestStackMetadataSwarm(t *testing.T) {
	name, service, typ := StackMetadata(map[string]string{
		"com.docker.stack.namespace":    "media",
		"com.docker.swarm.service.name": "media_sonarr",
	})
	if name != "media" || service != "sonarr" || typ != "swarm" {
		t.Fatalf("got %q %q %q", name, service, typ)
	}
}

func TestStackMetadataStandalone(t *testing.T) {
	name, service, typ := StackMetadata(map[string]string{"foo": "bar"})
	if name != "" || service != "" || typ != "" {
		t.Fatalf("standalone container must not be assigned to a stack: %q %q %q", name, service, typ)
	}
}

func TestListContainersIncludesComposeStackMetadata(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"ps -a --no-trunc --format"*)
    printf '%s\n' '{"ID":"abc","Names":"paperless-web","Image":"paperlessngx/paperless-ngx:latest","State":"running","Status":"Up","Ports":"","Networks":"paperless_default","CreatedAt":"now"}'
    exit 0 ;;
  *"inspect --format"*)
    printf '%s\n' '{"Id":"abc","Image":"sha256:123","Config":{"Labels":{"com.docker.compose.project":"paperless","com.docker.compose.service":"webserver"}}}'
    exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.ListContainers(context.Background(), "tcp://docker:2375")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one container, got %d", len(got))
	}
	if got[0].StackName != "paperless" || got[0].StackService != "webserver" || got[0].StackType != "compose" {
		t.Fatalf("stack metadata missing from list result: %#v", got[0])
	}
}

func TestContainerMountDetectsPersistentDataMount(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"inspect vibewatch --format"*)
    printf '%s\n' '[{"Type":"bind","Source":"/opt/vibewatch/data","Destination":"/data","RW":true}]'
    exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	m, ok, err := c.ContainerMount(context.Background(), "vibewatch", "/data")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || m.Type != "bind" || m.Source != "/opt/vibewatch/data" || !m.RW {
		t.Fatalf("unexpected mount: ok=%v mount=%#v", ok, m)
	}
}

func TestHostOverviewIncludesImageUsageAndContainerStats(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"info --format"*)
    printf '%s\n' '{"Name":"pi4","ServerVersion":"28.3.3","OperatingSystem":"Ubuntu 24.04","OSType":"linux","Architecture":"aarch64","KernelVersion":"6.8.0","Driver":"overlay2","DockerRootDir":"/var/lib/docker","NCPU":4,"MemTotal":8589934592,"Containers":3,"ContainersRunning":2,"ContainersStopped":1}'
    exit 0 ;;
  *"image ls -aq --no-trunc"*)
    printf '%s\n' 'sha256:used' 'sha256:unused'
    exit 0 ;;
  *"image inspect sha256:used sha256:unused"*)
    printf '%s\n' '[{"Id":"sha256:used","RepoTags":["app:latest"],"Size":1073741824,"Created":"2026-08-01T10:00:00Z"},{"Id":"sha256:unused","RepoTags":null,"Size":536870912,"Created":"2026-07-01T10:00:00Z"}]'
    exit 0 ;;
  *"ps -aq --no-trunc"*)
    printf '%s\n' 'container1'
    exit 0 ;;
  *"inspect --format {{.Image}} container1"*)
    printf '%s\n' 'sha256:used'
    exit 0 ;;
  *"system df --format"*)
    printf '%s\n' '{"Type":"Images","TotalCount":"2","Active":"1","Size":"1.25GB","Reclaimable":"500MB (40%)"}'
    exit 0 ;;
  *"stats --no-stream --format"*)
    printf '%s\n' '{"CPUPerc":"2.50%","MemUsage":"256MiB / 8GiB"}' '{"CPUPerc":"1.25%","MemUsage":"128MiB / 8GiB"}'
    exit 0 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	o, err := c.HostOverview(context.Background(), "tcp://docker:2375", true)
	if err != nil {
		t.Fatal(err)
	}
	if o.DockerVersion != "28.3.3" || o.CPUs != 4 || o.MemoryTotalBytes != 8589934592 {
		t.Fatalf("docker info not parsed: %#v", o)
	}
	if o.ImagesTotal != 2 || o.ImagesInUse != 1 || o.ImagesUnused != 1 || o.ImagesDangling != 1 {
		t.Fatalf("image state wrong: %#v", o)
	}
	if o.ImageDiskBytes != 1250000000 || o.ImageReclaimableBytes != 500000000 || !o.ImageDiskExact {
		t.Fatalf("disk usage wrong: %#v", o)
	}
	if o.ContainerCPUPercent != 3.75 || o.ContainerMemoryBytes != 402653184 || !o.ContainerStatsAvailable {
		t.Fatalf("stats wrong: %#v", o)
	}
	if len(o.Images) != 2 || !o.Images[0].Unused {
		t.Fatalf("image inventory missing/unsorted: %#v", o.Images)
	}
}

func TestParseDockerBytesAndPruneOutput(t *testing.T) {
	cases := map[string]int64{"500MB": 500000000, "1.5GiB": 1610612736, "212 B": 212, "11.63 MB (70%)": 11630000}
	for in, want := range cases {
		if got := parseDockerBytes(in); got != want {
			t.Fatalf("parseDockerBytes(%q)=%d want %d", in, got, want)
		}
	}
	if got := parsePruneReclaimed("Deleted Images:\nfoo\nTotal reclaimed space: 1.5GB\n"); got != 1500000000 {
		t.Fatalf("reclaimed=%d", got)
	}
}

func TestRemoveManagedWorkersRemovesOnlyVibewatchWorkers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  *"ps -a --format"*) printf '%s\n' 'vibewatch-worker-1' 'watchtower-ui-worker-2' 'portainer_agent'; exit 0 ;;
  "rm -f vibewatch-worker-1"|"rm -f watchtower-ui-worker-2") exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	removed, err := c.RemoveManagedWorkers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed=%d want 2", removed)
	}
	b, _ := os.ReadFile(logPath)
	got := string(b)
	if strings.Contains(got, "rm -f portainer_agent") {
		t.Fatalf("non-worker container was touched: %s", got)
	}
}

func TestHostOverviewUsesDirectDaemonMemoryCapacity(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"info --format {{json .}}"*) printf '%s\n' '{"Name":"host","ServerVersion":"28","OperatingSystem":"Linux","OSType":"linux","Architecture":"amd64","KernelVersion":"6","Driver":"overlay2","DockerRootDir":"/var/lib/docker","NCPU":0,"MemTotal":0,"Containers":0,"ContainersRunning":0,"ContainersStopped":0}'; exit 0 ;;
  *"info --format {{json .MemTotal}}"*) printf '%s\n' '17179869184'; exit 0 ;;
  *"info --format {{.NCPU}}"*) printf '%s\n' '8'; exit 0 ;;
  *"image ls -aq --no-trunc"*) exit 0 ;;
  *"system df --format"*) exit 1 ;;
  *"stats --no-stream --format"*) exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	o, err := c.HostOverview(context.Background(), "tcp://docker:2375", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.MemoryTotalBytes != 17179869184 || o.CPUs != 8 {
		t.Fatalf("direct capacity not applied: %#v", o)
	}
}

func TestParseMemoryValueAcceptsBytesAndHumanUnits(t *testing.T) {
	cases := map[string]int64{
		"8589934592":     8589934592,
		"\"8589934592\"": 8589934592,
		"8GiB":           8589934592,
		"7.5 GiB":        8053063680,
	}
	for in, want := range cases {
		if got := parseMemoryValue(in); got != want {
			t.Fatalf("parseMemoryValue(%q)=%d want %d", in, got, want)
		}
	}
}

func TestChooseMemoryTotalPrefersDockerInfo(t *testing.T) {
	got, source, diag := chooseMemoryTotal(1073741824, 8589934592)
	if got != 1073741824 || source != "docker-info" {
		t.Fatalf("got=%d source=%q diag=%q", got, source, diag)
	}
	if !strings.Contains(diag, "docker-info=1073741824") || !strings.Contains(diag, "docker-stats-limit=8589934592") {
		t.Fatalf("diagnostic missing candidates: %q", diag)
	}
}

func TestChooseMemoryTotalRejectsUnlimitedStatsSentinel(t *testing.T) {
	got, source, diag := chooseMemoryTotal(0, 9223372036854771712)
	if got != 0 || source != "unavailable" || !strings.Contains(diag, "rejected") {
		t.Fatalf("got=%d source=%q diag=%q", got, source, diag)
	}
}

func TestEnsureLocalWorkerRemainsUnscoped(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  "network inspect "*) exit 0 ;;
  "inspect -f "*) exit 1 ;;
  "rm -f "*) exit 0 ;;
  "run "*) echo fake-container-id; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	c.WorkerVersion = "0.4.5"
	if _, err := c.EnsureWorker(context.Background(), db.Host{ID: 2, Endpoint: "unix:///var/run/docker.sock", WorkerToken: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "com.centurylinklabs.watchtower.scope=vibewatch-worker-") {
		t.Fatalf("local-host worker must stay unscoped so it can manage ordinary unscoped containers:\n%s", got)
	}
	if !strings.Contains(got, "/var/run/docker.sock:/var/run/docker.sock") {
		t.Fatalf("local Docker socket mount missing:\n%s", got)
	}
}

func TestImageRepoDigestSelectsMatchingRepository(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:abc --format"*)
    printf '%s\n' '["other/app@sha256:111","ghcr.io/example/app@sha256:222"]'
    exit 0 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.ImageRepoDigest(context.Background(), "tcp://docker:2375", "sha256:abc", "ghcr.io/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:222" {
		t.Fatalf("digest=%q want sha256:222", got)
	}
}

func TestNormalizeRepoNameDockerHubAliases(t *testing.T) {
	for _, v := range []string{"nginx:latest", "docker.io/library/nginx:latest", "registry-1.docker.io/library/nginx@sha256:abc"} {
		if got := normalizeRepoName(v); got != "library/nginx" {
			t.Fatalf("normalizeRepoName(%q)=%q", v, got)
		}
	}
}

func TestImagePlatform(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"image inspect sha256:abc --format"*)
    printf '%s\n' '{"Id":"sha256:abc","Os":"linux","Architecture":"arm64","Variant":"v8"}'
    exit 0 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.ImagePlatform(context.Background(), "tcp://docker:2375", "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.OS != "linux" || got.Architecture != "arm64" || got.Variant != "v8" || got.ImageID != "sha256:abc" {
		t.Fatalf("unexpected platform: %#v", got)
	}
}

func TestVolumeIsAnonymousUsesDockerMarkerAndCompatibilityName(t *testing.T) {
	if !volumeIsAnonymous("friendly", map[string]string{"com.docker.volume.anonymous": ""}) {
		t.Fatal("Docker anonymous marker should classify volume as anonymous")
	}
	if !volumeIsAnonymous(strings.Repeat("a", 64), nil) {
		t.Fatal("64-hex Docker generated name should classify as anonymous fallback")
	}
	if volumeIsAnonymous("paperless_data", map[string]string{"com.docker.compose.volume": "data"}) {
		t.Fatal("named Compose volume must not be classified as anonymous")
	}
}

func TestPruneUnusedAnonymousVolumesUsesVerifiedIndividualDeletes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	anon := strings.Repeat("a", 64)
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  *"volume ls -q"*) printf '%s\n' '` + anon + `'; exit 0 ;;
  *"volume inspect"*) printf '%s\n' '[{"Name":"` + anon + `","Driver":"local","Scope":"local","Mountpoint":"/v","CreatedAt":"2026-08-11T00:00:00Z","Labels":{"com.docker.volume.anonymous":""}}]'; exit 0 ;;
  *"ps -aq --no-trunc"*) exit 0 ;;
  *"volume rm ` + anon + `"*) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	if _, err := c.PruneUnusedAnonymousVolumes(context.Background(), "tcp://docker:2375", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	got := string(b)
	if strings.Contains(got, "volume prune") {
		t.Fatalf("volume prune must not be used because retained volumes cannot be excluded: %s", got)
	}
	if !strings.Contains(got, "volume rm "+anon) {
		t.Fatalf("verified anonymous volume was not deleted individually: %s", got)
	}
}

func TestPruneUnusedAnonymousVolumesSkipsRollbackProtected(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	anon := strings.Repeat("b", 64)
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  *"volume ls -q"*) printf '%s\n' '` + anon + `'; exit 0 ;;
  *"volume inspect"*) printf '%s\n' '[{"Name":"` + anon + `","Driver":"local","Scope":"local","Mountpoint":"/v","CreatedAt":"2026-08-11T00:00:00Z","Labels":{"com.docker.volume.anonymous":""}}]'; exit 0 ;;
  *"ps -aq --no-trunc"*) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	result, err := c.PruneUnusedAnonymousVolumes(context.Background(), "tcp://docker:2375", map[string]bool{anon: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtectedVolumes != 1 || len(result.RemovedVolumes) != 0 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "volume rm "+anon) {
		t.Fatalf("rollback-protected anonymous volume was touched: %s", string(b))
	}
}

func TestRemoveUnusedNamedVolumeRefusesReferencedVolume(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker-args.log")
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  *"volume ls -q"*) printf '%s\n' 'paperless_data'; exit 0 ;;
  *"volume inspect paperless_data"*) printf '%s\n' '[{"Name":"paperless_data","Driver":"local","Scope":"local","Mountpoint":"/v","CreatedAt":"2026-08-11T00:00:00Z","Labels":{"com.docker.compose.volume":"data"}}]'; exit 0 ;;
  *"ps -aq --no-trunc"*) printf '%s\n' 'container1'; exit 0 ;;
  *"inspect container1"*) printf '%s\n' '[{"Mounts":[{"Type":"volume","Name":"paperless_data"}]}]'; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	err := c.RemoveUnusedNamedVolume(context.Background(), "tcp://docker:2375", "paperless_data")
	if err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("expected referenced-volume refusal, got %v", err)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "volume rm paperless_data") {
		t.Fatalf("referenced named volume was touched: %s", string(b))
	}
}

func TestVolumeInventoryKeepsHostVolumesWhenBatchInspectPartiallyFails(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"volume ls -q"*) printf '%s\n' 'goodvol' 'brokenvol'; exit 0 ;;
  *"volume inspect goodvol brokenvol"*) printf '%s\n' 'batch failed' >&2; exit 1 ;;
  *"volume inspect goodvol"*) printf '%s\n' '[{"Name":"goodvol","Driver":"local","Scope":"local","Mountpoint":"/good","CreatedAt":"2026-08-11T00:00:00Z","Labels":{}}]'; exit 0 ;;
  *"volume inspect brokenvol"*) printf '%s\n' 'plugin unavailable' >&2; exit 1 ;;
  *"ps -aq --no-trunc"*) exit 0 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.VolumeInventory(context.Background(), "tcp://docker:2375")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both volume names to remain visible, got %#v", got)
	}
	byName := map[string]VolumeSummary{}
	for _, v := range got {
		byName[v.Name] = v
	}
	if byName["goodvol"].Driver != "local" || byName["goodvol"].InspectError != "" {
		t.Fatalf("good volume not recovered after batch fallback: %#v", byName["goodvol"])
	}
	if byName["brokenvol"].InspectError == "" {
		t.Fatalf("broken volume should stay visible with inspect error: %#v", byName["brokenvol"])
	}
}

func TestVolumeInventoryDoesNotMarkVolumesUnusedWhenContainerUsageScanIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"volume ls -q"*) printf '%s\n' 'data'; exit 0 ;;
  *"volume inspect data"*) printf '%s\n' '[{"Name":"data","Driver":"local","Scope":"local","Mountpoint":"/data","CreatedAt":"2026-08-11T00:00:00Z","Labels":{}}]'; exit 0 ;;
  *"ps -aq --no-trunc"*) printf '%s\n' 'container1'; exit 0 ;;
  *"inspect container1"*) printf '%s\n' 'inspect failed' >&2; exit 1 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.VolumeInventory(context.Background(), "tcp://docker:2375")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("unexpected inventory: %#v", got)
	}
	if got[0].UsageKnown || got[0].Unused {
		t.Fatalf("incomplete container scan must not classify volume as unused: %#v", got[0])
	}
}

func TestVolumeInventoryFallbackVerifiesEachVolumeAndReportsContainers(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "docker")
	body := `#!/bin/sh
case "$*" in
  *"volume ls -q"*) printf '%s\n' 'data' 'cache'; exit 0 ;;
  *"volume inspect data cache"*) printf '%s\n' '[{"Name":"data","Driver":"local","Scope":"local","Mountpoint":"/data","CreatedAt":"2026-08-11T00:00:00Z","Labels":{}},{"Name":"cache","Driver":"local","Scope":"local","Mountpoint":"/cache","CreatedAt":"2026-08-11T00:00:00Z","Labels":{}}]'; exit 0 ;;
  *"ps -aq --no-trunc"*) printf '%s\n' 'container1' 'container2'; exit 0 ;;
  *"inspect container1 container2"*) printf '%s\n' 'batch failed' >&2; exit 1 ;;
  *"inspect container1"*) printf '%s\n' '[{"Id":"container1","Name":"/app","Mounts":[{"Type":"volume","Name":"data"}]}]'; exit 0 ;;
  *"inspect container2"*) printf '%s\n' 'inspect failed' >&2; exit 1 ;;
  *"ps -a --no-trunc --filter volume=data --format"*) printf 'container1\tapp\ncontainer2\tworker\n'; exit 0 ;;
  *"ps -a --no-trunc --filter volume=cache --format"*) exit 0 ;;
esac
printf 'unexpected: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(slog.Default())
	c.Binary = script
	got, err := c.VolumeInventory(context.Background(), "tcp://docker:2375")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VolumeSummary{}
	for _, v := range got {
		byName[v.Name] = v
	}
	data := byName["data"]
	if !data.UsageKnown || data.RefCount != 2 || !data.InUse || data.Unused {
		t.Fatalf("fallback did not recover data references: %#v", data)
	}
	if strings.Join(data.ReferenceContainers, ",") != "app,worker" {
		t.Fatalf("unexpected reference container names: %#v", data.ReferenceContainers)
	}
	cache := byName["cache"]
	if !cache.UsageKnown || cache.RefCount != 0 || cache.InUse || !cache.Unused {
		t.Fatalf("fallback did not verify unused cache volume: %#v", cache)
	}
}
