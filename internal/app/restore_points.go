package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

type restorePointView struct {
	db.RestorePoint
	HostName          string                       `json:"host_name"`
	RollbackAvailable bool                         `json:"rollback_available"`
	SnapshotAvailable bool                         `json:"snapshot_available"`
	ProtectionLevel   string                       `json:"protection_level"`
	Dependencies      []networkNamespaceDependency `json:"dependencies,omitempty"`
}

var restoreRepoClean = regexp.MustCompile(`[^a-z0-9._-]+`)
var restoreTagClean = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func restoreImageRef(hostID int64, container, snapshotID string) string {
	name := strings.ToLower(strings.TrimSpace(container))
	name = restoreRepoClean.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-_")
	if name == "" {
		name = "container"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	tag := restoreTagClean.ReplaceAllString(strings.TrimSpace(snapshotID), "-")
	tag = strings.Trim(tag, ".-_")
	if tag == "" {
		tag = time.Now().UTC().Format("20060102T150405Z")
	}
	if len(tag) > 120 {
		tag = tag[:120]
	}
	return fmt.Sprintf("vibewatch-restore/host-%d/%s:%s", hostID, name, tag)
}

func (a *App) findSnapshotByID(hostID int64, snapshotID, container string) (string, snapshotInfo, error) {
	if hostID <= 0 || strings.TrimSpace(snapshotID) == "" {
		return "", snapshotInfo{}, fmt.Errorf("invalid snapshot reference")
	}
	root := filepath.Join(a.containerBackupRoot(), fmt.Sprintf("host-%d", hostID))
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())) == snapshotID && strings.HasSuffix(strings.ToLower(d.Name()), ".zip") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", snapshotInfo{}, fmt.Errorf("recovery snapshot no longer exists")
	}
	info, _, err := readSnapshotInfo(found)
	if err != nil {
		return "", snapshotInfo{}, err
	}
	if info.HostID != hostID {
		return "", snapshotInfo{}, fmt.Errorf("snapshot host mismatch")
	}
	if strings.TrimSpace(container) != "" && !containsString(info.Containers, container) {
		return "", snapshotInfo{}, fmt.Errorf("snapshot does not contain container %s", container)
	}
	return found, info, nil
}

func (a *App) createRestorePointForSnapshot(ctx context.Context, hostID int64, container string, snap ContainerBackupSnapshot, reason, trigger string, deps []networkNamespaceDependencyRuntime) (db.RestorePoint, error) {
	path, info, err := a.findSnapshotByID(hostID, snap.ID, container)
	if err != nil {
		return db.RestorePoint{}, err
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		return db.RestorePoint{}, err
	}
	old, err := findInspectForContainer(raw, container)
	if err != nil {
		return db.RestorePoint{}, err
	}
	version, _ := a.Store.Version(ctx, hostID, container)
	cache, _ := a.Store.Cache(ctx, hostID, container)
	volumes, binds := 0, 0
	for _, m := range old.Mounts {
		switch m.Type {
		case "volume":
			volumes++
		case "bind":
			binds++
		}
	}
	base := db.RestorePoint{
		HostID:              hostID,
		ContainerName:       container,
		SnapshotID:          snap.ID,
		Reason:              reason,
		Trigger:             trigger,
		OriginalImageRef:    old.Config.Image,
		OriginalImageID:     old.Image,
		TargetDigest:        strings.TrimSpace(cache.LatestDigest),
		FromVersion:         version.Installed,
		UnitKind:            info.UnitKind,
		UnitKey:             info.UnitKey,
		StackType:           info.StackType,
		ConfigProtected:     db.Bool(true),
		VolumeDataProtected: db.Bool(false),
		VolumeCount:         volumes,
		BindCount:           binds,
		DependencyCount:     len(deps),
		DependenciesJSON:    dependencyRecordsJSON(deps),
	}
	if strings.EqualFold(info.StackType, "swarm") || strings.TrimSpace(old.Config.Labels["com.docker.swarm.service.name"]) != "" {
		base.Status = "config_only"
		id, e := a.Store.AddRestorePoint(ctx, base)
		if e != nil {
			return db.RestorePoint{}, e
		}
		base.ID = id
		return base, nil
	}
	// Validate that the captured runtime configuration can be translated back
	// into a safe docker create command before allowing the destructive update.
	// This makes "full restore point ready" a stronger promise than simply
	// having a committed filesystem image.
	if _, _, recreateErr := createArgsFromInspect(old, "vibewatch-restore-validation:latest"); recreateErr != nil {
		base.Status = "degraded"
		base.LastError = recreateErr.Error()
		id, addErr := a.Store.AddRestorePoint(context.Background(), base)
		if addErr == nil {
			base.ID = id
		}
		return base, recreateErr
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return db.RestorePoint{}, err
	}
	ref := restoreImageRef(hostID, container, snap.ID)
	base.ImageRef = ref
	commitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	imageID, commitErr := a.Docker.Run(commitCtx, h.Endpoint,
		"commit", "--pause=true",
		"--change", "LABEL io.vibewatch.restore-point="+snap.ID,
		"--change", "LABEL io.vibewatch.container="+container,
		container, ref,
	)
	imageID = strings.TrimSpace(imageID)
	if commitErr != nil || imageID == "" {
		base.Status = "degraded"
		base.LastError = "writable-layer capture failed"
		if commitErr != nil {
			base.LastError = commitErr.Error()
		}
		id, addErr := a.Store.AddRestorePoint(context.Background(), base)
		if addErr == nil {
			base.ID = id
		}
		if commitErr != nil {
			return base, commitErr
		}
		return base, fmt.Errorf("docker commit returned no image id")
	}
	base.ImageID = imageID
	base.Status = "ready"
	base.WritableLayer = db.Bool(true)
	id, err := a.Store.AddRestorePoint(ctx, base)
	if err != nil {
		_, _ = a.Docker.Run(context.Background(), h.Endpoint, "image", "rm", ref)
		return db.RestorePoint{}, err
	}
	base.ID = id
	_ = a.Store.Audit(context.Background(), "system", "restore-point.create", hostID, container, fmt.Sprintf("restore_point=%d snapshot=%s image=%s", id, snap.ID, ref))
	return base, nil
}

func (a *App) expireRestorePointsForSnapshot(ctx context.Context, hostID int64, snapshotID string) {
	points, err := a.Store.ExpireRestorePointsBySnapshot(ctx, hostID, snapshotID)
	if err != nil || len(points) == 0 {
		return
	}
	h, hostErr := a.Store.Host(ctx, hostID)
	removedDependencySnapshots := map[string]bool{}
	for _, rp := range points {
		// Cross-stack namespace dependents may have received a dedicated recovery
		// snapshot for this transaction. Once the parent restore point expires,
		// remove those transaction-only snapshots as well so retention, object
		// protection and restore availability expire atomically.
		for _, dep := range restorePointDependencies(rp) {
			depSnapshotID := strings.TrimSpace(dep.SnapshotID)
			if depSnapshotID == "" || depSnapshotID == snapshotID || removedDependencySnapshots[depSnapshotID] {
				continue
			}
			if depPath, _, depErr := a.findSnapshotByID(hostID, depSnapshotID, dep.SourceContainer); depErr == nil {
				if removeErr := os.Remove(depPath); removeErr != nil && !os.IsNotExist(removeErr) && a.Logger != nil {
					a.Logger.Warn("expired dependency snapshot could not be removed", "host_id", hostID, "restore_point", rp.ID, "snapshot", depSnapshotID, "error", removeErr)
				}
			}
			removedDependencySnapshots[depSnapshotID] = true
		}
		if hostErr == nil && strings.TrimSpace(rp.ImageRef) != "" {
			if _, e := a.Docker.Run(ctx, h.Endpoint, "image", "rm", rp.ImageRef); e != nil && a.Logger != nil {
				a.Logger.Warn("expired restore image could not be untagged", "host_id", hostID, "restore_point", rp.ID, "image", rp.ImageRef, "error", e)
			}
		}
		_ = a.Store.Audit(context.Background(), "system", "restore-point.expire", hostID, rp.ContainerName, fmt.Sprintf("restore_point=%d snapshot=%s", rp.ID, snapshotID))
	}
}

func restorePointProtectionLevel(rp db.RestorePoint) string {
	if bool(rp.WritableLayer) {
		return "full_container"
	}
	if bool(rp.ConfigProtected) {
		return "config_only"
	}
	return "degraded"
}

func (a *App) restorePointAvailable(ctx context.Context, rp db.RestorePoint) (bool, bool) {
	_, _, snapErr := a.findSnapshotByID(rp.HostID, rp.SnapshotID, rp.ContainerName)
	snapshotAvailable := snapErr == nil
	if !snapshotAvailable || rp.Status == "expired" || rp.Status == "failed" || !bool(rp.WritableLayer) || strings.TrimSpace(rp.ImageRef) == "" {
		return false, snapshotAvailable
	}
	if err := a.dependencySnapshotsAvailable(rp); err != nil {
		return false, snapshotAvailable
	}
	h, err := a.Store.Host(ctx, rp.HostID)
	if err != nil {
		return false, snapshotAvailable
	}
	return a.Docker.ImageExists(ctx, h.Endpoint, rp.ImageRef), snapshotAvailable
}

func (a *App) handleRestorePoints(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hostID, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	rows, err := a.Store.RestorePoints(r.Context(), limit, hostID, container)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]restorePointView, 0, len(rows))
	for _, rp := range rows {
		if !a.hostAllowed(r, rp.HostID) {
			continue
		}
		hostName := fmt.Sprintf("Host %d", rp.HostID)
		if h, e := a.Store.Host(r.Context(), rp.HostID); e == nil {
			hostName = h.Name
		}
		available, snapshotAvailable := a.restorePointAvailable(r.Context(), rp)
		if !snapshotAvailable && rp.Status != "expired" {
			// Expiry is an atomic restore-point cleanup operation: mark every
			// point linked to the snapshot expired and untag its internal restore
			// image so retention and Docker cleanup stay in sync.
			a.expireRestorePointsForSnapshot(context.Background(), rp.HostID, rp.SnapshotID)
			rp.Status = "expired"
		} else if bool(rp.WritableLayer) && snapshotAvailable && !available && rp.Status != "expired" {
			reason := "writable-layer restore image is unavailable"
			if depErr := a.dependencySnapshotsAvailable(rp); depErr != nil {
				reason = depErr.Error()
			}
			_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", reason)
			rp.Status = "degraded"
			rp.LastError = reason
		}
		out = append(out, restorePointView{RestorePoint: rp, HostName: hostName, RollbackAvailable: available, SnapshotAvailable: snapshotAvailable, ProtectionLevel: restorePointProtectionLevel(rp), Dependencies: restorePointDependencies(rp)})
	}
	writeJSON(w, 200, out)
}

func (a *App) snoozeDigestAfterRollback(ctx context.Context, hostID int64, container, digest string) {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return
	}
	cache, err := a.Store.Cache(ctx, hostID, container)
	if err != nil {
		return
	}
	cache.SnoozedDigest = digest
	cache.SnoozedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.EqualFold(strings.TrimSpace(cache.LatestDigest), digest) {
		cache.UpdateAvailable = db.Bool(false)
	}
	_ = a.Store.SaveCache(ctx, cache)
}

func (a *App) snoozeLatestAfterRollback(ctx context.Context, hostID int64, container, fallbackDigest string) string {
	cache, err := a.Store.Cache(ctx, hostID, container)
	if err != nil {
		a.snoozeDigestAfterRollback(ctx, hostID, container, fallbackDigest)
		return strings.TrimSpace(fallbackDigest)
	}
	candidate := strings.TrimSpace(cache.LatestDigest)
	if candidate == "" || strings.EqualFold(candidate, strings.TrimSpace(cache.CurrentDigest)) {
		candidate = strings.TrimSpace(fallbackDigest)
	}
	if candidate == "" || strings.EqualFold(candidate, strings.TrimSpace(cache.CurrentDigest)) {
		return ""
	}
	a.snoozeDigestAfterRollback(ctx, hostID, container, candidate)
	return candidate
}

func (a *App) inspectOne(ctx context.Context, hostID int64, container string) (inspectContainer, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return inspectContainer{}, err
	}
	raw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, container)
	if err != nil {
		return inspectContainer{}, err
	}
	var xs []inspectContainer
	if err := json.Unmarshal(raw, &xs); err != nil || len(xs) == 0 {
		if err != nil {
			return inspectContainer{}, err
		}
		return inspectContainer{}, fmt.Errorf("container inspect returned no result")
	}
	return xs[0], nil
}

func (a *App) verifyUpdatedContainer(ctx context.Context, hostID int64, container string) error {
	deadline := time.Now().Add(12 * time.Second)
	// Containers without a Docker healthcheck still get a short stability
	// window. We intentionally do not guess application health, but this catches
	// the common case where a new image starts and immediately crash-loops.
	noHealthStableSince := time.Time{}
	var lastErr error
	for {
		cur, err := a.inspectOne(ctx, hostID, container)
		if err != nil {
			lastErr = err
		} else {
			if cur.State.Restarting {
				return fmt.Errorf("container is restarting after update")
			}
			if !cur.State.Running || (cur.State.Status != "" && cur.State.Status != "running") {
				return fmt.Errorf("container is not running after update (state=%s exit=%d)", cur.State.Status, cur.State.ExitCode)
			}
			if cur.State.Health != nil {
				noHealthStableSince = time.Time{}
				switch strings.ToLower(strings.TrimSpace(cur.State.Health.Status)) {
				case "unhealthy":
					return fmt.Errorf("container healthcheck reports unhealthy after update")
				case "healthy":
					return nil
				}
			} else {
				// No Docker healthcheck exists. Running state is the strongest safe
				// signal Vibewatch can use without application-specific knowledge,
				// so require it to remain stable briefly instead of returning on the
				// first successful inspect.
				if noHealthStableSince.IsZero() {
					noHealthStableSince = time.Now()
				}
				if time.Since(noHealthStableSince) >= 4*time.Second {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			// A healthcheck that is still "starting" is not proof of failure.
			// Only explicit unhealthy/stopped/restarting states trigger rollback.
			if lastErr != nil {
				return lastErr
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) shouldAutoRollback(ctx context.Context, rp db.RestorePoint) bool {
	if rp.ID <= 0 || !bool(rp.WritableLayer) || strings.TrimSpace(rp.ImageRef) == "" || strings.EqualFold(rp.StackType, "swarm") {
		return false
	}
	cur, err := a.inspectOne(ctx, rp.HostID, rp.ContainerName)
	if err != nil {
		return true
	}
	if !cur.State.Running || cur.State.Restarting || strings.EqualFold(strings.TrimSpace(cur.State.Status), "exited") || strings.EqualFold(strings.TrimSpace(cur.State.Status), "dead") {
		return true
	}
	if cur.State.Health != nil && strings.EqualFold(strings.TrimSpace(cur.State.Health.Status), "unhealthy") {
		return true
	}
	// If Watchtower failed before replacing the container, the original image is
	// still running and there is nothing to repair automatically.
	return !strings.EqualFold(strings.TrimSpace(cur.Image), strings.TrimSpace(rp.OriginalImageID))
}

func (a *App) runAutomaticRollback(req updateRequest, rp db.RestorePoint, cause error) (bool, error) {
	if !a.shouldAutoRollback(a.ctx, rp) {
		return false, nil
	}
	id, err := a.Store.CreateJob(a.ctx, "rollback", "automatic", rp.HostID, rp.ContainerName, "queued")
	if err != nil {
		return true, err
	}
	message := "Automatic rollback queued after failed update"
	if cause != nil {
		message += ": " + cause.Error()
	}
	_ = a.Store.AddJobLog(a.ctx, id, "WARN", "rollback", message)
	a.jobProgress(a.ctx, id, 5, "Automatic rollback queued")
	_ = a.Store.Audit(a.ctx, "system", "rollback.auto.queue", rp.HostID, rp.ContainerName, fmt.Sprintf("restore_point=%d update_job=%d", rp.ID, req.JobID))
	return true, a.executeRestorePointRollback(id, rp, "system", "automatic")
}
