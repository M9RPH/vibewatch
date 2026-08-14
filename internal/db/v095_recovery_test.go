package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestV095ChainRunPersistsRecoveryContext(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	ctx := context.Background()
	s := &Store{Path: filepath.Join(t.TempDir(), "vibewatch.db")}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateJob(ctx, "chain", "chain-manual", 7, "Core", "queued")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.CreateUpdateChainRun(ctx, UpdateChainRun{ChainID: 9, ChainName: "Core", HostID: 7, JobID: jobID, Trigger: "manual", Actor: "owner", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUpdateChainRunRecovery(ctx, runID, "recovered", "reconciled_started_steps", "controller restarted", true); err != nil {
		t.Fatal(err)
	}
	run, err := s.UpdateChainRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.JobID != jobID || run.Status != "recovered" || run.RecoveryAction != "reconciled_started_steps" || run.RecoveredAt == "" || run.FinishedAt == "" {
		t.Fatalf("unexpected recovered run: %+v", run)
	}
}

func TestV095StartupDoesNotFailActiveChainJobBeforeRecovery(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	ctx := context.Background()
	s := &Store{Path: filepath.Join(t.TempDir(), "vibewatch.db")}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateJob(ctx, "chain", "chain-manual", 1, "Immich", "queued")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartJob(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUpdateChainRun(ctx, UpdateChainRun{ChainID: 1, ChainName: "Immich", HostID: 1, JobID: jobID, Trigger: "manual", Actor: "owner", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	count, err := s.FailActiveJobsWithoutTransaction(ctx, "controller restarted")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("active chain job must be preserved for chain recovery, got failed count %d", count)
	}
	job, err := s.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "running" {
		t.Fatalf("chain job was prematurely failed: %+v", job)
	}
}
