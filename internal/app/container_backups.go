package app

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

const defaultContainerSnapshotRetention = 3

type ContainerBackupSnapshot struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	CreatedAt string `json:"created_at"`
	Reason    string `json:"reason"`
	SizeBytes int64  `json:"size_bytes"`
}

type ContainerBackupUnit struct {
	HostID         int64                     `json:"host_id"`
	HostName       string                    `json:"host_name"`
	Kind           string                    `json:"kind"` // stack or service
	Key            string                    `json:"key"`
	Name           string                    `json:"name"`
	StackType      string                    `json:"stack_type,omitempty"`
	ContainerCount int                       `json:"container_count"`
	Containers     []string                  `json:"containers"`
	Live           bool                      `json:"live"`
	Snapshots      []ContainerBackupSnapshot `json:"snapshots"`
}

type snapshotInfo struct {
	SchemaVersion        int      `json:"schema_version"`
	VibewatchVersion     string   `json:"vibewatch_version"`
	CreatedAt            string   `json:"created_at"`
	Reason               string   `json:"reason"`
	HostID               int64    `json:"host_id"`
	HostName             string   `json:"host_name"`
	DockerEndpoint       string   `json:"docker_endpoint"`
	UnitKind             string   `json:"unit_kind"`
	UnitKey              string   `json:"unit_key"`
	UnitName             string   `json:"unit_name"`
	StackType            string   `json:"stack_type,omitempty"`
	Containers           []string `json:"containers"`
	Source               string   `json:"source"`
	ReconstructedCompose bool     `json:"reconstructed_compose"`
	ContainsSecrets      bool     `json:"contains_runtime_environment"`
	Note                 string   `json:"note"`
}

type inspectContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Hostname    string            `json:"Hostname"`
		Domainname  string            `json:"Domainname"`
		User        string            `json:"User"`
		Env         []string          `json:"Env"`
		Cmd         []string          `json:"Cmd"`
		Entrypoint  []string          `json:"Entrypoint"`
		Image       string            `json:"Image"`
		Labels      map[string]string `json:"Labels"`
		WorkingDir  string            `json:"WorkingDir"`
		Healthcheck *struct {
			Test        []string `json:"Test"`
			Interval    int64    `json:"Interval"`
			Timeout     int64    `json:"Timeout"`
			StartPeriod int64    `json:"StartPeriod"`
			Retries     int      `json:"Retries"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		Privileged     bool              `json:"Privileged"`
		ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
		NetworkMode    string            `json:"NetworkMode"`
		CapAdd         []string          `json:"CapAdd"`
		CapDrop        []string          `json:"CapDrop"`
		DNS            []string          `json:"Dns"`
		DNSSearch      []string          `json:"DnsSearch"`
		ExtraHosts     []string          `json:"ExtraHosts"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		ShmSize        int64             `json:"ShmSize"`
		Devices        []struct {
			PathOnHost        string `json:"PathOnHost"`
			PathInContainer   string `json:"PathInContainer"`
			CgroupPermissions string `json:"CgroupPermissions"`
		} `json:"Devices"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Propagation string `json:"Propagation"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	Image string `json:"Image"`
}

func (a *App) containerBackupRoot() string {
	return filepath.Join(a.Cfg.DataDir, "backups", "containers")
}

func (a *App) containerSnapshotRetention(ctx context.Context) int {
	if a == nil || a.Store == nil {
		return defaultContainerSnapshotRetention
	}
	v, err := strconv.Atoi(strings.TrimSpace(a.Store.Setting(ctx, "container_snapshot_retention", strconv.Itoa(defaultContainerSnapshotRetention))))
	if err != nil || v < 1 {
		return defaultContainerSnapshotRetention
	}
	if v > 20 {
		return 20
	}
	return v
}

func backupUnitID(hostID int64, kind, key string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + key))
	safeKey := sanitizeFilename(key)
	if len(safeKey) > 96 {
		safeKey = safeKey[:96]
	}
	return fmt.Sprintf("host-%d/%s-%s-%s", hostID, sanitizeFilename(kind), safeKey, hex.EncodeToString(h[:4]))
}

func (a *App) backupUnitDir(hostID int64, kind, key string) string {
	return filepath.Join(a.containerBackupRoot(), filepath.FromSlash(backupUnitID(hostID, kind, key)))
}

func backupUnitFromContainer(c dockercli.Container) (kind, key, name, stackType string) {
	if strings.TrimSpace(c.StackName) != "" {
		return "stack", c.StackName, c.StackName, c.StackType
	}
	return "service", c.Name, c.Name, ""
}

func (a *App) discoverContainerBackupUnits(ctx context.Context) ([]ContainerBackupUnit, error) {
	hosts, err := a.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	units := map[string]*ContainerBackupUnit{}
	for _, h := range hosts {
		if !bool(h.Enabled) {
			continue
		}
		cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
		if err != nil {
			a.Logger.Warn("container backup discovery skipped unreachable host", "host_id", h.ID, "host", h.Name, "error", err)
			continue
		}
		for _, c := range cs {
			if managed, _ := a.systemManagedContainer(c.Name); managed {
				continue
			}
			kind, key, name, stackType := backupUnitFromContainer(c)
			id := fmt.Sprintf("%d:%s:%s", h.ID, kind, key)
			u := units[id]
			if u == nil {
				u = &ContainerBackupUnit{HostID: h.ID, HostName: h.Name, Kind: kind, Key: key, Name: name, StackType: stackType, Live: true, Containers: []string{}, Snapshots: []ContainerBackupSnapshot{}}
				units[id] = u
			}
			if !containsString(u.Containers, c.Name) {
				u.Containers = append(u.Containers, c.Name)
			}
		}
	}
	// Also surface archived snapshots for services/stacks that no longer exist.
	archived, _ := a.scanArchivedBackupUnits()
	for _, au := range archived {
		id := fmt.Sprintf("%d:%s:%s", au.HostID, au.Kind, au.Key)
		if live := units[id]; live != nil {
			live.Snapshots = au.Snapshots
		} else {
			copy := au
			units[id] = &copy
		}
	}
	out := make([]ContainerBackupUnit, 0, len(units))
	for _, u := range units {
		sort.Strings(u.Containers)
		u.ContainerCount = len(u.Containers)
		if u.Snapshots == nil {
			u.Snapshots = a.listSnapshotsForUnit(u.HostID, u.Kind, u.Key)
		}
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (a *App) scanArchivedBackupUnits() ([]ContainerBackupUnit, error) {
	root := a.containerBackupRoot()
	entries := map[string]*ContainerBackupUnit{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".zip") {
			return nil
		}
		info, snap, e := readSnapshotInfo(path)
		if e != nil {
			return nil
		}
		id := fmt.Sprintf("%d:%s:%s", info.HostID, info.UnitKind, info.UnitKey)
		u := entries[id]
		if u == nil {
			u = &ContainerBackupUnit{HostID: info.HostID, HostName: info.HostName, Kind: info.UnitKind, Key: info.UnitKey, Name: info.UnitName, StackType: info.StackType, Containers: append([]string(nil), info.Containers...), Live: false, Snapshots: []ContainerBackupSnapshot{}}
			entries[id] = u
		}
		u.Snapshots = append(u.Snapshots, snap)
		return nil
	})
	if os.IsNotExist(err) {
		return []ContainerBackupUnit{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]ContainerBackupUnit, 0, len(entries))
	for _, u := range entries {
		sort.Slice(u.Snapshots, func(i, j int) bool { return u.Snapshots[i].CreatedAt > u.Snapshots[j].CreatedAt })
		u.ContainerCount = len(u.Containers)
		out = append(out, *u)
	}
	return out, nil
}

func readSnapshotInfo(path string) (snapshotInfo, ContainerBackupSnapshot, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return snapshotInfo{}, ContainerBackupSnapshot{}, err
	}
	defer zr.Close()
	var info snapshotInfo
	found := false
	for _, f := range zr.File {
		if f.Name != "backup-info.json" {
			continue
		}
		rc, e := f.Open()
		if e != nil {
			return info, ContainerBackupSnapshot{}, e
		}
		b, e := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if e != nil {
			return info, ContainerBackupSnapshot{}, e
		}
		if e = json.Unmarshal(b, &info); e != nil {
			return info, ContainerBackupSnapshot{}, e
		}
		found = true
		break
	}
	if !found {
		return info, ContainerBackupSnapshot{}, fmt.Errorf("backup-info.json missing")
	}
	st, _ := os.Stat(path)
	snap := ContainerBackupSnapshot{ID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Filename: filepath.Base(path), CreatedAt: info.CreatedAt, Reason: info.Reason}
	if st != nil {
		snap.SizeBytes = st.Size()
	}
	return info, snap, nil
}

func (a *App) listSnapshotsForUnit(hostID int64, kind, key string) []ContainerBackupSnapshot {
	dir := a.backupUnitDir(hostID, kind, key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []ContainerBackupSnapshot{}
	}
	out := []ContainerBackupSnapshot{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			continue
		}
		_, snap, err := readSnapshotInfo(filepath.Join(dir, e.Name()))
		if err == nil {
			out = append(out, snap)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (a *App) findBackupUnit(ctx context.Context, hostID int64, kind, key string) (ContainerBackupUnit, error) {
	units, err := a.discoverContainerBackupUnits(ctx)
	if err != nil {
		return ContainerBackupUnit{}, err
	}
	for _, u := range units {
		if u.HostID == hostID && u.Kind == kind && u.Key == key {
			return u, nil
		}
	}
	return ContainerBackupUnit{}, fmt.Errorf("backup target not found")
}

func (a *App) createSnapshotForContainer(ctx context.Context, hostID int64, container, reason string) (ContainerBackupSnapshot, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return ContainerBackupSnapshot{}, err
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return ContainerBackupSnapshot{}, err
	}
	var target *dockercli.Container
	for i := range cs {
		if cs[i].Name == container {
			target = &cs[i]
			break
		}
	}
	if target == nil {
		return ContainerBackupSnapshot{}, fmt.Errorf("container %s not found", container)
	}
	kind, key, _, _ := backupUnitFromContainer(*target)
	return a.createContainerSnapshot(ctx, hostID, kind, key, reason)
}

func (a *App) createContainerSnapshot(ctx context.Context, hostID int64, kind, key, reason string) (ContainerBackupSnapshot, error) {
	if kind != "stack" && kind != "service" {
		return ContainerBackupSnapshot{}, fmt.Errorf("invalid backup kind")
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return ContainerBackupSnapshot{}, err
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return ContainerBackupSnapshot{}, err
	}
	selected := []dockercli.Container{}
	stackType := ""
	unitName := key
	for _, c := range cs {
		if managed, _ := a.systemManagedContainer(c.Name); managed {
			continue
		}
		ck, ckey, cname, cstack := backupUnitFromContainer(c)
		if ck == kind && ckey == key {
			selected = append(selected, c)
			unitName = cname
			if stackType == "" {
				stackType = cstack
			}
		}
	}
	if len(selected) == 0 {
		return ContainerBackupSnapshot{}, fmt.Errorf("backup target is no longer present on Docker host")
	}
	sort.Slice(selected, func(i, j int) bool {
		aName := selected[i].StackService
		if aName == "" {
			aName = selected[i].Name
		}
		bName := selected[j].StackService
		if bName == "" {
			bName = selected[j].Name
		}
		return aName < bName
	})
	names := make([]string, 0, len(selected))
	imageRefs := []string{}
	for _, c := range selected {
		names = append(names, c.Name)
		if c.ImageID != "" {
			imageRefs = append(imageRefs, c.ImageID)
		} else if c.Image != "" {
			imageRefs = append(imageRefs, c.Image)
		}
	}
	inspectRaw, err := a.Docker.InspectContainersRaw(ctx, h.Endpoint, names...)
	if err != nil {
		return ContainerBackupSnapshot{}, err
	}
	var inspected []inspectContainer
	if err := json.Unmarshal(inspectRaw, &inspected); err != nil {
		return ContainerBackupSnapshot{}, fmt.Errorf("decode container inspect: %w", err)
	}
	imageRaw, imageErr := a.Docker.InspectImagesRaw(ctx, h.Endpoint, imageRefs...)
	if imageErr != nil {
		imageRaw = []byte("[]")
	}
	volumes, _ := a.Docker.VolumeInventory(ctx, h.Endpoint)
	usedVolumes := map[string]bool{}
	for _, ctr := range inspected {
		for _, m := range ctr.Mounts {
			if m.Type == "volume" && m.Name != "" {
				usedVolumes[m.Name] = true
			}
		}
	}
	volumeMeta := []dockercli.VolumeSummary{}
	for _, v := range volumes {
		if usedVolumes[v.Name] {
			volumeMeta = append(volumeMeta, v)
		}
	}

	compose := reconstructCompose(kind, key, selected, inspected)
	created := time.Now().UTC()
	info := snapshotInfo{
		SchemaVersion: 1, VibewatchVersion: a.Cfg.Version, CreatedAt: created.Format(time.RFC3339Nano), Reason: reason,
		HostID: hostID, HostName: h.Name, DockerEndpoint: h.Endpoint, UnitKind: kind, UnitKey: key, UnitName: unitName,
		StackType: stackType, Containers: names, Source: "docker-runtime", ReconstructedCompose: true, ContainsSecrets: true,
		Note: "compose.yaml is reconstructed from the active Docker runtime. It is a recovery configuration, not necessarily the original Compose/Portainer source. Runtime environment values may contain secrets. Volume data is not included.",
	}
	infoBytes, _ := json.MarshalIndent(info, "", "  ")
	volumeBytes, _ := json.MarshalIndent(volumeMeta, "", "  ")

	dir := a.backupUnitDir(hostID, kind, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ContainerBackupSnapshot{}, err
	}
	reasonPart := sanitizeFilename(reason)
	if reasonPart == "" {
		reasonPart = "snapshot"
	}
	base := created.Format("20060102T150405.000000000Z") + "-" + reasonPart
	path := filepath.Join(dir, base+".zip")
	if err := writeContainerSnapshotZip(path, []byte(compose), prettyJSON(inspectRaw), prettyJSON(imageRaw), volumeBytes, infoBytes); err != nil {
		return ContainerBackupSnapshot{}, err
	}
	_ = os.Chmod(path, 0o600)
	a.enforceSnapshotRetention(dir)
	st, _ := os.Stat(path)
	snap := ContainerBackupSnapshot{ID: base, Filename: filepath.Base(path), CreatedAt: info.CreatedAt, Reason: reason}
	if st != nil {
		snap.SizeBytes = st.Size()
	}
	_ = a.Store.Audit(context.Background(), "system", "container-backup.create", hostID, strings.Join(names, ","), fmt.Sprintf("unit=%s:%s reason=%s file=%s", kind, key, reason, filepath.Base(path)))
	for _, name := range names {
		baselineJSON := ""
		for _, current := range inspected {
			if strings.TrimPrefix(current.Name, "/") == name {
				baselineJSON = driftBaselineJSON(current)
				break
			}
		}
		_ = a.Store.SaveConfigDrift(context.Background(), db.ConfigDriftState{HostID: hostID, ContainerName: name, Status: "matches", DetailsJSON: "[]", BaselineAt: info.CreatedAt, BaselineJSON: baselineJSON, BaselineSource: "recovery-snapshot", CheckedAt: info.CreatedAt})
	}
	a.Logger.Info("container recovery snapshot created", "host_id", hostID, "host", h.Name, "unit_kind", kind, "unit", key, "containers", len(names), "reason", reason, "file", path)
	return snap, nil
}

func writeContainerSnapshotZip(path string, compose, inspectJSON, imagesJSON, volumesJSON, infoJSON []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	writeEntry := func(name string, body []byte) error {
		zf, e := zw.Create(name)
		if e != nil {
			return e
		}
		_, e = zf.Write(body)
		return e
	}
	entries := []struct {
		name string
		body []byte
	}{
		{"compose.yaml", compose},
		{"container-inspect.json", inspectJSON},
		{"images.json", imagesJSON},
		{"volumes.json", volumesJSON},
		{"backup-info.json", infoJSON},
	}
	var writeErr error
	for _, entry := range entries {
		if writeErr = writeEntry(entry.name, entry.body); writeErr != nil {
			break
		}
	}
	closeErr := zw.Close()
	fileCloseErr := f.Close()
	if writeErr != nil || closeErr != nil || fileCloseErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		return fileCloseErr
	}
	return os.Chmod(path, 0o600)
}

func prettyJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return b
	}
	return out
}

func (a *App) enforceSnapshotRetention(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	files := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	retention := a.containerSnapshotRetention(context.Background())
	if len(files) <= retention {
		return
	}
	for _, old := range files[:len(files)-retention] {
		path := filepath.Join(dir, old)
		if err := os.Remove(path); err == nil && a.Logger != nil {
			a.Logger.Info("container recovery snapshot removed by retention", "file", path, "retention", retention)
		}
	}
}

func (a *App) enforceAllSnapshotRetention() {
	root := a.containerBackupRoot()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		entries, e := os.ReadDir(path)
		if e != nil {
			return nil
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
				a.enforceSnapshotRetention(path)
				break
			}
		}
		return nil
	})
}

func isRollbackSnapshotReason(reason string) bool {
	switch strings.TrimSpace(strings.ToLower(reason)) {
	case "before-update", "before-manual-update", "before-automatic-update":
		return true
	default:
		return false
	}
}

// rollbackProtectedDockerObjects returns Docker objects referenced by retained
// pre-update recovery snapshots. Cleanup uses this to avoid invalidating an
// otherwise available rollback. Volumes keep a snapshot count so the UI can
// explain why an otherwise-unused volume is retained.
func (a *App) rollbackProtectedDockerObjects(hostID int64) (map[string]bool, map[string]bool, map[string]int) {
	images := map[string]bool{}
	networks := map[string]bool{}
	volumes := map[string]int{}
	root := filepath.Join(a.containerBackupRoot(), fmt.Sprintf("host-%d", hostID))
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".zip") {
			return nil
		}
		info, _, e := readSnapshotInfo(path)
		if e != nil || !isRollbackSnapshotReason(info.Reason) {
			return nil
		}
		snapshotVolumes := map[string]bool{}
		if raw, e := snapshotZipEntry(path, "images.json"); e == nil {
			var xs []snapshotImageInspect
			if json.Unmarshal(raw, &xs) == nil {
				for _, img := range xs {
					if id := strings.TrimSpace(img.ID); id != "" {
						images[id] = true
					}
				}
			}
		}
		if raw, e := snapshotZipEntry(path, "volumes.json"); e == nil {
			var xs []dockercli.VolumeSummary
			if json.Unmarshal(raw, &xs) == nil {
				for _, volume := range xs {
					if name := strings.TrimSpace(volume.Name); name != "" {
						snapshotVolumes[name] = true
					}
				}
			}
		}
		if raw, e := snapshotZipEntry(path, "container-inspect.json"); e == nil {
			var xs []inspectContainer
			if json.Unmarshal(raw, &xs) == nil {
				for _, ctr := range xs {
					if id := strings.TrimSpace(ctr.Image); id != "" {
						images[id] = true
					}
					for name := range ctr.NetworkSettings.Networks {
						name = strings.TrimSpace(name)
						if name != "" && name != "bridge" && name != "host" && name != "none" {
							networks[name] = true
						}
					}
					for _, mount := range ctr.Mounts {
						if mount.Type == "volume" {
							if name := strings.TrimSpace(mount.Name); name != "" {
								snapshotVolumes[name] = true
							}
						}
					}
				}
			}
		}
		for name := range snapshotVolumes {
			volumes[name]++
		}
		return nil
	})
	return images, networks, volumes
}

func yamlQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
func yamlDuration(ns int64) string {
	if ns <= 0 {
		return ""
	}
	return time.Duration(ns).String()
}

func reconstructCompose(kind, key string, containers []dockercli.Container, inspected []inspectContainer) string {
	metaByName := map[string]dockercli.Container{}
	for _, c := range containers {
		metaByName[c.Name] = c
	}
	// A replicated Swarm service may yield multiple task containers. A Compose
	// recovery file can represent the service once, so keep one representative.
	type serviceEntry struct {
		name string
		ctr  inspectContainer
		meta dockercli.Container
	}
	services := []serviceEntry{}
	seenServices := map[string]bool{}
	for _, ctr := range inspected {
		name := strings.TrimPrefix(ctr.Name, "/")
		meta := metaByName[name]
		serviceName := name
		if kind == "stack" && strings.TrimSpace(meta.StackService) != "" {
			serviceName = meta.StackService
		}
		if seenServices[serviceName] {
			continue
		}
		seenServices[serviceName] = true
		services = append(services, serviceEntry{name: serviceName, ctr: ctr, meta: meta})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].name < services[j].name })
	namedVolumes := map[string]bool{}
	networks := map[string]bool{}
	var b strings.Builder
	b.WriteString("# Vibewatch recovery Compose snapshot\n")
	b.WriteString("# Reconstructed from the active Docker runtime; this may differ from the original Compose/Portainer source.\n")
	b.WriteString("# Runtime environment values are included for recovery and may contain secrets. Volume contents are NOT included.\n")
	if kind == "stack" {
		b.WriteString("name: ")
		b.WriteString(yamlQuote(key))
		b.WriteString("\n")
	}
	b.WriteString("services:\n")
	for _, s := range services {
		c := s.ctr
		b.WriteString("  ")
		b.WriteString(yamlQuote(s.name))
		b.WriteString(":\n")
		image := strings.TrimSpace(c.Config.Image)
		if image == "" {
			image = s.meta.Image
		}
		if image != "" {
			b.WriteString("    image: ")
			b.WriteString(yamlQuote(image))
			b.WriteString("\n")
		}
		if kind == "service" {
			b.WriteString("    container_name: ")
			b.WriteString(yamlQuote(strings.TrimPrefix(c.Name, "/")))
			b.WriteString("\n")
		}
		if c.Config.Hostname != "" {
			b.WriteString("    hostname: ")
			b.WriteString(yamlQuote(c.Config.Hostname))
			b.WriteString("\n")
		}
		if c.Config.Domainname != "" {
			b.WriteString("    domainname: ")
			b.WriteString(yamlQuote(c.Config.Domainname))
			b.WriteString("\n")
		}
		if c.Config.User != "" {
			b.WriteString("    user: ")
			b.WriteString(yamlQuote(c.Config.User))
			b.WriteString("\n")
		}
		if c.Config.WorkingDir != "" {
			b.WriteString("    working_dir: ")
			b.WriteString(yamlQuote(c.Config.WorkingDir))
			b.WriteString("\n")
		}
		if len(c.Config.Entrypoint) > 0 {
			b.WriteString("    entrypoint:")
			for _, v := range c.Config.Entrypoint {
				b.WriteString("\n      - ")
				b.WriteString(yamlQuote(v))
			}
			b.WriteString("\n")
		}
		if len(c.Config.Cmd) > 0 {
			b.WriteString("    command:")
			for _, v := range c.Config.Cmd {
				b.WriteString("\n      - ")
				b.WriteString(yamlQuote(v))
			}
			b.WriteString("\n")
		}
		rp := strings.TrimSpace(c.HostConfig.RestartPolicy.Name)
		if rp != "" && rp != "no" {
			if rp == "on-failure" && c.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
				rp += ":" + strconv.Itoa(c.HostConfig.RestartPolicy.MaximumRetryCount)
			}
			b.WriteString("    restart: ")
			b.WriteString(yamlQuote(rp))
			b.WriteString("\n")
		}
		if c.HostConfig.Privileged {
			b.WriteString("    privileged: true\n")
		}
		if c.HostConfig.ReadonlyRootfs {
			b.WriteString("    read_only: true\n")
		}
		if len(c.Config.Env) > 0 {
			b.WriteString("    environment:\n")
			for _, v := range c.Config.Env {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.PortBindings) > 0 {
			keys := make([]string, 0, len(c.HostConfig.PortBindings))
			for k := range c.HostConfig.PortBindings {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := []string{}
			for _, containerPort := range keys {
				for _, p := range c.HostConfig.PortBindings[containerPort] {
					host := p.HostPort
					if p.HostIP != "" && p.HostIP != "0.0.0.0" && p.HostIP != "::" {
						hostIP := p.HostIP
						if strings.Contains(hostIP, ":") && !strings.HasPrefix(hostIP, "[") {
							hostIP = "[" + hostIP + "]"
						}
						host = hostIP + ":" + host
					}
					if host != "" {
						lines = append(lines, host+":"+containerPort)
					}
				}
			}
			if len(lines) > 0 {
				b.WriteString("    ports:\n")
				for _, v := range lines {
					b.WriteString("      - ")
					b.WriteString(yamlQuote(v))
					b.WriteString("\n")
				}
			}
		}
		recoveryMounts := make([]struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			Mode        string `json:"Mode"`
			RW          bool   `json:"RW"`
			Propagation string `json:"Propagation"`
		}, 0, len(c.Mounts))
		for _, m := range c.Mounts {
			if m.Type == "bind" || m.Type == "volume" {
				recoveryMounts = append(recoveryMounts, m)
			}
		}
		if len(recoveryMounts) > 0 {
			b.WriteString("    volumes:\n")
			for _, m := range recoveryMounts {
				b.WriteString("      - type: ")
				b.WriteString(m.Type)
				b.WriteString("\n")
				source := m.Source
				if m.Type == "volume" {
					source = m.Name
					if source != "" {
						namedVolumes[source] = true
					}
				}
				if source != "" {
					b.WriteString("        source: ")
					b.WriteString(yamlQuote(source))
					b.WriteString("\n")
				}
				b.WriteString("        target: ")
				b.WriteString(yamlQuote(m.Destination))
				b.WriteString("\n")
				if !m.RW {
					b.WriteString("        read_only: true\n")
				}
				if m.Type == "bind" && m.Propagation != "" && m.Propagation != "rprivate" {
					b.WriteString("        bind:\n          propagation: ")
					b.WriteString(yamlQuote(m.Propagation))
					b.WriteString("\n")
				}
			}
		}
		if len(c.HostConfig.Tmpfs) > 0 {
			keys := make([]string, 0, len(c.HostConfig.Tmpfs))
			for target := range c.HostConfig.Tmpfs {
				keys = append(keys, target)
			}
			sort.Strings(keys)
			b.WriteString("    tmpfs:\n")
			for _, target := range keys {
				v := target
				if opts := strings.TrimSpace(c.HostConfig.Tmpfs[target]); opts != "" {
					v += ":" + opts
				}
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		netMode := strings.TrimSpace(c.HostConfig.NetworkMode)
		if netMode == "host" || netMode == "none" || strings.HasPrefix(netMode, "container:") {
			b.WriteString("    network_mode: ")
			b.WriteString(yamlQuote(netMode))
			b.WriteString("\n")
		} else if len(c.NetworkSettings.Networks) > 0 {
			ns := make([]string, 0, len(c.NetworkSettings.Networks))
			for n := range c.NetworkSettings.Networks {
				if n != "bridge" && n != "host" && n != "none" {
					ns = append(ns, n)
					networks[n] = true
				}
			}
			sort.Strings(ns)
			if len(ns) > 0 {
				b.WriteString("    networks:\n")
				for _, n := range ns {
					b.WriteString("      - ")
					b.WriteString(yamlQuote(n))
					b.WriteString("\n")
				}
			}
		}
		labels := []string{}
		for k, v := range c.Config.Labels {
			if strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "com.docker.swarm.") || k == "com.docker.stack.namespace" {
				continue
			}
			labels = append(labels, k+"="+v)
		}
		sort.Strings(labels)
		if len(labels) > 0 {
			b.WriteString("    labels:\n")
			for _, v := range labels {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.CapAdd) > 0 {
			b.WriteString("    cap_add:\n")
			for _, v := range c.HostConfig.CapAdd {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.CapDrop) > 0 {
			b.WriteString("    cap_drop:\n")
			for _, v := range c.HostConfig.CapDrop {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.DNS) > 0 {
			b.WriteString("    dns:\n")
			for _, v := range c.HostConfig.DNS {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.DNSSearch) > 0 {
			b.WriteString("    dns_search:\n")
			for _, v := range c.HostConfig.DNSSearch {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.ExtraHosts) > 0 {
			b.WriteString("    extra_hosts:\n")
			for _, v := range c.HostConfig.ExtraHosts {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.SecurityOpt) > 0 {
			b.WriteString("    security_opt:\n")
			for _, v := range c.HostConfig.SecurityOpt {
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if len(c.HostConfig.Devices) > 0 {
			b.WriteString("    devices:\n")
			for _, d := range c.HostConfig.Devices {
				v := d.PathOnHost + ":" + d.PathInContainer
				if d.CgroupPermissions != "" {
					v += ":" + d.CgroupPermissions
				}
				b.WriteString("      - ")
				b.WriteString(yamlQuote(v))
				b.WriteString("\n")
			}
		}
		if c.HostConfig.ShmSize > 0 && c.HostConfig.ShmSize != 64*1024*1024 {
			b.WriteString("    shm_size: ")
			b.WriteString(yamlQuote(fmt.Sprintf("%db", c.HostConfig.ShmSize)))
			b.WriteString("\n")
		}
		if hc := c.Config.Healthcheck; hc != nil && len(hc.Test) > 0 {
			b.WriteString("    healthcheck:\n      test:")
			for _, v := range hc.Test {
				b.WriteString("\n        - ")
				b.WriteString(yamlQuote(v))
			}
			b.WriteString("\n")
			if d := yamlDuration(hc.Interval); d != "" {
				b.WriteString("      interval: ")
				b.WriteString(yamlQuote(d))
				b.WriteString("\n")
			}
			if d := yamlDuration(hc.Timeout); d != "" {
				b.WriteString("      timeout: ")
				b.WriteString(yamlQuote(d))
				b.WriteString("\n")
			}
			if d := yamlDuration(hc.StartPeriod); d != "" {
				b.WriteString("      start_period: ")
				b.WriteString(yamlQuote(d))
				b.WriteString("\n")
			}
			if hc.Retries > 0 {
				b.WriteString("      retries: ")
				b.WriteString(strconv.Itoa(hc.Retries))
				b.WriteString("\n")
			}
		}
	}
	if len(namedVolumes) > 0 {
		b.WriteString("volumes:\n")
		keys := make([]string, 0, len(namedVolumes))
		for k := range namedVolumes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, n := range keys {
			b.WriteString("  ")
			b.WriteString(yamlQuote(n))
			b.WriteString(":\n    external: true\n    name: ")
			b.WriteString(yamlQuote(n))
			b.WriteString("\n")
		}
	}
	if len(networks) > 0 {
		b.WriteString("networks:\n")
		keys := make([]string, 0, len(networks))
		for k := range networks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, n := range keys {
			b.WriteString("  ")
			b.WriteString(yamlQuote(n))
			b.WriteString(":\n    external: true\n    name: ")
			b.WriteString(yamlQuote(n))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (a *App) handleContainerBackups(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	units, err := a.discoverContainerBackupUnits(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retention": a.containerSnapshotRetention(r.Context()), "units": units, "backup_root": a.containerBackupRoot()})
}

func (a *App) handleContainerBackupSnapshot(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	var in struct {
		HostID int64  `json:"host_id"`
		Kind   string `json:"kind"`
		Key    string `json:"key"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostID <= 0 || strings.TrimSpace(in.Key) == "" {
		writeErr(w, http.StatusBadRequest, "host_id, kind and key are required")
		return
	}
	if !a.hostAllowed(r, in.HostID) {
		writeErr(w, http.StatusForbidden, "host access denied")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	snap, err := a.createContainerSnapshot(ctx, in.HostID, in.Kind, in.Key, "manual")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "container-backup.manual", in.HostID, "", fmt.Sprintf("%s:%s file=%s", in.Kind, in.Key, snap.Filename))
	writeJSON(w, http.StatusCreated, map[string]any{"snapshot": snap, "retention": a.containerSnapshotRetention(r.Context())})
}

func (a *App) resolveSnapshotPath(hostID int64, kind, key, snapshotID string) (string, snapshotInfo, error) {
	if hostID <= 0 || (kind != "stack" && kind != "service") || strings.TrimSpace(key) == "" || strings.TrimSpace(snapshotID) == "" {
		return "", snapshotInfo{}, fmt.Errorf("invalid snapshot reference")
	}
	dir := a.backupUnitDir(hostID, kind, key)
	clean := filepath.Base(snapshotID) + ".zip"
	path := filepath.Join(dir, clean)
	info, _, err := readSnapshotInfo(path)
	if err != nil {
		return "", snapshotInfo{}, err
	}
	if info.HostID != hostID || info.UnitKind != kind || info.UnitKey != key {
		return "", snapshotInfo{}, fmt.Errorf("snapshot metadata mismatch")
	}
	return path, info, nil
}

func (a *App) handleContainerBackupDownload(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	hostID, _ := strconv.ParseInt(r.URL.Query().Get("host_id"), 10, 64)
	kind := r.URL.Query().Get("kind")
	key := r.URL.Query().Get("key")
	snapID := r.URL.Query().Get("snapshot")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "zip"
	}
	path, info, err := a.resolveSnapshotPath(hostID, kind, key, snapID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "snapshot not found")
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "container-backup.download", hostID, "", fmt.Sprintf("unit=%s:%s snapshot=%s format=%s", kind, key, snapID, format))
	if a.Logger != nil {
		a.Logger.Info("container recovery snapshot download", "actor", a.actor(r), "host_id", hostID, "unit_kind", kind, "unit", key, "snapshot", snapID, "format", format)
	}
	safeName := sanitizeFilename(info.UnitName)
	if safeName == "" {
		safeName = "container-backup"
	}
	if format == "compose" {
		zr, err := zip.OpenReader(path)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.Name != "compose.yaml" {
				continue
			}
			rc, e := f.Open()
			if e != nil {
				writeErr(w, 500, e.Error())
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Type", "application/yaml")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s-compose.yaml"`, safeName, snapID))
			_, _ = io.Copy(w, rc)
			return
		}
		writeErr(w, 404, "compose.yaml missing")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 404, "snapshot not found")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, safeName, snapID))
	if st != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

func snapshotZipEntry(path, name string) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, e := f.Open()
			if e != nil {
				return nil, e
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("entry %s not found", name)
}

func (a *App) handleContainerBackupDownloadAll(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	units, err := a.discoverContainerBackupUnits(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	name := "vibewatch-container-backups-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	zw := zip.NewWriter(w)
	count := 0
	for _, u := range units {
		if !a.hostAllowed(r, u.HostID) {
			continue
		}
		hostPart := sanitizeFilename(u.HostName)
		if hostPart == "" {
			hostPart = fmt.Sprintf("host-%d", u.HostID)
		}
		unitPart := sanitizeFilename(u.Name)
		if unitPart == "" {
			unitPart = sanitizeFilename(u.Key)
		}
		for _, snap := range u.Snapshots {
			path, _, e := a.resolveSnapshotPath(u.HostID, u.Kind, u.Key, snap.ID)
			if e != nil {
				continue
			}
			f, e := os.Open(path)
			if e != nil {
				continue
			}
			entryName := filepath.ToSlash(filepath.Join(hostPart, u.Kind+"-"+unitPart, filepath.Base(path)))
			entry, e := zw.Create(entryName)
			if e == nil {
				_, e = io.Copy(entry, f)
			}
			_ = f.Close()
			if e != nil {
				_ = zw.Close()
				return
			}
			count++
		}
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "container-backup.download-all", 0, "", fmt.Sprintf("snapshots=%d", count))
	_ = zw.Close()
}
