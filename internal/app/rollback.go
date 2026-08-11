package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

type rollbackEndpointSettings struct {
	Aliases           []string `json:"Aliases"`
	IPAddress         string   `json:"IPAddress"`
	GlobalIPv6Address string   `json:"GlobalIPv6Address"`
	IPAMConfig        *struct {
		IPv4Address string `json:"IPv4Address"`
		IPv6Address string `json:"IPv6Address"`
	} `json:"IPAMConfig"`
}

func rollbackEndpoint(raw json.RawMessage) rollbackEndpointSettings {
	var x rollbackEndpointSettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &x)
	}
	return x
}

type snapshotImageInspect struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
}

func (a *App) findSnapshotForHistory(h db.UpdateHistory) (string, snapshotInfo, error) {
	if h.HostID <= 0 || h.SnapshotID == "" {
		return "", snapshotInfo{}, fmt.Errorf("snapshot reference missing")
	}
	root := filepath.Join(a.containerBackupRoot(), fmt.Sprintf("host-%d", h.HostID))
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())) == h.SnapshotID && strings.HasSuffix(strings.ToLower(d.Name()), ".zip") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", snapshotInfo{}, fmt.Errorf("recovery snapshot no longer exists")
	}
	info, _, err := readSnapshotInfo(found)
	if err != nil {
		return "", snapshotInfo{}, err
	}
	if info.HostID != h.HostID || !containsString(info.Containers, h.ContainerName) {
		return "", snapshotInfo{}, fmt.Errorf("snapshot does not match update history")
	}
	return found, info, nil
}

func findInspectForContainer(raw []byte, name string) (inspectContainer, error) {
	var xs []inspectContainer
	if err := json.Unmarshal(raw, &xs); err != nil {
		return inspectContainer{}, err
	}
	for _, x := range xs {
		if strings.TrimPrefix(x.Name, "/") == name {
			return x, nil
		}
	}
	// Swarm task names change, so fall back to the service label identity when
	// the concrete task name is no longer stable.
	for _, x := range xs {
		if svc := strings.TrimSpace(x.Config.Labels["com.docker.swarm.service.name"]); svc != "" && strings.Contains(name, strings.TrimPrefix(svc, strings.SplitN(svc, "_", 2)[0]+"_")) {
			return x, nil
		}
	}
	return inspectContainer{}, fmt.Errorf("container %s not found in snapshot", name)
}

func snapshotRollbackImage(path string, old inspectContainer) (localID, immutableRef string) {
	localID = strings.TrimSpace(old.Image)
	b, err := snapshotZipEntry(path, "images.json")
	if err != nil {
		return localID, ""
	}
	var xs []snapshotImageInspect
	if json.Unmarshal(b, &xs) != nil {
		return localID, ""
	}
	for _, x := range xs {
		if x.ID != localID {
			continue
		}
		for _, d := range x.RepoDigests {
			if strings.Contains(d, "@sha256:") {
				return localID, d
			}
		}
	}
	return localID, ""
}

func appendSortedArgs(args []string, flag string, values []string) []string {
	xs := append([]string(nil), values...)
	sort.Strings(xs)
	for _, v := range xs {
		if strings.TrimSpace(v) != "" {
			args = append(args, flag, v)
		}
	}
	return args
}

func createArgsFromInspect(c inspectContainer, targetImage string) ([]string, []string, error) {
	name := strings.TrimPrefix(c.Name, "/")
	if name == "" {
		return nil, nil, fmt.Errorf("snapshot container name is empty")
	}
	args := []string{"create", "--name", name}
	if c.Config.Hostname != "" {
		args = append(args, "--hostname", c.Config.Hostname)
	}
	if c.Config.Domainname != "" {
		args = append(args, "--domainname", c.Config.Domainname)
	}
	if c.Config.User != "" {
		args = append(args, "--user", c.Config.User)
	}
	if c.Config.WorkingDir != "" {
		args = append(args, "--workdir", c.Config.WorkingDir)
	}
	rp := strings.TrimSpace(c.HostConfig.RestartPolicy.Name)
	if rp != "" && rp != "no" {
		if rp == "on-failure" && c.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
			rp += ":" + strconv.Itoa(c.HostConfig.RestartPolicy.MaximumRetryCount)
		}
		args = append(args, "--restart", rp)
	}
	if c.HostConfig.Privileged {
		args = append(args, "--privileged")
	}
	if c.HostConfig.ReadonlyRootfs {
		args = append(args, "--read-only")
	}
	for _, v := range c.Config.Env {
		args = append(args, "--env", v)
	}
	labels := make([]string, 0, len(c.Config.Labels))
	for k, v := range c.Config.Labels {
		labels = append(labels, k+"="+v)
	}
	args = appendSortedArgs(args, "--label", labels)
	ports := []string{}
	for cp, bindings := range c.HostConfig.PortBindings {
		for _, p := range bindings {
			host := p.HostPort
			if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
				host = p.HostIP + ":" + host
			}
			if host != "" {
				ports = append(ports, host+":"+cp)
			}
		}
	}
	args = appendSortedArgs(args, "--publish", ports)
	for _, m := range c.Mounts {
		if m.Type != "bind" && m.Type != "volume" {
			continue
		}
		src := m.Source
		if m.Type == "volume" && m.Name != "" {
			src = m.Name
		}
		if src == "" || m.Destination == "" {
			continue
		}
		spec := "type=" + m.Type + ",src=" + src + ",dst=" + m.Destination
		if !m.RW {
			spec += ",readonly"
		}
		if m.Type == "bind" && m.Propagation != "" && m.Propagation != "rprivate" {
			spec += ",bind-propagation=" + m.Propagation
		}
		args = append(args, "--mount", spec)
	}
	for target, opt := range c.HostConfig.Tmpfs {
		v := target
		if strings.TrimSpace(opt) != "" {
			v += ":" + opt
		}
		args = append(args, "--tmpfs", v)
	}
	args = appendSortedArgs(args, "--cap-add", c.HostConfig.CapAdd)
	args = appendSortedArgs(args, "--cap-drop", c.HostConfig.CapDrop)
	args = appendSortedArgs(args, "--dns", c.HostConfig.DNS)
	args = appendSortedArgs(args, "--dns-search", c.HostConfig.DNSSearch)
	args = appendSortedArgs(args, "--add-host", c.HostConfig.ExtraHosts)
	args = appendSortedArgs(args, "--security-opt", c.HostConfig.SecurityOpt)
	for _, d := range c.HostConfig.Devices {
		v := d.PathOnHost + ":" + d.PathInContainer
		if d.CgroupPermissions != "" {
			v += ":" + d.CgroupPermissions
		}
		args = append(args, "--device", v)
	}
	if c.HostConfig.ShmSize > 0 {
		args = append(args, "--shm-size", strconv.FormatInt(c.HostConfig.ShmSize, 10))
	}
	networks := stableNetworks(c)
	firstNetwork := ""
	for _, n := range networks {
		if n != "bridge" && n != "host" && n != "none" {
			firstNetwork = n
			break
		}
	}
	mode := strings.TrimSpace(c.HostConfig.NetworkMode)
	if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
		args = append(args, "--network", mode)
	} else if firstNetwork != "" {
		args = append(args, "--network", firstNetwork)
		ep := rollbackEndpoint(c.NetworkSettings.Networks[firstNetwork])
		aliases := append([]string(nil), ep.Aliases...)
		sort.Strings(aliases)
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				args = append(args, "--network-alias", alias)
			}
		}
		if ep.IPAMConfig != nil && strings.TrimSpace(ep.IPAMConfig.IPv4Address) != "" {
			args = append(args, "--ip", ep.IPAMConfig.IPv4Address)
		}
		if ep.IPAMConfig != nil && strings.TrimSpace(ep.IPAMConfig.IPv6Address) != "" {
			args = append(args, "--ip6", ep.IPAMConfig.IPv6Address)
		}
	}
	if len(c.Config.Entrypoint) > 1 {
		return nil, nil, fmt.Errorf("rollback cannot safely recreate multi-element entrypoint for %s", name)
	}
	if len(c.Config.Entrypoint) == 1 {
		args = append(args, "--entrypoint", c.Config.Entrypoint[0])
	}
	args = append(args, targetImage)
	args = append(args, c.Config.Cmd...)
	extras := []string{}
	for _, n := range networks {
		if n != firstNetwork && n != "bridge" && n != "host" && n != "none" {
			extras = append(extras, n)
		}
	}
	return args, extras, nil
}

func (a *App) restoreContainerRuntime(ctx context.Context, endpoint string, c inspectContainer, targetImage string) error {
	name := strings.TrimPrefix(c.Name, "/")
	args, extraNetworks, err := createArgsFromInspect(c, targetImage)
	if err != nil {
		return err
	}
	_, _ = a.Docker.Run(ctx, endpoint, "rm", "-f", name)
	if _, err = a.Docker.Run(ctx, endpoint, args...); err != nil {
		return err
	}
	for _, n := range extraNetworks {
		connectArgs := []string{"network", "connect"}
		ep := rollbackEndpoint(c.NetworkSettings.Networks[n])
		aliases := append([]string(nil), ep.Aliases...)
		sort.Strings(aliases)
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				connectArgs = append(connectArgs, "--alias", alias)
			}
		}
		if ep.IPAMConfig != nil && strings.TrimSpace(ep.IPAMConfig.IPv4Address) != "" {
			connectArgs = append(connectArgs, "--ip", ep.IPAMConfig.IPv4Address)
		}
		if ep.IPAMConfig != nil && strings.TrimSpace(ep.IPAMConfig.IPv6Address) != "" {
			connectArgs = append(connectArgs, "--ip6", ep.IPAMConfig.IPv6Address)
		}
		connectArgs = append(connectArgs, n, name)
		if _, e := a.Docker.Run(ctx, endpoint, connectArgs...); e != nil {
			return fmt.Errorf("connect network %s: %w", n, e)
		}
	}
	if _, err = a.Docker.Run(ctx, endpoint, "start", name); err != nil {
		return err
	}
	return nil
}

func (a *App) executeRollback(jobID int64, hist db.UpdateHistory, actor string) {
	ctx := a.ctx
	started := time.Now()
	_ = a.Store.StartJob(ctx, jobID)
	a.jobProgress(ctx, jobID, 10, "Loading recovery snapshot")
	h, err := a.Store.Host(ctx, hist.HostID)
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	path, _, err := a.findSnapshotForHistory(hist)
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	old, err := findInspectForContainer(raw, hist.ContainerName)
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	currentRaw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, hist.ContainerName)
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	var currentList []inspectContainer
	if json.Unmarshal(currentRaw, &currentList) != nil || len(currentList) == 0 {
		a.failJob(jobID, fmt.Errorf("current container inspect unavailable"))
		return
	}
	current := currentList[0]
	currentContainer, currentVersion := a.currentContainerState(ctx, hist.HostID, hist.ContainerName)
	a.jobProgress(ctx, jobID, 22, "Creating pre-rollback safety snapshot")
	if _, err = a.createSnapshotForContainer(ctx, hist.HostID, hist.ContainerName, "before-rollback"); err != nil {
		a.failJob(jobID, fmt.Errorf("rollback safety snapshot failed: %w", err))
		return
	}
	localID, immutable := snapshotRollbackImage(path, old)
	target := localID
	swarmService := strings.TrimSpace(old.Config.Labels["com.docker.swarm.service.name"])
	if swarmService != "" {
		target = immutable
		if target == "" {
			a.failJob(jobID, fmt.Errorf("Swarm rollback requires an immutable repository digest in the snapshot"))
			return
		}
	}
	if target == "" {
		a.failJob(jobID, fmt.Errorf("previous image reference missing from snapshot"))
		return
	}
	a.jobProgress(ctx, jobID, 38, "Preparing previous image")
	if swarmService == "" && !a.Docker.ImageExists(ctx, h.Endpoint, target) {
		if immutable == "" {
			a.failJob(jobID, fmt.Errorf("previous image is no longer local and no immutable repository digest was captured"))
			return
		}
		if err = a.Docker.PullRemoteImage(ctx, h.Endpoint, immutable); err != nil {
			a.failJob(jobID, fmt.Errorf("pull previous image: %w", err))
			return
		}
		target = immutable
	}
	a.jobProgress(ctx, jobID, 58, "Restoring previous image")
	if swarmService != "" {
		_, err = a.Docker.Run(ctx, h.Endpoint, "service", "update", "--image", target, "--detach=true", swarmService)
	} else {
		err = a.restoreContainerRuntime(ctx, h.Endpoint, old, target)
		if err != nil {
			// Best-effort recovery to the state that existed immediately before
			// the rollback attempt. This makes rollback failure far less likely
			// to leave the service absent.
			fallbackImage := strings.TrimSpace(current.Image)
			if fallbackImage == "" {
				fallbackImage = currentContainer.ImageID
			}
			if fallbackImage != "" {
				_ = a.restoreContainerRuntime(context.Background(), h.Endpoint, current, fallbackImage)
			}
		}
	}
	if err != nil {
		a.jobProgress(ctx, jobID, 100, "Rollback failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: hist.HostID, ContainerName: hist.ContainerName, Action: "rollback", Trigger: "manual", Actor: actor, Status: "failed", FromVersion: currentVersion.Installed, ToVersion: hist.FromVersion, FromImageRef: currentContainer.Image, ToImageRef: hist.FromImageRef, FromDigest: currentContainer.ImageID, ToDigest: hist.FromDigest, SnapshotID: hist.SnapshotID, DurationMS: time.Since(started).Milliseconds(), Error: err.Error()})
		return
	}
	a.jobProgress(ctx, jobID, 82, "Verifying restored container")
	time.Sleep(1200 * time.Millisecond)
	_, _, _ = a.check(ctx, hist.HostID, hist.ContainerName, "post-rollback")
	if err := a.captureCurrentConfigDriftBaseline(ctx, hist.HostID, hist.ContainerName, "post-rollback"); err != nil {
		_ = a.Store.AddJobLog(ctx, jobID, "WARN", "config-drift", "Could not refresh post-rollback drift baseline: "+err.Error())
		a.Logger.Warn("post-rollback config drift baseline refresh failed", "host_id", hist.HostID, "container", hist.ContainerName, "error", err)
	}
	after, afterVersion := a.currentContainerState(ctx, hist.HostID, hist.ContainerName)
	a.jobProgress(ctx, jobID, 100, "Rollback completed")
	_ = a.Store.FinishJob(ctx, jobID, "success", "", "")
	_ = a.Store.Audit(ctx, actor, "rollback.success", hist.HostID, hist.ContainerName, fmt.Sprintf("history_id=%d snapshot=%s", hist.ID, hist.SnapshotID))
	_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: hist.HostID, ContainerName: hist.ContainerName, Action: "rollback", Trigger: "manual", Actor: actor, Status: "success", FromVersion: currentVersion.Installed, ToVersion: afterVersion.Installed, FromImageRef: currentContainer.Image, ToImageRef: after.Image, FromDigest: currentContainer.ImageID, ToDigest: after.ImageID, SnapshotID: hist.SnapshotID, DurationMS: time.Since(started).Milliseconds()})
}

func (a *App) handleRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HistoryID int64 `json:"history_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.HistoryID <= 0 {
		writeErr(w, 400, "history_id is required")
		return
	}
	hist, err := a.Store.UpdateHistoryEntry(r.Context(), in.HistoryID)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if !a.hostAllowed(r, hist.HostID) {
		writeErr(w, 403, "host access denied")
		return
	}
	if hist.Action != "update" || hist.Status != "success" || hist.SnapshotID == "" {
		writeErr(w, 409, "this history entry cannot be rolled back")
		return
	}
	if _, info, findErr := a.findSnapshotForHistory(hist); findErr != nil {
		writeErr(w, 409, findErr.Error())
		return
	} else if strings.EqualFold(info.StackType, "swarm") {
		writeErr(w, 409, "one-click rollback is not enabled for Docker Swarm services; use the recovery snapshot and Swarm service rollback controls")
		return
	}
	managed, _ := a.systemManagedContainer(hist.ContainerName)
	if managed {
		writeErr(w, 403, "system-managed containers cannot be rolled back here")
		return
	}
	active, _ := a.Store.HasActiveJob(r.Context(), hist.HostID, hist.ContainerName)
	if active {
		writeErr(w, 409, "another operation is already running for this container")
		return
	}
	id, err := a.Store.CreateJob(r.Context(), "rollback", "manual", hist.HostID, hist.ContainerName, "queued")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.AddJobLog(r.Context(), id, "INFO", "app", fmt.Sprintf("rollback queued from update history #%d", hist.ID))
	a.jobProgress(r.Context(), id, 5, "Queued")
	actor := a.actor(r)
	_ = a.Store.Audit(r.Context(), actor, "rollback.queue", hist.HostID, hist.ContainerName, fmt.Sprintf("history_id=%d", hist.ID))
	go a.executeRollback(id, hist, actor)
	writeJSON(w, 202, map[string]any{"job_id": id})
}
