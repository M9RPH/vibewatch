package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
)

type ConfigDriftChange struct {
	Field   string `json:"field"`
	Before  string `json:"before"`
	Current string `json:"current"`
}

type ConfigDriftView struct {
	Status     string              `json:"status"` // matches, drift, no_snapshot, unavailable, not_checked
	Changes    []ConfigDriftChange `json:"changes"`
	BaselineAt string              `json:"baseline_at"`
	CheckedAt  string              `json:"checked_at"`
	Error      string              `json:"error,omitempty"`
}

func driftViewFromDB(x db.ConfigDriftState) ConfigDriftView {
	out := ConfigDriftView{Status: x.Status, BaselineAt: x.BaselineAt, CheckedAt: x.CheckedAt, Error: x.Error, Changes: []ConfigDriftChange{}}
	if strings.TrimSpace(out.Status) == "" {
		out.Status = "not_checked"
	}
	_ = json.Unmarshal([]byte(x.DetailsJSON), &out.Changes)
	if out.Changes == nil {
		out.Changes = []ConfigDriftChange{}
	}
	return out
}

func driftStale(v string) bool {
	if strings.TrimSpace(v) == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t, err = time.Parse(time.RFC3339, v)
	}
	return err != nil || time.Since(t) > 5*time.Minute
}

func parseDriftTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func (a *App) shouldRebaselineLegacyPostUpdate(ctx context.Context, cached db.ConfigDriftState) bool {
	if strings.TrimSpace(cached.BaselineJSON) != "" || strings.TrimSpace(cached.BaselineAt) == "" {
		return false
	}
	rows, err := a.Store.UpdateHistory(ctx, 12, cached.HostID, cached.ContainerName)
	if err != nil {
		return false
	}
	baselineAt := parseDriftTime(cached.BaselineAt)
	for _, row := range rows {
		if row.Status != "success" || (row.Action != "update" && row.Action != "rollback") {
			continue
		}
		ts := parseDriftTime(row.TS)
		if !ts.IsZero() && (baselineAt.IsZero() || ts.After(baselineAt)) {
			return true
		}
	}
	return false
}

func inspectIdentity(c inspectContainer) string {
	labels := c.Config.Labels
	if project := strings.TrimSpace(labels["com.docker.compose.project"]); project != "" {
		return "compose:" + project + ":" + strings.TrimSpace(labels["com.docker.compose.service"])
	}
	if svc := strings.TrimSpace(labels["com.docker.swarm.service.name"]); svc != "" {
		return "swarm:" + svc
	}
	return "container:" + strings.TrimPrefix(c.Name, "/")
}

func stableUserLabels(labels map[string]string) []string {
	out := []string{}
	for k, v := range labels {
		if strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "com.docker.swarm.") || strings.HasPrefix(k, "com.docker.stack.") {
			continue
		}
		if strings.HasPrefix(k, "io.vibewatch.") {
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func stableMounts(c inspectContainer) []string {
	out := []string{}
	for _, m := range c.Mounts {
		if m.Type != "bind" && m.Type != "volume" {
			continue
		}
		src := m.Source
		if m.Type == "volume" && m.Name != "" {
			src = m.Name
		}
		out = append(out, fmt.Sprintf("%s:%s:%s:rw=%t:mode=%s:prop=%s", m.Type, src, m.Destination, m.RW, m.Mode, m.Propagation))
	}
	sort.Strings(out)
	return out
}
func stablePorts(c inspectContainer) []string {
	out := []string{}
	for cp, bindings := range c.HostConfig.PortBindings {
		for _, p := range bindings {
			out = append(out, p.HostIP+":"+p.HostPort+"->"+cp)
		}
	}
	sort.Strings(out)
	return out
}
func stableNetworks(c inspectContainer) []string {
	out := []string{}
	for n := range c.NetworkSettings.Networks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
func stableDevices(c inspectContainer) []string {
	out := []string{}
	for _, d := range c.HostConfig.Devices {
		out = append(out, d.PathOnHost+":"+d.PathInContainer+":"+d.CgroupPermissions)
	}
	sort.Strings(out)
	return out
}
func stableHealth(c inspectContainer) any {
	if c.Config.Healthcheck == nil {
		return nil
	}
	return c.Config.Healthcheck
}

func compactValue(v any) string {
	b, _ := json.Marshal(v)
	x := string(b)
	if len(x) > 320 {
		return x[:317] + "..."
	}
	return x
}

func envValues(xs []string) map[string]string {
	out := map[string]string{}
	for _, item := range xs {
		parts := strings.SplitN(item, "=", 2)
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		value := ""
		if len(parts) == 2 {
			value = parts[1]
		}
		out[key] = value
	}
	return out
}

func secretSafeMapChange(field string, before, current map[string]string) *ConfigDriftChange {
	added, removed, changed := []string{}, []string{}, []string{}
	for k, v := range before {
		nv, ok := current[k]
		if !ok {
			removed = append(removed, k)
		} else if nv != v {
			changed = append(changed, k)
		}
	}
	for k := range current {
		if _, ok := before[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	if len(added)+len(removed)+len(changed) == 0 {
		return nil
	}
	parts := func(prefix string, xs []string) string {
		if len(xs) == 0 {
			return ""
		}
		return prefix + ": " + strings.Join(xs, ", ")
	}
	beforeParts, currentParts := []string{}, []string{}
	if v := parts("Removed", removed); v != "" {
		beforeParts = append(beforeParts, v)
	}
	if v := parts("Changed", changed); v != "" {
		beforeParts = append(beforeParts, v)
		currentParts = append(currentParts, v)
	}
	if v := parts("Added", added); v != "" {
		currentParts = append(currentParts, v)
	}
	if len(beforeParts) == 0 {
		beforeParts = append(beforeParts, "No keys removed")
	}
	if len(currentParts) == 0 {
		currentParts = append(currentParts, "No keys added")
	}
	return &ConfigDriftChange{Field: field, Before: strings.Join(beforeParts, " · "), Current: strings.Join(currentParts, " · ")}
}

func userLabelMap(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "com.docker.swarm.") || strings.HasPrefix(k, "com.docker.stack.") || strings.HasPrefix(k, "io.vibewatch.") {
			continue
		}
		out[k] = v
	}
	return out
}

func commandDescriptor(xs []string) string {
	if len(xs) == 0 {
		return "Not set"
	}
	first := strings.TrimSpace(xs[0])
	if first == "" {
		first = "<empty>"
	}
	return fmt.Sprintf("%s · %d argument(s)", first, len(xs))
}

func healthDescriptor(c inspectContainer) string {
	h := c.Config.Healthcheck
	if h == nil {
		return "Not configured"
	}
	mode := "configured"
	if len(h.Test) > 0 {
		mode = h.Test[0]
	}
	return fmt.Sprintf("%s · interval=%s · timeout=%s · retries=%d", mode, time.Duration(h.Interval), time.Duration(h.Timeout), h.Retries)
}

type configDriftBaseline struct {
	ImageReference    string            `json:"image_reference"`
	Environment       map[string]string `json:"environment"`
	CommandHash       string            `json:"command_hash"`
	CommandDescriptor string            `json:"command_descriptor"`
	EntrypointHash    string            `json:"entrypoint_hash"`
	EntrypointDesc    string            `json:"entrypoint_descriptor"`
	User              string            `json:"user"`
	WorkingDir        string            `json:"working_dir"`
	RestartPolicy     string            `json:"restart_policy"`
	Ports             []string          `json:"ports"`
	Mounts            []string          `json:"mounts"`
	Networks          []string          `json:"networks"`
	HealthHash        string            `json:"health_hash"`
	HealthDescriptor  string            `json:"health_descriptor"`
	Privileged        bool              `json:"privileged"`
	ReadonlyRootfs    bool              `json:"readonly_rootfs"`
	CapabilitiesAdded []string          `json:"capabilities_added"`
	CapabilitiesDrop  []string          `json:"capabilities_dropped"`
	DNS               []string          `json:"dns"`
	ExtraHosts        []string          `json:"extra_hosts"`
	Devices           []string          `json:"devices"`
	Labels            map[string]string `json:"labels"`
}

func driftHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func hashedStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		sum := sha256.Sum256([]byte(v))
		out[k] = fmt.Sprintf("%x", sum[:])
	}
	return out
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

func baselineFromInspect(c inspectContainer) configDriftBaseline {
	return configDriftBaseline{
		ImageReference:    c.Config.Image,
		Environment:       hashedStringMap(envValues(c.Config.Env)),
		CommandHash:       driftHash(c.Config.Cmd),
		CommandDescriptor: commandDescriptor(c.Config.Cmd),
		EntrypointHash:    driftHash(c.Config.Entrypoint),
		EntrypointDesc:    commandDescriptor(c.Config.Entrypoint),
		User:              c.Config.User,
		WorkingDir:        c.Config.WorkingDir,
		RestartPolicy:     compactValue(c.HostConfig.RestartPolicy),
		Ports:             stablePorts(c),
		Mounts:            stableMounts(c),
		Networks:          stableNetworks(c),
		HealthHash:        driftHash(stableHealth(c)),
		HealthDescriptor:  healthDescriptor(c),
		Privileged:        c.HostConfig.Privileged,
		ReadonlyRootfs:    c.HostConfig.ReadonlyRootfs,
		CapabilitiesAdded: sortedCopy(c.HostConfig.CapAdd),
		CapabilitiesDrop:  sortedCopy(c.HostConfig.CapDrop),
		DNS:               sortedCopy(c.HostConfig.DNS),
		ExtraHosts:        sortedCopy(c.HostConfig.ExtraHosts),
		Devices:           stableDevices(c),
		Labels:            hashedStringMap(userLabelMap(c.Config.Labels)),
	}
}

func compareDriftBaseline(before configDriftBaseline, current inspectContainer) []ConfigDriftChange {
	changes := []ConfigDriftChange{}
	add := func(field string, a, b any) {
		aa, bb := compactValue(a), compactValue(b)
		if aa != bb {
			changes = append(changes, ConfigDriftChange{Field: field, Before: aa, Current: bb})
		}
	}
	add("Image reference", before.ImageReference, current.Config.Image)
	if ch := secretSafeMapChange("Environment", before.Environment, hashedStringMap(envValues(current.Config.Env))); ch != nil {
		changes = append(changes, *ch)
	}
	if before.CommandHash != driftHash(current.Config.Cmd) {
		changes = append(changes, ConfigDriftChange{Field: "Command", Before: before.CommandDescriptor, Current: commandDescriptor(current.Config.Cmd)})
	}
	if before.EntrypointHash != driftHash(current.Config.Entrypoint) {
		changes = append(changes, ConfigDriftChange{Field: "Entrypoint", Before: before.EntrypointDesc, Current: commandDescriptor(current.Config.Entrypoint)})
	}
	add("User", before.User, current.Config.User)
	add("Working directory", before.WorkingDir, current.Config.WorkingDir)
	add("Restart policy", before.RestartPolicy, compactValue(current.HostConfig.RestartPolicy))
	add("Ports", before.Ports, stablePorts(current))
	add("Mounts", before.Mounts, stableMounts(current))
	add("Networks", before.Networks, stableNetworks(current))
	if before.HealthHash != driftHash(stableHealth(current)) {
		changes = append(changes, ConfigDriftChange{Field: "Healthcheck", Before: before.HealthDescriptor, Current: healthDescriptor(current)})
	}
	add("Privileged", before.Privileged, current.HostConfig.Privileged)
	add("Read-only rootfs", before.ReadonlyRootfs, current.HostConfig.ReadonlyRootfs)
	add("Capabilities added", before.CapabilitiesAdded, sortedCopy(current.HostConfig.CapAdd))
	add("Capabilities dropped", before.CapabilitiesDrop, sortedCopy(current.HostConfig.CapDrop))
	add("DNS", before.DNS, sortedCopy(current.HostConfig.DNS))
	add("Extra hosts", before.ExtraHosts, sortedCopy(current.HostConfig.ExtraHosts))
	add("Devices", before.Devices, stableDevices(current))
	if ch := secretSafeMapChange("Labels", before.Labels, hashedStringMap(userLabelMap(current.Config.Labels))); ch != nil {
		changes = append(changes, *ch)
	}
	return changes
}

func compareInspectConfig(before, current inspectContainer) []ConfigDriftChange {
	return compareDriftBaseline(baselineFromInspect(before), current)
}

func driftBaselineJSON(c inspectContainer) string {
	b, _ := json.Marshal(baselineFromInspect(c))
	return string(b)
}

func (a *App) captureCurrentConfigDriftBaseline(ctx context.Context, hostID int64, container, source string) error {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	raw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, container)
	if err != nil {
		return err
	}
	var current []inspectContainer
	if err := json.Unmarshal(raw, &current); err != nil || len(current) == 0 {
		return fmt.Errorf("could not decode current container configuration")
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	return a.Store.SaveConfigDrift(context.Background(), db.ConfigDriftState{
		HostID: hostID, ContainerName: container, Status: "matches", DetailsJSON: "[]",
		BaselineAt: at, BaselineJSON: driftBaselineJSON(current[0]), BaselineSource: source, CheckedAt: at,
	})
}

func (a *App) latestSnapshotInspect(hostID int64, c dockercli.Container) (inspectContainer, string, error) {
	kind, key, _, _ := backupUnitFromContainer(c)
	snaps := a.listSnapshotsForUnit(hostID, kind, key)
	if len(snaps) == 0 {
		return inspectContainer{}, "", fmt.Errorf("no recovery snapshot")
	}
	path, info, err := a.resolveSnapshotPath(hostID, kind, key, snaps[0].ID)
	if err != nil {
		return inspectContainer{}, "", err
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		return inspectContainer{}, "", err
	}
	var old []inspectContainer
	if err = json.Unmarshal(raw, &old); err != nil {
		return inspectContainer{}, "", err
	}
	wanted := ""
	if c.StackName != "" && c.StackService != "" {
		if c.StackType == "swarm" {
			wanted = "swarm:" + c.StackName + "_" + c.StackService
		} else {
			wanted = "compose:" + c.StackName + ":" + c.StackService
		}
	} else {
		wanted = "container:" + c.Name
	}
	for _, x := range old {
		if inspectIdentity(x) == wanted || strings.TrimPrefix(x.Name, "/") == c.Name {
			return x, info.CreatedAt, nil
		}
	}
	return inspectContainer{}, info.CreatedAt, fmt.Errorf("container not present in latest snapshot")
}

func (a *App) refreshContainerDrift(ctx context.Context, hostID int64, c dockercli.Container) {
	cached, _ := a.Store.ConfigDrift(ctx, hostID, c.Name)
	// V0.6.0 used the latest recovery snapshot directly as the drift baseline.
	// If that snapshot was created immediately before a successful update, the
	// post-update container could be reported as drift even though only the image
	// changed. Rebaseline those legacy rows once on the current runtime state.
	if a.shouldRebaselineLegacyPostUpdate(ctx, cached) {
		if err := a.captureCurrentConfigDriftBaseline(ctx, hostID, c.Name, "legacy-post-update"); err == nil {
			return
		}
	}
	state := db.ConfigDriftState{
		HostID: hostID, ContainerName: c.Name, Status: "not_checked", DetailsJSON: "[]",
		BaselineAt: cached.BaselineAt, BaselineJSON: cached.BaselineJSON, BaselineSource: cached.BaselineSource,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	var baseline configDriftBaseline
	if strings.TrimSpace(state.BaselineJSON) != "" {
		if err := json.Unmarshal([]byte(state.BaselineJSON), &baseline); err != nil {
			state.BaselineJSON = ""
			state.BaselineSource = ""
		}
	}
	if strings.TrimSpace(state.BaselineJSON) == "" {
		before, baselineAt, err := a.latestSnapshotInspect(hostID, c)
		if err != nil {
			if strings.Contains(err.Error(), "no recovery snapshot") {
				state.Status = "no_snapshot"
			} else {
				state.Status = "unavailable"
				state.Error = err.Error()
			}
			_ = a.Store.SaveConfigDrift(context.Background(), state)
			return
		}
		baseline = baselineFromInspect(before)
		b, _ := json.Marshal(baseline)
		state.BaselineAt = baselineAt
		state.BaselineJSON = string(b)
		state.BaselineSource = "recovery-snapshot"
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		state.Status = "unavailable"
		state.Error = err.Error()
		_ = a.Store.SaveConfigDrift(context.Background(), state)
		return
	}
	raw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, c.Name)
	if err != nil {
		state.Status = "unavailable"
		state.Error = err.Error()
		_ = a.Store.SaveConfigDrift(context.Background(), state)
		return
	}
	var now []inspectContainer
	if err = json.Unmarshal(raw, &now); err != nil || len(now) == 0 {
		state.Status = "unavailable"
		state.Error = "could not decode current container configuration"
		_ = a.Store.SaveConfigDrift(context.Background(), state)
		return
	}
	changes := compareDriftBaseline(baseline, now[0])
	b, _ := json.Marshal(changes)
	state.DetailsJSON = string(b)
	state.Error = ""
	if len(changes) > 0 {
		state.Status = "drift"
	} else {
		state.Status = "matches"
	}
	_ = a.Store.SaveConfigDrift(context.Background(), state)
}
