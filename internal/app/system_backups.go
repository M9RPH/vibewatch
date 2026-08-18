package app

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/dockercli"
)

const backupBundleFormatVersion = 1

type backupTLSManifest struct {
	HostID       int64  `json:"host_id"`
	HostName     string `json:"host_name"`
	Endpoint     string `json:"endpoint"`
	CredentialID string `json:"credential_id"`
}

type backupBundleManifest struct {
	FormatVersion       int                 `json:"format_version"`
	VibewatchVersion    string              `json:"vibewatch_version"`
	CreatedAt           string              `json:"created_at"`
	DatabaseFile        string              `json:"database_file"`
	DatabaseSHA256      string              `json:"database_sha256"`
	RegistryKeyIncluded bool                `json:"registry_key_included"`
	TLSCredentials      []backupTLSManifest `json:"tls_credentials"`
	RestoreSupported    bool                `json:"restore_supported"`
	ContainsSecrets     bool                `json:"contains_secrets"`
}

type applicationBackupBundle struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
}

type backupValidationCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type backupValidationResult struct {
	Valid  bool                    `json:"valid"`
	Name   string                  `json:"name"`
	Checks []backupValidationCheck `json:"checks"`
}

func (a *App) backupBundleDir() string { return filepath.Join(a.Cfg.DataDir, "backups", "bundles") }

func safeBackupBundleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || !strings.HasPrefix(name, "vibewatch-backup-") || !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return "", fmt.Errorf("invalid backup bundle name")
	}
	return name, nil
}

func addZipBytes(zw *zip.Writer, name string, data []byte, mode os.FileMode) error {
	h := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
	h.SetMode(mode)
	h.Modified = time.Now().UTC()
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func addZipFile(zw *zip.Writer, name, path string, mode os.FileMode) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, addZipBytes(zw, name, data, mode)
}

func (a *App) createApplicationBackupBundle(ctx context.Context, actor string) (applicationBackupBundle, error) {
	// Reuse the existing transaction-consistent SQLite backup path instead of
	// inventing a second DB snapshot implementation. The raw DB snapshot remains
	// available as an additional recovery artifact.
	dbPath, err := a.createDatabaseBackup(ctx, "bundle-owner")
	if err != nil {
		return applicationBackupBundle{}, err
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		return applicationBackupBundle{}, err
	}
	sum := sha256.Sum256(dbBytes)
	stamp := time.Now().UTC()
	name := "vibewatch-backup-" + stamp.Format("20060102-150405.000000000") + ".zip"
	dir := a.backupBundleDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return applicationBackupBundle{}, err
	}
	tmp, err := os.CreateTemp(dir, ".bundle-*.tmp")
	if err != nil {
		return applicationBackupBundle{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	zw := zip.NewWriter(tmp)

	manifest := backupBundleManifest{
		FormatVersion:    backupBundleFormatVersion,
		VibewatchVersion: a.Cfg.Version,
		CreatedAt:        stamp.Format(time.RFC3339Nano),
		DatabaseFile:     "vibewatch.db",
		DatabaseSHA256:   hex.EncodeToString(sum[:]),
		RestoreSupported: false,
		ContainsSecrets:  true,
		TLSCredentials:   []backupTLSManifest{},
	}
	if err := addZipBytes(zw, "vibewatch.db", dbBytes, 0o600); err != nil {
		_ = zw.Close()
		_ = tmp.Close()
		return applicationBackupBundle{}, err
	}
	if err := addZipBytes(zw, "VERSION", []byte(strings.TrimSpace(a.Cfg.Version)+"\n"), 0o644); err != nil {
		_ = zw.Close()
		_ = tmp.Close()
		return applicationBackupBundle{}, err
	}
	if key, e := os.ReadFile(a.registryKeyPath()); e == nil && len(key) == 32 {
		manifest.RegistryKeyIncluded = true
		if err := addZipBytes(zw, "registry-credentials.key", key, 0o600); err != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return applicationBackupBundle{}, err
		}
	}
	hosts, _ := a.Store.Hosts(ctx)
	for _, h := range hosts {
		if dockercli.ConnectionType(h.Endpoint) != "tls" || !a.Docker.TLSConfigured(h.Endpoint) {
			continue
		}
		credID := filepath.Base(a.Docker.TLSCredentialDir(h.Endpoint))
		entry := backupTLSManifest{HostID: h.ID, HostName: h.Name, Endpoint: h.Endpoint, CredentialID: credID}
		for _, fn := range []string{"ca.pem", "cert.pem", "key.pem"} {
			if _, e := addZipFile(zw, filepath.Join("host-tls", credID, fn), filepath.Join(a.Docker.TLSCredentialDir(h.Endpoint), fn), 0o600); e != nil {
				_ = zw.Close()
				_ = tmp.Close()
				return applicationBackupBundle{}, fmt.Errorf("include TLS credentials for %s: %w", h.Name, e)
			}
		}
		// Vibewatch-managed secure quick setup keeps the private host CA only on
		// the controller. Include it when present so a controller backup can
		// retain certificate-management continuity. Manually supplied TLS hosts
		// simply do not have this optional file.
		if _, statErr := os.Stat(filepath.Join(a.Docker.TLSCredentialDir(h.Endpoint), "ca-key.pem")); statErr == nil {
			if _, e := addZipFile(zw, filepath.Join("host-tls", credID, "ca-key.pem"), filepath.Join(a.Docker.TLSCredentialDir(h.Endpoint), "ca-key.pem"), 0o600); e != nil {
				_ = zw.Close()
				_ = tmp.Close()
				return applicationBackupBundle{}, fmt.Errorf("include managed TLS CA for %s: %w", h.Name, e)
			}
		}
		manifest.TLSCredentials = append(manifest.TLSCredentials, entry)
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := addZipBytes(zw, "manifest.json", append(manifestBytes, '\n'), 0o600); err != nil {
		_ = zw.Close()
		_ = tmp.Close()
		return applicationBackupBundle{}, err
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return applicationBackupBundle{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return applicationBackupBundle{}, err
	}
	if err := tmp.Close(); err != nil {
		return applicationBackupBundle{}, err
	}
	finalPath := filepath.Join(dir, name)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return applicationBackupBundle{}, err
	}
	_ = os.Chmod(finalPath, 0o600)
	_ = a.Store.Audit(context.Background(), actor, "system.backup-bundle.create", 0, "", "file="+name)

	// Retain the ten newest complete bundles, mirroring the raw database policy.
	if entries, e := os.ReadDir(dir); e == nil {
		files := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "vibewatch-backup-") && strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
				files = append(files, e.Name())
			}
		}
		if len(files) > 10 {
			sort.Strings(files)
			for _, old := range files[:len(files)-10] {
				_ = os.Remove(filepath.Join(dir, old))
			}
		}
	}
	st, statErr := os.Stat(finalPath)
	if statErr != nil {
		return applicationBackupBundle{}, fmt.Errorf("stat completed backup bundle: %w", statErr)
	}
	return applicationBackupBundle{Name: name, SizeBytes: st.Size(), ModifiedAt: st.ModTime().UTC().Format(time.RFC3339Nano)}, nil
}

func (a *App) listApplicationBackupBundles() ([]applicationBackupBundle, error) {
	dir := a.backupBundleDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []applicationBackupBundle{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "vibewatch-backup-") || !strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			continue
		}
		st, e2 := e.Info()
		if e2 != nil {
			continue
		}
		out = append(out, applicationBackupBundle{Name: e.Name(), SizeBytes: st.Size(), ModifiedAt: st.ModTime().UTC().Format(time.RFC3339Nano)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func zipEntryBytes(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, 512*1024*1024))
	}
	return nil, os.ErrNotExist
}

func (a *App) validateApplicationBackupBundle(ctx context.Context, name string) backupValidationResult {
	result := backupValidationResult{Name: name, Valid: true, Checks: []backupValidationCheck{}}
	fail := func(n, d string) {
		result.Valid = false
		result.Checks = append(result.Checks, backupValidationCheck{Name: n, Status: "failed", Detail: d})
	}
	pass := func(n, d string) {
		result.Checks = append(result.Checks, backupValidationCheck{Name: n, Status: "ok", Detail: d})
	}
	path := filepath.Join(a.backupBundleDir(), name)
	zr, err := zip.OpenReader(path)
	if err != nil {
		fail("ZIP archive", err.Error())
		return result
	}
	defer zr.Close()
	manifestBytes, err := zipEntryBytes(&zr.Reader, "manifest.json")
	if err != nil {
		fail("Manifest", "manifest.json is missing")
		return result
	}
	var manifest backupBundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		fail("Manifest", err.Error())
		return result
	}
	if manifest.FormatVersion != backupBundleFormatVersion {
		fail("Backup format", fmt.Sprintf("unsupported format %d", manifest.FormatVersion))
	} else {
		pass("Backup format", fmt.Sprintf("format %d · created by Vibewatch %s", manifest.FormatVersion, manifest.VibewatchVersion))
	}
	dbBytes, err := zipEntryBytes(&zr.Reader, manifest.DatabaseFile)
	if err != nil {
		fail("Database", "database file is missing")
		return result
	}
	sum := sha256.Sum256(dbBytes)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(manifest.DatabaseSHA256)) {
		fail("Database checksum", "SHA256 does not match manifest")
	} else {
		pass("Database checksum", "SHA256 matches manifest")
	}
	tmp, err := os.CreateTemp("", "vibewatch-validate-*.db")
	if err != nil {
		fail("SQLite integrity", err.Error())
		return result
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	if err := os.WriteFile(tmpPath, dbBytes, 0o600); err != nil {
		fail("SQLite integrity", err.Error())
		return result
	}
	checkStore := db.New(tmpPath)
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	integrity, err := checkStore.IntegrityCheck(checkCtx)
	if err != nil || strings.TrimSpace(strings.ToLower(integrity)) != "ok" {
		if err != nil {
			fail("SQLite integrity", err.Error())
		} else {
			fail("SQLite integrity", integrity)
		}
	} else {
		pass("SQLite integrity", "PRAGMA integrity_check = ok")
	}
	if manifest.RegistryKeyIncluded {
		b, e := zipEntryBytes(&zr.Reader, "registry-credentials.key")
		if e != nil || len(b) != 32 {
			fail("Registry encryption key", "manifest expects a 32-byte registry key")
		} else {
			pass("Registry encryption key", "included")
		}
	} else {
		pass("Registry encryption key", "not present in this installation")
	}
	for _, t := range manifest.TLSCredentials {
		base := filepath.ToSlash(filepath.Join("host-tls", t.CredentialID))
		ca, e1 := zipEntryBytes(&zr.Reader, base+"/ca.pem")
		cert, e2 := zipEntryBytes(&zr.Reader, base+"/cert.pem")
		key, e3 := zipEntryBytes(&zr.Reader, base+"/key.pem")
		if e1 != nil || e2 != nil || e3 != nil {
			fail("TLS/mTLS · "+t.HostName, "credential files are incomplete")
			continue
		}
		if e := validateTLSCredentials(string(ca), string(cert), string(key)); e != nil {
			fail("TLS/mTLS · "+t.HostName, e.Error())
		} else {
			pass("TLS/mTLS · "+t.HostName, "CA and client certificate/key are valid PEM")
		}
	}
	return result
}

func (a *App) handleSystemBackups(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		x, err := a.listApplicationBackupBundles()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, x)
		return
	}
	if r.Method == http.MethodPost {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		x, err := a.createApplicationBackupBundle(ctx, a.actor(r))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, x)
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleSystemBackupSubroutes(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/system/backups/"), "/"), "/")
	if len(parts) < 1 {
		writeErr(w, 404, "not found")
		return
	}
	name, err := safeBackupBundleName(parts[0])
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	path := filepath.Join(a.backupBundleDir(), name)
	if len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet {
		if _, err := os.Stat(path); err != nil {
			writeErr(w, 404, "backup bundle not found")
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = a.Store.Audit(context.Background(), a.actor(r), "system.backup-bundle.download", 0, "", "file="+name)
		http.ServeFile(w, r, path)
		return
	}
	if len(parts) == 2 && parts[1] == "validate" && r.Method == http.MethodPost {
		if _, err := os.Stat(path); err != nil {
			writeErr(w, 404, "backup bundle not found")
			return
		}
		res := a.validateApplicationBackupBundle(r.Context(), name)
		_ = a.Store.Audit(context.Background(), a.actor(r), "system.backup-bundle.validate", 0, "", fmt.Sprintf("file=%s valid=%t", name, res.Valid))
		writeJSON(w, 200, res)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				writeErr(w, 404, "backup bundle not found")
			} else {
				writeErr(w, 500, err.Error())
			}
			return
		}
		_ = a.Store.Audit(context.Background(), a.actor(r), "system.backup-bundle.delete", 0, "", "file="+name)
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	writeErr(w, 404, "not found")
}
