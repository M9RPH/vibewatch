package main

import "testing"

func TestVibewatchEnvPrefersCanonicalAndFallsBackToLegacy(t *testing.T) {
	t.Setenv("VIBEWATCH_TEST_VALUE", "canonical")
	t.Setenv("WTUI_TEST_VALUE", "legacy")
	if got := vibewatchEnv("VIBEWATCH_TEST_VALUE", "WTUI_TEST_VALUE", "default"); got != "canonical" {
		t.Fatalf("canonical value should win, got %q", got)
	}

	t.Setenv("VIBEWATCH_TEST_VALUE", "")
	if got := vibewatchEnv("VIBEWATCH_TEST_VALUE", "WTUI_TEST_VALUE", "default"); got != "legacy" {
		t.Fatalf("legacy fallback should be used, got %q", got)
	}

	t.Setenv("WTUI_TEST_VALUE", "")
	if got := vibewatchEnv("VIBEWATCH_TEST_VALUE", "WTUI_TEST_VALUE", "default"); got != "default" {
		t.Fatalf("default should be used when neither variable is set, got %q", got)
	}
}
