package app

import "testing"

func TestV093AutomaticPreflightSafety(t *testing.T) {
	informational := []PreflightCheck{{Key: "docker_health", Status: preflightInfo, Title: "No Docker healthcheck"}}
	if blocked, reason := automaticPreflightBlocked(informational, false); blocked {
		t.Fatalf("informational check must not block automatic update: %s", reason)
	}

	clean := []PreflightCheck{{Key: "config", Status: preflightGreen, Title: "Config OK"}}
	if blocked, reason := automaticPreflightBlocked(clean, false); blocked {
		t.Fatalf("clean preflight unexpectedly blocked: %s", reason)
	}

	advisory := []PreflightCheck{{Key: "bind_mounts", Status: preflightYellow, Title: "Bind mounts recorded"}}
	if blocked, _ := automaticPreflightBlocked(advisory, false); !blocked {
		t.Fatal("advisory warning must block Auto Update by default")
	}
	if blocked, reason := automaticPreflightBlocked(advisory, true); blocked {
		t.Fatalf("explicitly allowed advisory warning should pass: %s", reason)
	}

	major := []PreflightCheck{{Key: "major_version", Status: preflightYellow, Title: "Major version update detected"}}
	if blocked, _ := automaticPreflightBlocked(major, true); !blocked {
		t.Fatal("major-version warning must still require manual approval")
	}

	unknownMajor := []PreflightCheck{{Key: "major_version", Status: preflightYellow, Title: "Major version could not be determined"}}
	if blocked, reason := automaticPreflightBlocked(unknownMajor, true); blocked {
		t.Fatalf("unknown major metadata is advisory when warnings are explicitly allowed: %s", reason)
	}

	red := []PreflightCheck{{Key: "restore_configuration", Status: preflightRed, Title: "Restore configuration invalid"}}
	if blocked, _ := automaticPreflightBlocked(red, true); !blocked {
		t.Fatal("red blocker must never be overridden")
	}
}

func TestV093SkippedTransactionIsTerminal(t *testing.T) {
	for _, state := range []string{txPreflight, txSnapshot, txRestorePoint} {
		if !validTransactionTransition(state, txSkipped) {
			t.Fatalf("%s must be allowed to end as skipped for automatic safety holds", state)
		}
	}
	if !transactionTerminalState(txSkipped) {
		t.Fatal("skipped transaction must be terminal")
	}
}
