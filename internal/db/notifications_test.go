package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNotificationTargetsRespectRolesAndHostAssignments(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 binary not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "t.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	h1, _ := s.CreateHost(ctx, "one", "tcp://one:2375", "t1", true)
	h2, _ := s.CreateHost(ctx, "two", "tcp://two:2375", "t2", true)
	g, _ := s.SaveHostGroup(ctx, HostGroup{Name: "group-two", HostIDs: []int64{h2}})
	adminID, err := s.SaveUser(ctx, User{Username: "ops-admin", PasswordHash: "x", Role: "admin", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := s.SaveUser(ctx, User{Username: "alice", PasswordHash: "x", Role: "user", Enabled: true, HostIDs: []int64{h1}})
	if err != nil {
		t.Fatal(err)
	}
	groupUserID, err := s.SaveUser(ctx, User{Username: "bob", PasswordHash: "x", Role: "user", Enabled: true, GroupIDs: []int64{g}})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []NotificationSettings{
		{UserID: adminID, PushoverAppToken: "admin-app-token-1234567890", PushoverUserKey: "admin-key", NotifyAutoUpdates: true, NotifyManualAvailable: true, NotifyManualUpdates: true},
		{UserID: userID, PushoverAppToken: "alice-app-token-1234567890", PushoverUserKey: "alice-key", NotifyAutoUpdates: true, NotifyManualAvailable: true, NotifyManualUpdates: true},
		{UserID: groupUserID, PushoverAppToken: "bob-app-token-1234567890", PushoverUserKey: "bob-key", NotifyAutoUpdates: true, NotifyManualAvailable: true, NotifyManualUpdates: true},
		{UserID: 0, PushoverAppToken: "owner-app-token-1234567890", PushoverUserKey: "owner-key", NotifyAutoUpdates: true, NotifyManualAvailable: true, NotifyManualUpdates: true},
	} {
		if err := s.SaveNotificationSettings(ctx, x); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := s.NotificationTargets(ctx, h1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, x := range targets {
		seen[x.Username] = true
	}
	if !seen["admin"] || !seen["ops-admin"] || !seen["alice"] || seen["bob"] {
		t.Fatalf("unexpected h1 targets: %#v", seen)
	}

	targets, err = s.NotificationTargets(ctx, h2, "auto")
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]bool{}
	for _, x := range targets {
		seen[x.Username] = true
	}
	if !seen["admin"] || !seen["ops-admin"] || !seen["bob"] || seen["alice"] {
		t.Fatalf("unexpected h2 targets: %#v", seen)
	}
	targets, err = s.NotificationTargets(ctx, h1, "manual_update")
	if err != nil {
		t.Fatal(err)
	}
	seen = map[string]bool{}
	for _, x := range targets {
		seen[x.Username] = true
	}
	if !seen["admin"] || !seen["ops-admin"] || !seen["alice"] || seen["bob"] {
		t.Fatalf("unexpected h1 manual-update targets: %#v", seen)
	}

}
