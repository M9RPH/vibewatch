package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUINotificationReadStateIsPerUserAndUpdatesFingerprint(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 binary not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "notifications.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUINotificationReads(ctx, 7, []UINotificationRead{{NotificationID: "update-1-plex", Fingerprint: "digest-a"}}); err != nil {
		t.Fatal(err)
	}
	reads, err := s.UINotificationReads(ctx, 7)
	if err != nil || len(reads) != 1 || reads[0].NotificationID != "update-1-plex" || reads[0].Fingerprint != "digest-a" || reads[0].ReadAt == "" {
		t.Fatalf("unexpected read state: %#v err=%v", reads, err)
	}
	other, err := s.UINotificationReads(ctx, 8)
	if err != nil || len(other) != 0 {
		t.Fatalf("read state leaked between users: %#v err=%v", other, err)
	}
	if err := s.SaveUINotificationReads(ctx, 7, []UINotificationRead{{NotificationID: "update-1-plex", Fingerprint: "digest-b"}}); err != nil {
		t.Fatal(err)
	}
	reads, err = s.UINotificationReads(ctx, 7)
	if err != nil || len(reads) != 1 || reads[0].Fingerprint != "digest-b" {
		t.Fatalf("fingerprint was not updated: %#v err=%v", reads, err)
	}
}
