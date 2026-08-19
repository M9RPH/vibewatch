package app

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateAppliedTargetImageRejectsSkippedUnchangedUpdate(t *testing.T) {
	err := validateAppliedTargetImage("sha256:old", "sha256:new", "sha256:old", 0, 1)
	if err == nil || !strings.Contains(err.Error(), "target image was not applied") {
		t.Fatalf("expected unchanged skipped update to fail, got %v", err)
	}
}

func TestValidateAppliedTargetImageAcceptsExactExpectedTarget(t *testing.T) {
	if err := validateAppliedTargetImage("sha256:old", "sha256:new", "sha256:new", 0, 1); err != nil {
		t.Fatalf("expected already-applied exact target to pass even if worker reports zero updates: %v", err)
	}
}

func TestValidateAppliedTargetImageRejectsWrongChangedImage(t *testing.T) {
	err := validateAppliedTargetImage("sha256:old", "sha256:expected", "sha256:other", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "does not match expected target") {
		t.Fatalf("expected wrong changed image to fail, got %v", err)
	}
}

func TestValidateAppliedTargetImageFallbackRequiresImageChange(t *testing.T) {
	if err := validateAppliedTargetImage("sha256:old", "", "sha256:new", 1, 0); err != nil {
		t.Fatalf("changed image should pass digest-unavailable fallback: %v", err)
	}
	if err := validateAppliedTargetImage("sha256:old", "", "sha256:old", 1, 0); err == nil {
		t.Fatal("unchanged image must fail even if worker claims updated=1")
	}
}

func TestCriticalChainFailureTreatsTargetImageVerificationAsSafetyFailure(t *testing.T) {
	err := fmt.Errorf("member update failed: post-update image verification failed: target image was not applied")
	if !criticalChainFailure(err) {
		t.Fatal("target-image verification failure must stop a chain even when Stop on Failure is disabled")
	}
}

func TestValidateAppliedTargetImageAdGuardMultiArchRegression(t *testing.T) {
	// Support bundle 2026-08-18: the worker correctly updated AdGuard Home
	// to Docker image/config ID e38d..., while its registry manifest digest
	// was aba9.... These are different OCI identities and must not be compared.
	before := "sha256:22817de3a54dbc8506987ca0783ff07606965c15e94aaa617acf051a24b3fd4a"
	expectedConfig := "sha256:e38d0ed0724eb284aa9e196359bd24cae4fff315b4550270cca948986b4b16b8"
	actual := expectedConfig
	if err := validateAppliedTargetImage(before, expectedConfig, actual, 1, 0); err != nil {
		t.Fatalf("correctly applied multi-arch target must pass config-digest verification: %v", err)
	}
}

func TestDeterministicTargetCorrectionEligibleForWrongChangedImage(t *testing.T) {
	if !deterministicTargetCorrectionEligible("sha256:44fe", "sha256:610c") {
		t.Fatal("wrong worker-selected image must be eligible for deterministic exact-target correction")
	}
	if !deterministicTargetCorrectionEligible("sha256:68fe", "sha256:610c") {
		t.Fatal("unchanged pre-update image must remain eligible for deterministic exact-target correction")
	}
	if deterministicTargetCorrectionEligible("sha256:610c", "sha256:610c") {
		t.Fatal("already correct target must not be recreated")
	}
	if deterministicTargetCorrectionEligible("", "sha256:610c") {
		t.Fatal("unknown running image identity must not trigger a blind deterministic recreate")
	}
}
