package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func compareInspectConfig(before, current inspectContainer) []ConfigDriftChange {
	return compareDriftBaseline(baselineFromInspect(before), current)
}

func TestRegistryCredentialEncryptionRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc, err := encryptRegistrySecret(key, "super-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "super-secret-token") {
		t.Fatal("plaintext leaked into ciphertext")
	}
	got, err := decryptRegistrySecret(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "super-secret-token" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigDriftIgnoresImmutableImageIDButDetectsEnvironment(t *testing.T) {
	var before, current inspectContainer
	before.Config.Image = "example/app:latest"
	current.Config.Image = "example/app:latest"
	before.Image = "sha256:old"
	current.Image = "sha256:new"
	before.Config.Env = []string{"A=1", "B=2"}
	current.Config.Env = []string{"B=2", "A=1"}
	before.NetworkSettings.Networks = map[string]json.RawMessage{"app_default": nil}
	current.NetworkSettings.Networks = map[string]json.RawMessage{"app_default": nil}
	if got := compareInspectConfig(before, current); len(got) != 0 {
		t.Fatalf("normal image-ID change reported as drift: %#v", got)
	}
	current.Config.Env = []string{"A=1", "B=3"}
	got := compareInspectConfig(before, current)
	if len(got) != 1 || got[0].Field != "Environment" {
		t.Fatalf("expected environment drift, got %#v", got)
	}
}

func TestRollbackCreateArgsPreservesCriticalRuntimeSettings(t *testing.T) {
	var c inspectContainer
	c.Name = "/app"
	c.Config.Image = "example/app:latest"
	c.Config.Env = []string{"A=1"}
	c.Config.Labels = map[string]string{"com.docker.compose.project": "demo"}
	c.HostConfig.RestartPolicy.Name = "unless-stopped"
	c.HostConfig.Privileged = true
	c.NetworkSettings.Networks = map[string]json.RawMessage{"demo_default": nil}
	args, extras, err := createArgsFromInspect(c, "sha256:old")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"create --name app", "--restart unless-stopped", "--privileged", "--env A=1", "--network demo_default", "sha256:old"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rollback args missing %q: %s", want, joined)
		}
	}
	if len(extras) != 0 {
		t.Fatalf("unexpected extra networks: %v", extras)
	}
}

func TestConfigDriftDoesNotPersistEnvironmentSecrets(t *testing.T) {
	var before, current inspectContainer
	before.Config.Env = []string{"API_TOKEN=old-secret", "MODE=prod"}
	current.Config.Env = []string{"API_TOKEN=new-secret", "MODE=prod"}
	got := compareInspectConfig(before, current)
	b, _ := json.Marshal(got)
	text := string(b)
	if strings.Contains(text, "old-secret") || strings.Contains(text, "new-secret") {
		t.Fatalf("environment secret leaked into drift details: %s", text)
	}
	if !strings.Contains(text, "API_TOKEN") {
		t.Fatalf("changed key missing from drift details: %s", text)
	}
}

func TestConfigDriftBaselineDoesNotPersistEnvironmentOrLabelValues(t *testing.T) {
	var current inspectContainer
	current.Config.Env = []string{"API_TOKEN=baseline-secret", "MODE=prod"}
	current.Config.Labels = map[string]string{"service.token": "label-secret", "service.mode": "prod"}
	text := driftBaselineJSON(current)
	if strings.Contains(text, "baseline-secret") || strings.Contains(text, "label-secret") {
		t.Fatalf("secret value leaked into drift baseline: %s", text)
	}
	if !strings.Contains(text, "API_TOKEN") || !strings.Contains(text, "service.token") {
		t.Fatalf("baseline should retain setting keys for change reporting: %s", text)
	}
}

func TestRollbackCreateArgsPreservesMultiEntrypointAndCommonResourceSettings(t *testing.T) {
	var c inspectContainer
	c.Name = "/app"
	c.Config.Entrypoint = []string{"/usr/bin/tini", "--"}
	c.Config.Cmd = []string{"/app/server", "--serve"}
	c.Config.Tty = true
	c.Config.OpenStdin = true
	c.HostConfig.Memory = 512 * 1024 * 1024
	c.HostConfig.NanoCpus = 1500000000
	c.HostConfig.CpuShares = 768
	c.HostConfig.CpusetCpus = "0-1"
	c.HostConfig.GroupAdd = []string{"44"}
	c.HostConfig.Sysctls = map[string]string{"net.core.somaxconn": "1024"}
	c.HostConfig.NetworkMode = "bridge"
	args, _, err := createArgsFromInspect(c, "restore:image")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--tty", "--interactive", "--memory 536870912", "--cpus 1.5", "--cpu-shares 768", "--cpuset-cpus 0-1", "--group-add 44", "--sysctl net.core.somaxconn=1024", "--entrypoint /usr/bin/tini", "restore:image -- /app/server --serve"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rollback args missing %q: %s", want, joined)
		}
	}
}

func TestRollbackCreateArgsRejectsUnsupportedDeviceRequests(t *testing.T) {
	var c inspectContainer
	if err := json.Unmarshal([]byte(`{"Name":"/app","HostConfig":{"DeviceRequests":[{"Driver":"custom-accelerator","Count":1,"Capabilities":[["compute"]]}]}}`), &c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createArgsFromInspect(c, "restore:image"); err == nil || !strings.Contains(err.Error(), "unsupported Docker device request") {
		t.Fatalf("expected unsupported device request error, got %v", err)
	}
}

func TestNetworkNamespaceCreateArgsAvoidConflictingDockerFlags(t *testing.T) {
	var c inspectContainer
	c.Name = "/xteve"
	c.Config.Hostname = "old-hostname"
	c.Config.Domainname = "example.test"
	c.Config.Image = "example/xteve:latest"
	c.HostConfig.NetworkMode = "container:old-parent-id"
	c.HostConfig.DNS = []string{"1.1.1.1"}
	c.HostConfig.DNSSearch = []string{"example.test"}
	c.HostConfig.DNSOptions = []string{"ndots:1"}
	c.HostConfig.ExtraHosts = []string{"demo:127.0.0.1"}
	c.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"8080/tcp": {{HostPort: "8080"}}}
	args, extras, err := createArgsFromInspect(c, "example/xteve:latest")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network container:old-parent-id") {
		t.Fatalf("container namespace network mode missing: %s", joined)
	}
	for _, forbidden := range []string{"--hostname", "--domainname", "--publish", "--publish-all", "--dns ", "--dns-search", "--dns-option", "--add-host"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("container network mode includes conflicting flag %q: %s", forbidden, joined)
		}
	}
	if len(extras) != 0 {
		t.Fatalf("container namespace recreation should not attach extra networks: %v", extras)
	}
}

func TestContainerNamespaceReferenceMatching(t *testing.T) {
	var target inspectContainer
	target.ID = "c08aafa7263a2cd2df1c2792b6da20194420cff1fe172d653d5c5887599691b3"
	target.Name = "/gluetun"
	for _, ref := range []string{target.ID, target.ID[:12], "gluetun"} {
		if !sameContainerIdentity(ref, target) {
			t.Fatalf("expected %q to resolve to target", ref)
		}
	}
	if sameContainerIdentity("other", target) {
		t.Fatal("unrelated container reference matched target")
	}
	if ref, ok := containerNamespaceRef("container:" + target.ID); !ok || ref != target.ID {
		t.Fatalf("network namespace ref parse failed: %q %t", ref, ok)
	}
	if _, ok := containerNamespaceRef("bridge"); ok {
		t.Fatal("bridge incorrectly classified as container namespace dependency")
	}
}

func TestMergeNetworkNamespaceDependenciesRetainedStateWins(t *testing.T) {
	retained := []networkNamespaceDependencyRuntime{{networkNamespaceDependency: networkNamespaceDependency{SourceContainer: "xteve", WasRunning: false, SnapshotID: "old-snapshot"}}}
	current := []networkNamespaceDependencyRuntime{
		{networkNamespaceDependency: networkNamespaceDependency{SourceContainer: "xteve", WasRunning: true, SnapshotID: "current"}},
		{networkNamespaceDependency: networkNamespaceDependency{SourceContainer: "sabnzbd", WasRunning: true}},
	}
	got := mergeDependencyRuntimes(retained, current)
	if len(got) != 2 {
		t.Fatalf("expected two unique dependents, got %#v", got)
	}
	byName := map[string]networkNamespaceDependencyRuntime{}
	for _, dep := range got {
		byName[dep.SourceContainer] = dep
	}
	if byName["xteve"].WasRunning || byName["xteve"].SnapshotID != "old-snapshot" {
		t.Fatalf("retained rollback state did not win for xteve: %#v", byName["xteve"])
	}
	if !byName["sabnzbd"].WasRunning {
		t.Fatalf("current-only dependent was not retained: %#v", byName["sabnzbd"])
	}
}

func TestNetworkNamespaceDependencyPersistenceRoundTrip(t *testing.T) {
	deps := []networkNamespaceDependencyRuntime{{networkNamespaceDependency: networkNamespaceDependency{
		Type:                networkNamespaceDependencyType,
		SourceContainer:     "xteve",
		SourceContainerID:   "dependent-id",
		TargetContainer:     "gluetun",
		TargetContainerID:   "parent-id",
		RequiresRecreate:    true,
		WasRunning:          true,
		OriginalNetworkMode: "container:parent-id",
		SnapshotID:          "snapshot-1",
		ComposeProject:      "vpn-stack",
		ComposeService:      "xteve",
	}}}
	text := dependencyRecordsJSON(deps)
	var rows []networkNamespaceDependency
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SourceContainer != "xteve" || rows[0].SnapshotID != "snapshot-1" || !rows[0].WasRunning {
		t.Fatalf("unexpected persisted dependency payload: %s", text)
	}
}

func TestDeterministicTargetRuntimeUsesNewImageDefaultsAndPreservesVPNOverrides(t *testing.T) {
	var current inspectContainer
	current.Name = "/sabnzbdvpn"
	current.Image = "sha256:old"
	current.Config.Image = "binhex/arch-sabnzbdvpn:latest"
	current.Config.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/bin",
		"HOME=/home/nobody",
		"VPN_ENABLED=yes",
		"VPN_CLIENT=wireguard",
		"LAN_NETWORK=192.168.1.0/24",
	}
	current.Config.Cmd = []string{"/bin/bash", "old-init.sh"}
	current.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}
	current.Config.Labels = map[string]string{
		"org.opencontainers.image.version": "old-image-version",
		"com.docker.compose.project":       "sabnzbd",
		"com.docker.compose.service":       "sabnzbdvpn",
	}
	current.Config.WorkingDir = "/"
	current.HostConfig.Privileged = true
	current.HostConfig.CapAdd = []string{"NET_ADMIN"}
	current.HostConfig.Sysctls = map[string]string{"net.ipv4.conf.all.src_valid_mark": "1"}
	current.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"8080/tcp": {{HostPort: "8080"}}}
	current.NetworkSettings.Networks = map[string]json.RawMessage{"sabnzbd_default": nil}

	var source imageRuntimeDefaults
	source.Config.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/bin",
		"HOME=/home/nobody",
	}
	source.Config.Cmd = []string{"/bin/bash", "old-init.sh"}
	source.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}
	source.Config.Labels = map[string]string{"org.opencontainers.image.version": "old-image-version"}
	source.Config.WorkingDir = "/"

	prepared, summary := targetRuntimeOverrides(current, source)
	if summary.Environment != 3 {
		t.Fatalf("expected only three user env overrides, got %#v env=%v", summary, prepared.Config.Env)
	}
	for _, inherited := range []string{"PATH=", "HOME="} {
		for _, got := range prepared.Config.Env {
			if strings.HasPrefix(got, inherited) {
				t.Fatalf("inherited image env %q must not be replayed onto the target: %v", inherited, prepared.Config.Env)
			}
		}
	}
	if prepared.Config.Cmd != nil || prepared.Config.Entrypoint != nil {
		t.Fatalf("inherited command/entrypoint must be supplied by the target image, got cmd=%v entrypoint=%v", prepared.Config.Cmd, prepared.Config.Entrypoint)
	}
	if _, ok := prepared.Config.Labels["org.opencontainers.image.version"]; ok {
		t.Fatalf("inherited OCI image label must not be pinned to the target: %v", prepared.Config.Labels)
	}
	if prepared.Config.Labels["com.docker.compose.project"] != "sabnzbd" || prepared.Config.Labels["com.docker.compose.service"] != "sabnzbdvpn" {
		t.Fatalf("compose runtime labels were not preserved: %v", prepared.Config.Labels)
	}
	args, _, err := createArgsFromInspect(prepared, "sha256:new")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--privileged", "--cap-add NET_ADMIN", "--sysctl net.ipv4.conf.all.src_valid_mark=1", "--publish 8080:8080/tcp", "--env VPN_ENABLED=yes", "--env VPN_CLIENT=wireguard", "--network sabnzbd_default", "sha256:new"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("VPN deterministic recreate args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"old-init.sh", "--entrypoint /usr/bin/dumb-init", "PATH=/usr/local", "org.opencontainers.image.version=old-image-version"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("old image default leaked into deterministic recreate (%q): %s", forbidden, joined)
		}
	}
}

func TestDeterministicTargetRuntimeIgnoresVibewatchNormalizedImageEntrypoint(t *testing.T) {
	var current inspectContainer
	current.Name = "/sabnzbdvpn"
	// Real SABnzbdVPN evidence from the support bundle:
	// healthy peer/source shape: Entrypoint=/usr/bin/dumb-init (2 args), Cmd=/bin/bash (2 args)
	// restored Host 5 shape: Entrypoint=/usr/bin/dumb-init (1 arg), Cmd=-- (3 args).
	current.Config.Entrypoint = []string{"/usr/bin/dumb-init"}
	current.Config.Cmd = []string{"--", "/bin/bash", "/usr/local/bin/init.sh"}

	var source imageRuntimeDefaults
	// This is the original image process pair before an older Vibewatch rollback
	// round-tripped it through docker create --entrypoint.
	source.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}
	source.Config.Cmd = []string{"/bin/bash", "/usr/local/bin/init.sh"}

	prepared, summary := targetRuntimeOverrides(current, source)
	if summary.Command || summary.Entrypoint {
		t.Fatalf("Vibewatch-normalized inherited startup contract must not become user overrides: %#v", summary)
	}
	if prepared.Config.Cmd != nil || prepared.Config.Entrypoint != nil {
		t.Fatalf("target image must own startup defaults, got cmd=%v entrypoint=%v", prepared.Config.Cmd, prepared.Config.Entrypoint)
	}
	args, _, err := createArgsFromInspect(prepared, "sha256:new")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "/usr/local/bin/init.sh") || strings.Contains(joined, "--entrypoint /usr/bin/dumb-init") {
		t.Fatalf("legacy source startup path leaked into target recreate: %s", joined)
	}
}

func TestDeterministicTargetRuntimeRecoversCommandOnlyOverrideAfterEntrypointNormalization(t *testing.T) {
	var current inspectContainer
	current.Config.Entrypoint = []string{"/usr/bin/dumb-init"}
	current.Config.Cmd = []string{"--", "/custom/server", "--serve"}

	var source imageRuntimeDefaults
	source.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}
	source.Config.Cmd = []string{"/bin/bash", "init.sh"}

	prepared, summary := targetRuntimeOverrides(current, source)
	if !summary.Command || summary.Entrypoint {
		t.Fatalf("expected command-only override after normalization, got %#v", summary)
	}
	if !slices.Equal(prepared.Config.Cmd, []string{"/custom/server", "--serve"}) {
		t.Fatalf("expected normalized entrypoint tail to be removed from command override, got %v", prepared.Config.Cmd)
	}
	if prepared.Config.Entrypoint != nil {
		t.Fatalf("target image entrypoint should remain authoritative, got %v", prepared.Config.Entrypoint)
	}
}

func TestDeterministicTargetRuntimePreservesExplicitCommandAndEntrypointOverrides(t *testing.T) {
	var current inspectContainer
	current.Config.Cmd = []string{"/custom/server", "--serve"}
	current.Config.Entrypoint = []string{"/custom/entrypoint"}
	current.Config.User = "1000:1000"
	var source imageRuntimeDefaults
	source.Config.Cmd = []string{"/bin/bash", "init.sh"}
	source.Config.Entrypoint = []string{"/usr/bin/dumb-init", "--"}

	prepared, summary := targetRuntimeOverrides(current, source)
	if !summary.Command || !summary.Entrypoint || !summary.User {
		t.Fatalf("explicit runtime overrides were not retained: %#v", summary)
	}
	if !strings.EqualFold(prepared.Config.Cmd[0], "/custom/server") || prepared.Config.Entrypoint[0] != "/custom/entrypoint" || prepared.Config.User != "1000:1000" {
		t.Fatalf("unexpected prepared overrides: cmd=%v entrypoint=%v user=%q", prepared.Config.Cmd, prepared.Config.Entrypoint, prepared.Config.User)
	}
}

func TestDeterministicRuntimeFidelityCoversVPNCriticalSettings(t *testing.T) {
	var before, after inspectContainer
	before.HostConfig.Privileged = true
	before.HostConfig.CapAdd = []string{"NET_ADMIN"}
	before.HostConfig.Sysctls = map[string]string{"net.ipv4.conf.all.src_valid_mark": "1"}
	before.HostConfig.RestartPolicy.Name = "unless-stopped"
	before.HostConfig.NetworkMode = "sabnzbd_default"
	before.NetworkSettings.Networks = map[string]json.RawMessage{"sabnzbd_default": nil}
	before.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"8080/tcp": {{HostPort: "8080"}}}
	after = before
	after.HostConfig.CapAdd = []string{"CAP_NET_ADMIN"} // Docker may normalize CAP_ prefix.
	if got := criticalRuntimeMismatches(before, after); len(got) != 0 {
		t.Fatalf("equivalent VPN runtime should pass fidelity check: %v", got)
	}
	after.HostConfig.Sysctls = map[string]string{}
	got := criticalRuntimeMismatches(before, after)
	if len(got) != 1 || got[0] != "sysctls" {
		t.Fatalf("expected missing WireGuard sysctl to be detected precisely, got %v", got)
	}
}
