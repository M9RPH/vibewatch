package app

import "testing"

func TestQuickSetupProgressTracking(t *testing.T) {
	for _, id := range []string{"qs-123-abc", "repair_host_42"} {
		if !validQuickSetupOperationID(id) {
			t.Fatalf("expected operation id %q to be valid", id)
		}
	}
	for _, id := range []string{"", "bad/id", "bad id", "../escape"} {
		if validQuickSetupOperationID(id) {
			t.Fatalf("expected operation id %q to be invalid", id)
		}
	}
	a := &App{quickSetupOps: map[string]quickSetupProgress{}}
	a.setQuickSetupProgress("qs-123-abc", "remote", "Configuring Docker", "running")
	got, ok := a.quickSetupProgress("qs-123-abc")
	if !ok || got.Stage != "remote" || got.Status != "running" || got.Message != "Configuring Docker" || got.UpdatedAt == "" {
		t.Fatalf("unexpected quick setup progress: %#v, ok=%v", got, ok)
	}
}
