package app

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestV094EligiblePersistentMountCountUsesWritableRollbackData(t *testing.T) {
	raw := `[
      {"Mounts":[
        {"Type":"volume","Name":"db_data","Source":"/var/lib/docker/volumes/db_data/_data","Destination":"/data","RW":true},
        {"Type":"volume","Name":"db_data","Source":"/var/lib/docker/volumes/db_data/_data","Destination":"/backup","RW":true},
        {"Type":"bind","Source":"/srv/config","Destination":"/config","RW":true},
        {"Type":"bind","Source":"/srv/media","Destination":"/media","RW":false},
        {"Type":"bind","Source":"/var/run/docker.sock","Destination":"/var/run/docker.sock","RW":true},
        {"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true}
      ]}
    ]`
	var inspects []inspectContainer
	if err := json.Unmarshal([]byte(raw), &inspects); err != nil {
		t.Fatal(err)
	}
	if got := eligiblePersistentMountCount(inspects); got != 2 {
		t.Fatalf("eligible persistent mount count = %d, want 2", got)
	}
}

func TestV094UnconfiguredDataProtectionPreflightClassification(t *testing.T) {
	clean := unconfiguredDataProtectionCheck("service", "stateless", 0, false, nil)
	if clean.Status != preflightGreen || clean.Key != "data_protection" {
		t.Fatalf("mount-less service should be green: %#v", clean)
	}

	persistent := unconfiguredDataProtectionCheck("stack", "immich", 2, false, nil)
	if persistent.Status != preflightYellow || persistent.Title != "Persistent data not protected" {
		t.Fatalf("unprotected persistent stack data should warn: %#v", persistent)
	}

	unavailable := unconfiguredDataProtectionCheck("stack", "immich", 0, false, errors.New("inspect unavailable"))
	if unavailable.Status != preflightYellow {
		t.Fatalf("unassessed unconfigured protection should remain advisory: %#v", unavailable)
	}
}
