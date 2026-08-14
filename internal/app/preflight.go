package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/registry"
)

const (
	preflightGreen  = "green"
	preflightInfo   = "info"
	preflightYellow = "yellow"
	preflightRed    = "red"
)

type PreflightCheck struct {
	Key         string `json:"key"`
	Status      string `json:"status"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Detail      string `json:"detail,omitempty"`
	Source      string `json:"source"`
	DurationMS  int64  `json:"duration_ms"`
	Blocking    bool   `json:"blocking"`
}

type PreflightResult struct {
	Status      string           `json:"status"` // ready, ready_with_warnings, blocked
	HostID      int64            `json:"host_id"`
	Container   string           `json:"container"`
	StartedAt   string           `json:"started_at"`
	FinishedAt  string           `json:"finished_at"`
	Prepared    bool             `json:"prepared"`
	SnapshotID  string           `json:"snapshot_id,omitempty"`
	RestoreID   int64            `json:"restore_point_id,omitempty"`
	Checks      []PreflightCheck `json:"checks"`
	Warnings    int              `json:"warnings"`
	Blocked     int              `json:"blocked"`
	Summary     string           `json:"summary"`
	lastCheckAt time.Time
	onCheck     func(PreflightCheck)
}

type preflightPrepared struct {
	Snapshot            ContainerBackupSnapshot
	RestorePoint        db.RestorePoint
	Dependencies        []networkNamespaceDependencyRuntime
	TargetInspect       inspectContainer
	DeferredDataWriters []string
}

func preflightCheckSource(key string) string {
	switch key {
	case "registry", "architecture", "new_image":
		return "registry manifest"
	case "volumes", "bind_mounts", "container_state", "docker_health", "dependencies", "restore_configuration":
		return "Docker Engine"
	case "custom_verification":
		return "Vibewatch verification profile"
	case "data_protection":
		return "Vibewatch data protection"
	case "restore_storage":
		return "Vibewatch restore storage"
	case "config_snapshot", "dependency_snapshots", "restore_point":
		return "Vibewatch recovery engine"
	case "transaction_state":
		return "Vibewatch transaction engine"
	case "automatic_safety":
		return "Vibewatch automation safety"
	case "major_version":
		return "Vibewatch version metadata"
	default:
		return "Vibewatch preflight"
	}
}

func (r *PreflightResult) add(key, status, title, description, detail string) {
	now := time.Now()
	duration := int64(0)
	if !r.lastCheckAt.IsZero() {
		duration = now.Sub(r.lastCheckAt).Milliseconds()
	}
	r.lastCheckAt = now
	check := PreflightCheck{Key: key, Status: status, Title: title, Description: description, Detail: detail, Source: preflightCheckSource(key), DurationMS: duration, Blocking: status == preflightRed}
	r.Checks = append(r.Checks, check)
	if r.onCheck != nil {
		r.onCheck(check)
	}
	if status == preflightYellow {
		r.Warnings++
	}
	if status == preflightRed {
		r.Blocked++
	}
}

func (r *PreflightResult) finish() {
	r.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	switch {
	case r.Blocked > 0:
		r.Status = "blocked"
		r.Summary = "Update blocked"
	case r.Warnings > 0:
		r.Status = "ready_with_warnings"
		r.Summary = "Ready with warnings"
	default:
		r.Status = "ready"
		r.Summary = "Ready"
	}
}

func isAutomaticUpdateTrigger(trigger string) bool {
	return strings.HasPrefix(trigger, "automation:") || strings.HasPrefix(trigger, "chain-auto:")
}

func automaticApprovalRequired(check PreflightCheck) bool {
	if check.Status != preflightYellow {
		return false
	}
	switch check.Key {
	case "container_state", "new_image":
		return true
	case "major_version":
		return strings.Contains(strings.ToLower(check.Title), "major version update detected")
	default:
		return false
	}
}

func automaticPreflightBlocked(checks []PreflightCheck, allowWarnings bool) (bool, string) {
	for _, check := range checks {
		if check.Status == preflightRed {
			return true, check.Title
		}
	}
	for _, check := range checks {
		if check.Status != preflightYellow {
			continue
		}
		if automaticApprovalRequired(check) {
			return true, check.Title
		}
		if !allowWarnings {
			return true, check.Title
		}
	}
	return false, ""
}
func applyAutomaticPreflightSafety(result *PreflightResult, req updateRequest) {
	if result == nil || !isAutomaticUpdateTrigger(req.Trigger) || result.Blocked != 0 {
		return
	}
	if blocked, reason := automaticPreflightBlocked(result.Checks, req.AllowPreflightWarnings); blocked {
		detail := "Automatic updates require a clean Preflight by default."
		if req.AllowPreflightWarnings {
			detail = "This warning requires manual approval even when advisory warnings are allowed."
		}
		result.add("automatic_safety", preflightRed, "Automatic update held by Preflight", detail, reason)
	}
}

func majorPart(v string) (int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V"))
	if v == "" {
		return 0, false
	}
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(v[:end])
	return n, err == nil
}

func (a *App) preflightProgress(ctx context.Context, req updateRequest, percent int, stage string) {
	if req.PreviewProgress != nil {
		req.PreviewProgress(percent, stage)
	}
	if req.JobID > 0 {
		a.jobProgress(ctx, req.JobID, percent, "Preflight · "+stage)
	}
}

func (a *App) transitionPreflightTransaction(ctx context.Context, req updateRequest, state, message string) error {
	if req.TransactionID <= 0 {
		return nil
	}
	tx, err := a.Store.UpdateTransaction(ctx, req.TransactionID)
	if err != nil {
		return err
	}
	return a.txTransition(ctx, &tx, state, "running", message)
}

func (a *App) runUpdatePreflight(ctx context.Context, req updateRequest, prepare bool) (PreflightResult, preflightPrepared) {
	result := PreflightResult{HostID: req.HostID, Container: req.Container, StartedAt: time.Now().UTC().Format(time.RFC3339), Prepared: prepare, Checks: []PreflightCheck{}, lastCheckAt: time.Now(), onCheck: req.PreviewCheck}
	prepared := preflightPrepared{}
	if req.JobID > 0 {
		_ = a.Store.AddJobLog(ctx, req.JobID, "INFO", "preflight", "update preflight started")
	}
	a.preflightProgress(ctx, req, 12, "Loading Docker host")
	_ = a.Store.Audit(context.Background(), req.Actor, "preflight.started", req.HostID, req.Container, fmt.Sprintf("trigger=%s prepare=%t", req.Trigger, prepare))

	h, err := a.Store.Host(ctx, req.HostID)
	if err != nil {
		result.add("host", preflightRed, "Docker host unavailable", "The configured Docker host could not be loaded.", err.Error())
		result.finish()
		return result, prepared
	}

	a.preflightProgress(ctx, req, 15, "Inspecting container and dependencies")
	target, deps, depErr := a.discoverNetworkNamespaceDependents(ctx, req.HostID, req.Container)
	if depErr != nil {
		result.add("dependencies", preflightRed, "Dependency scan failed", "Network namespace dependencies could not be verified safely.", depErr.Error())
	} else {
		prepared.TargetInspect, prepared.Dependencies = target, deps
		if len(deps) > 0 {
			result.add("dependencies", preflightGreen, "Docker dependencies resolved", fmt.Sprintf("%d network namespace dependent(s) will be recreated with the parent.", len(deps)), dependencyNames(deps))
		} else {
			result.add("dependencies", preflightGreen, "Docker dependencies resolved", "No direct network namespace dependents require recreation.", "")
		}
	}

	if strings.TrimSpace(target.ID) == "" {
		if depErr == nil {
			result.add("container_state", preflightRed, "Container not found", "The target container could not be inspected.", "")
		}
	} else if target.State.Restarting || strings.EqualFold(target.State.Status, "dead") {
		result.add("container_state", preflightRed, "Container state is unsafe", "The target is restarting or dead; update is blocked until its state is stable.", target.State.Status)
	} else if !target.State.Running {
		result.add("container_state", preflightYellow, "Container is not running", "A stopped container can be updated manually, but post-update verification may be less representative.", target.State.Status)
	} else {
		result.add("container_state", preflightGreen, "Container state plausible", "The target container is running and can be prepared for update.", target.State.Status)
	}

	a.preflightProgress(ctx, req, 19, "Checking health and verification configuration")
	profile, _ := a.effectiveVerificationProfile(ctx, req.HostID, req.Container)
	if target.Config.Healthcheck != nil {
		result.add("docker_health", preflightGreen, "Docker healthcheck configured", "Docker health will be evaluated after the update.", "")
	} else if profile.Configured {
		result.add("docker_health", preflightInfo, "No Docker healthcheck", "Custom verification is configured, so the missing Docker healthcheck is informational. Running-state stability is still checked before application verification.", "")
	} else {
		result.add("docker_health", preflightYellow, "No Docker healthcheck", "Only running-state stability is available. Configure custom verification to validate application functionality after the update.", "")
	}
	if profile.Configured {
		result.add("custom_verification", preflightGreen, "Custom verification configured", fmt.Sprintf("%d application-level check(s) will run after Docker health.", len(profile.Checks)), profile.ScopeType+":"+profile.ScopeKey)
	} else {
		result.add("custom_verification", preflightYellow, "No custom verification configured", "The update can proceed, but application-level functionality cannot be verified automatically.", "")
	}

	a.preflightProgress(ctx, req, 23, "Checking registry manifest and architecture")
	// Registry reachability, manifest existence and platform compatibility share
	// one manifest resolution so high-latency hosts do not trigger redundant registry calls.
	imageRef := strings.TrimSpace(target.Config.Image)
	if imageRef == "" {
		imageRef = strings.TrimSpace(target.Image)
	}
	platform := registry.Platform{}
	if imageRef != "" {
		if p, pe := a.Docker.ImagePlatform(ctx, h.Endpoint, target.Image); pe == nil {
			platform = registry.Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant}
		}
	}
	if strings.TrimSpace(platform.Architecture) == "" {
		if overview, oe := a.Docker.HostOverview(ctx, h.Endpoint, false); oe == nil {
			platform = registry.Platform{OS: overview.OSType, Architecture: overview.Architecture}
		}
	}
	if imageRef == "" {
		result.add("registry", preflightRed, "Image reference missing", "The target image reference could not be determined.", "")
		result.add("architecture", preflightRed, "Architecture unknown", "Image compatibility cannot be validated without an image reference.", "")
	} else if strings.TrimSpace(platform.Architecture) == "" {
		// Never let the registry helper silently fall back to the controller's
		// own GOARCH when the Docker host platform is unknown. That would make a
		// remote ARM host look compatible merely because Vibewatch runs on amd64
		// (or vice versa). We can still prove registry/manifest reachability, but
		// architecture compatibility is a safety-critical red check.
		regCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 20*time.Second, 35*time.Second))
		digest, regErr := a.Registry.RemoteDigest(regCtx, imageRef)
		cancel()
		if regErr != nil {
			result.add("registry", preflightRed, "Registry / manifest unavailable", "The target manifest could not be resolved before changing the running container.", regErr.Error())
		} else {
			result.add("registry", preflightGreen, "Image manifest available", "The registry and target image manifest are reachable.", digest)
		}
		result.add("architecture", preflightRed, "Host architecture not verified", "Vibewatch could not determine the target Docker host architecture, so image compatibility cannot be proven safely.", "")
	} else {
		regCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 20*time.Second, 35*time.Second))
		remote, regErr := a.Registry.RemoteStateForPlatform(regCtx, imageRef, platform)
		cancel()
		if regErr != nil {
			if strings.Contains(strings.ToLower(regErr.Error()), "no image for platform") {
				result.add("registry", preflightGreen, "Registry reachable", "The registry manifest was reached.", imageRef)
				result.add("architecture", preflightRed, "Architecture mismatch", "The target registry manifest has no image for this host platform.", regErr.Error())
			} else {
				result.add("registry", preflightRed, "Registry / manifest unavailable", "The target manifest could not be resolved before changing the running container.", regErr.Error())
				result.add("architecture", preflightRed, "Architecture not verified", "Platform compatibility could not be verified because manifest resolution failed.", "")
			}
		} else {
			result.add("registry", preflightGreen, "Image manifest available", "The registry and target image manifest are reachable.", remote.ManifestDigest)
			result.add("architecture", preflightGreen, "Architecture compatible", "A platform-specific image manifest exists for this host.", strings.Trim(strings.Join([]string{remote.Platform.OS, remote.Platform.Architecture, remote.Platform.Variant}, "/"), "/"))
		}
	}

	cache, _ := a.Store.Cache(ctx, req.HostID, req.Container)
	if bool(cache.UpdateAvailable) && strings.TrimSpace(cache.LatestDigest) != "" {
		result.add("new_image", preflightGreen, "New image detected", "The currently detected update digest is available for this update request.", cache.LatestDigest)
	} else {
		result.add("new_image", preflightYellow, "No newer digest currently detected", "The manifest is reachable, but Vibewatch has no newer digest cached. A chain step may be skipped as already current.", "")
	}

	version, _ := a.Store.Version(ctx, req.HostID, req.Container)
	if oldMajor, okOld := majorPart(version.Installed); okOld {
		if newMajor, okNew := majorPart(version.Latest); okNew && newMajor > oldMajor {
			result.add("major_version", preflightYellow, "Major version update detected", fmt.Sprintf("Version metadata indicates a major jump from %s to %s.", version.Installed, version.Latest), "Review application migration notes before updating.")
		} else {
			result.add("major_version", preflightGreen, "No major version jump detected", "Available version metadata does not indicate a major-version increase.", "")
		}
	} else {
		result.add("major_version", preflightYellow, "Major version could not be determined", "Readable version metadata is incomplete; digest-based update detection remains authoritative.", "")
	}

	a.preflightProgress(ctx, req, 32, "Checking volumes, bind mounts and restore configuration")
	volumeNames := []string{}
	binds := []string{}
	for _, m := range target.Mounts {
		switch m.Type {
		case "volume":
			if strings.TrimSpace(m.Name) != "" {
				volumeNames = append(volumeNames, m.Name)
			}
		case "bind":
			if strings.TrimSpace(m.Source) != "" {
				binds = append(binds, m.Source)
			}
		}
	}
	if len(volumeNames) == 0 {
		result.add("volumes", preflightGreen, "Docker volumes resolved", "The target does not depend on named Docker volumes.", "")
	} else if _, ve := a.Docker.Run(ctx, h.Endpoint, append([]string{"volume", "inspect"}, volumeNames...)...); ve != nil {
		result.add("volumes", preflightRed, "Docker volume missing or unreadable", "At least one referenced Docker volume could not be inspected.", ve.Error())
	} else {
		result.add("volumes", preflightGreen, "Docker volumes available", fmt.Sprintf("%d referenced Docker volume(s) are present.", len(volumeNames)), strings.Join(volumeNames, ", "))
	}
	if len(binds) == 0 {
		result.add("bind_mounts", preflightGreen, "Bind mounts resolved", "The target does not use host bind mounts.", "")
	} else if target.State.Running {
		result.add("bind_mounts", preflightGreen, "Bind mounts active", fmt.Sprintf("%d bind mount(s) are attached to the running container.", len(binds)), strings.Join(binds, ", "))
	} else {
		result.add("bind_mounts", preflightYellow, "Bind mounts recorded", "Bind mount sources are recorded, but the stopped container cannot prove current runtime reachability.", strings.Join(binds, ", "))
	}

	if dataCheck, configured := a.dataProtectionPreflight(ctx, req); configured {
		result.add(dataCheck.Key, dataCheck.Status, dataCheck.Title, dataCheck.Description, dataCheck.Detail)
	}
	if strings.TrimSpace(target.ID) != "" {
		storageCheck := a.restoreStoragePreflight(ctx, req, target, prepare)
		result.add(storageCheck.Key, storageCheck.Status, storageCheck.Title, storageCheck.Description, storageCheck.Detail)
	}

	if strings.TrimSpace(target.ID) != "" {
		if _, _, e := createArgsFromInspect(target, imageRef); e != nil {
			result.add("restore_configuration", preflightRed, "Restore configuration invalid", "The current runtime configuration cannot be translated into a safe restore command.", e.Error())
		} else if strings.TrimSpace(target.Config.Labels["com.docker.swarm.service.name"]) != "" {
			result.add("restore_configuration", preflightYellow, "Config-only rollback for Swarm", "A configuration snapshot can be retained, but writable-layer one-click rollback is not supported for Swarm services.", "")
		} else {
			result.add("restore_configuration", preflightGreen, "Restore configuration valid", "The current runtime configuration can be reconstructed for rollback.", "")
		}
	}

	applyAutomaticPreflightSafety(&result, req)

	if prepare && result.Blocked == 0 {
		if e := a.transitionPreflightTransaction(ctx, req, txSnapshot, "creating pre-update config snapshot"); e != nil {
			result.add("transaction_state", preflightRed, "Transaction persistence failed", "Vibewatch could not persist the snapshot stage; the running container will not be changed.", e.Error())
		}
		a.preflightProgress(ctx, req, 35, "Creating config snapshot")
		snapshotReason := "before-update"
		if req.Trigger == "manual" {
			snapshotReason = "before-manual-update"
		} else if strings.HasPrefix(req.Trigger, "automation:") || strings.HasPrefix(req.Trigger, "chain-auto:") {
			snapshotReason = "before-automatic-update"
		}
		if result.Blocked == 0 {
			snapshotCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 60*time.Second, 3*time.Minute))
			snap, e := a.createSnapshotForContainer(snapshotCtx, req.HostID, req.Container, snapshotReason)
			cancel()
			if e != nil {
				result.add("config_snapshot", preflightRed, "Config snapshot failed", "The mandatory pre-update configuration snapshot could not be created.", e.Error())
			} else {
				prepared.Snapshot = snap
				result.SnapshotID = snap.ID
				result.add("config_snapshot", preflightGreen, "Config snapshot created", "The pre-update configuration snapshot is retained for recovery.", snap.Filename)
				if len(prepared.Dependencies) > 0 {
					prepared.Dependencies, e = a.attachDependencySnapshots(ctx, req.HostID, snap, prepared.Dependencies)
					if e != nil {
						result.add("dependency_snapshots", preflightRed, "Dependent config snapshot failed", "A network-namespace dependent could not be protected before update.", e.Error())
					} else {
						result.add("dependency_snapshots", preflightGreen, "Dependent configs protected", "Network-namespace dependent runtime configurations are retained with the transaction.", dependencyNames(prepared.Dependencies))
					}
				}
				if result.Blocked == 0 {
					if e := a.transitionPreflightTransaction(ctx, req, txRestorePoint, "creating full restore point"); e != nil {
						result.add("transaction_state", preflightRed, "Transaction persistence failed", "Vibewatch could not persist the restore-point stage; the running container will not be changed.", e.Error())
					}
				}
				if result.Blocked == 0 {
					a.preflightProgress(ctx, req, 39, "Creating full restore point")
					restoreCtx, restoreCancel := context.WithTimeout(ctx, 10*time.Minute)
					deferRestart := map[string]bool{}
					if req.DeferTargetRestart {
						deferRestart[req.Container] = true
					}
					capture, re := a.createRestorePointForSnapshotWithOptions(restoreCtx, req.HostID, req.Container, snap, snapshotReason, req.Trigger, prepared.Dependencies, restorePointCaptureOptions{CaptureData: !req.SkipDataProtectionCapture, DeferWriterRestart: deferRestart})
					restoreCancel()
					rp := capture.RestorePoint
					prepared.RestorePoint = rp
					prepared.DeferredDataWriters = capture.DeferredWriters
					result.RestoreID = rp.ID
					if re != nil {
						result.add("restore_point", preflightRed, "Restore point failed", "The writable-layer restore point could not be prepared; the running container is left unchanged.", re.Error())
					} else if rp.Status == "config_only" {
						result.add("restore_point", preflightYellow, "Config-only restore point", "The recovery configuration is retained, but full writable-layer rollback is unavailable for this target.", fmt.Sprintf("restore point #%d", rp.ID))
					} else if bool(rp.VolumeDataProtected) {
						result.add("restore_point", preflightGreen, "Application restore point available", "Container state and selected persistent data are protected for rollback.", fmt.Sprintf("restore point #%d · %d data byte(s)", rp.ID, rp.DataBytes))
					} else {
						result.add("restore_point", preflightGreen, "Restore point available", "A full pre-update writable-layer restore point is ready.", fmt.Sprintf("restore point #%d", rp.ID))
					}
				}
			}
		}
	} else if !prepare {
		result.add("config_snapshot", preflightGreen, "Config snapshot pipeline ready", "The mandatory config snapshot will be created again immediately before the update is executed.", "")
		if strings.TrimSpace(target.Config.Labels["com.docker.swarm.service.name"]) != "" {
			result.add("restore_point", preflightYellow, "Full restore point unavailable for Swarm", "Vibewatch will retain a configuration recovery snapshot only.", "")
		} else {
			result.add("restore_point", preflightGreen, "Restore point pipeline ready", "A full writable-layer restore point will be created immediately before the update is executed.", "")
		}
	}

	// Preparation can add new advisory results (for example a config-only
	// restore point). Re-evaluate automatic safety before any image mutation.
	applyAutomaticPreflightSafety(&result, req)

	result.finish()
	if prepare {
		switch result.Status {
		case "blocked":
			a.preflightProgress(ctx, req, 42, "Blocked")
		case "ready_with_warnings":
			a.preflightProgress(ctx, req, 42, "Ready with warnings")
		default:
			a.preflightProgress(ctx, req, 42, "Ready")
		}
	}
	bs, _ := json.Marshal(result.Checks)
	if req.JobID > 0 {
		level := "INFO"
		if result.Status == "blocked" {
			level = "ERROR"
		} else if result.Status == "ready_with_warnings" {
			level = "WARN"
		}
		_ = a.Store.AddJobLog(context.Background(), req.JobID, level, "preflight", result.Summary)
	}
	_ = a.Store.Audit(context.Background(), req.Actor, "preflight."+result.Status, req.HostID, req.Container, string(bs))
	if result.Status == "blocked" {
		a.Logger.Warn("update preflight blocked", "host_id", req.HostID, "container", req.Container, "trigger", req.Trigger, "blocked_checks", result.Blocked, "warnings", result.Warnings, "details", string(bs))
	}
	return result, prepared
}

func (a *App) handleUpdatePreflight(w http.ResponseWriter, r *http.Request, hostID int64) {
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	if container == "" {
		writeErr(w, 400, "container is required")
		return
	}
	if managed, _ := a.systemManagedContainer(container); managed {
		writeErr(w, 409, "Vibewatch system containers are maintained from Owner Settings")
		return
	}
	if cm, chainManaged := a.stackChainForMember(r.Context(), hostID, container); chainManaged {
		writeErr(w, 409, fmt.Sprintf("%s is managed by update chain %s for stack %s; run the chain to preserve the configured update order", container, cm.ChainName, cm.ScopeKey))
		return
	}
	p, _ := a.Store.Policy(r.Context(), hostID, container)
	if p.Mode == "ignore" {
		writeErr(w, 409, "container is excluded; change its policy before updating it")
		return
	}

	if r.URL.Query().Get("stream") != "1" {
		res, _ := a.runUpdatePreflight(r.Context(), updateRequest{HostID: hostID, Container: container, Trigger: "manual-preview", Actor: a.actor(r)}, false)
		writeJSON(w, 200, res)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming preflight is not supported by this server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) {
		bs, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, bs)
		flusher.Flush()
	}

	send("progress", map[string]any{"percent": 5, "stage": "Starting preflight"})
	req := updateRequest{
		HostID:    hostID,
		Container: container,
		Trigger:   "manual-preview",
		Actor:     a.actor(r),
		PreviewProgress: func(percent int, stage string) {
			send("progress", map[string]any{"percent": percent, "stage": stage})
		},
		PreviewCheck: func(check PreflightCheck) {
			send("check", check)
		},
	}
	res, _ := a.runUpdatePreflight(r.Context(), req, false)
	send("result", res)
}
