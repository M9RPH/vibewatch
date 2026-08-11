package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *App) handleHostVolumes(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "host not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	volumes, err := a.Docker.VolumeInventory(ctx, h.Endpoint)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_, _, protected := a.rollbackProtectedDockerObjects(hostID)
	for i := range volumes {
		if n := protected[volumes[i].Name]; n > 0 {
			volumes[i].RollbackProtected = true
			volumes[i].RetainedSnapshots = n
			// A retained recovery snapshot may rely on this volume's existing data.
			// Keep it visible as unused when appropriate, but never cleanup-eligible.
		}
	}
	writeJSON(w, http.StatusOK, volumes)
}

func (a *App) handleAnonymousVolumePrune(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "host not found")
		return
	}
	jobID, err := a.Store.CreateJob(r.Context(), "volume-cleanup", "manual", hostID, "unused-anonymous-volumes", "queued")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Store.StartJob(r.Context(), jobID)
	_ = a.Store.AddJobLog(r.Context(), jobID, "info", "docker", "Pruning unused anonymous Docker volumes; named volumes are protected")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	_, _, retainedVolumes := a.rollbackProtectedDockerObjects(hostID)
	protected := map[string]bool{}
	for name := range retainedVolumes {
		protected[name] = true
	}
	result, err := a.Docker.PruneUnusedAnonymousVolumes(ctx, h.Endpoint, protected)
	if err != nil {
		_ = a.Store.AddJobLog(context.Background(), jobID, "error", "docker", err.Error())
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		a.Logger.Error("unused anonymous volume cleanup failed", "host", h.Name, "host_id", hostID, "job_id", jobID, "error", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	summary, _ := json.Marshal(result)
	_ = a.Store.AddJobLog(context.Background(), jobID, "info", "docker", fmt.Sprintf("Removed %d unused anonymous volumes; %d rollback-protected", len(result.RemovedVolumes), result.ProtectedVolumes))
	_ = a.Store.FinishJob(context.Background(), jobID, "success", string(summary), "")
	_ = a.Store.Audit(context.Background(), a.actor(r), "volumes.prune-anonymous", hostID, "", string(summary))
	a.Logger.Info("unused anonymous volume cleanup completed", "host", h.Name, "host_id", hostID, "job_id", jobID, "removed_volumes", len(result.RemovedVolumes))
	writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "result": result})
}

func (a *App) handleNamedVolumeDelete(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "volume name is required")
		return
	}
	h, err := a.Store.Host(r.Context(), hostID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "host not found")
		return
	}
	_, _, retainedVolumes := a.rollbackProtectedDockerObjects(hostID)
	if retainedVolumes[name] > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf("volume %s is referenced by %d retained rollback snapshot(s); refusing deletion", name, retainedVolumes[name]))
		return
	}
	jobID, err := a.Store.CreateJob(r.Context(), "volume-cleanup", "manual", hostID, name, "queued")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = a.Store.StartJob(r.Context(), jobID)
	_ = a.Store.AddJobLog(r.Context(), jobID, "warning", "docker", "Explicitly deleting one unused named Docker volume")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := a.Docker.RemoveUnusedNamedVolume(ctx, h.Endpoint, name); err != nil {
		_ = a.Store.AddJobLog(context.Background(), jobID, "error", "docker", err.Error())
		_ = a.Store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	_ = a.Store.FinishJob(context.Background(), jobID, "success", `{"removed":true}`, "")
	_ = a.Store.Audit(context.Background(), a.actor(r), "volume.delete-named", hostID, "", name)
	a.Logger.Warn("unused named volume deleted", "host", h.Name, "host_id", hostID, "volume", name, "actor", a.actor(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job_id": jobID, "volume": name})
}
