package app

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestNormalizeChainInputOrdersSteps(t *testing.T) {
	in, err := normalizeChainInput(updateChainInput{Name: "Paperless", HostID: 1, StopOnFailure: true, Steps: []db.UpdateChainStep{{Position: 9, ContainerName: "redis", WaitSeconds: 2}, {Position: 4, ContainerName: "paperless", WaitSeconds: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	if in.Steps[0].Position != 1 || in.Steps[1].Position != 2 {
		t.Fatalf("positions not normalized: %#v", in.Steps)
	}
	if in.Steps[0].CurrentAction != "skip" || in.Steps[1].CurrentAction != "skip" {
		t.Fatalf("legacy/default current action must be skip: %#v", in.Steps)
	}
	if _, err := normalizeChainInput(updateChainInput{Name: "dup", HostID: 1, Steps: []db.UpdateChainStep{{ContainerName: "redis"}, {ContainerName: "redis"}}}); err == nil {
		t.Fatal("duplicate members must be rejected")
	}
}

func TestNormalizeStackChainOwnsPolicy(t *testing.T) {
	in, err := normalizeChainInput(updateChainInput{Name: "Paperless", HostID: 1, ScopeType: "stack", ScopeKey: "paperless", PolicyMode: "auto", Steps: []db.UpdateChainStep{{ContainerName: "db"}, {ContainerName: "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	if in.ScopeType != "stack" || in.ScopeKey != "paperless" || in.PolicyMode != "auto" {
		t.Fatalf("stack chain normalization mismatch: %#v", in)
	}
	if _, err := normalizeChainInput(updateChainInput{Name: "bad", HostID: 1, ScopeType: "stack", Steps: []db.UpdateChainStep{{ContainerName: "web"}}}); err == nil {
		t.Fatal("stack chain without a stack key must be rejected")
	}
	custom, err := normalizeChainInput(updateChainInput{Name: "custom", HostID: 1, ScopeType: "custom", ScopeKey: "ignored", PolicyMode: "auto", Steps: []db.UpdateChainStep{{ContainerName: "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	if custom.ScopeKey != "" || custom.PolicyMode != "auto" {
		t.Fatalf("custom chains must own member policy just like stack chains: %#v", custom)
	}
}

func TestCriticalChainFailureClassification(t *testing.T) {
	for _, v := range []string{
		"preflight blocked",
		"custom verification failed",
		"automatic rollback failed",
		"restore point failed",
		"dependency recreation failed",
		"registry manifest unavailable",
		"worker did not become ready",
		"Docker host connection timed out",
		"context deadline exceeded",
	} {
		if !criticalChainFailure(fmt.Errorf("%s", v)) {
			t.Fatalf("expected critical chain failure for %q", v)
		}
	}
	if criticalChainFailure(fmt.Errorf("application-specific update step returned a non-critical error")) {
		t.Fatal("ordinary application failure must remain controlled by StopOnFailure")
	}
}

func TestNormalizeChainCurrentAction(t *testing.T) {
	in, err := normalizeChainInput(updateChainInput{Name: "actions", HostID: 1, Steps: []db.UpdateChainStep{{ContainerName: "a", CurrentAction: "restart"}, {ContainerName: "b", CurrentAction: "recreate"}}})
	if err != nil {
		t.Fatal(err)
	}
	if in.Steps[0].CurrentAction != "restart" || in.Steps[1].CurrentAction != "recreate" {
		t.Fatalf("current actions not preserved: %#v", in.Steps)
	}
	if _, err := normalizeChainInput(updateChainInput{Name: "bad-action", HostID: 1, Steps: []db.UpdateChainStep{{ContainerName: "a", CurrentAction: "destroy"}}}); err == nil {
		t.Fatal("invalid current action must be rejected")
	}
}

func TestChainMemberManagementLocksEveryMember(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	store := db.New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := store.SaveUpdateChain(ctx, db.UpdateChain{Name: "Photos", HostID: 7, ScopeType: "stack", ScopeKey: "photos", PolicyMode: "auto"}, []db.UpdateChainStep{{ContainerName: "photos-web"}, {ContainerName: "photos-db"}, {ContainerName: "photos-cache"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveUpdateChain(ctx, db.UpdateChain{Name: "Custom", HostID: 7, ScopeType: "custom", PolicyMode: "manual"}, []db.UpdateChainStep{{ContainerName: "standalone"}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	managed, err := a.chainMemberManagement(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"photos-web", "photos-db", "photos-cache"} {
		cm, ok := managed[name]
		if !ok {
			t.Fatalf("expected %s to be chain managed: %#v", name, managed)
		}
		if cm.ChainName != "Photos" || cm.PolicyMode != "auto" {
			t.Fatalf("unexpected chain management for %s: %#v", name, cm)
		}
	}
	if cm, ok := managed["standalone"]; !ok {
		t.Fatal("custom-chain member must be chain managed")
	} else if cm.ChainName != "Custom" || cm.PolicyMode != "manual" {
		t.Fatalf("unexpected custom chain management: %#v", cm)
	}
}
