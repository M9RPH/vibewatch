package db

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestV094DataProtectionProfileAndStorageCacheRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	profile := DataProtectionProfile{HostID: 7, ScopeType: "stack", ScopeKey: "immich", Enabled: Bool(true), MountsJSON: `["volume:postgres_data"]`}
	if err := s.SaveDataProtectionProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	got, err := s.DataProtectionProfile(ctx, 7, "stack", "immich")
	if err != nil {
		t.Fatal(err)
	}
	if !bool(got.Enabled) || got.MountsJSON != profile.MountsJSON || got.UpdatedAt == "" {
		t.Fatalf("unexpected profile: %+v", got)
	}
	storage := HostStorageCache{HostID: 7, HostTotalBytes: 1000, HostFreeBytes: 400, RestoreTotalBytes: 800, RestoreFreeBytes: 300, CheckedAt: "2026-08-14T07:00:00Z"}
	if err := s.SaveHostStorageCache(ctx, storage); err != nil {
		t.Fatal(err)
	}
	stored, err := s.HostStorageCache(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HostFreeBytes != 400 || stored.RestoreFreeBytes != 300 || stored.CheckedAt == "" {
		t.Fatalf("unexpected storage cache: %+v", stored)
	}
}

func TestV094DataProtectionProfileMountsJSONHydratesFromSQLiteJSON(t *testing.T) {
	var got DataProtectionProfile
	if err := json.Unmarshal([]byte(`{"host_id":2,"scope_type":"stack","scope_key":"mealie","enabled":1,"mounts_json":"[\"volume:db\",\"bind:/srv/app\"]","updated_at":"2026-08-14T06:35:25Z"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.MountsJSON != `["volume:db","bind:/srv/app"]` {
		t.Fatalf("mount selection was not hydrated: %q", got.MountsJSON)
	}
}
