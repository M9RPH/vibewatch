package app

import (
	"testing"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
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
