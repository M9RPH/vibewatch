package app

import (
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
