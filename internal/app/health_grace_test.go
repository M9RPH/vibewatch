package app

import (
	"context"
	"testing"
	"time"
)

func TestDockerHealthVerificationWindowHasUsefulGrace(t *testing.T) {
	if got := dockerHealthVerificationWindow(nil); got != 12*time.Second {
		t.Fatalf("no-health window = %s, want 12s", got)
	}
	hc := &inspectHealthcheck{Interval: int64(5 * time.Second), Timeout: int64(2 * time.Second)}
	if got := dockerHealthVerificationWindow(hc); got < 45*time.Second {
		t.Fatalf("short healthcheck window = %s, want at least 45s", got)
	}
	slow := &inspectHealthcheck{StartPeriod: int64(30 * time.Second), Interval: int64(20 * time.Second), Timeout: int64(5 * time.Second)}
	if got := dockerHealthVerificationWindow(slow); got < 80*time.Second || got > 2*time.Minute {
		t.Fatalf("derived healthcheck window = %s, expected derived bounded grace", got)
	}
	huge := &inspectHealthcheck{StartPeriod: int64(10 * time.Minute), Interval: int64(time.Minute)}
	if got := dockerHealthVerificationWindow(huge); got != 2*time.Minute {
		t.Fatalf("healthcheck window cap = %s, want 2m", got)
	}
}

func TestContainerRuntimeVerificationRetriesTransientRestartingState(t *testing.T) {
	calls := 0
	inspect := func(context.Context) (inspectContainer, error) {
		calls++
		var cur inspectContainer
		if calls == 1 {
			cur.State.Restarting = true
			cur.State.Status = "restarting"
			return cur, nil
		}
		cur.State.Running = true
		cur.State.Status = "running"
		return cur, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := verifyContainerRuntimeWithInspector(ctx, inspect, 100*time.Millisecond, 3*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("transient restarting state should recover: %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected repeated inspection after restarting state, got %d call(s)", calls)
	}
}

func TestContainerRuntimeVerificationWaitsForStartupDeadline(t *testing.T) {
	calls := 0
	inspect := func(context.Context) (inspectContainer, error) {
		calls++
		var cur inspectContainer
		cur.State.Status = "created"
		return cur, nil
	}
	started := time.Now()
	err := verifyContainerRuntimeWithInspector(context.Background(), inspect, 12*time.Millisecond, 2*time.Millisecond, 2*time.Millisecond)
	if err == nil {
		t.Fatal("permanently non-running container must fail")
	}
	if calls < 2 {
		t.Fatalf("non-running startup state must be retried, got %d call(s)", calls)
	}
	if time.Since(started) < 10*time.Millisecond {
		t.Fatalf("verification failed immediately instead of honoring startup grace: %s", time.Since(started))
	}
}
