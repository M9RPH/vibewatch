package app

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

func (a *App) removeHostTLSCredentials(endpoint string) error {
	if strings.ToLower(strings.TrimSpace(endpoint)) == "" {
		return nil
	}
	return os.RemoveAll(a.Docker.TLSCredentialDir(endpoint))
}
