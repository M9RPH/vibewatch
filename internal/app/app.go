package app

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/auth"
	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
	"github.com/watchtower-ui/watchtower-ui/internal/notify"
	"github.com/watchtower-ui/watchtower-ui/internal/registry"
	"github.com/watchtower-ui/watchtower-ui/internal/releases"
	"github.com/watchtower-ui/watchtower-ui/internal/scheduler"
	"github.com/watchtower-ui/watchtower-ui/internal/sshsetup"
	"github.com/watchtower-ui/watchtower-ui/internal/watchtower"
)

type Config struct {
	DataDir        string
	WebDir         string
	Timezone       string
	Version        string
	AppImage       string
	ControllerName string
}
type App struct {
	Cfg           Config
	Store         *db.Store
	Docker        *dockercli.Client
	WT            *watchtower.Client
	Releases      *releases.GitHubClient
	Registry      *registry.Client
	Pushover      *notify.Pushover
	SSH           *sshsetup.Client
	Logger        *slog.Logger
	Auth          *auth.Manager
	Events        *dockercli.EventWatcher
	Queue         chan updateRequest
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	workerOpMu    sync.RWMutex
	maintenanceMu sync.Mutex
	infoRefreshMu sync.Mutex
	infoRefresh   map[string]bool
	registryMu    sync.Mutex
	registryKey   []byte
}
type updateRequest struct {
	JobID     int64
	HostID    int64
	Container string
	Trigger   string
	Actor     string
}

type ContainerView struct {
	dockercli.Container
	SystemManaged bool             `json:"system_managed"`
	SystemRole    string           `json:"system_role,omitempty"`
	Policy        db.Policy        `json:"policy"`
	Cache         db.Cache         `json:"update"`
	Version       db.VersionInfo   `json:"version"`
	ConfigDrift   ConfigDriftView  `json:"config_drift"`
	RestorePoint  *db.RestorePoint `json:"restore_point,omitempty"`
}

type hostInput struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}
type HostView struct {
	db.Host
	Worker          dockercli.WorkerState `json:"worker"`
	DockerReachable bool                  `json:"docker_reachable"`
	DockerVersion   string                `json:"docker_version,omitempty"`
	DockerError     string                `json:"docker_error,omitempty"`
}

type policyInput struct {
	Mode                 string `json:"mode"`
	CheckIntervalMinutes int    `json:"check_interval_minutes"`
	ReleaseRepo          string `json:"release_repo"`
}
type scheduleInput struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Cron       string   `json:"cron"`
	Action     string   `json:"action"`
	HostID     int64    `json:"host_id"`
	Containers []string `json:"containers"`
	Enabled    bool     `json:"enabled"`
}
type automationInput struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Enabled    bool   `json:"enabled"`
}
type groupInput struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	HostIDs     []int64 `json:"host_ids"`
}
type userInput struct {
	ID       int64   `json:"id"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Role     string  `json:"role"`
	Enabled  bool    `json:"enabled"`
	HostIDs  []int64 `json:"host_ids"`
	GroupIDs []int64 `json:"group_ids"`
}
type publicUser struct {
	ID              int64   `json:"id"`
	Username        string  `json:"username"`
	Role            string  `json:"role"`
	Enabled         bool    `json:"enabled"`
	HostIDs         []int64 `json:"host_ids"`
	GroupIDs        []int64 `json:"group_ids"`
	CreatedAt       string  `json:"created_at"`
	PasswordManaged bool    `json:"password_managed,omitempty"`
}

type clientErrorInput struct {
	Message string `json:"message"`
	Stack   string `json:"stack"`
	URL     string `json:"url"`
}

type notificationInput struct {
	PushoverAppToken      string `json:"pushover_app_token"`
	ClearPushoverAppToken bool   `json:"clear_pushover_app_token"`
	PushoverUserKey       string `json:"pushover_user_key"`
	NotifyAutoUpdates     bool   `json:"notify_auto_updates"`
	NotifyManualAvailable bool   `json:"notify_manual_available"`
	NotifyManualUpdates   bool   `json:"notify_manual_updates"`
}
type systemSettingsInput struct {
	WorkerAutoUpdate           bool   `json:"worker_auto_update"`
	WorkerUpdateCron           string `json:"worker_update_cron"`
	SelfUpdateAuto             bool   `json:"self_update_auto"`
	SelfUpdateCron             string `json:"self_update_cron"`
	ContainerSnapshotRetention int    `json:"container_snapshot_retention"`
}
type quickSetupInput struct {
	Name                   string `json:"name"`
	IP                     string `json:"ip"`
	SSHPort                int    `json:"ssh_port"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	AcknowledgeInsecureTCP bool   `json:"acknowledge_insecure_tcp"`
}

func New(cfg Config, store *db.Store, docker *dockercli.Client, wt *watchtower.Client, gh *releases.GitHubClient, reg *registry.Client, po *notify.Pushover, ssh *sshsetup.Client, logger *slog.Logger, authm *auth.Manager) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{Cfg: cfg, Store: store, Docker: docker, WT: wt, Releases: gh, Registry: reg, Pushover: po, SSH: ssh, Logger: logger, Auth: authm, Events: dockercli.NewEventWatcher(), Queue: make(chan updateRequest, 200), ctx: ctx, cancel: cancel, infoRefresh: map[string]bool{}}
}
func (a *App) Start() {
	if err := a.reloadRegistryCredentials(a.ctx); err != nil && a.Logger != nil {
		a.Logger.Warn("registry credentials could not be loaded", "error", err)
	}
	go a.updateWorker()
	go a.automationLoop()
	go a.workerSupervisorLoop()
	go a.systemMaintenanceLoop()
	hosts, _ := a.Store.Hosts(a.ctx)
	for _, h := range hosts {
		if h.Enabled {
			a.Events.Start(a.ctx, a.Docker, a.Store, h)
			go a.startHostWorker(h, "startup")
		}
	}
	// The Compose migration helper is intentionally one-shot. Once this
	// controller is alive its work is complete, so remove the exited helper
	// instead of leaving it behind in `docker ps -a`. Legacy runtime/network
	// cleanup remains best-effort and independent.
	go func() {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(3 * time.Second):
			a.Docker.CleanupMigrationContainer(context.Background())
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(87 * time.Second):
			a.Docker.CleanupLegacyRuntime(context.Background())
		}
	}()
}

func (a *App) startHostWorker(h db.Host, reason string) {
	a.workerOpMu.RLock()
	defer a.workerOpMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	a.Logger.Info("vibewatch worker launch requested", "host", h.Name, "host_id", h.ID, "reason", reason, "endpoint", h.Endpoint)
	if _, err := a.Docker.Ping(ctx, h.Endpoint); err != nil {
		a.Logger.Error("vibewatch worker launch skipped: target docker endpoint unavailable", "host", h.Name, "host_id", h.ID, "error", err)
		return
	}
	if _, err := a.ensureWorker(ctx, h); err != nil {
		state := a.Docker.WorkerState(context.Background(), h.ID)
		logs, _ := a.Docker.WorkerLogsRecent(context.Background(), h.ID, 120)
		a.Logger.Error("vibewatch worker not ready", "host", h.Name, "host_id", h.ID, "state", state.Status, "exit_code", state.ExitCode, "restarting", state.Restarting, "error", err, "worker_logs", logs)
		return
	}
	a.Logger.Info("vibewatch worker ready", "host", h.Name, "host_id", h.ID, "worker", a.Docker.WorkerState(context.Background(), h.ID).Name)
}
func (a *App) workerSupervisorLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			hosts, err := a.Store.Hosts(a.ctx)
			if err != nil {
				a.Logger.Error("worker supervisor could not load hosts", "error", err)
				continue
			}
			for _, h := range hosts {
				if !h.Enabled {
					continue
				}
				state := a.Docker.WorkerState(a.ctx, h.ID)
				if !state.Running && !state.Restarting {
					a.Logger.Warn("worker supervisor detected stopped worker", "host", h.Name, "host_id", h.ID, "status", state.Status, "exit_code", state.ExitCode)
					go a.startHostWorker(h, "supervisor")
				}
			}
		}
	}
}

func (a *App) Stop() {
	a.cancel()
	// Workers are dynamically created and therefore outside the Compose
	// project. Remove them synchronously on graceful controller shutdown so
	// `docker compose down` leaves no Vibewatch workers (and can remove the
	// internal network cleanly). A subsequent `up` recreates them from the
	// persistent host records.
	a.workerOpMu.Lock()
	defer a.workerOpMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	removed, err := a.Docker.RemoveManagedWorkers(ctx)
	if err != nil {
		a.Logger.Warn("worker shutdown cleanup failed", "removed", removed, "error", err)
		return
	}
	a.Logger.Info("worker shutdown cleanup complete", "removed", removed)
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": a.Cfg.Version, "timezone": a.Cfg.Timezone})
	})
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Auth.Session(r)
		writeJSON(w, 200, map[string]any{"authenticated": ok, "auth_enabled": a.Auth.Enabled(), "user": id})
	})
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("POST /api/logout", a.handleLogout)
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/hosts", a.handleHosts)
	protected.HandleFunc("POST /api/hosts", a.handleHosts)
	protected.HandleFunc("POST /api/hosts/quick-setup", a.handleHostQuickSetup)
	protected.HandleFunc("/api/hosts/", a.handleHostSubroutes)
	protected.HandleFunc("GET /api/jobs", a.handleJobs)
	protected.HandleFunc("GET /api/jobs/", a.handleJobLogs)
	protected.HandleFunc("GET /api/job-status/", a.handleJobStatus)
	protected.HandleFunc("GET /api/automations", a.handleAutomations)
	protected.HandleFunc("POST /api/automations", a.handleAutomations)
	protected.HandleFunc("DELETE /api/automations/", a.handleAutomationDelete)
	protected.HandleFunc("GET /api/groups", a.handleGroups)
	protected.HandleFunc("POST /api/groups", a.handleGroups)
	protected.HandleFunc("DELETE /api/groups/", a.handleGroupDelete)
	protected.HandleFunc("GET /api/users", a.handleUsers)
	protected.HandleFunc("POST /api/users", a.handleUsers)
	protected.HandleFunc("DELETE /api/users/", a.handleUserDelete)
	protected.HandleFunc("GET /api/notifications/me", a.handleNotificationSettings)
	protected.HandleFunc("PUT /api/notifications/me", a.handleNotificationSettings)
	protected.HandleFunc("POST /api/notifications/test", a.handleNotificationTest)
	protected.HandleFunc("GET /api/system/settings", a.handleSystemSettings)
	protected.HandleFunc("PUT /api/system/settings", a.handleSystemSettings)
	protected.HandleFunc("POST /api/system/worker-update", a.handleWorkerUpdate)
	protected.HandleFunc("POST /api/system/backup", a.handleSystemBackup)
	protected.HandleFunc("GET /api/system/self-update", a.handleSelfUpdate)
	protected.HandleFunc("POST /api/system/self-update", a.handleSelfUpdate)
	// Legacy V0.1 schedule endpoints remain readable for diagnostics only.
	protected.HandleFunc("GET /api/schedules", a.handleSchedules)
	protected.HandleFunc("GET /api/audit", a.handleAudit)
	protected.HandleFunc("GET /api/docker-events", a.handleDockerEvents)
	protected.HandleFunc("GET /api/logs/application", a.handleApplicationLogs)
	protected.HandleFunc("GET /api/logs/pushover", a.handlePushoverLogs)
	protected.HandleFunc("GET /api/container-backups", a.handleContainerBackups)
	protected.HandleFunc("GET /api/container-backups/download-all", a.handleContainerBackupDownloadAll)
	protected.HandleFunc("POST /api/container-backups/snapshot", a.handleContainerBackupSnapshot)
	protected.HandleFunc("GET /api/container-backups/download", a.handleContainerBackupDownload)
	protected.HandleFunc("GET /api/update-history", a.handleUpdateHistory)
	protected.HandleFunc("GET /api/restore-points", a.handleRestorePoints)
	protected.HandleFunc("POST /api/rollback", a.handleRollback)
	protected.HandleFunc("GET /api/system/registry-credentials", a.handleRegistryCredentials)
	protected.HandleFunc("POST /api/system/registry-credentials", a.handleRegistryCredentials)
	protected.HandleFunc("PUT /api/system/registry-credentials", a.handleRegistryCredentials)
	protected.HandleFunc("DELETE /api/system/registry-credentials/", a.handleRegistryCredentialDelete)
	protected.HandleFunc("GET /api/support-bundle", a.handleSupportBundle)
	protected.HandleFunc("POST /api/client-errors", a.handleClientError)
	mux.Handle("/api/", a.Auth.Middleware(a.activeUserMiddleware(protected)))
	mux.Handle("/", spaHandler(a.Cfg.WebDir))
	return requestLog(a.Logger, mux)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var x struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	username := strings.TrimSpace(x.Username)
	if username == "" || strings.EqualFold(username, "admin") || strings.EqualFold(username, "owner") {
		overrideHash := strings.TrimSpace(a.Store.Setting(r.Context(), "owner_password_hash", ""))
		if !a.Auth.Enabled() && overrideHash == "" {
			id := auth.Identity{Username: "admin", Role: "owner"}
			a.Auth.SetSession(w, id)
			writeJSON(w, 200, map[string]any{"ok": true, "user": id})
			return
		}
		valid := false
		if overrideHash != "" {
			valid = auth.VerifyPassword(overrideHash, x.Password)
		} else {
			valid = a.Auth.CheckAdminPassword(x.Password)
		}
		if valid {
			id := auth.Identity{UserID: 0, Username: "admin", Role: "owner"}
			a.Auth.SetSession(w, id)
			_ = a.Store.Audit(r.Context(), id.Username, "login", 0, "", "")
			writeJSON(w, 200, map[string]any{"ok": true, "user": id})
			return
		}
		writeErr(w, 401, "invalid username or password")
		return
	}
	u, err := a.Store.UserByUsername(r.Context(), username)
	if err != nil || !bool(u.Enabled) || !auth.VerifyPassword(u.PasswordHash, x.Password) {
		writeErr(w, 401, "invalid username or password")
		return
	}
	id := auth.Identity{UserID: u.ID, Username: u.Username, Role: u.Role}
	a.Auth.SetSession(w, id)
	_ = a.Store.Audit(r.Context(), id.Username, "login", 0, "", "")
	writeJSON(w, 200, map[string]any{"ok": true, "user": id})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.Auth.Logout(w)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) activeUserMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.Auth.Session(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if id.UserID > 0 {
			u, err := a.Store.User(r.Context(), id.UserID)
			if err != nil || !bool(u.Enabled) || u.Role != id.Role {
				writeErr(w, http.StatusUnauthorized, "account disabled, changed or unavailable")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) identity(r *http.Request) auth.Identity { id, _ := a.Auth.Session(r); return id }
func (a *App) actor(r *http.Request) string {
	id := a.identity(r)
	if id.Username != "" {
		return id.Username
	}
	return "system"
}
func (a *App) isAdmin(r *http.Request) bool {
	role := a.identity(r).Role
	return role == "owner" || role == "admin"
}
func (a *App) isOwner(r *http.Request) bool { return a.identity(r).Role == "owner" }
func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if a.isAdmin(r) {
		return true
	}
	writeErr(w, http.StatusForbidden, "admin permission required")
	return false
}
func (a *App) requireOwner(w http.ResponseWriter, r *http.Request) bool {
	if a.isOwner(r) {
		return true
	}
	writeErr(w, http.StatusForbidden, "owner permission required")
	return false
}
func (a *App) allowedHostSet(ctx context.Context, id auth.Identity) map[int64]bool {
	out := map[int64]bool{}
	if id.Role == "owner" || id.Role == "admin" {
		hs, _ := a.Store.Hosts(ctx)
		for _, h := range hs {
			out[h.ID] = true
		}
		return out
	}
	xs, _ := a.Store.AllowedHostIDs(ctx, id.UserID)
	for _, v := range xs {
		out[v] = true
	}
	return out
}
func (a *App) hostAllowed(r *http.Request, hostID int64) bool {
	return a.allowedHostSet(r.Context(), a.identity(r))[hostID]
}

func (a *App) handleHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		hosts, err := a.Store.Hosts(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		allowed := a.allowedHostSet(r.Context(), a.identity(r))
		filtered := make([]db.Host, 0, len(hosts))
		for _, h := range hosts {
			if allowed[h.ID] {
				filtered = append(filtered, h)
			}
		}
		views := make([]HostView, len(filtered))
		var wg sync.WaitGroup
		for i, h := range filtered {
			i, h := i, h
			wg.Add(1)
			go func() {
				defer wg.Done()
				view := HostView{Host: h, Worker: a.Docker.WorkerState(r.Context(), h.ID)}
				if h.Enabled {
					ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
					defer cancel()
					if version, pingErr := a.Docker.Ping(ctx, h.Endpoint); pingErr != nil {
						view.DockerError = pingErr.Error()
					} else {
						view.DockerReachable = true
						view.DockerVersion = version
					}
				}
				views[i] = view
			}()
		}
		wg.Wait()
		writeJSON(w, 200, views)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	var in hostInput
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Endpoint) == "" {
		writeErr(w, 400, "name and endpoint are required")
		return
	}
	token, tokenErr := randomToken(32)
	if tokenErr != nil {
		a.Logger.Error("failed to generate worker API token", "error", tokenErr)
		writeErr(w, 500, "failed to generate worker API token")
		return
	}
	id, err := a.Store.CreateHost(r.Context(), strings.TrimSpace(in.Name), strings.TrimSpace(in.Endpoint), token, db.Bool(in.Enabled))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h, _ := a.Store.Host(r.Context(), id)
	if h.Enabled {
		a.Events.Start(a.ctx, a.Docker, a.Store, h)
		go a.startHostWorker(h, "host-created")
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "host.create", id, "", in.Endpoint)
	writeJSON(w, 201, h)
}

func (a *App) handleHostQuickSetup(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	if a.SSH == nil {
		writeErr(w, 503, "SSH quick setup is unavailable in this build")
		return
	}
	var in quickSetupInput
	if json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	in.Name, in.IP, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.IP), strings.TrimSpace(in.Username)
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.Name == "" || in.Password == "" {
		writeErr(w, 400, "host name, IP, username and SSH password are required")
		return
	}
	if !in.AcknowledgeInsecureTCP {
		writeErr(w, 400, "confirm the Docker TCP 2375 security warning before running quick setup")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := a.SSH.ConfigureDockerTCP(ctx, in.IP, in.Username, in.Password, in.SSHPort)
	in.Password = ""
	if err != nil {
		a.Logger.Warn("SSH Docker quick setup failed", "actor", a.actor(r), "ip", in.IP, "username", in.Username, "error", err)
		writeErr(w, 502, err.Error())
		return
	}
	pingCtx, pingCancel := context.WithTimeout(r.Context(), 8*time.Second)
	version, pingErr := a.Docker.Ping(pingCtx, res.Endpoint)
	pingCancel()
	if pingErr != nil {
		a.Logger.Warn("SSH Docker quick setup configured endpoint but controller cannot reach it", "actor", a.actor(r), "endpoint", res.Endpoint, "error", pingErr)
		writeJSON(w, 502, map[string]any{"configured": true, "host_created": false, "endpoint": res.Endpoint, "error": "Docker was configured on the remote host, but Vibewatch cannot reach " + res.Endpoint + ": " + pingErr.Error()})
		return
	}
	token, tokenErr := randomToken(32)
	if tokenErr != nil {
		writeErr(w, 500, "failed to generate worker API token")
		return
	}
	id, createErr := a.Store.CreateHost(r.Context(), in.Name, res.Endpoint, token, db.Bool(true))
	if createErr != nil {
		writeErr(w, 400, "Docker endpoint is reachable, but the Vibewatch host could not be created: "+createErr.Error())
		return
	}
	h, _ := a.Store.Host(r.Context(), id)
	a.Events.Start(a.ctx, a.Docker, a.Store, h)
	go a.startHostWorker(h, "ssh-quick-setup")
	_ = a.Store.Audit(r.Context(), a.actor(r), "host.quick-setup", id, "", fmt.Sprintf("endpoint=%s ssh_user=%s", res.Endpoint, res.Username))
	a.Logger.Info("SSH Docker quick setup completed", "actor", a.actor(r), "host_id", id, "endpoint", res.Endpoint, "docker_version", version)
	writeJSON(w, 201, map[string]any{"configured": true, "host_created": true, "host": h, "endpoint": res.Endpoint, "docker_version": version})
}

func (a *App) systemManagedContainer(name string) (bool, string) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	controller := strings.TrimSpace(strings.TrimPrefix(a.Cfg.ControllerName, "/"))
	if name != "" && (name == controller || name == "watchtower-ui" || name == "vibewatch") {
		return true, "controller"
	}
	for _, prefix := range []string{"watchtower-ui-worker-", "vibewatch-worker-"} {
		if strings.HasPrefix(name, prefix) {
			return true, "worker"
		}
	}
	for _, updater := range []string{"watchtower-ui-self-updater", "vibewatch-self-updater", "vibewatch-runtime-migrate"} {
		if name == updater {
			return true, "maintenance"
		}
	}
	return false, ""
}

func (a *App) handleHostSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/hosts/"), "/"), "/")
	if len(parts) < 1 {
		writeErr(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid host id")
		return
	}
	if !a.hostAllowed(r, id) {
		writeErr(w, http.StatusForbidden, "host access denied")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPut {
		if !a.requireAdmin(w, r) {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
			writeErr(w, 400, "host name is required")
			return
		}
		if err := a.Store.RenameHost(r.Context(), id, strings.TrimSpace(in.Name)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "host.rename", id, "", strings.TrimSpace(in.Name))
		h, _ := a.Store.Host(r.Context(), id)
		writeJSON(w, 200, h)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if !a.requireAdmin(w, r) {
			return
		}
		a.Events.Stop(id)
		_ = a.Docker.RemoveWorker(context.Background(), id)
		if err := a.Store.DeleteHost(r.Context(), id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "host.delete", id, "", "")
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		h, e := a.Store.Host(r.Context(), id)
		if e != nil {
			writeErr(w, 404, "host not found")
			return
		}
		v, e := a.Docker.Ping(r.Context(), h.Endpoint)
		if e != nil {
			writeErr(w, 502, e.Error())
			return
		}
		base, workerErr := a.ensureWorker(r.Context(), h)
		if workerErr != nil {
			writeJSON(w, 200, map[string]any{"ok": true, "docker_version": v, "worker_ready": false, "worker": a.Docker.WorkerState(r.Context(), h.ID), "worker_error": workerErr.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "docker_version": v, "worker_ready": true, "worker_url": base, "worker": a.Docker.WorkerState(r.Context(), h.ID)})
		return
	}
	if len(parts) == 2 && parts[1] == "overview" && r.Method == http.MethodGet {
		a.handleHostOverview(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && parts[2] == "prune" && r.Method == http.MethodPost {
		a.handleImagePrune(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "networks" && r.Method == http.MethodGet {
		a.handleHostNetworks(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "networks" && parts[2] == "prune" && r.Method == http.MethodPost {
		a.handleNetworkPrune(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "build-cache" && parts[2] == "prune" && r.Method == http.MethodPost {
		a.handleBuildCachePrune(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "volumes" && r.Method == http.MethodGet {
		a.handleHostVolumes(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "volumes" && parts[2] == "prune-anonymous" && r.Method == http.MethodPost {
		a.handleAnonymousVolumePrune(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "volumes" && parts[2] == "named" && r.Method == http.MethodDelete {
		a.handleNamedVolumeDelete(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "containers" && r.Method == http.MethodGet {
		a.handleContainers(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "check" && r.Method == http.MethodPost {
		container := r.URL.Query().Get("container")
		if container == "" {
			res, e := a.policyScanHost(r.Context(), id, "manual-bulk", false)
			if e != nil {
				writeErr(w, 502, e.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"result": res})
			return
		}
		if managed, _ := a.systemManagedContainer(container); managed {
			writeErr(w, 409, "Vibewatch system containers are maintained from Owner Settings")
			return
		}
		p, _ := a.Store.Policy(r.Context(), id, container)
		if p.Mode == "ignore" {
			writeErr(w, 409, "container is excluded; change its policy before checking it")
			return
		}
		jobID, e := a.enqueueCheck(r.Context(), id, container, "manual", a.actor(r))
		if e != nil {
			writeErr(w, 409, e.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "status": "queued"})
		return
	}
	if len(parts) == 2 && parts[1] == "update" && r.Method == http.MethodPost {
		container := r.URL.Query().Get("container")
		if container == "" {
			writeErr(w, 400, "container is required")
			return
		}
		if managed, _ := a.systemManagedContainer(container); managed {
			writeErr(w, 409, "Vibewatch system containers are maintained from Owner Settings")
			return
		}
		jobID, e := a.enqueueUpdate(r.Context(), id, container, "manual", a.actor(r))
		if e != nil {
			writeErr(w, 409, e.Error())
			return
		}
		writeJSON(w, 202, map[string]any{"job_id": jobID, "status": "queued"})
		return
	}
	if len(parts) == 2 && parts[1] == "snooze" && r.Method == http.MethodPut {
		container := strings.TrimSpace(r.URL.Query().Get("container"))
		if container == "" {
			writeErr(w, 400, "container is required")
			return
		}
		if managed, _ := a.systemManagedContainer(container); managed {
			writeErr(w, 409, "Vibewatch system containers cannot be snoozed")
			return
		}
		var in struct {
			Snoozed bool `json:"snoozed"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		cache, err := a.Store.Cache(r.Context(), id, container)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if in.Snoozed {
			if strings.TrimSpace(cache.LatestDigest) == "" || strings.TrimSpace(cache.CurrentDigest) == "" || digestEqual(cache.CurrentDigest, cache.LatestDigest) {
				writeErr(w, 409, "no concrete pending image digest is available to snooze")
				return
			}
			cache.SnoozedDigest = cache.LatestDigest
			cache.SnoozedAt = time.Now().UTC().Format(time.RFC3339)
			cache.UpdateAvailable = false
			if strings.TrimSpace(cache.FirstDetectedAt) == "" {
				cache.FirstDetectedAt = cache.SnoozedAt
			}
		} else {
			cache.SnoozedDigest = ""
			cache.SnoozedAt = ""
			cache.UpdateAvailable = db.Bool(!digestEqual(cache.CurrentDigest, cache.LatestDigest) && strings.TrimSpace(cache.LatestDigest) != "")
			if bool(cache.UpdateAvailable) && strings.TrimSpace(cache.FirstDetectedAt) == "" {
				cache.FirstDetectedAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
		if err := a.Store.SaveCache(r.Context(), cache); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		action := "update.unsnooze"
		details := "current digest snooze removed"
		if in.Snoozed {
			action = "update.snooze"
			details = "digest=" + cache.SnoozedDigest
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), action, id, container, details)
		writeJSON(w, 200, cache)
		return
	}
	if len(parts) >= 3 && parts[1] == "policies" && r.Method == http.MethodPut {
		container, err := urlPathJoin(parts[2:])
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if managed, _ := a.systemManagedContainer(container); managed {
			writeErr(w, 409, "Vibewatch system container policies are managed internally")
			return
		}
		var in policyInput
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		if in.Mode != "manual" && in.Mode != "auto" && in.Mode != "ignore" {
			writeErr(w, 400, "mode must be manual, auto or ignore")
			return
		}
		p := db.Policy{HostID: id, ContainerName: container, Mode: in.Mode, CheckIntervalMinutes: in.CheckIntervalMinutes, ReleaseRepo: in.ReleaseRepo}
		if err := a.Store.SavePolicy(r.Context(), p); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "policy.update", id, container, in.Mode)
		writeJSON(w, 200, p)
		return
	}
	if len(parts) >= 3 && parts[1] == "release-notes" && r.Method == http.MethodGet {
		container, err := urlPathJoin(parts[2:])
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		a.handleReleaseNotes(w, r, id, container)
		return
	}
	writeErr(w, 404, "not found")
}

func urlPathJoin(p []string) (string, error) {
	if len(p) == 0 {
		return "", errors.New("missing container")
	}
	return strings.Join(p, "/"), nil
}

func (a *App) handleHostOverview(w http.ResponseWriter, r *http.Request, hostID int64) {
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	includeImages := r.URL.Query().Get("include_images") == "1" || strings.EqualFold(r.URL.Query().Get("include_images"), "true")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	overview, err := a.Docker.HostOverview(ctx, h.Endpoint, includeImages)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	protectedImages, _, _ := a.rollbackProtectedDockerObjects(hostID)
	if includeImages {
		for i := range overview.Images {
			if protectedImages[overview.Images[i].ID] {
				overview.Images[i].RollbackProtected = true
				if overview.Images[i].Unused {
					overview.ImagesRollbackProtected++
				}
			}
			if overview.Images[i].Unused && !overview.Images[i].RollbackProtected {
				overview.ImagesCleanupEligible++
			}
		}
	} else {
		// The summary can still expose how many currently local unused images are
		// protected without returning the full inventory.
		if xs, e := a.Docker.ImageInventory(ctx, h.Endpoint); e == nil {
			for _, img := range xs {
				if img.Unused && protectedImages[img.ID] {
					overview.ImagesRollbackProtected++
				}
				if img.Unused && !protectedImages[img.ID] {
					overview.ImagesCleanupEligible++
				}
			}
		}
	}
	writeJSON(w, 200, overview)
}

func (a *App) handleImagePrune(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	jobID, err := a.Store.CreateJob(r.Context(), "image-cleanup", "manual", hostID, "unused-images", "queued")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.StartJob(r.Context(), jobID)
	_ = a.Store.AddJobLog(r.Context(), jobID, "info", "docker", "Pruning images not referenced by any container")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	protected, _, _ := a.rollbackProtectedDockerObjects(hostID)
	result, err := a.Docker.PruneUnusedImages(ctx, h.Endpoint, protected)
	if err != nil {
		_ = a.Store.AddJobLog(context.Background(), jobID, "error", "docker", err.Error())
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		a.Logger.Error("unused image cleanup failed", "host", h.Name, "host_id", hostID, "job_id", jobID, "error", err)
		writeErr(w, 502, err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	_ = a.Store.AddJobLog(context.Background(), jobID, "info", "docker", fmt.Sprintf("Removed %d unused images; %d rollback-protected; %d failed; reclaimed %d bytes", result.RemovedImages, result.ProtectedImages, result.FailedImages, result.ReclaimedBytes))
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), a.actor(r), "images.prune", hostID, "", string(summary))
	a.Logger.Info("unused image cleanup completed", "host", h.Name, "host_id", hostID, "job_id", jobID, "removed_images", result.RemovedImages, "reclaimed_bytes", result.ReclaimedBytes)
	writeJSON(w, 200, map[string]any{"job_id": jobID, "result": result})
}

func (a *App) handleHostNetworks(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	xs, err := a.Docker.NetworkInventory(ctx, h.Endpoint)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	_, protected, _ := a.rollbackProtectedDockerObjects(hostID)
	for i := range xs {
		if protected[xs[i].Name] {
			xs[i].RollbackProtected = true
			xs[i].Unused = false
		}
	}
	writeJSON(w, 200, xs)
}

func (a *App) handleNetworkPrune(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	jobID, err := a.Store.CreateJob(r.Context(), "network-cleanup", "manual", hostID, "unused-networks", "queued")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.StartJob(r.Context(), jobID)
	_, protected, _ := a.rollbackProtectedDockerObjects(hostID)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	result, err := a.Docker.PruneUnusedNetworks(ctx, h.Endpoint, protected)
	if err != nil {
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		writeErr(w, 502, err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), a.actor(r), "networks.prune", hostID, "", string(summary))
	writeJSON(w, 200, map[string]any{"job_id": jobID, "result": result})
}

func (a *App) handleBuildCachePrune(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	jobID, err := a.Store.CreateJob(r.Context(), "build-cache-cleanup", "manual", hostID, "build-cache", "queued")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.StartJob(r.Context(), jobID)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, err := a.Docker.PruneBuildCache(ctx, h.Endpoint)
	if err != nil {
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		writeErr(w, 502, err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), a.actor(r), "build-cache.prune", hostID, "", string(summary))
	writeJSON(w, 200, map[string]any{"job_id": jobID, "result": result})
}

func imageStateStale(lastChecked, lastError string) bool {
	if strings.TrimSpace(lastChecked) == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, lastChecked)
	if err != nil {
		return true
	}
	maxAge := 30 * time.Minute
	if strings.TrimSpace(lastError) != "" {
		maxAge = 10 * time.Minute
	}
	return time.Since(t) > maxAge
}

func (a *App) beginInfoRefresh(hostID int64, container string) bool {
	key := fmt.Sprintf("%d:%s", hostID, container)
	a.infoRefreshMu.Lock()
	defer a.infoRefreshMu.Unlock()
	if a.infoRefresh == nil {
		a.infoRefresh = map[string]bool{}
	}
	if a.infoRefresh[key] {
		return false
	}
	a.infoRefresh[key] = true
	return true
}

func (a *App) endInfoRefresh(hostID int64, container string) {
	key := fmt.Sprintf("%d:%s", hostID, container)
	a.infoRefreshMu.Lock()
	delete(a.infoRefresh, key)
	a.infoRefreshMu.Unlock()
}

// readOnlyRegistryCheck compares the local image-config digest with the config
// digest of the matching remote platform manifest. It deliberately never asks
// Watchtower to pull, stop, recreate or update a container. This is the
// informational path used by Excluded policies.
func (a *App) readOnlyRegistryCheck(ctx context.Context, hostID int64, c dockercli.Container, trigger string) (watchtower.CheckResponse, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	old, _ := a.Store.Cache(ctx, hostID, c.Name)
	cache := old
	cache.HostID = hostID
	cache.ContainerName = c.Name
	cache.Image = c.Image
	cache.ImageID = c.ImageID
	cache.LastCheckedAt = now
	cache.LastError = ""

	item := watchtower.CheckItem{Name: c.Name, Image: c.Image, ImageID: c.ImageID, Timestamp: now}
	var errs []string
	var h db.Host
	var platform registry.Platform

	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		errs = append(errs, "host: "+err.Error())
	} else {
		inspectRef := c.ImageID
		if strings.TrimSpace(inspectRef) == "" {
			inspectRef = c.Image
		}
		local, platformErr := a.Docker.ImagePlatform(ctx, h.Endpoint, inspectRef)
		if platformErr != nil {
			errs = append(errs, "local image platform: "+platformErr.Error())
		} else {
			platform = registry.Platform{OS: local.OS, Architecture: local.Architecture, Variant: local.Variant}
			cache.CurrentDigest = strings.TrimSpace(local.ImageID)
			item.Digest = cache.CurrentDigest
		}
	}

	if a.Registry == nil {
		errs = append(errs, "registry client unavailable")
	} else if strings.TrimSpace(platform.Architecture) != "" {
		remote, remoteErr := a.Registry.RemoteStateForPlatform(ctx, c.Image, platform)
		if remoteErr != nil {
			errs = append(errs, "registry image state: "+remoteErr.Error())
		} else {
			// Config digests are platform-specific and directly comparable to the
			// local Docker image ID. This avoids false positives when only another
			// architecture in a multi-platform tag changes.
			cache.LatestDigest = strings.TrimSpace(remote.ConfigDigest)
			item.LatestDigest = cache.LatestDigest
		}
	}

	if len(errs) == 0 && cache.CurrentDigest != "" && cache.LatestDigest != "" {
		available := !strings.EqualFold(strings.TrimSpace(cache.CurrentDigest), strings.TrimSpace(cache.LatestDigest))
		cache = applyTrackedUpdateState(old, cache, available, now)
		item.UpdateAvailable = bool(cache.UpdateAvailable)
	} else {
		cache.LastError = strings.Join(errs, "; ")
		item.Error = cache.LastError
		item.UpdateAvailable = bool(cache.UpdateAvailable)
	}
	_ = a.Store.SaveCache(ctx, cache)
	_ = a.Store.TouchPolicy(ctx, hostID, c.Name)

	// Refresh readable metadata separately. Digest state remains authoritative
	// even when neither image publishes a human-readable version label. Use the
	// target image platform so ARM and x86 hosts do not borrow each other's OCI
	// version metadata from a multi-platform tag.
	if h.ID != 0 {
		labelRef := c.ImageID
		if strings.TrimSpace(labelRef) == "" {
			labelRef = c.Image
		}
		if labels, labelErr := a.Docker.ImageLabels(ctx, h.Endpoint, labelRef); labelErr == nil {
			installed, source := dockercli.InstalledVersion(c.Image, labels)
			p, _ := a.Store.Policy(ctx, hostID, c.Name)
			repo := strings.TrimSpace(p.ReleaseRepo)
			if repo == "" {
				repo, _ = releases.DetectFromLabels(labels)
			}
			if repo == "" {
				repo, _ = releases.FallbackFromImage(c.Image)
			}
			go a.refreshVersion(context.Background(), hostID, c.Name, c.Image, repo, installed, source, platform)
		}
	}

	res := watchtower.CheckResponse{Containers: []watchtower.CheckItem{item}, Count: 1, Timestamp: now}
	if cache.LastError != "" {
		return res, errors.New(cache.LastError)
	}
	return res, nil
}

func (a *App) refreshExcludedImageState(hostID int64, c dockercli.Container) {
	if !a.beginInfoRefresh(hostID, c.Name) {
		return
	}
	defer a.endInfoRefresh(hostID, c.Name)
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if _, err := a.readOnlyRegistryCheck(ctx, hostID, c, "excluded-info"); err != nil {
		a.Logger.Debug("excluded read-only registry check unavailable", "host_id", hostID, "container", c.Name, "error", err)
	}
}

func (a *App) handleContainers(w http.ResponseWriter, r *http.Request, hostID int64) {
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	cs, err := a.Docker.ListContainers(r.Context(), h.Endpoint)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	latestRestorePoints, _ := a.Store.LatestRestorePointsForHost(r.Context(), hostID)
	views := make([]ContainerView, 0, len(cs))
	for _, c := range cs {
		p, _ := a.Store.Policy(r.Context(), hostID, c.Name)
		cache, _ := a.Store.Cache(r.Context(), hostID, c.Name)
		v, _ := a.Store.Version(r.Context(), hostID, c.Name)
		managed, role := a.systemManagedContainer(c.Name)
		labelRef := c.ImageID
		if labelRef == "" {
			labelRef = c.Image
		}
		labels, e := a.Docker.ImageLabels(r.Context(), h.Endpoint, labelRef)
		if e == nil {
			installed, source := dockercli.InstalledVersion(c.Image, labels)
			if installed != "" {
				v.Installed = installed
				v.InstalledSource = source
			}
			repo := strings.TrimSpace(p.ReleaseRepo)
			if repo == "" {
				repo, _ = releases.DetectFromLabels(labels)
			}
			if repo == "" {
				repo, _ = releases.FallbackFromImage(c.Image)
			}
			if repo != "" {
				v.ReleaseRepo = repo
			}
			_ = a.Store.SaveVersion(r.Context(), v)
			if versionStale(v.CheckedAt) {
				platform := registry.Platform{}
				if local, pe := a.Docker.ImagePlatform(r.Context(), h.Endpoint, labelRef); pe == nil {
					platform = registry.Platform{OS: local.OS, Architecture: local.Architecture, Variant: local.Variant}
				}
				go a.refreshVersion(context.Background(), hostID, c.Name, c.Image, repo, v.Installed, v.InstalledSource, platform)
			}
		}
		if !managed && p.Mode == "ignore" && imageStateStale(cache.LastCheckedAt, cache.LastError) {
			go a.refreshExcludedImageState(hostID, c)
		}
		driftDB, _ := a.Store.ConfigDrift(r.Context(), hostID, c.Name)
		drift := driftViewFromDB(driftDB)
		if !managed && driftStale(drift.CheckedAt) {
			go func(cc dockercli.Container) {
				ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
				defer cancel()
				a.refreshContainerDrift(ctx, hostID, cc)
			}(c)
		}
		var restorePoint *db.RestorePoint
		if rp, ok := latestRestorePoints[c.Name]; ok {
			copy := rp
			restorePoint = &copy
		}
		views = append(views, ContainerView{Container: c, SystemManaged: managed, SystemRole: role, Policy: p, Cache: cache, Version: v, ConfigDrift: drift, RestorePoint: restorePoint})
	}
	writeJSON(w, 200, views)
}

func versionStale(v string) bool {
	if v == "" {
		return true
	}
	t, e := time.Parse(time.RFC3339, v)
	return e != nil || time.Since(t) > time.Hour
}
func (a *App) refreshVersion(ctx context.Context, hostID int64, container, image, repo, installed, installedSource string, platforms ...registry.Platform) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	v, _ := a.Store.Version(ctx, hostID, container)
	if installed != "" {
		v.Installed = installed
		v.InstalledSource = installedSource
	}
	v.ReleaseRepo = repo
	v.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	v.Error = ""
	// The registry image that Watchtower actually follows is authoritative for
	// the readable target version when it publishes OCI version metadata.
	if a.Registry != nil && strings.TrimSpace(image) != "" {
		var rv registry.Version
		var err error
		if len(platforms) > 0 && strings.TrimSpace(platforms[0].Architecture) != "" {
			rv, err = a.Registry.RemoteVersionForPlatform(ctx, image, platforms[0])
		} else {
			rv, err = a.Registry.RemoteVersion(ctx, image)
		}
		if err == nil && rv.Version != "" {
			v.Latest = strings.TrimPrefix(strings.TrimSpace(rv.Version), "v")
			v.LatestSource = rv.Source
			_ = a.Store.SaveVersion(context.Background(), v)
			return
		} else if err != nil {
			v.Error = err.Error()
		}
	}
	// GitHub release metadata remains a useful fallback and supplies patch notes.
	if strings.TrimSpace(repo) != "" {
		rel, err := a.Releases.Latest(ctx, repo)
		if err == nil {
			v.Latest = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
			v.LatestSource = "github-release"
			v.PublishedAt = rel.PublishedAt
			v.Error = ""
		} else if v.Error == "" {
			v.Error = err.Error()
		}
	}
	_ = a.Store.SaveVersion(context.Background(), v)
}

func (a *App) ensureWorker(ctx context.Context, h db.Host) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	base, err := a.Docker.EnsureWorker(ctx, h)
	if err != nil {
		return "", err
	}
	if err := a.WT.WaitReady(ctx, base); err != nil {
		state := a.Docker.WorkerState(context.Background(), h.ID)
		logs, _ := a.Docker.WorkerLogsRecent(context.Background(), h.ID, 80)
		if logs != "" {
			return "", fmt.Errorf("%w (worker status=%s exit_code=%d restarting=%t): %s", err, state.Status, state.ExitCode, state.Restarting, logs)
		}
		return "", fmt.Errorf("%w (worker status=%s exit_code=%d restarting=%t)", err, state.Status, state.ExitCode, state.Restarting)
	}
	return base, nil
}

func (a *App) jobProgress(ctx context.Context, jobID int64, percent int, stage string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	_ = a.Store.AddJobLog(ctx, jobID, "INFO", "progress", fmt.Sprintf("%d|%s", percent, strings.TrimSpace(stage)))
}

func (a *App) check(ctx context.Context, hostID int64, container, trigger string) (watchtower.CheckResponse, int64, error) {
	jobID, err := a.Store.CreateJob(ctx, "check", trigger, hostID, container, "running")
	if err != nil {
		return watchtower.CheckResponse{}, 0, err
	}
	_ = a.Store.StartJob(ctx, jobID)
	a.jobProgress(ctx, jobID, 10, "Starting update check")
	res, err := a.runCheck(ctx, jobID, hostID, container, trigger)
	return res, jobID, err
}

func (a *App) enqueueCheck(ctx context.Context, hostID int64, container, trigger, actor string) (int64, error) {
	active, err := a.Store.HasActiveJob(ctx, hostID, container)
	if err != nil {
		return 0, err
	}
	if active {
		return 0, fmt.Errorf("a check or update job is already queued or running for %s", container)
	}
	id, err := a.Store.CreateJob(ctx, "check", trigger, hostID, container, "queued")
	if err != nil {
		return 0, err
	}
	a.jobProgress(ctx, id, 5, "Queued")
	if actor == "" {
		actor = "system"
	}
	_ = a.Store.Audit(ctx, actor, "check.queue", hostID, container, trigger)
	go func() {
		ctx := a.ctx
		_ = a.Store.StartJob(ctx, id)
		a.jobProgress(ctx, id, 10, "Starting update check")
		_, _ = a.runCheck(ctx, id, hostID, container, trigger)
	}()
	return id, nil
}

func (a *App) markCheckUnavailable(ctx context.Context, hostID int64, container string, err error) {
	if strings.TrimSpace(container) == "" || err == nil {
		return
	}
	cache, _ := a.Store.Cache(ctx, hostID, container)
	cache.HostID = hostID
	cache.ContainerName = container
	cache.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	cache.LastError = err.Error()
	_ = a.Store.SaveCache(ctx, cache)
}

func (a *App) runCheck(ctx context.Context, jobID, hostID int64, container, trigger string) (watchtower.CheckResponse, error) {
	a.workerOpMu.RLock()
	defer a.workerOpMu.RUnlock()
	started := time.Now()
	_ = a.Store.AddJobLog(ctx, jobID, "INFO", "app", "update check started")
	a.jobProgress(ctx, jobID, 20, "Preparing update worker")
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		a.jobProgress(ctx, jobID, 100, "Check failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		return watchtower.CheckResponse{}, err
	}
	base, err := a.ensureWorker(ctx, h)
	if err != nil {
		a.markCheckUnavailable(ctx, hostID, container, err)
		_ = a.Store.AddJobLog(ctx, jobID, "ERROR", "app", err.Error())
		a.jobProgress(ctx, jobID, 100, "Check failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		return watchtower.CheckResponse{}, err
	}
	a.jobProgress(ctx, jobID, 45, "Checking registry and image digest")
	res, raw, err := a.WT.Check(ctx, base, h.WorkerToken, container)
	if logs, e := a.Docker.WorkerLogs(ctx, hostID, started); e == nil && logs != "" {
		_ = a.Store.AddJobLog(ctx, jobID, "DEBUG", "watchtower", logs)
	}
	if err != nil {
		a.markCheckUnavailable(ctx, hostID, container, err)
		_ = a.Store.AddJobLog(ctx, jobID, "ERROR", "watchtower", err.Error())
		a.jobProgress(ctx, jobID, 100, "Check failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", string(raw), err.Error())
		return res, err
	}
	a.jobProgress(ctx, jobID, 75, "Processing check result")
	for i := range res.Containers {
		item := &res.Containers[i]
		now := time.Now().UTC().Format(time.RFC3339)
		old, _ := a.Store.Cache(ctx, hostID, item.Name)
		c := db.Cache{HostID: hostID, ContainerName: item.Name, Image: item.Image, ImageID: item.ImageID, CurrentDigest: item.Digest, LatestDigest: item.LatestDigest, UpdateAvailable: db.Bool(item.UpdateAvailable), LastCheckedAt: now, LastError: item.Error}
		if strings.TrimSpace(item.Error) == "" && strings.TrimSpace(c.CurrentDigest) != "" && strings.TrimSpace(c.LatestDigest) != "" {
			c = applyTrackedUpdateState(old, c, item.UpdateAvailable, now)
		} else {
			// A transient/per-container check error must not silently forget when a
			// previously known update was first seen or remove its digest snooze.
			c.FirstDetectedAt = old.FirstDetectedAt
			c.SnoozedDigest = old.SnoozedDigest
			c.SnoozedAt = old.SnoozedAt
			if cacheHasSnoozedUpdate(old) {
				c.UpdateAvailable = false
			}
		}
		item.UpdateAvailable = bool(c.UpdateAvailable)
		_ = a.Store.SaveCache(ctx, c)
		_ = a.Store.TouchPolicy(ctx, hostID, item.Name)
		if p, e := a.Store.Policy(ctx, hostID, item.Name); e == nil {
			if p.Mode == "manual" && item.UpdateAvailable {
				fingerprint := strings.TrimSpace(item.LatestDigest)
				if fingerprint == "" {
					fingerprint = strings.TrimSpace(item.ImageID) + ":update"
				}
				if hname, e3 := a.Store.Host(ctx, hostID); e3 == nil {
					go a.notifyHostUsers(hostID, "manual", item.Name, "Update available · "+item.Name, hname.Name+" · "+item.Name+" has a new container image available.", fingerprint)
				}
			}
			if h2, e2 := a.Store.Host(ctx, hostID); e2 == nil {
				imageRef := item.ImageID
				if imageRef == "" {
					imageRef = item.Image
				}
				if labels, e3 := a.Docker.ImageLabels(ctx, h2.Endpoint, imageRef); e3 == nil {
					installed, source := dockercli.InstalledVersion(item.Image, labels)
					repo := strings.TrimSpace(p.ReleaseRepo)
					if repo == "" {
						repo, _ = releases.DetectFromLabels(labels)
					}
					if repo == "" {
						repo, _ = releases.FallbackFromImage(item.Image)
					}
					platform := registry.Platform{}
					if local, pe := a.Docker.ImagePlatform(ctx, h2.Endpoint, imageRef); pe == nil {
						platform = registry.Platform{OS: local.OS, Architecture: local.Architecture, Variant: local.Variant}
					}
					go a.refreshVersion(context.Background(), hostID, item.Name, item.Image, repo, installed, source, platform)
				}
			}
		}
	}
	a.jobProgress(ctx, jobID, 92, "Saving update state")
	_ = a.Store.AddJobLog(ctx, jobID, "INFO", "app", fmt.Sprintf("check completed: %d container(s)", res.Count))
	a.jobProgress(ctx, jobID, 100, "Check completed")
	_ = a.Store.FinishJob(ctx, jobID, "success", string(raw), "")
	return res, nil
}

func (a *App) enqueueUpdate(ctx context.Context, hostID int64, container, trigger, actor string) (int64, error) {
	if managed, _ := a.systemManagedContainer(container); managed {
		return 0, fmt.Errorf("Vibewatch system containers are maintained from Owner Settings")
	}
	active, err := a.Store.HasActiveJob(ctx, hostID, container)
	if err != nil {
		return 0, err
	}
	if active {
		return 0, fmt.Errorf("an update job is already queued or running for %s", container)
	}
	p, _ := a.Store.Policy(ctx, hostID, container)
	if p.Mode == "ignore" {
		return 0, fmt.Errorf("container is excluded; change its policy before updating it")
	}
	if cache, e := a.Store.Cache(ctx, hostID, container); e == nil && cacheHasSnoozedUpdate(cache) {
		return 0, fmt.Errorf("the currently detected image update is snoozed; wait for the next digest or remove the snooze")
	}
	id, err := a.Store.CreateJob(ctx, "update", trigger, hostID, container, "queued")
	if err != nil {
		return 0, err
	}
	_ = a.Store.AddJobLog(ctx, id, "INFO", "app", "update queued")
	a.jobProgress(ctx, id, 5, "Queued")
	if actor == "" {
		actor = "system"
	}
	_ = a.Store.Audit(ctx, actor, "update.queue", hostID, container, trigger)
	select {
	case a.Queue <- updateRequest{JobID: id, HostID: hostID, Container: container, Trigger: trigger, Actor: actor}:
		return id, nil
	default:
		_ = a.Store.FinishJob(ctx, id, "failed", "", "update queue full")
		return 0, fmt.Errorf("update queue full")
	}
}

func (a *App) updateWorker() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case req := <-a.Queue:
			a.executeUpdate(req)
		}
	}
}
func (a *App) executeUpdate(req updateRequest) {
	ctx := a.ctx
	_ = a.Store.StartJob(ctx, req.JobID)
	a.jobProgress(ctx, req.JobID, 10, "Starting update")
	started := time.Now()
	beforeContainer, beforeVersion := a.currentContainerState(ctx, req.HostID, req.Container)
	beforeCache, _ := a.Store.Cache(ctx, req.HostID, req.Container)
	historySnapshotID := ""
	restorePointID := int64(0)
	attemptedDigest := strings.TrimSpace(beforeCache.LatestDigest)
	dependencyCount := 0
	dependencyStatus := "none"
	dependencyDetails := ""
	var dependencyCtx []networkNamespaceDependencyRuntime
	var targetBeforeInspect inspectContainer
	defer func() {
		a.recordUpdateHistory(req, beforeContainer, beforeVersion, historySnapshotID, restorePointID, attemptedDigest, started, dependencyCount, dependencyStatus, dependencyDetails)
	}()
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "app", "update started")

	h, err := a.Store.Host(ctx, req.HostID)
	if err != nil {
		a.failJob(req.JobID, err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	notifyAutomatic := func(status string, resultErr error) {
		if !strings.HasPrefix(req.Trigger, "automation:") {
			return
		}
		title := "Automatic update completed · " + req.Container
		msg := h.Name + " · " + req.Container + " was updated successfully."
		if status != "success" {
			title = "Automatic update failed · " + req.Container
			msg = h.Name + " · " + req.Container + " update failed."
			if resultErr != nil && strings.TrimSpace(resultErr.Error()) != "" {
				msg += " " + resultErr.Error()
			}
		}
		go a.notifyHostUsers(req.HostID, "auto", req.Container, title, msg, "")
	}

	a.jobProgress(ctx, req.JobID, 14, "Resolving container dependencies")
	targetBeforeInspect, dependencyCtx, err = a.discoverNetworkNamespaceDependents(ctx, req.HostID, req.Container)
	if err != nil {
		err = fmt.Errorf("network namespace dependency scan failed: %w", err)
		a.failJob(req.JobID, err)
		_ = a.Store.Audit(ctx, "system", "update.blocked-dependency-scan", req.HostID, req.Container, err.Error())
		notifyAutomatic("failed", err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	dependencyCount = len(dependencyCtx)
	if dependencyCount > 0 {
		dependencyStatus = "detected"
		dependencyDetails = dependencyNames(dependencyCtx)
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "dependency", fmt.Sprintf("Detected %d network namespace dependent(s): %s", dependencyCount, dependencyDetails))
		for _, dep := range dependencyCtx {
			detail := fmt.Sprintf("type=%s parent=%s dependent=%s parent_id=%s dependent_id=%s was_running=%t", networkNamespaceDependencyType, req.Container, dep.SourceContainer, targetBeforeInspect.ID, dep.SourceContainerID, dep.WasRunning)
			_ = a.Store.Audit(ctx, "system", "dependency.detected", req.HostID, dep.SourceContainer, detail)
		}
	}
	finishFailed := func(raw []byte, failure error, allowAutoRollback bool, rp db.RestorePoint) {
		if dependencyCount > 0 && (dependencyStatus == "detected" || dependencyStatus == "prepared") {
			dependencyStatus = "not_run"
		}
		finalErr := failure
		if allowAutoRollback && rp.ID > 0 {
			attempted, rollbackErr := a.runAutomaticRollback(req, rp, failure)
			if attempted {
				if rollbackErr == nil {
					finalErr = fmt.Errorf("%v; automatic rollback completed from restore point #%d", failure, rp.ID)
					_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "rollback", fmt.Sprintf("Automatic rollback completed from restore point #%d", rp.ID))
				} else {
					finalErr = fmt.Errorf("%v; automatic rollback failed: %v", failure, rollbackErr)
					_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "rollback", "Automatic rollback failed: "+rollbackErr.Error())
				}
			}
		}
		a.jobProgress(ctx, req.JobID, 100, "Update failed")
		_ = a.Store.FinishJob(ctx, req.JobID, "failed", string(raw), finalErr.Error())
		_ = a.Store.Audit(ctx, "system", "update.failed", req.HostID, req.Container, finalErr.Error())
		notifyAutomatic("failed", finalErr)
		a.notifyManualUpdateResult(req, "failed", finalErr)
	}

	a.jobProgress(ctx, req.JobID, 20, "Creating recovery snapshot")
	snapshotReason := "before-update"
	if req.Trigger == "manual" {
		snapshotReason = "before-manual-update"
	} else if strings.HasPrefix(req.Trigger, "automation:") {
		snapshotReason = "before-automatic-update"
	}
	snapshotCtx, snapshotCancel := context.WithTimeout(ctx, 60*time.Second)
	snap, snapshotErr := a.createSnapshotForContainer(snapshotCtx, req.HostID, req.Container, snapshotReason)
	snapshotCancel()
	if snapshotErr != nil {
		err := fmt.Errorf("pre-update recovery snapshot failed: %w", snapshotErr)
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "backup", err.Error()+"; update blocked")
		a.jobProgress(ctx, req.JobID, 100, "Update blocked: backup failed")
		_ = a.Store.FinishJob(ctx, req.JobID, "failed", "", err.Error())
		_ = a.Store.Audit(ctx, "system", "update.blocked-backup", req.HostID, req.Container, err.Error())
		a.Logger.Error("pre-update container recovery snapshot failed; update blocked", "host_id", req.HostID, "container", req.Container, "job_id", req.JobID, "error", snapshotErr)
		notifyAutomatic("failed", err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	historySnapshotID = snap.ID
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "backup", "Recovery snapshot created: "+snap.Filename)
	if dependencyCount > 0 {
		a.jobProgress(ctx, req.JobID, 27, "Protecting dependent container configurations")
		dependencyCtx, err = a.attachDependencySnapshots(ctx, req.HostID, snap, dependencyCtx)
		if err != nil {
			dependencyStatus = "snapshot_failed"
			err = fmt.Errorf("pre-update dependency recovery snapshot failed: %w", err)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "dependency", err.Error()+"; update blocked")
			a.jobProgress(ctx, req.JobID, 100, "Update blocked: dependency backup failed")
			_ = a.Store.FinishJob(ctx, req.JobID, "failed", "", err.Error())
			_ = a.Store.Audit(ctx, "system", "update.blocked-dependency-backup", req.HostID, req.Container, err.Error())
			notifyAutomatic("failed", err)
			a.notifyManualUpdateResult(req, "failed", err)
			return
		}
		dependencyStatus = "prepared"
	}

	a.jobProgress(ctx, req.JobID, 31, "Capturing full container restore point")
	restoreCtx, restoreCancel := context.WithTimeout(ctx, 10*time.Minute)
	rp, restoreErr := a.createRestorePointForSnapshot(restoreCtx, req.HostID, req.Container, snap, snapshotReason, req.Trigger, dependencyCtx)
	restoreCancel()
	restorePointID = rp.ID
	if restoreErr != nil {
		err := fmt.Errorf("pre-update full restore point failed: %w", restoreErr)
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "rollback", err.Error()+"; update blocked")
		a.jobProgress(ctx, req.JobID, 100, "Update blocked: restore point failed")
		_ = a.Store.FinishJob(ctx, req.JobID, "failed", "", err.Error())
		_ = a.Store.Audit(ctx, "system", "update.blocked-restore-point", req.HostID, req.Container, err.Error())
		notifyAutomatic("failed", err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	if rp.Status == "config_only" {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "rollback", "Docker Swarm target: configuration recovery snapshot retained, but writable-layer one-click rollback is not supported")
	} else {
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "rollback", fmt.Sprintf("Full restore point #%d ready · writable layer captured as %s", rp.ID, rp.ImageRef))
	}

	a.jobProgress(ctx, req.JobID, 40, "Preparing update worker")
	// Only the actual Watchtower operation needs to hold the worker-operation
	// read lock. Keeping it across snapshot/verification/rollback would force
	// nested read-lock acquisition during post-update checks and could deadlock
	// when worker maintenance is waiting for the write lock.
	a.workerOpMu.RLock()
	base, err := a.ensureWorker(ctx, h)
	if err != nil {
		a.workerOpMu.RUnlock()
		finishFailed(nil, err, false, rp)
		return
	}
	a.jobProgress(ctx, req.JobID, 50, "Update engine working")
	res, raw, err := a.WT.Update(ctx, base, h.WorkerToken, req.Container)
	if logs, e := a.Docker.WorkerLogs(ctx, req.HostID, started); e == nil && logs != "" {
		_ = a.Store.AddJobLog(ctx, req.JobID, "DEBUG", "watchtower", logs)
	}
	a.workerOpMu.RUnlock()
	if err != nil {
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "watchtower", err.Error())
		finishFailed(raw, err, true, rp)
		return
	}

	a.jobProgress(ctx, req.JobID, 78, "Update engine completed")
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "app", fmt.Sprintf("update completed: updated=%d failed=%d skipped=%d", res.Summary.Updated, res.Summary.Failed, res.Summary.Skipped))
	if res.Summary.Failed > 0 {
		failure := errors.New(strings.TrimSpace(res.Error))
		if strings.TrimSpace(res.Error) == "" {
			failure = fmt.Errorf("update engine reported %d failed container operation(s)", res.Summary.Failed)
		}
		finishFailed(raw, failure, true, rp)
		return
	}

	a.jobProgress(ctx, req.JobID, 86, "Verifying updated container")
	if verifyErr := a.verifyUpdatedContainer(ctx, req.HostID, req.Container); verifyErr != nil {
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "verify", verifyErr.Error())
		finishFailed(raw, fmt.Errorf("post-update verification failed: %w", verifyErr), true, rp)
		return
	}

	if dependencyCount > 0 {
		parentAfter, inspectErr := a.inspectOne(ctx, req.HostID, req.Container)
		if inspectErr != nil {
			dependencyStatus = "failed"
			finishFailed(raw, fmt.Errorf("post-update parent inspect for dependency recreation failed: %w", inspectErr), true, rp)
			return
		}
		if strings.TrimSpace(parentAfter.ID) != "" && strings.TrimSpace(parentAfter.ID) != strings.TrimSpace(targetBeforeInspect.ID) {
			dependencyStatus = "recreating"
			if depErr := a.recreateNetworkNamespaceDependents(ctx, req.JobID, req.HostID, req.Container, parentAfter.ID, dependencyCtx); depErr != nil {
				dependencyStatus = "failed"
				finishFailed(raw, depErr, true, rp)
				return
			}
			dependencyStatus = "success"
		} else {
			dependencyStatus = "not_required"
			_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "dependency", "Parent container ID did not change; dependent recreation was not required")
		}
	}

	a.jobProgress(ctx, req.JobID, 95, "Refreshing update state")
	_, _, _ = a.check(ctx, req.HostID, req.Container, "post-update")
	if err := a.captureCurrentConfigDriftBaseline(ctx, req.HostID, req.Container, "post-update"); err != nil {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "config-drift", "Could not refresh post-update drift baseline: "+err.Error())
		a.Logger.Warn("post-update config drift baseline refresh failed", "host_id", req.HostID, "container", req.Container, "error", err)
	}
	a.jobProgress(ctx, req.JobID, 100, "Update completed")
	_ = a.Store.FinishJob(ctx, req.JobID, "success", string(raw), "")
	_ = a.Store.Audit(ctx, "system", "update.success", req.HostID, req.Container, string(raw))
	notifyAutomatic("success", nil)
	if res.Summary.Updated > 0 {
		a.notifyManualUpdateResult(req, "success", nil)
	}
}

func (a *App) notifyManualUpdateResult(req updateRequest, status string, updateErr error) {
	if req.Trigger != "manual" {
		return
	}
	hostName := fmt.Sprintf("Host %d", req.HostID)
	if h, err := a.Store.Host(context.Background(), req.HostID); err == nil {
		hostName = h.Name
	}
	title := "Manual update completed · " + req.Container
	message := hostName + " · " + req.Container + " was updated successfully."
	if status != "success" {
		title = "Manual update failed · " + req.Container
		message = hostName + " · " + req.Container + " update failed."
		if updateErr != nil && strings.TrimSpace(updateErr.Error()) != "" {
			message += " " + updateErr.Error()
		}
	}
	go a.notifyHostUsers(req.HostID, "manual_update", req.Container, title, message, "")
}

func (a *App) failJob(id int64, err error) {
	_ = a.Store.AddJobLog(context.Background(), id, "ERROR", "app", err.Error())
	a.jobProgress(context.Background(), id, 100, "Operation failed")
	_ = a.Store.FinishJob(context.Background(), id, "failed", "", err.Error())
}

func (a *App) automationLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-ticker.C:
			a.runAutomations(now)
		}
	}
}
func (a *App) policyScanHost(ctx context.Context, hostID int64, trigger string, installAuto bool) (watchtower.CheckResponse, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return watchtower.CheckResponse{}, err
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return watchtower.CheckResponse{}, err
	}
	var aggregate watchtower.CheckResponse
	for _, c := range cs {
		if managed, _ := a.systemManagedContainer(c.Name); managed {
			continue
		}
		p, _ := a.Store.Policy(ctx, hostID, c.Name)
		if p.Mode == "ignore" {
			res, e := a.readOnlyRegistryCheck(ctx, hostID, c, trigger+":excluded-info")
			if e != nil {
				a.Logger.Debug("excluded read-only registry check unavailable", "host_id", hostID, "container", c.Name, "error", e)
				continue
			}
			aggregate.Count += res.Count
			aggregate.Containers = append(aggregate.Containers, res.Containers...)
			continue
		}
		if c.State != "running" {
			continue
		}
		res, _, e := a.check(ctx, hostID, c.Name, trigger)
		if e != nil {
			a.Logger.Warn("policy scan container failed", "host_id", hostID, "container", c.Name, "error", e)
			continue
		}
		aggregate.Count += res.Count
		aggregate.Containers = append(aggregate.Containers, res.Containers...)
		if installAuto && p.Mode == "auto" {
			for _, item := range res.Containers {
				if item.Name == c.Name && item.UpdateAvailable {
					_, _ = a.enqueueUpdate(ctx, hostID, c.Name, trigger, "scheduler")
				}
			}
		}
	}
	return aggregate, nil
}

func (a *App) runAutomations(now time.Time) {
	scheduleNow := now
	if loc, err := time.LoadLocation(a.Cfg.Timezone); err == nil {
		scheduleNow = now.In(loc)
	}
	xs, err := a.Store.Automations(a.ctx)
	if err != nil {
		return
	}
	for _, rule := range xs {
		if !bool(rule.Enabled) || !scheduler.Match(rule.Cron, scheduleNow) {
			continue
		}
		if rule.LastRunAt != "" {
			if t, e := time.Parse(time.RFC3339Nano, rule.LastRunAt); e == nil && t.Format("2006-01-02T15:04") == now.UTC().Format("2006-01-02T15:04") {
				continue
			}
		}
		_ = a.Store.TouchAutomation(a.ctx, rule.ID)
		var hostIDs []int64
		switch rule.TargetType {
		case "all":
			hs, _ := a.Store.Hosts(a.ctx)
			for _, h := range hs {
				if h.Enabled {
					hostIDs = append(hostIDs, h.ID)
				}
			}
		case "group":
			hostIDs, _ = a.Store.HostsForGroup(a.ctx, rule.TargetID)
		default:
			if rule.TargetID > 0 {
				hostIDs = []int64{rule.TargetID}
			}
		}
		_ = a.Store.Audit(a.ctx, "scheduler", "automation.run", 0, "", fmt.Sprintf("%s target=%s:%d", rule.Name, rule.TargetType, rule.TargetID))
		for _, hostID := range hostIDs {
			h, e := a.Store.Host(a.ctx, hostID)
			if e != nil || !bool(h.Enabled) {
				continue
			}
			_, _ = a.policyScanHost(a.ctx, hostID, fmt.Sprintf("automation:%d", rule.ID), true)
		}
	}
}

func (a *App) handleAutomations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		x, err := a.Store.Automations(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if !a.isAdmin(r) {
			allowed := a.allowedHostSet(r.Context(), a.identity(r))
			filtered := make([]db.Automation, 0)
			for _, rule := range x {
				visible := false
				switch rule.TargetType {
				case "all":
					visible = len(allowed) > 0
				case "host":
					visible = allowed[rule.TargetID]
				case "group":
					ids, _ := a.Store.HostsForGroup(r.Context(), rule.TargetID)
					for _, id := range ids {
						if allowed[id] {
							visible = true
							break
						}
					}
				}
				if visible {
					filtered = append(filtered, rule)
				}
			}
			x = filtered
		}
		writeJSON(w, 200, x)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	var in automationInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "automation name is required")
		return
	}
	if err := scheduler.Validate(in.Cron); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.TargetType != "all" && in.TargetType != "host" && in.TargetType != "group" {
		writeErr(w, 400, "target_type must be all, host or group")
		return
	}
	if in.TargetType != "all" && in.TargetID <= 0 {
		writeErr(w, 400, "target is required")
		return
	}
	x := db.Automation{ID: in.ID, Name: strings.TrimSpace(in.Name), Cron: in.Cron, TargetType: in.TargetType, TargetID: in.TargetID, Enabled: db.Bool(in.Enabled)}
	id, err := a.Store.SaveAutomation(r.Context(), x)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "automation.save", 0, "", in.Name)
	writeJSON(w, 200, map[string]any{"id": id})
}
func (a *App) handleAutomationDelete(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/automations/"), "/"), 10, 64)
	if err := a.Store.DeleteAutomation(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		x, err := a.Store.HostGroups(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if !a.isAdmin(r) {
			u, e := a.Store.User(r.Context(), a.identity(r).UserID)
			if e != nil {
				writeErr(w, 500, e.Error())
				return
			}
			assignedGroups := map[int64]bool{}
			for _, id := range u.GroupIDs {
				assignedGroups[id] = true
			}
			allowedHosts := a.allowedHostSet(r.Context(), a.identity(r))
			f := make([]db.HostGroup, 0)
			for _, g := range x {
				visible := assignedGroups[g.ID]
				visibleHosts := make([]int64, 0)
				for _, hid := range g.HostIDs {
					if allowedHosts[hid] {
						visible = true
						visibleHosts = append(visibleHosts, hid)
					}
				}
				if visible {
					g.HostIDs = visibleHosts
					f = append(f, g)
				}
			}
			x = f
		}
		writeJSON(w, 200, x)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	var in groupInput
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "group name is required")
		return
	}
	id, err := a.Store.SaveHostGroup(r.Context(), db.HostGroup{ID: in.ID, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), HostIDs: in.HostIDs})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "group.save", 0, "", in.Name)
	writeJSON(w, 200, map[string]any{"id": id})
}
func (a *App) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/"), 10, 64)
	if err := a.Store.DeleteHostGroup(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func managedUserVisible(viewer auth.Identity, owner bool, u db.User) bool {
	if owner || u.Role != "admin" {
		return true
	}
	// An Admin can see their own Admin account for password maintenance,
	// while peer Admin accounts remain Owner-only.
	return u.ID == viewer.UserID
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	owner := a.isOwner(r)
	if r.Method == http.MethodGet {
		x, err := a.Store.Users(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out := make([]publicUser, 0, len(x)+1)
		// The Owner is bootstrap-backed rather than a row in users, but it is
		// still a real managed account from the operator's point of view.
		out = append(out, publicUser{ID: 0, Username: "admin", Role: "owner", Enabled: true, HostIDs: []int64{}, GroupIDs: []int64{}, PasswordManaged: strings.TrimSpace(a.Store.Setting(r.Context(), "owner_password_hash", "")) != ""})
		current := a.identity(r)
		for _, u := range x {
			if !managedUserVisible(current, owner, u) {
				continue
			}
			out = append(out, publicUser{ID: u.ID, Username: u.Username, Role: u.Role, Enabled: bool(u.Enabled), HostIDs: u.HostIDs, GroupIDs: u.GroupIDs, CreatedAt: u.CreatedAt})
		}
		writeJSON(w, 200, out)
		return
	}
	var in userInput
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Username) == "" {
		writeErr(w, 400, "username is required")
		return
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	username := strings.TrimSpace(in.Username)
	// ID 0 is reserved for the environment/bootstrap Owner. Changing its
	// password creates a persistent hash in /data; from then on that hash takes
	// precedence over WTUI_ADMIN_PASSWORD without trying to rewrite .env.
	if role == "owner" || (in.ID == 0 && strings.EqualFold(username, "admin")) {
		if !owner {
			writeErr(w, 403, "only the owner can change the owner account")
			return
		}
		if !strings.EqualFold(username, "admin") {
			writeErr(w, 400, "owner username is fixed to admin")
			return
		}
		if strings.TrimSpace(in.Password) == "" {
			writeErr(w, 400, "a new owner password is required")
			return
		}
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := a.Store.SetSetting(r.Context(), "owner_password_hash", h); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "owner.password.change", 0, "", "persistent owner password enabled")
		writeJSON(w, 200, map[string]any{"id": 0, "password_managed": true})
		return
	}
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		writeErr(w, 400, "role must be admin or user")
		return
	}
	if strings.EqualFold(username, "admin") || strings.EqualFold(username, "owner") {
		writeErr(w, 400, "username is reserved for the owner account")
		return
	}
	selfAdminRequest := !owner && in.ID > 0 && role == "admin" && in.ID == a.identity(r).UserID
	if !owner && role != "user" && !selfAdminRequest {
		writeErr(w, 403, "only the owner can create or modify admins")
		return
	}
	if in.ID > 0 && !owner {
		existing, e := a.Store.User(r.Context(), in.ID)
		if e != nil {
			writeErr(w, 404, "account not found")
			return
		}
		if existing.Role == "admin" {
			current := a.identity(r)
			if existing.ID != current.UserID {
				writeErr(w, 403, "admin accounts can only be managed by the owner")
				return
			}
			// An Admin may change only their own password. Role, username and
			// enabled state remain Owner-controlled.
			if strings.TrimSpace(in.Password) == "" {
				writeErr(w, 400, "a new password is required")
				return
			}
			h, he := auth.HashPassword(in.Password)
			if he != nil {
				writeErr(w, 400, he.Error())
				return
			}
			existing.PasswordHash = h
			id, se := a.Store.SaveUser(r.Context(), existing)
			if se != nil {
				writeErr(w, 400, se.Error())
				return
			}
			_ = a.Store.Audit(r.Context(), a.actor(r), "user.password.change", 0, "", existing.Username)
			writeJSON(w, 200, map[string]any{"id": id, "password_changed": true})
			return
		}
		if existing.Role != "user" {
			writeErr(w, 403, "this account can only be managed by the owner")
			return
		}
		// Admins can fully manage User-scoped assignments, but cannot
		// promote the account or change the role hierarchy.
		role = "user"
	}
	u := db.User{ID: in.ID, Username: username, Role: role, Enabled: db.Bool(in.Enabled), HostIDs: in.HostIDs, GroupIDs: in.GroupIDs}
	if role == "admin" {
		u.HostIDs = nil
		u.GroupIDs = nil
	}
	if in.ID == 0 && in.Password == "" {
		writeErr(w, 400, "password is required for a new user")
		return
	}
	if in.Password != "" {
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		u.PasswordHash = h
	}
	id, err := a.Store.SaveUser(r.Context(), u)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "user.save", 0, "", u.Username+" role="+u.Role)
	writeJSON(w, 200, map[string]any{"id": id})
}
func (a *App) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/users/"), "/"), 10, 64)
	if id == 0 {
		writeErr(w, 409, "owner account cannot be deleted")
		return
	}
	if !a.isOwner(r) {
		if u, e := a.Store.User(r.Context(), id); e != nil || u.Role != "user" {
			writeErr(w, 403, "admin accounts can only be deleted by the owner")
			return
		}
	}
	if err := a.Store.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	x, err := a.Store.Jobs(r.Context(), 150)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !a.isAdmin(r) {
		allowed := a.allowedHostSet(r.Context(), a.identity(r))
		f := make([]db.Job, 0, len(x))
		for _, j := range x {
			if allowed[j.HostID] {
				f = append(f, j)
			}
		}
		x = f
	}
	writeJSON(w, 200, x)
}
func (a *App) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	idS := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/")
	id, _ := strconv.ParseInt(idS, 10, 64)
	if !a.isAdmin(r) {
		jobs, _ := a.Store.Jobs(r.Context(), 500)
		ok := false
		for _, j := range jobs {
			if j.ID == id && a.hostAllowed(r, j.HostID) {
				ok = true
				break
			}
		}
		if !ok {
			writeErr(w, http.StatusForbidden, "job access denied")
			return
		}
	}
	x, err := a.Store.JobLogs(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, x)
}
func latestJobProgress(logs []db.JobLog, status string) (int, string) {
	percent := 0
	stage := "Queued"
	for i := len(logs) - 1; i >= 0; i-- {
		if logs[i].Source != "progress" {
			continue
		}
		parts := strings.SplitN(logs[i].Message, "|", 2)
		if len(parts) != 2 {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			if n < 0 {
				n = 0
			}
			if n > 100 {
				n = 100
			}
			percent = n
			stage = strings.TrimSpace(parts[1])
			break
		}
	}
	if status == "success" {
		percent = 100
		if stage == "" || stage == "Queued" {
			stage = "Completed"
		}
	}
	if status == "failed" {
		percent = 100
		if stage == "" || stage == "Queued" {
			stage = "Failed"
		}
	}
	return percent, stage
}

func (a *App) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	idS := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/job-status/"), "/")
	id, err := strconv.ParseInt(idS, 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := a.Store.Job(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if !a.isAdmin(r) && !a.hostAllowed(r, job.HostID) {
		writeErr(w, http.StatusForbidden, "job access denied")
		return
	}
	logs, _ := a.Store.JobLogs(r.Context(), id)
	percent, stage := latestJobProgress(logs, job.Status)
	writeJSON(w, 200, map[string]any{"job": job, "progress": map[string]any{"percent": percent, "stage": stage}})
}

func (a *App) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		x, err := a.Store.Schedules(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, x)
		return
	}
	var in scheduleInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if err := scheduler.Validate(in.Cron); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Action != "check" && in.Action != "update" {
		writeErr(w, 400, "action must be check or update")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "schedule name is required")
		return
	}
	if in.HostID <= 0 {
		writeErr(w, 400, "host is required")
		return
	}
	cleanTargets := make([]string, 0, len(in.Containers))
	seenTargets := map[string]bool{}
	for _, target := range in.Containers {
		target = strings.TrimSpace(target)
		if target == "" || seenTargets[target] {
			continue
		}
		seenTargets[target] = true
		cleanTargets = append(cleanTargets, target)
	}
	if len(cleanTargets) == 0 {
		writeErr(w, 400, "select at least one container or all running containers")
		return
	}
	if seenTargets["*"] && len(cleanTargets) > 1 {
		writeErr(w, 400, "all running containers cannot be combined with individual containers")
		return
	}
	b, _ := json.Marshal(cleanTargets)
	x := db.Schedule{ID: in.ID, Name: in.Name, Cron: in.Cron, Action: in.Action, HostID: in.HostID, Containers: string(b), Enabled: db.Bool(in.Enabled)}
	id, err := a.Store.SaveSchedule(r.Context(), x)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), "admin", "schedule.save", in.HostID, "", in.Name)
	writeJSON(w, 200, map[string]any{"id": id})
}
func (a *App) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/schedules/"), "/"), 10, 64)
	if err := a.Store.DeleteSchedule(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	x, err := a.Store.Audits(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !a.isAdmin(r) {
		allowed := a.allowedHostSet(r.Context(), a.identity(r))
		actor := a.actor(r)
		f := make([]db.Audit, 0)
		for _, e := range x {
			if e.Actor == actor || (e.HostID > 0 && allowed[e.HostID]) {
				f = append(f, e)
			}
		}
		x = f
	}
	writeJSON(w, 200, x)
}
func (a *App) handleDockerEvents(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
	if id > 0 && !a.hostAllowed(r, id) {
		writeErr(w, http.StatusForbidden, "host access denied")
		return
	}
	x, err := a.Store.DockerEvents(r.Context(), id, 250)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if id == 0 && !a.isAdmin(r) {
		allowed := a.allowedHostSet(r.Context(), a.identity(r))
		f := make([]db.DockerEvent, 0)
		for _, e := range x {
			if allowed[e.HostID] {
				f = append(f, e)
			}
		}
		x = f
	}
	writeJSON(w, 200, x)
}

func (a *App) handleReleaseNotes(w http.ResponseWriter, r *http.Request, hostID int64, container string) {
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	p, _ := a.Store.Policy(r.Context(), hostID, container)
	repo := p.ReleaseRepo
	cs, err := a.Docker.ListContainers(r.Context(), h.Endpoint)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	image := ""
	labelRef := ""
	for _, c := range cs {
		if c.Name == container {
			image = c.Image
			labelRef = c.ImageID
			if labelRef == "" {
				labelRef = image
			}
			break
		}
	}
	if repo == "" && image != "" {
		labels, e := a.Docker.ImageLabels(r.Context(), h.Endpoint, labelRef)
		if e == nil {
			repo, _ = releases.DetectFromLabels(labels)
		}
	}
	if repo == "" {
		repo, _ = releases.FallbackFromImage(image)
	}
	if repo == "" {
		writeErr(w, 404, "no GitHub release source could be detected; set a custom repository in the container policy")
		return
	}
	rel, err := a.Releases.Latest(r.Context(), repo)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, rel)
}

func (a *App) handleApplicationLogs(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	path := filepath.Join(a.Cfg.DataDir, "logs", "app.log")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, 500, err.Error())
		return
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 500 {
		lines = lines[len(lines)-500:]
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}
func (a *App) handlePushoverLogs(w http.ResponseWriter, r *http.Request) {
	id := a.identity(r)
	var userID *int64
	if id.Role != "owner" && id.Role != "admin" {
		uid := id.UserID
		userID = &uid
	}
	x, err := a.Store.NotificationDeliveries(r.Context(), userID, 300)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, x)
}

func (a *App) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="vibewatch-support.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	hosts, _ := a.Store.Hosts(r.Context())
	jobs, _ := a.Store.Jobs(r.Context(), 200)
	audits, _ := a.Store.Audits(r.Context(), 200)
	events, _ := a.Store.DockerEvents(r.Context(), 0, 200)
	automations, _ := a.Store.Automations(r.Context())
	groups, _ := a.Store.HostGroups(r.Context())
	writeZipJSON(zw, "hosts.json", hosts)
	writeZipJSON(zw, "jobs.json", jobs)
	writeZipJSON(zw, "audit.json", audits)
	writeZipJSON(zw, "docker-events.json", events)
	writeZipJSON(zw, "automations.json", automations)
	writeZipJSON(zw, "host-groups.json", groups)
	if history, e := a.Store.UpdateHistory(r.Context(), 500, 0, ""); e == nil {
		writeZipJSON(zw, "update-history.json", history)
	}
	if creds, e := a.Store.RegistryCredentials(r.Context()); e == nil {
		meta := make([]map[string]any, 0, len(creds))
		for _, c := range creds {
			meta = append(meta, map[string]any{"id": c.ID, "registry": c.Registry, "username": c.Username, "secret_configured": c.SecretEnc != "", "updated_at": c.UpdatedAt})
		}
		writeZipJSON(zw, "registry-credentials.json", meta)
	}
	if result, err := a.Store.IntegrityCheck(r.Context()); err != nil {
		writeZipJSON(zw, "database-integrity.json", map[string]any{"ok": false, "error": err.Error()})
	} else {
		writeZipJSON(zw, "database-integrity.json", map[string]any{"ok": strings.TrimSpace(result) == "ok", "result": result})
	}
	// Digest/version state is intentionally exported without credentials so an
	// Unknown version or Excluded read-only check can be diagnosed directly.
	containerState := map[string]any{}
	for _, h := range hosts {
		key := fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))
		cs, err := a.Docker.ListContainers(r.Context(), h.Endpoint)
		if err != nil {
			containerState[key] = map[string]any{"error": err.Error()}
			continue
		}
		rows := make([]map[string]any, 0, len(cs))
		for _, c := range cs {
			p, _ := a.Store.Policy(r.Context(), h.ID, c.Name)
			cache, _ := a.Store.Cache(r.Context(), h.ID, c.Name)
			v, _ := a.Store.Version(r.Context(), h.ID, c.Name)
			drift, _ := a.Store.ConfigDrift(r.Context(), h.ID, c.Name)
			rows = append(rows, map[string]any{"name": c.Name, "image": c.Image, "image_id": c.ImageID, "state": c.State, "policy": p, "digest_state": cache, "version_metadata": v, "config_drift": drift})
		}
		containerState[key] = rows
	}
	writeZipJSON(zw, "container-update-state.json", containerState)
	// Recovery-backup diagnostics contain metadata only. The ZIP contents themselves
	// may include runtime environment secrets and are therefore never embedded in a
	// support bundle.
	if backupUnits, e := a.discoverContainerBackupUnits(r.Context()); e == nil {
		writeZipJSON(zw, "container-backups.json", map[string]any{"retention": a.containerSnapshotRetention(r.Context()), "units": backupUnits})
	} else {
		writeZipJSON(zw, "container-backups.json", map[string]any{"retention": a.containerSnapshotRetention(r.Context()), "error": e.Error()})
	}
	// Include the exact Docker-derived host metrics used by the Dashboard so
	// memory/CPU/storage discrepancies can be diagnosed from future bundles.
	hostOverviews := map[string]any{}
	for _, h := range hosts {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		o, e := a.Docker.HostOverview(ctx, h.Endpoint, false)
		cancel()
		key := fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))
		if e != nil {
			hostOverviews[key] = map[string]any{"error": e.Error()}
		} else {
			hostOverviews[key] = o
		}
	}
	writeZipJSON(zw, "host-overviews.json", hostOverviews)
	volumeDiag := map[string]any{}
	for _, h := range hosts {
		key := fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		vols, e := a.Docker.VolumeInventory(ctx, h.Endpoint)
		cancel()
		if e != nil {
			volumeDiag[key] = map[string]any{"error": e.Error()}
			continue
		}
		rows := make([]map[string]any, 0, len(vols))
		for _, v := range vols {
			rows = append(rows, map[string]any{"name": v.Name, "driver": v.Driver, "scope": v.Scope, "created_at": v.CreatedAt, "in_use": v.InUse, "ref_count": v.RefCount, "anonymous": v.Anonymous, "unused": v.Unused})
		}
		volumeDiag[key] = rows
	}
	writeZipJSON(zw, "host-volumes.json", volumeDiag)
	// Notification diagnostics intentionally omit every user's Pushover App
	// Token and User Key. Only non-secret configuration state is exported.
	notifDiag := []map[string]any{}
	users, _ := a.Store.Users(r.Context())
	for _, u := range users {
		ns, _ := a.Store.NotificationSettings(r.Context(), u.ID)
		notifDiag = append(notifDiag, map[string]any{
			"user_id": u.ID, "username": u.Username, "role": u.Role,
			"app_token_configured":    strings.TrimSpace(ns.PushoverAppToken) != "",
			"user_key_configured":     strings.TrimSpace(ns.PushoverUserKey) != "",
			"notify_auto_updates":     bool(ns.NotifyAutoUpdates),
			"notify_manual_available": bool(ns.NotifyManualAvailable),
			"notify_manual_updates":   bool(ns.NotifyManualUpdates),
		})
	}
	ownerNS, _ := a.Store.NotificationSettings(r.Context(), 0)
	notifDiag = append(notifDiag, map[string]any{
		"user_id": 0, "username": "admin", "role": "owner",
		"app_token_configured":    strings.TrimSpace(ownerNS.PushoverAppToken) != "",
		"user_key_configured":     strings.TrimSpace(ownerNS.PushoverUserKey) != "",
		"notify_auto_updates":     bool(ownerNS.NotifyAutoUpdates),
		"notify_manual_available": bool(ownerNS.NotifyManualAvailable),
		"notify_manual_updates":   bool(ownerNS.NotifyManualUpdates),
	})
	writeZipJSON(zw, "notifications.json", map[string]any{"accounts": notifDiag, "credential_model": "per-user app token + per-user user key"})
	deliveries, _ := a.Store.NotificationDeliveries(r.Context(), nil, 500)
	writeZipJSON(zw, "pushover-deliveries.json", deliveries)
	if mount, mounted, err := a.Docker.ContainerMount(r.Context(), a.Cfg.ControllerName, a.Cfg.DataDir); err == nil {
		writeZipJSON(zw, "persistence.json", map[string]any{"data_dir": a.Cfg.DataDir, "database_path": a.Store.Path, "mounted": mounted, "mount": mount, "last_backup_at": a.Store.Setting(r.Context(), "last_backup_at", ""), "last_backup_file": a.Store.Setting(r.Context(), "last_backup_file", "")})
	}
	workerStates := map[string]dockercli.WorkerState{}
	for _, h := range hosts {
		key := fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))
		workerStates[key] = a.Docker.WorkerState(r.Context(), h.ID)
		if logs, e := a.Docker.WorkerLogsRecent(r.Context(), h.ID, 300); e == nil || logs != "" {
			if f, ce := zw.Create("workers/" + key + ".log"); ce == nil {
				_, _ = f.Write([]byte(logs))
			}
		}
	}
	writeZipJSON(zw, "workers.json", workerStates)
	if f, err := zw.Create("application.log"); err == nil {
		path := filepath.Join(a.Cfg.DataDir, "logs", "app.log")
		if src, e := os.Open(path); e == nil {
			defer src.Close()
			_, _ = io.Copy(f, io.LimitReader(src, 5<<20))
		}
	}
}
func writeZipJSON(z *zip.Writer, name string, v any) {
	f, err := z.Create(name)
	if err != nil {
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	_, _ = f.Write(b)
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "host"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
func requestLog(l *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		l.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func spaHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if root == "" {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(root, filepath.Clean(r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			http.ServeFile(w, r, p)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}

func settingBool(s *db.Store, ctx context.Context, key string, def bool) bool {
	fallback := "false"
	if def {
		fallback = "true"
	}
	v := strings.TrimSpace(strings.ToLower(s.Setting(ctx, key, fallback)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
func (a *App) systemSettings(ctx context.Context) map[string]any {
	mount, mounted, mountErr := a.Docker.ContainerMount(ctx, a.Cfg.ControllerName, a.Cfg.DataDir)
	persistent := mounted && mount.RW && (mount.Type == "bind" || mount.Type == "volume")
	mountError := ""
	if mountErr != nil {
		mountError = mountErr.Error()
	}
	return map[string]any{
		"worker_auto_update":           settingBool(a.Store, ctx, "worker_auto_update", true),
		"worker_update_cron":           a.Store.Setting(ctx, "worker_update_cron", "30 3 * * *"),
		"worker_last_update_at":        a.Store.Setting(ctx, "worker_last_update_at", ""),
		"worker_last_update_result":    a.Store.Setting(ctx, "worker_last_update_result", ""),
		"self_update_auto":             settingBool(a.Store, ctx, "self_update_auto", false),
		"self_update_cron":             a.Store.Setting(ctx, "self_update_cron", "15 4 * * 0"),
		"self_update_last_at":          a.Store.Setting(ctx, "self_update_last_at", ""),
		"app_version":                  a.Cfg.Version,
		"app_image":                    a.Cfg.AppImage,
		"controller_name":              a.Cfg.ControllerName,
		"worker_image":                 a.Docker.WorkerImage,
		"data_dir":                     a.Cfg.DataDir,
		"database_path":                a.Store.Path,
		"backup_dir":                   filepath.Join(a.Cfg.DataDir, "backups"),
		"last_backup_at":               a.Store.Setting(ctx, "last_backup_at", ""),
		"last_backup_file":             a.Store.Setting(ctx, "last_backup_file", ""),
		"container_snapshot_retention": a.containerSnapshotRetention(ctx),
		"data_mount_persistent":        persistent,
		"data_mount_type":              mount.Type,
		"data_mount_source":            mount.Source,
		"data_mount_rw":                mount.RW,
		"data_mount_error":             mountError,
	}
}

func (a *App) handleSystemSettings(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, a.systemSettings(r.Context()))
		return
	}
	var in systemSettingsInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if err := scheduler.Validate(in.WorkerUpdateCron); err != nil {
		writeErr(w, 400, "worker update schedule: "+err.Error())
		return
	}
	if err := scheduler.Validate(in.SelfUpdateCron); err != nil {
		writeErr(w, 400, "self update schedule: "+err.Error())
		return
	}
	retention := in.ContainerSnapshotRetention
	if retention == 0 {
		retention = a.containerSnapshotRetention(r.Context())
	}
	if retention < 1 || retention > 20 {
		writeErr(w, 400, "container snapshot retention must be between 1 and 20")
		return
	}
	_ = a.Store.SetSetting(r.Context(), "worker_auto_update", strconv.FormatBool(in.WorkerAutoUpdate))
	_ = a.Store.SetSetting(r.Context(), "worker_update_cron", in.WorkerUpdateCron)
	_ = a.Store.SetSetting(r.Context(), "self_update_auto", strconv.FormatBool(in.SelfUpdateAuto))
	_ = a.Store.SetSetting(r.Context(), "self_update_cron", in.SelfUpdateCron)
	_ = a.Store.SetSetting(r.Context(), "container_snapshot_retention", strconv.Itoa(retention))
	a.enforceAllSnapshotRetention()
	_ = a.Store.Audit(r.Context(), a.actor(r), "system.settings", 0, "", "maintenance settings changed")
	writeJSON(w, 200, a.systemSettings(r.Context()))
}
func (a *App) systemMaintenanceLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-t.C:
			a.runSystemMaintenance(now)
		}
	}
}
func sameMinuteUTC(v string, now time.Time) bool {
	if v == "" {
		return false
	}
	t, e := time.Parse(time.RFC3339Nano, v)
	return e == nil && t.UTC().Format("2006-01-02T15:04") == now.UTC().Format("2006-01-02T15:04")
}
func (a *App) runSystemMaintenance(now time.Time) {
	local := now
	if loc, e := time.LoadLocation(a.Cfg.Timezone); e == nil {
		local = now.In(loc)
	}
	if settingBool(a.Store, a.ctx, "worker_auto_update", true) {
		cron := a.Store.Setting(a.ctx, "worker_update_cron", "30 3 * * *")
		last := a.Store.Setting(a.ctx, "worker_last_update_at", "")
		if scheduler.Match(cron, local) && !sameMinuteUTC(last, now) {
			_ = a.Store.SetSetting(a.ctx, "worker_last_update_at", now.UTC().Format(time.RFC3339Nano))
			go a.performWorkerUpdate("scheduled")
		}
	}
	if settingBool(a.Store, a.ctx, "self_update_auto", false) && strings.TrimSpace(a.Cfg.AppImage) != "" {
		cron := a.Store.Setting(a.ctx, "self_update_cron", "15 4 * * 0")
		last := a.Store.Setting(a.ctx, "self_update_last_at", "")
		if scheduler.Match(cron, local) && !sameMinuteUTC(last, now) {
			_ = a.Store.SetSetting(a.ctx, "self_update_last_at", now.UTC().Format(time.RFC3339Nano))
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if _, err := a.createDatabaseBackup(ctx, "scheduled-self-update"); err != nil {
					a.Logger.Error("scheduled self update backup failed", "error", err)
					cancel()
					return
				}
				cancel()
				if err := a.Docker.LaunchSelfUpdate(context.Background(), a.Cfg.ControllerName); err != nil {
					a.Logger.Error("scheduled self update failed", "error", err)
				}
			}()
		}
	}
}
func (a *App) performWorkerUpdate(trigger string) (map[string]any, error) {
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	changed, before, after, err := a.Docker.PullImage(ctx, a.Docker.WorkerImage)
	if err != nil {
		_ = a.Store.SetSetting(ctx, "worker_last_update_result", "failed: "+err.Error())
		return nil, err
	}
	result := map[string]any{"image": a.Docker.WorkerImage, "changed": changed, "before": before, "after": after, "recreated": 0, "failed": 0}
	if !changed {
		_ = a.Store.SetSetting(ctx, "worker_last_update_result", "current")
		return result, nil
	}
	a.workerOpMu.Lock()
	defer a.workerOpMu.Unlock()
	hosts, _ := a.Store.Hosts(ctx)
	recreated, failed := 0, 0
	for _, h := range hosts {
		if !bool(h.Enabled) {
			continue
		}
		_ = a.Docker.RemoveWorker(ctx, h.ID)
		if _, e := a.ensureWorker(ctx, h); e != nil {
			failed++
			a.Logger.Error("worker update recreate failed", "host", h.Name, "error", e)
		} else {
			recreated++
		}
	}
	result["recreated"] = recreated
	result["failed"] = failed
	_ = a.Store.SetSetting(ctx, "worker_last_update_result", fmt.Sprintf("updated; recreated=%d failed=%d", recreated, failed))
	_ = a.Store.Audit(ctx, "system", "workers.update", 0, "", fmt.Sprintf("trigger=%s recreated=%d failed=%d", trigger, recreated, failed))
	return result, nil
}
func (a *App) createDatabaseBackup(ctx context.Context, trigger string) (string, error) {
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	dir := filepath.Join(a.Cfg.DataDir, "backups")
	name := "vibewatch-" + time.Now().UTC().Format("20060102-150405.000000000") + ".db"
	dest := filepath.Join(dir, name)
	if err := a.Store.Backup(ctx, dest); err != nil {
		return "", err
	}
	// Registry credentials are encrypted with a random key stored under /data.
	// Preserve that key alongside DB backups when it exists, otherwise a restored
	// database would retain encrypted rows that could no longer be decrypted.
	if keyBytes, e := os.ReadFile(a.registryKeyPath()); e == nil && len(keyBytes) == 32 {
		_ = os.WriteFile(dest+".registry-key", keyBytes, 0o600)
	}
	_ = a.Store.SetSetting(ctx, "last_backup_at", time.Now().UTC().Format(time.RFC3339Nano))
	_ = a.Store.SetSetting(ctx, "last_backup_file", name)
	_ = a.Store.Audit(ctx, "system", "database.backup", 0, "", "trigger="+trigger+" file="+name)
	// Keep the ten newest automatic/manual backups so scheduled self-updates do
	// not grow the persistent data directory without bound.
	if entries, err := os.ReadDir(dir); err == nil {
		files := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && (strings.HasPrefix(e.Name(), "vibewatch-") || strings.HasPrefix(e.Name(), "watchtower-ui-")) && strings.HasSuffix(e.Name(), ".db") {
				files = append(files, e.Name())
			}
		}
		if len(files) > 10 {
			sort.Strings(files)
			for _, old := range files[:len(files)-10] {
				_ = os.Remove(filepath.Join(dir, old))
				_ = os.Remove(filepath.Join(dir, old+".registry-key"))
			}
		}
	}
	return dest, nil
}

func (a *App) handleSystemBackup(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	dest, err := a.createDatabaseBackup(ctx, "manual-owner")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "file": filepath.Base(dest)})
}

func (a *App) handleWorkerUpdate(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	res, err := a.performWorkerUpdate("manual-owner")
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, res)
}
func (a *App) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		out := a.systemSettings(r.Context())
		out["available"] = false
		if strings.TrimSpace(a.Cfg.AppImage) != "" && a.Registry != nil {
			if rv, e := a.Registry.RemoteVersion(r.Context(), a.Cfg.AppImage); e == nil {
				out["latest_version"] = rv.Version
				out["latest_source"] = rv.Source
				out["available"] = rv.Version != "" && strings.TrimPrefix(rv.Version, "v") != strings.TrimPrefix(a.Cfg.Version, "v")
			} else {
				out["check_error"] = e.Error()
			}
		}
		writeJSON(w, 200, out)
		return
	}
	if strings.TrimSpace(a.Cfg.AppImage) == "" {
		writeErr(w, 409, "self update is not configured; set WTUI_APP_IMAGE to the published registry image")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	if _, err := a.createDatabaseBackup(ctx, "manual-self-update"); err != nil {
		cancel()
		writeErr(w, 500, "database backup before self update failed: "+err.Error())
		return
	}
	cancel()
	_ = a.Store.Audit(r.Context(), a.actor(r), "self-update.launch", 0, "", a.Cfg.AppImage)
	if err := a.Docker.LaunchSelfUpdate(r.Context(), a.Cfg.ControllerName); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "self update helper launched; the controller will restart only if its image is newer"})
}

func clipText(v string, max int) string {
	v = strings.TrimSpace(v)
	if len(v) <= max {
		return v
	}
	return v[:max] + "…"
}

func (a *App) handleClientError(w http.ResponseWriter, r *http.Request) {
	var in clientErrorInput
	if json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	a.Logger.Error("browser client error",
		"actor", a.actor(r),
		"message", clipText(in.Message, 4000),
		"stack", clipText(in.Stack, 12000),
		"url", clipText(in.URL, 2000),
	)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func maskSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 6 {
		return "configured"
	}
	return "••••••" + v[len(v)-6:]
}

func (a *App) handleNotificationSettings(w http.ResponseWriter, r *http.Request) {
	id := a.identity(r)
	uid := id.UserID
	if r.Method == http.MethodGet {
		x, e := a.Store.NotificationSettings(r.Context(), uid)
		if e != nil {
			writeErr(w, 500, e.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"settings":             x,
			"app_token_configured": strings.TrimSpace(x.PushoverAppToken) != "",
			"masked_app_token":     maskSecret(x.PushoverAppToken),
			"pushover_ready":       strings.TrimSpace(x.PushoverAppToken) != "" && strings.TrimSpace(x.PushoverUserKey) != "",
		})
		return
	}
	var in notificationInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	current, _ := a.Store.NotificationSettings(r.Context(), uid)
	appToken := strings.TrimSpace(current.PushoverAppToken)
	if in.ClearPushoverAppToken {
		appToken = ""
	} else if strings.TrimSpace(in.PushoverAppToken) != "" {
		appToken = strings.TrimSpace(in.PushoverAppToken)
		if len(appToken) < 20 || len(appToken) > 128 || strings.ContainsAny(appToken, " \t\r\n") {
			writeErr(w, 400, "Pushover.net application API token format looks invalid")
			return
		}
	}
	x := db.NotificationSettings{UserID: uid, PushoverAppToken: appToken, PushoverUserKey: strings.TrimSpace(in.PushoverUserKey), NotifyAutoUpdates: db.Bool(in.NotifyAutoUpdates), NotifyManualAvailable: db.Bool(in.NotifyManualAvailable), NotifyManualUpdates: db.Bool(in.NotifyManualUpdates)}
	if e := a.Store.SaveNotificationSettings(r.Context(), x); e != nil {
		writeErr(w, 500, e.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "notifications.save", 0, "", "per-user Pushover credentials/preferences updated")
	writeJSON(w, 200, map[string]any{
		"ok":                   true,
		"app_token_configured": appToken != "",
		"masked_app_token":     maskSecret(appToken),
		"pushover_ready":       appToken != "" && strings.TrimSpace(x.PushoverUserKey) != "",
	})
}
func (a *App) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	x, e := a.Store.NotificationSettings(r.Context(), a.identity(r).UserID)
	if e != nil {
		writeErr(w, 500, e.Error())
		return
	}
	if strings.TrimSpace(x.PushoverAppToken) == "" {
		writeErr(w, 400, "save your own Pushover.net Application API Token/Key first")
		return
	}
	if strings.TrimSpace(x.PushoverUserKey) == "" {
		writeErr(w, 400, "save your own Pushover.net User Key first")
		return
	}
	if a.Pushover == nil {
		writeErr(w, 503, "Pushover client is unavailable")
		return
	}
	title := "Vibewatch"
	e = a.Pushover.Send(r.Context(), x.PushoverAppToken, x.PushoverUserKey, title, "Test notification from Vibewatch")
	status, errText := "success", ""
	if e != nil {
		status, errText = "failed", redactPushoverError(e.Error(), x.PushoverAppToken, x.PushoverUserKey)
	}
	_ = a.Store.AddNotificationDelivery(context.Background(), db.NotificationDelivery{UserID: a.identity(r).UserID, Username: a.actor(r), Event: "test", Title: title, Status: status, Error: errText})
	if e != nil {
		writeErr(w, 502, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func redactPushoverError(v string, secrets ...string) string {
	out := v
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			out = strings.ReplaceAll(out, secret, "REDACTED")
		}
	}
	return out
}

func (a *App) notifyHostUsers(hostID int64, event, container, title, message, fingerprint string) {
	if a.Pushover == nil {
		return
	}
	targets, err := a.Store.NotificationTargets(context.Background(), hostID, event)
	if err != nil {
		return
	}
	for _, t := range targets {
		if fingerprint != "" {
			if a.Store.NotificationFingerprint(context.Background(), t.UserID, hostID, container, event) == fingerprint {
				continue
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.Pushover.Send(ctx, t.PushoverAppToken, t.PushoverUserKey, title, message)
		cancel()
		status, errText := "success", ""
		if err != nil {
			status, errText = "failed", redactPushoverError(err.Error(), t.PushoverAppToken, t.PushoverUserKey)
		}
		_ = a.Store.AddNotificationDelivery(context.Background(), db.NotificationDelivery{UserID: t.UserID, Username: t.Username, HostID: hostID, ContainerName: container, Event: event, Title: title, Status: status, Error: errText})
		if err != nil {
			a.Logger.Warn("pushover notification failed", "user", t.Username, "host_id", hostID, "event", event, "error", err)
			continue
		}
		if fingerprint != "" {
			_ = a.Store.SaveNotificationFingerprint(context.Background(), t.UserID, hostID, container, event, fingerprint)
		}
	}
}
