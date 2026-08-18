package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type managedMTLSBundle struct {
	CAPEM          string
	CAKeyPEM       string
	ServerCertPEM  string
	ServerKeyPEM   string
	ClientCertPEM  string
	ClientKeyPEM   string
	ClientNotAfter time.Time
}

func dockerEndpointHost(endpoint string) string {
	raw := strings.TrimSpace(endpoint)
	for _, prefix := range []string{"tcp://", "tls://"} {
		if strings.HasPrefix(strings.ToLower(raw), prefix) {
			raw = raw[len(prefix):]
			break
		}
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}

func normalizeHostEndpoint(connectionType, endpoint string) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(connectionType))
	ep := strings.TrimSpace(endpoint)
	if kind == "" {
		kind = "tcp"
		if strings.HasPrefix(strings.ToLower(ep), "unix://") {
			kind = "local"
		} else if strings.HasPrefix(strings.ToLower(ep), "tls://") {
			kind = "tls"
		}
	}
	switch kind {
	case "local":
		if ep == "" {
			ep = "unix:///var/run/docker.sock"
		}
		if !strings.HasPrefix(strings.ToLower(ep), "unix://") {
			return "", fmt.Errorf("local Docker hosts require a unix:// endpoint")
		}
		return ep, nil
	case "tcp":
		if !strings.HasPrefix(strings.ToLower(ep), "tcp://") {
			return "", fmt.Errorf("TCP Docker hosts require a tcp:// endpoint")
		}
		return "tcp://" + ep[len("tcp://"):], nil
	case "tls":
		lower := strings.ToLower(ep)
		if strings.HasPrefix(lower, "tcp://") {
			ep = "tls://" + ep[len("tcp://"):]
		} else if strings.HasPrefix(lower, "tls://") {
			ep = "tls://" + ep[len("tls://"):]
		}
		if !strings.HasPrefix(ep, "tls://") || strings.TrimSpace(ep[len("tls://"):]) == "" {
			return "", fmt.Errorf("TLS/mTLS Docker hosts require a tls:// or tcp:// endpoint")
		}
		return ep, nil
	default:
		return "", fmt.Errorf("connection_type must be local, tcp or tls")
	}
}

func validateTLSCredentials(caPEM, certPEM, keyPEM string) error {
	caPEM, certPEM, keyPEM = strings.TrimSpace(caPEM), strings.TrimSpace(certPEM), strings.TrimSpace(keyPEM)
	if caPEM == "" || certPEM == "" || keyPEM == "" {
		return fmt.Errorf("CA certificate, client certificate and client private key are required")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return fmt.Errorf("CA certificate is not valid PEM")
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return fmt.Errorf("client certificate/private key validation failed: %w", err)
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func pemECPrivateKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

func generateManagedMTLS(ip string, now time.Time) (managedMTLSBundle, error) {
	parsedIP := net.ParseIP(strings.TrimSpace(ip))
	if parsedIP == nil {
		return managedMTLSBundle{}, fmt.Errorf("secure quick setup requires a valid host IP address")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	notBefore := now.Add(-5 * time.Minute)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	caSerial, err := randomSerial()
	if err != nil {
		return managedMTLSBundle{}, err
	}
	caTpl := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Vibewatch Docker Host CA " + parsedIP.String(), Organization: []string{"Vibewatch"}},
		NotBefore:             notBefore,
		NotAfter:              now.AddDate(5, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	caKeyPEM, err := pemECPrivateKey(caKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return managedMTLSBundle{}, err
	}
	serverTpl := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "docker-host-" + parsedIP.String(), Organization: []string{"Vibewatch"}},
		NotBefore:    notBefore,
		NotAfter:     now.AddDate(5, 0, 0),
		IPAddresses:  []net.IP{parsedIP},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTpl, caTpl, &serverKey.PublicKey, caKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	serverKeyPEM, err := pemECPrivateKey(serverKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	clientSerial, err := randomSerial()
	if err != nil {
		return managedMTLSBundle{}, err
	}
	clientNotAfter := now.AddDate(1, 0, 0)
	clientTpl := &x509.Certificate{
		SerialNumber: clientSerial,
		Subject:      pkix.Name{CommonName: "vibewatch-controller", Organization: []string{"Vibewatch"}},
		NotBefore:    notBefore,
		NotAfter:     clientNotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTpl, caTpl, &clientKey.PublicKey, caKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}
	clientKeyPEM, err := pemECPrivateKey(clientKey)
	if err != nil {
		return managedMTLSBundle{}, err
	}

	return managedMTLSBundle{
		CAPEM:          caPEM,
		CAKeyPEM:       caKeyPEM,
		ServerCertPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})),
		ServerKeyPEM:   serverKeyPEM,
		ClientCertPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})),
		ClientKeyPEM:   clientKeyPEM,
		ClientNotAfter: clientNotAfter,
	}, nil
}

func writeSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (a *App) saveHostTLSCredentials(endpoint, caPEM, certPEM, keyPEM string) error {
	if err := validateTLSCredentials(caPEM, certPEM, keyPEM); err != nil {
		return err
	}
	dir := a.Docker.TLSCredentialDir(endpoint)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string]string{"ca.pem": caPEM + "\n", "cert.pem": certPEM + "\n", "key.pem": keyPEM + "\n"}
	for name, contents := range files {
		if err := writeSecretFile(filepath.Join(dir, name), []byte(contents)); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (a *App) saveManagedHostCA(endpoint, caKeyPEM string) error {
	if strings.TrimSpace(caKeyPEM) == "" {
		return nil
	}
	return writeSecretFile(filepath.Join(a.Docker.TLSCredentialDir(endpoint), "ca-key.pem"), []byte(strings.TrimSpace(caKeyPEM)+"\n"))
}

func (a *App) hostTLSInfo(endpoint string) (managed bool, expiresAt string) {
	if a.Docker == nil || !a.Docker.TLSConfigured(endpoint) {
		return false, ""
	}
	dir := a.Docker.TLSCredentialDir(endpoint)
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); err == nil {
		managed = true
	}
	b, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		return managed, ""
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return managed, ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return managed, ""
	}
	return managed, cert.NotAfter.UTC().Format(time.RFC3339)
}

func (a *App) removeHostTLSCredentials(endpoint string) error {
	if strings.ToLower(strings.TrimSpace(endpoint)) == "" {
		return nil
	}
	return os.RemoveAll(a.Docker.TLSCredentialDir(endpoint))
}

// saveManagedHostTLSCredentialsAtomic commits the complete Vibewatch-managed
// client identity as one directory swap. Secure Quick Setup probes the new mTLS
// identity before calling this helper, so an existing managed host never gets a
// half-written CA/certificate/key set if the local filesystem operation fails.
func (a *App) saveManagedHostTLSCredentialsAtomic(endpoint, caPEM, certPEM, keyPEM, caKeyPEM string) error {
	if err := validateTLSCredentials(caPEM, certPEM, keyPEM); err != nil {
		return err
	}
	if strings.TrimSpace(caKeyPEM) == "" {
		return fmt.Errorf("managed CA private key is required")
	}
	dir := a.Docker.TLSCredentialDir(endpoint)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".managed-tls-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}
	files := map[string]string{
		"ca.pem":     strings.TrimSpace(caPEM) + "\n",
		"cert.pem":   strings.TrimSpace(certPEM) + "\n",
		"key.pem":    strings.TrimSpace(keyPEM) + "\n",
		"ca-key.pem": strings.TrimSpace(caKeyPEM) + "\n",
	}
	for name, contents := range files {
		if err := writeSecretFile(filepath.Join(tmp, name), []byte(contents)); err != nil {
			return fmt.Errorf("write staged %s: %w", name, err)
		}
	}

	backup := dir + ".quicksetup-previous"
	_ = os.RemoveAll(backup)
	hadExisting := false
	if _, err := os.Stat(dir); err == nil {
		hadExisting = true
		if err := os.Rename(dir, backup); err != nil {
			return fmt.Errorf("stage previous TLS credentials: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if hadExisting {
			_ = os.Rename(backup, dir)
		}
		return fmt.Errorf("commit managed TLS credentials: %w", err)
	}
	if hadExisting {
		_ = os.RemoveAll(backup)
	}
	return nil
}
