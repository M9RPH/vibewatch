package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/watchtower-ui/watchtower-ui/internal/registry"
)

type registryCredentialInput struct {
	ID       int64  `json:"id"`
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

type registryCredentialView struct {
	ID               int64  `json:"id"`
	Registry         string `json:"registry"`
	Username         string `json:"username"`
	SecretConfigured bool   `json:"secret_configured"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func normalizeRegistrySetting(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimSuffix(v, "/")
	switch v {
	case "docker.io", "index.docker.io":
		return "registry-1.docker.io"
	}
	return v
}

func (a *App) registryKeyPath() string {
	return filepath.Join(a.Cfg.DataDir, "registry-credentials.key")
}

func (a *App) ensureRegistryKey() ([]byte, error) {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	if len(a.registryKey) == 32 {
		return append([]byte(nil), a.registryKey...), nil
	}
	path := a.registryKeyPath()
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("registry credential key has invalid length")
		}
		_ = os.Chmod(path, 0o600)
		a.registryKey = append([]byte(nil), b...)
		return append([]byte(nil), b...), nil
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	a.registryKey = append([]byte(nil), b...)
	return append([]byte(nil), b...), nil
}

func encryptRegistrySecret(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), []byte("vibewatch-registry-v1"))
	return "v1:" + base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func decryptRegistrySecret(key []byte, encoded string) (string, error) {
	if !strings.HasPrefix(encoded, "v1:") {
		return "", fmt.Errorf("unsupported registry credential encoding")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted registry credential is truncated")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte("vibewatch-registry-v1"))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (a *App) reloadRegistryCredentials(ctx context.Context) error {
	if a.Registry == nil {
		return nil
	}
	rows, err := a.Store.RegistryCredentials(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		a.Registry.SetCredentials(nil)
		return nil
	}
	key, err := a.ensureRegistryKey()
	if err != nil {
		return err
	}
	creds := make([]registry.Credential, 0, len(rows))
	for _, row := range rows {
		secret, e := decryptRegistrySecret(key, row.SecretEnc)
		if e != nil {
			if a.Logger != nil {
				a.Logger.Warn("registry credential could not be decrypted", "registry", row.Registry, "error", e)
			}
			continue
		}
		creds = append(creds, registry.Credential{Registry: row.Registry, Username: row.Username, Secret: secret})
	}
	a.Registry.SetCredentials(creds)
	return nil
}

func (a *App) handleRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := a.Store.RegistryCredentials(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out := make([]registryCredentialView, 0, len(rows))
		for _, x := range rows {
			out = append(out, registryCredentialView{ID: x.ID, Registry: x.Registry, Username: x.Username, SecretConfigured: x.SecretEnc != "", CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt})
		}
		writeJSON(w, 200, out)
	case http.MethodPost, http.MethodPut:
		var in registryCredentialInput
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		in.Registry = normalizeRegistrySetting(in.Registry)
		in.Username = strings.TrimSpace(in.Username)
		if in.Registry == "" {
			writeErr(w, 400, "registry is required")
			return
		}
		secret := strings.TrimSpace(in.Secret)
		if secret == "" && in.ID > 0 {
			rows, _ := a.Store.RegistryCredentials(r.Context())
			for _, x := range rows {
				if x.ID == in.ID {
					secretEnc := x.SecretEnc
					id, err := a.Store.SaveRegistryCredential(r.Context(), in.Registry, in.Username, secretEnc)
					if err != nil {
						writeErr(w, 500, err.Error())
						return
					}
					_ = a.reloadRegistryCredentials(r.Context())
					_ = a.Store.Audit(r.Context(), a.actor(r), "registry-credential.update", 0, "", in.Registry)
					writeJSON(w, 200, map[string]any{"ok": true, "id": id})
					return
				}
			}
		}
		if secret == "" {
			writeErr(w, 400, "registry secret/token is required")
			return
		}
		key, err := a.ensureRegistryKey()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		enc, err := encryptRegistrySecret(key, secret)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if in.ID > 0 {
			_ = a.Store.DeleteRegistryCredential(r.Context(), in.ID)
		}
		id, err := a.Store.SaveRegistryCredential(r.Context(), in.Registry, in.Username, enc)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err = a.reloadRegistryCredentials(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "registry-credential.save", 0, "", in.Registry)
		writeJSON(w, 200, map[string]any{"ok": true, "id": id})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (a *App) handleRegistryCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/system/registry-credentials/")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		writeErr(w, 400, "invalid credential id")
		return
	}
	if err := a.Store.DeleteRegistryCredential(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.reloadRegistryCredentials(r.Context())
	_ = a.Store.Audit(r.Context(), a.actor(r), "registry-credential.delete", 0, "", fmt.Sprintf("id=%d", id))
	writeJSON(w, 200, map[string]any{"ok": true})
}
