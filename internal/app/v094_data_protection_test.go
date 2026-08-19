package app

import (
	"fmt"
	"testing"

	"github.com/m9rph/vibewatch/internal/db"
)

func TestV094NetworkStorageClassification(t *testing.T) {
	for _, fs := range []string{"nfs", "nfs4", "cifs", "smb2", "fuse.sshfs", "fuse.rclone", "ceph", "glusterfs", "davfs2"} {
		if got := storageClassForFSProbe(fs, ""); got != "external" {
			t.Fatalf("expected %q to be external, got %q", fs, got)
		}
	}
	for _, fs := range []string{"ext4", "xfs", "btrfs", "overlayfs"} {
		if got := storageClassForFSProbe(fs, ""); got != "local" {
			t.Fatalf("expected %q to stay local, got %q", fs, got)
		}
	}
	if got := storageClassForFSProbe("autofs", ""); got != "unknown" {
		t.Fatalf("expected autofs to stay unknown, got %q", got)
	}
}

func TestV094NetworkStorageClassificationByFilesystemMagic(t *testing.T) {
	cases := map[string]string{
		"fe534d42": "smb2",
		"ff534d42": "cifs",
		"6969":     "nfs",
		"65735546": "fuse",
	}
	for magic, wantType := range cases {
		if got := storageClassForFSProbe("UNKNOWN", magic); got != "external" {
			t.Fatalf("expected magic %s to be external, got %q", magic, got)
		}
		if got := canonicalFSType("UNKNOWN", magic); got != wantType {
			t.Fatalf("expected magic %s to normalize to %q, got %q", magic, wantType, got)
		}
	}
}

func TestV094UnknownFilesystemDoesNotBecomeLocal(t *testing.T) {
	if got := storageClassForFSProbe("mysteryfs", "deadbeef"); got != "unknown" {
		t.Fatalf("expected unknown filesystem to stay unknown, got %q", got)
	}
}

func TestV094SystemBindMountsAreNotDataProtectionTargets(t *testing.T) {
	for _, source := range []string{"/", "/proc", "/proc/1", "/sys", "/dev", "/var/run/docker.sock", "/run/docker.sock"} {
		if !unsupportedDataBind(source) {
			t.Fatalf("expected %q to be rejected", source)
		}
	}
	for _, source := range []string{"/home/docker/plex/config", "/mnt/nas/media", "/srv/postgres"} {
		if unsupportedDataBind(source) {
			t.Fatalf("expected %q to be selectable", source)
		}
	}
}

func TestV094RestorePointProtectionLevelIncludesData(t *testing.T) {
	rp := db.RestorePoint{WritableLayer: db.Bool(true), ConfigProtected: db.Bool(true), VolumeDataProtected: db.Bool(true)}
	if got := restorePointProtectionLevel(rp); got != "full_application" {
		t.Fatalf("expected full_application, got %q", got)
	}
}

func TestV094SelectedMountKeysAreStable(t *testing.T) {
	got := selectedMountKeys(`["volume:db","bind:/srv/app","volume:db",""]`)
	if len(got) != 2 || !got["volume:db"] || !got["bind:/srv/app"] {
		t.Fatalf("unexpected selected keys: %#v", got)
	}
	if dataMountKey("volume", "postgres_data", "/ignored") != "volume:postgres_data" {
		t.Fatal("named volume key changed")
	}
	if dataMountKey("bind", "", "/srv/app") != "bind:/srv/app" {
		t.Fatal("bind key changed")
	}
}

func TestV094LocalDriverBindVolumeIsNotBlindlyMarkedLocal(t *testing.T) {
	options := map[string]string{"type": "none", "o": "bind", "device": "/mnt/nas/app"}
	if got := volumeMetadataStorageClass("local", options); got != "unknown" {
		t.Fatalf("expected bind-backed local volume to require host-source probe, got %q", got)
	}
	if got := volumeBindSource(options); got != "/mnt/nas/app" {
		t.Fatalf("unexpected bind source %q", got)
	}
}

func TestRetainedManifestInventoryFallbackAllowsLostComposeIdentity(t *testing.T) {
	if !retainedManifestInventoryFallbackAllowed("stack", fmt.Errorf("container is not part of a Compose stack")) {
		t.Fatal("expected retained restore manifest to remain usable after broken target loses Compose labels")
	}
	if !retainedManifestInventoryFallbackAllowed("stack", fmt.Errorf("container sabnzbdvpn not found")) {
		t.Fatal("expected retained restore manifest to remain usable when failed recreate temporarily removes target")
	}
	if retainedManifestInventoryFallbackAllowed("service", fmt.Errorf("container is not part of a Compose stack")) {
		t.Fatal("service scope should not use stack-identity fallback")
	}
	if retainedManifestInventoryFallbackAllowed("stack", fmt.Errorf("docker daemon unavailable")) {
		t.Fatal("unrelated Docker errors must still fail closed")
	}
}
