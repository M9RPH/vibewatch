package db

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEmptyDiagnosticLogsAreArrays(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed in test environment")
	}
	s := New(filepath.Join(t.TempDir(), "vibewatch.db"))
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, err := s.DockerEvents(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if events == nil {
		t.Fatal("DockerEvents returned nil; API would serialize null instead of []")
	}
	deliveries, err := s.NotificationDeliveries(context.Background(), nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries == nil {
		t.Fatal("NotificationDeliveries returned nil; API would serialize null instead of []")
	}
}
