package app

import (
	"encoding/json"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestV094RestorePointDataDetailsExposeProtectedMounts(t *testing.T) {
	manifest := dataArchiveManifest{
		SchemaVersion: 1,
		ScopeType:     "stack",
		ScopeKey:      "immich",
		Entries: []dataArchiveEntry{{
			Key: "volume:postgres_data", Type: "volume", Name: "postgres_data",
			Destinations: []string{"/var/lib/postgresql/data"}, StorageClass: "local",
			FSType: "ext4", SizeBytes: 12345,
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	scopeType, scopeKey, mounts := restorePointDataDetails(db.RestorePoint{DataManifestJSON: string(raw)})
	if scopeType != "stack" || scopeKey != "immich" {
		t.Fatalf("unexpected scope %s/%s", scopeType, scopeKey)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected one mount, got %d", len(mounts))
	}
	if mounts[0].Name != "postgres_data" || mounts[0].SizeBytes != 12345 || mounts[0].StorageClass != "local" {
		t.Fatalf("unexpected mount: %+v", mounts[0])
	}
}

func TestV094RestorePointChainRunID(t *testing.T) {
	cases := map[string]int64{"chain:42": 42, "chain-auto:77": 77, "chain-recreate:9": 9, "manual": 0}
	for input, want := range cases {
		if got := restorePointChainRunID(input); got != want {
			t.Fatalf("%q: got %d want %d", input, got, want)
		}
	}
}
