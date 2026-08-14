package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestV093PersistsAutomaticPreflightWarningPolicy(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}

	if err := s.SavePolicy(ctx, Policy{HostID: 1, ContainerName: "upsnap", Mode: "auto", CheckIntervalMinutes: 30, AllowPreflightWarnings: Bool(true)}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Policy(ctx, 1, "upsnap")
	if err != nil {
		t.Fatal(err)
	}
	if !bool(p.AllowPreflightWarnings) {
		t.Fatal("container policy lost allow_preflight_warnings")
	}

	id, err := s.SaveUpdateChain(ctx, UpdateChain{Name: "immich", HostID: 1, ScopeType: "stack", ScopeKey: "immich", PolicyMode: "auto", AllowPreflightWarnings: Bool(true), StopOnFailure: Bool(true)}, []UpdateChainStep{{ContainerName: "immich_server", Position: 1}})
	if err != nil {
		t.Fatal(err)
	}
	chains, err := s.UpdateChains(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range chains {
		if c.ID == id {
			found = true
			if !bool(c.AllowPreflightWarnings) {
				t.Fatal("stack chain lost allow_preflight_warnings")
			}
		}
	}
	if !found {
		t.Fatalf("saved chain %d not returned", id)
	}
}
