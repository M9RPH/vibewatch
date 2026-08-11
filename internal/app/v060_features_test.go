package app

import (
	"encoding/json"
	"strings"
	"testing"
)

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
