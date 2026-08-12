package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
)

func TestSnapshotRetentionKeepsNewestThree(t *testing.T) {
	dir := t.TempDir()
	names := []string{"20260811T010000.000000000Z-a.zip", "20260811T020000.000000000Z-b.zip", "20260811T030000.000000000Z-c.zip", "20260811T040000.000000000Z-d.zip"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{}
	a.enforceSnapshotRetention(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d snapshots, want 3", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, names[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest snapshot was not removed")
	}
}

func TestReconstructedComposeContainsRecoveryData(t *testing.T) {
	meta := dockercli.Container{Name: "paperless-web", Image: "paperless:latest", StackName: "paperless", StackService: "web", StackType: "compose"}
	var ctr inspectContainer
	ctr.Name = "/paperless-web"
	ctr.Config.Image = "paperless:latest"
	ctr.Config.Env = []string{"PAPERLESS_URL=https://paperless.example", "SECRET=value"}
	ctr.HostConfig.RestartPolicy.Name = "unless-stopped"
	ctr.Mounts = append(ctr.Mounts, struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Propagation string `json:"Propagation"`
	}{Type: "volume", Name: "paperless_data", Source: "/var/lib/docker/volumes/paperless_data/_data", Destination: "/usr/src/paperless/data", RW: true})
	ctr.NetworkSettings.Networks = map[string]json.RawMessage{"paperless_default": nil}
	got, err := reconstructCompose("stack", "paperless", []dockercli.Container{meta}, []inspectContainer{ctr})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name: "paperless"`, `"web":`, `image: "paperless:latest"`, `"SECRET=value"`, `source: "paperless_data"`, `external: true`, `name: "paperless_data"`, `name: "paperless_default"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("compose missing %q:\n%s", want, got)
		}
	}
}

func TestSnapshotZipContainsRecoveryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.zip")
	if err := writeContainerSnapshotZip(
		path,
		[]byte("services:\n  app:\n    image: example/app:latest\n"),
		[]byte(`[{}]`),
		[]byte(`[{}]`),
		[]byte(`[{"name":"app_data"}]`),
		[]byte(`{"schema_version":1}`),
	); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("snapshot permissions too broad: %o", st.Mode().Perm())
	}
	for _, name := range []string{"compose.yaml", "container-inspect.json", "images.json", "volumes.json", "backup-info.json"} {
		if _, err := snapshotZipEntry(path, name); err != nil {
			t.Fatalf("snapshot missing %s: %v", name, err)
		}
	}
	compose, err := snapshotZipEntry(path, "compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "example/app:latest") {
		t.Fatalf("unexpected compose content: %s", compose)
	}
}

func TestRollbackProtectedDockerObjectsIncludesRetainedVolumes(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "backups", "containers", "host-7", "stack-test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	info, _ := json.Marshal(snapshotInfo{SchemaVersion: 1, CreatedAt: "2026-08-11T12:00:00Z", Reason: "before-update", HostID: 7, UnitKind: "stack", UnitKey: "test", UnitName: "test"})
	path := filepath.Join(dir, "20260811T120000.000000000Z-before-update.zip")
	if err := writeContainerSnapshotZip(path, []byte("services: {}\n"), []byte(`[{"Mounts":[{"Type":"volume","Name":"inspect_only"}],"NetworkSettings":{"Networks":{}},"Image":"sha256:abc"}]`), []byte(`[{"Id":"sha256:abc"}]`), []byte(`[{"name":"data"}]`), info); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: Config{DataDir: dataDir}}
	images, _, volumes := a.rollbackProtectedDockerObjects(7)
	if !images["sha256:abc"] {
		t.Fatalf("snapshot image was not protected: %#v", images)
	}
	if volumes["data"] != 1 || volumes["inspect_only"] != 1 {
		t.Fatalf("retained volume protection incomplete: %#v", volumes)
	}
}

func TestReconstructedComposeNormalizesComposeNetworkNamespaceDependency(t *testing.T) {
	parentID := "c08aafa7263a2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	depID := "d111111111112cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	all := []dockercli.Container{
		{ID: parentID, Name: "gluetun", Image: "qmcgaw/gluetun:latest", StackName: "media", StackService: "gluetun", StackType: "compose"},
		{ID: depID, Name: "xteve", Image: "xteve-image", StackName: "media", StackService: "xteve", StackType: "compose"},
	}
	var parent inspectContainer
	parent.ID = parentID
	parent.Name = "/gluetun"
	parent.Config.Image = "qmcgaw/gluetun:latest"
	parent.Config.Hostname = parentID[:12] // Docker-generated runtime hostname; must not be persisted.
	parent.Config.Labels = map[string]string{
		"com.docker.compose.project": "media",
		"com.docker.compose.service": "gluetun",
	}
	var dep inspectContainer
	dep.ID = depID
	dep.Name = "/xteve"
	dep.Config.Image = "xteve-image"
	dep.Config.Hostname = parentID[:12] // inherited runtime namespace hostname
	dep.Config.Labels = map[string]string{
		"com.docker.compose.project": "media",
		"com.docker.compose.service": "xteve",
	}
	dep.HostConfig.NetworkMode = "container:" + parentID
	dep.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"34400/tcp": {{HostPort: "34400"}}}
	dep.HostConfig.DNS = []string{"1.1.1.1"}
	dep.HostConfig.ExtraHosts = []string{"example:127.0.0.1"}

	got, err := reconstructCompose("stack", "media", all, []inspectContainer{parent, dep})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `network_mode: "service:gluetun"`) {
		t.Fatalf("compose did not normalize namespace dependency to service reference:\n%s", got)
	}
	for _, forbidden := range []string{parentID, `hostname: "` + parentID[:12] + `"`, `"34400:34400/tcp"`, `dns:`, `extra_hosts:`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("compose retained runtime/conflicting value %q:\n%s", forbidden, got)
		}
	}
}

func TestReconstructedComposeNormalizesExternalNetworkNamespaceToContainerName(t *testing.T) {
	parentID := "aaaaaaaaaaaa2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	depID := "bbbbbbbbbbbb2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	all := []dockercli.Container{
		{ID: parentID, Name: "gluetun", Image: "qmcgaw/gluetun:latest"},
		{ID: depID, Name: "xteve", Image: "xteve-image"},
	}
	var dep inspectContainer
	dep.ID = depID
	dep.Name = "/xteve"
	dep.Config.Image = "xteve-image"
	dep.Config.Hostname = parentID[:12]
	dep.HostConfig.NetworkMode = "container:" + parentID

	got, err := reconstructCompose("service", "xteve", all, []inspectContainer{dep})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `network_mode: "container:gluetun"`) {
		t.Fatalf("compose did not normalize namespace dependency to stable container name:\n%s", got)
	}
	if strings.Contains(got, parentID) || strings.Contains(got, `hostname:`) {
		t.Fatalf("compose retained stale runtime identity:\n%s", got)
	}
}

func TestReconstructedComposeRejectsUnresolvedRuntimeNetworkNamespaceID(t *testing.T) {
	var dep inspectContainer
	dep.ID = "bbbbbbbbbbbb2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	dep.Name = "/xteve"
	dep.Config.Image = "xteve-image"
	dep.HostConfig.NetworkMode = "container:c08aafa7263a2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"

	if _, err := reconstructCompose("service", "xteve", []dockercli.Container{{ID: dep.ID, Name: "xteve", Image: "xteve-image"}}, []inspectContainer{dep}); err == nil {
		t.Fatal("expected unresolved runtime container ID to make recovery compose generation fail closed")
	}
}
