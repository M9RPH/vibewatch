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
	"github.com/watchtower-ui/watchtower-ui/internal/dockercli"
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

func appendSortedMapArgs(args []string, flag string, values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		args = append(args, flag, k+"="+values[k])
	}
	return args
}

func dockerLinkArg(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return strings.TrimPrefix(v, "/")
	}
	source := strings.Trim(strings.TrimSpace(parts[0]), "/")
	aliasPath := strings.Trim(strings.TrimSpace(parts[1]), "/")
	aliasParts := strings.Split(aliasPath, "/")
	alias := aliasParts[len(aliasParts)-1]
	if source == "" {
		return ""
	}
	if alias == "" || alias == source {
		return source
	}
	return source + ":" + alias
}

func deviceRequestGPUArg(driver string, count int64, ids []string, capabilities [][]string, options map[string]string) (string, error) {
	hasGPU := false
	for _, group := range capabilities {
		for _, cap := range group {
			if strings.EqualFold(strings.TrimSpace(cap), "gpu") {
				hasGPU = true
			}
		}
	}
	driver = strings.ToLower(strings.TrimSpace(driver))
	if !hasGPU || (driver != "" && driver != "nvidia") {
		return "", fmt.Errorf("rollback cannot safely recreate unsupported Docker device request driver=%q capabilities=%v", driver, capabilities)
	}
	if len(options) > 0 {
		return "", fmt.Errorf("rollback cannot safely recreate GPU device request options %v", options)
	}
	if len(ids) > 0 {
		xs := append([]string(nil), ids...)
		sort.Strings(xs)
		return "device=" + strings.Join(xs, ","), nil
	}
	if count > 0 {
		return strconv.FormatInt(count, 10), nil
	}
	return "all", nil
}

func createArgsFromInspect(c inspectContainer, targetImage string) ([]string, []string, error) {
	name := strings.TrimPrefix(c.Name, "/")
	if name == "" {
		return nil, nil, fmt.Errorf("snapshot container name is empty")
	}
	args := []string{"create", "--name", name}
	containerNetworkMode := strings.HasPrefix(strings.TrimSpace(c.HostConfig.NetworkMode), "container:")
	if c.Config.Hostname != "" && !containerNetworkMode {
		args = append(args, "--hostname", c.Config.Hostname)
	}
	if c.Config.Domainname != "" && !containerNetworkMode {
		args = append(args, "--domainname", c.Config.Domainname)
	}
	if c.Config.User != "" {
		args = append(args, "--user", c.Config.User)
	}
	if c.Config.WorkingDir != "" {
		args = append(args, "--workdir", c.Config.WorkingDir)
	}
	if c.Config.Tty {
		args = append(args, "--tty")
	}
	if c.Config.OpenStdin {
		args = append(args, "--interactive")
	}
	if strings.TrimSpace(c.Config.StopSignal) != "" {
		args = append(args, "--stop-signal", c.Config.StopSignal)
	}
	if c.Config.StopTimeout != nil && *c.Config.StopTimeout >= 0 {
		args = append(args, "--stop-timeout", strconv.Itoa(*c.Config.StopTimeout))
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
	if c.HostConfig.AutoRemove {
		args = append(args, "--rm")
	}
	if c.HostConfig.Init != nil && *c.HostConfig.Init {
		args = append(args, "--init")
	}
	for _, pair := range []struct {
		flag  string
		value string
	}{
		{"--ipc", c.HostConfig.IpcMode},
		{"--pid", c.HostConfig.PidMode},
		{"--uts", c.HostConfig.UTSMode},
		{"--cgroupns", c.HostConfig.CgroupnsMode},
		{"--userns", c.HostConfig.UsernsMode},
		{"--cgroup-parent", c.HostConfig.CgroupParent},
	} {
		if strings.TrimSpace(pair.value) != "" {
			args = append(args, pair.flag, pair.value)
		}
	}
	if runtime := strings.TrimSpace(c.HostConfig.Runtime); runtime != "" && runtime != "runc" {
		args = append(args, "--runtime", runtime)
	}
	if c.HostConfig.Memory > 0 {
		args = append(args, "--memory", strconv.FormatInt(c.HostConfig.Memory, 10))
	}
	if c.HostConfig.MemoryReservation > 0 {
		args = append(args, "--memory-reservation", strconv.FormatInt(c.HostConfig.MemoryReservation, 10))
	}
	if c.HostConfig.MemorySwap != 0 {
		args = append(args, "--memory-swap", strconv.FormatInt(c.HostConfig.MemorySwap, 10))
	}
	if c.HostConfig.NanoCpus > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(float64(c.HostConfig.NanoCpus)/1_000_000_000, 'f', -1, 64))
	}
	if c.HostConfig.CpuShares > 0 {
		args = append(args, "--cpu-shares", strconv.FormatInt(c.HostConfig.CpuShares, 10))
	}
	if strings.TrimSpace(c.HostConfig.CpusetCpus) != "" {
		args = append(args, "--cpuset-cpus", c.HostConfig.CpusetCpus)
	}
	if strings.TrimSpace(c.HostConfig.CpusetMems) != "" {
		args = append(args, "--cpuset-mems", c.HostConfig.CpusetMems)
	}
	if c.HostConfig.PidsLimit != nil {
		args = append(args, "--pids-limit", strconv.FormatInt(*c.HostConfig.PidsLimit, 10))
	}
	if c.HostConfig.OomKillDisable != nil && *c.HostConfig.OomKillDisable {
		args = append(args, "--oom-kill-disable")
	}
	if c.HostConfig.OomScoreAdj != 0 {
		args = append(args, "--oom-score-adj", strconv.Itoa(c.HostConfig.OomScoreAdj))
	}
	for _, v := range c.Config.Env {
		args = append(args, "--env", v)
	}
	labels := make([]string, 0, len(c.Config.Labels))
	for k, v := range c.Config.Labels {
		labels = append(labels, k+"="+v)
	}
	args = appendSortedArgs(args, "--label", labels)
	if !containerNetworkMode {
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
		if c.HostConfig.PublishAllPorts {
			args = append(args, "--publish-all")
		}
	}
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
	args = appendSortedArgs(args, "--group-add", c.HostConfig.GroupAdd)
	for _, link := range c.HostConfig.Links {
		if v := dockerLinkArg(link); v != "" {
			args = append(args, "--link", v)
		}
	}
	args = appendSortedArgs(args, "--volumes-from", c.HostConfig.VolumesFrom)
	if !containerNetworkMode {
		args = appendSortedArgs(args, "--dns", c.HostConfig.DNS)
		args = appendSortedArgs(args, "--dns-search", c.HostConfig.DNSSearch)
		args = appendSortedArgs(args, "--dns-option", c.HostConfig.DNSOptions)
		args = appendSortedArgs(args, "--add-host", c.HostConfig.ExtraHosts)
	}
	args = appendSortedArgs(args, "--security-opt", c.HostConfig.SecurityOpt)
	args = appendSortedMapArgs(args, "--sysctl", c.HostConfig.Sysctls)
	if typ := strings.TrimSpace(c.HostConfig.LogConfig.Type); typ != "" {
		args = append(args, "--log-driver", typ)
		args = appendSortedMapArgs(args, "--log-opt", c.HostConfig.LogConfig.Config)
	}
	ulimits := append([]struct {
		Name string `json:"Name"`
		Hard int64  `json:"Hard"`
		Soft int64  `json:"Soft"`
	}(nil), c.HostConfig.Ulimits...)
	sort.Slice(ulimits, func(i, j int) bool { return ulimits[i].Name < ulimits[j].Name })
	for _, u := range ulimits {
		if strings.TrimSpace(u.Name) != "" {
			args = append(args, "--ulimit", fmt.Sprintf("%s=%d:%d", u.Name, u.Soft, u.Hard))
		}
	}
	if len(c.HostConfig.DeviceRequests) > 1 {
		return nil, nil, fmt.Errorf("rollback cannot safely recreate %d Docker device requests", len(c.HostConfig.DeviceRequests))
	}
	for _, request := range c.HostConfig.DeviceRequests {
		gpu, e := deviceRequestGPUArg(request.Driver, request.Count, request.DeviceIDs, request.Capabilities, request.Options)
		if e != nil {
			return nil, nil, e
		}
		args = append(args, "--gpus", gpu)
	}
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
	cmd := append([]string(nil), c.Config.Cmd...)
	if len(c.Config.Entrypoint) >= 1 {
		args = append(args, "--entrypoint", c.Config.Entrypoint[0])
		// docker create exposes --entrypoint as one executable string. Preserve
		// effective argv for a multi-element Docker Entrypoint by moving the
		// remaining entrypoint elements in front of Cmd.
		if len(c.Config.Entrypoint) > 1 {
			cmd = append(append([]string(nil), c.Config.Entrypoint[1:]...), cmd...)
		}
	}
	args = append(args, targetImage)
	args = append(args, cmd...)
	extras := []string{}
	for _, n := range networks {
		if n != firstNetwork && n != "bridge" && n != "host" && n != "none" {
			extras = append(extras, n)
		}
	}
	return args, extras, nil
}

func (a *App) prepareRuntimeRestoreRef(ctx context.Context, endpoint, sourceImage, originalRef string) string {
	sourceImage = strings.TrimSpace(sourceImage)
	originalRef = strings.TrimSpace(originalRef)
	if sourceImage == "" || originalRef == "" || strings.HasPrefix(originalRef, "sha256:") || strings.Contains(originalRef, "@sha256:") {
		return sourceImage
	}
	if _, err := a.Docker.Run(ctx, endpoint, "image", "tag", sourceImage, originalRef); err == nil {
		return originalRef
	}
	return sourceImage
}

func (a *App) recreateContainerRuntime(ctx context.Context, endpoint string, c inspectContainer, targetImage string, start bool, networkModeOverride string) error {
	name := strings.TrimPrefix(c.Name, "/")
	createSpec := c
	if strings.TrimSpace(networkModeOverride) != "" {
		createSpec.HostConfig.NetworkMode = strings.TrimSpace(networkModeOverride)
		// A container that joins another container's namespace cannot also be
		// connected to ordinary Docker networks. Clear stale inspect network
		// attachments so the recreation is driven solely by --network container:.
		if strings.HasPrefix(createSpec.HostConfig.NetworkMode, "container:") {
			createSpec.NetworkSettings.Networks = map[string]json.RawMessage{}
		}
	}
	args, extraNetworks, err := createArgsFromInspect(createSpec, targetImage)
	if err != nil {
		return err
	}
	_, _ = a.Docker.Run(ctx, endpoint, "rm", "-f", name)
	if _, err = a.Docker.Run(ctx, endpoint, args...); err != nil {
		return err
	}
	for _, n := range extraNetworks {
		connectArgs := []string{"network", "connect"}
		ep := rollbackEndpoint(createSpec.NetworkSettings.Networks[n])
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
	if start {
		if _, err = a.Docker.Run(ctx, endpoint, "start", name); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) restoreContainerRuntime(ctx context.Context, endpoint string, c inspectContainer, targetImage string) error {
	return a.recreateContainerRuntime(ctx, endpoint, c, targetImage, true, "")
}

func (a *App) executeRollback(jobID int64, hist db.UpdateHistory, actor string) {
	ctx := a.ctx
	started := time.Now()
	if !a.beginAsyncJob(ctx, jobID) {
		return
	}
	leaseKey, leaseOwner, leaseErr := a.acquireOperationLease(ctx, jobID, hist.HostID, hist.ContainerName, "rollback-legacy")
	if leaseErr != nil {
		a.failJob(jobID, leaseErr)
		return
	}
	stopHB := a.startLeaseHeartbeat(ctx, leaseKey, leaseOwner, 0)
	defer stopHB()
	defer a.Store.ReleaseOperationLease(context.Background(), leaseKey, leaseOwner)
	a.jobProgress(ctx, jobID, 10, "Loading recovery snapshot")
	h, err := a.Store.Host(ctx, hist.HostID)
	if err != nil {
		a.failJob(jobID, err)
		return
	}
	opCtx, opCancel := context.WithTimeout(a.ctx, dockerOperationTimeout(h.Endpoint, 20*time.Minute, 35*time.Minute))
	defer opCancel()
	ctx = opCtx
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
		target = a.prepareRuntimeRestoreRef(ctx, h.Endpoint, target, old.Config.Image)
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
				fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 8*time.Minute, 15*time.Minute))
				_ = a.restoreContainerRuntime(fallbackCtx, h.Endpoint, current, fallbackImage)
				fallbackCancel()
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
	if snoozed := a.snoozeLatestAfterRollback(ctx, hist.HostID, hist.ContainerName, hist.ToDigest); snoozed != "" {
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "rollback", "Update digest snoozed after rollback: "+snoozed)
	}
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

func (a *App) executeRestorePointRollback(jobID int64, rp db.RestorePoint, actor, trigger string) error {
	ctx := a.ctx
	started := time.Now()
	if !a.beginAsyncJob(ctx, jobID) {
		return fmt.Errorf("rollback job was cancelled before execution")
	}
	if trigger != "automatic" {
		leaseKey, leaseOwner, leaseErr := a.acquireOperationLease(ctx, jobID, rp.HostID, rp.ContainerName, "rollback")
		if leaseErr != nil {
			a.failJob(jobID, leaseErr)
			return leaseErr
		}
		stopHB := a.startLeaseHeartbeat(ctx, leaseKey, leaseOwner, 0)
		defer stopHB()
		defer a.Store.ReleaseOperationLease(context.Background(), leaseKey, leaseOwner)
	}
	a.jobProgress(ctx, jobID, 8, "Validating restore point integrity")
	integrityCtx, integrityCancel := context.WithTimeout(ctx, 3*time.Minute)
	integrity := a.validateRestorePointIntegrity(integrityCtx, rp)
	integrityCancel()
	if integrity.Status == "expired" || integrity.Status == "degraded" {
		err := fmt.Errorf("restore point integrity is %s; rollback blocked before destructive work", integrity.Status)
		a.failJob(jobID, err)
		return err
	}
	a.jobProgress(ctx, jobID, 10, "Loading restore point")
	h, err := a.Store.Host(ctx, rp.HostID)
	if err != nil {
		a.failJob(jobID, err)
		return err
	}
	opCtx, opCancel := context.WithTimeout(a.ctx, dockerOperationTimeout(h.Endpoint, 20*time.Minute, 35*time.Minute))
	defer opCancel()
	ctx = opCtx
	path, _, err := a.findSnapshotByID(rp.HostID, rp.SnapshotID, rp.ContainerName)
	if err != nil {
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "expired", err.Error())
		a.failJob(jobID, err)
		return err
	}
	if !bool(rp.WritableLayer) || strings.TrimSpace(rp.ImageRef) == "" {
		err = fmt.Errorf("restore point does not contain a writable-layer container image")
		a.failJob(jobID, err)
		return err
	}
	if !a.Docker.ImageExists(ctx, h.Endpoint, rp.ImageRef) {
		err = fmt.Errorf("restore image is no longer available on Docker host")
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
		a.failJob(jobID, err)
		return err
	}
	raw, err := snapshotZipEntry(path, "container-inspect.json")
	if err != nil {
		a.failJob(jobID, err)
		return err
	}
	old, err := findInspectForContainer(raw, rp.ContainerName)
	if err != nil {
		a.failJob(jobID, err)
		return err
	}
	if strings.TrimSpace(old.Config.Labels["com.docker.swarm.service.name"]) != "" || strings.EqualFold(rp.StackType, "swarm") {
		err = fmt.Errorf("full restore points are not enabled for Docker Swarm services")
		a.failJob(jobID, err)
		return err
	}

	rollbackDeps, depErr := a.persistedDependencyRuntimes(rp)
	if depErr != nil {
		err = fmt.Errorf("rollback dependency context is incomplete: %w", depErr)
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
		a.failJob(jobID, err)
		return err
	}

	// Re-scan the live parent before any destructive rollback step. This catches
	// namespace dependents added after the restore point was created. Retained
	// pre-update configs win for original dependents; current-only dependents are
	// preserved exactly as they are now and rebound to the restored parent.
	currentDependencyCtx := []networkNamespaceDependencyRuntime{}
	if _, liveDeps, scanErr := a.discoverNetworkNamespaceDependents(ctx, rp.HostID, rp.ContainerName); scanErr == nil {
		currentDependencyCtx = liveDeps
	} else if _, inspectErr := a.inspectOne(ctx, rp.HostID, rp.ContainerName); inspectErr == nil {
		err = fmt.Errorf("rollback blocked because current network namespace dependencies could not be scanned safely: %w", scanErr)
		a.failJob(jobID, err)
		return err
	} else {
		_ = a.Store.AddJobLog(ctx, jobID, "WARN", "dependency", "Current parent container is unavailable; using retained dependency transaction only")
	}
	rollbackExecutionDeps := mergeDependencyRuntimes(rollbackDeps, currentDependencyCtx)
	if len(rollbackExecutionDeps) > 0 {
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "dependency", fmt.Sprintf("Rollback transaction includes %d network namespace dependent(s): %s", len(rollbackExecutionDeps), dependencyNames(rollbackExecutionDeps)))
	}

	// Capture the state immediately before rollback as an in-memory config plus a
	// temporary committed image. This is not retained as another restore point;
	// it exists only so a failed rollback can put the current container back.
	var current inspectContainer
	var currentContainer dockercli.Container
	var currentVersion db.VersionInfo
	safetyRef := ""
	if cur, inspectErr := a.inspectOne(ctx, rp.HostID, rp.ContainerName); inspectErr == nil {
		current = cur
		currentContainer, currentVersion = a.currentContainerState(ctx, rp.HostID, rp.ContainerName)
		a.jobProgress(ctx, jobID, 20, "Creating rollback safety image")
		safetyRef = restoreImageRef(rp.HostID, rp.ContainerName, "safety-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
		safetyCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		_, safetyErr := a.Docker.Run(safetyCtx, h.Endpoint, "commit", "--pause=true", rp.ContainerName, safetyRef)
		cancel()
		if safetyErr != nil {
			safetyRef = ""
			_ = a.Store.AddJobLog(ctx, jobID, "WARN", "rollback", "Temporary safety image could not be created: "+safetyErr.Error())
		}
	} else {
		currentContainer, currentVersion = a.currentContainerState(ctx, rp.HostID, rp.ContainerName)
		_ = a.Store.AddJobLog(ctx, jobID, "WARN", "rollback", "Current container is missing or cannot be inspected; restoring directly from restore point")
	}
	if safetyRef != "" {
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 2*time.Minute, 5*time.Minute))
			defer cleanupCancel()
			_, _ = a.Docker.Run(cleanupCtx, h.Endpoint, "image", "rm", safetyRef)
		}()
	}
	recoverSafety := func() error {
		if safetyRef == "" || strings.TrimSpace(current.Name) == "" {
			return fmt.Errorf("no pre-rollback safety image is available")
		}
		// Any dependent rebound during a partial rollback must be stopped before
		// the safety parent is recreated, otherwise it would again retain a stale
		// network namespace reference.
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), dockerOperationTimeout(h.Endpoint, 10*time.Minute, 20*time.Minute))
		defer recoveryCancel()
		stopDependentsBestEffort(recoveryCtx, a, h.Endpoint, rollbackExecutionDeps)
		fallback := a.prepareRuntimeRestoreRef(recoveryCtx, h.Endpoint, safetyRef, current.Config.Image)
		if fallbackErr := a.restoreContainerRuntime(recoveryCtx, h.Endpoint, current, fallback); fallbackErr != nil {
			return fallbackErr
		}
		if len(currentDependencyCtx) > 0 {
			parent, inspectErr := a.inspectOne(recoveryCtx, rp.HostID, rp.ContainerName)
			if inspectErr != nil {
				return fmt.Errorf("safety parent restored but could not be inspected for dependent rebinding: %w", inspectErr)
			}
			if depRestoreErr := a.recreateNetworkNamespaceDependents(recoveryCtx, jobID, rp.HostID, rp.ContainerName, parent.ID, currentDependencyCtx); depRestoreErr != nil {
				return fmt.Errorf("safety parent restored but dependent rebinding failed: %w", depRestoreErr)
			}
		}
		return nil
	}

	if len(rollbackExecutionDeps) > 0 {
		a.jobProgress(ctx, jobID, 38, "Stopping network namespace dependents")
		stopDependentsBestEffort(ctx, a, h.Endpoint, rollbackExecutionDeps)
	}

	a.jobProgress(ctx, jobID, 48, "Restoring container filesystem and configuration")
	target := a.prepareRuntimeRestoreRef(ctx, h.Endpoint, rp.ImageRef, old.Config.Image)
	err = a.restoreContainerRuntime(ctx, h.Endpoint, old, target)
	if err != nil && safetyRef != "" && strings.TrimSpace(current.Name) != "" {
		_ = a.Store.AddJobLog(ctx, jobID, "WARN", "rollback", "Restore failed; attempting safety recovery of the pre-rollback container and its namespace dependents")
		originalErr := err
		if fallbackErr := recoverSafety(); fallbackErr != nil {
			err = fmt.Errorf("restore failed: %v; safety recovery also failed: %v", originalErr, fallbackErr)
		} else {
			err = fmt.Errorf("restore failed: %v; pre-rollback runtime was recovered", originalErr)
		}
	}
	if err != nil {
		a.jobProgress(ctx, jobID, 100, "Rollback failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
		_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: rp.HostID, ContainerName: rp.ContainerName, Action: "rollback", Trigger: trigger, Actor: actor, Status: "failed", FromVersion: currentVersion.Installed, ToVersion: rp.FromVersion, FromImageRef: currentContainer.Image, ToImageRef: rp.OriginalImageRef, FromDigest: currentContainer.ImageID, ToDigest: rp.OriginalImageID, SnapshotID: rp.SnapshotID, RestorePointID: rp.ID, DurationMS: time.Since(started).Milliseconds(), Error: err.Error(), DependencyCount: len(rollbackExecutionDeps), DependencyStatus: func() string {
			if len(rollbackExecutionDeps) > 0 {
				return "not_restored"
			}
			return "none"
		}(), DependencyDetails: dependencyNames(rollbackExecutionDeps)})
		return err
	}

	a.jobProgress(ctx, jobID, 72, "Verifying restored parent container")
	if verifyErr := a.verifyUpdatedContainer(ctx, rp.HostID, rp.ContainerName); verifyErr != nil {
		err = fmt.Errorf("restored container verification failed: %w", verifyErr)
		if safetyRef != "" && strings.TrimSpace(current.Name) != "" {
			_ = a.Store.AddJobLog(ctx, jobID, "WARN", "rollback", "Restored parent failed verification; attempting pre-rollback safety recovery")
			if fallbackErr := recoverSafety(); fallbackErr != nil {
				err = fmt.Errorf("%v; safety recovery also failed: %v", err, fallbackErr)
			} else {
				err = fmt.Errorf("%v; pre-rollback runtime was recovered", err)
			}
		}
		a.jobProgress(ctx, jobID, 100, "Rollback verification failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
		return err
	}
	if len(rollbackExecutionDeps) > 0 {
		parentAfter, inspectErr := a.inspectOne(ctx, rp.HostID, rp.ContainerName)
		if inspectErr != nil {
			err = fmt.Errorf("restored parent could not be inspected for dependency recreation: %w", inspectErr)
			a.jobProgress(ctx, jobID, 100, "Rollback dependency restore failed")
			_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
			_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
			return err
		}
		if depRestoreErr := a.recreateNetworkNamespaceDependents(ctx, jobID, rp.HostID, rp.ContainerName, parentAfter.ID, rollbackExecutionDeps); depRestoreErr != nil {
			err = fmt.Errorf("parent rollback completed but dependent recreation failed: %w", depRestoreErr)
			if safetyRef != "" && strings.TrimSpace(current.Name) != "" {
				_ = a.Store.AddJobLog(ctx, jobID, "WARN", "rollback", "Dependent restore failed; attempting recovery of the complete pre-rollback runtime")
				if fallbackErr := recoverSafety(); fallbackErr != nil {
					err = fmt.Errorf("%v; safety recovery also failed: %v", err, fallbackErr)
				} else {
					err = fmt.Errorf("%v; pre-rollback runtime was recovered", err)
				}
			}
			a.jobProgress(ctx, jobID, 100, "Rollback dependency restore failed")
			_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
			_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
			_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: rp.HostID, ContainerName: rp.ContainerName, Action: "rollback", Trigger: trigger, Actor: actor, Status: "failed", FromVersion: currentVersion.Installed, ToVersion: rp.FromVersion, FromImageRef: currentContainer.Image, ToImageRef: rp.OriginalImageRef, FromDigest: currentContainer.ImageID, ToDigest: rp.OriginalImageID, SnapshotID: rp.SnapshotID, RestorePointID: rp.ID, DurationMS: time.Since(started).Milliseconds(), Error: err.Error(), DependencyCount: len(rollbackExecutionDeps), DependencyStatus: "failed", DependencyDetails: dependencyNames(rollbackExecutionDeps)})
			return err
		}
	}
	rollbackVerification := a.runCustomVerification(ctx, rp.HostID, rp.ContainerName, trigger, actor, jobID)
	rollbackVerificationJSON := "[]"
	if bs, e := json.Marshal(rollbackVerification); e == nil {
		rollbackVerificationJSON = string(bs)
	}
	if rollbackVerification.Status == verificationStatusFailed {
		err = fmt.Errorf("post-rollback custom verification failed: %s", rollbackVerification.Error)
		a.jobProgress(ctx, jobID, 100, "Rollback verification failed")
		_ = a.Store.FinishJob(ctx, jobID, "failed", "", err.Error())
		_ = a.Store.SetRestorePointStatus(context.Background(), rp.ID, "degraded", err.Error())
		_ = a.Store.Audit(ctx, actor, "rollback.verification.failed", rp.HostID, rp.ContainerName, err.Error())
		_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: rp.HostID, ContainerName: rp.ContainerName, Action: "rollback", Trigger: trigger, Actor: actor, Status: "failed", FromVersion: currentVersion.Installed, ToVersion: rp.FromVersion, FromImageRef: currentContainer.Image, ToImageRef: rp.OriginalImageRef, FromDigest: currentContainer.ImageID, ToDigest: rp.OriginalImageID, SnapshotID: rp.SnapshotID, RestorePointID: rp.ID, DurationMS: time.Since(started).Milliseconds(), Error: err.Error(), DependencyCount: len(rollbackExecutionDeps), DependencyStatus: func() string {
			if len(rollbackExecutionDeps) > 0 {
				return "success"
			}
			return "none"
		}(), DependencyDetails: dependencyNames(rollbackExecutionDeps), VerificationStatus: rollbackVerification.Status, VerificationDetails: rollbackVerificationJSON})
		return err
	}
	_, _, _ = a.check(ctx, rp.HostID, rp.ContainerName, "post-rollback")
	if snoozed := a.snoozeLatestAfterRollback(ctx, rp.HostID, rp.ContainerName, rp.TargetDigest); snoozed != "" {
		_ = a.Store.AddJobLog(ctx, jobID, "INFO", "rollback", "Update digest snoozed after rollback: "+snoozed)
	}
	if err := a.captureCurrentConfigDriftBaseline(ctx, rp.HostID, rp.ContainerName, "post-rollback"); err != nil {
		_ = a.Store.AddJobLog(ctx, jobID, "WARN", "config-drift", "Could not refresh post-rollback drift baseline: "+err.Error())
	}
	after, afterVersion := a.currentContainerState(ctx, rp.HostID, rp.ContainerName)
	_ = a.Store.MarkRestorePointRestored(ctx, rp.ID, "")
	a.jobProgress(ctx, jobID, 100, "Rollback completed")
	_ = a.Store.FinishJob(ctx, jobID, "success", "", "")
	_ = a.Store.Audit(ctx, actor, "rollback.success", rp.HostID, rp.ContainerName, fmt.Sprintf("restore_point=%d snapshot=%s trigger=%s", rp.ID, rp.SnapshotID, trigger))
	_, _ = a.Store.AddUpdateHistory(ctx, db.UpdateHistory{HostID: rp.HostID, ContainerName: rp.ContainerName, Action: "rollback", Trigger: trigger, Actor: actor, Status: "success", FromVersion: currentVersion.Installed, ToVersion: afterVersion.Installed, FromImageRef: currentContainer.Image, ToImageRef: after.Image, FromDigest: currentContainer.ImageID, ToDigest: after.ImageID, SnapshotID: rp.SnapshotID, RestorePointID: rp.ID, DurationMS: time.Since(started).Milliseconds(), DependencyCount: len(rollbackExecutionDeps), DependencyStatus: func() string {
		if len(rollbackExecutionDeps) > 0 {
			return "success"
		}
		return "none"
	}(), DependencyDetails: dependencyNames(rollbackExecutionDeps), VerificationStatus: rollbackVerification.Status, VerificationDetails: rollbackVerificationJSON})
	return nil
}

func (a *App) handleRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HistoryID      int64 `json:"history_id"`
		RestorePointID int64 `json:"restore_point_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.HistoryID <= 0 && in.RestorePointID <= 0) {
		writeErr(w, 400, "history_id or restore_point_id is required")
		return
	}

	var rp db.RestorePoint
	var hist db.UpdateHistory
	var err error
	if in.RestorePointID > 0 {
		rp, err = a.Store.RestorePoint(r.Context(), in.RestorePointID)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
	} else {
		hist, err = a.Store.UpdateHistoryEntry(r.Context(), in.HistoryID)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		if hist.Action != "update" || hist.Status != "success" || hist.SnapshotID == "" {
			writeErr(w, 409, "this history entry cannot be rolled back")
			return
		}
		if hist.RestorePointID > 0 {
			rp, err = a.Store.RestorePoint(r.Context(), hist.RestorePointID)
			if err != nil {
				writeErr(w, 409, "linked full restore point is unavailable: "+err.Error())
				return
			}
		}
	}

	if rp.ID > 0 {
		if !a.hostAllowed(r, rp.HostID) {
			writeErr(w, 403, "host access denied")
			return
		}
		if strings.EqualFold(rp.StackType, "swarm") || !bool(rp.WritableLayer) {
			writeErr(w, 409, "this restore point is configuration-only; one-click full rollback is not available")
			return
		}
		available, _ := a.restorePointAvailable(r.Context(), rp)
		if !available {
			writeErr(w, 409, "restore point is degraded, expired, or its writable-layer image is unavailable")
			return
		}
		managed, _ := a.systemManagedContainer(rp.ContainerName)
		if managed {
			writeErr(w, 403, "system-managed containers cannot be rolled back here")
			return
		}
		if chainID, reserved := a.chainReservation(rp.HostID, rp.ContainerName); reserved {
			writeErr(w, 409, fmt.Sprintf("container is reserved by active update chain #%d", chainID))
			return
		}
		active, _ := a.Store.HasActiveJob(r.Context(), rp.HostID, rp.ContainerName)
		if active {
			writeErr(w, 409, "another operation is already running for this container")
			return
		}
		id, err := a.Store.CreateJob(r.Context(), "rollback", "manual", rp.HostID, rp.ContainerName, "queued")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = a.Store.AddJobLog(r.Context(), id, "INFO", "app", fmt.Sprintf("full rollback queued from restore point #%d", rp.ID))
		a.jobProgress(r.Context(), id, 5, "Queued")
		actor := a.actor(r)
		_ = a.Store.Audit(r.Context(), actor, "rollback.queue", rp.HostID, rp.ContainerName, fmt.Sprintf("restore_point=%d snapshot=%s", rp.ID, rp.SnapshotID))
		go a.executeRestorePointRollback(id, rp, actor, "manual")
		writeJSON(w, 202, map[string]any{"job_id": id, "restore_point_id": rp.ID})
		return
	}

	// Compatibility path for update history created before full restore points
	// existed. This restores configuration plus the original image only.
	if !a.hostAllowed(r, hist.HostID) {
		writeErr(w, 403, "host access denied")
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
	if chainID, reserved := a.chainReservation(hist.HostID, hist.ContainerName); reserved {
		writeErr(w, 409, fmt.Sprintf("container is reserved by active update chain #%d", chainID))
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
	_ = a.Store.AddJobLog(r.Context(), id, "INFO", "app", fmt.Sprintf("legacy config/image rollback queued from update history #%d", hist.ID))
	a.jobProgress(r.Context(), id, 5, "Queued")
	actor := a.actor(r)
	_ = a.Store.Audit(r.Context(), actor, "rollback.queue", hist.HostID, hist.ContainerName, fmt.Sprintf("history_id=%d", hist.ID))
	go a.executeRollback(id, hist, actor)
	writeJSON(w, 202, map[string]any{"job_id": id})
}
