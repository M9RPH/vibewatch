package app

import (
	"context"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestV094RestoreStorageRequirementAddsSafetyReserve(t *testing.T) {
	if got := restoreStorageRequirementBytes(0); got != restoreStorageBaseReserveBytes {
		t.Fatalf("empty payload should still reserve %d bytes, got %d", restoreStorageBaseReserveBytes, got)
	}
	oneGiB := int64(1024 * 1024 * 1024)
	if got := restoreStorageRequirementBytes(oneGiB); got != oneGiB+restoreStorageBaseReserveBytes {
		t.Fatalf("1 GiB payload should use base reserve, got %d", got)
	}
	tenGiB := int64(10 * 1024 * 1024 * 1024)
	want := tenGiB + tenGiB*restoreStorageReservePercent/100
	if got := restoreStorageRequirementBytes(tenGiB); got != want {
		t.Fatalf("large payload should use percentage reserve: want %d got %d", want, got)
	}
}

func TestV094CleanupAutomationActionsAreExplicit(t *testing.T) {
	rule := db.Automation{Kind: "cleanup", CleanupImages: db.Bool(true), CleanupBuildCache: db.Bool(true)}
	actions := automationCleanupActions(rule)
	if len(actions) != 2 || actions[0] != "images" || actions[1] != "build-cache" {
		t.Fatalf("unexpected cleanup actions: %#v", actions)
	}
	if got := normalizeAutomationKind(""); got != "policy" {
		t.Fatalf("legacy automations must default to policy, got %q", got)
	}
}

func TestPhase9AutomationTargetsHostKeepsPolicyAndEnabledGuards(t *testing.T) {
	a := &App{}
	if !a.automationTargetsHost(context.Background(), db.Automation{Kind: "policy", Enabled: db.Bool(true), TargetType: "all"}, 9) {
		t.Fatal("enabled policy automation targeting all hosts must include the host")
	}
	if !a.automationTargetsHost(context.Background(), db.Automation{Kind: "", Enabled: db.Bool(true), TargetType: "host", TargetID: 9}, 9) {
		t.Fatal("legacy empty-kind automation must retain policy semantics")
	}
	if a.automationTargetsHost(context.Background(), db.Automation{Kind: "cleanup", Enabled: db.Bool(true), TargetType: "all"}, 9) {
		t.Fatal("cleanup automation must not be treated as a policy automation")
	}
	if a.automationTargetsHost(context.Background(), db.Automation{Kind: "policy", Enabled: db.Bool(false), TargetType: "all"}, 9) {
		t.Fatal("disabled policy automation must not be treated as active")
	}
	if a.automationTargetsHost(context.Background(), db.Automation{Kind: "policy", Enabled: db.Bool(true), TargetType: "host", TargetID: 10}, 9) {
		t.Fatal("host-targeted automation must not include a different host")
	}
}
