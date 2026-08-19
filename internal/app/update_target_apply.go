package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
	"github.com/m9rph/vibewatch/internal/watchtower"
)

// localTargetImageAvailable proves that the exact platform-specific OCI config
// digest expected by the transaction is already present in the target Docker
// Engine. This is intentionally based on the immutable image ID, not a mutable
// tag or manifest-list digest.
func (a *App) localTargetImageAvailable(ctx context.Context, endpoint, expectedImageID string) bool {
	expectedImageID = strings.TrimSpace(expectedImageID)
	if expectedImageID == "" {
		return false
	}
	p, err := a.Docker.ImagePlatform(ctx, endpoint, expectedImageID)
	if err != nil {
		return false
	}
	return digestEqual(p.ImageID, expectedImageID)
}

func mutableApplicationImageRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	lower := strings.ToLower(ref)
	return !strings.HasPrefix(lower, "sha256:") && !strings.Contains(lower, "@sha256:")
}

func vibewatchRestoreRuntime(c inspectContainer) bool {
	labels := c.Config.Labels
	if len(labels) == 0 {
		return false
	}
	return strings.TrimSpace(labels["io.vibewatch.restore-point"]) != "" ||
		strings.TrimSpace(labels["io.vibewatch.restore-original-image-id"]) != ""
}

// alignMutableImageRefToTarget makes the human-readable application ref (for
// example repo/app:latest) point at the transaction's already-proven immutable
// config digest. This is important after a rollback: the running container may
// intentionally use a Vibewatch restore commit while the mutable registry tag
// must continue to represent the remote application target, never a restore
// image. Docker retagging does not alter the running container.
func (a *App) alignMutableImageRefToTarget(ctx context.Context, h db.Host, prepared preflightPrepared, expectedImageID string) (bool, string, error) {
	ref := strings.TrimSpace(prepared.TargetInspect.Config.Image)
	expectedImageID = strings.TrimSpace(expectedImageID)
	if !mutableApplicationImageRef(ref) || expectedImageID == "" {
		return false, "", nil
	}
	if !a.localTargetImageAvailable(ctx, h.Endpoint, expectedImageID) {
		return false, "", fmt.Errorf("expected target image %s is not available locally for mutable ref alignment", expectedImageID)
	}
	previous := ""
	if current, err := a.Docker.ImagePlatform(ctx, h.Endpoint, ref); err == nil {
		previous = strings.TrimSpace(current.ImageID)
		if digestEqual(previous, expectedImageID) {
			return false, previous, nil
		}
	}
	if _, err := a.Docker.Run(ctx, h.Endpoint, "image", "tag", expectedImageID, ref); err != nil {
		return false, previous, fmt.Errorf("align mutable image ref %s to target %s: %w", ref, expectedImageID, err)
	}
	resolved, err := a.Docker.ImagePlatform(ctx, h.Endpoint, ref)
	if err != nil {
		return false, previous, fmt.Errorf("verify mutable image ref %s after target alignment: %w", ref, err)
	}
	if !digestEqual(resolved.ImageID, expectedImageID) {
		return false, previous, fmt.Errorf("mutable image ref %s resolved to %s after alignment, expected %s", ref, resolved.ImageID, expectedImageID)
	}
	return true, previous, nil
}

// ensureExpectedTargetImageLocal makes the exact transaction target available
// on the Docker host. It first reuses an already downloaded immutable image and
// otherwise performs a bounded direct Docker pull of the configured image ref.
// The pulled tag is accepted only when Docker resolves it to expectedImageID.
func (a *App) ensureExpectedTargetImageLocal(ctx context.Context, h db.Host, prepared preflightPrepared, expectedImageID string) error {
	if a.localTargetImageAvailable(ctx, h.Endpoint, expectedImageID) {
		return nil
	}
	imageRef := strings.TrimSpace(prepared.TargetInspect.Config.Image)
	if imageRef == "" {
		return fmt.Errorf("configured image reference is unavailable")
	}
	var lastErr error
	for _, delay := range []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second} {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if err := a.Docker.PullRemoteImage(ctx, h.Endpoint, imageRef); err != nil {
			lastErr = err
			continue
		}
		// The transaction target is an immutable config digest. Prove that exact
		// image exists after the pull instead of trusting the mutable tag identity.
		// This also avoids stale :latest metadata if another actor pulled the tag.
		if a.localTargetImageAvailable(ctx, h.Endpoint, expectedImageID) {
			return nil
		}
		p, inspectErr := a.Docker.ImagePlatform(ctx, h.Endpoint, imageRef)
		if inspectErr != nil {
			lastErr = inspectErr
			continue
		}
		lastErr = fmt.Errorf("pulled image %s resolved to %s, expected transaction target %s", imageRef, p.ImageID, expectedImageID)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("target image pull did not complete")
	}
	return fmt.Errorf("make expected target image %s available locally: %w", expectedImageID, lastErr)
}

type imageRuntimeDefaults struct {
	ID       string   `json:"Id"`
	Parent   string   `json:"Parent"`
	RepoTags []string `json:"RepoTags"`
	RootFS   struct {
		Layers []string `json:"Layers"`
	} `json:"RootFS"`
	Config struct {
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
		User       string            `json:"User"`
		WorkingDir string            `json:"WorkingDir"`
		StopSignal string            `json:"StopSignal"`
	} `json:"Config"`
}

type runtimeOverrideSummary struct {
	Environment int
	Labels      int
	Command     bool
	Entrypoint  bool
	User        bool
	WorkingDir  bool
	StopSignal  bool
}

func imageRuntimeDefaultsFromRaw(raw []byte) (imageRuntimeDefaults, error) {
	var rows []imageRuntimeDefaults
	if err := json.Unmarshal(raw, &rows); err != nil {
		return imageRuntimeDefaults{}, fmt.Errorf("decode source image runtime defaults: %w", err)
	}
	if len(rows) != 1 {
		return imageRuntimeDefaults{}, fmt.Errorf("expected one source image inspect result, got %d", len(rows))
	}
	return rows[0], nil
}

func (a *App) inspectImageRuntimeDefaults(ctx context.Context, endpoint, imageID string) (imageRuntimeDefaults, error) {
	raw, err := a.Docker.InspectImagesRaw(ctx, endpoint, imageID)
	if err != nil {
		return imageRuntimeDefaults{}, err
	}
	return imageRuntimeDefaultsFromRaw(raw)
}

func imageLayerPrefixOrEqual(parent, child []string) bool {
	if len(parent) == 0 || len(parent) > len(child) {
		return false
	}
	for i := range parent {
		if parent[i] != child[i] {
			return false
		}
	}
	return true
}

func imageLayerPrefix(parent, child []string) bool {
	return len(parent) < len(child) && imageLayerPrefixOrEqual(parent, child)
}

func (a *App) localImageRuntimeDefaults(ctx context.Context, endpoint string) ([]imageRuntimeDefaults, error) {
	out, err := a.Docker.Run(ctx, endpoint, "image", "ls", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ids := make([]string, 0)
	for _, id := range strings.Fields(out) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows := make([]imageRuntimeDefaults, 0, len(ids))
	const batchSize = 64
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		raw, inspectErr := a.Docker.InspectImagesRaw(ctx, endpoint, ids[start:end]...)
		if inspectErr != nil {
			return nil, inspectErr
		}
		var batch []imageRuntimeDefaults
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("decode local image inventory for restore lineage: %w", err)
		}
		rows = append(rows, batch...)
	}
	return rows, nil
}

// inferLegacyRestoreParent recovers the immutable parent of an old Vibewatch
// docker-commit image whose restore-point DB metadata has already aged out.
// Modern Docker no longer guarantees ImageInspect.Parent, so Parent is accepted
// only after validating its RootFS as a strict prefix. If Parent is absent, the
// complete local image inventory is searched and exactly one deepest layer-prefix
// match is required. Ambiguous ancestry still fails closed.
func (a *App) inferLegacyRestoreParent(ctx context.Context, endpoint string, restore imageRuntimeDefaults) (imageRuntimeDefaults, string, error) {
	if len(restore.RootFS.Layers) < 2 {
		return imageRuntimeDefaults{}, "", fmt.Errorf("restore image %s has insufficient layer metadata for safe ancestry recovery", restore.ID)
	}
	if parentID := strings.TrimSpace(restore.Parent); parentID != "" && parentID != "<missing>" && parentID != strings.TrimSpace(restore.ID) {
		if parent, err := a.inspectImageRuntimeDefaults(ctx, endpoint, parentID); err == nil && imageLayerPrefixOrEqual(parent.RootFS.Layers, restore.RootFS.Layers) {
			return parent, strings.TrimSpace(parent.ID), nil
		}
	}
	images, err := a.localImageRuntimeDefaults(ctx, endpoint)
	if err != nil {
		return imageRuntimeDefaults{}, "", fmt.Errorf("inspect local image ancestry: %w", err)
	}
	maxLayers := -1
	candidates := make([]imageRuntimeDefaults, 0, 2)
	for _, candidate := range images {
		if strings.TrimSpace(candidate.ID) == "" || digestEqual(candidate.ID, restore.ID) {
			continue
		}
		if !imageLayerPrefix(candidate.RootFS.Layers, restore.RootFS.Layers) {
			continue
		}
		depth := len(candidate.RootFS.Layers)
		if depth > maxLayers {
			maxLayers = depth
			candidates = []imageRuntimeDefaults{candidate}
		} else if depth == maxLayers {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], strings.TrimSpace(candidates[0].ID), nil
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, strings.TrimSpace(candidate.ID))
		}
		sort.Strings(ids)
		return imageRuntimeDefaults{}, "", fmt.Errorf("restore image %s has ambiguous local ancestry at layer depth %d: %s", restore.ID, maxLayers, strings.Join(ids, ", "))
	}
	return imageRuntimeDefaults{}, "", fmt.Errorf("restore image %s has no unique local image ancestor", restore.ID)
}

// deterministicSourceDefaults returns the image defaults that existed BEFORE
// the container's user/Compose overrides were materialized. A restored
// Vibewatch container runs from a committed restore image whose Config already
// contains every effective environment value and command; using that commit as
// the baseline would incorrectly classify real user VPN/app settings as image
// defaults and drop them on the next update. The restore-point label lets us
// walk back to the original pre-update base image instead.
func (a *App) deterministicSourceDefaults(ctx context.Context, h db.Host, target inspectContainer) (imageRuntimeDefaults, string, error) {
	sourceID := strings.TrimSpace(target.Image)
	if sourceID == "" {
		return imageRuntimeDefaults{}, "", fmt.Errorf("source image id is unavailable")
	}
	containerName := strings.TrimPrefix(strings.TrimSpace(target.Name), "/")
	points, pointsErr := a.Store.RestorePoints(ctx, 2000, h.ID, containerName)
	if pointsErr != nil {
		return imageRuntimeDefaults{}, "", fmt.Errorf("load restore lineage for deterministic runtime: %w", pointsErr)
	}
	bySnapshot := map[string]db.RestorePoint{}
	for _, rp := range points {
		if key := strings.TrimSpace(rp.SnapshotID); key != "" {
			bySnapshot[key] = rp
		}
	}
	seen := map[string]bool{}
	for depth := 0; depth < 12; depth++ {
		if seen[sourceID] {
			return imageRuntimeDefaults{}, "", fmt.Errorf("restore image lineage contains a cycle at %s", sourceID)
		}
		seen[sourceID] = true
		defaults, err := a.inspectImageRuntimeDefaults(ctx, h.Endpoint, sourceID)
		if err != nil {
			return imageRuntimeDefaults{}, "", fmt.Errorf("inspect source image defaults %s: %w", sourceID, err)
		}
		snapshotID := strings.TrimSpace(defaults.Config.Labels["io.vibewatch.restore-point"])
		if snapshotID == "" {
			return defaults, sourceID, nil
		}
		// New restore images embed their immediate immutable source image so the
		// lineage survives DB/config-snapshot retention. Prefer that provenance.
		if embeddedID := strings.TrimSpace(defaults.Config.Labels["io.vibewatch.restore-original-image-id"]); embeddedID != "" {
			if !a.Docker.ImageExists(ctx, h.Endpoint, embeddedID) {
				return imageRuntimeDefaults{}, "", fmt.Errorf("restored runtime %s embeds original image %s, but that image is no longer local; refusing to guess user overrides", snapshotID, embeddedID)
			}
			sourceID = embeddedID
			continue
		}

		// Existing restore points created before embedded provenance use the DB
		// lineage while it is still retained.
		if rp, exists := bySnapshot[snapshotID]; exists && strings.TrimSpace(rp.OriginalImageID) != "" {
			originalID := strings.TrimSpace(rp.OriginalImageID)
			if !a.Docker.ImageExists(ctx, h.Endpoint, originalID) {
				return imageRuntimeDefaults{}, "", fmt.Errorf("restored runtime %s references original image %s, but that image is no longer local; refusing to guess user overrides", snapshotID, originalID)
			}
			sourceID = originalID
			continue
		}

		// Legacy restore commits can outlive both their config snapshot and DB
		// restore-point row. Recover only from Docker's immutable layer ancestry,
		// requiring an unambiguous deepest local ancestor. This is evidence, not a
		// heuristic based on environment names or mutable tags.
		restoreImageID := sourceID
		_, legacyID, legacyErr := a.inferLegacyRestoreParent(ctx, h.Endpoint, defaults)
		if legacyErr != nil {
			return imageRuntimeDefaults{}, "", fmt.Errorf("restored runtime image %s references snapshot %s whose original-image metadata is unavailable, and Docker ancestry recovery failed: %w", sourceID, snapshotID, legacyErr)
		}
		if a.Logger != nil {
			a.Logger.Info("legacy restore image lineage recovered from Docker ancestry", "host_id", h.ID, "container", containerName, "restore_image", restoreImageID, "snapshot", snapshotID, "original_image", legacyID)
		}
		sourceID = legacyID
	}
	return imageRuntimeDefaults{}, "", fmt.Errorf("restore image lineage exceeds safe traversal depth")
}

func envRuntimeOverrides(containerEnv, imageEnv []string) []string {
	base := map[string]string{}
	for _, item := range imageEnv {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		base[key] = value
	}
	out := make([]string, 0, len(containerEnv))
	for _, item := range containerEnv {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			out = append(out, item)
			continue
		}
		if inherited, exists := base[key]; !exists || inherited != value {
			out = append(out, item)
		}
	}
	return out
}

func restoreProvenanceLabel(key string) bool {
	switch strings.TrimSpace(key) {
	case "io.vibewatch.restore-point", "io.vibewatch.restore-original-image-id", "io.vibewatch.restore-original-image-ref", "io.vibewatch.restore-store":
		return true
	default:
		return false
	}
}

func labelRuntimeOverrides(containerLabels, imageLabels map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range containerLabels {
		// Restore provenance describes the synthetic rollback image, not the
		// application runtime contract. Carrying it onto a successful forward
		// target would make the freshly updated container look like another
		// restore runtime and poison future lineage/update decisions.
		if restoreProvenanceLabel(key) {
			continue
		}
		if inherited, exists := imageLabels[key]; !exists || inherited != value {
			out[key] = value
		}
	}
	return out
}

// targetRuntimeOverrides converts a Docker container's fully materialized
// Config back into the user/runtime overrides that should be carried to a new
// image. Docker inspect merges image defaults (Env, Cmd, Entrypoint, labels,
// user/workdir/stop-signal) into the container config, so replaying that config
// verbatim pins defaults from the OLD image onto the NEW image. That is unsafe
// for deterministic updates when an upstream image changes its startup command
// or default environment. HostConfig, mounts, ports, networks, capabilities,
// sysctls and Compose labels remain preserved by createArgsFromInspect.
func dockerCreateNormalizedProcess(entrypoint, cmd []string) ([]string, []string) {
	normalizedEntrypoint := append([]string(nil), entrypoint...)
	normalizedCmd := append([]string(nil), cmd...)
	if len(normalizedEntrypoint) > 1 {
		normalizedCmd = append(append([]string(nil), normalizedEntrypoint[1:]...), normalizedCmd...)
		normalizedEntrypoint = normalizedEntrypoint[:1]
	}
	return normalizedEntrypoint, normalizedCmd
}

func hasStringPrefix(values, prefix []string) bool {
	if len(prefix) > len(values) {
		return false
	}
	return slices.Equal(values[:len(prefix)], prefix)
}

// processRuntimeOverrides derives command/entrypoint overrides while accounting
// for Vibewatch's own docker-create round-trip. Docker CLI exposes --entrypoint
// as a single executable string, so createArgsFromInspect preserves a multi-part
// image Entrypoint by keeping its first element as --entrypoint and moving the
// remaining elements in front of Cmd. That representation is execution-
// equivalent but must NOT later be mistaken for a user override during a
// deterministic forward update.
func processRuntimeOverrides(containerEntrypoint, containerCmd, sourceEntrypoint, sourceCmd []string) (entrypoint, cmd []string, entrypointOverride, commandOverride bool) {
	// Native Docker representation: no user process override.
	if slices.Equal(containerEntrypoint, sourceEntrypoint) && slices.Equal(containerCmd, sourceCmd) {
		return nil, nil, false, false
	}

	// Vibewatch rollback/recreate representation: a multi-element image
	// Entrypoint becomes [first] + Cmd=[remaining entrypoint..., original Cmd].
	// Treat that representation as inherited image defaults too.
	normalizedSourceEntrypoint, normalizedSourceCmd := dockerCreateNormalizedProcess(sourceEntrypoint, sourceCmd)
	if slices.Equal(containerEntrypoint, normalizedSourceEntrypoint) && slices.Equal(containerCmd, normalizedSourceCmd) {
		return nil, nil, false, false
	}

	// If only Cmd changed while Entrypoint is still the source image's native
	// representation, preserve only the command override so the target image is
	// still free to supply its own Entrypoint.
	if slices.Equal(containerEntrypoint, sourceEntrypoint) {
		return nil, append([]string(nil), containerCmd...), false, true
	}

	// The same command-only override may have passed through a Vibewatch
	// rollback. Strip the source Entrypoint tail that createArgsFromInspect had
	// to prepend to Cmd before carrying the real command override forward.
	if len(sourceEntrypoint) > 1 && slices.Equal(containerEntrypoint, sourceEntrypoint[:1]) && hasStringPrefix(containerCmd, sourceEntrypoint[1:]) {
		recoveredCmd := append([]string(nil), containerCmd[len(sourceEntrypoint)-1:]...)
		if slices.Equal(recoveredCmd, sourceCmd) {
			return nil, nil, false, false
		}
		return nil, recoveredCmd, false, true
	}

	// Entrypoint truly differs from the source image. Preserve the effective
	// container process pair exactly. Docker documents that --entrypoint clears
	// the image CMD, so any current Cmd must be replayed alongside an explicit
	// entrypoint override even when it happens to equal the old image Cmd.
	entrypoint = append([]string(nil), containerEntrypoint...)
	entrypointOverride = true
	cmd = append([]string(nil), containerCmd...)
	commandOverride = len(cmd) > 0
	return entrypoint, cmd, entrypointOverride, commandOverride
}

func targetRuntimeOverrides(container inspectContainer, source imageRuntimeDefaults) (inspectContainer, runtimeOverrideSummary) {
	out := container
	out.Config.Env = envRuntimeOverrides(container.Config.Env, source.Config.Env)
	out.Config.Labels = labelRuntimeOverrides(container.Config.Labels, source.Config.Labels)
	out.Config.Entrypoint, out.Config.Cmd, _, _ = processRuntimeOverrides(container.Config.Entrypoint, container.Config.Cmd, source.Config.Entrypoint, source.Config.Cmd)
	if container.Config.User == source.Config.User {
		out.Config.User = ""
	}
	if container.Config.WorkingDir == source.Config.WorkingDir {
		out.Config.WorkingDir = ""
	}
	if container.Config.StopSignal == source.Config.StopSignal {
		out.Config.StopSignal = ""
	}
	return out, runtimeOverrideSummary{
		Environment: len(out.Config.Env),
		Labels:      len(out.Config.Labels),
		Command:     len(out.Config.Cmd) > 0,
		Entrypoint:  len(out.Config.Entrypoint) > 0,
		User:        strings.TrimSpace(out.Config.User) != "",
		WorkingDir:  strings.TrimSpace(out.Config.WorkingDir) != "",
		StopSignal:  strings.TrimSpace(out.Config.StopSignal) != "",
	}
}

func (a *App) preparedRuntimeOverrides(ctx context.Context, h db.Host, target inspectContainer) (inspectContainer, runtimeOverrideSummary, string, error) {
	defaults, sourceID, err := a.deterministicSourceDefaults(ctx, h, target)
	if err != nil {
		return inspectContainer{}, runtimeOverrideSummary{}, "", err
	}
	prepared, summary := targetRuntimeOverrides(target, defaults)
	return prepared, summary, sourceID, nil
}

func preparedRuntimeFidelityMismatches(before, expectedOverrides, after inspectContainer) []string {
	mismatches := criticalRuntimeMismatches(before, after)
	mismatches = append(mismatches, runtimeOverrideFidelityMismatches(expectedOverrides, after)...)
	sort.Strings(mismatches)
	return slices.Compact(mismatches)
}

func fidelityMounts(c inspectContainer) []string {
	out := []string{}
	for _, m := range c.Mounts {
		if m.Type != "bind" && m.Type != "volume" {
			continue
		}
		source := m.Source
		if m.Type == "volume" && m.Name != "" {
			source = m.Name
		}
		// Docker may normalize an explicitly written :rw to an empty Mode after
		// recreation even though RW/readonly semantics are identical. Compare the
		// effective mount contract rather than CLI spelling.
		out = append(out, fmt.Sprintf("%s:%s:%s:rw=%t:prop=%s", m.Type, source, m.Destination, m.RW, m.Propagation))
	}
	sort.Strings(out)
	return out
}

func normalizedCapabilities(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.ToUpper(strings.TrimSpace(x))
		x = strings.TrimPrefix(x, "CAP_")
		if x != "" {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func runtimeEnvMap(items []string) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// runtimeOverrideFidelityMismatches verifies the runtime data Vibewatch itself
// promised to carry across an image boundary. It intentionally checks only the
// derived overrides, not the target image's new defaults. Environment values
// are compared in memory but never included in errors/logs; only variable names
// are returned so support bundles remain secret-safe.
func runtimeOverrideFidelityMismatches(expected, actual inspectContainer) []string {
	fields := []string{}
	expectedEnv := runtimeEnvMap(expected.Config.Env)
	actualEnv := runtimeEnvMap(actual.Config.Env)
	envKeys := make([]string, 0, len(expectedEnv))
	for key := range expectedEnv {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		if got, ok := actualEnv[key]; !ok || got != expectedEnv[key] {
			fields = append(fields, "env:"+key)
		}
	}
	labelKeys := make([]string, 0, len(expected.Config.Labels))
	for key := range expected.Config.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		if got, ok := actual.Config.Labels[key]; !ok || got != expected.Config.Labels[key] {
			fields = append(fields, "label:"+key)
		}
	}
	if len(expected.Config.Entrypoint) > 0 && !slices.Equal(expected.Config.Entrypoint, actual.Config.Entrypoint) {
		fields = append(fields, "entrypoint_override")
	}
	if len(expected.Config.Cmd) > 0 && !slices.Equal(expected.Config.Cmd, actual.Config.Cmd) {
		fields = append(fields, "command_override")
	}
	if strings.TrimSpace(expected.Config.User) != "" && expected.Config.User != actual.Config.User {
		fields = append(fields, "user_override")
	}
	if strings.TrimSpace(expected.Config.WorkingDir) != "" && expected.Config.WorkingDir != actual.Config.WorkingDir {
		fields = append(fields, "workdir_override")
	}
	if strings.TrimSpace(expected.Config.StopSignal) != "" && expected.Config.StopSignal != actual.Config.StopSignal {
		fields = append(fields, "stop_signal_override")
	}
	return fields
}

func criticalRuntimeMismatches(before, after inspectContainer) []string {
	fields := []string{}
	add := func(field string, mismatch bool) {
		if mismatch {
			fields = append(fields, field)
		}
	}
	// Config fields that Docker create preserves independently of target-image
	// defaults. Image-owned Env/Cmd/Entrypoint/User/WorkDir/StopSignal are checked
	// separately through runtimeOverrideFidelityMismatches.
	add("hostname", before.Config.Hostname != after.Config.Hostname)
	add("domainname", before.Config.Domainname != after.Config.Domainname)
	add("tty", before.Config.Tty != after.Config.Tty)
	add("open_stdin", before.Config.OpenStdin != after.Config.OpenStdin)
	add("stdin_once", before.Config.StdinOnce != after.Config.StdinOnce)
	add("stop_timeout", compactValue(before.Config.StopTimeout) != compactValue(after.Config.StopTimeout))

	add("privileged", before.HostConfig.Privileged != after.HostConfig.Privileged)
	add("readonly_rootfs", before.HostConfig.ReadonlyRootfs != after.HostConfig.ReadonlyRootfs)
	add("auto_remove", before.HostConfig.AutoRemove != after.HostConfig.AutoRemove)
	add("init", compactValue(before.HostConfig.Init) != compactValue(after.HostConfig.Init))
	add("restart_policy", compactValue(before.HostConfig.RestartPolicy) != compactValue(after.HostConfig.RestartPolicy))
	add("network_mode", strings.TrimSpace(before.HostConfig.NetworkMode) != strings.TrimSpace(after.HostConfig.NetworkMode))
	add("ipc_mode", before.HostConfig.IpcMode != after.HostConfig.IpcMode)
	add("pid_mode", before.HostConfig.PidMode != after.HostConfig.PidMode)
	add("uts_mode", before.HostConfig.UTSMode != after.HostConfig.UTSMode)
	add("cgroupns_mode", before.HostConfig.CgroupnsMode != after.HostConfig.CgroupnsMode)
	add("userns_mode", before.HostConfig.UsernsMode != after.HostConfig.UsernsMode)
	add("cgroup_parent", before.HostConfig.CgroupParent != after.HostConfig.CgroupParent)
	add("runtime", strings.TrimSpace(before.HostConfig.Runtime) != strings.TrimSpace(after.HostConfig.Runtime))

	add("memory", before.HostConfig.Memory != after.HostConfig.Memory)
	add("memory_reservation", before.HostConfig.MemoryReservation != after.HostConfig.MemoryReservation)
	add("memory_swap", before.HostConfig.MemorySwap != after.HostConfig.MemorySwap)
	add("nano_cpus", before.HostConfig.NanoCpus != after.HostConfig.NanoCpus)
	add("cpu_shares", before.HostConfig.CpuShares != after.HostConfig.CpuShares)
	add("cpuset_cpus", before.HostConfig.CpusetCpus != after.HostConfig.CpusetCpus)
	add("cpuset_mems", before.HostConfig.CpusetMems != after.HostConfig.CpusetMems)
	add("pids_limit", compactValue(before.HostConfig.PidsLimit) != compactValue(after.HostConfig.PidsLimit))
	add("oom_kill_disable", compactValue(before.HostConfig.OomKillDisable) != compactValue(after.HostConfig.OomKillDisable))
	add("oom_score_adj", before.HostConfig.OomScoreAdj != after.HostConfig.OomScoreAdj)
	add("shm_size", before.HostConfig.ShmSize != after.HostConfig.ShmSize)

	add("ports", !slices.Equal(stablePorts(before), stablePorts(after)))
	add("publish_all_ports", before.HostConfig.PublishAllPorts != after.HostConfig.PublishAllPorts)
	add("mounts", !slices.Equal(fidelityMounts(before), fidelityMounts(after)))
	add("tmpfs", !maps.Equal(before.HostConfig.Tmpfs, after.HostConfig.Tmpfs))
	add("networks", !slices.Equal(stableNetworks(before), stableNetworks(after)))
	add("cap_add", !slices.Equal(normalizedCapabilities(before.HostConfig.CapAdd), normalizedCapabilities(after.HostConfig.CapAdd)))
	add("cap_drop", !slices.Equal(normalizedCapabilities(before.HostConfig.CapDrop), normalizedCapabilities(after.HostConfig.CapDrop)))
	add("group_add", !slices.Equal(sortedCopy(before.HostConfig.GroupAdd), sortedCopy(after.HostConfig.GroupAdd)))
	add("links", !slices.Equal(sortedCopy(before.HostConfig.Links), sortedCopy(after.HostConfig.Links)))
	add("volumes_from", !slices.Equal(sortedCopy(before.HostConfig.VolumesFrom), sortedCopy(after.HostConfig.VolumesFrom)))
	add("sysctls", !maps.Equal(before.HostConfig.Sysctls, after.HostConfig.Sysctls))
	add("dns", !slices.Equal(sortedCopy(before.HostConfig.DNS), sortedCopy(after.HostConfig.DNS)))
	add("dns_search", !slices.Equal(sortedCopy(before.HostConfig.DNSSearch), sortedCopy(after.HostConfig.DNSSearch)))
	add("dns_options", !slices.Equal(sortedCopy(before.HostConfig.DNSOptions), sortedCopy(after.HostConfig.DNSOptions)))
	add("extra_hosts", !slices.Equal(sortedCopy(before.HostConfig.ExtraHosts), sortedCopy(after.HostConfig.ExtraHosts)))
	add("devices", !slices.Equal(stableDevices(before), stableDevices(after)))
	add("device_requests", compactValue(before.HostConfig.DeviceRequests) != compactValue(after.HostConfig.DeviceRequests))
	add("security_opt", !slices.Equal(sortedCopy(before.HostConfig.SecurityOpt), sortedCopy(after.HostConfig.SecurityOpt)))
	add("log_config", compactValue(before.HostConfig.LogConfig) != compactValue(after.HostConfig.LogConfig))
	add("ulimits", compactValue(before.HostConfig.Ulimits) != compactValue(after.HostConfig.Ulimits))
	return fields
}

// applyPreparedTargetImage recreates the protected target from the exact image
// ID already present on the Docker host. This is a deterministic fallback for
// a Watchtower no-op after a previous rollback left the desired image local but
// the running container on the previous image.
func (a *App) applyPreparedTargetImage(ctx context.Context, h db.Host, prepared preflightPrepared, expectedImageID string, jobID int64) error {
	target := prepared.TargetInspect
	if strings.TrimSpace(target.ID) == "" {
		return fmt.Errorf("prepared target runtime is unavailable")
	}
	if strings.TrimSpace(expectedImageID) == "" {
		return fmt.Errorf("expected target image id is unavailable")
	}
	if strings.TrimSpace(target.Config.Labels["com.docker.swarm.service.name"]) != "" {
		return fmt.Errorf("direct target-image recreation is not supported for Docker Swarm services")
	}
	if !a.localTargetImageAvailable(ctx, h.Endpoint, expectedImageID) {
		return fmt.Errorf("expected target image %s is not available locally", expectedImageID)
	}

	// Rebuild the new container from the runtime DELTA relative to the source
	// image, not from Docker inspect's fully materialized old image defaults.
	// This lets the target image supply its own Cmd/Entrypoint/default Env and
	// OCI labels while preserving actual user/Compose overrides and HostConfig.
	createTarget, overrideSummary, defaultsSourceID, defaultsErr := a.preparedRuntimeOverrides(ctx, h, target)
	if defaultsErr != nil {
		return fmt.Errorf("derive safe runtime overrides for deterministic target: %w", defaultsErr)
	}
	if jobID > 0 {
		_ = a.Store.AddJobLog(context.Background(), jobID, "INFO", "image-verify", fmt.Sprintf("deterministic target runtime delta prepared from source_image=%s: env_overrides=%d label_overrides=%d command_override=%t entrypoint_override=%t user_override=%t workdir_override=%t stop_signal_override=%t", defaultsSourceID, overrideSummary.Environment, overrideSummary.Labels, overrideSummary.Command, overrideSummary.Entrypoint, overrideSummary.User, overrideSummary.WorkingDir, overrideSummary.StopSignal))
	}

	if len(prepared.Dependencies) > 0 {
		stopDependentsBestEffort(ctx, a, h.Endpoint, prepared.Dependencies)
	}
	wasRunning := target.State.Running || target.State.Restarting
	if err := a.recreateContainerRuntime(ctx, h.Endpoint, createTarget, expectedImageID, wasRunning, ""); err != nil {
		return fmt.Errorf("recreate target from local image %s: %w", expectedImageID, err)
	}
	after, inspectErr := a.inspectOne(ctx, h.ID, strings.TrimPrefix(target.Name, "/"))
	if inspectErr != nil {
		return fmt.Errorf("deterministic target recreated but runtime fidelity could not be inspected: %w", inspectErr)
	}
	if mismatches := criticalRuntimeMismatches(target, after); len(mismatches) > 0 {
		return fmt.Errorf("deterministic target runtime fidelity mismatch in: %s", strings.Join(mismatches, ", "))
	}
	if mismatches := runtimeOverrideFidelityMismatches(createTarget, after); len(mismatches) > 0 {
		return fmt.Errorf("deterministic target override fidelity mismatch in: %s", strings.Join(mismatches, ", "))
	}
	if jobID > 0 {
		_ = a.Store.AddJobLog(context.Background(), jobID, "INFO", "image-verify", "deterministic target runtime fidelity verified (environment/labels, host permissions, ports, mounts, networks and security settings preserved)")
	}
	return nil
}

// probeRecoveredApplication waits for an effective Custom Verification profile
// without persisting a second verification result. It is used after a cold Data
// Protection snapshot so infrastructure services (DNS, proxy, databases, etc.)
// are actually usable again before registry/update activity resumes.
func (a *App) probeRecoveredApplication(ctx context.Context, hostID int64, container string) error {
	profile, err := a.effectiveVerificationProfile(ctx, hostID, container)
	if err != nil || !profile.Configured {
		return nil
	}
	if profile.StartDelaySeconds > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(profile.StartDelaySeconds) * time.Second):
		}
	}
	for i, check := range profile.Checks {
		attempts := profile.RetryCount + 1
		if attempts < 1 {
			attempts = 1
		}
		var last VerificationCheckResult
		for attempt := 1; attempt <= attempts; attempt++ {
			last = a.executeVerificationCheck(ctx, check)
			if last.Status == verificationStatusVerified {
				break
			}
			if attempt < attempts && profile.RetryIntervalSeconds > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(profile.RetryIntervalSeconds) * time.Second):
				}
			}
		}
		if last.Status != verificationStatusVerified {
			return fmt.Errorf("recovery check %d (%s %s) failed: %s", i+1, last.Type, last.Target, last.Error)
		}
	}
	return nil
}

// updateSummaryWithLocalRecreate keeps the normal summary shape consumed by the
// UI while recording that Vibewatch, rather than Watchtower, applied the exact
// already-downloaded target image.
func updateSummaryWithLocalRecreate(raw []byte, res watchtower.UpdateResponse) ([]byte, watchtower.UpdateResponse) {
	workerUpdated := res.Summary.Updated
	workerSkipped := res.Summary.Skipped
	res.Summary.Updated = 1
	payload := map[string]any{
		"summary": res.Summary,
		"vibewatch": map[string]any{
			"target_recreate": true,
			"worker_updated":  workerUpdated,
			"worker_skipped":  workerSkipped,
		},
	}
	if len(raw) > 0 {
		var worker any
		if json.Unmarshal(raw, &worker) == nil {
			payload["worker"] = worker
		}
	}
	if bs, err := json.Marshal(payload); err == nil {
		return bs, res
	}
	return raw, res
}
