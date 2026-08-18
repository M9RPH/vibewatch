package app

import (
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestRestorePointStorageMetrics(t *testing.T) {
	rp := db.RestorePoint{DataBytes: 300}
	archive, total, estimated := restorePointStorageMetrics(rp, 200, 500)
	if archive != 500 {
		t.Fatalf("archive bytes = %d, want 500", archive)
	}
	if total != 1000 {
		t.Fatalf("footprint bytes = %d, want 1000", total)
	}
	if !estimated {
		t.Fatal("expected footprint to be marked estimated when a Docker image size is included")
	}

	archive, total, estimated = restorePointStorageMetrics(db.RestorePoint{DataBytes: -1}, -1, 0)
	if archive != 0 || total != 0 || estimated {
		t.Fatalf("negative values should clamp to zero: archive=%d total=%d estimated=%v", archive, total, estimated)
	}
}
