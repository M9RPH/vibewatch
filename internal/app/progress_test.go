package app

import (
	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"testing"
)

func TestLatestJobProgressUsesNewestProgressEntry(t *testing.T) {
	logs := []db.JobLog{
		{Source: "progress", Message: "10|Starting update"},
		{Source: "app", Message: "noise"},
		{Source: "progress", Message: "38|Update engine working"},
	}
	pct, stage := latestJobProgress(logs, "running")
	if pct != 38 || stage != "Update engine working" {
		t.Fatalf("got %d %q", pct, stage)
	}
}

func TestLatestJobProgressTerminalStatusIsOneHundred(t *testing.T) {
	logs := []db.JobLog{{Source: "progress", Message: "45|Checking registry"}}
	pct, _ := latestJobProgress(logs, "success")
	if pct != 100 {
		t.Fatalf("success should be 100%%, got %d", pct)
	}
	pct, _ = latestJobProgress(nil, "failed")
	if pct != 100 {
		t.Fatalf("failed should be terminal 100%%, got %d", pct)
	}
	pct, stage := latestJobProgress(nil, "cancelled")
	if pct != 100 || stage != "Cancelled before execution" {
		t.Fatalf("cancelled should be terminal 100%% with cancellation stage, got %d %q", pct, stage)
	}
}
