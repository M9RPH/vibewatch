package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/app"
	"github.com/watchtower-ui/watchtower-ui/internal/auth"
	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
	logx "github.com/watchtower-ui/watchtower-ui/internal/logging"
	"github.com/watchtower-ui/watchtower-ui/internal/notify"
	"github.com/watchtower-ui/watchtower-ui/internal/registry"
	"github.com/watchtower-ui/watchtower-ui/internal/releases"
	"github.com/watchtower-ui/watchtower-ui/internal/sshsetup"
	"github.com/watchtower-ui/watchtower-ui/internal/watchtower"
)

var version = "dev"

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func trimStartupBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "pre-start-") && strings.HasSuffix(e.Name(), ".db") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Strings(files)
	for _, name := range files[:len(files)-keep] {
		_ = os.Remove(filepath.Join(dir, name))
		_ = os.Remove(filepath.Join(dir, name+".registry-key"))
	}
}

func main() {
	data := env("WTUI_DATA_DIR", "/data")
	logger, _, err := logx.New(filepath.Join(data, "logs", "app.log"), env("WTUI_LOG_LEVEL", "INFO"))
	if err != nil {
		log.Fatal(err)
	}
	legacyDBPath := filepath.Join(data, "watchtower-ui.db")
	dbPath := filepath.Join(data, "vibewatch.db")
	// One-time in-place migration from the pre-rebrand database filename. The
	// entire /data directory is persistent, so an atomic rename preserves all
	// hosts, users, policies, schedules, tokens, jobs and logs.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if _, legacyErr := os.Stat(legacyDBPath); legacyErr == nil {
			if renameErr := os.Rename(legacyDBPath, dbPath); renameErr != nil {
				logger.Error("legacy database rename failed", "from", legacyDBPath, "to", dbPath, "error", renameErr)
				os.Exit(1)
			}
			logger.Info("legacy database renamed", "from", filepath.Base(legacyDBPath), "to", filepath.Base(dbPath))
		}
	}
	store := db.New(dbPath)
	// V0.5.2: Docker event history moved out of the primary database. V0.5.0/
	// V0.5.1 could accumulate more than 100k healthcheck/exec events in SQLite;
	// if that event B-tree/index alone became malformed, rebuild only the proven-
	// healthy core tables into a clean primary DB before normal startup. A raw
	// copy of the damaged DB (+ WAL/SHM sidecars) is retained first.
	if st, statErr := os.Stat(dbPath); statErr == nil && st.Size() > 0 {
		backupDir := filepath.Join(data, "backups")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		repaired, preserved, repairErr := store.RepairDockerEventCorruption(ctx, backupDir)
		if repairErr != nil {
			logger.Warn("database event-corruption auto-repair not applied", "error", repairErr)
		} else if repaired {
			logger.Warn("database repaired by rebuilding core state without corrupted Docker events", "preserved_database", preserved)
		}
		cancel()
	}

	// Snapshot an existing database before this binary runs additive schema
	// migrations. This also protects manual docker-compose/image upgrades that
	// bypass the in-app self-update helper. Keep only the five newest startup
	// snapshots; explicit pre-self-update/manual backups have their own retention.
	if st, statErr := os.Stat(dbPath); statErr == nil && st.Size() > 0 {
		backupDir := filepath.Join(data, "backups")
		backupPath := filepath.Join(backupDir, "pre-start-"+time.Now().UTC().Format("20060102-150405.000000000")+".db")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if backupErr := store.Backup(ctx, backupPath); backupErr != nil {
			logger.Warn("pre-migration database backup failed", "error", backupErr)
		} else {
			keyPath := filepath.Join(data, "registry-credentials.key")
			if keyBytes, keyErr := os.ReadFile(keyPath); keyErr == nil {
				if writeErr := os.WriteFile(backupPath+".registry-key", keyBytes, 0o600); writeErr != nil {
					logger.Warn("registry credential key backup failed", "error", writeErr)
				}
			}
			logger.Info("pre-migration database backup created", "file", filepath.Base(backupPath))
			trimStartupBackups(backupDir, 5)
		}
		cancel()
	}
	if err := store.Init(context.Background()); err != nil {
		logger.Error("database init failed", "error", err)
		os.Exit(1)
	}
	if migrated, err := store.MigrateLegacyDockerEvents(context.Background(), 500); err != nil {
		logger.Warn("legacy Docker event history could not be migrated", "error", err)
	} else if migrated > 0 {
		logger.Info("legacy Docker event history moved to bounded JSONL store", "events", migrated)
	}
	d := dockercli.New(logger)
	d.WorkerImage = env("WTUI_WATCHTOWER_IMAGE", "nickfedor/watchtower:latest")
	d.WorkerNetwork = env("WTUI_WORKER_NETWORK", "vibewatch-internal")
	d.WorkerPort = env("WTUI_WORKER_PORT", "8080")
	secret := env("WTUI_SESSION_SECRET", "")
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = hex.EncodeToString(b)
		logger.Warn("WTUI_SESSION_SECRET is not set; sessions will reset after restart")
	}
	secure := strings.EqualFold(env("WTUI_SECURE_COOKIE", "false"), "true")
	ownerEnvPassword := env("WTUI_ADMIN_PASSWORD", "")
	ownerOverrideHash := store.Setting(context.Background(), "owner_password_hash", "")
	authPassword := ownerEnvPassword
	if authPassword == "" && ownerOverrideHash != "" {
		// Keep cookie authentication enabled even when the Owner has moved from
		// the bootstrap .env password to a persistent Vibewatch password.
		authPassword = "__vibewatch_owner_password_override__"
	}
	authm := auth.New(authPassword, secret, secure)
	if !authm.Enabled() {
		logger.Warn("authentication is disabled; set WTUI_ADMIN_PASSWORD before exposing the UI")
	}
	// V0.4.5 migration: Pushover credentials are now fully per-account. If an
	// older installation had a shared application token, import it once into
	// the Owner account (reserved user_id 0). It is never inherited by Admins
	// or Users. The old settings/.env values are otherwise inactive.
	ownerNotifications, _ := store.NotificationSettings(context.Background(), 0)
	legacyPersistentToken := strings.TrimSpace(store.Setting(context.Background(), "pushover_app_token", ""))
	if strings.TrimSpace(ownerNotifications.PushoverAppToken) == "" {
		legacyToken := legacyPersistentToken
		if legacyToken == "" {
			legacyToken = strings.TrimSpace(env("PUSHOVER_APP_TOKEN", ""))
		}
		if legacyToken != "" {
			ownerNotifications.PushoverAppToken = legacyToken
			if err := store.SaveNotificationSettings(context.Background(), ownerNotifications); err != nil {
				logger.Warn("could not migrate legacy Pushover token to Owner account", "error", err)
			} else {
				logger.Info("legacy Pushover application token migrated to Owner account")
			}
		}
	}
	// The old persistent shared token is no longer an active credential source.
	// Clear that duplicate secret after migration; a legacy environment value is
	// left untouched on the host but is never used for Admin/User delivery.
	if legacyPersistentToken != "" && strings.TrimSpace(ownerNotifications.PushoverAppToken) != "" {
		_ = store.SetSetting(context.Background(), "pushover_app_token", "")
	}
	a := app.New(app.Config{DataDir: data, WebDir: env("WTUI_WEB_DIR", "/app/web"), Timezone: env("TZ", "Europe/Berlin"), Version: version, AppImage: env("WTUI_APP_IMAGE", ""), ControllerName: env("WTUI_CONTAINER_NAME", "vibewatch")}, store, d, watchtower.New(), releases.New(), registry.New(), notify.NewPushover(), sshsetup.New(data), logger, authm)
	a.Start()
	defer a.Stop()
	srv := &http.Server{Addr: env("WTUI_LISTEN", ":8080"), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		logger.Info("vibewatch started", "addr", srv.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
