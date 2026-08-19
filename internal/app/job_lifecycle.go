package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

// registerJobCancellation gives a running update a cooperative cancellation
// signal. Cancellation is deliberately separated from the controller context:
// once an atomic Docker mutation begins, the pipeline switches to a non-user-
// cancellable context and settles the runtime before honoring the request.
func (a *App) registerJobCancellation(jobID int64) (context.Context, func()) {
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	a.jobCancelMu.Lock()
	if a.jobCancels == nil {
		a.jobCancels = map[int64]context.CancelFunc{}
	}
	a.jobCancels[jobID] = cancel
	a.jobCancelMu.Unlock()

	// Close the tiny race where the DB status can become cancel_requested after
	// ClaimQueuedJob but before this in-memory control is registered.
	if job, err := a.Store.Job(context.Background(), jobID); err == nil && job.Status == "cancel_requested" {
		cancel()
	}
	return ctx, func() {
		a.jobCancelMu.Lock()
		delete(a.jobCancels, jobID)
		a.jobCancelMu.Unlock()
		cancel()
	}
}

// registerJobCancellationSignal registers a cooperative cancellation owner
// without deriving the chain's execution context from it. Chains must finish an
// already-started child update/restart/recreate atomically and only stop before
// the next step, so the signal is observed explicitly between safe points.
func (a *App) registerJobCancellationSignal(jobID int64) (<-chan struct{}, func()) {
	signalCtx, cancel := context.WithCancel(context.Background())
	a.jobCancelMu.Lock()
	if a.jobCancels == nil {
		a.jobCancels = map[int64]context.CancelFunc{}
	}
	a.jobCancels[jobID] = cancel
	a.jobCancelMu.Unlock()
	if job, err := a.Store.Job(context.Background(), jobID); err == nil && job.Status == "cancel_requested" {
		cancel()
	}
	return signalCtx.Done(), func() {
		a.jobCancelMu.Lock()
		delete(a.jobCancels, jobID)
		a.jobCancelMu.Unlock()
		cancel()
	}
}

func (a *App) signalJobCancellation(jobID int64) bool {
	a.jobCancelMu.Lock()
	cancel := a.jobCancels[jobID]
	a.jobCancelMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (a *App) jobCancelRequested(_ context.Context, jobID int64) bool {
	// Persisted job state is authoritative. The per-job context is also derived
	// from the controller context, so ctx.Err() alone cannot distinguish a user
	// safe-cancel from an ordinary controller shutdown/restart.
	job, err := a.Store.Job(context.Background(), jobID)
	return err == nil && job.Status == "cancel_requested"
}

// finishJobDurable must not inherit a cancelled request/update context. A
// terminal job write is part of Vibewatch's transaction durability contract;
// bounded retries prevent a transient sqlite/CLI contention from leaving a
// completed goroutine permanently visible as "running".
func (a *App) finishJobDurable(jobID int64, status, summary, errMsg string) error {
	delays := []time.Duration{0, 200 * time.Millisecond, 750 * time.Millisecond, 1500 * time.Millisecond}
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		lastErr = a.Store.FinishJob(ctx, jobID, status, summary, errMsg)
		cancel()
		if lastErr == nil {
			return nil
		}
	}
	if a.Logger != nil {
		a.Logger.Error("durable job finalization failed", "job_id", jobID, "status", status, "error", lastErr)
	}
	return lastErr
}

func terminalJobStateForTransaction(tx db.UpdateTransaction) (status, summary, errMsg string) {
	switch tx.State {
	case txSuccess:
		return "success", `{"reconciled_terminal_transaction":true}`, ""
	case txSkipped:
		return "skipped", `{"reconciled_terminal_transaction":true}`, tx.Error
	case txCancelled:
		return "cancelled", `{"reconciled_terminal_transaction":true}`, ""
	case txRolledBack:
		return "failed", `{"reconciled_terminal_transaction":true,"rolled_back":true}`, firstNonEmpty(tx.Error, "update failed and was rolled back")
	default:
		return "failed", `{"reconciled_terminal_transaction":true}`, firstNonEmpty(tx.Error, "update transaction failed")
	}
}

func (a *App) cancelUpdateBeforeMutation(req updateRequest, tx *db.UpdateTransaction, message string) {
	if strings.TrimSpace(message) == "" {
		message = "safe cancellation requested before image mutation"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if tx != nil && tx.ID > 0 {
		if fresh, err := a.Store.UpdateTransaction(ctx, tx.ID); err == nil {
			*tx = fresh
		}
	}
	if tx != nil && tx.ID > 0 && !transactionTerminalState(tx.State) && preMutationTransactionState(tx.State) {
		_ = a.txTransition(ctx, tx, txCancelled, "cancelled", message)
		_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "cancelled", "safe_cancel", "")
	}
	a.jobProgress(context.Background(), req.JobID, 100, "Cancelled safely before image mutation")
	_ = a.Store.AddJobLog(context.Background(), req.JobID, "WARN", "app", message)
	_ = a.finishJobDurable(req.JobID, "cancelled", `{"safe_cancel":true,"mutation_started":false}`, "")
	_ = a.Store.Audit(context.Background(), req.Actor, "job.cancelled-safe", req.HostID, req.Container, fmt.Sprintf("job=%d transaction=%d", req.JobID, req.TransactionID))
}

// ensureUpdateJobTerminal is the final safety net for every executeUpdate
// return path. It intentionally runs before history recording (defer LIFO), so
// history can never preserve a stale running status after the goroutine ended.
func (a *App) ensureUpdateJobTerminal(req updateRequest, txID int64) {
	job, err := a.Store.Job(context.Background(), req.JobID)
	if err != nil || (job.Status != "running" && job.Status != "cancel_requested") {
		return
	}
	tx, txErr := a.Store.UpdateTransactionByJob(context.Background(), req.JobID)
	if txErr != nil {
		msg := "update pipeline exited without a persisted transaction terminal state"
		if job.Status == "cancel_requested" {
			_ = a.finishJobDurable(req.JobID, "cancelled", `{"safe_cancel":true,"mutation_started":false}`, "")
		} else {
			_ = a.finishJobDurable(req.JobID, "failed", "", msg)
		}
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), req.JobID)
		return
	}
	if transactionTerminalState(tx.State) {
		status, summary, errMsg := terminalJobStateForTransaction(tx)
		_ = a.finishJobDurable(req.JobID, status, summary, errMsg)
		return
	}
	if preMutationTransactionState(tx.State) {
		if job.Status == "cancel_requested" {
			a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation reconciled while the update was still pre-mutation")
			return
		}
		msg := fmt.Sprintf("update pipeline exited unexpectedly before image mutation (transaction #%d state=%s)", tx.ID, tx.State)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = a.txTransition(ctx, &tx, txFailed, "failed", msg)
		cancel()
		_ = a.finishJobDurable(req.JobID, "failed", "", msg)
		return
	}

	// A return after mutation without a terminal transaction is ambiguous. Never
	// claim it was cancelled: persist recovery_required and terminate the UI job,
	// so a subsequent recovery pass can reconcile or roll back safely.
	msg := fmt.Sprintf("update pipeline exited after image mutation without terminal settlement (transaction #%d state=%s); recovery is required", tx.ID, tx.State)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	_ = a.Store.SetUpdateTransactionRecovery(ctx, tx.ID, "recovery_required", "pipeline_exit", msg)
	cancel()
	_ = a.finishJobDurable(req.JobID, "failed", `{"recovery_required":true}`, msg)
}

func (a *App) reconcileOrphanedUpdateCancel(job db.Job, actor string) (string, error) {
	tx, err := a.Store.UpdateTransactionByJob(context.Background(), job.ID)
	if err != nil {
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), job.ID)
		if e := a.finishJobDurable(job.ID, "cancelled", `{"safe_cancel":true,"orphaned":true}`, ""); e != nil {
			return "", e
		}
		return "cancelled", nil
	}
	if transactionTerminalState(tx.State) {
		status, summary, errMsg := terminalJobStateForTransaction(tx)
		if e := a.finishJobDurable(job.ID, status, summary, errMsg); e != nil {
			return "", e
		}
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), job.ID)
		return status, nil
	}
	if preMutationTransactionState(tx.State) {
		req := updateRequest{JobID: job.ID, TransactionID: tx.ID, HostID: job.HostID, Container: job.ContainerName, Trigger: job.Trigger, Actor: actor}
		a.cancelUpdateBeforeMutation(req, &tx, "safe cancellation reconciled for an orphaned pre-mutation update")
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), job.ID)
		return "cancelled", nil
	}

	// No active goroutine owns this post-mutation transaction. Recovery is the
	// only safe action; it may keep a verified updated runtime or roll it back.
	_ = a.Store.AddJobLog(context.Background(), job.ID, "WARN", "recovery", "safe cancel found an orphaned post-mutation transaction; starting transaction recovery")
	go a.recoverUpdateTransaction(tx)
	return "recovering", nil
}

// reconcileOrphanedChainCancel handles a persisted running/cancel_requested
// chain job whose in-memory executor no longer exists. Never guess that the
// current step is safe: release only the chain-owned lease and enter the same
// crash-recovery path used after a controller restart.
func (a *App) reconcileOrphanedChainCancel(job db.Job, actor string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run, err := a.Store.UpdateChainRunByJob(ctx, job.ID)
	if err != nil {
		if finishErr := a.finishJobDurable(job.ID, "cancelled", `{"safe_cancel":true,"orphaned":true}`, ""); finishErr != nil {
			return "", finishErr
		}
		_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), job.ID)
		return "orphaned_cancelled", nil
	}
	_ = a.Store.ReleaseOperationLeaseByJob(context.Background(), job.ID)
	msg := "safe cancellation found an orphaned chain executor; transaction-safe chain recovery is required before the run can be settled"
	_ = a.Store.SetUpdateChainRunRecovery(ctx, run.ID, "recovery_required", "safe_cancel_orphaned", msg, false)
	_ = a.Store.AddJobLog(context.Background(), job.ID, "WARN", "recovery", msg)
	_ = a.Store.Audit(context.Background(), actor, "chain.cancel-orphaned", job.HostID, "", fmt.Sprintf("job=%d run=%d", job.ID, run.ID))
	go a.recoverInterruptedChainRun(run)
	return "recovery", nil
}
