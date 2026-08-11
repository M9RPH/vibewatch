package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNotificationDeliveryLogRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 binary not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "delivery.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNotificationDelivery(ctx, NotificationDelivery{
		UserID: 3, Username: "alice", HostID: 7, ContainerName: "netbird",
		Event: "manual_update", Title: "Manual update completed · netbird", Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := s.NotificationDeliveries(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Username != "alice" || all[0].Event != "manual_update" || all[0].Status != "success" {
		t.Fatalf("unexpected delivery log: %#v", all)
	}
	uid := int64(3)
	mine, err := s.NotificationDeliveries(ctx, &uid, 10)
	if err != nil || len(mine) != 1 {
		t.Fatalf("filtered delivery log failed: %#v err=%v", mine, err)
	}
	other := int64(4)
	none, err := s.NotificationDeliveries(ctx, &other, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("user filter leaked delivery records: %#v err=%v", none, err)
	}
}
