package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/dockercli"
)

type updateHistoryView struct {
	db.UpdateHistory
	RollbackAvailable bool `json:"rollback_available"`
}

func (a *App) currentContainerState(ctx context.Context, hostID int64, name string) (dockercli.Container, db.VersionInfo) {
	v, _ := a.Store.Version(ctx, hostID, name)
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return dockercli.Container{}, v
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return dockercli.Container{}, v
	}
	for _, c := range cs {
		if c.Name == name {
			return c, v
		}
	}
	return dockercli.Container{}, v
}

func (a *App) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	hostID, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	rows, err := a.Store.UpdateHistory(r.Context(), limit, hostID, container)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]updateHistoryView, 0, len(rows))
	for _, x := range rows {
		if !a.hostAllowed(r, x.HostID) {
			continue
		}
		available := false
		if x.Action == "update" && x.Status == "success" && x.SnapshotID != "" {
			if x.RestorePointID > 0 {
				if rp, e := a.Store.RestorePoint(r.Context(), x.RestorePointID); e == nil {
					available, _ = a.restorePointAvailable(r.Context(), rp)
				}
			} else if _, info, e := a.findSnapshotForHistory(x); e == nil && !strings.EqualFold(info.StackType, "swarm") {
				available = true
			}
		}
		out = append(out, updateHistoryView{UpdateHistory: x, RollbackAvailable: available})
	}
	writeJSON(w, 200, out)
}

func (a *App) recordUpdateHistory(req updateRequest, before dockercli.Container, beforeVersion db.VersionInfo, snapshotID string, restorePointID int64, attemptedDigest string, started time.Time, dependencyCount int, dependencyStatus, dependencyDetails, preflightStatus, preflightDetails, verificationStatus, verificationDetails string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	job, err := a.Store.Job(ctx, req.JobID)
	if err != nil {
		return
	}
	after, afterVersion := a.currentContainerState(ctx, req.HostID, req.Container)
	toDigest := after.ImageID
	if toDigest == "" {
		toDigest = before.ImageID
	}
	toRef := after.Image
	if toRef == "" {
		toRef = before.Image
	}
	toVersion := afterVersion.Installed
	if toVersion == "" {
		toVersion = beforeVersion.Installed
	}
	if job.Status == "failed" && strings.TrimSpace(attemptedDigest) != "" && strings.EqualFold(strings.TrimSpace(toDigest), strings.TrimSpace(before.ImageID)) {
		toDigest = strings.TrimSpace(attemptedDigest)
		if strings.TrimSpace(beforeVersion.Latest) != "" {
			toVersion = beforeVersion.Latest
		}
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "system"
	}
	_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: req.HostID, ContainerName: req.Container, Action: "update", Trigger: req.Trigger, Actor: actor, Status: job.Status, FromVersion: beforeVersion.Installed, ToVersion: toVersion, FromImageRef: before.Image, ToImageRef: toRef, FromDigest: before.ImageID, ToDigest: toDigest, SnapshotID: snapshotID, RestorePointID: restorePointID, DurationMS: time.Since(started).Milliseconds(), Error: job.Error, DependencyCount: dependencyCount, DependencyStatus: dependencyStatus, DependencyDetails: dependencyDetails, PreflightStatus: preflightStatus, PreflightDetails: preflightDetails, VerificationStatus: verificationStatus, VerificationDetails: verificationDetails})
}
