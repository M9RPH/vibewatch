package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/m9rph/vibewatch/internal/registry"
)

// validateAppliedTargetImage is the final invariant between the update engine
// and the health/verification pipeline. A successful HTTP response from the
// worker is not proof that the requested image was actually applied: worker
// failures such as pull/DNS errors may be reported as skipped operations.
//
// expectedImageID MUST be the platform-specific OCI config digest. Docker's
// container inspect .Image field is that config digest. Registry manifest
// digests are a different identifier and must never be compared to it.
func validateAppliedTargetImage(beforeImageID, expectedImageID, actualImageID string, updated, skipped int) error {
	beforeImageID = strings.TrimSpace(beforeImageID)
	expectedImageID = strings.TrimSpace(expectedImageID)
	actualImageID = strings.TrimSpace(actualImageID)

	if actualImageID == "" {
		return fmt.Errorf("post-update image verification failed: running container image id is unavailable (worker updated=%d skipped=%d)", updated, skipped)
	}
	if expectedImageID != "" {
		if digestEqual(actualImageID, expectedImageID) {
			return nil
		}
		if beforeImageID != "" && digestEqual(actualImageID, beforeImageID) {
			return fmt.Errorf("post-update image verification failed: target image was not applied; running container still uses pre-update image %s, expected image id %s (worker updated=%d skipped=%d)", actualImageID, expectedImageID, updated, skipped)
		}
		return fmt.Errorf("post-update image verification failed: running image id %s does not match expected target image id %s (worker updated=%d skipped=%d)", actualImageID, expectedImageID, updated, skipped)
	}

	// Compatibility fallback for registries where a platform config digest
	// cannot be resolved. We can still require an observable image-ID change.
	if beforeImageID != "" {
		if !digestEqual(actualImageID, beforeImageID) {
			return nil
		}
		return fmt.Errorf("post-update image verification failed: running image did not change from %s (worker updated=%d skipped=%d)", beforeImageID, updated, skipped)
	}
	if updated > 0 {
		return nil
	}
	return fmt.Errorf("post-update image verification failed: worker did not report an applied update and no comparable image id is available (updated=%d skipped=%d)", updated, skipped)
}

func deterministicTargetCorrectionEligible(actualImageID, expectedImageID string) bool {
	actualImageID = strings.TrimSpace(actualImageID)
	expectedImageID = strings.TrimSpace(expectedImageID)
	return actualImageID != "" && expectedImageID != "" && !digestEqual(actualImageID, expectedImageID)
}

// resolveExpectedTargetImageID resolves the platform-specific OCI config
// digest for the image reference currently configured on the target container.
// That digest is directly comparable to Docker container inspect .Image.
func (a *App) resolveExpectedTargetImageID(ctx context.Context, hostID int64, container string) (string, error) {
	return a.resolveExpectedTargetImageIDMode(ctx, hostID, container, false)
}

func (a *App) resolveExpectedTargetImageIDFresh(ctx context.Context, hostID int64, container string) (string, error) {
	return a.resolveExpectedTargetImageIDMode(ctx, hostID, container, true)
}

func (a *App) resolveExpectedTargetImageIDMode(ctx context.Context, hostID int64, container string, fresh bool) (string, error) {
	if a.Registry == nil {
		return "", fmt.Errorf("registry client unavailable")
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return "", err
	}
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return "", err
	}
	imageRef := strings.TrimSpace(cur.Config.Image)
	if imageRef == "" {
		return "", fmt.Errorf("container image reference is unavailable")
	}
	localRef := strings.TrimSpace(cur.Image)
	if localRef == "" {
		localRef = imageRef
	}
	platform, err := a.Docker.ImagePlatform(ctx, h.Endpoint, localRef)
	if err != nil {
		return "", fmt.Errorf("resolve target platform: %w", err)
	}
	wanted := registry.Platform{OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant}
	var remote registry.RemoteImageState
	releaseContinuityRead := a.acquireContinuityRead(ctx)
	if fresh {
		remote, err = a.Registry.RemoteStateForPlatformFresh(ctx, imageRef, wanted)
	} else {
		remote, err = a.Registry.RemoteStateForPlatform(ctx, imageRef, wanted)
	}
	releaseContinuityRead()
	if err != nil {
		return "", fmt.Errorf("resolve target image config digest: %w", err)
	}
	id := strings.TrimSpace(remote.ConfigDigest)
	if id == "" {
		return "", fmt.Errorf("target image config digest is empty")
	}
	return id, nil
}

func (a *App) verifyAppliedTargetImage(ctx context.Context, hostID int64, container, beforeImageID, expectedImageID string, updated, skipped int) (string, error) {
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return "", fmt.Errorf("post-update image verification failed: inspect live container: %w", err)
	}
	actual := strings.TrimSpace(cur.Image)
	if err := validateAppliedTargetImage(beforeImageID, expectedImageID, actual, updated, skipped); err != nil {
		return actual, err
	}
	return actual, nil
}

func (a *App) verifyRecoveredTargetImage(ctx context.Context, hostID int64, container, expectedImageID string) (string, error) {
	cur, err := a.inspectOne(ctx, hostID, container)
	if err != nil {
		return "", fmt.Errorf("recovery image verification failed: inspect live container: %w", err)
	}
	actual := strings.TrimSpace(cur.Image)
	expectedImageID = strings.TrimSpace(expectedImageID)
	// Older transactions legitimately lack target_image_id. Preserve their
	// compatibility recovery path instead of comparing Docker's config digest
	// with the older target_digest field, which may contain a manifest digest.
	if expectedImageID == "" {
		return actual, nil
	}
	if !digestEqual(actual, expectedImageID) {
		return actual, fmt.Errorf("recovery image verification failed: running image %s does not match transaction target image id %s", actual, expectedImageID)
	}
	return actual, nil
}
