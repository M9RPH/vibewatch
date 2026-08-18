package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m9rph/vibewatch/internal/dockercli"
)

func TestSaveManagedHostTLSCredentialsAtomicReplacesCompleteIdentity(t *testing.T) {
	docker := dockercli.New(slog.Default())
	docker.HostTLSRoot = t.TempDir()
	a := &App{Docker: docker}
	endpoint := "tls://192.0.2.20:2376"

	first, err := generateManagedMTLS("192.0.2.20", time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.saveManagedHostTLSCredentialsAtomic(endpoint, first.CAPEM, first.ClientCertPEM, first.ClientKeyPEM, first.CAKeyPEM); err != nil {
		t.Fatal(err)
	}
	second, err := generateManagedMTLS("192.0.2.20", time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.saveManagedHostTLSCredentialsAtomic(endpoint, second.CAPEM, second.ClientCertPEM, second.ClientKeyPEM, second.CAKeyPEM); err != nil {
		t.Fatal(err)
	}

	dir := docker.TLSCredentialDir(endpoint)
	for name, want := range map[string]string{
		"ca.pem":     second.CAPEM,
		"cert.pem":   second.ClientCertPEM,
		"key.pem":    second.ClientKeyPEM,
		"ca-key.pem": second.CAKeyPEM,
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.TrimSpace(string(b)) != strings.TrimSpace(want) {
			t.Fatalf("%s did not contain the newly committed managed identity", name)
		}
	}
	if _, err := os.Stat(dir + ".quicksetup-previous"); !os.IsNotExist(err) {
		t.Fatalf("credential backup directory left behind after atomic commit: %v", err)
	}
}
