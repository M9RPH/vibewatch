package app

import (
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func TestV095RestorePointActiveChainTriggerProtection(t *testing.T) {
	runs := []db.UpdateChainRun{{ID: 42, Status: "running"}}
	for _, trigger := range []string{"chain:42", "chain-auto:42", "chain-recreate:42"} {
		if !restorePointUsedByActiveRun(db.RestorePoint{Trigger: trigger}, runs) {
			t.Fatalf("expected %s to be protected by active chain", trigger)
		}
	}
	if restorePointUsedByActiveRun(db.RestorePoint{Trigger: "chain:41"}, runs) {
		t.Fatal("unrelated restore point must not be protected")
	}
}

func TestV095ChainTerminalStates(t *testing.T) {
	for _, status := range []string{"success", "failed", "rolled_back", "restarted", "recreated", "skipped_current", "skipped_snoozed", "interrupted"} {
		if !chainRunStepTerminal(status) {
			t.Fatalf("expected terminal state %s", status)
		}
	}
	for _, status := range []string{"checking", "updating", "restarting", "recreating"} {
		if chainRunStepTerminal(status) {
			t.Fatalf("expected non-terminal state %s", status)
		}
	}
}
