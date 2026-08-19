package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/devupdate"
)

type developerUpdateSupport struct {
	Enabled        bool                          `json:"enabled"`
	Available      bool                          `json:"available"`
	Reason         string                        `json:"reason,omitempty"`
	CurrentVersion string                        `json:"current_version"`
	ProjectDir     string                        `json:"project_dir,omitempty"`
	ProjectMounted bool                          `json:"project_mounted"`
	ComposeReady   bool                          `json:"compose_ready"`
	LastUpdate     *devupdate.Status             `json:"last_update,omitempty"`
	DiskPreflight  *developerUpdateDiskPreflight `json:"disk_preflight,omitempty"`
}

type developerUpdateDiskPreflight struct {
	Ready               bool   `json:"ready"`
	Reason              string `json:"reason,omitempty"`
	WorkspaceFreeBytes  int64  `json:"workspace_free_bytes"`
	DataFreeBytes       int64  `json:"data_free_bytes"`
	DockerFreeBytes     int64  `json:"docker_free_bytes"`
	DockerTotalBytes    int64  `json:"docker_total_bytes"`
	DockerRoot          string `json:"docker_root,omitempty"`
	RequiredDockerBytes int64  `json:"required_docker_bytes"`
}

func (a *App) developerUpdateMountSources(ctx context.Context) (string, string, error) {
	projectDir := strings.TrimSpace(a.Cfg.ProjectDir)
	if projectDir == "" {
		return "", "", fmt.Errorf("development project directory is not configured")
	}
	projectMount, mounted, err := a.Docker.ContainerMount(ctx, a.Cfg.ControllerName, projectDir)
	if err != nil {
		return "", "", fmt.Errorf("inspect controller project mount: %w", err)
	}
	if !mounted || !projectMount.RW || projectMount.Type != "bind" || strings.TrimSpace(projectMount.Source) == "" {
		return "", "", fmt.Errorf("source-build project directory is not mounted as a writable bind mount")
	}
	dataMount, mounted, err := a.Docker.ContainerMount(ctx, a.Cfg.ControllerName, a.Cfg.DataDir)
	if err != nil {
		return "", "", fmt.Errorf("inspect controller data mount: %w", err)
	}
	if !mounted || !dataMount.RW || dataMount.Type != "bind" || strings.TrimSpace(dataMount.Source) == "" {
		return "", "", fmt.Errorf("persistent Vibewatch data is not mounted as a writable bind mount; development self-update is disabled to avoid changing storage semantics")
	}
	return projectMount.Source, dataMount.Source, nil
}

func (a *App) developerUpdateDiskPreflight(ctx context.Context) developerUpdateDiskPreflight {
	out := developerUpdateDiskPreflight{RequiredDockerBytes: devupdate.MinDockerBuildFreeBytes}
	workspace, err := devupdate.DiskUsage(a.Cfg.ProjectDir)
	if err != nil {
		out.Reason = "Could not inspect project filesystem capacity: " + err.Error()
		return out
	}
	out.WorkspaceFreeBytes = workspace.FreeBytes
	if workspace.FreeBytes < devupdate.MinWorkspaceFreeBytes || workspace.FreeInodes < devupdate.MinFreeInodes {
		out.Reason = fmt.Sprintf("Project filesystem has insufficient recovery headroom (%s free). Free at least %s before installing a development update.", formatDevBytes(workspace.FreeBytes), formatDevBytes(devupdate.MinWorkspaceFreeBytes))
		return out
	}
	data, err := devupdate.DiskUsage(a.Cfg.DataDir)
	if err != nil {
		out.Reason = "Could not inspect persistent data filesystem capacity: " + err.Error()
		return out
	}
	out.DataFreeBytes = data.FreeBytes
	if data.FreeBytes < devupdate.MinDataFreeBytes || data.FreeInodes < devupdate.MinFreeInodes {
		out.Reason = fmt.Sprintf("Persistent data filesystem has insufficient update headroom (%s free). Free at least %s before installing a development update.", formatDevBytes(data.FreeBytes), formatDevBytes(devupdate.MinDataFreeBytes))
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	dockerFS, err := a.Docker.DockerRootFilesystemUsage(probeCtx, a.Cfg.ControllerName)
	if err != nil {
		out.Reason = "Could not verify Docker build filesystem capacity: " + err.Error()
		return out
	}
	out.DockerRoot = dockerFS.Path
	out.DockerFreeBytes = dockerFS.FreeBytes
	out.DockerTotalBytes = dockerFS.TotalBytes
	if dockerFS.FreeBytes < devupdate.MinDockerBuildFreeBytes || dockerFS.FreeInodes < devupdate.MinFreeInodes {
		out.Reason = fmt.Sprintf("Docker build filesystem has only %s free; at least %s is required. Free Docker build cache or expand the filesystem before installing.", formatDevBytes(dockerFS.FreeBytes), formatDevBytes(devupdate.MinDockerBuildFreeBytes))
		return out
	}
	out.Ready = true
	return out
}

func formatDevBytes(n int64) string {
	const gib = int64(1 << 30)
	const mib = int64(1 << 20)
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	}
	return fmt.Sprintf("%.0f MiB", float64(n)/float64(mib))
}

func devUpdateSwitchAttempted(st devupdate.Status) bool {
	if st.SwitchAttempted {
		return true
	}
	state := strings.ToLower(strings.TrimSpace(st.State))
	if state == "switching" || state == "verifying" {
		return true
	}
	return state == "rolling_back" && st.Percent >= 80
}

func (a *App) reconcileDeveloperUpdates(ctx context.Context) {
	if !a.Cfg.DeveloperUpdates {
		return
	}
	a.devUpdateMu.Lock()
	defer a.devUpdateMu.Unlock()
	devupdate.CleanupStateTemps(a.Cfg.DataDir)
	for attempts := 0; attempts < 16; attempts++ {
		st, ok := devupdate.ActiveStatus(a.Cfg.DataDir)
		if !ok {
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		running, err := a.Docker.DevelopmentUpdaterRunning(probeCtx, st.ID)
		cancel()
		if err != nil {
			if a.Logger != nil {
				a.Logger.Warn("could not inspect development updater helper during reconciliation", "update_id", st.ID, "error", err)
			}
			return
		}
		if running {
			return
		}
		if err := a.reconcileOrphanedDeveloperUpdate(st); err != nil && a.Logger != nil {
			a.Logger.Error("orphaned development update requires recovery", "update_id", st.ID, "error", err)
		}
		// Continue so multiple historical active states left by older builds do
		// not reveal themselves one at a time after each restart.
	}
}

func (a *App) reconcileOrphanedDeveloperUpdate(st devupdate.Status) error {
	p := devupdate.PathsFor(a.Cfg.DataDir)
	backup := filepath.Join(p.Backups, st.ID, "source")
	backupVersion, backupErr := devupdate.SourceTreeVersion(backup)
	workspaceVersion, workspaceErr := devupdate.SourceTreeVersion(a.Cfg.ProjectDir)
	switchAttempted := devUpdateSwitchAttempted(st)
	cancelled := st.CancelRequested || devupdate.CancelRequested(a.Cfg.DataDir, st.ID)

	terminal := func(state, stage, message string, cause error) error {
		st.State, st.Stage, st.Percent, st.Message = state, stage, 100, message
		st.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		st.CancelRequested = cancelled
		if cause != nil {
			st.Error = cause.Error()
		} else {
			st.Error = ""
		}
		devupdate.ClearCancel(a.Cfg.DataDir, st.ID)
		return devupdate.WriteStatus(a.Cfg.DataDir, st)
	}
	restoreBackup := func() error {
		if backupErr != nil {
			return fmt.Errorf("source rollback backup unavailable: %w", backupErr)
		}
		if err := devupdate.ApplySource(backup, a.Cfg.ProjectDir); err != nil {
			return err
		}
		got, err := devupdate.SourceTreeVersion(a.Cfg.ProjectDir)
		if err != nil {
			return err
		}
		if got != backupVersion {
			return fmt.Errorf("restored workspace version %s does not match backup %s", got, backupVersion)
		}
		return nil
	}

	if !switchAttempted {
		state := strings.ToLower(strings.TrimSpace(st.State))
		needsRestore := state == "applying" || state == "building" || state == "rolling_back" || state == "cancel_requested" || state == "recovery_required" || workspaceErr != nil
		if needsRestore {
			if err := restoreBackup(); err != nil {
				joined := fmt.Errorf("independent updater disappeared before controller switch and source restore could not be completed: %w", err)
				_ = terminal("recovery_required", "Manual source recovery required", "The updater helper is gone and the previous source could not be proven restored. New development updates are blocked.", joined)
				return joined
			}
		}
		if cancelled {
			return terminal("cancelled", "Development update cancelled", "Safe cancel completed before the controller switch. The previous source tree is active.", nil)
		}
		return terminal("failed", "Interrupted development update reconciled", "The updater helper stopped before the controller switch. The previous source tree was verified/restored and the running controller was left unchanged.", errors.New("development updater helper exited before completing the update"))
	}

	// After a controller switch, the compiled running version is authoritative.
	// If the old controller is running again, finish the source rollback. If the
	// target controller is running and the helper vanished during verification,
	// only accept it when Docker itself reports healthy.
	if backupErr == nil && strings.TrimSpace(a.Cfg.Version) == backupVersion {
		if err := restoreBackup(); err != nil {
			joined := fmt.Errorf("previous controller is running but source reconciliation failed: %w", err)
			_ = terminal("recovery_required", "Source reconciliation required", "The previous controller is running, but its source tree could not be proven restored.", joined)
			return joined
		}
		return terminal("rolled_back", "Previous version restored", "The updater helper disappeared during switch/recovery, but the previous running controller and source tree were reconciled successfully.", errors.New("development update interrupted during controller switch"))
	}
	if strings.TrimSpace(a.Cfg.Version) == strings.TrimSpace(st.Version) && workspaceErr == nil && workspaceVersion == strings.TrimSpace(st.Version) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		health, healthErr := a.Docker.Run(healthCtx, "", "inspect", "-f", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", a.Cfg.ControllerName)
		cancel()
		if healthErr == nil && strings.TrimSpace(health) == "healthy" && strings.ToLower(strings.TrimSpace(st.State)) != "rolling_back" {
			return terminal("completed", "Development update reconciled", "The updater helper exited after the controller switch, but the target source and healthy target controller are both active.", nil)
		}
	}
	cause := fmt.Errorf("orphaned updater after controller switch: running=%s target=%s backup=%s workspace=%s", a.Cfg.Version, st.Version, backupVersion, workspaceVersion)
	_ = terminal("recovery_required", "Development update recovery required", "The updater helper disappeared after a controller switch and Vibewatch cannot prove a safe terminal state. New development updates are blocked.", cause)
	return cause
}

func (a *App) developerUpdateSupport(ctx context.Context) developerUpdateSupport {
	out := developerUpdateSupport{
		Enabled:        a.Cfg.DeveloperUpdates,
		CurrentVersion: a.Cfg.Version,
		ProjectDir:     a.Cfg.ProjectDir,
	}
	if a.Cfg.DeveloperUpdates {
		a.reconcileDeveloperUpdates(ctx)
	}
	if st, ok := devupdate.LatestStatus(a.Cfg.DataDir); ok {
		out.LastUpdate = &st
		if st.State == "recovery_required" {
			out.Reason = firstNonEmpty(st.Message, "A previous development update requires recovery before another package can be installed.")
			return out
		}
	}
	if !a.Cfg.DeveloperUpdates {
		out.Reason = "Development updates are disabled for this installation. They are enabled automatically by the source-build compose overlay."
		return out
	}
	projectDir := strings.TrimSpace(a.Cfg.ProjectDir)
	if _, _, err := a.developerUpdateMountSources(ctx); err != nil {
		out.Reason = err.Error() + ". Recreate Vibewatch once with docker-compose.build.yml from this patch."
		return out
	}
	out.ProjectMounted = true
	if err := devupdate.ValidateSourceTree(projectDir); err != nil {
		out.Reason = "The mounted project directory does not look like a complete Vibewatch source tree: " + err.Error()
		return out
	}
	if _, err := os.Stat("/usr/local/bin/vibewatch-dev-updater"); err != nil {
		out.Reason = "The development updater helper is missing from this controller image."
		return out
	}
	composeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if cmdOut, cmdErr := exec.CommandContext(composeCtx, "docker", "compose", "version").CombinedOutput(); cmdErr != nil {
		out.Reason = "Docker Compose plugin is unavailable in the controller image: " + strings.TrimSpace(string(cmdOut))
		return out
	}
	out.ComposeReady = true
	preflight := a.developerUpdateDiskPreflight(ctx)
	out.DiskPreflight = &preflight
	out.Available = true
	return out
}

func (a *App) handleDeveloperUpdateInfo(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, a.developerUpdateSupport(r.Context()))
}

func (a *App) handleDeveloperUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	support := a.developerUpdateSupport(r.Context())
	if !support.Available {
		writeErr(w, http.StatusConflict, support.Reason)
		return
	}
	if active, ok := devupdate.ActiveStatus(a.Cfg.DataDir); ok {
		writeErr(w, http.StatusConflict, fmt.Sprintf("development update %s is already %s", active.ID, active.State))
		return
	}
	if disk, err := devupdate.DiskUsage(a.Cfg.DataDir); err != nil {
		writeErr(w, http.StatusInsufficientStorage, "could not verify development-update staging capacity: "+err.Error())
		return
	} else if disk.FreeBytes < devupdate.MinDataFreeBytes || disk.FreeInodes < devupdate.MinFreeInodes {
		writeErr(w, http.StatusInsufficientStorage, fmt.Sprintf("development-update storage has only %s free; at least %s is required before uploading", formatDevBytes(disk.FreeBytes), formatDevBytes(devupdate.MinDataFreeBytes)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, devupdate.MaxUploadBytes+(2<<20))
	if err := r.ParseMultipartForm(devupdate.MaxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "could not read development ZIP: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "select a Vibewatch development ZIP")
		return
	}
	defer file.Close()
	st, err := devupdate.StageArchive(a.Cfg.DataDir, header.Filename, file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "developer-update.upload", 0, "", fmt.Sprintf("id=%s file=%s version=%s sha256=%s", st.ID, st.Filename, st.Version, st.SHA256))
	writeJSON(w, http.StatusCreated, st)
}

func (a *App) handleDeveloperUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	support := a.developerUpdateSupport(r.Context())
	if !support.Available {
		writeErr(w, http.StatusConflict, support.Reason)
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		writeErr(w, http.StatusBadRequest, "development update id is required")
		return
	}
	st, err := devupdate.ReadStatus(a.Cfg.DataDir, strings.TrimSpace(in.ID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "staged development update not found")
		return
	}
	if st.State != "uploaded" {
		writeErr(w, http.StatusConflict, "development update is not in an installable state")
		return
	}
	if active, ok := devupdate.ActiveStatus(a.Cfg.DataDir); ok && active.ID != st.ID {
		writeErr(w, http.StatusConflict, fmt.Sprintf("development update %s is already %s", active.ID, active.State))
		return
	}
	// Resolve the real host-side bind sources before marking this package active.
	// If the source-build mounts disappeared between the support check and this
	// request, the package must remain in the retryable "uploaded" state.
	projectSource, dataSource, mountErr := a.developerUpdateMountSources(r.Context())
	if mountErr != nil {
		writeErr(w, http.StatusConflict, mountErr.Error())
		return
	}
	preflight := a.developerUpdateDiskPreflight(r.Context())
	if !preflight.Ready {
		writeErr(w, http.StatusInsufficientStorage, preflight.Reason)
		return
	}
	stageSource := filepath.Join(devupdate.PathsFor(a.Cfg.DataDir).Staged, st.ID, "source")
	if err := devupdate.ValidateSourceTree(stageSource); err != nil {
		writeErr(w, http.StatusConflict, "staged development source is incomplete: "+err.Error())
		return
	}

	backupCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	backup, err := a.createDatabaseBackup(backupCtx, "developer-update")
	cancel()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "database backup before development update failed: "+err.Error())
		return
	}
	st.DatabaseBackup = backup
	st.State = "queued"
	st.Stage = "Queued"
	st.Percent = 2
	st.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	st.Message = "Database backup created. Launching the independent development updater."
	st.Error = ""
	if err := devupdate.WriteStatus(a.Cfg.DataDir, st); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	launchCtx, launchCancel := context.WithTimeout(r.Context(), 30*time.Second)
	err = a.Docker.LaunchDevelopmentUpdater(launchCtx, a.Cfg.ControllerName, st.ID, a.Cfg.ProjectDir, a.Cfg.DataDir, projectSource, dataSource, backup)
	launchCancel()
	if err != nil {
		st.State = "failed"
		st.Stage = "Updater launch failed"
		st.Percent = 100
		st.Error = err.Error()
		st.Message = "The controller was not changed. The development updater helper could not be started."
		st.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = devupdate.WriteStatus(a.Cfg.DataDir, st)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "developer-update.install", 0, "", fmt.Sprintf("id=%s file=%s version=%s db_backup=%s", st.ID, st.Filename, st.Version, filepath.Base(backup)))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"id":         st.ID,
		"update_url": "/developer-update.html?update=" + st.ID,
	})
}

func (a *App) handleDeveloperUpdateRecover(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		writeErr(w, http.StatusBadRequest, "development update id is required")
		return
	}
	id := strings.TrimSpace(in.ID)
	st, err := devupdate.ReadStatus(a.Cfg.DataDir, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "development update not found")
		return
	}
	if st.State != "recovery_required" && !devupdate.IsActiveState(st.State) {
		writeErr(w, http.StatusConflict, "development update does not require recovery")
		return
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	running, probeErr := a.Docker.DevelopmentUpdaterRunning(probeCtx, id)
	cancel()
	if probeErr != nil {
		writeErr(w, http.StatusBadGateway, probeErr.Error())
		return
	}
	if running {
		writeErr(w, http.StatusConflict, "development updater helper is still running; use Safe cancel or wait for it to finish")
		return
	}
	a.devUpdateMu.Lock()
	err = a.reconcileOrphanedDeveloperUpdate(st)
	a.devUpdateMu.Unlock()
	if next, readErr := devupdate.ReadStatus(a.Cfg.DataDir, id); readErr == nil {
		st = next
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "developer-update.recover", 0, "", "id="+id)
	if err != nil && st.State == "recovery_required" {
		writeJSON(w, http.StatusConflict, st)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *App) handleDeveloperUpdateCancel(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		writeErr(w, http.StatusBadRequest, "development update id is required")
		return
	}
	id := strings.TrimSpace(in.ID)
	st, err := devupdate.ReadStatus(a.Cfg.DataDir, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "development update not found")
		return
	}
	if !devupdate.IsActiveState(st.State) {
		writeJSON(w, http.StatusOK, st)
		return
	}
	if err := devupdate.RequestCancel(a.Cfg.DataDir, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st.CancelRequested = true
	st.Message = "Safe cancel requested. Pre-switch work will stop and restore the previous source; an in-progress controller switch or recovery is allowed to finish atomically."
	_ = devupdate.WriteStatus(a.Cfg.DataDir, st)
	_ = a.Store.Audit(r.Context(), a.actor(r), "developer-update.cancel", 0, "", "id="+id)
	probeCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	running, probeErr := a.Docker.DevelopmentUpdaterRunning(probeCtx, id)
	cancel()
	if probeErr == nil && !running {
		a.reconcileDeveloperUpdates(context.Background())
		if next, readErr := devupdate.ReadStatus(a.Cfg.DataDir, id); readErr == nil {
			st = next
		}
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (a *App) handleDeveloperUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !a.requireOwner(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "development update id is required")
		return
	}
	st, err := devupdate.ReadStatus(a.Cfg.DataDir, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "development update not found")
		return
	}
	writeJSON(w, http.StatusOK, st)
}
