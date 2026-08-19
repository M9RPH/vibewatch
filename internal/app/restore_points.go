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

	"github.com/m9rph/vibewatch/internal/db"
)

type restorePointDataMountView struct {
	Key          string   `json:"key"`
	Type         string   `json:"type"`
	Name         string   `json:"name,omitempty"`
	Source       string   `json:"source"`
	Destinations []string `json:"destinations,omitempty"`
	Owners       []string `json:"owners,omitempty"`
	StorageClass string   `json:"storage_class"`
	FSType       string   `json:"fs_type,omitempty"`
	SizeBytes    int64    `json:"size_bytes"`
}

type restorePointView struct {
	db.RestorePoint
	HostName           string                       `json:"host_name"`
	RollbackAvailable  bool                         `json:"rollback_available"`
	SnapshotAvailable  bool                         `json:"snapshot_available"`
	ProtectionLevel    string                       `json:"protection_level"`
	Dependencies       []networkNamespaceDependency `json:"dependencies,omitempty"`
	DataScopeType      string                       `json:"data_scope_type,omitempty"`
	DataScopeKey       string                       `json:"data_scope_key,omitempty"`
	DataMounts         []restorePointDataMountView  `json:"data_mounts,omitempty"`
	SnapshotBytes      int64                        `json:"snapshot_bytes"`
	RestoreImageBytes  int64                        `json:"restore_image_bytes"`
	ArchiveBytes       int64                        `json:"archive_bytes"`
	FootprintBytes     int64                        `json:"footprint_bytes"`
	FootprintEstimated bool                         `json:"footprint_estimated"`
	ChainRunID         int64                        `json:"chain_run_id,omitempty"`
	ChainID            int64                        `json:"chain_id,omitempty"`
	ChainName          string                       `json:"chain_name,omitempty"`
	ChainTrigger       string                       `json:"chain_trigger,omitempty"`
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

type restorePointCaptureOptions struct {
	CaptureData                bool
	DeferWriterRestart         map[string]bool
	ContinuityGuardHeld        bool
	HoldContinuityAfterCapture bool
}

type restorePointCaptureResult struct {
	RestorePoint        db.RestorePoint
	DeferredWriters     []string
	RestartedWriters    []string
	ContinuityHeld      bool
	ContinuityExclusive bool
	ReleaseContinuity   func()
}

func (a *App) createRestorePointForSnapshotWithOptions(ctx context.Context, hostID int64, container string, snap ContainerBackupSnapshot, reason, trigger string, deps []networkNamespaceDependencyRuntime, opts restorePointCaptureOptions) (restorePointCaptureResult, error) {
	path, info, err := a.findSnapshotByID(hostID, snap.ID, container)
	if err != nil {
		return restorePointCaptureResult{}, err
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		return restorePointCaptureResult{}, err
	}
	old, err := findInspectForContainer(raw, container)
	if err != nil {
		return restorePointCaptureResult{}, err
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
			return restorePointCaptureResult{}, e
		}
		base.ID = id
		return restorePointCaptureResult{RestorePoint: base}, nil
	}
	if _, _, recreateErr := createArgsFromInspect(old, "vibewatch-restore-validation:latest"); recreateErr != nil {
		base.Status = "degraded"
		base.LastError = recreateErr.Error()
		id, addErr := a.Store.AddRestorePoint(context.Background(), base)
		if addErr == nil {
			base.ID = id
		}
		return restorePointCaptureResult{RestorePoint: base}, recreateErr
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return restorePointCaptureResult{}, err
	}
	profile, protectedMounts, dataConfigured, dataErr := a.selectedDataProtectionMounts(ctx, hostID, container)
	if dataErr != nil {
		return restorePointCaptureResult{}, fmt.Errorf("load data protection profile: %w", dataErr)
	}
	dataConfigured = dataConfigured && opts.CaptureData
	continuityHeldHere := false
	continuityExclusive := false
	releaseContinuity := func() {}
	if dataConfigured && !opts.ContinuityGuardHeld {
		for _, deferred := range opts.DeferWriterRestart {
			if deferred {
				return restorePointCaptureResult{}, fmt.Errorf("deferred data-writer restart requires an outer continuity guard")
			}
		}
		// A cold snapshot only needs to stop the rest of the fleet when the
		// protected target itself provides DNS. Ordinary data writers can be
		// captured concurrently with work on other hosts.
		lockedCtx, release, exclusive := a.acquireContinuityMutation(ctx, old)
		ctx = lockedCtx
		releaseContinuity = release
		continuityHeldHere = true
		continuityExclusive = exclusive
	}
	defer func() {
		if continuityHeldHere {
			releaseContinuity()
		}
	}()

	var stoppedWriters []string
	if dataConfigured {
		stoppedWriters, err = a.stopDataWriters(ctx, hostID, protectedMounts)
		if err != nil {
			return restorePointCaptureResult{}, fmt.Errorf("prepare cold data restore point: %w", err)
		}
	}
	deferred := []string{}
	immediate := []string{}
	for _, name := range stoppedWriters {
		if opts.DeferWriterRestart != nil && opts.DeferWriterRestart[name] {
			deferred = append(deferred, name)
		} else {
			immediate = append(immediate, name)
		}
	}
	completed := false
	defer func() {
		if completed || len(stoppedWriters) == 0 {
			return
		}
		restartCtx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
		defer cancel()
		if restartErr := a.ensureDataWritersRunning(restartCtx, hostID, stoppedWriters); restartErr != nil {
			if a.Logger != nil {
				a.Logger.Error("data writers failed to recover after aborted restore-point capture", "host_id", hostID, "container", container, "error", restartErr)
			}
			return
		}
		if containsString(stoppedWriters, container) {
			recoveryErr := a.verifyUpdatedContainer(restartCtx, hostID, container)
			if recoveryErr == nil {
				recoveryErr = a.probeRecoveredApplication(restartCtx, hostID, container)
			}
			if recoveryErr == nil && containerProvidesDNS(old) {
				recoveryErr = a.verifyDNSControlPlaneRecovery(restartCtx, old)
			}
			if recoveryErr != nil && a.Logger != nil {
				a.Logger.Error("protected service failed continuity recovery after aborted restore-point capture", "host_id", hostID, "container", container, "error", recoveryErr)
			}
		}
	}()

	ref := restoreImageRef(hostID, container, snap.ID)
	base.ImageRef = ref
	commitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	commitArgs := []string{
		"commit", "--pause=true",
		"--change", "LABEL io.vibewatch.restore-point=" + snap.ID,
		"--change", "LABEL io.vibewatch.container=" + container,
	}
	// Persist immutable restore provenance inside the commit image itself. The
	// DB/config-snapshot retention policy may legitimately delete older metadata
	// while a restored container keeps running for months; deterministic forward
	// updates must therefore not depend on that external metadata surviving.
	if originalID := strings.TrimSpace(old.Image); originalID != "" {
		commitArgs = append(commitArgs, "--change", "LABEL io.vibewatch.restore-original-image-id="+originalID)
	}
	if originalRef := strings.TrimSpace(old.Config.Image); originalRef != "" {
		commitArgs = append(commitArgs, "--change", "LABEL io.vibewatch.restore-original-image-ref="+originalRef)
	}
	commitArgs = append(commitArgs, container, ref)
	imageID, commitErr := a.Docker.Run(commitCtx, h.Endpoint, commitArgs...)
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
			return restorePointCaptureResult{RestorePoint: base}, commitErr
		}
		return restorePointCaptureResult{RestorePoint: base}, fmt.Errorf("docker commit returned no image id")
	}
	base.ImageID = imageID
	base.Status = "ready"
	base.WritableLayer = db.Bool(true)
	var dataManifest dataArchiveManifest
	if dataConfigured {
		dataManifest, err = a.captureDataArchive(ctx, hostID, h.Endpoint, profile, snap.ID, protectedMounts)
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
			_ = a.removeDataRestoreArtifacts(cleanupCtx, hostID, dataManifest)
			cleanupCancel()
			_, _ = a.Docker.Run(context.Background(), h.Endpoint, "image", "rm", ref)
			return restorePointCaptureResult{}, fmt.Errorf("data restore point capture failed: %w", err)
		}
		manifestJSON, _ := json.Marshal(dataManifest)
		base.VolumeDataProtected = db.Bool(len(dataManifest.Entries) > 0)
		base.DataManifestJSON = string(manifestJSON)
		base.DataBytes = dataManifest.TotalBytes
	}
	if len(immediate) > 0 {
		restartCtx, restartCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
		restartErr := a.ensureDataWritersRunning(restartCtx, hostID, immediate)
		restartCancel()
		if restartErr != nil {
			if dataConfigured {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
				_ = a.removeDataRestoreArtifacts(cleanupCtx, hostID, dataManifest)
				cleanupCancel()
			}
			_, _ = a.Docker.Run(context.Background(), h.Endpoint, "image", "rm", ref)
			return restorePointCaptureResult{}, fmt.Errorf("data writers did not recover after restore-point capture: %w", restartErr)
		}
	}
	if continuityHeldHere && !opts.HoldContinuityAfterCapture {
		releaseContinuity()
		continuityHeldHere = false
	}
	if dataConfigured {
		_ = a.Store.InvalidateHostStorageCache(context.Background(), hostID)
	}
	id, err := a.Store.AddRestorePoint(ctx, base)
	if err != nil {
		_, _ = a.Docker.Run(context.Background(), h.Endpoint, "image", "rm", ref)
		if dataConfigured {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
			_ = a.removeDataRestoreArtifacts(cleanupCtx, hostID, dataManifest)
			cleanupCancel()
		}
		return restorePointCaptureResult{}, err
	}
	base.ID = id
	completed = true
	result := restorePointCaptureResult{RestorePoint: base, DeferredWriters: deferred, RestartedWriters: immediate}
	if continuityHeldHere && opts.HoldContinuityAfterCapture {
		// Transfer only lock ownership metadata, never the context used while the
		// restore point was captured. Callers intentionally wrap capture in a
		// bounded timeout; exporting that child context would make the subsequent
		// update pipeline inherit a context that is cancelled as soon as capture
		// returns. The caller re-attaches the continuity marker to its own durable
		// job context while this release function keeps the actual RWMutex held.
		result.ContinuityHeld = true
		result.ContinuityExclusive = continuityExclusive
		result.ReleaseContinuity = releaseContinuity
		continuityHeldHere = false // ownership transferred to the update pipeline
	}
	_ = a.Store.Audit(context.Background(), "system", "restore-point.create", hostID, container, fmt.Sprintf("restore_point=%d snapshot=%s image=%s deferred_writers=%s", id, snap.ID, ref, strings.Join(deferred, ",")))
	return result, nil
}

func (a *App) expireRestorePointsForSnapshot(ctx context.Context, hostID int64, snapshotID string) {
	if a.restoreSnapshotPinned(ctx, hostID, snapshotID) {
		if a.Logger != nil {
			a.Logger.Warn("restore point retention skipped pinned unresolved recovery snapshot", "host_id", hostID, "snapshot", snapshotID)
		}
		return
	}
	points, err := a.Store.ExpireRestorePointsBySnapshot(ctx, hostID, snapshotID)
	if err != nil || len(points) == 0 {
		return
	}
	h, hostErr := a.Store.Host(ctx, hostID)
	removedDependencySnapshots := map[string]bool{}
	for _, rp := range points {
		if bool(rp.VolumeDataProtected) && strings.TrimSpace(rp.DataManifestJSON) != "" {
			if manifest, manifestErr := decodeDataManifest(rp.DataManifestJSON); manifestErr == nil && len(manifest.Entries) > 0 {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
				if cleanupErr := a.removeDataRestoreArtifacts(cleanupCtx, rp.HostID, manifest); cleanupErr != nil && a.Logger != nil {
					a.Logger.Warn("expired data restore point could not be removed", "host_id", rp.HostID, "restore_point", rp.ID, "error", cleanupErr)
				}
				cleanupCancel()
			}
		}
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

func restorePointDataDetails(rp db.RestorePoint) (string, string, []restorePointDataMountView) {
	manifest, err := decodeDataManifest(rp.DataManifestJSON)
	if err != nil || len(manifest.Entries) == 0 {
		return "", "", nil
	}
	mounts := make([]restorePointDataMountView, 0, len(manifest.Entries))
	for _, e := range manifest.Entries {
		mounts = append(mounts, restorePointDataMountView{Key: e.Key, Type: e.Type, Name: e.Name, Source: e.Source, Destinations: e.Destinations, Owners: e.Owners, StorageClass: e.StorageClass, FSType: e.FSType, SizeBytes: e.SizeBytes})
	}
	return manifest.ScopeType, manifest.ScopeKey, mounts
}

func restorePointChainRunID(trigger string) int64 {
	t := strings.TrimSpace(trigger)
	for _, prefix := range []string{"chain-auto:", "chain-recreate:", "chain:"} {
		if strings.HasPrefix(t, prefix) {
			id, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(t, prefix)), 10, 64)
			return id
		}
	}
	return 0
}

func restorePointProtectionLevel(rp db.RestorePoint) string {
	if bool(rp.WritableLayer) && bool(rp.VolumeDataProtected) {
		return "full_application"
	}
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

func restorePointStorageMetrics(rp db.RestorePoint, snapshotBytes, imageBytes int64) (archiveBytes, footprintBytes int64, estimated bool) {
	if snapshotBytes < 0 {
		snapshotBytes = 0
	}
	dataBytes := rp.DataBytes
	if dataBytes < 0 {
		dataBytes = 0
	}
	if imageBytes < 0 {
		imageBytes = 0
	}
	archiveBytes = snapshotBytes + dataBytes
	footprintBytes = archiveBytes + imageBytes
	// Docker reports the logical image size. Restore images normally share base
	// layers with the source image, so physical disk consumption can be lower.
	estimated = imageBytes > 0
	return
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
	chainRunCache := map[int64]db.UpdateChainRun{}
	chainRunMissing := map[int64]bool{}
	imageSizeCache := map[int64]map[string]int64{}
	imageSizeLoaded := map[int64]bool{}
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
		dataScopeType, dataScopeKey, dataMounts := restorePointDataDetails(rp)
		snapshotBytes := int64(0)
		if snapshotPath, _, snapshotErr := a.findSnapshotByID(rp.HostID, rp.SnapshotID, rp.ContainerName); snapshotErr == nil {
			if stat, statErr := os.Stat(snapshotPath); statErr == nil {
				snapshotBytes = stat.Size()
			}
		}
		restoreImageBytes := int64(0)
		if bool(rp.WritableLayer) && (strings.TrimSpace(rp.ImageID) != "" || strings.TrimSpace(rp.ImageRef) != "") {
			if !imageSizeLoaded[rp.HostID] {
				imageSizeLoaded[rp.HostID] = true
				index := map[string]int64{}
				if h, hostErr := a.Store.Host(r.Context(), rp.HostID); hostErr == nil {
					if images, imageErr := a.Docker.ImageInventory(r.Context(), h.Endpoint); imageErr == nil {
						for _, image := range images {
							if strings.TrimSpace(image.ID) != "" {
								index[strings.TrimSpace(image.ID)] = image.SizeBytes
							}
							for _, tag := range image.RepoTags {
								if strings.TrimSpace(tag) != "" {
									index[strings.TrimSpace(tag)] = image.SizeBytes
								}
							}
						}
					}
				}
				imageSizeCache[rp.HostID] = index
			}
			if index := imageSizeCache[rp.HostID]; index != nil {
				restoreImageBytes = index[strings.TrimSpace(rp.ImageID)]
				if restoreImageBytes <= 0 {
					restoreImageBytes = index[strings.TrimSpace(rp.ImageRef)]
				}
			}
		}
		archiveBytes, footprintBytes, footprintEstimated := restorePointStorageMetrics(rp, snapshotBytes, restoreImageBytes)
		view := restorePointView{RestorePoint: rp, HostName: hostName, RollbackAvailable: available, SnapshotAvailable: snapshotAvailable, ProtectionLevel: restorePointProtectionLevel(rp), Dependencies: restorePointDependencies(rp), DataScopeType: dataScopeType, DataScopeKey: dataScopeKey, DataMounts: dataMounts, SnapshotBytes: snapshotBytes, RestoreImageBytes: restoreImageBytes, ArchiveBytes: archiveBytes, FootprintBytes: footprintBytes, FootprintEstimated: footprintEstimated}
		if runID := restorePointChainRunID(rp.Trigger); runID > 0 {
			view.ChainRunID = runID
			run, cached := chainRunCache[runID]
			if !cached && !chainRunMissing[runID] {
				if loaded, runErr := a.Store.UpdateChainRun(r.Context(), runID); runErr == nil {
					run = loaded
					chainRunCache[runID] = loaded
					cached = true
				} else {
					chainRunMissing[runID] = true
				}
			}
			if cached {
				view.ChainID = run.ChainID
				view.ChainName = run.ChainName
				view.ChainTrigger = run.Trigger
			}
		}
		out = append(out, view)
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

func dockerHealthVerificationWindow(hc *inspectHealthcheck) time.Duration {
	if hc == nil {
		return 12 * time.Second
	}
	// Docker health transitions can legitimately lag behind container startup,
	// especially when an image has a start period or a long health interval. A
	// transient unhealthy result is therefore treated as recoverable during this
	// bounded grace window instead of triggering an immediate rollback.
	window := 45 * time.Second
	startPeriod := time.Duration(hc.StartPeriod)
	interval := time.Duration(hc.Interval)
	timeout := time.Duration(hc.Timeout)
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout < 0 {
		timeout = 0
	}
	derived := startPeriod + 2*interval + timeout + 10*time.Second
	if derived > window {
		window = derived
	}
	if window > 2*time.Minute {
		window = 2 * time.Minute
	}
	return window
}

const containerStartupVerificationWindow = 45 * time.Second

func (a *App) verifyUpdatedContainer(ctx context.Context, hostID int64, container string) error {
	return verifyContainerRuntimeWithInspector(ctx, func(inspectCtx context.Context) (inspectContainer, error) {
		return a.inspectOne(inspectCtx, hostID, container)
	}, containerStartupVerificationWindow, 4*time.Second, 2*time.Second)
}

// verifyContainerRuntimeWithInspector is split from Docker I/O so startup-race
// behavior can be regression-tested deterministically. Restarting/not-running
// observations are retryable until startupWindow expires; only a stable running
// runtime (or a healthy Docker healthcheck) lets the pipeline advance.
func verifyContainerRuntimeWithInspector(ctx context.Context, inspect func(context.Context) (inspectContainer, error), startupWindow, noHealthStableWindow, pollInterval time.Duration) error {
	started := time.Now()
	if startupWindow <= 0 {
		startupWindow = containerStartupVerificationWindow
	}
	if noHealthStableWindow <= 0 {
		noHealthStableWindow = 4 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	deadline := started.Add(startupWindow)
	noHealthStableSince := time.Time{}
	var lastErr error
	lastHealth := ""
	healthWindowSet := false
	for {
		cur, err := inspect(ctx)
		if err != nil {
			lastErr = err
			noHealthStableSince = time.Time{}
		} else if cur.State.Restarting {
			// Restarting immediately after docker start/recreate is a retryable
			// startup observation, not proof that recovery failed.
			lastErr = fmt.Errorf("container is still restarting after update")
			noHealthStableSince = time.Time{}
		} else if !cur.State.Running || (cur.State.Status != "" && cur.State.Status != "running") {
			lastErr = fmt.Errorf("container is not yet running after update (state=%s exit=%d)", cur.State.Status, cur.State.ExitCode)
			noHealthStableSince = time.Time{}
		} else {
			// A later healthy runtime observation supersedes any transient inspect,
			// restarting or not-running observation seen during startup.
			lastErr = nil
			if cur.State.Health != nil {
				noHealthStableSince = time.Time{}
				if !healthWindowSet {
					healthDeadline := started.Add(dockerHealthVerificationWindow(cur.Config.Healthcheck))
					if healthDeadline.After(deadline) {
						deadline = healthDeadline
					}
					healthWindowSet = true
				}
				lastHealth = strings.ToLower(strings.TrimSpace(cur.State.Health.Status))
				switch lastHealth {
				case "healthy":
					return nil
				case "unhealthy":
					lastErr = fmt.Errorf("container healthcheck is still unhealthy")
				case "starting", "":
					// Keep polling until the bounded deadline. Custom Verification remains
					// the stronger application-level gate after Docker lifecycle recovery.
				}
			} else {
				if noHealthStableSince.IsZero() {
					noHealthStableSince = time.Now()
				}
				if time.Since(noHealthStableSince) >= noHealthStableWindow {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("container did not reach a stable running state within %s: %w", time.Since(started).Round(time.Millisecond), lastErr)
			}
			if lastHealth == "unhealthy" {
				return fmt.Errorf("container healthcheck remained unhealthy for %s after update", time.Since(started).Round(time.Millisecond))
			}
			if !healthWindowSet {
				stableFor := time.Duration(0)
				if !noHealthStableSince.IsZero() {
					stableFor = time.Since(noHealthStableSince)
				}
				return fmt.Errorf("container did not remain continuously running for the required %s stability window (stable for %s)", noHealthStableWindow, stableFor.Round(time.Millisecond))
			}
			// A Docker healthcheck that is still starting is not proof of application
			// failure. Custom Verification, when configured, remains authoritative.
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
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

func (a *App) runAutomaticRollback(ctx context.Context, req updateRequest, rp db.RestorePoint, cause error) (bool, error) {
	if ctx == nil {
		ctx = a.ctx
	}
	if !a.shouldAutoRollback(ctx, rp) {
		return false, nil
	}
	id, err := a.Store.CreateJob(ctx, "rollback", "automatic", rp.HostID, rp.ContainerName, "queued")
	if err != nil {
		return true, err
	}
	message := "Automatic rollback queued after failed update"
	if cause != nil {
		message += ": " + cause.Error()
	}
	_ = a.Store.AddJobLog(ctx, id, "WARN", "rollback", message)
	a.jobProgress(ctx, id, 5, "Automatic rollback queued")
	_ = a.Store.Audit(ctx, "system", "rollback.auto.queue", rp.HostID, rp.ContainerName, fmt.Sprintf("restore_point=%d update_job=%d", rp.ID, req.JobID))
	return true, a.executeRestorePointRollback(ctx, id, rp, "system", "automatic")
}
