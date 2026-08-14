package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func chainRunStepTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "success", "failed", "rolled_back", "restarted", "recreated", "skipped_current", "skipped_snoozed", "skipped_preflight", "blocked_preflight", "interrupted":
		return true
	default:
		return false
	}
}

func (a *App) chainRunRestorePoint(ctx context.Context, runID, hostID int64, container string, jobID int64) db.RestorePoint {
	if jobID > 0 {
		if tx, err := a.Store.UpdateTransactionByJob(ctx, jobID); err == nil && tx.RestorePointID > 0 {
			if rp, err := a.Store.RestorePoint(ctx, tx.RestorePointID); err == nil {
				return rp
			}
		}
	}
	rows, _ := a.Store.RestorePoints(ctx, 200, hostID, container)
	want := []string{fmt.Sprintf("chain-recreate:%d", runID), fmt.Sprintf("chain:%d", runID), fmt.Sprintf("chain-auto:%d", runID)}
	for _, rp := range rows {
		for _, trigger := range want {
			if strings.TrimSpace(rp.Trigger) == trigger {
				return rp
			}
		}
	}
	return db.RestorePoint{}
}

func (a *App) recoverInterruptedChainRuns() {
	runs, err := a.Store.ActiveUpdateChainRuns(context.Background())
	if err != nil {
		if a.Logger != nil {
			a.Logger.Warn("chain recovery scan failed", "error", err)
		}
		return
	}
	for _, run := range runs {
		a.recoverInterruptedChainRun(run)
	}
}

func (a *App) recoverInterruptedChainRun(run db.UpdateChainRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	chain, err := a.Store.UpdateChain(ctx, run.ChainID)
	if err != nil {
		msg := "interrupted chain definition is unavailable: " + err.Error()
		_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovery_required", "manual_intervention", msg, false)
		if run.JobID > 0 {
			_ = a.Store.FinishJob(ctx, run.JobID, "failed", "", msg)
		}
		return
	}
	steps, err := a.Store.UpdateChainRunSteps(ctx, run.ID)
	if err != nil {
		msg := "interrupted chain steps are unavailable: " + err.Error()
		_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovery_required", "manual_intervention", msg, false)
		if run.JobID > 0 {
			_ = a.Store.FinishJob(ctx, run.JobID, "failed", "", msg)
		}
		return
	}
	_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovering", "reconcile_started_steps", "", false)
	if run.JobID > 0 {
		_ = a.Store.AddJobLog(ctx, run.JobID, "WARN", "recovery", "Controller restart detected; reconciling the interrupted update chain. Remaining unstarted steps will not be resumed automatically.")
	}

	completed := []completedChainAction{}
	recoveredMutation := false
	rolledBackStep := false
	recoveryRequired := ""
	forceBaselineRollback := false
	firstOpen := -1

	for i, st := range steps {
		status := strings.TrimSpace(st.Status)
		if chainRunStepTerminal(status) {
			switch status {
			case "success":
				rp := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, st.JobID)
				completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "update", RestorePointID: rp.ID})
			case "recreated":
				rp := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, st.JobID)
				completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "recreate", RestorePointID: rp.ID})
			case "restarted":
				completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "restart"})
			case "rolled_back":
				rolledBackStep = true
			}
			continue
		}
		if firstOpen < 0 {
			firstOpen = i
		}

		if st.JobID > 0 {
			tx, txErr := a.Store.UpdateTransactionByJob(ctx, st.JobID)
			job, jobErr := a.Store.Job(ctx, st.JobID)
			if txErr == nil {
				switch tx.Status {
				case "success":
					_ = a.Store.UpdateChainRunStep(ctx, st.ID, "success", st.JobID, "", true)
					rp := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, st.JobID)
					completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "update", RestorePointID: rp.ID})
					recoveredMutation = true
					continue
				case "rolled_back":
					_ = a.Store.UpdateChainRunStep(ctx, st.ID, "rolled_back", st.JobID, firstNonEmpty(tx.Error, "update was rolled back during crash recovery"), true)
					rolledBackStep = true
					recoveredMutation = true
					// If an earlier member owns the shared protected-data baseline while this
					// failed step reused it, software and data must return to that baseline.
					curRP := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, st.JobID)
					if !bool(curRP.VolumeDataProtected) {
						for _, done := range completed {
							if done.RestorePointID <= 0 {
								continue
							}
							if rp, e := a.Store.RestorePoint(ctx, done.RestorePointID); e == nil && bool(rp.VolumeDataProtected) {
								forceBaselineRollback = true
								break
							}
						}
					}
					continue
				case "recovery_required", "recovering", "running":
					recoveryRequired = fmt.Sprintf("%s still requires transaction recovery", st.ContainerName)
					continue
				}
			}
			if jobErr == nil {
				switch job.Status {
				case "success":
					_ = a.Store.UpdateChainRunStep(ctx, st.ID, "success", st.JobID, "", true)
					rp := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, st.JobID)
					completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "update", RestorePointID: rp.ID})
					recoveredMutation = true
					continue
				case "failed", "cancelled", "skipped":
					_ = a.Store.UpdateChainRunStep(ctx, st.ID, "failed", st.JobID, firstNonEmpty(job.Error, "interrupted update did not complete"), true)
					rolledBackStep = true
					continue
				}
			}
			recoveryRequired = fmt.Sprintf("%s has an unresolved child update job", st.ContainerName)
			continue
		}

		switch status {
		case "checking":
			_ = a.Store.UpdateChainRunStep(ctx, st.ID, "interrupted", 0, "controller restarted before this chain step mutated the container", true)
		case "restarting":
			h, hostErr := a.Store.Host(ctx, run.HostID)
			if hostErr != nil {
				recoveryRequired = hostErr.Error()
				continue
			}
			cur, inspectErr := a.inspectOne(ctx, run.HostID, st.ContainerName)
			if inspectErr != nil {
				recoveryRequired = fmt.Sprintf("%s restart recovery inspect failed: %v", st.ContainerName, inspectErr)
				continue
			}
			if !cur.State.Running && !cur.State.Restarting {
				if _, startErr := a.Docker.Run(ctx, h.Endpoint, "start", st.ContainerName); startErr != nil {
					recoveryRequired = fmt.Sprintf("%s could not be restarted after controller recovery: %v", st.ContainerName, startErr)
					continue
				}
			}
			if verifyErr := a.verifyChainLifecycleAction(ctx, run.HostID, st.ContainerName, fmt.Sprintf("chain-recovery:%d", run.ID), "system", run.JobID, true); verifyErr != nil {
				recoveryRequired = fmt.Sprintf("%s restart recovery verification failed: %v", st.ContainerName, verifyErr)
				continue
			}
			_ = a.Store.UpdateChainRunStep(ctx, st.ID, "restarted", 0, "", true)
			completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "restart"})
			recoveredMutation = true
		case "recreating":
			rp := a.chainRunRestorePoint(ctx, run.ID, run.HostID, st.ContainerName, 0)
			if rp.ID == 0 {
				recoveryRequired = fmt.Sprintf("%s recreate was interrupted and no restore point can be linked", st.ContainerName)
				continue
			}
			if verifyErr := a.verifyChainLifecycleAction(ctx, run.HostID, st.ContainerName, fmt.Sprintf("chain-recovery:%d", run.ID), "system", run.JobID, true); verifyErr == nil {
				_ = a.Store.UpdateChainRunStep(ctx, st.ID, "recreated", 0, "", true)
				completed = append(completed, completedChainAction{Container: st.ContainerName, Kind: "recreate", RestorePointID: rp.ID})
				recoveredMutation = true
				continue
			}
			if rbErr := a.rollbackChainRestorePoint(ctx, run.ID, run.JobID, st.ContainerName, rp, "system", "crash-recovery"); rbErr != nil {
				recoveryRequired = fmt.Sprintf("%s recreate recovery rollback failed: %v", st.ContainerName, rbErr)
				continue
			}
			_ = a.Store.UpdateChainRunStep(ctx, st.ID, "rolled_back", 0, "recreate was restored after controller restart", true)
			rolledBackStep = true
			recoveredMutation = true
		default:
			recoveryRequired = fmt.Sprintf("%s stopped in unknown chain state %q", st.ContainerName, status)
		}
	}

	if recoveryRequired != "" {
		_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovery_required", "manual_intervention", recoveryRequired, false)
		_ = a.Store.TouchUpdateChain(ctx, chain.ID, "recovery_required")
		if run.JobID > 0 {
			_ = a.Store.AddJobLog(ctx, run.JobID, "ERROR", "recovery", recoveryRequired)
			_ = a.Store.FinishJob(ctx, run.JobID, "failed", `{"status":"recovery_required"}`, recoveryRequired)
		}
		_ = a.Store.Audit(ctx, "system", "chain.recovery-required", run.HostID, "", fmt.Sprintf("chain=%d run=%d %s", chain.ID, run.ID, recoveryRequired))
		return
	}

	if (bool(chain.RollbackCompleted) && rolledBackStep || forceBaselineRollback) && len(completed) > 0 {
		if run.JobID > 0 {
			_ = a.Store.AddJobLog(ctx, run.JobID, "WARN", "recovery", "Interrupted chain requires completed members to return to the same recovery baseline.")
		}
		failed := a.rollbackCompletedChainMembers(ctx, run.ID, run.JobID, completed, run.HostID, "system")
		if len(failed) > 0 {
			msg := "chain crash-recovery rollback incomplete: " + strings.Join(failed, "; ")
			_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovery_required", "rollback_incomplete", msg, false)
			_ = a.Store.TouchUpdateChain(ctx, chain.ID, "recovery_required")
			if run.JobID > 0 {
				_ = a.Store.FinishJob(ctx, run.JobID, "failed", `{"status":"recovery_required"}`, msg)
			}
			return
		}
		recoveredMutation = true
	}

	// Never resume the remaining chain automatically after a controller restart.
	// The already-started work has been reconciled; untouched steps are marked so
	// the operator can clearly see where execution stopped and can run the chain
	// again after reviewing the current state.
	for _, st := range steps {
		if chainRunStepTerminal(st.Status) {
			continue
		}
		if fresh, e := a.Store.UpdateChainRunSteps(ctx, run.ID); e == nil {
			for _, x := range fresh {
				if x.ID == st.ID && !chainRunStepTerminal(x.Status) {
					_ = a.Store.UpdateChainRunStep(ctx, x.ID, "interrupted", x.JobID, "not resumed automatically after controller restart", true)
				}
			}
		}
	}

	status := "interrupted"
	action := "safe_abort"
	message := "controller restart interrupted the chain; no remaining steps were resumed"
	if recoveredMutation || rolledBackStep {
		status = "recovered"
		action = "reconciled_started_steps"
		message = "started chain work was reconciled after controller restart; remaining steps were not resumed"
	}
	_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, status, action, message, true)
	_ = a.Store.TouchUpdateChain(ctx, chain.ID, status)
	if run.JobID > 0 {
		_ = a.Store.AddJobLog(ctx, run.JobID, "WARN", "recovery", message)
		_ = a.Store.FinishJob(ctx, run.JobID, "failed", fmt.Sprintf(`{"status":%q,"recovered_after_restart":true}`, status), message)
	}
	_ = a.Store.Audit(ctx, "system", "chain.recovered", run.HostID, "", fmt.Sprintf("chain=%d run=%d status=%s first_open=%d", chain.ID, run.ID, status, firstOpen))
}

func restorePointUsedByActiveRun(rp db.RestorePoint, activeRuns []db.UpdateChainRun) bool {
	trigger := strings.TrimSpace(rp.Trigger)
	for _, run := range activeRuns {
		for _, prefix := range []string{"chain:", "chain-auto:", "chain-recreate:"} {
			if trigger == fmt.Sprintf("%s%d", prefix, run.ID) {
				return true
			}
		}
	}
	return false
}

func (a *App) activeRestorePointReferences(ctx context.Context) map[int64]bool {
	keep := map[int64]bool{}
	if txs, err := a.Store.ActiveUpdateTransactions(ctx); err == nil {
		for _, tx := range txs {
			if tx.RestorePointID > 0 {
				keep[tx.RestorePointID] = true
			}
		}
	}
	runs, _ := a.Store.ActiveUpdateChainRuns(ctx)
	points, _ := a.Store.RestorePoints(ctx, 5000, 0, "")
	for _, rp := range points {
		if restorePointUsedByActiveRun(rp, runs) {
			keep[rp.ID] = true
		}
	}
	for _, run := range runs {
		steps, _ := a.Store.UpdateChainRunSteps(ctx, run.ID)
		for _, st := range steps {
			if st.JobID <= 0 {
				continue
			}
			if tx, err := a.Store.UpdateTransactionByJob(ctx, st.JobID); err == nil && tx.RestorePointID > 0 {
				keep[tx.RestorePointID] = true
			}
		}
	}
	return keep
}

func (a *App) removeOrphanHelperContainers(ctx context.Context, points []db.RestorePoint) (int, []string) {
	hosts := map[int64]bool{}
	for _, rp := range points {
		hosts[rp.HostID] = true
	}
	if rows, err := a.Store.Hosts(ctx); err == nil {
		for _, h := range rows {
			if bool(h.Enabled) {
				hosts[h.ID] = true
			}
		}
	}
	removed := 0
	errs := []string{}
	for hostID := range hosts {
		h, err := a.Store.Host(ctx, hostID)
		if err != nil || !bool(h.Enabled) {
			continue
		}
		owner := fmt.Sprintf("system:helper-gc:%d:%d", hostID, time.Now().UTC().UnixNano())
		key := hostLeaseKey(hostID)
		ok, leaseErr := a.Store.AcquireOperationLease(ctx, db.OperationLease{ResourceKey: key, HostID: hostID, Owner: owner, OperationType: "helper-gc"}, 10*time.Minute)
		if leaseErr != nil {
			errs = append(errs, fmt.Sprintf("%s: helper GC lease: %v", h.Name, leaseErr))
			continue
		}
		if !ok {
			continue
		}
		func() {
			defer a.Store.ReleaseOperationLease(context.Background(), key, owner)
			out, listErr := a.Docker.Run(ctx, h.Endpoint, "ps", "-aq", "--filter", "label=io.vibewatch.system-role=helper")
			if listErr != nil {
				errs = append(errs, fmt.Sprintf("%s: list helpers: %v", h.Name, listErr))
				return
			}
			ids := strings.Fields(out)
			if len(ids) == 0 {
				return
			}
			args := append([]string{"rm", "-f"}, ids...)
			if _, rmErr := a.Docker.Run(ctx, h.Endpoint, args...); rmErr != nil {
				errs = append(errs, fmt.Sprintf("%s: remove orphan helpers: %v", h.Name, rmErr))
				return
			}
			removed += len(ids)
		}()
	}
	return removed, errs
}

func (a *App) removeRestorePointArtifacts(ctx context.Context, rp db.RestorePoint) error {
	if a.dependencySnapshotPinned(ctx, rp.HostID, rp.SnapshotID) {
		return fmt.Errorf("snapshot is pinned by another retained recovery transaction")
	}
	var errs []string
	if manifest, err := decodeDataManifest(rp.DataManifestJSON); err == nil && len(manifest.Entries) > 0 {
		if err := a.removeDataRestoreArtifacts(ctx, rp.HostID, manifest); err != nil {
			errs = append(errs, "data: "+err.Error())
		}
	}
	for _, dep := range restorePointDependencies(rp) {
		id := strings.TrimSpace(dep.SnapshotID)
		if id == "" || id == rp.SnapshotID || a.dependencySnapshotPinned(ctx, rp.HostID, id) {
			continue
		}
		if path, _, err := a.findSnapshotByID(rp.HostID, id, dep.SourceContainer); err == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, "dependency snapshot: "+err.Error())
			}
		}
	}
	if path, _, err := a.findSnapshotByID(rp.HostID, rp.SnapshotID, rp.ContainerName); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, "snapshot: "+err.Error())
		}
	}
	if strings.TrimSpace(rp.ImageRef) != "" {
		if h, err := a.Store.Host(ctx, rp.HostID); err == nil {
			if _, err := a.Docker.Run(ctx, h.Endpoint, "image", "rm", rp.ImageRef); err != nil && a.Docker.ImageExists(ctx, h.Endpoint, rp.ImageRef) {
				errs = append(errs, "restore image: "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

type recoveryCleanupResult struct {
	Removed   int      `json:"removed"`
	Skipped   int      `json:"skipped"`
	Failed    int      `json:"failed"`
	FreedData int64    `json:"freed_data_bytes"`
	Errors    []string `json:"errors"`
}

func (a *App) cleanupUnusableRestorePoints(ctx context.Context) recoveryCleanupResult {
	out := recoveryCleanupResult{Errors: []string{}}
	keep := a.activeRestorePointReferences(ctx)
	points, err := a.Store.RestorePoints(ctx, 5000, 0, "")
	if err != nil {
		out.Failed++
		out.Errors = append(out.Errors, err.Error())
		return out
	}
	for _, rp := range points {
		unusable := rp.Status == "expired" || rp.Status == "degraded" || rp.Status == "failed" || rp.IntegrityStatus == "expired" || rp.IntegrityStatus == "degraded"
		if !unusable {
			continue
		}
		if keep[rp.ID] {
			out.Skipped++
			continue
		}
		owner := fmt.Sprintf("system:recovery-cleanup:%d:%d", rp.ID, time.Now().UTC().UnixNano())
		key := hostLeaseKey(rp.HostID)
		ok, leaseErr := a.Store.AcquireOperationLease(ctx, db.OperationLease{ResourceKey: key, HostID: rp.HostID, Owner: owner, OperationType: "recovery-cleanup"}, 15*time.Minute)
		if leaseErr != nil || !ok {
			out.Skipped++
			continue
		}
		err := func() error {
			defer a.Store.ReleaseOperationLease(context.Background(), key, owner)
			if err := a.removeRestorePointArtifacts(ctx, rp); err != nil {
				return err
			}
			return a.Store.DeleteRestorePoint(ctx, rp.ID)
		}()
		if err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("restore point #%d: %v", rp.ID, err))
			continue
		}
		out.Removed++
		out.FreedData += rp.DataBytes
	}
	_ = a.Store.Audit(context.Background(), "system", "recovery.cleanup-unusable", 0, "", fmt.Sprintf("removed=%d skipped=%d failed=%d", out.Removed, out.Skipped, out.Failed))
	return out
}

func (a *App) handleRecoveryCleanupUnusable(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Minute)
	defer cancel()
	writeJSON(w, 200, a.cleanupUnusableRestorePoints(ctx))
}
