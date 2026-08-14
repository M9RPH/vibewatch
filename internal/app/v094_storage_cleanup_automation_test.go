package app

import (
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
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
