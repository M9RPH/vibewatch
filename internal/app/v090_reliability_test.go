package app

import "testing"

func TestV090TransactionStateMachineRejectsUnsafeTransitions(t *testing.T) {
	good := [][2]string{{txQueued, txPreflight}, {txPreflight, txSnapshot}, {txSnapshot, txRestorePoint}, {txRestorePoint, txPrepared}, {txPrepared, txUpdating}, {txUpdating, txDockerHealth}, {txDockerHealth, txVerifying}, {txVerifying, txRefreshing}, {txRefreshing, txSuccess}}
	for _, x := range good {
		if !validTransactionTransition(x[0], x[1]) {
			t.Fatalf("expected transition %s -> %s", x[0], x[1])
		}
	}
	bad := [][2]string{{txQueued, txUpdating}, {txPreflight, txSuccess}, {txPrepared, txSuccess}, {txSuccess, txRollback}, {txRolledBack, txUpdating}}
	for _, x := range bad {
		if validTransactionTransition(x[0], x[1]) {
			t.Fatalf("unsafe transition accepted %s -> %s", x[0], x[1])
		}
	}
}

func TestV090PreflightDiagnosticsClassifySourcesAndBlocking(t *testing.T) {
	r := PreflightResult{}
	r.add("registry", preflightGreen, "Manifest", "ok", "")
	r.add("volumes", preflightRed, "Volume", "missing", "v1")
	if len(r.Checks) != 2 {
		t.Fatal("missing checks")
	}
	if r.Checks[0].Source != "registry manifest" {
		t.Fatalf("source=%q", r.Checks[0].Source)
	}
	if !r.Checks[1].Blocking {
		t.Fatal("red check must be blocking")
	}
}

func TestV090CrashRecoveryNamespaceReconciliation(t *testing.T) {
	var parent inspectContainer
	parent.ID = "abcdef1234567890"
	parent.Name = "/gluetun"
	var dep inspectContainer
	dep.HostConfig.NetworkMode = "container:abcdef1234567890"
	if networkNamespaceNeedsRecreate(dep, parent) {
		t.Fatal("dependent already bound to current parent must not be recreated")
	}
	dep.HostConfig.NetworkMode = "container:1111111111111111"
	if !networkNamespaceNeedsRecreate(dep, parent) {
		t.Fatal("stale namespace id must require recreate")
	}
	dep.HostConfig.NetworkMode = "bridge"
	if !networkNamespaceNeedsRecreate(dep, parent) {
		t.Fatal("lost namespace relationship must require recreate during recovery")
	}
}
