package app

import (
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

func digestEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// applyTrackedUpdateState preserves the first time a concrete remote image was
// detected and binds snooze state to exactly that remote digest. When the
// registry moves to another digest, the old snooze expires automatically.
func applyTrackedUpdateState(old, next db.Cache, rawAvailable bool, now string) db.Cache {
	if strings.TrimSpace(now) == "" {
		now = time.Now().UTC().Format(time.RFC3339)
	}
	latest := strings.TrimSpace(next.LatestDigest)
	current := strings.TrimSpace(next.CurrentDigest)
	oldLatest := strings.TrimSpace(old.LatestDigest)

	if !rawAvailable {
		next.UpdateAvailable = false
		next.FirstDetectedAt = ""
		next.SnoozedDigest = ""
		next.SnoozedAt = ""
		return next
	}

	if strings.TrimSpace(old.FirstDetectedAt) != "" && latest != "" && oldLatest != "" && strings.EqualFold(latest, oldLatest) {
		next.FirstDetectedAt = old.FirstDetectedAt
	} else if strings.TrimSpace(old.FirstDetectedAt) != "" && latest == "" && oldLatest == "" && bool(old.UpdateAvailable) {
		// Compatibility fallback for an update engine response without a digest.
		next.FirstDetectedAt = old.FirstDetectedAt
	} else {
		next.FirstDetectedAt = now
	}

	if latest != "" && digestEqual(old.SnoozedDigest, latest) && !digestEqual(current, latest) {
		next.SnoozedDigest = old.SnoozedDigest
		next.SnoozedAt = old.SnoozedAt
		next.UpdateAvailable = false
		return next
	}

	// A different latest digest is a new update and automatically clears the
	// previous snooze.
	next.SnoozedDigest = ""
	next.SnoozedAt = ""
	next.UpdateAvailable = db.Bool(rawAvailable)
	return next
}

func cacheHasSnoozedUpdate(c db.Cache) bool {
	return strings.TrimSpace(c.SnoozedDigest) != "" && digestEqual(c.SnoozedDigest, c.LatestDigest) && !digestEqual(c.CurrentDigest, c.LatestDigest)
}
