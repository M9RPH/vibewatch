package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func newLifecycleTestStore(t *testing.T) *db.Store {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	s := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnsureUpdateJobTerminalReconcilesTerminalTransaction(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "update", "manual", 5, "sabnzbdvpn", "running")
	if err != nil {
		t.Fatal(err)
	}
	txID, err := s.CreateUpdateTransaction(ctx, db.UpdateTransaction{JobID: jobID, HostID: 5, ContainerName: "sabnzbdvpn", Trigger: "manual", Actor: "test", State: txFailed, Status: "failed", Error: "protected service recovery failed"})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: s}
	a.ensureUpdateJobTerminal(updateRequest{JobID: jobID, HostID: 5, Container: "sabnzbdvpn", Trigger: "manual", Actor: "test"}, txID)
	job, err := s.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.FinishedAt == "" {
		t.Fatalf("terminal transaction did not settle stale running job: %+v", job)
	}
}

func TestEnsureUpdateJobTerminalHonorsPreMutationCancelRequest(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "update", "manual", 5, "sabnzbdvpn", "running")
	if err != nil {
		t.Fatal(err)
	}
	txID, err := s.CreateUpdateTransaction(ctx, db.UpdateTransaction{JobID: jobID, HostID: 5, ContainerName: "sabnzbdvpn", Trigger: "manual", Actor: "test", State: txPrepared, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RequestRunningJobCancel(ctx, jobID, "test cancel"); err != nil || !ok {
		t.Fatalf("request cancel: ok=%v err=%v", ok, err)
	}
	a := &App{Store: s}
	a.ensureUpdateJobTerminal(updateRequest{JobID: jobID, TransactionID: txID, HostID: 5, Container: "sabnzbdvpn", Trigger: "manual", Actor: "test"}, txID)
	job, err := s.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancelled" || job.FinishedAt == "" {
		t.Fatalf("pre-mutation cancel did not settle job: %+v", job)
	}
	tx, err := s.UpdateTransaction(ctx, txID)
	if err != nil {
		t.Fatal(err)
	}
	if tx.State != txCancelled || tx.Status != "cancelled" {
		t.Fatalf("transaction not cancelled safely: state=%s status=%s", tx.State, tx.Status)
	}
}

func TestStartupReconcilesStaleRunningJobFromTerminalTransaction(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "update", "manual", 5, "sabnzbdvpn", "running")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUpdateTransaction(ctx, db.UpdateTransaction{
		JobID: jobID, HostID: 5, ContainerName: "sabnzbdvpn", Trigger: "manual", Actor: "test",
		State: txFailed, Status: "failed", Error: "protected service did not recover after cold snapshot",
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{Store: s, ctx: context.Background()}
	a.recoverInterruptedJobs()
	job, err := s.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.FinishedAt == "" {
		t.Fatalf("startup did not reconcile stale active job: %+v", job)
	}
	active, err := s.HasActiveJob(ctx, 5, "sabnzbdvpn")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("reconciled terminal job must not keep the target reserved")
	}
}

func TestCancellationOwnerRegisteredBeforeRunningClaim(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "update", "manual", 5, "sabnzbdvpn", "queued")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: s, ctx: context.Background(), jobCancels: map[int64]context.CancelFunc{}}
	jobCtx, cleanup := a.registerJobCancellation(jobID)
	defer cleanup()
	claimed, err := s.ClaimQueuedJob(ctx, jobID)
	if err != nil || !claimed {
		t.Fatalf("claim job: claimed=%v err=%v", claimed, err)
	}
	requested, err := s.RequestRunningJobCancel(ctx, jobID, "race test")
	if err != nil || !requested {
		t.Fatalf("request cancel: requested=%v err=%v", requested, err)
	}
	if !a.signalJobCancellation(jobID) {
		t.Fatal("running claim must already have an in-memory cancellation owner")
	}
	select {
	case <-jobCtx.Done():
	default:
		t.Fatal("registered job context was not cancelled")
	}
	if !a.jobCancelRequested(jobCtx, jobID) {
		t.Fatal("persisted cancellation request was not observable")
	}
}
