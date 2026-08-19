package app

import (
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestUpdateTrackingSnoozeExpiresOnNextDigest(t *testing.T) {
	first := "2026-08-11T10:00:00Z"
	old := db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:old", LatestDigest: "sha256:new1"}
	tracked := applyTrackedUpdateState(old, db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:old", LatestDigest: "sha256:new1"}, true, first)
	if !bool(tracked.UpdateAvailable) || tracked.FirstDetectedAt != first {
		t.Fatalf("first detection not tracked: %+v", tracked)
	}

	tracked.SnoozedDigest = "sha256:new1"
	tracked.SnoozedAt = "2026-08-11T10:05:00Z"
	tracked.UpdateAvailable = false
	same := applyTrackedUpdateState(tracked, db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:old", LatestDigest: "sha256:new1"}, true, "2026-08-11T11:00:00Z")
	if bool(same.UpdateAvailable) || !cacheHasSnoozedUpdate(same) || same.FirstDetectedAt != first {
		t.Fatalf("same digest should remain snoozed: %+v", same)
	}

	nextAt := "2026-08-12T08:00:00Z"
	next := applyTrackedUpdateState(same, db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:old", LatestDigest: "sha256:new2"}, true, nextAt)
	if !bool(next.UpdateAvailable) || cacheHasSnoozedUpdate(next) || next.SnoozedDigest != "" || next.FirstDetectedAt != nextAt {
		t.Fatalf("new digest should clear snooze and become available: %+v", next)
	}
}

func TestUpdateTrackingClearsWhenCurrent(t *testing.T) {
	old := db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:old", LatestDigest: "sha256:new", FirstDetectedAt: "x", SnoozedDigest: "sha256:new", SnoozedAt: "y"}
	next := applyTrackedUpdateState(old, db.Cache{HostID: 1, ContainerName: "demo", CurrentDigest: "sha256:new", LatestDigest: "sha256:new"}, false, "2026-08-11T12:00:00Z")
	if bool(next.UpdateAvailable) || next.FirstDetectedAt != "" || next.SnoozedDigest != "" || next.SnoozedAt != "" {
		t.Fatalf("current image should clear pending update tracking: %+v", next)
	}
}

func TestCacheHasSnoozedUpdateSurvivesDigestlessPostRollbackCheck(t *testing.T) {
	c := db.Cache{SnoozedDigest: "sha256:new", SnoozedAt: "2026-08-14T08:51:57Z", LatestDigest: "", CurrentDigest: ""}
	if !cacheHasSnoozedUpdate(c) {
		t.Fatal("explicit rollback/manual snooze must survive a digest-less check response")
	}
}

func TestRollbackSnoozedFromHistory(t *testing.T) {
	c := db.Cache{SnoozedDigest: "sha256:new", SnoozedAt: "2026-08-14T08:51:57Z"}
	h := []db.UpdateHistory{{Action: "update", Status: "failed", TS: "2026-08-14T08:51:59.997684879Z"}, {Action: "rollback", Status: "success", TS: "2026-08-14T08:51:57.556648172Z"}}
	if !rollbackSnoozedFromHistory(c, h) {
		t.Fatal("successful rollback adjacent to snooze timestamp should be identified as rollback snooze")
	}
}

func TestClearUpdateSnoozeRestoresKnownTargetAfterRollbackCacheCollapse(t *testing.T) {
	c := db.Cache{
		HostID:          1,
		ContainerName:   "adguardhome",
		CurrentDigest:   "sha256:old",
		LatestDigest:    "sha256:old",
		SnoozedDigest:   "sha256:new",
		SnoozedAt:       "2026-08-19T04:05:00Z",
		UpdateAvailable: false,
	}
	got := clearUpdateSnooze(c, "2026-08-19T06:45:00Z")
	if got.SnoozedDigest != "" || got.SnoozedAt != "" {
		t.Fatalf("snooze was not cleared: %+v", got)
	}
	if got.LatestDigest != "sha256:new" || !bool(got.UpdateAvailable) {
		t.Fatalf("known snoozed target should become actionable again: %+v", got)
	}
}

func TestClearUpdateSnoozeKeepsCurrentRemoteIdentityWhenUseful(t *testing.T) {
	c := db.Cache{
		HostID:          1,
		ContainerName:   "demo",
		CurrentDigest:   "sha256:old",
		LatestDigest:    "sha256:newer",
		SnoozedDigest:   "sha256:new",
		SnoozedAt:       "2026-08-19T04:05:00Z",
		UpdateAvailable: false,
	}
	got := clearUpdateSnooze(c, "2026-08-19T06:45:00Z")
	if got.LatestDigest != "sha256:newer" || !bool(got.UpdateAvailable) {
		t.Fatalf("newer known registry identity must win over stale snooze: %+v", got)
	}
}

func TestCacheHasSnoozedUpdateSurvivesPostRollbackLatestCollapse(t *testing.T) {
	c := db.Cache{CurrentDigest: "sha256:old", LatestDigest: "sha256:old", SnoozedDigest: "sha256:new", SnoozedAt: "2026-08-19T04:05:00Z"}
	if !cacheHasSnoozedUpdate(c) {
		t.Fatal("rollback snooze must remain authoritative when latest temporarily collapses to the running image")
	}
}
