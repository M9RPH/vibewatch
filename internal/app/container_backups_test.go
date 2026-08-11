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
	got := reconstructCompose("stack", "paperless", []dockercli.Container{meta}, []inspectContainer{ctr})
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
