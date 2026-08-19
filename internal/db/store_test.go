package db

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostWorkerTokenDecodesFromSQLiteJSONButNeverSerializes(t *testing.T) {
	const token = "super-secret-worker-token"
	raw := `[{"id":2,"name":"Pi4","endpoint":"tcp://192.168.1.250:2375","enabled":1,"worker_token":"` + token + `","created_at":"2026-08-10T08:42:23Z"}]`
	var hosts []Host
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].WorkerToken != token {
		t.Fatalf("worker token was not restored from sqlite3 JSON: %+v", hosts)
	}

	encoded, err := json.Marshal(hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "worker_token") {
		t.Fatalf("worker token leaked through JSON serialization: %s", encoded)
	}
}

func TestBackupUsesSQLiteConsistentVacuumInto(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sqlite.log")
	script := filepath.Join(dir, "sqlite3")
	body := `#!/bin/sh
cat >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	dbPath := filepath.Join(dir, "watchtower-ui.db")
	if err := os.WriteFile(dbPath, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backups", "snapshot.db")
	s := New(dbPath)
	if err := s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "VACUUM INTO") || !strings.Contains(got, backup) {
		t.Fatalf("backup did not use VACUUM INTO for destination: %s", got)
	}
}

func TestQueuedJobCancellationAndClaimAreAtomic(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	dir := t.TempDir()
	s := New(filepath.Join(dir, "watchtower-ui.db"))
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateJob(ctx, "update", "manual", 1, "app", "queued")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelQueuedJob(ctx, id, "cancelled by test")
	if err != nil || !cancelled {
		t.Fatalf("cancel queued job: cancelled=%v err=%v", cancelled, err)
	}
	job, err := s.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancelled" || job.FinishedAt == "" {
		t.Fatalf("unexpected cancelled job state: %+v", job)
	}
	claimed, err := s.ClaimQueuedJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("a cancelled job must not be claimable")
	}

	id2, err := s.CreateJob(ctx, "update", "manual", 1, "app2", "queued")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = s.ClaimQueuedJob(ctx, id2)
	if err != nil || !claimed {
		t.Fatalf("claim queued job: claimed=%v err=%v", claimed, err)
	}
	cancelled, err = s.CancelQueuedJob(ctx, id2, "too late")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled {
		t.Fatal("a running job must not be cancellable")
	}
}

func TestRunningUpdateCanRequestCooperativeCancellation(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	dir := t.TempDir()
	s := New(filepath.Join(dir, "watchtower-ui.db"))
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateJob(ctx, "update", "manual", 7, "sabnzbdvpn", "queued")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimQueuedJob(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("claim queued update: claimed=%v err=%v", claimed, err)
	}
	requested, err := s.RequestRunningJobCancel(ctx, id, "safe cancel test")
	if err != nil || !requested {
		t.Fatalf("request running cancellation: requested=%v err=%v", requested, err)
	}
	job, err := s.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancel_requested" {
		t.Fatalf("status=%q, want cancel_requested", job.Status)
	}
	active, err := s.HasActiveJob(ctx, 7, "sabnzbdvpn")
	if err != nil || !active {
		t.Fatalf("cancel_requested job must continue reserving its target: active=%v err=%v", active, err)
	}
	requested, err = s.RequestRunningJobCancel(ctx, id, "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if requested {
		t.Fatal("duplicate request must not rewrite an already cancel_requested job")
	}
}
