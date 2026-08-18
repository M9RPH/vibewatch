package app

import (
	"context"
	"encoding/json"
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
	Enabled        bool              `json:"enabled"`
	Available      bool              `json:"available"`
	Reason         string            `json:"reason,omitempty"`
	CurrentVersion string            `json:"current_version"`
	ProjectDir     string            `json:"project_dir,omitempty"`
	ProjectMounted bool              `json:"project_mounted"`
	ComposeReady   bool              `json:"compose_ready"`
	LastUpdate     *devupdate.Status `json:"last_update,omitempty"`
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

func (a *App) developerUpdateSupport(ctx context.Context) developerUpdateSupport {
	out := developerUpdateSupport{
		Enabled:        a.Cfg.DeveloperUpdates,
		CurrentVersion: a.Cfg.Version,
		ProjectDir:     a.Cfg.ProjectDir,
	}
	if st, ok := devupdate.LatestStatus(a.Cfg.DataDir); ok {
		out.LastUpdate = &st
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
	for _, required := range []string{"docker-compose.yml", "docker-compose.build.yml", "Dockerfile", "go.mod"} {
		if st, statErr := os.Stat(filepath.Join(projectDir, required)); statErr != nil || st.IsDir() {
			out.Reason = "The mounted project directory does not look like a Vibewatch source tree."
			return out
		}
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
	backupCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	backup, err := a.createDatabaseBackup(backupCtx, "developer-update")
	cancel()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "database backup before development update failed: "+err.Error())
		return
	}
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
