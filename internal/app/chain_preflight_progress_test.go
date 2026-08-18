package app

import "testing"

func TestChainPreflightProgressTracking(t *testing.T) {
	a := &App{chainPreflightOps: map[string]ChainPreflightProgress{}}
	a.setChainPreflightProgress(ChainPreflightProgress{OperationID: "chain-preflight-123", ChainID: 9, ChainName: "media", HostID: 2, Status: "running", Stage: "member", Message: "Checking plex", CurrentPosition: 1, Total: 2, Steps: []ChainPreflightStepView{{Position: 1, ContainerName: "plex", Status: "ready_with_warnings", Warnings: 1}}})
	got, ok := a.chainPreflightProgress("chain-preflight-123")
	if !ok {
		t.Fatal("expected progress entry")
	}
	if got.ChainID != 9 || got.CurrentPosition != 1 || got.Total != 2 || len(got.Steps) != 1 {
		t.Fatalf("unexpected progress: %#v", got)
	}
	got.Steps[0].ContainerName = "mutated"
	again, _ := a.chainPreflightProgress("chain-preflight-123")
	if again.Steps[0].ContainerName != "plex" {
		t.Fatal("progress read must return an isolated step slice")
	}
}
