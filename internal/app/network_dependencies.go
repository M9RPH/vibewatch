package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
)

const networkNamespaceDependencyType = "network_namespace"

type networkNamespaceDependency struct {
	Type                string `json:"type"`
	SourceContainer     string `json:"source_container"`
	SourceContainerID   string `json:"source_container_id"`
	TargetContainer     string `json:"target_container"`
	TargetContainerID   string `json:"target_container_id"`
	RequiresRecreate    bool   `json:"requires_recreate"`
	WasRunning          bool   `json:"was_running"`
	OriginalNetworkMode string `json:"original_network_mode"`
	SnapshotID          string `json:"snapshot_id,omitempty"`
	ComposeProject      string `json:"compose_project,omitempty"`
	ComposeService      string `json:"compose_service,omitempty"`
}

type networkNamespaceDependencyRuntime struct {
	networkNamespaceDependency
	Inspect inspectContainer `json:"-"`
}

func containerNamespaceRef(mode string) (string, bool) {
	mode = strings.TrimSpace(mode)
	if !strings.HasPrefix(mode, "container:") {
		return "", false
	}
	ref := strings.TrimSpace(strings.TrimPrefix(mode, "container:"))
	return ref, ref != ""
}

func sameContainerIdentity(ref string, target inspectContainer) bool {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "/"))
	if ref == "" {
		return false
	}
	targetID := strings.TrimSpace(target.ID)
	targetName := strings.TrimPrefix(strings.TrimSpace(target.Name), "/")
	if targetID != "" && (ref == targetID || (len(ref) >= 12 && strings.HasPrefix(targetID, ref))) {
		return true
	}
	return targetName != "" && ref == targetName
}

func decodeInspectOne(raw []byte) (inspectContainer, error) {
	var xs []inspectContainer
	if err := json.Unmarshal(raw, &xs); err != nil {
		return inspectContainer{}, err
	}
	if len(xs) == 0 {
		return inspectContainer{}, fmt.Errorf("container inspect returned no result")
	}
	return xs[0], nil
}

// discoverNetworkNamespaceDependents captures direct Docker Engine-level
// container:<id> dependencies before the target container is recreated.
// Compose network_mode: service:<name> resolves to this relationship in the
// runtime HostConfig, so detection deliberately relies on Docker inspect.
func (a *App) discoverNetworkNamespaceDependents(ctx context.Context, hostID int64, targetName string) (inspectContainer, []networkNamespaceDependencyRuntime, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return inspectContainer{}, nil, err
	}
	targetRaw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, targetName)
	if err != nil {
		return inspectContainer{}, nil, err
	}
	target, err := decodeInspectOne(targetRaw)
	if err != nil {
		return inspectContainer{}, nil, err
	}
	containers, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return inspectContainer{}, nil, err
	}
	candidates := make([]dockercli.Container, 0, len(containers))
	candidateIDs := make([]string, 0, len(containers))
	for _, c := range containers {
		if c.Name == targetName || (target.ID != "" && c.ID == target.ID) {
			continue
		}
		candidates = append(candidates, c)
		candidateIDs = append(candidateIDs, c.ID)
	}
	inspected := []inspectContainer{}
	if len(candidateIDs) > 0 {
		raw, inspectErr := a.Docker.InspectContainersRaw(ctx, h.Endpoint, candidateIDs...)
		if inspectErr != nil {
			return inspectContainer{}, nil, fmt.Errorf("batch inspect possible namespace dependents: %w", inspectErr)
		}
		if inspectErr = json.Unmarshal(raw, &inspected); inspectErr != nil {
			return inspectContainer{}, nil, fmt.Errorf("decode possible namespace dependents: %w", inspectErr)
		}
	}
	deps := make([]networkNamespaceDependencyRuntime, 0)
	for _, c := range candidates {
		var depInspect inspectContainer
		found := false
		for _, x := range inspected {
			if sameContainerIdentity(c.ID, x) || sameContainerIdentity(c.Name, x) {
				depInspect = x
				found = true
				break
			}
		}
		if !found {
			// Missing a single container during the pre-update scan could hide an
			// actual namespace dependency. Fail closed rather than recreating the
			// parent without a complete dependency graph.
			return inspectContainer{}, nil, fmt.Errorf("possible namespace dependent %s was not returned by Docker batch inspect", c.Name)
		}
		ref, ok := containerNamespaceRef(depInspect.HostConfig.NetworkMode)
		if !ok {
			continue
		}
		matches := sameContainerIdentity(ref, target)
		if !matches {
			// Docker normally stores the resolved full ID. The fallback also
			// handles engines/configurations that retain a name/short reference.
			resolved, resolveErr := a.Docker.Run(ctx, h.Endpoint, "inspect", "--format", "{{.Id}}", ref)
			matches = resolveErr == nil && strings.TrimSpace(resolved) == strings.TrimSpace(target.ID)
		}
		if !matches {
			continue
		}
		labels := depInspect.Config.Labels
		deps = append(deps, networkNamespaceDependencyRuntime{
			networkNamespaceDependency: networkNamespaceDependency{
				Type:                networkNamespaceDependencyType,
				SourceContainer:     strings.TrimPrefix(depInspect.Name, "/"),
				SourceContainerID:   depInspect.ID,
				TargetContainer:     strings.TrimPrefix(target.Name, "/"),
				TargetContainerID:   target.ID,
				RequiresRecreate:    true,
				WasRunning:          depInspect.State.Running,
				OriginalNetworkMode: depInspect.HostConfig.NetworkMode,
				ComposeProject:      strings.TrimSpace(labels["com.docker.compose.project"]),
				ComposeService:      strings.TrimSpace(labels["com.docker.compose.service"]),
			},
			Inspect: depInspect,
		})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].SourceContainer < deps[j].SourceContainer })
	return target, deps, nil
}

func dependencyNames(deps []networkNamespaceDependencyRuntime) string {
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		if dep.SourceContainer != "" {
			names = append(names, dep.SourceContainer)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func dependencyRecordsJSON(deps []networkNamespaceDependencyRuntime) string {
	rows := make([]networkNamespaceDependency, 0, len(deps))
	for _, dep := range deps {
		rows = append(rows, dep.networkNamespaceDependency)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func restorePointDependencies(rp db.RestorePoint) []networkNamespaceDependency {
	var deps []networkNamespaceDependency
	if strings.TrimSpace(rp.DependenciesJSON) == "" {
		return deps
	}
	if json.Unmarshal([]byte(rp.DependenciesJSON), &deps) != nil {
		return nil
	}
	return deps
}

// attachDependencySnapshots ensures every dependent has a retained runtime
// configuration. Dependents in the same backup unit reuse the parent's
// snapshot; cross-stack/standalone dependents receive their own recovery
// snapshot and therefore participate in the same retention/protection model.
func (a *App) attachDependencySnapshots(ctx context.Context, hostID int64, parentSnap ContainerBackupSnapshot, deps []networkNamespaceDependencyRuntime) ([]networkNamespaceDependencyRuntime, error) {
	if len(deps) == 0 {
		return deps, nil
	}
	_, parentInfo, err := a.findSnapshotByID(hostID, parentSnap.ID, "")
	if err != nil {
		return nil, err
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return nil, err
	}
	// A stack snapshot may contain several dependents. Track coverage so we
	// create at most one extra snapshot for a backup unit and reuse it for all
	// dependents contained in that unit. This also avoids unnecessary retention
	// churn for multi-dependent VPN stacks.
	coveredBy := map[string]string{}
	for _, name := range parentInfo.Containers {
		coveredBy[name] = parentSnap.ID
	}
	for i := range deps {
		if _, _, validateErr := createArgsFromInspect(deps[i].Inspect, "vibewatch-dependency-validation:latest"); validateErr != nil {
			return nil, fmt.Errorf("dependency %s cannot be recreated safely: %w", deps[i].SourceContainer, validateErr)
		}
		if snapshotID := coveredBy[deps[i].SourceContainer]; snapshotID != "" {
			deps[i].SnapshotID = snapshotID
			continue
		}
		snapshotCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 60*time.Second, 3*time.Minute))
		snap, snapErr := a.createSnapshotForContainer(snapshotCtx, hostID, deps[i].SourceContainer, "before-dependency-recreate")
		cancel()
		if snapErr != nil {
			return nil, fmt.Errorf("dependency %s recovery snapshot: %w", deps[i].SourceContainer, snapErr)
		}
		_, info, infoErr := a.findSnapshotByID(hostID, snap.ID, "")
		if infoErr != nil {
			return nil, fmt.Errorf("dependency %s recovery snapshot metadata: %w", deps[i].SourceContainer, infoErr)
		}
		for _, name := range info.Containers {
			coveredBy[name] = snap.ID
		}
		deps[i].SnapshotID = snap.ID
	}
	return deps, nil
}

func (a *App) dependencyInspectFromSnapshot(hostID int64, dep networkNamespaceDependency) (inspectContainer, error) {
	if strings.TrimSpace(dep.SnapshotID) == "" {
		return inspectContainer{}, fmt.Errorf("dependency %s has no recovery snapshot", dep.SourceContainer)
	}
	path, _, err := a.findSnapshotByID(hostID, dep.SnapshotID, dep.SourceContainer)
	if err != nil {
		return inspectContainer{}, err
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		return inspectContainer{}, err
	}
	return findInspectForContainer(raw, dep.SourceContainer)
}

func (a *App) verifyDependentState(ctx context.Context, hostID int64, name string, shouldRun bool) error {
	if shouldRun {
		return a.verifyUpdatedContainer(ctx, hostID, name)
	}
	cur, err := a.inspectOne(ctx, hostID, name)
	if err != nil {
		return err
	}
	if cur.State.Running || cur.State.Restarting {
		return fmt.Errorf("dependent container %s should remain stopped but is running", name)
	}
	return nil
}

func (a *App) recreateOneNetworkNamespaceDependent(ctx context.Context, hostID int64, dep networkNamespaceDependencyRuntime, parentID string) error {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(parentID) == "" {
		return fmt.Errorf("new parent container id is empty")
	}
	sourceImage := strings.TrimSpace(dep.Inspect.Image)
	if sourceImage == "" {
		sourceImage = strings.TrimSpace(dep.Inspect.Config.Image)
	}
	if sourceImage == "" {
		return fmt.Errorf("dependent container %s image reference is unavailable", dep.SourceContainer)
	}
	imageRef := a.prepareRuntimeRestoreRef(ctx, h.Endpoint, sourceImage, dep.Inspect.Config.Image)
	if err := a.recreateContainerRuntime(ctx, h.Endpoint, dep.Inspect, imageRef, dep.WasRunning, "container:"+parentID); err != nil {
		return err
	}
	return a.verifyDependentState(ctx, hostID, dep.SourceContainer, dep.WasRunning)
}

func (a *App) recreateNetworkNamespaceDependents(ctx context.Context, jobID, hostID int64, parentName, parentID string, deps []networkNamespaceDependencyRuntime) error {
	if len(deps) == 0 {
		return nil
	}
	for i, dep := range deps {
		stage := fmt.Sprintf("Recreating network dependent %s (%d/%d)", dep.SourceContainer, i+1, len(deps))
		a.jobProgress(ctx, jobID, 88+int(float64(i)*6/float64(len(deps))), stage)
		detail := fmt.Sprintf("type=%s parent=%s dependent=%s old_parent_id=%s new_parent_id=%s was_running=%t", networkNamespaceDependencyType, parentName, dep.SourceContainer, dep.TargetContainerID, parentID, dep.WasRunning)
		_ = a.Store.Audit(ctx, "system", "dependency.recreate.started", hostID, dep.SourceContainer, detail)
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "dependency", fmt.Sprintf("Recreating %s because it shares %s network namespace", dep.SourceContainer, parentName))
		if err := a.recreateOneNetworkNamespaceDependent(ctx, hostID, dep, parentID); err != nil {
			_ = a.Store.Audit(context.Background(), "system", "dependency.recreate.failed", hostID, dep.SourceContainer, detail+" error="+err.Error())
			_ = a.Store.AddJobLog(context.Background(), jobID, "ERROR", "dependency", fmt.Sprintf("%s recreate failed: %v", dep.SourceContainer, err))
			return fmt.Errorf("dependent container %s recreation failed: %w", dep.SourceContainer, err)
		}
		_ = a.Store.Audit(ctx, "system", "dependency.recreate.success", hostID, dep.SourceContainer, detail)
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "dependency", fmt.Sprintf("%s recreated successfully and rebound to parent container %s", dep.SourceContainer, parentID))
		if err := a.captureCurrentConfigDriftBaseline(ctx, hostID, dep.SourceContainer, "post-dependency-recreate"); err != nil {
			_ = a.Store.AddJobLog(ctx, jobID, "WARN", "config-drift", fmt.Sprintf("Could not refresh %s drift baseline after dependency recreate: %v", dep.SourceContainer, err))
		}
	}
	return nil
}

// persistedDependencyRuntimes loads the exact pre-update runtime configuration
// from the retained snapshots so rollback can recreate dependents against the
// newly restored parent namespace.
func (a *App) persistedDependencyRuntimes(rp db.RestorePoint) ([]networkNamespaceDependencyRuntime, error) {
	deps := restorePointDependencies(rp)
	out := make([]networkNamespaceDependencyRuntime, 0, len(deps))
	for _, dep := range deps {
		if dep.Type != networkNamespaceDependencyType || !dep.RequiresRecreate {
			continue
		}
		inspect, err := a.dependencyInspectFromSnapshot(rp.HostID, dep)
		if err != nil {
			return nil, fmt.Errorf("load dependency %s recovery config: %w", dep.SourceContainer, err)
		}
		out = append(out, networkNamespaceDependencyRuntime{networkNamespaceDependency: dep, Inspect: inspect})
	}
	return out, nil
}

func stopDependentsBestEffort(ctx context.Context, a *App, endpoint string, deps []networkNamespaceDependencyRuntime) {
	for _, dep := range deps {
		_, _ = a.Docker.Run(ctx, endpoint, "stop", "-t", "10", dep.SourceContainer)
	}
}

func mergeDependencyRuntimes(primary, extra []networkNamespaceDependencyRuntime) []networkNamespaceDependencyRuntime {
	// Primary wins for identical source containers. During rollback the primary
	// set is the retained pre-update configuration, while extra contains any
	// namespace dependents that exist only in the current runtime.
	byName := make(map[string]networkNamespaceDependencyRuntime, len(primary)+len(extra))
	for _, dep := range extra {
		byName[dep.SourceContainer] = dep
	}
	for _, dep := range primary {
		byName[dep.SourceContainer] = dep
	}
	out := make([]networkNamespaceDependencyRuntime, 0, len(byName))
	for _, dep := range byName {
		out = append(out, dep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceContainer < out[j].SourceContainer })
	return out
}

func (a *App) dependencySnapshotPinned(ctx context.Context, hostID int64, snapshotID string) bool {
	snapshotID = strings.TrimSpace(snapshotID)
	if hostID <= 0 || snapshotID == "" {
		return false
	}
	points, err := a.Store.RestorePoints(ctx, 2000, hostID, "")
	if err != nil {
		// Retention must fail safe: if restore-point state cannot be read, do not
		// prune a snapshot that might be part of an active rollback transaction.
		return true
	}
	for _, rp := range points {
		if rp.Status == "expired" || rp.Status == "failed" {
			continue
		}
		for _, dep := range restorePointDependencies(rp) {
			if strings.TrimSpace(dep.SnapshotID) == snapshotID {
				return true
			}
		}
	}
	return false
}

func (a *App) dependencySnapshotsAvailable(rp db.RestorePoint) error {
	for _, dep := range restorePointDependencies(rp) {
		if dep.Type != networkNamespaceDependencyType || !dep.RequiresRecreate {
			continue
		}
		if strings.TrimSpace(dep.SnapshotID) == "" {
			return fmt.Errorf("dependency %s has no retained recovery snapshot", dep.SourceContainer)
		}
		if _, _, err := a.findSnapshotByID(rp.HostID, dep.SnapshotID, dep.SourceContainer); err != nil {
			return fmt.Errorf("dependency %s recovery snapshot is unavailable: %w", dep.SourceContainer, err)
		}
	}
	return nil
}
