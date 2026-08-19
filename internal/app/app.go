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

	"github.com/m9rph/vibewatch/internal/auth"
	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/devupdate"
	"github.com/m9rph/vibewatch/internal/dockercli"
	"github.com/m9rph/vibewatch/internal/notify"
	"github.com/m9rph/vibewatch/internal/registry"
	"github.com/m9rph/vibewatch/internal/releases"
	"github.com/m9rph/vibewatch/internal/scheduler"
	"github.com/m9rph/vibewatch/internal/sshsetup"
	"github.com/m9rph/vibewatch/internal/watchtower"
)

type Config struct {
	DataDir          string
	WebDir           string
	Timezone         string
	Version          string
	AppImage         string
	ControllerName   string
	DeveloperUpdates bool
	ProjectDir       string
}
type App struct {
	Cfg               Config
	Store             *db.Store
	Docker            *dockercli.Client
	WT                *watchtower.Client
	Releases          *releases.GitHubClient
	Registry          *registry.Client
	Pushover          *notify.Pushover
	SSH               *sshsetup.Client
	Logger            *slog.Logger
	Auth              *auth.Manager
	Events            *dockercli.EventWatcher
	Queue             chan updateRequest
	ctx               context.Context
	cancel            context.CancelFunc
	ensureMu          sync.Mutex
	ensureLocks       map[int64]*sync.Mutex
	workerOpMu        sync.RWMutex
	maintenanceMu     sync.Mutex
	infoRefreshMu     sync.Mutex
	infoRefresh       map[string]bool
	registryMu        sync.Mutex
	registryKey       []byte
	hostHealthMu      sync.Mutex
	hostHealth        map[int64]hostHealthState
	hostHealthRun     map[int64]bool
	quickSetupMu      sync.Mutex
	quickSetupOps     map[string]quickSetupProgress
	chainPreflightMu  sync.Mutex
	chainPreflightOps map[string]ChainPreflightProgress
	chainMu           sync.Mutex
	chainReserved     map[string]int64
	continuityMu      sync.RWMutex
	jobCancelMu       sync.Mutex
	jobCancels        map[int64]context.CancelFunc
	devUpdateMu       sync.Mutex
	mutationGate      *mutationPipelineGate
}
type updateRequest struct {
	JobID                     int64
	TransactionID             int64
	HostID                    int64
	Container                 string
	Trigger                   string
	Actor                     string
	AllowPreflightWarnings    bool
	SkipDataProtectionCapture bool
	PreviewProgress           func(percent int, stage string)
	PreviewCheck              func(check PreflightCheck)
}

type AutomationHoldView struct {
	Held      bool   `json:"held"`
	Source    string `json:"source"`
	Reason    string `json:"reason"`
	HeldAt    string `json:"held_at"`
	ChainID   int64  `json:"chain_id,omitempty"`
	ChainName string `json:"chain_name,omitempty"`
}

func deriveAutomationHold(cache db.Cache, policy db.Policy, chain *ChainManagementView, history []db.UpdateHistory) *AutomationHoldView {
	if !bool(cache.UpdateAvailable) || cacheHasSnoozedUpdate(cache) {
		return nil
	}
	if chain != nil && chain.PolicyMode == "auto" && chain.LastStatus == "blocked" {
		return &AutomationHoldView{Held: true, Source: "chain", Reason: "Automatic chain held by Preflight", HeldAt: chain.LastRunAt, ChainID: chain.ChainID, ChainName: chain.ChainName}
	}
	if policy.Mode != "auto" || len(history) == 0 {
		return nil
	}
	last := history[0]
	if last.Status != "skipped" || last.PreflightStatus != "blocked" || !strings.HasPrefix(last.Trigger, "automation:") {
		return nil
	}
	return &AutomationHoldView{Held: true, Source: "container", Reason: firstNonEmpty(last.Error, "Automatic update held by Preflight"), HeldAt: last.TS}
}

type DataProtectionSummaryView struct {
	Enabled    bool   `json:"enabled"`
	ScopeType  string `json:"scope_type"`
	ScopeKey   string `json:"scope_key"`
	MountCount int    `json:"mount_count"`
}

type ContainerView struct {
	dockercli.Container
	SystemManaged   bool                       `json:"system_managed"`
	SystemRole      string                     `json:"system_role,omitempty"`
	Policy          db.Policy                  `json:"policy"`
	Cache           db.Cache                   `json:"update"`
	Version         db.VersionInfo             `json:"version"`
	ConfigDrift     ConfigDriftView            `json:"config_drift"`
	Verification    db.VerificationState       `json:"verification"`
	DataProtection  *DataProtectionSummaryView `json:"data_protection,omitempty"`
	RestorePoint    *db.RestorePoint           `json:"restore_point,omitempty"`
	ChainManagement *ChainManagementView       `json:"chain_management,omitempty"`
	AutomationHold  *AutomationHoldView        `json:"automation_hold,omitempty"`
	RollbackSnoozed bool                       `json:"rollback_snoozed,omitempty"`
}

type hostInput struct {
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	Enabled        bool   `json:"enabled"`
	ConnectionType string `json:"connection_type"`
	TLSCA          string `json:"tls_ca"`
	TLSCert        string `json:"tls_cert"`
	TLSKey         string `json:"tls_key"`
}
type HostView struct {
	db.Host
	ConnectionType          string                `json:"connection_type"`
	TLSConfigured           bool                  `json:"tls_configured"`
	TLSManaged              bool                  `json:"tls_managed"`
	TLSExpiresAt            string                `json:"tls_expires_at,omitempty"`
	Worker                  dockercli.WorkerState `json:"worker"`
	DockerReachable         bool                  `json:"docker_reachable"`
	DockerReachabilityKnown bool                  `json:"docker_reachability_known"`
	DockerChecking          bool                  `json:"docker_checking"`
	DockerVersion           string                `json:"docker_version,omitempty"`
	DockerError             string                `json:"docker_error,omitempty"`
	DockerLastAttempt       string                `json:"docker_last_attempt,omitempty"`
	DockerLastSuccess       string                `json:"docker_last_success,omitempty"`
	DockerProbeDurationMS   int64                 `json:"docker_probe_duration_ms,omitempty"`
}

type hostHealthState struct {
	Known       bool
	Reachable   bool
	Checking    bool
	Version     string
	Error       string
	LastAttempt string
	LastSuccess string
	DurationMS  int64
}

type policyInput struct {
	Mode                   string `json:"mode"`
	CheckIntervalMinutes   int    `json:"check_interval_minutes"`
	ReleaseRepo            string `json:"release_repo"`
	AllowPreflightWarnings bool   `json:"allow_preflight_warnings"`
}
type automationInput struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Cron              string `json:"cron"`
	TargetType        string `json:"target_type"`
	TargetID          int64  `json:"target_id"`
	Kind              string `json:"kind"`
	CleanupImages     bool   `json:"cleanup_images"`
	CleanupNetworks   bool   `json:"cleanup_networks"`
	CleanupBuildCache bool   `json:"cleanup_build_cache"`
	CleanupVolumes    bool   `json:"cleanup_volumes"`
	Enabled           bool   `json:"enabled"`
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
	HostID                 int64  `json:"host_id"`
	Name                   string `json:"name"`
	IP                     string `json:"ip"`
	SSHPort                int    `json:"ssh_port"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	Mode                   string `json:"mode"`
	AcknowledgeInsecureTCP bool   `json:"acknowledge_insecure_tcp"`
	OperationID            string `json:"operation_id"`
}

type quickSetupProgress struct {
	OperationID string `json:"operation_id"`
	Stage       string `json:"stage"`
	Message     string `json:"message"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updated_at"`
}

func New(cfg Config, store *db.Store, docker *dockercli.Client, wt *watchtower.Client, gh *releases.GitHubClient, reg *registry.Client, po *notify.Pushover, ssh *sshsetup.Client, logger *slog.Logger, authm *auth.Manager) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{Cfg: cfg, Store: store, Docker: docker, WT: wt, Releases: gh, Registry: reg, Pushover: po, SSH: ssh, Logger: logger, Auth: authm, Events: dockercli.NewEventWatcher(), Queue: make(chan updateRequest, 200), ctx: ctx, cancel: cancel, infoRefresh: map[string]bool{}, ensureLocks: map[int64]*sync.Mutex{}, hostHealth: map[int64]hostHealthState{}, hostHealthRun: map[int64]bool{}, quickSetupOps: map[string]quickSetupProgress{}, chainPreflightOps: map[string]ChainPreflightProgress{}, chainReserved: map[string]int64{}, jobCancels: map[int64]context.CancelFunc{}, mutationGate: newMutationPipelineGate(maxConcurrentUpdatePipelines)}
}
func remoteDockerEndpoint(endpoint string) bool {
	v := strings.ToLower(strings.TrimSpace(endpoint))
	return v != "" && !strings.HasPrefix(v, "unix://")
}

func dockerOperationTimeout(endpoint string, local, remote time.Duration) time.Duration {
	if remoteDockerEndpoint(endpoint) {
		return remote
	}
	return local
}

func (a *App) hostHealthView(hostID int64) hostHealthState {
	a.hostHealthMu.Lock()
	defer a.hostHealthMu.Unlock()
	return a.hostHealth[hostID]
}

func (a *App) updateHostHealth(hostID int64, fn func(hostHealthState) hostHealthState) hostHealthState {
	a.hostHealthMu.Lock()
	defer a.hostHealthMu.Unlock()
	v := fn(a.hostHealth[hostID])
	a.hostHealth[hostID] = v
	return v
}

func hostHealthStale(v hostHealthState, now time.Time) bool {
	if !v.Known || strings.TrimSpace(v.LastAttempt) == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, v.LastAttempt)
	if err != nil {
		return true
	}
	// Reachable hosts are refreshed often enough to keep the UI honest without
	// turning /api/hosts into a stream of synchronous Docker round trips. Failed
	// hosts are retried sooner so a transient VPN hiccup recovers quickly.
	maxAge := 30 * time.Second
	if !v.Reachable {
		maxAge = 12 * time.Second
	}
	return now.Sub(t) >= maxAge
}

func (a *App) probeHost(ctx context.Context, h db.Host) (hostHealthState, error) {
	started := time.Now()
	now := started.UTC().Format(time.RFC3339Nano)
	a.updateHostHealth(h.ID, func(v hostHealthState) hostHealthState {
		v.Checking = true
		v.LastAttempt = now
		return v
	})
	version, err := a.Docker.PingReliable(ctx, h.Endpoint)
	duration := time.Since(started).Milliseconds()
	state := a.updateHostHealth(h.ID, func(v hostHealthState) hostHealthState {
		v.Known = true
		v.Checking = false
		v.LastAttempt = now
		v.DurationMS = duration
		if err != nil {
			v.Reachable = false
			v.Error = err.Error()
			return v
		}
		v.Reachable = true
		v.Version = version
		v.Error = ""
		v.LastSuccess = time.Now().UTC().Format(time.RFC3339Nano)
		return v
	})
	return state, err
}

func (a *App) scheduleHostProbe(h db.Host, force bool) {
	if !bool(h.Enabled) {
		return
	}
	a.hostHealthMu.Lock()
	state := a.hostHealth[h.ID]
	if a.hostHealthRun[h.ID] || (!force && !hostHealthStale(state, time.Now())) {
		a.hostHealthMu.Unlock()
		return
	}
	a.hostHealthRun[h.ID] = true
	a.hostHealthMu.Unlock()
	go func() {
		defer func() {
			a.hostHealthMu.Lock()
			delete(a.hostHealthRun, h.ID)
			a.hostHealthMu.Unlock()
		}()
		t := dockerOperationTimeout(h.Endpoint, 6*time.Second, 22*time.Second)
		ctx, cancel := context.WithTimeout(a.ctx, t)
		defer cancel()
		_, _ = a.probeHost(ctx, h)
	}()
}

func (a *App) recoverInterruptedJobs() {
	// First settle the narrow failure mode where an update transaction reached a
	// terminal state but the final jobs UPDATE failed. This is exactly the class
	// of stale "running" row that made a completed failure look hung in v1.0.8.
	if jobs, listErr := a.Store.Jobs(a.ctx, 5000); listErr == nil {
		for _, job := range jobs {
			if job.Status != "running" && job.Status != "cancel_requested" {
				continue
			}
			tx, txErr := a.Store.UpdateTransactionByJob(a.ctx, job.ID)
			if txErr != nil || !transactionTerminalState(tx.State) {
				continue
			}
			status, summary, errMsg := terminalJobStateForTransaction(tx)
			if finishErr := a.finishJobDurable(job.ID, status, summary, errMsg); finishErr == nil && a.Logger != nil {
				a.Logger.Warn("reconciled stale active job from terminal update transaction", "job_id", job.ID, "transaction_id", tx.ID, "status", status)
			}
		}
	}
	reason := "controller restarted or previous operation was interrupted"
	count, err := a.Store.FailActiveJobsWithoutTransaction(a.ctx, reason)
	if err != nil {
		a.Logger.Warn("could not recover interrupted jobs", "error", err)
	} else if count > 0 {
		a.Logger.Warn("recovered interrupted jobs", "count", count)
	}
	// Update chains are recovered after their child update transactions have been
	// reconciled. Marking them failed here would destroy the context required to
	// decide whether a started step completed, rolled back or needs attention.
}

func (a *App) Start() {
	a.reconcileDeveloperUpdates(context.Background())
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	keepDevID := ""
	if active, ok := devupdate.ActiveStatus(a.Cfg.DataDir); ok {
		keepDevID = active.ID
	}
	if err := a.Docker.CleanupOrphanedDevelopmentUpdaters(cleanupCtx, keepDevID); err != nil && a.Logger != nil {
		a.Logger.Warn("could not clean orphaned development updater helpers", "error", err)
	}
	cleanupCancel()
	a.recoverInterruptedJobs()
	if err := a.reloadRegistryCredentials(a.ctx); err != nil && a.Logger != nil {
		a.Logger.Warn("registry credentials could not be loaded", "error", err)
	}
	go a.updateWorker()
	go a.recoverInterruptedTransactions()
	go a.automationLoop()
	go a.workerSupervisorLoop()
	go a.systemMaintenanceLoop()
	hosts, _ := a.Store.Hosts(a.ctx)
	for _, h := range hosts {
		if h.Enabled {
			a.scheduleHostProbe(h, true)
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
	if _, err := a.probeHost(ctx, h); err != nil {
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
	protected.HandleFunc("GET /api/hosts/quick-setup/progress", a.handleHostQuickSetupProgress)
	protected.HandleFunc("/api/hosts/", a.handleHostSubroutes)
	protected.HandleFunc("GET /api/jobs", a.handleJobs)
	protected.HandleFunc("POST /api/jobs/{id}/cancel", a.handleJobCancel)
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
	protected.HandleFunc("GET /api/notifications/read-state", a.handleNotificationReadState)
	protected.HandleFunc("POST /api/notifications/read-state", a.handleNotificationReadState)
	protected.HandleFunc("POST /api/notifications/test", a.handleNotificationTest)
	protected.HandleFunc("GET /api/system/settings", a.handleSystemSettings)
	protected.HandleFunc("PUT /api/system/settings", a.handleSystemSettings)
	protected.HandleFunc("POST /api/system/worker-update", a.handleWorkerUpdate)
	protected.HandleFunc("POST /api/system/backup", a.handleSystemBackup)
	protected.HandleFunc("GET /api/system/backups", a.handleSystemBackups)
	protected.HandleFunc("POST /api/system/backups", a.handleSystemBackups)
	protected.HandleFunc("/api/system/backups/", a.handleSystemBackupSubroutes)
	protected.HandleFunc("GET /api/system/self-update", a.handleSelfUpdate)
	protected.HandleFunc("POST /api/system/self-update", a.handleSelfUpdate)
	protected.HandleFunc("GET /api/system/developer-update", a.handleDeveloperUpdateInfo)
	protected.HandleFunc("POST /api/system/developer-update/upload", a.handleDeveloperUpdateUpload)
	protected.HandleFunc("POST /api/system/developer-update/install", a.handleDeveloperUpdateInstall)
	protected.HandleFunc("POST /api/system/developer-update/cancel", a.handleDeveloperUpdateCancel)
	protected.HandleFunc("POST /api/system/developer-update/recover", a.handleDeveloperUpdateRecover)
	protected.HandleFunc("GET /api/system/developer-update/status", a.handleDeveloperUpdateStatus)
	protected.HandleFunc("GET /api/audit", a.handleAudit)
	protected.HandleFunc("GET /api/docker-events", a.handleDockerEvents)
	protected.HandleFunc("GET /api/logs/application", a.handleApplicationLogs)
	protected.HandleFunc("GET /api/logs/pushover", a.handlePushoverLogs)
	protected.HandleFunc("/api/export/", a.handleExport)
	protected.HandleFunc("GET /api/container-backups", a.handleContainerBackups)
	protected.HandleFunc("GET /api/container-backups/download-all", a.handleContainerBackupDownloadAll)
	protected.HandleFunc("POST /api/container-backups/snapshot", a.handleContainerBackupSnapshot)
	protected.HandleFunc("DELETE /api/container-backups/archived", a.handleContainerBackupArchivedDelete)
	protected.HandleFunc("GET /api/container-backups/download", a.handleContainerBackupDownload)
	protected.HandleFunc("GET /api/update-history", a.handleUpdateHistory)
	protected.HandleFunc("GET /api/update-chains", a.handleUpdateChains)
	protected.HandleFunc("POST /api/update-chains", a.handleUpdateChains)
	protected.HandleFunc("/api/update-chains/", a.handleUpdateChainSubroutes)
	protected.HandleFunc("GET /api/update-chain-runs", a.handleUpdateChainRuns)
	protected.HandleFunc("GET /api/restore-points", a.handleRestorePoints)
	protected.HandleFunc("GET /api/update-transactions", a.handleUpdateTransactions)
	protected.HandleFunc("GET /api/verification-history", a.handleVerificationHistory)
	protected.HandleFunc("GET /api/recovery/status", a.handleRecoveryStatus)
	protected.HandleFunc("POST /api/recovery/gc", a.handleRecoveryGC)
	protected.HandleFunc("POST /api/recovery/cleanup-unusable", a.handleRecoveryCleanupUnusable)
	protected.HandleFunc("POST /api/rollback", a.handleRollback)
	protected.HandleFunc("GET /api/service-icons/resolve", a.handleServiceIconResolve)
	protected.HandleFunc("GET /api/service-icons/catalog", a.handleServiceIconCatalog)
	protected.HandleFunc("GET /api/service-icons/file/{slug}", a.handleServiceIconFile)
	protected.HandleFunc("PUT /api/service-icons/override", a.handleServiceIconOverride)
	protected.HandleFunc("DELETE /api/service-icons/override", a.handleServiceIconOverride)
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
		for i, h := range filtered {
			if bool(h.Enabled) {
				a.scheduleHostProbe(h, false)
			}
			health := a.hostHealthView(h.ID)
			managedTLS, tlsExpiresAt := a.hostTLSInfo(h.Endpoint)
			views[i] = HostView{
				Host:                    h,
				ConnectionType:          dockercli.ConnectionType(h.Endpoint),
				TLSConfigured:           a.Docker.TLSConfigured(h.Endpoint),
				TLSManaged:              managedTLS,
				TLSExpiresAt:            tlsExpiresAt,
				Worker:                  a.Docker.WorkerState(r.Context(), h.ID),
				DockerReachable:         health.Reachable,
				DockerReachabilityKnown: health.Known,
				DockerChecking:          health.Checking,
				DockerVersion:           health.Version,
				DockerError:             health.Error,
				DockerLastAttempt:       health.LastAttempt,
				DockerLastSuccess:       health.LastSuccess,
				DockerProbeDurationMS:   health.DurationMS,
			}
		}
		writeJSON(w, 200, views)
		return
	}
	if !a.requireAdmin(w, r) {
		return
	}
	var in hostInput
	if json.NewDecoder(io.LimitReader(r.Body, 512*1024)).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "name and endpoint are required")
		return
	}
	endpoint, endpointErr := normalizeHostEndpoint(in.ConnectionType, in.Endpoint)
	if endpointErr != nil {
		writeErr(w, 400, endpointErr.Error())
		return
	}
	if dockercli.ConnectionType(endpoint) == "tls" && !a.isOwner(r) {
		writeErr(w, http.StatusForbidden, "TLS/mTLS Docker host credentials can only be configured by the owner")
		return
	}
	if dockercli.ConnectionType(endpoint) == "tls" {
		if err := validateTLSCredentials(in.TLSCA, in.TLSCert, in.TLSKey); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	token, tokenErr := randomToken(32)
	if tokenErr != nil {
		a.Logger.Error("failed to generate worker API token", "error", tokenErr)
		writeErr(w, 500, "failed to generate worker API token")
		return
	}
	id, err := a.Store.CreateHost(r.Context(), strings.TrimSpace(in.Name), endpoint, token, db.Bool(in.Enabled))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if dockercli.ConnectionType(endpoint) == "tls" {
		if err := a.saveHostTLSCredentials(endpoint, in.TLSCA, in.TLSCert, in.TLSKey); err != nil {
			_ = a.Store.DeleteHost(context.Background(), id)
			_ = a.removeHostTLSCredentials(endpoint)
			writeErr(w, 500, "TLS credentials could not be persisted: "+err.Error())
			return
		}
	}
	in.TLSCA, in.TLSCert, in.TLSKey = "", "", ""
	h, _ := a.Store.Host(r.Context(), id)
	if h.Enabled {
		a.Events.Start(a.ctx, a.Docker, a.Store, h)
		go a.startHostWorker(h, "host-created")
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "host.create", id, "", fmt.Sprintf("connection=%s endpoint=%s", dockercli.ConnectionType(endpoint), endpoint))
	writeJSON(w, 201, h)
}

func validQuickSetupOperationID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 96 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) setQuickSetupProgress(id, stage, message, status string) {
	id = strings.TrimSpace(id)
	if !validQuickSetupOperationID(id) {
		return
	}
	now := time.Now().UTC()
	a.quickSetupMu.Lock()
	defer a.quickSetupMu.Unlock()
	for key, item := range a.quickSetupOps {
		ts, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
		if err == nil && now.Sub(ts) > 20*time.Minute {
			delete(a.quickSetupOps, key)
		}
	}
	a.quickSetupOps[id] = quickSetupProgress{OperationID: id, Stage: stage, Message: message, Status: status, UpdatedAt: now.Format(time.RFC3339Nano)}
}

func (a *App) quickSetupProgress(id string) (quickSetupProgress, bool) {
	a.quickSetupMu.Lock()
	defer a.quickSetupMu.Unlock()
	v, ok := a.quickSetupOps[strings.TrimSpace(id)]
	return v, ok
}

func (a *App) handleHostQuickSetupProgress(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !validQuickSetupOperationID(id) {
		writeErr(w, 400, "invalid quick setup operation id")
		return
	}
	progress, ok := a.quickSetupProgress(id)
	if !ok {
		writeErr(w, 404, "quick setup operation not found")
		return
	}
	writeJSON(w, 200, progress)
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
	in.OperationID = strings.TrimSpace(in.OperationID)
	if in.OperationID != "" && !validQuickSetupOperationID(in.OperationID) {
		writeErr(w, 400, "invalid quick setup operation id")
		return
	}
	progressDone := false
	progress := func(stage, message string) { a.setQuickSetupProgress(in.OperationID, stage, message, "running") }
	if in.OperationID != "" {
		progress("prepare", "Validating Quick Setup request and target host.")
		defer func() {
			if progressDone {
				return
			}
			if current, ok := a.quickSetupProgress(in.OperationID); ok && current.Status == "running" {
				a.setQuickSetupProgress(in.OperationID, current.Stage, "Quick Setup stopped during this stage. See the error details below.", "failed")
			}
		}()
	}
	in.Name, in.IP, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.IP), strings.TrimSpace(in.Username)
	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.Mode == "" {
		in.Mode = "tcp" // backwards compatibility with the original quick-setup request
	}
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.Name == "" || in.IP == "" || in.Username == "" || in.Password == "" {
		writeErr(w, 400, "host name, IP, username and SSH password are required")
		return
	}
	if in.Mode != "secure" && in.Mode != "tcp" {
		writeErr(w, 400, "quick setup mode must be secure or tcp")
		return
	}
	if in.HostID > 0 && in.Mode != "secure" {
		writeErr(w, 400, "existing hosts can only be reconfigured through secure quick setup")
		return
	}
	if in.Mode == "tcp" && !in.AcknowledgeInsecureTCP {
		writeErr(w, 400, "confirm the Docker TCP 2375 security warning before running legacy TCP quick setup")
		return
	}
	var existingHost db.Host
	if in.HostID > 0 {
		var hostErr error
		existingHost, hostErr = a.Store.Host(r.Context(), in.HostID)
		if hostErr != nil {
			writeErr(w, 404, "host selected for secure reconfiguration was not found")
			return
		}
		if oldHost := dockerEndpointHost(existingHost.Endpoint); oldHost != "" && !strings.EqualFold(oldHost, in.IP) {
			writeErr(w, 409, "secure repair must use the existing Docker host IP; change the host connection explicitly before moving a managed certificate identity to another address")
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = existingHost.Name
		}
	}
	if existing, listErr := a.Store.Hosts(r.Context()); listErr == nil {
		for _, h := range existing {
			if h.ID == in.HostID {
				continue
			}
			otherIP := dockerEndpointHost(h.Endpoint)
			if otherIP == "" || !strings.EqualFold(otherIP, in.IP) {
				continue
			}
			if in.HostID == 0 && in.Mode == "secure" && dockercli.ConnectionType(h.Endpoint) == "tcp" {
				writeErr(w, http.StatusConflict, "this Docker host already exists as a legacy TCP connection; select that host and use Secure this host to upgrade it in place")
				return
			}
			writeErr(w, http.StatusConflict, "this Docker host IP already belongs to another Vibewatch host")
			return
		}
	}

	if in.Mode == "secure" {
		progress("remote", "Connecting over SSH, inspecting Docker configuration, staging certificates and restarting Docker safely.")
	} else {
		progress("remote", "Connecting over SSH, inspecting Docker configuration and staging the legacy TCP listener safely.")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	var (
		res        sshsetup.Result
		err        error
		managed    *managedMTLSBundle
		setupLabel string
	)
	if in.Mode == "secure" {
		bundle, genErr := generateManagedMTLS(in.IP, time.Now().UTC())
		if genErr != nil {
			writeErr(w, 500, "failed to generate secure connection credentials: "+genErr.Error())
			return
		}
		managed = &bundle
		res, err = a.SSH.ConfigureDockerMTLS(ctx, in.IP, in.Username, in.Password, in.SSHPort, bundle.CAPEM, bundle.ServerCertPEM, bundle.ServerKeyPEM)
		setupLabel = "secure"
	} else {
		res, err = a.SSH.ConfigureDockerTCP(ctx, in.IP, in.Username, in.Password, in.SSHPort)
		setupLabel = "legacy-tcp"
	}
	if err != nil {
		in.Password = ""
		a.Logger.Warn("SSH Docker quick setup failed", "actor", a.actor(r), "mode", setupLabel, "ip", in.IP, "username", in.Username, "error", err)
		writeErr(w, 502, err.Error())
		return
	}

	progress("verify", "Remote Docker setup passed local validation. Verifying the new endpoint from the Vibewatch controller.")

	rollbackMode := in.Mode
	if rollbackMode != "secure" {
		rollbackMode = "tcp"
	}
	rollbackRemote := func(reason string) (string, error) {
		if strings.TrimSpace(res.TransactionToken) == "" {
			return "no staged remote transaction", nil
		}
		rbCtx, rbCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer rbCancel()
		out, rbErr := a.SSH.RollbackDockerSetup(rbCtx, in.IP, in.Username, in.Password, in.SSHPort, res.TransactionToken, rollbackMode)
		if rbErr != nil {
			a.Logger.Error("SSH Docker quick setup rollback failed", "actor", a.actor(r), "mode", setupLabel, "ip", in.IP, "reason", reason, "error", rbErr, "output", out)
		} else {
			a.Logger.Info("SSH Docker quick setup rollback completed", "actor", a.actor(r), "mode", setupLabel, "ip", in.IP, "reason", reason, "output", out)
		}
		return out, rbErr
	}
	remoteDiagnostics := func() string {
		diagCtx, diagCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer diagCancel()
		out, diagErr := a.SSH.DockerSetupDiagnostics(diagCtx, in.IP, in.Username, in.Password, in.SSHPort)
		if diagErr != nil {
			return "diagnostics unavailable: " + diagErr.Error()
		}
		return out
	}

	trustedCA := ""
	pingCtx, pingCancel := context.WithTimeout(r.Context(), 30*time.Second)
	var version string
	var pingErr error
	if managed != nil {
		trustedCA = managed.CAPEM
		if strings.TrimSpace(res.CAPEM) != "" {
			trustedCA = res.CAPEM
		}
		// Probe with temporary credentials first. Existing managed client files are
		// intentionally left untouched until the new identity has proved it can
		// complete an mTLS handshake from this controller.
		version, pingErr = a.Docker.PingTLSCredentials(pingCtx, res.Endpoint, trustedCA, managed.ClientCertPEM, managed.ClientKeyPEM)
	} else {
		version, pingErr = a.Docker.PingReliable(pingCtx, res.Endpoint)
	}
	pingCancel()
	if pingErr != nil {
		diagnostics := remoteDiagnostics()
		rollbackOut, rollbackErr := rollbackRemote("controller endpoint validation failed")
		in.Password = ""
		a.Logger.Warn("SSH Docker quick setup endpoint validation failed", "actor", a.actor(r), "mode", setupLabel, "endpoint", res.Endpoint, "error", pingErr, "remote_diagnostics", diagnostics, "rollback_output", rollbackOut, "rollback_error", rollbackErr)
		errText := "Docker accepted the staged configuration, but Vibewatch could not reach " + res.Endpoint + ": " + pingErr.Error()
		if rollbackErr == nil {
			errText += ". The previous Docker configuration was restored automatically."
		} else {
			errText += ". Automatic rollback also failed; check the Docker host immediately."
		}
		writeJSON(w, 502, map[string]any{
			"configured": false, "rolled_back": rollbackErr == nil, "host_created": false, "endpoint": res.Endpoint,
			"secure": in.Mode == "secure", "error": errText,
		})
		return
	}

	progress("integrate", "Docker endpoint verified. Saving the connection, credentials and host integration in Vibewatch.")

	var id int64
	if in.HostID > 0 {
		id = in.HostID
		a.Events.Stop(id)
		_ = a.Docker.RemoveWorker(context.Background(), id)
		if updateErr := a.Store.UpdateHostEndpoint(r.Context(), id, res.Endpoint); updateErr != nil {
			rollbackOut, rollbackErr := rollbackRemote("existing host endpoint persistence failed")
			in.Password = ""
			a.Events.Start(a.ctx, a.Docker, a.Store, existingHost)
			go a.startHostWorker(existingHost, "ssh-quick-setup-rollback")
			a.Logger.Error("secure Docker reachable but existing host endpoint update failed", "host_id", id, "error", updateErr, "rollback_output", rollbackOut, "rollback_error", rollbackErr)
			writeErr(w, 500, "secure Docker is reachable, but Vibewatch could not update the existing host connection: "+updateErr.Error())
			return
		}
	} else {
		token, tokenErr := randomToken(32)
		if tokenErr != nil {
			_, _ = rollbackRemote("worker token generation failed")
			in.Password = ""
			writeErr(w, 500, "failed to generate worker API token")
			return
		}
		var createErr error
		id, createErr = a.Store.CreateHost(r.Context(), in.Name, res.Endpoint, token, db.Bool(true))
		if createErr != nil {
			rollbackOut, rollbackErr := rollbackRemote("host creation failed")
			in.Password = ""
			a.Logger.Error("Docker endpoint reachable but host creation failed", "endpoint", res.Endpoint, "error", createErr, "rollback_output", rollbackOut, "rollback_error", rollbackErr)
			writeErr(w, 400, "Docker endpoint is reachable, but the Vibewatch host could not be created: "+createErr.Error())
			return
		}
	}

	if managed != nil {
		if saveErr := a.saveManagedHostTLSCredentialsAtomic(res.Endpoint, trustedCA, managed.ClientCertPEM, managed.ClientKeyPEM, managed.CAKeyPEM); saveErr != nil {
			if in.HostID > 0 {
				_ = a.Store.UpdateHostEndpoint(r.Context(), id, existingHost.Endpoint)
				a.Events.Start(a.ctx, a.Docker, a.Store, existingHost)
				go a.startHostWorker(existingHost, "ssh-quick-setup-rollback")
			} else {
				_ = a.Store.DeleteHost(r.Context(), id)
			}
			if in.HostID == 0 || !strings.EqualFold(strings.TrimSpace(existingHost.Endpoint), strings.TrimSpace(res.Endpoint)) {
				_ = a.removeHostTLSCredentials(res.Endpoint)
			}
			rollbackOut, rollbackErr := rollbackRemote("managed client credential persistence failed")
			in.Password = ""
			a.Logger.Error("secure Docker reachable but managed client credential persistence failed", "host_id", id, "error", saveErr, "rollback_output", rollbackOut, "rollback_error", rollbackErr)
			writeErr(w, 500, "secure Docker was reachable, but Vibewatch could not atomically persist its managed client identity; the host change was rolled back: "+saveErr.Error())
			return
		}
	}

	if in.HostID > 0 {
		if dockercli.ConnectionType(existingHost.Endpoint) == "tls" && !strings.EqualFold(strings.TrimSpace(existingHost.Endpoint), strings.TrimSpace(res.Endpoint)) {
			_ = a.removeHostTLSCredentials(existingHost.Endpoint)
		}
		if strings.TrimSpace(in.Name) != "" && strings.TrimSpace(in.Name) != existingHost.Name {
			_ = a.Store.RenameHost(r.Context(), id, strings.TrimSpace(in.Name))
		}
	}

	h, hostErr := a.Store.Host(r.Context(), id)
	if hostErr != nil {
		// The endpoint + credentials are already consistent at this point. Do not
		// risk rolling a healthy remote daemon back because a post-write DB read
		// unexpectedly failed; surface the storage error and leave the transaction
		// backup available for a later repair.
		in.Password = ""
		writeErr(w, 500, "host was updated but could not be read back: "+hostErr.Error())
		return
	}

	progress("finalize", "Vibewatch integration saved. Finalizing the remote transaction and starting the host worker.")

	commitCtx, commitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	commitOut, commitErr := a.SSH.CommitDockerSetup(commitCtx, in.IP, in.Username, in.Password, in.SSHPort, res.TransactionToken)
	commitCancel()
	if commitErr != nil {
		// A commit failure only leaves the remote rollback snapshot behind; the
		// active Docker endpoint and Vibewatch state are already mutually valid.
		a.Logger.Warn("SSH Docker quick setup could not remove remote rollback snapshot", "host_id", id, "mode", setupLabel, "error", commitErr, "output", commitOut)
	}
	in.Password = ""

	a.Events.Start(a.ctx, a.Docker, a.Store, h)
	go a.startHostWorker(h, "ssh-quick-setup-"+setupLabel)
	action := "host.quick-setup"
	if in.HostID > 0 {
		action = "host.connection.secure"
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), action, id, "", fmt.Sprintf("mode=%s endpoint=%s ssh_user=%s", setupLabel, res.Endpoint, res.Username))
	a.Logger.Info("SSH Docker quick setup completed", "actor", a.actor(r), "mode", setupLabel, "host_id", id, "endpoint", res.Endpoint, "docker_version", version)
	response := map[string]any{"configured": true, "host_created": in.HostID == 0, "host_reconfigured": in.HostID > 0, "host": h, "endpoint": res.Endpoint, "docker_version": version, "secure": in.Mode == "secure"}
	if managed != nil {
		response["certificate_expires_at"] = managed.ClientNotAfter.UTC().Format(time.RFC3339)
	}
	a.setQuickSetupProgress(in.OperationID, "complete", "Quick Setup completed successfully. The Docker host is ready in Vibewatch.", "completed")
	progressDone = true
	writeJSON(w, 201, response)
}

func (a *App) systemManagedContainer(name string) (bool, string) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	controller := strings.TrimSpace(strings.TrimPrefix(a.Cfg.ControllerName, "/"))
	if name != "" && (name == controller || name == "watchtower-ui" || name == "vibewatch") {
		return true, "controller"
	}
	for _, prefix := range []string{"watchtower-ui-worker-", "vibewatch-worker-", "vibewatch-helper-", "vibewatch-dev-updater-"} {
		if strings.HasPrefix(name, prefix) {
			if prefix == "vibewatch-helper-" {
				return true, "helper"
			}
			if prefix == "vibewatch-dev-updater-" {
				return true, "maintenance"
			}
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
		hostBeforeDelete, _ := a.Store.Host(r.Context(), id)
		a.Events.Stop(id)
		_ = a.Docker.RemoveWorker(context.Background(), id)
		if err := a.Store.DeleteHost(r.Context(), id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if dockercli.ConnectionType(hostBeforeDelete.Endpoint) == "tls" {
			_ = a.removeHostTLSCredentials(hostBeforeDelete.Endpoint)
		}
		_ = a.Store.Audit(r.Context(), a.actor(r), "host.delete", id, "", "")
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if len(parts) == 2 && parts[1] == "tls" && r.Method == http.MethodPut {
		if !a.requireOwner(w, r) {
			return
		}
		h, e := a.Store.Host(r.Context(), id)
		if e != nil {
			writeErr(w, 404, "host not found")
			return
		}
		if dockercli.ConnectionType(h.Endpoint) != "tls" {
			writeErr(w, 409, "host is not configured for TLS/mTLS")
			return
		}
		var in struct {
			TLSCA   string `json:"tls_ca"`
			TLSCert string `json:"tls_cert"`
			TLSKey  string `json:"tls_key"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 512*1024)).Decode(&in) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		if err := a.saveHostTLSCredentials(h.Endpoint, in.TLSCA, in.TLSCert, in.TLSKey); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		_ = a.Docker.RemoveWorker(context.Background(), id)
		go a.startHostWorker(h, "tls-credentials-rotated")
		a.scheduleHostProbe(h, true)
		_ = a.Store.Audit(r.Context(), a.actor(r), "host.tls.rotate", id, "", "TLS/mTLS client credentials replaced")
		writeJSON(w, 200, map[string]any{"ok": true, "tls_configured": true})
		return
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		h, e := a.Store.Host(r.Context(), id)
		if e != nil {
			writeErr(w, 404, "host not found")
			return
		}
		testTimeout := dockerOperationTimeout(h.Endpoint, 45*time.Second, 3*time.Minute)
		testCtx, testCancel := context.WithTimeout(r.Context(), testTimeout)
		defer testCancel()
		health, e := a.probeHost(testCtx, h)
		if e != nil {
			writeErr(w, 502, e.Error())
			return
		}
		v := health.Version
		base, workerErr := a.ensureWorker(testCtx, h)
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
	if len(parts) == 2 && parts[1] == "storage" && r.Method == http.MethodGet {
		a.handleHostStorage(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "data-protection" && (r.Method == http.MethodGet || r.Method == http.MethodPut) {
		a.handleDataProtection(w, r, id)
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
	if len(parts) == 2 && parts[1] == "why-update" && r.Method == http.MethodGet {
		a.handleWhyUpdate(w, r, id)
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
		if cm, managedByChain := a.chainForMember(r.Context(), id, container); managedByChain {
			if cm.PolicyMode == "ignore" {
				writeErr(w, 409, fmt.Sprintf("stack %s is Excluded by update chain %s", cm.ScopeKey, cm.ChainName))
				return
			}
		} else if p.Mode == "ignore" {
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
	if len(parts) == 2 && parts[1] == "preflight" && r.Method == http.MethodPost {
		a.handleUpdatePreflight(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "verification" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodDelete) {
		a.handleVerification(w, r, id)
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
			cache = clearUpdateSnooze(cache, time.Now().UTC().Format(time.RFC3339))
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
		if cm, chainManaged := a.chainForMember(r.Context(), id, container); chainManaged {
			current, _ := a.Store.Policy(r.Context(), id, container)
			if in.Mode != current.Mode {
				writeErr(w, 409, fmt.Sprintf("policy is managed by update chain %s for stack %s", cm.ChainName, cm.ScopeKey))
				return
			}
		}
		interval := in.CheckIntervalMinutes
		if interval < 1 {
			// Web UI V2 no longer exposes per-container polling cadence; Automations own
			// scheduling. Preserve a legacy value when an older database/client still has one.
			if current, err := a.Store.Policy(r.Context(), id, container); err == nil && current.CheckIntervalMinutes > 0 {
				interval = current.CheckIntervalMinutes
			} else {
				interval = 30
			}
		}
		p := db.Policy{HostID: id, ContainerName: container, Mode: in.Mode, CheckIntervalMinutes: interval, ReleaseRepo: in.ReleaseRepo, AllowPreflightWarnings: db.Bool(in.AllowPreflightWarnings)}
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
	ctx, cancel := context.WithTimeout(r.Context(), dockerOperationTimeout(h.Endpoint, 35*time.Second, 90*time.Second))
	defer cancel()
	// Collect the image inventory once. The summary endpoint previously caused
	// HostOverview to inventory images and then repeated that inventory solely
	// to calculate rollback-protection counts.
	overview, err := a.Docker.HostOverview(ctx, h.Endpoint, true)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	protectedImages, _, _ := a.rollbackProtectedDockerObjects(hostID)
	a.protectDataHelperImage(ctx, h.Endpoint, protectedImages)
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
	if !includeImages {
		overview.Images = nil
	}
	writeJSON(w, 200, overview)
}

func cleanupJobTimeout(endpoint string) time.Duration {
	return dockerOperationTimeout(endpoint, 10*time.Minute, 30*time.Minute)
}

func (a *App) cleanupProgressCallback(jobID int64) dockercli.CleanupProgress {
	return func(completed, total int, stage string) {
		pct := 10
		lower := strings.ToLower(strings.TrimSpace(stage))
		switch {
		case strings.HasPrefix(lower, "removing"):
			pct = 20
			if total > 0 {
				pct += int(float64(completed) / float64(total) * 65)
			}
		case strings.HasPrefix(lower, "pruning"):
			pct = 45
		case strings.HasPrefix(lower, "refreshing"):
			pct = 90
		case strings.HasPrefix(lower, "measuring"), strings.HasPrefix(lower, "inventorying"):
			pct = 12
		}
		a.jobProgress(context.Background(), jobID, pct, stage)
	}
}

// respondCleanupJob keeps the cleanup API backward compatible. The dashboard
// opts into async=1 and receives the job immediately. Older callers may still
// wait for the historical {job_id,result} response; a disconnected HTTP client
// no longer cancels the Docker cleanup itself.
func (a *App) respondCleanupJob(w http.ResponseWriter, r *http.Request, jobID int64) {
	if r.URL.Query().Get("async") == "1" || strings.EqualFold(r.URL.Query().Get("async"), "true") {
		writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "status": "queued"})
		return
	}
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			// The background cleanup keeps running even when the legacy waiting
			// HTTP request has been closed by a browser or reverse proxy.
			return
		default:
		}
		job, err := a.Store.Job(r.Context(), jobID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		switch job.Status {
		case "success":
			var result any = map[string]any{}
			if strings.TrimSpace(job.SummaryJSON) != "" {
				_ = json.Unmarshal([]byte(job.SummaryJSON), &result)
			}
			writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "result": result})
			return
		case "failed":
			writeErr(w, http.StatusBadGateway, firstNonEmpty(job.Error, "cleanup failed"))
			return
		case "cancelled":
			writeErr(w, http.StatusConflict, "cleanup cancelled")
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) runImageCleanup(jobID int64, h db.Host, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupJobTimeout(h.Endpoint))
	defer cancel()
	if !a.beginAsyncJob(ctx, jobID) {
		return
	}
	a.jobProgress(context.Background(), jobID, 5, "Acquiring host cleanup lease")
	releaseLease, leaseErr := a.holdHostOperationLease(ctx, jobID, h.ID, "image-cleanup")
	if leaseErr != nil {
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", leaseErr.Error())
		return
	}
	defer releaseLease()
	_ = a.Store.AddJobLog(context.Background(), jobID, "info", "docker", "Pruning images not referenced by any container")
	protected, _, _ := a.rollbackProtectedDockerObjects(h.ID)
	a.protectDataHelperImage(ctx, h.Endpoint, protected)
	result, err := a.Docker.PruneUnusedImagesWithProgress(ctx, h.Endpoint, protected, a.cleanupProgressCallback(jobID))
	if err != nil {
		a.jobProgress(context.Background(), jobID, 100, "Image cleanup failed")
		_ = a.Store.AddJobLog(context.Background(), jobID, "error", "docker", err.Error())
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		a.Logger.Error("unused image cleanup failed", "host", h.Name, "host_id", h.ID, "job_id", jobID, "error", err)
		return
	}
	summary, _ := json.Marshal(result)
	_ = a.Store.AddJobLog(context.Background(), jobID, "info", "docker", fmt.Sprintf("Removed %d unused images; %d rollback-protected; %d failed; reclaimed %d bytes", result.RemovedImages, result.ProtectedImages, result.FailedImages, result.ReclaimedBytes))
	a.jobProgress(context.Background(), jobID, 100, "Image cleanup completed")
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), actor, "images.prune", h.ID, "", string(summary))
	a.Logger.Info("unused image cleanup completed", "host", h.Name, "host_id", h.ID, "job_id", jobID, "removed_images", result.RemovedImages, "reclaimed_bytes", result.ReclaimedBytes)
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
	a.jobProgress(context.Background(), jobID, 2, "Cleanup queued")
	actor := a.actor(r)
	go a.runImageCleanup(jobID, h, actor)
	a.respondCleanupJob(w, r, jobID)
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
	ctx, cancel := context.WithTimeout(r.Context(), dockerOperationTimeout(h.Endpoint, 45*time.Second, 90*time.Second))
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

func (a *App) runNetworkCleanup(jobID int64, h db.Host, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupJobTimeout(h.Endpoint))
	defer cancel()
	if !a.beginAsyncJob(ctx, jobID) {
		return
	}
	a.jobProgress(context.Background(), jobID, 5, "Acquiring host cleanup lease")
	releaseLease, leaseErr := a.holdHostOperationLease(ctx, jobID, h.ID, "network-cleanup")
	if leaseErr != nil {
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", leaseErr.Error())
		return
	}
	defer releaseLease()
	_, protected, _ := a.rollbackProtectedDockerObjects(h.ID)
	result, err := a.Docker.PruneUnusedNetworksWithProgress(ctx, h.Endpoint, protected, a.cleanupProgressCallback(jobID))
	if err != nil {
		a.jobProgress(context.Background(), jobID, 100, "Network cleanup failed")
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	a.jobProgress(context.Background(), jobID, 100, "Network cleanup completed")
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), actor, "networks.prune", h.ID, "", string(summary))
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
	a.jobProgress(context.Background(), jobID, 2, "Cleanup queued")
	actor := a.actor(r)
	go a.runNetworkCleanup(jobID, h, actor)
	a.respondCleanupJob(w, r, jobID)
}

func (a *App) runBuildCacheCleanup(jobID int64, h db.Host, actor string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupJobTimeout(h.Endpoint))
	defer cancel()
	if !a.beginAsyncJob(ctx, jobID) {
		return
	}
	a.jobProgress(context.Background(), jobID, 5, "Acquiring host cleanup lease")
	releaseLease, leaseErr := a.holdHostOperationLease(ctx, jobID, h.ID, "build-cache-cleanup")
	if leaseErr != nil {
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", leaseErr.Error())
		return
	}
	defer releaseLease()
	result, err := a.Docker.PruneBuildCacheWithProgress(ctx, h.Endpoint, a.cleanupProgressCallback(jobID))
	if err != nil {
		a.jobProgress(context.Background(), jobID, 100, "Build cache cleanup failed")
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	a.jobProgress(context.Background(), jobID, 100, "Build cache cleanup completed")
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), actor, "build-cache.prune", h.ID, "", string(summary))
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
	a.jobProgress(context.Background(), jobID, 2, "Cleanup queued")
	actor := a.actor(r)
	go a.runBuildCacheCleanup(jobID, h, actor)
	a.respondCleanupJob(w, r, jobID)
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

	item := watchtower.CheckItem{Name: c.Name, Image: c.Image, ImageID: c.ImageID}
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
		releaseContinuityRead := a.acquireContinuityRead(ctx)
		remote, remoteErr := a.Registry.RemoteStateForPlatform(ctx, c.Image, platform)
		releaseContinuityRead()
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

	res := watchtower.CheckResponse{Containers: []watchtower.CheckItem{item}, Count: 1}
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
	// Bound a slow VPN/remote Docker endpoint without discarding already cached
	// browser data. Request cancellation also terminates the Docker CLI process.
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerOperationTimeout(h.Endpoint, 25*time.Second, 75*time.Second))
	defer cancel()
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	latestRestorePoints, _ := a.Store.LatestRestorePointsForHost(ctx, hostID)
	stackManagement, _ := a.stackChainManagement(ctx, hostID)
	chainMemberManagement, _ := a.chainMemberManagement(ctx, hostID)

	// Inspect image labels/platforms in batches. Previously first-load rendering
	// could issue an image-inspect round-trip for every container, which becomes
	// very visible across higher-latency VPN links.
	imageRefs := make([]string, 0, len(cs))
	for _, c := range cs {
		ref := strings.TrimSpace(c.ImageID)
		if ref == "" {
			ref = strings.TrimSpace(c.Image)
		}
		if ref != "" {
			imageRefs = append(imageRefs, ref)
		}
	}
	imageMeta, metaErr := a.Docker.ImageMetadataBatch(ctx, h.Endpoint, imageRefs...)
	if metaErr != nil {
		a.Logger.Debug("container image metadata batch unavailable", "host_id", hostID, "error", metaErr)
		imageMeta = map[string]dockercli.ImageMetadata{}
	}

	dataProfiles, _ := a.Store.DataProtectionProfiles(ctx, hostID)
	serviceDataProtection := map[string]DataProtectionSummaryView{}
	stackDataProtection := map[string]DataProtectionSummaryView{}
	for _, dp := range dataProfiles {
		if !bool(dp.Enabled) {
			continue
		}
		count := len(selectedMountKeys(dp.MountsJSON))
		if count == 0 {
			continue
		}
		summary := DataProtectionSummaryView{Enabled: true, ScopeType: dp.ScopeType, ScopeKey: dp.ScopeKey, MountCount: count}
		if dp.ScopeType == "stack" {
			stackDataProtection[dp.ScopeKey] = summary
		} else {
			serviceDataProtection[dp.ScopeKey] = summary
		}
	}

	views := make([]ContainerView, 0, len(cs))
	for _, c := range cs {
		p, _ := a.Store.Policy(ctx, hostID, c.Name)
		cache, _ := a.Store.Cache(ctx, hostID, c.Name)
		v, _ := a.Store.Version(ctx, hostID, c.Name)
		managed, role := a.systemManagedContainer(c.Name)
		labelRef := strings.TrimSpace(c.ImageID)
		if labelRef == "" {
			labelRef = strings.TrimSpace(c.Image)
		}
		if meta, ok := imageMeta[labelRef]; ok {
			installed, source := dockercli.InstalledVersion(c.Image, meta.Labels)
			if installed != "" {
				v.Installed = installed
				v.InstalledSource = source
			}
			repo := strings.TrimSpace(p.ReleaseRepo)
			if repo == "" {
				repo, _ = releases.DetectFromLabels(meta.Labels)
			}
			if repo == "" {
				repo, _ = releases.FallbackFromImage(c.Image)
			}
			if repo != "" {
				v.ReleaseRepo = repo
			}
			_ = a.Store.SaveVersion(ctx, v)
			if versionStale(v.CheckedAt) {
				platform := registry.Platform{OS: meta.OS, Architecture: meta.Architecture, Variant: meta.Variant}
				go a.refreshVersion(context.Background(), hostID, c.Name, c.Image, repo, v.Installed, v.InstalledSource, platform)
			}
		} else if versionStale(v.CheckedAt) {
			// Metadata refresh is asynchronous; do not turn a container-page request
			// into a sequence of remote Docker calls when batch metadata was missing.
			go a.refreshVersion(context.Background(), hostID, c.Name, c.Image, strings.TrimSpace(p.ReleaseRepo), v.Installed, v.InstalledSource)
		}
		effectiveMode := p.Mode
		if c.StackName != "" {
			if cm, ok := stackManagement[c.StackName]; ok {
				effectiveMode = cm.PolicyMode
			} else if cm, ok := chainMemberManagement[c.Name]; ok {
				effectiveMode = cm.PolicyMode
			}
		} else if cm, ok := chainMemberManagement[c.Name]; ok {
			effectiveMode = cm.PolicyMode
		}
		if !managed && effectiveMode == "ignore" && imageStateStale(cache.LastCheckedAt, cache.LastError) {
			go a.refreshExcludedImageState(hostID, c)
		}
		driftDB, _ := a.Store.ConfigDrift(ctx, hostID, c.Name)
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
		verification, _ := a.Store.VerificationState(ctx, hostID, c.Name)
		if !managed {
			profile, _ := a.effectiveVerificationProfile(ctx, hostID, c.Name)
			verification = a.effectiveVerificationState(ctx, hostID, c.Name, profile)
		}
		var dataProtection *DataProtectionSummaryView
		if !managed {
			if dp, ok := serviceDataProtection[c.Name]; ok {
				copy := dp
				dataProtection = &copy
			} else if c.StackName != "" {
				if dp, ok := stackDataProtection[c.StackName]; ok {
					copy := dp
					dataProtection = &copy
				}
			}
			if dataProtection == nil {
				scopeType, scopeKey := "service", c.Name
				if c.StackName != "" && !strings.EqualFold(c.StackType, "swarm") {
					scopeType, scopeKey = "stack", c.StackName
				}
				dataProtection = &DataProtectionSummaryView{Enabled: false, ScopeType: scopeType, ScopeKey: scopeKey, MountCount: 0}
			}
		}
		var chainManagement *ChainManagementView
		if c.StackName != "" {
			if cm, ok := stackManagement[c.StackName]; ok {
				copy := cm
				chainManagement = &copy
			}
		}
		// A stack chain owns the policy of every chain member. Fall back to
		// explicit step membership as well so the UI stays read-only even when
		// Docker stack metadata is temporarily incomplete or stale.
		if chainManagement == nil {
			if cm, ok := chainMemberManagement[c.Name]; ok {
				copy := cm
				chainManagement = &copy
			}
		}
		var holdHistory []db.UpdateHistory
		if !managed && chainManagement == nil && p.Mode == "auto" && bool(cache.UpdateAvailable) && !cacheHasSnoozedUpdate(cache) {
			holdHistory, _ = a.Store.UpdateHistory(ctx, 1, hostID, c.Name)
		}
		var automationHold *AutomationHoldView
		rollbackSnoozed := false
		if !managed {
			automationHold = deriveAutomationHold(cache, p, chainManagement, holdHistory)
			if strings.TrimSpace(cache.SnoozedDigest) != "" {
				recentHistory, _ := a.Store.UpdateHistory(ctx, 6, hostID, c.Name)
				rollbackSnoozed = rollbackSnoozedFromHistory(cache, recentHistory)
			}
		}
		views = append(views, ContainerView{Container: c, SystemManaged: managed, SystemRole: role, Policy: p, Cache: cache, Version: v, ConfigDrift: drift, Verification: verification, DataProtection: dataProtection, RestorePoint: restorePoint, ChainManagement: chainManagement, AutomationHold: automationHold, RollbackSnoozed: rollbackSnoozed})
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
func updateVersionParts(v string) ([3]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V"))
	if v == "" {
		return [3]int{}, false
	}
	// Accept common semver-ish tags such as 1.2.3, 1.2.3-r1 or 1.2.
	// Classification is informational only; digest comparison stays authoritative.
	parts := strings.SplitN(v, ".", 4)
	if len(parts) < 2 {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			out[i] = 0
			continue
		}
		part := parts[i]
		end := 0
		for end < len(part) && part[end] >= '0' && part[end] <= '9' {
			end++
		}
		if end == 0 {
			return [3]int{}, false
		}
		n, err := strconv.Atoi(part[:end])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func classifyVersionChange(installed, latest string) string {
	oldV, okOld := updateVersionParts(installed)
	newV, okNew := updateVersionParts(latest)
	if !okOld || !okNew {
		return "unknown"
	}
	if oldV[0] != newV[0] {
		return "major"
	}
	if oldV[1] != newV[1] {
		return "minor"
	}
	if oldV[2] != newV[2] {
		return "patch"
	}
	return "image"
}

func explicitSecurityRelease(name, body string) bool {
	text := strings.ToLower(strings.TrimSpace(name + "\n" + body))
	for _, marker := range []string{"cve-", "security update", "security release", "security fix", "security fixes", "security vulnerability", "vulnerability fix", "vulnerability fixes"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func sameReleaseVersion(a, b string) bool {
	norm := func(v string) string { return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")) }
	return norm(a) != "" && norm(a) == norm(b)
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
	v.SecurityUpdate = db.Bool(false)
	v.UpdateKind = ""

	registryResolved := false
	// The registry image that Watchtower actually follows is authoritative for
	// the readable target version when it publishes OCI version metadata.
	if a.Registry != nil && strings.TrimSpace(image) != "" {
		var rv registry.Version
		var err error
		releaseContinuityRead := a.acquireContinuityRead(ctx)
		if len(platforms) > 0 && strings.TrimSpace(platforms[0].Architecture) != "" {
			rv, err = a.Registry.RemoteVersionForPlatform(ctx, image, platforms[0])
		} else {
			rv, err = a.Registry.RemoteVersion(ctx, image)
		}
		releaseContinuityRead()
		if err == nil && rv.Version != "" {
			v.Latest = strings.TrimPrefix(strings.TrimSpace(rv.Version), "v")
			v.LatestSource = rv.Source
			registryResolved = true
		} else if err != nil {
			v.Error = err.Error()
		}
	}

	// GitHub release metadata remains a useful fallback and is also the only
	// source from which Vibewatch labels an update as Security. We deliberately
	// require explicit release wording/CVE metadata; a patch version alone is
	// never presented as a security update.
	if strings.TrimSpace(repo) != "" {
		rel, err := a.Releases.Latest(ctx, repo)
		if err == nil {
			if !registryResolved {
				v.Latest = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
				v.LatestSource = "github-release"
			}
			if sameReleaseVersion(rel.TagName, v.Latest) {
				v.PublishedAt = rel.PublishedAt
				v.SecurityUpdate = db.Bool(explicitSecurityRelease(rel.Name, rel.Body))
			}
			v.Error = ""
		} else if !registryResolved && v.Error == "" {
			v.Error = err.Error()
		}
	}
	v.UpdateKind = classifyVersionChange(v.Installed, v.Latest)
	_ = a.Store.SaveVersion(context.Background(), v)
}

func (a *App) ensureWorker(ctx context.Context, h db.Host) (string, error) {
	// Worker readiness is serialized per host, not globally. A slow VPN host
	// must never block checks or updates on unrelated LAN hosts.
	a.ensureMu.Lock()
	lock := a.ensureLocks[h.ID]
	if lock == nil {
		lock = &sync.Mutex{}
		a.ensureLocks[h.ID] = lock
	}
	a.ensureMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	base, err := a.Docker.EnsureWorker(ctx, h)
	if err != nil {
		return "", err
	}
	readyTimeout := dockerOperationTimeout(h.Endpoint, 60*time.Second, 4*time.Minute)
	if err := a.WT.WaitReadyFor(ctx, base, readyTimeout); err != nil {
		state := a.Docker.WorkerState(context.Background(), h.ID)
		logs, _ := a.Docker.WorkerLogsRecent(context.Background(), h.ID, 80)
		if logs != "" {
			return "", fmt.Errorf("%w (worker status=%s exit_code=%d restarting=%t): %s", err, state.Status, state.ExitCode, state.Restarting, logs)
		}
		return "", fmt.Errorf("%w (worker status=%s exit_code=%d restarting=%t)", err, state.Status, state.ExitCode, state.Restarting)
	}
	return base, nil
}

// beginAsyncJob safely starts a queued asynchronous job. Jobs created directly
// in the running state (for example internal post-rollback checks) are allowed
// through unchanged. A queued job cancelled by the user is never resurrected.
func (a *App) beginAsyncJob(ctx context.Context, jobID int64) bool {
	job, err := a.Store.Job(ctx, jobID)
	if err != nil {
		return false
	}
	switch job.Status {
	case "running":
		return true
	case "queued":
		ok, claimErr := a.Store.ClaimQueuedJob(ctx, jobID)
		return claimErr == nil && ok
	default:
		return false
	}
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
	if chainID, reserved := a.chainReservation(hostID, container); reserved {
		return 0, fmt.Errorf("%s is reserved by running update chain #%d", container, chainID)
	}
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
		timeout := 12 * time.Minute
		if h, e := a.Store.Host(a.ctx, hostID); e == nil && remoteDockerEndpoint(h.Endpoint) {
			timeout = 15 * time.Minute
		}
		ctx, cancel := context.WithTimeout(a.ctx, timeout)
		defer cancel()
		if !a.beginAsyncJob(ctx, id) {
			return
		}
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
	// Treat the complete check as shared control-plane work, not just the manifest
	// request. This also protects Docker/Watchtower endpoint access if a remote
	// host is addressed by DNS. Multiple checks remain fully parallel, while a
	// pending DNS-service mutation prevents new checks from starting.
	checkCtx, releaseContinuity := a.acquireContinuityShared(ctx)
	defer releaseContinuity()
	ctx = checkCtx
	started := time.Now()
	_ = a.Store.AddJobLog(ctx, jobID, "INFO", "app", "update check started")
	a.jobProgress(ctx, jobID, 20, "Resolving running image")
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		a.jobProgress(ctx, jobID, 100, "Check failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		return watchtower.CheckResponse{}, err
	}

	// For a concrete container, Vibewatch owns update detection. Compare the
	// running immutable Docker image/config ID with the config digest of the
	// matching remote platform manifest. Watchtower remains the mutation worker,
	// but its check endpoint is not authoritative after a rollback because a
	// newer image can remain locally tagged while the container runs the old ID.
	if strings.TrimSpace(container) != "" && a.Registry != nil {
		cur, inspectErr := a.inspectOne(ctx, hostID, container)
		if inspectErr == nil {
			name := strings.TrimPrefix(strings.TrimSpace(cur.Name), "/")
			if name == "" {
				name = container
			}
			ref := strings.TrimSpace(cur.Config.Image)
			if ref == "" {
				ref = strings.TrimSpace(cur.Image)
			}
			state := strings.TrimSpace(cur.State.Status)
			c := dockercli.Container{Name: name, Image: ref, ImageID: strings.TrimSpace(cur.Image), State: state}
			a.jobProgress(ctx, jobID, 45, "Checking registry target identity")
			res, registryErr := a.readOnlyRegistryCheck(ctx, hostID, c, trigger+":registry-authoritative")
			payload, _ := json.Marshal(map[string]any{"api_version": "v1", "containers": res.Containers, "count": res.Count, "timestamp": time.Now().UTC().Format(time.RFC3339)})
			if registryErr == nil {
				a.jobProgress(ctx, jobID, 92, "Saving update state")
				_ = a.Store.AddJobLog(ctx, jobID, "INFO", "registry", "authoritative platform image check completed")
				a.jobProgress(ctx, jobID, 100, "Check completed")
				_ = a.Store.FinishJob(ctx, jobID, "success", string(payload), "")
				return res, nil
			}
			_ = a.Store.AddJobLog(ctx, jobID, "WARN", "registry", "authoritative registry check unavailable; falling back to update worker: "+registryErr.Error())
		} else {
			_ = a.Store.AddJobLog(ctx, jobID, "WARN", "docker", "container inspect for authoritative registry check failed; falling back to update worker: "+inspectErr.Error())
		}
	}

	// Compatibility fallback for bulk checks or registries that Vibewatch cannot
	// resolve directly. This path retains the legacy Watchtower check behavior.
	a.workerOpMu.RLock()
	defer a.workerOpMu.RUnlock()
	a.jobProgress(ctx, jobID, 20, "Preparing update worker")
	base, err := a.ensureWorker(ctx, h)
	if err != nil {
		a.markCheckUnavailable(ctx, hostID, container, err)
		_ = a.Store.AddJobLog(ctx, jobID, "ERROR", "app", err.Error())
		a.jobProgress(ctx, jobID, 100, "Check failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		return watchtower.CheckResponse{}, err
	}
	a.jobProgress(ctx, jobID, 45, "Checking registry and image digest")
	releaseContinuityRead := a.acquireContinuityRead(ctx)
	res, raw, err := a.WT.Check(ctx, base, h.WorkerToken, container)
	releaseContinuityRead()
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
			// A transient/per-container check error or a digest-less post-rollback
			// response must not silently forget when an update was first seen or
			// remove its explicit digest snooze. Preserve the last remote digest
			// while using the currently running image ID as a local identity fallback.
			c.FirstDetectedAt = old.FirstDetectedAt
			c.SnoozedDigest = old.SnoozedDigest
			c.SnoozedAt = old.SnoozedAt
			if strings.TrimSpace(old.SnoozedDigest) != "" {
				if strings.TrimSpace(c.LatestDigest) == "" {
					c.LatestDigest = firstNonEmpty(old.LatestDigest, old.SnoozedDigest)
				}
				if strings.TrimSpace(c.CurrentDigest) == "" {
					c.CurrentDigest = firstNonEmpty(item.ImageID, old.CurrentDigest)
				}
			}
			if cacheHasSnoozedUpdate(c) || cacheHasSnoozedUpdate(old) {
				c.UpdateAvailable = false
			}
		}
		item.UpdateAvailable = bool(c.UpdateAvailable)
		_ = a.Store.SaveCache(ctx, c)
		_ = a.Store.TouchPolicy(ctx, hostID, item.Name)
		if p, e := a.Store.Policy(ctx, hostID, item.Name); e == nil {
			effectiveMode := p.Mode
			if cm, chainManaged := a.chainForMember(ctx, hostID, item.Name); chainManaged {
				effectiveMode = cm.PolicyMode
			}
			if effectiveMode == "manual" && item.UpdateAvailable {
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

func (a *App) enqueueUpdate(ctx context.Context, hostID int64, container, trigger, actor string, updateFlags ...bool) (int64, error) {
	if managed, _ := a.systemManagedContainer(container); managed {
		return 0, fmt.Errorf("Vibewatch system containers are maintained from Owner Settings")
	}
	isChainTrigger := strings.HasPrefix(trigger, "chain:") || strings.HasPrefix(trigger, "chain-auto:")
	if chainID, reserved := a.chainReservation(hostID, container); reserved && !isChainTrigger {
		return 0, fmt.Errorf("%s is reserved by running update chain #%d", container, chainID)
	}
	if !isChainTrigger {
		if cm, chainManaged := a.chainForMember(ctx, hostID, container); chainManaged {
			return 0, fmt.Errorf("%s is managed by update chain %s for stack %s; run the chain instead of updating an individual member", container, cm.ChainName, cm.ScopeKey)
		}
	}
	active, err := a.Store.HasActiveJob(ctx, hostID, container)
	if err != nil {
		return 0, err
	}
	if active {
		return 0, fmt.Errorf("an update job is already queued or running for %s", container)
	}
	if recoveryRequired, recoveryErr := a.Store.HasRecoveryRequiredTransaction(ctx, hostID, container); recoveryErr != nil {
		return 0, recoveryErr
	} else if recoveryRequired {
		return 0, fmt.Errorf("%s has an interrupted update transaction that requires recovery before another update can start", container)
	}
	p, _ := a.Store.Policy(ctx, hostID, container)
	if !isChainTrigger && p.Mode == "ignore" {
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
	allowWarnings := false
	skipDataCapture := false
	if len(updateFlags) > 0 {
		allowWarnings = updateFlags[0]
	}
	if len(updateFlags) > 1 {
		skipDataCapture = updateFlags[1]
	}
	select {
	case a.Queue <- updateRequest{JobID: id, HostID: hostID, Container: container, Trigger: trigger, Actor: actor, AllowPreflightWarnings: allowWarnings, SkipDataProtectionCapture: skipDataCapture}:
		return id, nil
	default:
		_ = a.Store.FinishJob(ctx, id, "failed", "", "update queue full")
		return 0, fmt.Errorf("update queue full")
	}
}

const maxConcurrentUpdatePipelines = 3

func nextDispatchableUpdate(pending []updateRequest, busyHosts map[int64]bool) int {
	for i, req := range pending {
		if !busyHosts[req.HostID] {
			return i
		}
	}
	return -1
}

// updateWorker is a bounded multi-host dispatcher. Preflight, restore-point
// preparation and local Docker inspection can overlap on different hosts, but
// only one pipeline is allowed per host. The global continuity barrier then
// serializes only the destructive stop/recreate/readiness windows. This keeps
// large homelab runs moving without allowing simultaneous service outages.
func (a *App) updateWorker() {
	pending := make([]updateRequest, 0, maxConcurrentUpdatePipelines*2)
	busyHosts := map[int64]bool{}
	done := make(chan int64, maxConcurrentUpdatePipelines)
	running := 0

	for {
		for running < maxConcurrentUpdatePipelines {
			i := nextDispatchableUpdate(pending, busyHosts)
			if i < 0 {
				break
			}
			req := pending[i]
			pending = append(pending[:i], pending[i+1:]...)
			busyHosts[req.HostID] = true
			running++
			go func(r updateRequest) {
				a.executeUpdate(r)
				done <- r.HostID
			}(req)
		}

		select {
		case <-a.ctx.Done():
			return
		case req := <-a.Queue:
			pending = append(pending, req)
		case hostID := <-done:
			delete(busyHosts, hostID)
			if running > 0 {
				running--
			}
		}
	}
}
func (a *App) executeUpdate(req updateRequest) {
	// Register the in-memory owner before claiming the queued DB row. This closes
	// the claim/register race: a cancel arriving immediately after the claim sees
	// an active cooperative owner and can never misclassify the just-started job
	// as an orphan. If the queued job was cancelled first, ClaimQueuedJob simply
	// fails and this owner is removed without touching Docker.
	jobCtx, unregisterCancel := a.registerJobCancellation(req.JobID)
	defer unregisterCancel()
	ctx := jobCtx
	if !a.beginAsyncJob(ctx, req.JobID) {
		return
	}
	if a.jobCancelRequested(ctx, req.JobID) {
		a.cancelUpdateBeforeMutation(req, nil, "safe cancellation requested before update preparation started")
		return
	}
	ctx, releasePipeline, pipelineErr := a.acquireMutationPipeline(ctx, req.HostID)
	if pipelineErr != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, nil, "safe cancellation requested while waiting for the host mutation scheduler")
			return
		}
		a.failJob(req.JobID, fmt.Errorf("acquire host mutation scheduler: %w", pipelineErr))
		return
	}
	defer releasePipeline()
	leaseKey, leaseOwner, leaseErr := a.acquireOperationLease(ctx, req.JobID, req.HostID, req.Container, "update")
	if leaseErr != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, nil, "safe cancellation requested while waiting for the update lease")
			return
		}
		a.failJob(req.JobID, leaseErr)
		return
	}
	leaseReleased := false
	releaseLease := func() {
		if !leaseReleased {
			_ = a.Store.ReleaseOperationLease(context.Background(), leaseKey, leaseOwner)
			leaseReleased = true
		}
	}
	defer releaseLease()

	a.jobProgress(ctx, req.JobID, 8, "Starting update pipeline")
	started := time.Now()
	beforeContainer, beforeVersion := a.currentContainerState(ctx, req.HostID, req.Container)
	beforeCache, _ := a.Store.Cache(ctx, req.HostID, req.Container)
	historySnapshotID := ""
	restorePointID := int64(0)
	attemptedDigest := strings.TrimSpace(beforeCache.LatestDigest)
	expectedTargetImageID := ""
	dependencyCount := 0
	dependencyStatus := "none"
	dependencyDetails := ""
	preflightStatus := ""
	preflightDetails := ""
	verificationStatus := verificationStatusNotConfigured
	verificationDetails := "[]"

	txID, txErr := a.Store.CreateUpdateTransaction(ctx, db.UpdateTransaction{JobID: req.JobID, HostID: req.HostID, ContainerName: req.Container, Trigger: req.Trigger, Actor: req.Actor, State: txQueued, Status: "running", TargetDigest: attemptedDigest})
	if txErr != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, nil, "safe cancellation requested before the update transaction was created")
			return
		}
		a.failJob(req.JobID, fmt.Errorf("create persistent update transaction: %w", txErr))
		return
	}
	tx, _ := a.Store.UpdateTransaction(ctx, txID)
	req.TransactionID = txID
	_ = a.Store.RenewOperationLease(context.Background(), leaseKey, leaseOwner, txID, operationLeaseTTL)
	stopHeartbeat := a.startLeaseHeartbeat(a.ctx, leaseKey, leaseOwner, txID)
	defer stopHeartbeat()
	if a.jobCancelRequested(ctx, req.JobID) {
		a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested before preflight started")
		return
	}
	if err := a.txTransition(ctx, &tx, txPreflight, "running", "update preflight started"); err != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while persisting preflight state")
			return
		}
		a.failJob(req.JobID, fmt.Errorf("persist update transaction preflight state: %w", err))
		return
	}

	defer func() {
		a.recordUpdateHistory(req, beforeContainer, beforeVersion, historySnapshotID, restorePointID, attemptedDigest, expectedTargetImageID, started, dependencyCount, dependencyStatus, dependencyDetails, preflightStatus, preflightDetails, verificationStatus, verificationDetails)
	}()
	defer a.ensureUpdateJobTerminal(req, txID)
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "app", fmt.Sprintf("update pipeline started · transaction #%d", txID))

	h, err := a.Store.Host(ctx, req.HostID)
	if err != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while loading the Docker host")
			return
		}
		_ = a.txTransition(ctx, &tx, txFailed, "failed", err.Error())
		a.failJob(req.JobID, err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	notifyAutomatic := func(status string, resultErr error) {
		if !strings.HasPrefix(req.Trigger, "automation:") && !strings.HasPrefix(req.Trigger, "chain-auto:") {
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

	// Manual, automatic and chain updates all enter the exact same preflight and
	// recovery preparation path. The transaction id is passed into preflight so
	// snapshot and restore-point stages are durable state-machine transitions.
	a.jobProgress(ctx, req.JobID, 12, "Running update preflight")
	// A protected data capture may stop the target briefly to obtain a cold
	// snapshot. Always restore the target to its original running/stopped state
	// before invoking the update worker. Earlier builds deferred the restart of a
	// running target as an optimization; that made a worker-side skip look like a
	// successful update when the target remained on the old image.
	pipelineBaseCtx := ctx
	preflight, prepared := a.runUpdatePreflight(ctx, req, true)
	preflightContinuityHeld := prepared.ContinuityHeld && prepared.ReleaseContinuity != nil
	if preflightContinuityHeld {
		// The restore-point capture deliberately runs under its own timeout. Only
		// the lock itself crosses that boundary; bind the continuity marker to the
		// durable per-job context so restoreCancel() cannot poison the remainder of
		// the update pipeline with context.Canceled.
		ctx = withHeldContinuityMarker(pipelineBaseCtx, prepared.ContinuityExclusive)
		defer func() {
			if preflightContinuityHeld {
				prepared.ReleaseContinuity()
			}
		}()
	}
	if fresh, e := a.Store.UpdateTransaction(ctx, txID); e == nil {
		tx = fresh
	}
	preflightStatus = preflight.Status
	if bs, e := json.Marshal(preflight.Checks); e == nil {
		preflightDetails = string(bs)
	}
	for _, check := range preflight.Checks {
		if check.Key == "custom_verification" && check.Status == preflightGreen {
			verificationStatus = "pending"
			verificationDetails = `[{"type":"custom_verification","status":"pending","detail":"configured; waiting for post-update execution"}]`
			break
		}
	}
	historySnapshotID = prepared.Snapshot.ID
	restorePointID = prepared.RestorePoint.ID
	dependencyCount = len(prepared.Dependencies)
	if dependencyCount > 0 {
		dependencyStatus = "prepared"
		dependencyDetails = dependencyNames(prepared.Dependencies)
	}
	if a.jobCancelRequested(ctx, req.JobID) {
		a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested during update preflight; protected writers were restored before cancellation")
		return
	}
	if preflight.Status == "blocked" {
		err := fmt.Errorf("update preflight blocked the update")
		automaticSafetyHold := false
		for _, check := range preflight.Checks {
			if check.Status == preflightRed {
				err = fmt.Errorf("preflight blocked: %s: %s", check.Title, firstNonEmpty(check.Detail, check.Description))
				if check.Key == "automatic_safety" {
					automaticSafetyHold = true
				}
				break
			}
		}
		if automaticSafetyHold {
			verificationStatus = "not_run"
			verificationDetails = `[{"type":"custom_verification","status":"not_run","detail":"automatic update held by Preflight"}]`
			_ = a.txTransition(ctx, &tx, txSkipped, "skipped", err.Error())
			a.jobProgress(ctx, req.JobID, 100, "Automatic update held by Preflight")
			_ = a.finishJobDurable(req.JobID, "skipped", `{"reason":"preflight_warning"}`, err.Error())
			_ = a.Store.Audit(ctx, req.Actor, "update.skipped-preflight", req.HostID, req.Container, err.Error())
			return
		}
		_ = a.txTransition(ctx, &tx, txFailed, "failed", err.Error())
		a.jobProgress(ctx, req.JobID, 100, "Update blocked by preflight")
		_ = a.finishJobDurable(req.JobID, "failed", "", err.Error())
		_ = a.Store.Audit(ctx, req.Actor, "update.blocked-preflight", req.HostID, req.Container, err.Error())
		notifyAutomatic("failed", err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	if preflight.Status == "ready_with_warnings" {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "preflight", fmt.Sprintf("preflight ready with %d warning(s)", preflight.Warnings))
	}

	// A cold Data Protection capture can temporarily stop the very service that
	// provides DNS/proxy connectivity for the controller. Prove recovery before
	// any post-capture registry lookup. The continuity guard transferred
	// from restore-point capture keeps DNS mutations from racing this recovery; for
	// DNS targets it is exclusive and also keeps other hosts' registry checks out until this
	// sequence succeeds.
	recoveredTargetID := ""
	if containsString(prepared.RestartedDataWriters, req.Container) {
		a.jobProgress(ctx, req.JobID, 43, "Waiting for protected service recovery")
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "data-protection", "cold snapshot complete; waiting for the previously running target to become stable before registry/update activity")
		stableCtx, stableCancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 90*time.Second, 3*time.Minute))
		stableErr := a.verifyUpdatedContainer(stableCtx, req.HostID, req.Container)
		if stableErr == nil {
			stableErr = a.probeRecoveredApplication(stableCtx, req.HostID, req.Container)
		}
		if stableErr == nil {
			for attempt, delay := range []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second} {
				if delay > 0 {
					select {
					case <-stableCtx.Done():
						stableErr = stableCtx.Err()
					case <-time.After(delay):
					}
				}
				if stableErr != nil {
					break
				}
				recoveredTargetID, stableErr = a.resolveExpectedTargetImageIDFresh(stableCtx, req.HostID, req.Container)
				if stableErr == nil {
					break
				}
				_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "data-protection", fmt.Sprintf("fresh registry probe after protected service restart failed (attempt %d/4): %v", attempt+1, stableErr))
			}
		}
		stableCancel()
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while waiting for protected service recovery")
			return
		}
		if stableErr != nil {
			failure := fmt.Errorf("protected service did not recover after cold snapshot: %w", stableErr)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "data-protection", failure.Error())
			_ = a.txTransition(ctx, &tx, txFailed, "failed", failure.Error())
			a.jobProgress(ctx, req.JobID, 100, "Protected service recovery failed")
			_ = a.finishJobDurable(req.JobID, "failed", "", failure.Error())
			_ = a.Store.Audit(ctx, req.Actor, "update.protected-service-recovery-failed", req.HostID, req.Container, failure.Error())
			notifyAutomatic("failed", failure)
			a.notifyManualUpdateResult(req, "failed", failure)
			return
		}
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "data-protection", "previously running target recovered after cold snapshot and passed runtime/application plus fresh registry continuity checks")
	}

	// The worker's latest_digest can be a registry MANIFEST digest, while
	// Docker container inspect .Image is the platform image CONFIG digest.
	// Resolve and persist the config digest explicitly. v1.0.3 accidentally
	// compared these different OCI identities and rolled back correctly updated
	// multi-arch images such as AdGuard Home on arm64.
	targetID := strings.TrimSpace(recoveredTargetID)
	var targetErr error
	if targetID == "" {
		targetID, targetErr = a.resolveExpectedTargetImageID(ctx, req.HostID, req.Container)
	}
	if targetErr != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while resolving the target image")
			return
		}
		err := fmt.Errorf("pre-update target image identity could not be resolved: %w", targetErr)
		_ = a.txTransition(ctx, &tx, txFailed, "failed", err.Error())
		a.jobProgress(ctx, req.JobID, 100, "Update blocked before image mutation")
		_ = a.finishJobDurable(req.JobID, "failed", "", err.Error())
		_ = a.Store.Audit(ctx, req.Actor, "update.target-identity-failed", req.HostID, req.Container, err.Error())
		notifyAutomatic("failed", err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	expectedTargetImageID = strings.TrimSpace(targetID)
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", fmt.Sprintf("target identities resolved: detected_digest=%s expected_image_id=%s", attemptedDigest, expectedTargetImageID))
	if err := a.Store.SetUpdateTransactionPrepared(ctx, txID, historySnapshotID, restorePointID, attemptedDigest, expectedTargetImageID); err != nil {
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while persisting prepared recovery context")
			return
		}
		err = fmt.Errorf("persist prepared recovery context: %w", err)
		_ = a.txTransition(ctx, &tx, txFailed, "failed", err.Error())
		a.failJob(req.JobID, err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	tx.SnapshotID, tx.RestorePointID, tx.TargetDigest, tx.TargetImageID = historySnapshotID, restorePointID, attemptedDigest, expectedTargetImageID

	if tx.State != txPrepared {
		if err := a.txTransition(ctx, &tx, txPrepared, "running", "snapshot and restore point prepared"); err != nil {
			if a.jobCancelRequested(ctx, req.JobID) {
				a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while persisting the prepared transaction state")
				return
			}
			err = fmt.Errorf("persist prepared transaction state: %w", err)
			a.failJob(req.JobID, err)
			a.notifyManualUpdateResult(req, "failed", err)
			return
		}
	}
	if a.jobCancelRequested(ctx, req.JobID) {
		a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested after recovery preparation and before image mutation")
		return
	}
	rp := prepared.RestorePoint

	finishFailed := func(raw []byte, failure error, allowAutoRollback bool) {
		if dependencyCount > 0 && (dependencyStatus == "prepared" || dependencyStatus == "detected") {
			dependencyStatus = "not_run"
		}
		finalErr := failure
		rollbackAttempted := false
		if allowAutoRollback && rp.ID > 0 {
			_ = a.txTransition(ctx, &tx, txRollback, "running", "failure detected; evaluating automatic rollback")
			attempted, rollbackErr := a.runAutomaticRollback(ctx, req, rp, failure)
			rollbackAttempted = attempted
			if attempted {
				if rollbackErr == nil && containerProvidesDNS(prepared.TargetInspect) {
					if restored, inspectErr := a.inspectOne(ctx, req.HostID, req.Container); inspectErr != nil {
						rollbackErr = fmt.Errorf("rollback completed but DNS target could not be inspected for continuity recovery: %w", inspectErr)
					} else if dnsErr := a.verifyDNSControlPlaneRecovery(ctx, restored); dnsErr != nil {
						rollbackErr = fmt.Errorf("rollback completed but DNS control-plane continuity did not recover: %w", dnsErr)
					}
				}
				if rollbackErr == nil {
					finalErr = fmt.Errorf("%v; automatic rollback completed from restore point #%d", failure, rp.ID)
					_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "rollback", fmt.Sprintf("Automatic rollback completed from restore point #%d", rp.ID))
					_ = a.txTransition(ctx, &tx, txRolledBack, "rolled_back", finalErr.Error())
					_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "rolled_back", "automatic_rollback", failure.Error())
				} else {
					finalErr = fmt.Errorf("%v; automatic rollback failed: %v", failure, rollbackErr)
					_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "rollback", "Automatic rollback failed: "+rollbackErr.Error())
					// A failed rollback is not a terminal failed update: the live runtime is
					// unresolved and must block further mutation until recovery reconciles it.
					_ = a.txTransition(ctx, &tx, txRecoveryRequired, "recovery_required", finalErr.Error())
					_ = a.Store.SetUpdateTransactionRecovery(context.Background(), tx.ID, "recovery_required", "rollback_failed", rollbackErr.Error())
				}
			}
		}
		if !rollbackAttempted && !transactionTerminalState(tx.State) {
			_ = a.txTransition(ctx, &tx, txFailed, "failed", finalErr.Error())
		}
		a.jobProgress(ctx, req.JobID, 100, "Update failed")
		_ = a.finishJobDurable(req.JobID, "failed", string(raw), finalErr.Error())
		_ = a.Store.Audit(ctx, "system", "update.failed", req.HostID, req.Container, finalErr.Error())
		notifyAutomatic("failed", finalErr)
		a.notifyManualUpdateResult(req, "failed", finalErr)
	}

	if preflightContinuityHeld {
		prepared.ReleaseContinuity()
		preflightContinuityHeld = false
		ctx = pipelineBaseCtx
	}

	// Materialize and pin the transaction's already-resolved immutable target
	// before Watchtower is allowed to mutate the runtime. Rollbacks intentionally
	// leave the running container on an older/restore image, but the mutable app
	// tag must never point at a Vibewatch restore commit: Watchtower would treat
	// that local tag as the update and recreate the wrong image. Keeping this
	// preparation before txUpdating also makes failures safe and cancellable.
	if strings.TrimSpace(expectedTargetImageID) != "" {
		ref := strings.TrimSpace(prepared.TargetInspect.Config.Image)
		previousRefID := ""
		if mutableApplicationImageRef(ref) {
			if currentRef, refErr := a.Docker.ImagePlatform(ctx, h.Endpoint, ref); refErr == nil {
				previousRefID = strings.TrimSpace(currentRef.ImageID)
			}
		}
		releaseRead := a.acquireContinuityRead(ctx)
		a.jobProgress(ctx, req.JobID, 43, "Preparing exact target image")
		targetPrepErr := a.ensureExpectedTargetImageLocal(ctx, h, prepared, expectedTargetImageID)
		if targetPrepErr == nil {
			_, _, targetPrepErr = a.alignMutableImageRefToTarget(ctx, h, prepared, expectedTargetImageID)
		}
		releaseRead()
		if targetPrepErr != nil {
			if a.jobCancelRequested(ctx, req.JobID) {
				a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while preparing the exact target image")
				return
			}
			failure := fmt.Errorf("prepare authoritative transaction target before worker mutation: %w", targetPrepErr)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "image-verify", failure.Error())
			finishFailed(nil, failure, false)
			return
		}
		if mutableApplicationImageRef(ref) && previousRefID != "" && !digestEqual(previousRefID, expectedTargetImageID) {
			_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", fmt.Sprintf("normalized mutable image ref %s from %s to transaction target %s before invoking worker", ref, previousRefID, expectedTargetImageID))
		} else {
			_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", fmt.Sprintf("authoritative transaction target is local before worker mutation: %s", expectedTargetImageID))
		}
	}

	// Freeze the user/runtime contract before any destructive mutation. This is
	// derived against the immutable source image, so image defaults may evolve
	// while explicit environment/Compose/process overrides remain mandatory.
	runtimeContract, runtimeContractSummary, runtimeContractSourceID, runtimeContractErr := a.preparedRuntimeOverrides(ctx, h, prepared.TargetInspect)
	if runtimeContractErr != nil {
		failure := fmt.Errorf("derive pre-update runtime fidelity contract: %w", runtimeContractErr)
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "runtime-fidelity", failure.Error())
		finishFailed(nil, failure, false)
		return
	}
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "runtime-fidelity", fmt.Sprintf("pre-update runtime contract prepared from source_image=%s: env_overrides=%d label_overrides=%d command_override=%t entrypoint_override=%t", runtimeContractSourceID, runtimeContractSummary.Environment, runtimeContractSummary.Labels, runtimeContractSummary.Command, runtimeContractSummary.Entrypoint))

	// Restore-derived runtimes are synthetic docker-commit containers. Asking
	// Watchtower to reconstruct them has proven unsafe: the worker can preserve
	// image defaults while dropping materialized user environment/Compose
	// metadata. Vibewatch already has the pre-update inspect plus immutable source
	// lineage, so use the deterministic delta recreator directly for this case.
	directTargetApply := vibewatchRestoreRuntime(prepared.TargetInspect)
	base := ""
	workerLocked := false
	if directTargetApply {
		a.jobProgress(ctx, req.JobID, 45, "Preparing deterministic restore-runtime update")
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", "restore-derived runtime detected; bypassing Watchtower recreate and applying the immutable transaction target with Vibewatch runtime-delta fidelity")
	} else {
		a.jobProgress(ctx, req.JobID, 45, "Preparing update worker")
		a.workerOpMu.RLock()
		workerLocked = true
		var workerErr error
		base, workerErr = a.ensureWorker(ctx, h)
		if workerErr != nil {
			a.workerOpMu.RUnlock()
			workerLocked = false
			if a.jobCancelRequested(ctx, req.JobID) {
				a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while preparing the update worker")
				return
			}
			finishFailed(nil, workerErr, false)
			return
		}
	}
	// Enter the continuity guard before image mutation. Waiting for this guard is
	// still a safe cancellation point; only after the guard has been acquired and
	// the transaction is persisted as updating do we switch to an atomic context.
	continuityBaseCtx := ctx
	mutationCtx, releaseContinuity, continuityExclusive := a.acquireContinuityMutation(ctx, prepared.TargetInspect)
	ctx = mutationCtx
	if continuityExclusive {
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "continuity", "DNS-capable target entered exclusive control-plane continuity window")
	}
	continuityHeld := true
	defer func() {
		if continuityHeld {
			releaseContinuity()
		}
	}()
	if a.jobCancelRequested(continuityBaseCtx, req.JobID) {
		releaseContinuity()
		continuityHeld = false
		if workerLocked {
			a.workerOpMu.RUnlock()
			workerLocked = false
		}
		ctx = continuityBaseCtx
		a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested while waiting for the mutation window")
		return
	}
	updateExecutionDetail := "Watchtower update execution started"
	if directTargetApply {
		updateExecutionDetail = "deterministic restore-runtime target execution started"
	}
	if err := a.txTransition(ctx, &tx, txUpdating, "running", updateExecutionDetail); err != nil {
		releaseContinuity()
		continuityHeld = false
		if workerLocked {
			a.workerOpMu.RUnlock()
			workerLocked = false
		}
		ctx = continuityBaseCtx
		if a.jobCancelRequested(ctx, req.JobID) {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation requested at the final pre-mutation transaction boundary")
			return
		}
		err = fmt.Errorf("persist destructive update stage: %w", err)
		a.failJob(req.JobID, err)
		a.notifyManualUpdateResult(req, "failed", err)
		return
	}
	// From this point onward user cancellation is cooperative: never terminate a
	// Watchtower/recreate/verification/rollback operation halfway through. The
	// cancellation request remains persisted and is observed at the next safe
	// settlement point.
	criticalCtx, criticalCancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Minute)
	defer criticalCancel()
	ctx = criticalCtx
	var res watchtower.UpdateResponse
	var raw []byte
	if directTargetApply {
		a.jobProgress(ctx, req.JobID, 55, "Applying exact target image")
		if applyErr := a.applyPreparedTargetImage(ctx, h, prepared, expectedTargetImageID, req.JobID); applyErr != nil {
			failure := fmt.Errorf("restore-runtime deterministic target recreate failed: %w", applyErr)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "image-verify", failure.Error())
			finishFailed(nil, failure, true)
			return
		}
		raw, res = updateSummaryWithLocalRecreate(nil, watchtower.UpdateResponse{})
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", "restore-derived runtime recreated directly from the immutable transaction target")
	} else {
		a.jobProgress(ctx, req.JobID, 55, "Update engine working")
		var updateErr error
		res, raw, updateErr = a.WT.Update(ctx, base, h.WorkerToken, req.Container)
		if logs, e := a.Docker.WorkerLogs(ctx, req.HostID, started); e == nil && logs != "" {
			_ = a.Store.AddJobLog(ctx, req.JobID, "DEBUG", "watchtower", logs)
		}
		if workerLocked {
			a.workerOpMu.RUnlock()
			workerLocked = false
		}
		if updateErr != nil {
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "watchtower", updateErr.Error())
			finishFailed(raw, updateErr, true)
			return
		}
	}

	a.jobProgress(ctx, req.JobID, 72, "Update engine completed")
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "app", fmt.Sprintf("update completed: updated=%d failed=%d skipped=%d", res.Summary.Updated, res.Summary.Failed, res.Summary.Skipped))
	if res.Summary.Failed > 0 {
		failure := errors.New(strings.TrimSpace(res.Error))
		if strings.TrimSpace(res.Error) == "" {
			failure = fmt.Errorf("update engine reported %d failed container operation(s)", res.Summary.Failed)
		}
		finishFailed(raw, failure, true)
		return
	}
	if err := a.txTransition(ctx, &tx, txImageVerify, "running", "target image verification started"); err != nil {
		finishFailed(raw, fmt.Errorf("persist target image verification stage: %w", err), true)
		return
	}
	a.jobProgress(ctx, req.JobID, 75, "Verifying target image")
	actualImage, imageErr := a.verifyAppliedTargetImage(ctx, req.HostID, req.Container, beforeContainer.ImageID, expectedTargetImageID, res.Summary.Updated, res.Summary.Skipped)
	if imageErr != nil {
		unchanged := strings.TrimSpace(actualImage) != "" && strings.TrimSpace(beforeContainer.ImageID) != "" && digestEqual(actualImage, beforeContainer.ImageID)
		// The worker is advisory for mutation, while expectedTargetImageID is the
		// immutable transaction contract. Correct *any* provable mismatch, not only
		// a no-op: a polluted mutable tag can make Watchtower switch from one old
		// restore image to another old restore image. Image verification must catch
		// that and Vibewatch must then apply the exact prepared target itself.
		if deterministicTargetCorrectionEligible(actualImage, expectedTargetImageID) {
			if unchanged {
				_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "image-verify", fmt.Sprintf("update worker left the running image unchanged at %s; correcting to transaction target %s", actualImage, expectedTargetImageID))
			} else {
				_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "image-verify", fmt.Sprintf("update worker switched the running image to unexpected image %s; correcting to transaction target %s", actualImage, expectedTargetImageID))
			}
			a.jobProgress(ctx, req.JobID, 74, "Preparing exact target image")
			if localErr := a.ensureExpectedTargetImageLocal(ctx, h, prepared, expectedTargetImageID); localErr != nil {
				imageErr = fmt.Errorf("%v; deterministic target preparation failed: %w", imageErr, localErr)
			} else {
				a.jobProgress(ctx, req.JobID, 76, "Applying exact target image")
				if applyErr := a.applyPreparedTargetImage(ctx, h, prepared, expectedTargetImageID, req.JobID); applyErr != nil {
					failure := fmt.Errorf("post-update target recreate failed: %w", applyErr)
					_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "image-verify", failure.Error())
					finishFailed(raw, failure, true)
					return
				}
				actualImage, imageErr = a.verifyAppliedTargetImage(ctx, req.HostID, req.Container, beforeContainer.ImageID, expectedTargetImageID, 1, res.Summary.Skipped)
				if imageErr == nil {
					raw, res = updateSummaryWithLocalRecreate(raw, res)
					_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", "deterministic target recreate applied the expected image successfully")
				}
			}
		}
		if imageErr != nil {
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "image-verify", imageErr.Error())
			unchanged = strings.TrimSpace(actualImage) != "" && strings.TrimSpace(beforeContainer.ImageID) != "" && digestEqual(actualImage, beforeContainer.ImageID)
			finishFailed(raw, imageErr, !unchanged)
			if unchanged {
				_ = a.Store.SetUpdateTransactionRecovery(context.Background(), tx.ID, "failed", "target_not_applied", imageErr.Error())
			}
			return
		}
	}
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "image-verify", fmt.Sprintf("target image applied: %s", actualImage))
	if res.Summary.Updated == 0 {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "watchtower", "worker reported updated=0, but the expected target image is already active; continuing with runtime verification")
	}

	// Verify configuration fidelity before health checks. A container can be on
	// the correct image yet be unusable because a recreator dropped explicit Env,
	// Compose labels or host/network settings. Repair exactly once using the
	// immutable target plus the pre-mutation contract instead of waiting for an
	// indirect application/health failure.
	afterMutation, fidelityInspectErr := a.inspectOne(ctx, req.HostID, req.Container)
	if fidelityInspectErr != nil {
		failure := fmt.Errorf("post-update runtime fidelity inspect failed: %w", fidelityInspectErr)
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "runtime-fidelity", failure.Error())
		finishFailed(raw, failure, true)
		return
	}
	if mismatches := preparedRuntimeFidelityMismatches(prepared.TargetInspect, runtimeContract, afterMutation); len(mismatches) > 0 {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "runtime-fidelity", fmt.Sprintf("post-update runtime contract mismatch in %s; correcting by deterministic target recreation", strings.Join(mismatches, ", ")))
		a.jobProgress(ctx, req.JobID, 78, "Repairing runtime configuration")
		if applyErr := a.applyPreparedTargetImage(ctx, h, prepared, expectedTargetImageID, req.JobID); applyErr != nil {
			failure := fmt.Errorf("post-update runtime fidelity repair failed: %w", applyErr)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "runtime-fidelity", failure.Error())
			finishFailed(raw, failure, true)
			return
		}
		actualImage, imageErr = a.verifyAppliedTargetImage(ctx, req.HostID, req.Container, beforeContainer.ImageID, expectedTargetImageID, 1, res.Summary.Skipped)
		if imageErr != nil {
			failure := fmt.Errorf("post-update runtime fidelity repair applied an unexpected image: %w", imageErr)
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "runtime-fidelity", failure.Error())
			finishFailed(raw, failure, true)
			return
		}
		afterMutation, fidelityInspectErr = a.inspectOne(ctx, req.HostID, req.Container)
		if fidelityInspectErr != nil {
			failure := fmt.Errorf("post-update runtime fidelity repair inspect failed: %w", fidelityInspectErr)
			finishFailed(raw, failure, true)
			return
		}
		if repaired := preparedRuntimeFidelityMismatches(prepared.TargetInspect, runtimeContract, afterMutation); len(repaired) > 0 {
			failure := fmt.Errorf("post-update runtime fidelity mismatch remains after deterministic repair in: %s", strings.Join(repaired, ", "))
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "runtime-fidelity", failure.Error())
			finishFailed(raw, failure, true)
			return
		}
		raw, res = updateSummaryWithLocalRecreate(raw, res)
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "runtime-fidelity", "runtime contract restored successfully on the immutable target image")
	} else {
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "runtime-fidelity", "post-update runtime contract verified")
	}

	_ = a.txTransition(ctx, &tx, txDockerHealth, "running", "Docker health verification started")
	a.jobProgress(ctx, req.JobID, 80, "Checking Docker health")
	if verifyErr := a.verifyUpdatedContainer(ctx, req.HostID, req.Container); verifyErr != nil {
		verificationStatus = verificationStatusFailed
		verificationDetails = fmt.Sprintf(`[{"type":"docker_health","status":"failed","error":%q}]`, verifyErr.Error())
		_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "verify", verifyErr.Error())
		finishFailed(raw, fmt.Errorf("post-update Docker health verification failed: %w", verifyErr), true)
		return
	}
	_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "verify", "Docker health/running-state verification passed")

	if dependencyCount > 0 {
		_ = a.txTransition(ctx, &tx, txDependencies, "running", "network namespace dependency reconciliation started")
		parentAfter, inspectErr := a.inspectOne(ctx, req.HostID, req.Container)
		if inspectErr != nil {
			dependencyStatus = "failed"
			finishFailed(raw, fmt.Errorf("post-update parent inspect for dependency recreation failed: %w", inspectErr), true)
			return
		}
		if strings.TrimSpace(parentAfter.ID) != "" && strings.TrimSpace(parentAfter.ID) != strings.TrimSpace(prepared.TargetInspect.ID) {
			dependencyStatus = "recreating"
			if depErr := a.recreateNetworkNamespaceDependents(ctx, req.JobID, req.HostID, req.Container, parentAfter.ID, prepared.Dependencies); depErr != nil {
				dependencyStatus = "failed"
				finishFailed(raw, depErr, true)
				return
			}
			dependencyStatus = "success"
		} else {
			dependencyStatus = "not_required"
			_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "dependency", "Parent container ID did not change; dependent recreation was not required")
		}
	}

	_ = a.txTransition(ctx, &tx, txVerifying, "running", "custom verification started")
	a.jobProgress(ctx, req.JobID, 88, "Running custom verification")
	verification := a.runCustomVerification(ctx, req.HostID, req.Container, req.Trigger, req.Actor, req.JobID)
	verificationStatus = verification.Status
	if bs, e := json.Marshal(verification); e == nil {
		verificationDetails = string(bs)
	}
	if verification.Status == verificationStatusFailed {
		finishFailed(raw, fmt.Errorf("custom verification failed: %s", verification.Error), true)
		return
	}
	if verification.Status == verificationStatusNotConfigured {
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "verify", "No custom verification configured; Docker health/running-state result is authoritative")
	}

	// DNS providers are control-plane infrastructure. HTTP/Docker health alone
	// can become ready before port 53 is actually usable, so prove controller
	// name resolution before allowing queued registry checks to continue.
	if containerProvidesDNS(prepared.TargetInspect) {
		a.jobProgress(ctx, req.JobID, 92, "Verifying DNS control-plane recovery")
		if dnsErr := a.verifyDNSControlPlaneRecovery(ctx, prepared.TargetInspect); dnsErr != nil {
			_ = a.Store.AddJobLog(ctx, req.JobID, "ERROR", "continuity", dnsErr.Error())
			finishFailed(raw, fmt.Errorf("post-update control-plane continuity verification failed: %w", dnsErr), true)
			return
		}
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "continuity", "DNS-capable target restored controller name resolution; queued network checks may resume")
	}

	// The disruptive window is over. Re-open parallel discovery before the
	// non-destructive metadata refresh below. Keep using the bounded atomic
	// context for settlement: the user's job context may now be cancelled, but a
	// late safe-cancel request must not strand a successfully mutated runtime in
	// txVerifying/cancel_requested.
	releaseContinuity()
	continuityHeld = false
	ctx = criticalCtx

	_ = a.txTransition(ctx, &tx, txRefreshing, "running", "refreshing update metadata and config drift baseline")
	a.jobProgress(ctx, req.JobID, 95, "Refreshing update state")
	_, _, _ = a.check(ctx, req.HostID, req.Container, "post-update")
	if err := a.captureCurrentConfigDriftBaseline(ctx, req.HostID, req.Container, "post-update"); err != nil {
		_ = a.Store.AddJobLog(ctx, req.JobID, "WARN", "config-drift", "Could not refresh post-update drift baseline: "+err.Error())
		a.Logger.Warn("post-update config drift baseline refresh failed", "host_id", req.HostID, "container", req.Container, "error", err)
	}
	if a.jobCancelRequested(pipelineBaseCtx, req.JobID) {
		_ = a.Store.AddJobLog(context.Background(), req.JobID, "WARN", "app", "safe cancellation was requested after image mutation started; the atomic update and verification completed successfully instead of interrupting Docker mid-flight")
		_ = a.Store.Audit(context.Background(), req.Actor, "job.cancel-too-late", req.HostID, req.Container, fmt.Sprintf("job=%d transaction=%d update completed safely", req.JobID, txID))
	}
	_ = a.txTransition(ctx, &tx, txSuccess, "success", "update transaction completed")
	a.jobProgress(ctx, req.JobID, 100, "Update completed")
	_ = a.finishJobDurable(req.JobID, "success", string(raw), "")
	_ = a.Store.Audit(ctx, "system", "update.success", req.HostID, req.Container, fmt.Sprintf("transaction=%d %s", txID, string(raw)))
	notifyAutomatic("success", nil)
	if res.Summary.Updated > 0 {
		a.notifyManualUpdateResult(req, "success", nil)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return "blocked"
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
	_ = a.finishJobDurable(id, "failed", "", err.Error())
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
	releaseHostDiscovery := a.acquireContinuityRead(ctx)
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	releaseHostDiscovery()
	if err != nil {
		return watchtower.CheckResponse{}, err
	}
	stackManagement, _ := a.stackChainManagement(ctx, hostID)
	automationID := int64(0)
	if strings.HasPrefix(trigger, "automation:") {
		automationID, _ = strconv.ParseInt(strings.TrimPrefix(trigger, "automation:"), 10, 64)
	}
	var aggregate watchtower.CheckResponse
	var aggregateMu sync.Mutex
	jobs := make(chan dockercli.Container)
	var wg sync.WaitGroup
	workerCount := 2
	if len(cs) < workerCount {
		workerCount = len(cs)
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if managed, _ := a.systemManagedContainer(c.Name); managed {
					continue
				}
				if _, reserved := a.chainReservation(hostID, c.Name); reserved {
					a.Logger.Debug("policy scan skipped container reserved by update chain", "host_id", hostID, "container", c.Name)
					continue
				}
				p, _ := a.Store.Policy(ctx, hostID, c.Name)
				mode := p.Mode
				chainManaged := false
				if c.StackName != "" {
					if cm, ok := stackManagement[c.StackName]; ok {
						chainManaged = true
						mode = cm.PolicyMode
						if installAuto {
							// Stack policy belongs to the chain. Auto mode is executed only by
							// the ordered chain transaction; Manual/Excluded are refreshed only
							// by the automation explicitly bound to that chain.
							if mode == "auto" {
								continue
							}
							if automationID == 0 || cm.AutomationID != automationID {
								continue
							}
						}
					}
				}
				var res watchtower.CheckResponse
				var e error
				if mode == "ignore" {
					res, e = a.readOnlyRegistryCheck(ctx, hostID, c, trigger+":excluded-info")
					if e != nil {
						a.Logger.Debug("excluded read-only registry check unavailable", "host_id", hostID, "container", c.Name, "error", e)
						continue
					}
				} else {
					if c.State != "running" {
						continue
					}
					res, _, e = a.check(ctx, hostID, c.Name, trigger)
					if e != nil {
						a.Logger.Warn("policy scan container failed", "host_id", hostID, "container", c.Name, "error", e)
						continue
					}
				}
				aggregateMu.Lock()
				aggregate.Count += res.Count
				aggregate.Containers = append(aggregate.Containers, res.Containers...)
				aggregateMu.Unlock()
				if installAuto && mode == "auto" && !chainManaged {
					for _, item := range res.Containers {
						if item.Name == c.Name && item.UpdateAvailable {
							_, _ = a.enqueueUpdate(ctx, hostID, c.Name, trigger, "scheduler", bool(p.AllowPreflightWarnings))
						}
					}
				}
			}
		}()
	}
	for _, c := range cs {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return aggregate, ctx.Err()
		case jobs <- c:
		}
	}
	close(jobs)
	wg.Wait()
	return aggregate, nil
}

func normalizeAutomationKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "cleanup" {
		return "cleanup"
	}
	return "policy"
}

func automationCleanupActions(rule db.Automation) []string {
	actions := []string{}
	if bool(rule.CleanupImages) {
		actions = append(actions, "images")
	}
	if bool(rule.CleanupNetworks) {
		actions = append(actions, "networks")
	}
	if bool(rule.CleanupBuildCache) {
		actions = append(actions, "build-cache")
	}
	if bool(rule.CleanupVolumes) {
		actions = append(actions, "volumes")
	}
	return actions
}

func (a *App) runScheduledCleanupHost(rule db.Automation, hostID int64) {
	h, err := a.Store.Host(context.Background(), hostID)
	if err != nil || !bool(h.Enabled) {
		return
	}
	actions := automationCleanupActions(rule)
	if len(actions) == 0 {
		return
	}
	_ = a.Store.Audit(context.Background(), "scheduler", "cleanup-automation.run", hostID, "", fmt.Sprintf("automation=%d name=%s actions=%s", rule.ID, rule.Name, strings.Join(actions, ",")))
	for _, action := range actions {
		var jobType, target string
		switch action {
		case "images":
			jobType, target = "image-cleanup", "unused-images"
		case "networks":
			jobType, target = "network-cleanup", "unused-networks"
		case "build-cache":
			jobType, target = "build-cache-cleanup", "build-cache"
		case "volumes":
			jobType, target = "volume-cleanup", "unused-anonymous-volumes"
		default:
			continue
		}
		jobID, createErr := a.Store.CreateJob(context.Background(), jobType, fmt.Sprintf("automation:%d", rule.ID), hostID, target, "queued")
		if createErr != nil {
			continue
		}
		a.jobProgress(context.Background(), jobID, 2, "Scheduled cleanup queued")
		switch action {
		case "images":
			a.runImageCleanup(jobID, h, "scheduler")
		case "networks":
			a.runNetworkCleanup(jobID, h, "scheduler")
		case "build-cache":
			a.runBuildCacheCleanup(jobID, h, "scheduler")
		case "volumes":
			a.runAnonymousVolumeCleanup(jobID, h, "scheduler")
		}
	}
	_ = a.Store.InvalidateHostStorageCache(context.Background(), hostID)
	_ = a.probeHostStorage(context.Background(), hostID, true)
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
		_ = a.Store.Audit(a.ctx, "scheduler", "automation.run", 0, "", fmt.Sprintf("%s kind=%s target=%s:%d", rule.Name, normalizeAutomationKind(rule.Kind), rule.TargetType, rule.TargetID))
		if normalizeAutomationKind(rule.Kind) == "cleanup" {
			for _, hostID := range hostIDs {
				go a.runScheduledCleanupHost(rule, hostID)
			}
			continue
		}
		// Chains bound to this existing policy run reserve their members before the
		// ordinary policy scan starts. The scan therefore skips those containers
		// and cannot race or duplicate the explicitly ordered chain transaction.
		a.startAutomationChains(a.ctx, rule, hostIDs)

		// Host discovery is intentionally parallel and bounded. The continuity
		// barrier in the registry/update paths prevents a destructive lifecycle
		// window from racing these scans, so we get faster multi-host discovery
		// without turning the whole automation into one long serial chain.
		const maxParallelHostScans = 3
		sem := make(chan struct{}, maxParallelHostScans)
		var scanWG sync.WaitGroup
		for _, hostID := range hostIDs {
			h, e := a.Store.Host(a.ctx, hostID)
			if e != nil || !bool(h.Enabled) {
				continue
			}
			scanWG.Add(1)
			go func(id int64) {
				defer scanWG.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-a.ctx.Done():
					return
				}
				_, _ = a.policyScanHost(a.ctx, id, fmt.Sprintf("automation:%d", rule.ID), true)
			}(hostID)
		}
		scanWG.Wait()
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
	in.Kind = normalizeAutomationKind(in.Kind)
	if in.Kind == "cleanup" && !in.CleanupImages && !in.CleanupNetworks && !in.CleanupBuildCache && !in.CleanupVolumes {
		writeErr(w, 400, "select at least one cleanup action")
		return
	}
	if in.Kind == "policy" {
		in.CleanupImages, in.CleanupNetworks, in.CleanupBuildCache, in.CleanupVolumes = false, false, false, false
	}
	x := db.Automation{ID: in.ID, Name: strings.TrimSpace(in.Name), Cron: in.Cron, TargetType: in.TargetType, TargetID: in.TargetID, Kind: in.Kind, CleanupImages: db.Bool(in.CleanupImages), CleanupNetworks: db.Bool(in.CleanupNetworks), CleanupBuildCache: db.Bool(in.CleanupBuildCache), CleanupVolumes: db.Bool(in.CleanupVolumes), Enabled: db.Bool(in.Enabled)}
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
	// precedence over the bootstrap environment password without trying to rewrite .env.
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
func (a *App) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
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
	actor := a.actor(r)
	switch job.Status {
	case "queued":
		reason := "cancelled before execution by " + actor
		ok, cancelErr := a.Store.CancelQueuedJob(r.Context(), id, reason)
		if cancelErr != nil {
			writeErr(w, http.StatusInternalServerError, cancelErr.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusConflict, "job already started or is no longer queued")
			return
		}
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), id)
		_ = a.Store.AddJobLog(context.Background(), id, "WARN", "app", reason)
		a.jobProgress(context.Background(), id, 100, "Cancelled before execution")
		_ = a.Store.Audit(context.Background(), actor, "job.cancelled", job.HostID, job.ContainerName, fmt.Sprintf("job=%d type=%s trigger=%s", job.ID, job.Type, job.Trigger))
		job.Status = "cancelled"
		job.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "queued", "job": job})
		return

	case "cancel_requested":
		// Idempotent: a second click/retry must never turn a cooperative cancel
		// into a forceful interruption.
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "mode": "safe_point", "job": job})
		return

	case "running":
		if job.Type != "update" && job.Type != "chain" {
			writeErr(w, http.StatusConflict, "safe cancellation of a running job is supported for container update and update-chain jobs")
			return
		}
		reason := "safe cancellation requested by " + actor
		ok, cancelErr := a.Store.RequestRunningJobCancel(r.Context(), id, reason)
		if cancelErr != nil {
			writeErr(w, http.StatusInternalServerError, cancelErr.Error())
			return
		}
		if !ok {
			latest, latestErr := a.Store.Job(r.Context(), id)
			if latestErr == nil && latest.Status == "cancel_requested" {
				writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "mode": "safe_point", "job": latest})
				return
			}
			writeErr(w, http.StatusConflict, "job is no longer running")
			return
		}
		_ = a.Store.AddJobLog(context.Background(), id, "WARN", "app", reason+"; Vibewatch will stop at the next transaction-safe point and will not kill an atomic Docker mutation")
		_ = a.Store.Audit(context.Background(), actor, "job.cancel-requested", job.HostID, job.ContainerName, fmt.Sprintf("job=%d type=%s trigger=%s", job.ID, job.Type, job.Trigger))
		active := a.signalJobCancellation(id)
		mode := "safe_point"
		if !active {
			// A persisted running row with no in-memory owner is an orphan. Reconcile
			// it immediately instead of requiring a controller restart.
			if job.Type == "chain" {
				mode, err = a.reconcileOrphanedChainCancel(job, actor)
			} else {
				mode, err = a.reconcileOrphanedUpdateCancel(job, actor)
			}
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		latest, _ := a.Store.Job(context.Background(), id)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "mode": mode, "job": latest})
		return
	default:
		writeErr(w, http.StatusConflict, "job is already finished")
	}
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
	if status == "cancelled" {
		percent = 100
		if stage == "" || stage == "Queued" {
			stage = "Cancelled before execution"
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
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if len(lines) > 500 {
		lines = lines[len(lines)-500:]
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
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
	jobLogDiag := map[string]any{}
	for _, job := range jobs {
		logs, e := a.Store.JobLogs(r.Context(), job.ID)
		if e != nil || len(logs) == 0 {
			continue
		}
		// Keep support bundles bounded even for unusually chatty long-running jobs.
		if len(logs) > 250 {
			logs = logs[len(logs)-250:]
		}
		jobLogDiag[strconv.FormatInt(job.ID, 10)] = logs
	}
	writeZipJSON(zw, "job-logs.json", jobLogDiag)
	writeZipJSON(zw, "audit.json", audits)
	writeZipJSON(zw, "docker-events.json", events)
	writeZipJSON(zw, "automations.json", automations)
	writeZipJSON(zw, "host-groups.json", groups)
	if chainViews, e := a.chainViews(r.Context()); e == nil {
		writeZipJSON(zw, "update-chains.json", chainViews)
	}
	if chainRuns, e := a.Store.UpdateChainRuns(r.Context(), 0, 200); e == nil {
		runDiag := make([]map[string]any, 0, len(chainRuns))
		for _, run := range chainRuns {
			steps, _ := a.Store.UpdateChainRunSteps(r.Context(), run.ID)
			runDiag = append(runDiag, map[string]any{"run": run, "steps": steps})
		}
		writeZipJSON(zw, "update-chain-runs.json", runDiag)
	}
	verificationDiag := map[string]any{}
	for _, h := range hosts {
		profiles, _ := a.Store.VerificationProfiles(r.Context(), h.ID)
		rows := make([]map[string]any, 0, len(profiles))
		for _, p := range profiles {
			checks := verificationChecks(p.ChecksJSON)
			checkMeta := make([]map[string]any, 0, len(checks))
			for _, c := range checks {
				checkMeta = append(checkMeta, map[string]any{"type": c.Type, "port": c.Port, "expected_status": c.ExpectedStatus, "content_match_configured": c.ExpectedContent != "", "timeout_seconds": c.TimeoutSeconds})
			}
			rows = append(rows, map[string]any{"scope_type": p.ScopeType, "scope_key": p.ScopeKey, "enabled": bool(p.Enabled), "start_delay_seconds": p.StartDelaySeconds, "retry_count": p.RetryCount, "retry_interval_seconds": p.RetryIntervalSeconds, "checks": checkMeta})
		}
		verificationDiag[fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))] = rows
	}
	writeZipJSON(zw, "verification-profiles.json", verificationDiag)
	dataProtectionDiag := map[string]any{}
	for _, h := range hosts {
		profiles, _ := a.Store.DataProtectionProfiles(r.Context(), h.ID)
		profileMeta := make([]map[string]any, 0, len(profiles))
		for _, p := range profiles {
			profileMeta = append(profileMeta, map[string]any{"host_id": p.HostID, "scope_type": p.ScopeType, "scope_key": p.ScopeKey, "enabled": bool(p.Enabled), "mount_count": len(selectedMountKeys(p.MountsJSON)), "updated_at": p.UpdatedAt})
		}
		storage, _ := a.Store.HostStorageCache(r.Context(), h.ID)
		dataProtectionDiag[fmt.Sprintf("host-%d-%s", h.ID, sanitizeFilename(h.Name))] = map[string]any{"profiles": profileMeta, "storage_cache": storage}
	}
	writeZipJSON(zw, "data-protection.json", dataProtectionDiag)
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
			profile, _ := a.effectiveVerificationProfile(r.Context(), h.ID, c.Name)
			verification := a.effectiveVerificationState(r.Context(), h.ID, c.Name, profile)
			rows = append(rows, map[string]any{"name": c.Name, "image": c.Image, "image_id": c.ImageID, "state": c.State, "policy": p, "digest_state": cache, "version_metadata": v, "config_drift": drift, "verification_state": verification})
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
	// Recovery storage is reconciled periodically. This central GC validates
	// retained restore points first, then lets the existing retention/protection
	// logic expire only objects that are no longer part of a valid transaction.
	lastGC := a.Store.Setting(a.ctx, "recovery_gc_last_at", "")
	gcDue := true
	if t, e := time.Parse(time.RFC3339Nano, lastGC); e == nil && now.Sub(t) < 6*time.Hour {
		gcDue = false
	}
	if gcDue {
		_ = a.Store.SetSetting(a.ctx, "recovery_gc_last_at", now.UTC().Format(time.RFC3339Nano))
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			a.runRecoveryGC(ctx, "scheduled")
		}()
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
			releaseContinuityRead := a.acquireContinuityRead(r.Context())
			rv, e := a.Registry.RemoteVersion(r.Context(), a.Cfg.AppImage)
			releaseContinuityRead()
			if e == nil {
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
		writeErr(w, 409, "self update is not configured; set VIBEWATCH_APP_IMAGE to the published registry image")
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

func (a *App) handleNotificationReadState(w http.ResponseWriter, r *http.Request) {
	uid := a.identity(r).UserID
	if r.Method == http.MethodGet {
		reads, err := a.Store.UINotificationReads(r.Context(), uid)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"reads": reads})
		return
	}
	var in struct {
		Items []db.UINotificationRead `json:"items"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(in.Items) == 0 || len(in.Items) > 100 {
		writeErr(w, 400, "notification items must contain between 1 and 100 entries")
		return
	}
	items := make([]db.UINotificationRead, 0, len(in.Items))
	for _, item := range in.Items {
		id := strings.TrimSpace(item.NotificationID)
		fingerprint := strings.TrimSpace(item.Fingerprint)
		if id == "" || fingerprint == "" || len(id) > 256 || len(fingerprint) > 2048 {
			writeErr(w, 400, "invalid notification read state")
			return
		}
		items = append(items, db.UINotificationRead{NotificationID: id, Fingerprint: fingerprint})
	}
	if err := a.Store.SaveUINotificationReads(r.Context(), uid, items); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
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
