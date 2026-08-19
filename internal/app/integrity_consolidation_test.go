package app

import (
	"context"
	"testing"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestMutationPipelineGateSerializesSameHostButAllowsOtherHosts(t *testing.T) {
	g := newMutationPipelineGate(3)
	ctx := context.Background()
	releaseA, err := g.acquire(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()

	sameHost := make(chan struct{})
	go func() {
		release, e := g.acquire(ctx, 7)
		if e == nil {
			close(sameHost)
			release()
		}
	}()
	select {
	case <-sameHost:
		t.Fatal("same-host lifecycle mutation entered concurrently")
	case <-time.After(40 * time.Millisecond):
	}

	otherHost := make(chan struct{})
	go func() {
		release, e := g.acquire(ctx, 8)
		if e == nil {
			close(otherHost)
			release()
		}
	}()
	select {
	case <-otherHost:
	case <-time.After(time.Second):
		t.Fatal("unrelated host was unnecessarily serialized")
	}

	releaseA()
	select {
	case <-sameHost:
	case <-time.After(time.Second):
		t.Fatal("same-host waiter did not resume after owner released")
	}
}

func TestMutationPipelineGateGlobalLimitIncludesChainLifecycle(t *testing.T) {
	g := newMutationPipelineGate(2)
	ctx := context.Background()
	r1, err := g.acquire(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	r2, err := g.acquire(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r2()

	third := make(chan struct{})
	go func() {
		r, e := g.acquire(ctx, 3)
		if e == nil {
			close(third)
			r()
		}
	}()
	select {
	case <-third:
		t.Fatal("global mutation limit was exceeded")
	case <-time.After(40 * time.Millisecond):
	}
	r1()
	select {
	case <-third:
	case <-time.After(time.Second):
		t.Fatal("global mutation waiter did not resume")
	}
}

func TestRecoveryPinClassifiesRollbackFailureAndLegacyFailure(t *testing.T) {
	cases := []db.UpdateTransaction{
		{Status: "recovery_required", RestorePointID: 1},
		{Status: "failed", RecoveryAction: "rollback_failed", RestorePointID: 2},
		{Status: "failed", Error: "update failed; automatic rollback failed: restore unavailable", RestorePointID: 3},
	}
	for _, tx := range cases {
		if !recoveryTransactionPinsRestorePoint(tx) {
			t.Fatalf("unresolved transaction should pin restore point: %+v", tx)
		}
	}
	if recoveryTransactionPinsRestorePoint(db.UpdateTransaction{Status: "success", RestorePointID: 4}) {
		t.Fatal("successful transaction must not pin restore point indefinitely")
	}
}

func TestRecoveryPinClassifiesIncompleteChainRollback(t *testing.T) {
	if !recoveryChainRunPinsRestorePoints(db.UpdateChainRun{Status: "recovery_required"}) {
		t.Fatal("recovery-required chain must pin restore points")
	}
	if !recoveryChainRunPinsRestorePoints(db.UpdateChainRun{Status: "failed", Error: "rollback of completed members incomplete: db"}) {
		t.Fatal("legacy failed chain with incomplete rollback must stay pinned")
	}
	if recoveryChainRunPinsRestorePoints(db.UpdateChainRun{Status: "success"}) {
		t.Fatal("successful chain must not pin restore points indefinitely")
	}
}

func TestFinishChainSafeCancelPreservesCompletedStepAndCancelsOpenStep(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "chain", "manual", 5, "media", "running")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.CreateUpdateChainRun(ctx, db.UpdateChainRun{ChainID: 99, ChainName: "media", HostID: 5, JobID: jobID, Trigger: "manual", Actor: "test", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	completedID, _ := s.AddUpdateChainRunStep(ctx, db.UpdateChainRunStep{RunID: runID, Position: 1, ContainerName: "a", Status: "success"})
	openID, _ := s.AddUpdateChainRunStep(ctx, db.UpdateChainRunStep{RunID: runID, Position: 2, ContainerName: "b", Status: "checking"})
	a := &App{Store: s}
	a.finishChainSafeCancel(db.UpdateChain{ID: 99, HostID: 5}, jobID, runID, "test", "cancel after settled step")
	steps, err := s.UpdateChainRunSteps(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	status := map[int64]string{}
	for _, step := range steps {
		status[step.ID] = step.Status
	}
	if status[completedID] != "success" || status[openID] != "cancelled" {
		t.Fatalf("unexpected safe-cancel step settlement: %#v", status)
	}
	job, _ := s.Job(ctx, jobID)
	if job.Status != "cancelled" {
		t.Fatalf("chain job not cancelled: %+v", job)
	}
	run, _ := s.UpdateChainRun(ctx, runID)
	if run.Status != "cancelled" || run.RecoveryAction != "safe_cancel" {
		t.Fatalf("chain run not safely cancelled: %+v", run)
	}
}

func TestExpandedRuntimeFidelityDetectsResourceLimitLoss(t *testing.T) {
	var before, after inspectContainer
	before.HostConfig.Memory = 512 * 1024 * 1024
	before.HostConfig.NanoCpus = 1500000000
	pids := int64(256)
	before.HostConfig.PidsLimit = &pids
	before.HostConfig.Tmpfs = map[string]string{"/tmp": "rw,noexec"}
	after = before
	if got := criticalRuntimeMismatches(before, after); len(got) != 0 {
		t.Fatalf("identical runtime contract mismatch: %v", got)
	}
	after.HostConfig.Memory = 0
	after.HostConfig.PidsLimit = nil
	got := criticalRuntimeMismatches(before, after)
	want := map[string]bool{"memory": true, "pids_limit": true}
	for _, field := range got {
		delete(want, field)
	}
	if len(want) != 0 {
		t.Fatalf("resource fidelity loss not detected: got=%v missing=%v", got, want)
	}
}

func TestRollbackFailureStateBlocksNewMutationUntilRecovery(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	jobID, err := s.CreateJob(ctx, "update", "manual", 9, "db", "running")
	if err != nil {
		t.Fatal(err)
	}
	txID, err := s.CreateUpdateTransaction(ctx, db.UpdateTransaction{JobID: jobID, HostID: 9, ContainerName: "db", State: txRollback, Status: "running", RestorePointID: 44})
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := s.UpdateTransaction(ctx, txID)
	a := &App{Store: s}
	if err := a.txTransition(ctx, &tx, txRecoveryRequired, "recovery_required", "automatic rollback failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUpdateTransactionRecovery(ctx, txID, "recovery_required", "rollback_failed", "restore unavailable"); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.HasRecoveryRequiredTransaction(ctx, 9, "db")
	if err != nil || !blocked {
		t.Fatalf("failed rollback must block another mutation: blocked=%v err=%v", blocked, err)
	}
	active, err := s.ActiveUpdateTransactions(ctx)
	if err != nil || len(active) != 1 || active[0].ID != txID {
		t.Fatalf("recovery-required transaction must stay active: %#v err=%v", active, err)
	}
}

func TestMutationPipelineContextIsReentrantForSameHost(t *testing.T) {
	a := &App{mutationGate: newMutationPipelineGate(1)}
	ctx, release, err := a.acquireMutationPipeline(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !mutationPipelineHeld(ctx, 42) {
		t.Fatal("mutation pipeline ownership marker missing")
	}
	start := time.Now()
	nested, nestedRelease, err := a.acquireMutationPipeline(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	defer nestedRelease()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("nested same-host rollback/recovery blocked on its own mutation gate")
	}
	if !mutationPipelineHeld(nested, 42) {
		t.Fatal("nested context lost mutation pipeline ownership")
	}
}

func TestReliabilityPrunePreservesLegacyRollbackFailure(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	// Keep the retention window deliberately small by inserting >100 terminal
	// rows; PruneReliabilityHistory enforces a minimum of 100.
	var protectedID int64
	for i := 0; i < 130; i++ {
		jobID, err := s.CreateJob(ctx, "update", "manual", 1, "svc", "failed")
		if err != nil {
			t.Fatal(err)
		}
		txID, err := s.CreateUpdateTransaction(ctx, db.UpdateTransaction{JobID: jobID, HostID: 1, ContainerName: "svc", State: txFailed, Status: "failed", RestorePointID: int64(1000 + i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			protectedID = txID
			if err := s.SetUpdateTransactionRecovery(ctx, txID, "failed", "rollback_failed", "automatic rollback failed: restore unavailable"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.PruneReliabilityHistory(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTransaction(ctx, protectedID); err != nil {
		t.Fatalf("legacy unresolved rollback transaction was pruned: %v", err)
	}
}

func TestRestorePointInsertDoesNotPruneExpiredRecoveryMetadata(t *testing.T) {
	s := newLifecycleTestStore(t)
	ctx := context.Background()
	first, err := s.AddRestorePoint(ctx, db.RestorePoint{HostID: 1, ContainerName: "svc", SnapshotID: "pinned-old", Status: "expired"})
	if err != nil {
		t.Fatal(err)
	}
	// Historically AddRestorePoint opportunistically deleted expired rows beyond
	// 5000 without understanding recovery references. Populate beyond that limit
	// and prove the oldest metadata survives until graph-aware GC removes it.
	for i := 0; i < 5005; i++ {
		if _, err := s.AddRestorePoint(ctx, db.RestorePoint{HostID: 1, ContainerName: "svc", SnapshotID: "later", Status: "expired"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RestorePoint(ctx, first); err != nil {
		t.Fatalf("restore metadata was pruned outside graph-aware recovery GC: %v", err)
	}
}
