package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/dockercli"
)

func testClientCertificatePEM(t *testing.T) (string, string, string) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Minute)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Vibewatch Test CA"}, NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clientTpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "vibewatch-client"}, NotBefore: now, NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTpl, caTpl, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})
	return string(caPEM), string(certPEM), string(keyPEM)
}

func TestV092NormalizeDockerConnectionEndpoints(t *testing.T) {
	cases := []struct{ kind, in, want string }{
		{"local", "", "unix:///var/run/docker.sock"},
		{"tcp", "tcp://192.0.2.10:2375", "tcp://192.0.2.10:2375"},
		{"tls", "tcp://192.0.2.10:2376", "tls://192.0.2.10:2376"},
		{"tls", "tls://docker.example:2376", "tls://docker.example:2376"},
		{"tls", "TLS://Docker.EXAMPLE:2376", "tls://Docker.EXAMPLE:2376"},
	}
	for _, tc := range cases {
		got, err := normalizeHostEndpoint(tc.kind, tc.in)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%s %s => %q, want %q", tc.kind, tc.in, got, tc.want)
		}
	}
	if _, err := normalizeHostEndpoint("tls", "unix:///var/run/docker.sock"); err == nil {
		t.Fatal("TLS must reject unix endpoint")
	}
	if _, err := normalizeHostEndpoint("ssh", "ssh://host"); err == nil {
		t.Fatal("unsupported SSH endpoint must be rejected in v0.9.2")
	}
}

func TestV092TLSCredentialValidation(t *testing.T) {
	ca, cert, key := testClientCertificatePEM(t)
	if err := validateTLSCredentials(ca, cert, key); err != nil {
		t.Fatalf("valid test certificate rejected: %v", err)
	}
	if err := validateTLSCredentials("not pem", cert, key); err == nil {
		t.Fatal("invalid CA accepted")
	}
	if err := validateTLSCredentials(ca, cert, "not a key"); err == nil {
		t.Fatal("invalid client key accepted")
	}
}

func TestV092BackupBundleFilenameValidation(t *testing.T) {
	if got, err := safeBackupBundleName("vibewatch-backup-20260813-120000.zip"); err != nil || got == "" {
		t.Fatalf("valid bundle rejected: %q %v", got, err)
	}
	for _, bad := range []string{"../vibewatch-backup-x.zip", "backup.zip", "vibewatch-backup-x.tar", "/tmp/vibewatch-backup-x.zip"} {
		if _, err := safeBackupBundleName(bad); err == nil {
			t.Fatalf("unsafe bundle filename accepted: %s", bad)
		}
	}
}

func TestV092BackupBundleRoundTripValidation(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not installed in this test environment")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := db.New(filepath.Join(dataDir, "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	docker := dockercli.New(slog.Default())
	docker.HostTLSRoot = filepath.Join(dataDir, "host-tls")
	a := &App{Cfg: Config{DataDir: dataDir, Version: "0.9.2"}, Store: store, Docker: docker, Logger: slog.Default()}
	bundle, err := a.createApplicationBackupBundle(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Name == "" || bundle.SizeBytes <= 0 {
		t.Fatalf("invalid bundle metadata: %+v", bundle)
	}
	res := a.validateApplicationBackupBundle(ctx, bundle.Name)
	if !res.Valid {
		t.Fatalf("generated bundle did not validate: %+v", res.Checks)
	}
}
