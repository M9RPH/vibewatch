package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUserHostPermissionsIncludeGroups(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 binary not available in test environment")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "t.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	h1, e := s.CreateHost(ctx, "one", "tcp://one:2375", "t1", true)
	if e != nil {
		t.Fatal(e)
	}
	h2, e := s.CreateHost(ctx, "two", "tcp://two:2375", "t2", true)
	if e != nil {
		t.Fatal(e)
	}
	g, e := s.SaveHostGroup(ctx, HostGroup{Name: "core", HostIDs: []int64{h2}})
	if e != nil {
		t.Fatal(e)
	}
	u, e := s.SaveUser(ctx, User{Username: "demo", PasswordHash: "x", Role: "user", Enabled: true, HostIDs: []int64{h1}, GroupIDs: []int64{g}})
	if e != nil {
		t.Fatal(e)
	}
	ids, e := s.AllowedHostIDs(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 || ids[0] != h1 || ids[1] != h2 {
		t.Fatalf("unexpected permissions %#v", ids)
	}
}
