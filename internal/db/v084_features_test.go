package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestV084VerificationHistoryAndChainsRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveVerificationProfile(ctx, VerificationProfile{HostID: 1, ScopeType: "container", ScopeKey: "app", Enabled: Bool(true), StartDelaySeconds: 4, RetryCount: 2, RetryIntervalSeconds: 3, ChecksJSON: `[{"type":"http","url":"http://app/health"}]`}); err != nil {
		t.Fatal(err)
	}
	p, err := s.VerificationProfile(ctx, 1, "container", "app")
	if err != nil {
		t.Fatal(err)
	}
	if !bool(p.Enabled) || p.RetryCount != 2 {
		t.Fatalf("profile lost: %#v", p)
	}
	if err := s.SaveVerificationState(ctx, VerificationState{HostID: 1, ContainerName: "app", Status: "verified", DetailsJSON: `[]`, CheckedAt: "2026-08-12T20:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	st, err := s.VerificationState(ctx, 1, "app")
	if err != nil || st.Status != "verified" {
		t.Fatalf("state lost: %#v err=%v", st, err)
	}
	hid, err := s.AddUpdateHistory(ctx, UpdateHistory{HostID: 1, ContainerName: "app", Status: "success", PreflightStatus: "ready_with_warnings", PreflightDetails: `[{"key":"docker_health"}]`, VerificationStatus: "verified", VerificationDetails: `{"status":"verified"}`})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.UpdateHistoryEntry(ctx, hid)
	if err != nil {
		t.Fatal(err)
	}
	if h.PreflightStatus != "ready_with_warnings" || h.VerificationStatus != "verified" {
		t.Fatalf("history fields lost: %#v", h)
	}
	cid, err := s.SaveUpdateChain(ctx, UpdateChain{Name: "Core", HostID: 1, AutomationID: 7, ScopeType: "stack", ScopeKey: "core", PolicyMode: "auto", StopOnFailure: Bool(true), RollbackCompleted: Bool(true)}, []UpdateChainStep{{ContainerName: "redis", Position: 1, WaitSeconds: 2}, {ContainerName: "app", Position: 2}})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := s.UpdateChain(ctx, cid)
	if err != nil || chain.AutomationID != 7 || chain.ScopeType != "stack" || chain.ScopeKey != "core" || chain.PolicyMode != "auto" {
		t.Fatalf("chain stack policy binding lost: %#v err=%v", chain, err)
	}
	steps, err := s.UpdateChainSteps(ctx, cid)
	if err != nil || len(steps) != 2 || steps[0].ContainerName != "redis" {
		t.Fatalf("chain lost: %#v err=%v", steps, err)
	}
	rid, err := s.CreateUpdateChainRun(ctx, UpdateChainRun{ChainID: cid, ChainName: "Core", HostID: 1, Trigger: "manual", Actor: "owner", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUpdateChainRunStep(ctx, UpdateChainRunStep{RunID: rid, Position: 1, ContainerName: "redis", Status: "updating"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.FailActiveUpdateChainRuns(ctx, "controller restarted")
	if err != nil || n != 1 {
		t.Fatalf("recovery failed n=%d err=%v", n, err)
	}
	runs, err := s.UpdateChainRuns(ctx, cid, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("interrupted run not failed: %#v err=%v", runs, err)
	}
}

func TestV084MigrationFromPreflightLessHistory(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vibewatch.db")
	// Minimal v0.8.3.2-compatible history table: the v0.8.4 columns do not exist yet.
	sql := `CREATE TABLE update_history (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, action TEXT NOT NULL DEFAULT 'update', trigger TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, from_version TEXT NOT NULL DEFAULT '', to_version TEXT NOT NULL DEFAULT '', from_image_ref TEXT NOT NULL DEFAULT '', to_image_ref TEXT NOT NULL DEFAULT '', from_digest TEXT NOT NULL DEFAULT '', to_digest TEXT NOT NULL DEFAULT '', snapshot_id TEXT NOT NULL DEFAULT '', restore_point_id INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', dependency_count INTEGER NOT NULL DEFAULT 0, dependency_status TEXT NOT NULL DEFAULT '', dependency_details TEXT NOT NULL DEFAULT ''); INSERT INTO update_history(ts,host_id,container_name,status) VALUES('2026-08-12T20:00:00Z',1,'legacy','success');`
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("old schema create: %v: %s", err, out)
	}
	s := New(path)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UpdateHistory(ctx, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ContainerName != "legacy" || rows[0].PreflightStatus != "" || rows[0].VerificationStatus != "" {
		t.Fatalf("legacy history migration mismatch: %#v", rows)
	}
	// New v0.8.4 tables must be immediately usable after the same Init call.
	if err := s.SaveVerificationProfile(ctx, VerificationProfile{HostID: 1, ScopeType: "container", ScopeKey: "legacy", Enabled: Bool(true), ChecksJSON: `[{"type":"tcp","host":"127.0.0.1","port":80}]`}); err != nil {
		t.Fatal(err)
	}
}

func TestV084DeletingAutomationUnbindsChains(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	aid, err := s.SaveAutomation(ctx, Automation{Name: "Nightly", Cron: "0 4 * * *", TargetType: "host", TargetID: 1, Enabled: Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	cid, err := s.SaveUpdateChain(ctx, UpdateChain{Name: "Core", HostID: 1, AutomationID: aid, StopOnFailure: Bool(true)}, []UpdateChainStep{{ContainerName: "app", Position: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAutomation(ctx, aid); err != nil {
		t.Fatal(err)
	}
	chain, err := s.UpdateChain(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	if chain.AutomationID != 0 {
		t.Fatalf("deleted automation must leave chain manual-only, got automation_id=%d", chain.AutomationID)
	}
}

func TestV0842MigratesLegacyChainsToCustomPolicyInheritance(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vibewatch.db")
	sql := `CREATE TABLE update_chains (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, automation_id INTEGER NOT NULL DEFAULT 0, stop_on_failure INTEGER NOT NULL DEFAULT 1, rollback_completed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, last_run_at TEXT NOT NULL DEFAULT '', last_status TEXT NOT NULL DEFAULT 'never'); INSERT INTO update_chains(name,host_id,created_at,updated_at) VALUES('Legacy',1,'now','now');`
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("legacy chain schema: %v: %s", err, out)
	}
	store := New(path)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	chains, err := store.UpdateChains(ctx)
	if err != nil || len(chains) != 1 {
		t.Fatalf("legacy chain missing: %#v err=%v", chains, err)
	}
	if chains[0].ScopeType != "custom" || chains[0].PolicyMode != "inherit" || chains[0].ScopeKey != "" {
		t.Fatalf("legacy chain migration must preserve v0.8.4.1 semantics: %#v", chains[0])
	}
}

func TestV0842CurrentActionMigrationDefaultsToSkip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vibewatch.db")
	sql := `CREATE TABLE update_chains (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, automation_id INTEGER NOT NULL DEFAULT 0, scope_type TEXT NOT NULL DEFAULT 'custom', scope_key TEXT NOT NULL DEFAULT '', policy_mode TEXT NOT NULL DEFAULT 'inherit', stop_on_failure INTEGER NOT NULL DEFAULT 1, rollback_completed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, last_run_at TEXT NOT NULL DEFAULT '', last_status TEXT NOT NULL DEFAULT 'never'); CREATE TABLE update_chain_steps (id INTEGER PRIMARY KEY AUTOINCREMENT, chain_id INTEGER NOT NULL, position INTEGER NOT NULL, container_name TEXT NOT NULL, wait_seconds INTEGER NOT NULL DEFAULT 0, UNIQUE(chain_id,position)); INSERT INTO update_chains(name,host_id,created_at,updated_at) VALUES('Legacy',1,'now','now'); INSERT INTO update_chain_steps(chain_id,position,container_name,wait_seconds) VALUES(1,1,'web',0);`
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		t.Fatalf("legacy chain step schema: %v: %s", err, out)
	}
	store := New(path)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := store.UpdateChainSteps(ctx, 1)
	if err != nil || len(steps) != 1 {
		t.Fatalf("migrated steps missing: %#v err=%v", steps, err)
	}
	if steps[0].CurrentAction != "skip" {
		t.Fatalf("legacy step must default to skip, got %#v", steps[0])
	}
}
