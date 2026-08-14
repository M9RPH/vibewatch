package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

const operationLeaseTTL = 2 * time.Hour

const (
	txQueued           = "queued"
	txPreflight        = "preflight"
	txSnapshot         = "snapshot"
	txRestorePoint     = "restore_point"
	txPrepared         = "prepared"
	txUpdating         = "updating"
	txDockerHealth     = "docker_health"
	txDependencies     = "dependencies"
	txVerifying        = "verifying"
	txRefreshing       = "refreshing"
	txRecovering       = "recovering"
	txRollback         = "rollback"
	txSuccess          = "success"
	txFailed           = "failed"
	txSkipped          = "skipped"
	txRolledBack       = "rolled_back"
	txRecoveryRequired = "recovery_required"
)

var transactionTransitions = map[string]map[string]bool{
	txQueued:           {txPreflight: true, txFailed: true, txRecovering: true},
	txPreflight:        {txSnapshot: true, txPrepared: true, txFailed: true, txSkipped: true, txRecovering: true},
	txSnapshot:         {txRestorePoint: true, txFailed: true, txSkipped: true, txRecovering: true},
	txRestorePoint:     {txPrepared: true, txFailed: true, txSkipped: true, txRecovering: true},
	txPrepared:         {txUpdating: true, txFailed: true, txRecovering: true},
	txUpdating:         {txDockerHealth: true, txRollback: true, txFailed: true, txRecovering: true},
	txDockerHealth:     {txDependencies: true, txVerifying: true, txRollback: true, txFailed: true, txRecovering: true},
	txDependencies:     {txVerifying: true, txRollback: true, txFailed: true, txRecovering: true},
	txVerifying:        {txRefreshing: true, txRollback: true, txFailed: true, txRecovering: true},
	txRefreshing:       {txSuccess: true, txFailed: true, txRecovering: true},
	txRecovering:       {txDockerHealth: true, txDependencies: true, txVerifying: true, txRollback: true, txSuccess: true, txFailed: true, txRecoveryRequired: true},
	txRollback:         {txRolledBack: true, txFailed: true, txRecoveryRequired: true},
	txRecoveryRequired: {txRecovering: true, txRollback: true, txFailed: true},
}

func validTransactionTransition(from, to string) bool {
	if from == to {
		return true
	}
	if to == txRecovering && from != txSuccess && from != txFailed && from != txRolledBack {
		return true
	}
	if to == txFailed && from != txSuccess && from != txRolledBack {
		return true
	}
	return transactionTransitions[from][to]
}

func transactionTerminalState(state string) bool {
	return state == txSuccess || state == txFailed || state == txSkipped || state == txRolledBack
}

func (a *App) txTransition(ctx context.Context, tx *db.UpdateTransaction, toState, status, message string) error {
	if tx == nil || tx.ID <= 0 {
		return nil
	}
	from := tx.State
	if !validTransactionTransition(from, toState) {
		return fmt.Errorf("invalid update transaction transition %s -> %s", from, toState)
	}
	duration := int64(0)
	if t, err := time.Parse(time.RFC3339Nano, tx.UpdatedAt); err == nil {
		duration = time.Since(t).Milliseconds()
	}
	if err := a.Store.TransitionUpdateTransaction(ctx, tx.ID, from, toState, status, message, duration); err != nil {
		return err
	}
	tx.State = toState
	if status != "" {
		tx.Status = status
	}
	tx.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if transactionTerminalState(toState) {
		tx.FinishedAt = tx.UpdatedAt
	}
	if tx.JobID > 0 {
		_ = a.Store.AddJobLog(context.Background(), tx.JobID, "INFO", "transaction", fmt.Sprintf("%s → %s · %s", from, toState, message))
	}
	return nil
}

func containerLeaseKey(hostID int64, container string) string {
	return fmt.Sprintf("container:%d:%s", hostID, strings.ToLower(strings.TrimSpace(container)))
}

func (a *App) acquireOperationLease(ctx context.Context, jobID, hostID int64, container, operation string) (string, string, error) {
	owner := fmt.Sprintf("job:%d", jobID)
	key := containerLeaseKey(hostID, container)
	ok, err := a.Store.AcquireOperationLease(ctx, db.OperationLease{ResourceKey: key, HostID: hostID, ContainerName: container, Owner: owner, OperationType: operation, JobID: jobID}, operationLeaseTTL)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("container is locked by another update/rollback operation")
	}
	return key, owner, nil
}

func hostLeaseKey(hostID int64) string { return fmt.Sprintf("host:%d", hostID) }

func (a *App) acquireHostOperationLease(ctx context.Context, jobID, hostID int64, operation string) (string, string, error) {
	owner := fmt.Sprintf("job:%d", jobID)
	key := hostLeaseKey(hostID)
	ok, err := a.Store.AcquireOperationLease(ctx, db.OperationLease{ResourceKey: key, HostID: hostID, Owner: owner, OperationType: operation, JobID: jobID}, operationLeaseTTL)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("host is locked by another update/rollback/cleanup operation")
	}
	return key, owner, nil
}

func (a *App) holdHostOperationLease(ctx context.Context, jobID, hostID int64, operation string) (func(), error) {
	key, owner, err := a.acquireHostOperationLease(ctx, jobID, hostID, operation)
	if err != nil {
		return nil, err
	}
	stop := a.startLeaseHeartbeat(ctx, key, owner, 0)
	return func() {
		stop()
		_ = a.Store.ReleaseOperationLease(context.Background(), key, owner)
	}, nil
}

func (a *App) startLeaseHeartbeat(ctx context.Context, key, owner string, txID int64) func() {
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_ = a.Store.RenewOperationLease(context.Background(), key, owner, txID, operationLeaseTTL)
			}
		}
	}()
	return cancel
}

type RestoreIntegrityCheck struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type RestoreIntegrity struct {
	Status    string                  `json:"status"`
	CheckedAt string                  `json:"checked_at"`
	Checks    []RestoreIntegrityCheck `json:"checks"`
}

func (a *App) validateRestorePointIntegrity(ctx context.Context, rp db.RestorePoint) RestoreIntegrity {
	out := RestoreIntegrity{Status: "ready", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Checks: []RestoreIntegrityCheck{}}
	add := func(key, status, detail string) {
		out.Checks = append(out.Checks, RestoreIntegrityCheck{Key: key, Status: status, Detail: detail})
		if status == "missing" {
			out.Status = "degraded"
		}
	}
	path, _, snapErr := a.findSnapshotByID(rp.HostID, rp.SnapshotID, rp.ContainerName)
	if snapErr != nil {
		add("config_snapshot", "missing", snapErr.Error())
		out.Status = "expired"
		raw, _ := json.Marshal(out)
		_ = a.Store.SetRestorePointIntegrity(context.Background(), rp.ID, out.Status, string(raw))
		return out
	}
	add("config_snapshot", "ok", path)
	h, hostErr := a.Store.Host(ctx, rp.HostID)
	if hostErr != nil {
		add("docker_host", "missing", hostErr.Error())
	} else {
		add("docker_host", "ok", h.Name)
	}
	if bool(rp.WritableLayer) {
		if hostErr != nil || strings.TrimSpace(rp.ImageRef) == "" || !a.Docker.ImageExists(ctx, h.Endpoint, rp.ImageRef) {
			add("restore_image", "missing", firstNonEmpty(rp.ImageRef, "restore image unavailable"))
		} else {
			add("restore_image", "ok", rp.ImageRef)
		}
	} else {
		add("restore_image", "not_required", "configuration-only restore point")
	}
	if bool(rp.VolumeDataProtected) {
		if hostErr != nil {
			add("data_restore_point", "missing", "Docker host unavailable")
		} else if dataErr := a.dataArchiveExists(ctx, rp); dataErr != nil {
			add("data_restore_point", "missing", dataErr.Error())
		} else {
			add("data_restore_point", "ok", fmt.Sprintf("%d byte(s) protected", rp.DataBytes))
		}
	} else {
		add("data_restore_point", "not_required", "no persistent data selected")
	}
	if depErr := a.dependencySnapshotsAvailable(rp); depErr != nil {
		add("dependency_snapshots", "missing", depErr.Error())
	} else {
		add("dependency_snapshots", "ok", fmt.Sprintf("%d dependency snapshot(s)", rp.DependencyCount))
	}
	if raw, err := snapshotZipEntry(path, "container-inspect.json"); err == nil {
		if old, err := findInspectForContainer(raw, rp.ContainerName); err == nil && hostErr == nil {
			vols := []string{}
			for _, m := range old.Mounts {
				if m.Type == "volume" && strings.TrimSpace(m.Name) != "" {
					vols = append(vols, m.Name)
				}
			}
			if len(vols) > 0 {
				if _, err := a.Docker.Run(ctx, h.Endpoint, append([]string{"volume", "inspect"}, vols...)...); err != nil {
					add("volumes", "missing", err.Error())
				} else {
					add("volumes", "ok", strings.Join(vols, ", "))
				}
			} else {
				add("volumes", "not_required", "no named volumes")
			}
			nets := make([]string, 0, len(old.NetworkSettings.Networks))
			for name := range old.NetworkSettings.Networks {
				name = strings.TrimSpace(name)
				if name != "" && name != "bridge" && name != "host" && name != "none" && name != "default" {
					nets = append(nets, name)
				}
			}
			sort.Strings(nets)
			if len(nets) > 0 {
				args := append([]string{"network", "inspect"}, nets...)
				if _, err := a.Docker.Run(ctx, h.Endpoint, args...); err != nil {
					add("networks", "missing", err.Error())
				} else {
					add("networks", "ok", strings.Join(nets, ", "))
				}
			} else {
				add("networks", "not_required", "no custom networks")
			}
		}
	}
	raw, _ := json.Marshal(out)
	_ = a.Store.SetRestorePointIntegrity(context.Background(), rp.ID, out.Status, string(raw))
	return out
}

type recoverySummary struct {
	RestorePoints     int               `json:"restore_points"`
	Ready             int               `json:"ready"`
	Degraded          int               `json:"degraded"`
	Expired           int               `json:"expired"`
	ProtectedImages   int               `json:"protected_images"`
	ProtectedNetworks int               `json:"protected_networks"`
	ProtectedVolumes  int               `json:"protected_volumes"`
	LatestGC          *db.RecoveryGCRun `json:"latest_gc,omitempty"`
}

func (a *App) removeOrphanRestoreImages(ctx context.Context, points []db.RestorePoint) (int, []string) {
	keepByHost := map[int64]map[string]bool{}
	hosts := map[int64]bool{}
	for _, rp := range points {
		hosts[rp.HostID] = true
		if rp.Status == "expired" || strings.TrimSpace(rp.ImageRef) == "" {
			continue
		}
		if keepByHost[rp.HostID] == nil {
			keepByHost[rp.HostID] = map[string]bool{}
		}
		keepByHost[rp.HostID][strings.TrimSpace(rp.ImageRef)] = true
	}
	removed := 0
	errs := []string{}
	for hostID := range hosts {
		select {
		case <-ctx.Done():
			return removed, append(errs, ctx.Err().Error())
		default:
		}
		h, err := a.Store.Host(ctx, hostID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("host %d: %v", hostID, err))
			continue
		}
		owner := fmt.Sprintf("system:recovery-gc:%d:%d", time.Now().UTC().UnixNano(), hostID)
		key := hostLeaseKey(hostID)
		ok, leaseErr := a.Store.AcquireOperationLease(ctx, db.OperationLease{ResourceKey: key, HostID: hostID, Owner: owner, OperationType: "recovery-gc"}, 10*time.Minute)
		if leaseErr != nil {
			errs = append(errs, fmt.Sprintf("%s: recovery GC lease: %v", h.Name, leaseErr))
			continue
		}
		if !ok {
			// A live update/rollback/chain lifecycle action owns this host. Skipping
			// cleanup is the safe outcome and should not turn a healthy GC run red.
			continue
		}
		func() {
			defer a.Store.ReleaseOperationLease(context.Background(), key, owner)
			listCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 2*time.Minute, 4*time.Minute))
			defer cancel()
			out, listErr := a.Docker.Run(listCtx, h.Endpoint, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
			if listErr != nil {
				errs = append(errs, fmt.Sprintf("%s: list restore images: %v", h.Name, listErr))
				return
			}
			prefix := fmt.Sprintf("vibewatch-restore/host-%d/", hostID)
			seen := map[string]bool{}
			for _, line := range strings.Split(out, "\n") {
				ref := strings.TrimSpace(line)
				if ref == "" || seen[ref] || !strings.HasPrefix(ref, prefix) {
					continue
				}
				seen[ref] = true
				if keepByHost[hostID][ref] {
					continue
				}
				rmCtx, rmCancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 90*time.Second, 3*time.Minute))
				_, rmErr := a.Docker.Run(rmCtx, h.Endpoint, "image", "rm", ref)
				rmCancel()
				if rmErr != nil {
					errs = append(errs, fmt.Sprintf("%s: remove orphan restore image %s: %v", h.Name, ref, rmErr))
					continue
				}
				removed++
			}
		}()
	}
	return removed, errs
}

func (a *App) runRecoveryGC(ctx context.Context, trigger string) db.RecoveryGCRun {
	run := db.RecoveryGCRun{TS: time.Now().UTC().Format(time.RFC3339Nano), Status: "success", ErrorsJSON: "[]"}
	errs := []string{}
	// Existing snapshot retention remains authoritative for config archives and
	// pinned dependency snapshots. The v0.9 GC then validates the resulting
	// restore graph and removes only Vibewatch-owned orphan restore images.
	a.enforceAllSnapshotRetention()
	points, err := a.Store.RestorePoints(ctx, 5000, 0, "")
	if err != nil {
		run.Status = "failed"
		errs = append(errs, err.Error())
	} else {
		run.RestorePointsChecked = len(points)
		for _, rp := range points {
			if rp.Status == "expired" {
				run.Expired++
				continue
			}
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			integrity := a.validateRestorePointIntegrity(checkCtx, rp)
			cancel()
			switch integrity.Status {
			case "expired":
				a.expireRestorePointsForSnapshot(context.Background(), rp.HostID, rp.SnapshotID)
				run.Expired++
			case "degraded":
				_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", "integrity validation failed")
				run.Degraded++
			case "ready":
				// A retained restore image/network/volume may have been restored by an
				// operator after a temporary degradation. Only full points with a real
				// captured image are eligible for automatic status healing.
				if rp.Status == "degraded" && bool(rp.WritableLayer) && strings.TrimSpace(rp.ImageRef) != "" {
					_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "ready", "")
				}
			}
		}
		removed, imageErrs := a.removeOrphanRestoreImages(ctx, points)
		run.ImagesRemoved = removed
		errs = append(errs, imageErrs...)
		helperRemoved, helperErrs := a.removeOrphanHelperContainers(ctx, points)
		run.HelpersRemoved = helperRemoved
		errs = append(errs, helperErrs...)
	}
	if pruneErr := a.Store.PruneReliabilityHistory(context.Background(), 5000); pruneErr != nil {
		errs = append(errs, "reliability history retention: "+pruneErr.Error())
	}
	if len(errs) > 0 {
		b, _ := json.Marshal(errs)
		run.ErrorsJSON = string(b)
		if run.Status == "success" {
			run.Status = "warning"
		}
	}
	_, _ = a.Store.AddRecoveryGCRun(context.Background(), run)
	_ = a.Store.Audit(context.Background(), "system", "recovery.gc", 0, "", fmt.Sprintf("trigger=%s checked=%d degraded=%d expired=%d images_removed=%d helpers_removed=%d", trigger, run.RestorePointsChecked, run.Degraded, run.Expired, run.ImagesRemoved, run.HelpersRemoved))
	return run
}

func (a *App) recoveryStorageSummary(ctx context.Context) recoverySummary {
	out := recoverySummary{}
	points, _ := a.Store.RestorePoints(ctx, 5000, 0, "")
	for _, rp := range points {
		out.RestorePoints++
		switch {
		case rp.Status == "expired" || rp.IntegrityStatus == "expired":
			out.Expired++
		case rp.Status == "degraded" || rp.Status == "failed" || rp.IntegrityStatus == "degraded":
			out.Degraded++
		default:
			out.Ready++
		}
	}
	hostSeen := map[int64]bool{}
	for _, rp := range points {
		hostSeen[rp.HostID] = true
	}
	for hostID := range hostSeen {
		images, nets, vols := a.rollbackProtectedDockerObjects(hostID)
		out.ProtectedImages += len(images)
		out.ProtectedNetworks += len(nets)
		out.ProtectedVolumes += len(vols)
	}
	if runs, _ := a.Store.RecoveryGCRuns(ctx, 1); len(runs) > 0 {
		out.LatestGC = &runs[0]
	}
	return out
}

func (a *App) handleUpdateTransactions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconvAtoiDefault(r.URL.Query().Get("limit"), 200)
	rows, err := a.Store.UpdateTransactions(r.Context(), limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]db.UpdateTransaction, 0, len(rows))
	for _, x := range rows {
		if a.hostAllowed(r, x.HostID) {
			out = append(out, x)
		}
	}
	writeJSON(w, 200, out)
}
func (a *App) handleVerificationHistory(w http.ResponseWriter, r *http.Request) {
	hostID, _ := parseInt64(r.URL.Query().Get("host_id"))
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	limit, _ := strconvAtoiDefault(r.URL.Query().Get("limit"), 100)
	if hostID > 0 && !a.hostAllowed(r, hostID) {
		writeErr(w, 403, "host access denied")
		return
	}
	rows, err := a.Store.VerificationHistory(r.Context(), hostID, container, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if hostID == 0 {
		filtered := rows[:0]
		for _, x := range rows {
			if a.hostAllowed(r, x.HostID) {
				filtered = append(filtered, x)
			}
		}
		rows = filtered
	}
	writeJSON(w, 200, rows)
}
func (a *App) handleRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	points, _ := a.Store.RestorePoints(r.Context(), 5000, 0, "")
	out := recoverySummary{}
	hostSeen := map[int64]bool{}
	for _, rp := range points {
		if !a.hostAllowed(r, rp.HostID) {
			continue
		}
		out.RestorePoints++
		hostSeen[rp.HostID] = true
		switch {
		case rp.Status == "expired" || rp.IntegrityStatus == "expired":
			out.Expired++
		case rp.Status == "degraded" || rp.Status == "failed" || rp.IntegrityStatus == "degraded":
			out.Degraded++
		default:
			out.Ready++
		}
	}
	for id := range hostSeen {
		images, nets, vols := a.rollbackProtectedDockerObjects(id)
		out.ProtectedImages += len(images)
		out.ProtectedNetworks += len(nets)
		out.ProtectedVolumes += len(vols)
	}
	if runs, _ := a.Store.RecoveryGCRuns(r.Context(), 1); len(runs) > 0 {
		out.LatestGC = &runs[0]
	}
	writeJSON(w, 200, out)
}
func (a *App) handleRecoveryGC(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	writeJSON(w, 200, a.runRecoveryGC(ctx, "manual"))
}

func parseInt64(v string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(strings.TrimSpace(v), &n)
	return n, err
}
func strconvAtoiDefault(v string, d int) (int, error) {
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(v), &n); err != nil || n <= 0 {
		return d, nil
	}
	return n, nil
}

func (a *App) recoverInterruptedTransactions() {
	// Give Docker workers and remote VPN endpoints a short window to settle. A
	// transaction interrupted during Watchtower may have completed remotely even
	// though the controller connection vanished.
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(8 * time.Second):
	}
	_ = a.Store.ExpireOperationLeases(context.Background())
	txs, err := a.Store.ActiveUpdateTransactions(context.Background())
	if err != nil {
		a.Logger.Warn("transaction recovery scan failed", "error", err)
	} else {
		for i := range txs {
			a.recoverUpdateTransaction(txs[i])
		}
	}
	// Chains are reconciled only after their child update transactions. This lets
	// the chain recovery layer reason from final child outcomes instead of racing
	// the single-container crash recovery path.
	a.recoverInterruptedChainRuns()
	// A controller crash can strand a helper before Docker processes --rm. Once
	// transaction/chain recovery has released its leases, remove only helpers on
	// otherwise idle hosts.
	if points, e := a.Store.RestorePoints(context.Background(), 5000, 0, ""); e == nil {
		removed, errs := a.removeOrphanHelperContainers(context.Background(), points)
		if removed > 0 && a.Logger != nil {
			a.Logger.Info("removed orphan Vibewatch helper containers after recovery", "count", removed)
		}
		if len(errs) > 0 && a.Logger != nil {
			a.Logger.Warn("orphan helper cleanup after recovery had warnings", "errors", strings.Join(errs, "; "))
		}
	}
}

func networkNamespaceNeedsRecreate(cur, parent inspectContainer) bool {
	ref, ok := containerNamespaceRef(cur.HostConfig.NetworkMode)
	return !ok || !sameContainerIdentity(ref, parent)
}

func preMutationTransactionState(state string) bool {
	return state == txQueued || state == txPreflight || state == txSnapshot || state == txRestorePoint || state == txPrepared
}

func (a *App) recoverUpdateTransaction(tx db.UpdateTransaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if preMutationTransactionState(tx.State) {
		msg := "controller restart interrupted update before the image mutation stage"
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "interrupted", "safe_abort", msg)
		_ = a.Store.FinishJob(ctx, tx.JobID, "failed", "", msg)
		_ = a.Store.ReleaseOperationLeaseByJob(ctx, tx.JobID)
		_ = a.Store.Audit(ctx, "system", "transaction.recovered-safe-abort", tx.HostID, tx.ContainerName, fmt.Sprintf("transaction=%d state=%s", tx.ID, tx.State))
		return
	}
	tx.Status = "recovering"
	_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovering", "reconcile_runtime", "")
	_ = a.txTransition(ctx, &tx, txRecovering, "recovering", "controller restart reconciliation")
	// The previous controller process cannot still own this job after startup.
	// Reclaim its persisted lease immediately instead of waiting for the normal
	// two-hour TTL; unrelated job/host leases remain untouched.
	_ = a.Store.ReleaseOperationLeaseByJob(ctx, tx.JobID)
	key, owner, leaseErr := a.acquireOperationLease(ctx, tx.JobID, tx.HostID, tx.ContainerName, "recovery")
	if leaseErr != nil {
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovery_required", "lease_conflict", leaseErr.Error())
		return
	}
	stopHB := a.startLeaseHeartbeat(ctx, key, owner, tx.ID)
	defer stopHB()
	defer a.Store.ReleaseOperationLease(context.Background(), key, owner)
	var rp db.RestorePoint
	if tx.RestorePointID > 0 {
		rp, _ = a.Store.RestorePoint(ctx, tx.RestorePointID)
	}
	if rp.ID > 0 {
		integrity := a.validateRestorePointIntegrity(ctx, rp)
		if integrity.Status == "expired" || integrity.Status == "degraded" {
			msg := "interrupted update needs recovery but restore point integrity is " + integrity.Status
			_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovery_required", "manual_intervention", msg)
			_ = a.Store.FinishJob(ctx, tx.JobID, "failed", "", msg)
			return
		}
	}
	// First try to reconcile the live runtime. If it is healthy and custom
	// verification passes, preserve the successfully updated service rather than
	// rolling it back merely because the controller restarted.
	liveErr := a.verifyUpdatedContainer(ctx, tx.HostID, tx.ContainerName)
	if liveErr == nil && rp.ID > 0 {
		if deps, depErr := a.persistedDependencyRuntimes(rp); depErr == nil && len(deps) > 0 {
			if parent, pe := a.inspectOne(ctx, tx.HostID, tx.ContainerName); pe == nil {
				need := false
				for _, dep := range deps {
					cur, e := a.inspectOne(ctx, tx.HostID, dep.SourceContainer)
					if e != nil {
						need = true
						break
					}
					if networkNamespaceNeedsRecreate(cur, parent) {
						need = true
						break
					}
				}
				if need {
					liveErr = a.recreateNetworkNamespaceDependents(ctx, tx.JobID, tx.HostID, tx.ContainerName, parent.ID, deps)
				}
			}
		}
	}
	if liveErr == nil {
		vr := a.runCustomVerification(ctx, tx.HostID, tx.ContainerName, "recovery", "system", tx.JobID)
		if vr.Status == verificationStatusFailed {
			liveErr = fmt.Errorf("recovery verification failed: %s", vr.Error)
		}
	}
	if liveErr == nil {
		_ = a.txTransition(ctx, &tx, txSuccess, "success", "runtime reconciled after controller restart")
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "success", "kept_updated_runtime", "")
		_ = a.Store.FinishJob(ctx, tx.JobID, "success", `{"recovered_after_restart":true}`, "")
		_ = a.Store.Audit(ctx, "system", "transaction.recovered-success", tx.HostID, tx.ContainerName, fmt.Sprintf("transaction=%d", tx.ID))
		return
	}
	if rp.ID == 0 {
		msg := "runtime reconciliation failed and no full restore point is linked: " + liveErr.Error()
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovery_required", "manual_intervention", msg)
		_ = a.Store.FinishJob(ctx, tx.JobID, "failed", "", msg)
		return
	}
	_ = a.txTransition(ctx, &tx, txRollback, "recovering", "runtime reconciliation failed; restoring pre-update state")
	// Release the transaction lease before invoking the normal rollback engine;
	// the update job itself remains active so no user operation can race through
	// the legacy active-job guard during this tiny hand-off.
	stopHB()
	_ = a.Store.ReleaseOperationLease(ctx, key, owner)
	rbJob, err := a.Store.CreateJob(ctx, "rollback", "recovery", rp.HostID, rp.ContainerName, "queued")
	if err == nil {
		err = a.executeRestorePointRollback(rbJob, rp, "system", "recovery")
	}
	if err != nil {
		_ = a.txTransition(ctx, &tx, txFailed, "failed", "automatic crash-recovery rollback failed: "+err.Error())
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovery_required", "rollback_failed", err.Error())
		_ = a.Store.FinishJob(ctx, tx.JobID, "failed", "", err.Error())
		return
	}
	_ = a.txTransition(ctx, &tx, txRolledBack, "rolled_back", "pre-update state restored after controller restart")
	_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "rolled_back", "automatic_rollback", liveErr.Error())
	_ = a.Store.FinishJob(ctx, tx.JobID, "failed", `{"recovered_by_rollback":true}`, "update interrupted; pre-update state restored")
	_ = a.Store.Audit(ctx, "system", "transaction.recovered-rollback", tx.HostID, tx.ContainerName, fmt.Sprintf("transaction=%d restore_point=%d", tx.ID, rp.ID))
}

// os import is kept here deliberately: integrity validation treats a missing
// snapshot as an expired restore point while preserving the exact filesystem error.
var _ = os.ErrNotExist
