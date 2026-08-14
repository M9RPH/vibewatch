package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m9rph/vibewatch/internal/db"
)

const (
	dataHelperImage                = "busybox:1.36.1"
	dataRestoreVolume              = "vibewatch-restore-points"
	mountClassificationTTL         = 24 * time.Hour
	mountClassificationCacheSchema = "fsclass-v2"
	mountSizeTTL                   = 30 * time.Minute
	hostStorageTTL                 = 15 * time.Minute
)

type DataProtectionMount struct {
	Key           string   `json:"key"`
	Type          string   `json:"type"`
	Name          string   `json:"name,omitempty"`
	Source        string   `json:"source"`
	Destinations  []string `json:"destinations"`
	Owners        []string `json:"owners"`
	Writable      bool     `json:"writable"`
	StorageClass  string   `json:"storage_class"`
	FSType        string   `json:"fs_type,omitempty"`
	Selected      bool     `json:"selected"`
	SizeBytes     int64    `json:"size_bytes"`
	SizeKnown     bool     `json:"size_known"`
	SizeCheckedAt string   `json:"size_checked_at,omitempty"`
	SizeError     string   `json:"size_error,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type DataProtectionProfileView struct {
	HostID     int64                 `json:"host_id"`
	ScopeType  string                `json:"scope_type"`
	ScopeKey   string                `json:"scope_key"`
	Enabled    bool                  `json:"enabled"`
	Mounts     []DataProtectionMount `json:"mounts"`
	Retention  int                   `json:"retention"`
	UpdatedAt  string                `json:"updated_at"`
	Configured bool                  `json:"configured"`
}

type dataArchiveEntry struct {
	Key          string   `json:"key"`
	Type         string   `json:"type"`
	Name         string   `json:"name,omitempty"`
	Source       string   `json:"source"`
	Destinations []string `json:"destinations,omitempty"`
	Owners       []string `json:"owners,omitempty"`
	StorageClass string   `json:"storage_class"`
	FSType       string   `json:"fs_type,omitempty"`
	Archive      string   `json:"archive"`
	SHA256       string   `json:"sha256"`
	SizeBytes    int64    `json:"size_bytes"`
}

type dataArchiveManifest struct {
	SchemaVersion int                `json:"schema_version"`
	HostID        int64              `json:"host_id"`
	ScopeType     string             `json:"scope_type"`
	ScopeKey      string             `json:"scope_key"`
	SnapshotID    string             `json:"snapshot_id"`
	CreatedAt     string             `json:"created_at"`
	Entries       []dataArchiveEntry `json:"entries"`
	TotalBytes    int64              `json:"total_bytes"`
}

func dataMountKey(t, name, source string) string {
	if t == "volume" && strings.TrimSpace(name) != "" {
		return "volume:" + strings.TrimSpace(name)
	}
	return "bind:" + strings.TrimSpace(source)
}

func dataHelperArgs(hostID int64, purpose string) []string {
	name := fmt.Sprintf("vibewatch-helper-%s-%d-%d", sanitizeFilename(purpose), hostID, time.Now().UTC().UnixNano())
	return []string{"--name", name, "--label", "io.vibewatch.system-role=helper"}
}

func dataMountArchiveName(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8]) + ".tar.gz"
}

func selectedMountKeys(raw string) map[string]bool {
	var xs []string
	_ = json.Unmarshal([]byte(raw), &xs)
	out := map[string]bool{}
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out[x] = true
		}
	}
	return out
}

func unsupportedDataBind(source string) bool {
	source = strings.TrimSpace(strings.TrimSuffix(source, "/"))
	if source == "" {
		return true
	}
	switch source {
	case "/", "/proc", "/sys", "/dev", "/var/run/docker.sock", "/run/docker.sock":
		return true
	}
	return strings.HasPrefix(source, "/proc/") || strings.HasPrefix(source, "/sys/") || strings.HasPrefix(source, "/dev/")
}

func normalizeFSType(fs string) string {
	fs = strings.ToLower(strings.TrimSpace(fs))
	if i := strings.IndexByte(fs, '\n'); i >= 0 {
		fs = strings.TrimSpace(fs[:i])
	}
	return fs
}

func networkFSType(fs string) bool {
	fs = normalizeFSType(fs)
	if fs == "" {
		return false
	}
	if strings.HasPrefix(fs, "fuse.") || fs == "fuse" {
		// FUSE filesystems are commonly backed by remote/user-space storage.
		// Treat them conservatively rather than promising local consistency.
		return true
	}
	for _, marker := range []string{"nfs", "cifs", "smb", "9p", "ceph", "gluster", "davfs", "sshfs", "afs", "lustre", "gpfs"} {
		if strings.Contains(fs, marker) {
			return true
		}
	}
	return false
}

func localFSType(fs string) bool {
	fs = normalizeFSType(fs)
	switch fs {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "jfs", "reiserfs", "reiser4", "bcachefs", "nilfs2", "overlay", "overlayfs", "tmpfs", "ramfs", "ubifs":
		return true
	default:
		return false
	}
}

func storageClassForFSType(fs string) string {
	if networkFSType(fs) {
		return "external"
	}
	if localFSType(fs) {
		return "local"
	}
	return "unknown"
}

func normalizeFSMagic(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimPrefix(v, "0x")
	v = strings.TrimLeft(v, "0")
	if v == "" {
		return "0"
	}
	return v
}

func networkFSMagic(v string) bool {
	switch normalizeFSMagic(v) {
	case "fe534d42", // SMB2
		"ff534d42", // CIFS
		"517b",     // legacy SMB
		"6969",     // NFS
		"65735546", // FUSE
		"1021997",  // 9P
		"c36400":   // Ceph
		return true
	default:
		return false
	}
}

func localFSMagic(v string) bool {
	switch normalizeFSMagic(v) {
	case "ef53", // ext2/3/4
		"58465342", // XFS
		"9123683e", // btrfs
		"2fc12fc1", // ZFS
		"f2f52010", // F2FS
		"794c7630", // overlayfs
		"1021994",  // tmpfs
		"858458f6": // ramfs
		return true
	default:
		return false
	}
}

func canonicalFSType(fs, magic string) string {
	fs = normalizeFSType(fs)
	if fs != "" && !strings.HasPrefix(fs, "unknown") {
		return fs
	}
	switch normalizeFSMagic(magic) {
	case "fe534d42":
		return "smb2"
	case "ff534d42":
		return "cifs"
	case "517b":
		return "smb"
	case "6969":
		return "nfs"
	case "65735546":
		return "fuse"
	case "1021997":
		return "9p"
	case "c36400":
		return "ceph"
	case "ef53":
		return "ext"
	case "58465342":
		return "xfs"
	case "9123683e":
		return "btrfs"
	case "2fc12fc1":
		return "zfs"
	case "f2f52010":
		return "f2fs"
	case "794c7630":
		return "overlay"
	case "1021994":
		return "tmpfs"
	case "858458f6":
		return "ramfs"
	}
	return fs
}

func storageClassForFSProbe(fs, magic string) string {
	if networkFSType(fs) || networkFSMagic(magic) {
		return "external"
	}
	if localFSType(fs) || localFSMagic(magic) {
		return "local"
	}
	return "unknown"
}

func volumeBindSource(options map[string]string) string {
	t := strings.ToLower(strings.TrimSpace(options["type"]))
	o := strings.ToLower(strings.TrimSpace(options["o"]))
	device := strings.TrimSpace(options["device"])
	if t == "none" && device != "" && (strings.Contains(o, "bind") || strings.Contains(o, "rbind")) {
		return device
	}
	return ""
}

func volumeMetadataStorageClass(driver string, options map[string]string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "local" {
		return "external"
	}
	if len(options) == 0 {
		return "local"
	}
	joined := driver
	for k, v := range options {
		joined += " " + strings.ToLower(k+"="+v)
	}
	if networkFSType(options["type"]) || strings.Contains(joined, "nfs") || strings.Contains(joined, "cifs") || strings.Contains(joined, "smb") || strings.Contains(joined, "addr=") {
		return "external"
	}
	if volumeBindSource(options) != "" {
		// A local-driver bind volume inherits the storage type of its host source.
		return "unknown"
	}
	if t := strings.TrimSpace(options["type"]); t != "" {
		return storageClassForFSType(t)
	}
	return "unknown"
}

func parseRFC3339Any(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (a *App) protectDataHelperImage(ctx context.Context, endpoint string, protected map[string]bool) {
	if protected == nil {
		return
	}
	out, err := a.Docker.Run(ctx, endpoint, "image", "inspect", "--format", "{{.Id}}", dataHelperImage)
	if err == nil {
		if id := strings.TrimSpace(out); id != "" {
			protected[id] = true
		}
	}
}

func (a *App) ensureRestoreVolume(ctx context.Context, endpoint string) error {
	_, err := a.Docker.Run(ctx, endpoint, "volume", "create", "--label", "io.vibewatch.restore-store=true", dataRestoreVolume)
	return err
}

func (a *App) classifyBindMount(ctx context.Context, hostID int64, endpoint, key, source string) (string, string, string) {
	// Cache keys are schema-versioned so a classification-rule change never keeps
	// an old "local" result alive for the full TTL after an upgrade.
	cacheKey := mountClassificationCacheSchema + ":" + key
	if cached, err := a.Store.DataMountCache(ctx, hostID, cacheKey); err == nil && cached.CheckedAt != "" && time.Since(parseRFC3339Any(cached.CheckedAt)) < mountClassificationTTL {
		return cached.StorageClass, cached.FSType, cached.Error
	}
	probeCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(endpoint, 45*time.Second, 90*time.Second))
	defer cancel()
	args := append([]string{"run"}, dataHelperArgs(hostID, "mount-probe")...)
	// Ask BusyBox for both the human-readable filesystem type and the kernel
	// filesystem magic. Some BusyBox builds report SMB2 as UNKNOWN even though
	// the magic number is unambiguous, so relying on %T alone is not robust.
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=bind,source="+source+",target=/source,readonly", dataHelperImage, "sh", "-c", "stat -f -c '%T|%t' /source 2>/dev/null || stat -f /source")
	out, err := a.Docker.Run(probeCtx, endpoint, args...)
	firstLine := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	parts := strings.SplitN(firstLine, "|", 2)
	fsRaw, fsMagic := "", ""
	if len(parts) > 0 {
		fsRaw = parts[0]
	}
	if len(parts) > 1 {
		fsMagic = parts[1]
	}
	fsType := canonicalFSType(fsRaw, fsMagic)
	class, errText := "unknown", ""
	if err != nil {
		errText = err.Error()
	} else {
		class = storageClassForFSProbe(fsRaw, fsMagic)
		if class == "unknown" {
			detail := firstNonEmpty(fsType, normalizeFSMagic(fsMagic))
			if detail != "" && detail != "0" {
				errText = "Unrecognized filesystem type: " + detail
			}
		}
	}
	_ = a.Store.SaveDataMountCache(context.Background(), db.DataMountCache{HostID: hostID, MountKey: cacheKey, StorageClass: class, FSType: fsType, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Error: errText})
	return class, fsType, errText
}

func (a *App) dataProtectionInventory(ctx context.Context, hostID int64, container, scopeType string) (string, []DataProtectionMount, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return "", nil, err
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return "", nil, err
	}
	var targetName, stackName, stackType string
	for _, c := range cs {
		if c.Name == container {
			targetName, stackName, stackType = c.Name, c.StackName, c.StackType
			break
		}
	}
	if targetName == "" {
		return "", nil, fmt.Errorf("container %s not found", container)
	}
	if strings.EqualFold(stackType, "swarm") {
		return "", nil, fmt.Errorf("data protection for Docker Swarm services is not enabled in v0.9.5")
	}
	scopeKey := targetName
	if scopeType == "stack" {
		if stackName == "" {
			return "", nil, fmt.Errorf("container is not part of a Compose stack")
		}
		scopeKey = stackName
	} else {
		scopeType = "service"
	}
	type agg struct {
		m DataProtectionMount
	}
	byKey := map[string]*agg{}
	for _, c := range cs {
		if scopeType == "stack" && c.StackName != scopeKey {
			continue
		}
		if scopeType == "service" && c.Name != scopeKey {
			continue
		}
		ins, e := a.inspectOne(ctx, hostID, c.Name)
		if e != nil {
			return "", nil, fmt.Errorf("inspect %s: %w", c.Name, e)
		}
		for _, m := range ins.Mounts {
			if !m.RW {
				continue
			}
			if m.Type != "volume" && m.Type != "bind" {
				continue
			}
			if m.Type == "bind" && unsupportedDataBind(m.Source) {
				continue
			}
			key := dataMountKey(m.Type, m.Name, m.Source)
			if key == "bind:" || key == "volume:" {
				continue
			}
			x := byKey[key]
			if x == nil {
				x = &agg{m: DataProtectionMount{Key: key, Type: m.Type, Name: m.Name, Source: m.Source, Destinations: []string{}, Owners: []string{}, Writable: m.RW, StorageClass: "unknown"}}
				byKey[key] = x
			}
			if !containsString(x.m.Owners, c.Name) {
				x.m.Owners = append(x.m.Owners, c.Name)
			}
			if !containsString(x.m.Destinations, m.Destination) {
				x.m.Destinations = append(x.m.Destinations, m.Destination)
			}
			x.m.Writable = x.m.Writable || m.RW
		}
	}
	// Named volume drivers/options can identify network storage without a helper.
	for _, x := range byKey {
		if x.m.Type == "volume" {
			var rows []struct {
				Driver  string            `json:"Driver"`
				Options map[string]string `json:"Options"`
			}
			raw, e := a.Docker.Run(ctx, h.Endpoint, "volume", "inspect", x.m.Name)
			if e != nil || json.Unmarshal([]byte(raw), &rows) != nil || len(rows) == 0 {
				x.m.StorageClass, x.m.Error = "unknown", firstNonEmpty(func() string {
					if e != nil {
						return e.Error()
					}
					return "volume inspect unavailable"
				}())
			} else {
				driver := strings.ToLower(rows[0].Driver)
				options := rows[0].Options
				x.m.StorageClass = volumeMetadataStorageClass(driver, options)
				if source := volumeBindSource(options); source != "" {
					class, fsType, probeErr := a.classifyBindMount(ctx, hostID, h.Endpoint, "volume-source:"+x.m.Key, source)
					x.m.StorageClass, x.m.FSType = class, fsType
					if probeErr != "" {
						x.m.Error = probeErr
					}
				} else if t := strings.TrimSpace(options["type"]); t != "" {
					x.m.FSType = normalizeFSType(t)
				}
			}
		} else {
			x.m.StorageClass, x.m.FSType, x.m.Error = a.classifyBindMount(ctx, hostID, h.Endpoint, x.m.Key, x.m.Source)
		}
		sort.Strings(x.m.Owners)
		sort.Strings(x.m.Destinations)
	}
	out := make([]DataProtectionMount, 0, len(byKey))
	for _, x := range byKey {
		out = append(out, x.m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StorageClass != out[j].StorageClass {
			return out[i].StorageClass < out[j].StorageClass
		}
		return out[i].Key < out[j].Key
	})
	return scopeKey, out, nil
}

func (a *App) dataProtectionProfileView(ctx context.Context, hostID int64, container, scopeType string) (DataProtectionProfileView, error) {
	scopeKey, mounts, err := a.dataProtectionInventory(ctx, hostID, container, scopeType)
	if err != nil {
		return DataProtectionProfileView{}, err
	}
	profile, _ := a.Store.DataProtectionProfile(ctx, hostID, scopeType, scopeKey)
	selected := selectedMountKeys(profile.MountsJSON)
	for i := range mounts {
		mounts[i].Selected = selected[mounts[i].Key]
	}
	return DataProtectionProfileView{HostID: hostID, ScopeType: scopeType, ScopeKey: scopeKey, Enabled: bool(profile.Enabled), Mounts: mounts, Retention: a.containerSnapshotRetention(ctx), UpdatedAt: profile.UpdatedAt, Configured: profile.UpdatedAt != ""}, nil
}

func (a *App) dataProtectionProfileViewWithSizes(ctx context.Context, hostID int64, container, scopeType string) (DataProtectionProfileView, error) {
	view, err := a.dataProtectionProfileView(ctx, hostID, container, scopeType)
	if err != nil {
		return view, err
	}
	view.Mounts = a.populateDataMountSizes(ctx, hostID, view.Mounts)
	return view, nil
}

func (a *App) populateDataMountSizes(ctx context.Context, hostID int64, mounts []DataProtectionMount, force ...bool) []DataProtectionMount {
	if len(mounts) == 0 {
		return mounts
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return mounts
	}
	pending := make([]int, 0, len(mounts))
	forceScan := len(force) > 0 && force[0]
	for i := range mounts {
		if mounts[i].StorageClass == "external" {
			mounts[i].SizeError = "External storage size is not scanned automatically"
			continue
		}
		if mounts[i].StorageClass != "local" {
			mounts[i].SizeError = "Size unavailable for unknown storage"
			continue
		}
		if cached, cacheErr := a.Store.DataMountCache(ctx, hostID, mounts[i].Key); !forceScan && cacheErr == nil && cached.SizeCheckedAt != "" && time.Since(parseRFC3339Any(cached.SizeCheckedAt)) < mountSizeTTL {
			mounts[i].SizeBytes = cached.SizeBytes
			mounts[i].SizeKnown = cached.SizeError == ""
			mounts[i].SizeCheckedAt = cached.SizeCheckedAt
			mounts[i].SizeError = cached.SizeError
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return mounts
	}
	probeCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 60*time.Second, 2*time.Minute))
	defer cancel()
	args := append([]string{"run"}, dataHelperArgs(hostID, "mount-size")...)
	args = append(args, "--rm", "--network", "none", "--read-only")
	commands := []string{"set +e"}
	active := make([]int, 0, len(pending))
	for _, idx := range pending {
		target := fmt.Sprintf("/source/%d", len(active))
		mountArg, mountErr := helperMountArg(mounts[idx], target, true)
		if mountErr != nil {
			mounts[idx].SizeError = mountErr.Error()
			_ = a.Store.SaveDataMountSize(context.Background(), hostID, mounts[idx].Key, 0, time.Now().UTC().Format(time.RFC3339Nano), mounts[idx].SizeError)
			continue
		}
		pos := len(active)
		active = append(active, idx)
		args = append(args, "--mount", mountArg)
		commands = append(commands, fmt.Sprintf(`v=$(timeout 15 du -sk %s 2>/dev/null | awk 'NR==1 {print $1*1024}'); if [ -n "$v" ]; then echo "SIZE|%d|$v"; else echo "ERR|%d"; fi`, target, pos, pos))
	}
	if len(active) == 0 {
		return mounts
	}
	args = append(args, dataHelperImage, "sh", "-c", strings.Join(commands, "; "))
	out, runErr := a.Docker.Run(probeCtx, h.Endpoint, args...)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if runErr != nil {
		for _, idx := range active {
			mounts[idx].SizeError = runErr.Error()
			mounts[idx].SizeCheckedAt = now
			_ = a.Store.SaveDataMountSize(context.Background(), hostID, mounts[idx].Key, 0, now, mounts[idx].SizeError)
		}
		return mounts
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) < 2 {
			continue
		}
		pos, convErr := strconv.Atoi(parts[1])
		if convErr != nil || pos < 0 || pos >= len(active) {
			continue
		}
		idx := active[pos]
		seen[pos] = true
		mounts[idx].SizeCheckedAt = now
		if parts[0] == "SIZE" && len(parts) == 3 {
			sz, parseErr := strconv.ParseInt(parts[2], 10, 64)
			if parseErr == nil {
				mounts[idx].SizeBytes = sz
				mounts[idx].SizeKnown = true
				mounts[idx].SizeError = ""
				_ = a.Store.SaveDataMountSize(context.Background(), hostID, mounts[idx].Key, sz, now, "")
				continue
			}
		}
		mounts[idx].SizeError = "Mount size could not be measured"
		_ = a.Store.SaveDataMountSize(context.Background(), hostID, mounts[idx].Key, 0, now, mounts[idx].SizeError)
	}
	for pos, idx := range active {
		if seen[pos] {
			continue
		}
		mounts[idx].SizeCheckedAt = now
		mounts[idx].SizeError = "Mount size probe returned no data"
		_ = a.Store.SaveDataMountSize(context.Background(), hostID, mounts[idx].Key, 0, now, mounts[idx].SizeError)
	}
	return mounts
}

func (a *App) effectiveDataProtectionProfile(ctx context.Context, hostID int64, container string) (db.DataProtectionProfile, []string, bool) {
	if p, err := a.Store.DataProtectionProfile(ctx, hostID, "service", container); err == nil && bool(p.Enabled) {
		keys := selectedMountKeys(p.MountsJSON)
		if len(keys) > 0 {
			xs := make([]string, 0, len(keys))
			for k := range keys {
				xs = append(xs, k)
			}
			sort.Strings(xs)
			return p, xs, true
		}
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return db.DataProtectionProfile{}, nil, false
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return db.DataProtectionProfile{}, nil, false
	}
	stack := ""
	for _, c := range cs {
		if c.Name == container {
			stack = c.StackName
			break
		}
	}
	if stack != "" {
		if p, err := a.Store.DataProtectionProfile(ctx, hostID, "stack", stack); err == nil && bool(p.Enabled) {
			keys := selectedMountKeys(p.MountsJSON)
			if len(keys) > 0 {
				xs := make([]string, 0, len(keys))
				for k := range keys {
					xs = append(xs, k)
				}
				sort.Strings(xs)
				return p, xs, true
			}
		}
	}
	return db.DataProtectionProfile{}, nil, false
}

func (a *App) handleDataProtection(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.requireAdmin(w, r) {
		return
	}
	container := strings.TrimSpace(r.URL.Query().Get("container"))
	if container == "" {
		writeErr(w, 400, "container is required")
		return
	}
	scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
	if scopeType != "stack" {
		scopeType = "service"
	}
	if r.Method == http.MethodGet {
		var view DataProtectionProfileView
		var err error
		if r.URL.Query().Get("sizes") == "1" {
			view, err = a.dataProtectionProfileViewWithSizes(r.Context(), hostID, container, scopeType)
		} else {
			view, err = a.dataProtectionProfileView(r.Context(), hostID, container, scopeType)
		}
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, view)
		return
	}
	if r.Method != http.MethodPut {
		writeErr(w, 405, "method not allowed")
		return
	}
	view, err := a.dataProtectionProfileView(r.Context(), hostID, container, scopeType)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	var in struct {
		Enabled bool     `json:"enabled"`
		Mounts  []string `json:"mounts"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	allowed := map[string]bool{}
	for _, m := range view.Mounts {
		allowed[m.Key] = true
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, k := range in.Mounts {
		k = strings.TrimSpace(k)
		if !allowed[k] {
			writeErr(w, 400, "selected mount is not part of this scope: "+k)
			return
		}
		if !seen[k] {
			selected = append(selected, k)
			seen[k] = true
		}
	}
	sort.Strings(selected)
	raw, _ := json.Marshal(selected)
	if len(selected) == 0 {
		in.Enabled = false
	}
	if err := a.Store.SaveDataProtectionProfile(r.Context(), db.DataProtectionProfile{HostID: hostID, ScopeType: scopeType, ScopeKey: view.ScopeKey, Enabled: db.Bool(in.Enabled), MountsJSON: string(raw)}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.Store.Audit(r.Context(), a.actor(r), "data-protection.save", hostID, container, fmt.Sprintf("scope=%s:%s enabled=%t mounts=%d", scopeType, view.ScopeKey, in.Enabled, len(selected)))
	updated, err := a.dataProtectionProfileView(r.Context(), hostID, container, scopeType)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, updated)
}

func (a *App) probeHostStorage(ctx context.Context, hostID int64, force bool) db.HostStorageCache {
	cached, _ := a.Store.HostStorageCache(ctx, hostID)
	if !force && cached.CheckedAt != "" && time.Since(parseRFC3339Any(cached.CheckedAt)) < hostStorageTTL {
		return cached
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return db.HostStorageCache{HostID: hostID, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Error: err.Error()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 60*time.Second, 2*time.Minute))
	defer cancel()
	result := db.HostStorageCache{HostID: hostID, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}

	// Probe the host root filesystem and Docker's own storage filesystem without
	// creating the restore-point volume just because somebody opened Dashboard.
	// The actual restore volume is created lazily only when data protection is used.
	dockerRoot, rootErr := a.Docker.Run(probeCtx, h.Endpoint, "info", "--format", "{{.DockerRootDir}}")
	dockerRoot = strings.TrimSpace(dockerRoot)
	args := append([]string{"run"}, dataHelperArgs(hostID, "storage-probe")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=bind,source=/,target=/host,readonly")
	cmd := `df -Pk /host | awk 'NR==2 {printf "HOST|%.0f|%.0f\n",$2*1024,$4*1024}'`
	if rootErr == nil && dockerRoot != "" && !strings.Contains(dockerRoot, ",") {
		args = append(args, "--mount", "type=bind,source="+dockerRoot+",target=/restore,readonly")
		cmd += `; df -Pk /restore | awk 'NR==2 {printf "RESTORE|%.0f|%.0f\n",$2*1024,$4*1024}'`
	}
	args = append(args, dataHelperImage, "sh", "-c", cmd)
	out, err := a.Docker.Run(probeCtx, h.Endpoint, args...)
	if err != nil {
		if cached.HostTotalBytes > 0 || cached.RestoreTotalBytes > 0 {
			cached.Error = err.Error()
			cached.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = a.Store.SaveHostStorageCache(context.Background(), cached)
			return cached
		}
		result.Error = err.Error()
	} else {
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Split(strings.TrimSpace(line), "|")
			if len(parts) != 3 {
				continue
			}
			total, _ := strconv.ParseInt(parts[1], 10, 64)
			free, _ := strconv.ParseInt(parts[2], 10, 64)
			if parts[0] == "HOST" {
				result.HostTotalBytes, result.HostFreeBytes = total, free
			} else if parts[0] == "RESTORE" {
				result.RestoreTotalBytes, result.RestoreFreeBytes = total, free
			}
		}
	}
	if result.HostTotalBytes == 0 && result.RestoreTotalBytes == 0 {
		if cached.HostTotalBytes > 0 || cached.RestoreTotalBytes > 0 {
			cached.Error = "storage probe returned no capacity data"
			cached.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = a.Store.SaveHostStorageCache(context.Background(), cached)
			return cached
		}
		if result.Error == "" {
			result.Error = "storage probe returned no capacity data"
		}
	}
	if result.Error == "" {
		_ = a.Store.SaveHostStorageCache(context.Background(), result)
	}
	return result
}

func (a *App) handleHostStorage(w http.ResponseWriter, r *http.Request, hostID int64) {
	if !a.hostAllowed(r, hostID) {
		writeErr(w, 403, "host access denied")
		return
	}
	force := r.URL.Query().Get("refresh") == "1" && a.isAdmin(r)
	writeJSON(w, 200, a.probeHostStorage(r.Context(), hostID, force))
}

const (
	restoreStorageBaseReserveBytes = int64(512 * 1024 * 1024)
	restoreStorageReservePercent   = int64(15)
)

func restoreStorageRequirementBytes(payload int64) int64 {
	if payload < 0 {
		payload = 0
	}
	reserve := payload * restoreStorageReservePercent / 100
	if reserve < restoreStorageBaseReserveBytes {
		reserve = restoreStorageBaseReserveBytes
	}
	return payload + reserve
}

func preflightByteSize(v int64) string {
	if v < 0 {
		v = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	n := float64(v)
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func (a *App) containerWritableLayerBytes(ctx context.Context, hostID int64, container string) (int64, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, dockerOperationTimeout(h.Endpoint, 20*time.Second, 40*time.Second))
	defer cancel()
	out, err := a.Docker.Run(probeCtx, h.Endpoint, "container", "inspect", "--size", "--format", "{{.SizeRw}}", container)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid writable-layer size %q", strings.TrimSpace(out))
	}
	if v < 0 {
		v = 0
	}
	return v, nil
}

func (a *App) restoreStoragePreflight(ctx context.Context, req updateRequest, target inspectContainer, force bool) PreflightCheck {
	if strings.TrimSpace(target.Config.Labels["com.docker.swarm.service.name"]) != "" {
		return PreflightCheck{Key: "restore_storage", Status: preflightInfo, Title: "Host restore storage not required", Description: "This Swarm target uses config-only rollback in v0.9.5; no host-local writable-layer/data restore archive is created.", Source: "Vibewatch restore storage", Blocking: false}
	}

	storage := a.probeHostStorage(ctx, req.HostID, force)
	if storage.RestoreTotalBytes <= 0 {
		detail := firstNonEmpty(storage.Error, "Docker/restore filesystem capacity could not be measured")
		return PreflightCheck{Key: "restore_storage", Status: preflightRed, Title: "Restore storage unavailable", Description: "Vibewatch cannot prove that the Docker host has enough free space to create the mandatory restore point.", Detail: detail, Source: "Vibewatch restore storage", Blocking: true}
	}

	writableBytes, writableErr := a.containerWritableLayerBytes(ctx, req.HostID, req.Container)
	knownPayload := writableBytes
	localUnknown := writableErr != nil
	externalUnknown := false
	selectedCount := 0
	if !req.SkipDataProtectionCapture {
		_, mounts, configured, err := a.selectedDataProtectionMounts(ctx, req.HostID, req.Container)
		if err != nil {
			localUnknown = true
		} else if configured {
			selectedCount = len(mounts)
			mounts = a.populateDataMountSizes(ctx, req.HostID, mounts, force)
			for _, m := range mounts {
				switch m.StorageClass {
				case "local":
					if m.SizeKnown {
						knownPayload += m.SizeBytes
					} else {
						localUnknown = true
					}
				case "external", "unknown":
					externalUnknown = true
				}
			}
		}
	}

	required := restoreStorageRequirementBytes(knownPayload)
	free := storage.RestoreFreeBytes
	detailParts := []string{fmt.Sprintf("%s required (incl. reserve)", preflightByteSize(required)), fmt.Sprintf("%s free", preflightByteSize(free))}
	if selectedCount > 0 {
		detailParts = append(detailParts, fmt.Sprintf("%d protected mount(s)", selectedCount))
	}
	if writableErr != nil {
		detailParts = append(detailParts, "writable-layer size unavailable")
	}
	if externalUnknown {
		detailParts = append(detailParts, "external/unknown data size excluded")
	}
	detail := strings.Join(detailParts, " · ")

	if free < required {
		return PreflightCheck{Key: "restore_storage", Status: preflightRed, Title: "Insufficient restore storage", Description: "The Docker host does not have enough free restore storage for the pre-update restore point and safety reserve. The update is blocked before any data is changed.", Detail: detail, Source: "Vibewatch restore storage", Blocking: true}
	}
	if localUnknown {
		return PreflightCheck{Key: "restore_storage", Status: preflightYellow, Title: "Restore storage estimate incomplete", Description: "Free host storage is available, but Vibewatch could not measure every local component of the restore point. Automatic clean-Preflight updates remain conservative.", Detail: detail, Source: "Vibewatch restore storage", Blocking: false}
	}
	if externalUnknown {
		return PreflightCheck{Key: "restore_storage", Status: preflightInfo, Title: "Restore storage capacity available", Description: "Known local restore data fits in the available host storage. External or unknown mount sizes are intentionally not scanned automatically and are excluded from this capacity estimate.", Detail: detail, Source: "Vibewatch restore storage", Blocking: false}
	}
	return PreflightCheck{Key: "restore_storage", Status: preflightGreen, Title: "Restore storage capacity ready", Description: "The Docker host has enough measured free space for the known restore-point payload plus a safety reserve.", Detail: detail, Source: "Vibewatch restore storage", Blocking: false}
}

func eligiblePersistentMountCount(inspects []inspectContainer) int {
	seen := map[string]bool{}
	for _, ins := range inspects {
		for _, m := range ins.Mounts {
			if !m.RW {
				continue
			}
			if m.Type != "volume" && m.Type != "bind" {
				continue
			}
			if m.Type == "bind" && unsupportedDataBind(m.Source) {
				continue
			}
			key := dataMountKey(m.Type, m.Name, m.Source)
			if key == "bind:" || key == "volume:" {
				continue
			}
			seen[key] = true
		}
	}
	return len(seen)
}

func unconfiguredDataProtectionCheck(scopeType, scopeKey string, mounts int, swarm bool, inspectErr error) PreflightCheck {
	scopeLabel := "service"
	if scopeType == "stack" {
		scopeLabel = "Compose stack"
	}
	if inspectErr != nil {
		return PreflightCheck{Key: "data_protection", Status: preflightYellow, Title: "Data protection not configured", Description: "Vibewatch could not fully assess persistent mounts while no Data Protection profile is configured.", Detail: inspectErr.Error(), Source: "Vibewatch data protection", Blocking: false}
	}
	if mounts == 0 {
		return PreflightCheck{Key: "data_protection", Status: preflightGreen, Title: "No persistent mounts detected", Description: fmt.Sprintf("No bind mounts or Docker volumes were detected for this %s; the container writable layer remains covered by the normal Restore Point.", scopeLabel), Detail: scopeKey, Source: "Vibewatch data protection", Blocking: false}
	}
	desc := fmt.Sprintf("%d persistent mount(s) are present in this %s, but none are protected by Data Protection.", mounts, scopeLabel)
	if swarm {
		desc = fmt.Sprintf("%d persistent mount(s) are present in this Swarm service. Data Protection for Swarm services is not available in v0.9.5.", mounts)
	}
	return PreflightCheck{Key: "data_protection", Status: preflightYellow, Title: "Persistent data not protected", Description: desc, Detail: "Configure Data Protection for rollback-relevant mounts, or explicitly accept this advisory warning for the update.", Source: "Vibewatch data protection", Blocking: false}
}

func (a *App) unconfiguredDataProtectionPreflight(ctx context.Context, req updateRequest) PreflightCheck {
	h, err := a.Store.Host(ctx, req.HostID)
	if err != nil {
		return unconfiguredDataProtectionCheck("service", req.Container, 0, false, err)
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return unconfiguredDataProtectionCheck("service", req.Container, 0, false, err)
	}
	scopeType, scopeKey := "service", req.Container
	names := []string{req.Container}
	swarm := false
	for _, c := range cs {
		if c.Name != req.Container {
			continue
		}
		swarm = strings.EqualFold(c.StackType, "swarm")
		if c.StackName != "" && !swarm {
			scopeType, scopeKey = "stack", c.StackName
			names = names[:0]
			for _, member := range cs {
				if member.StackName == c.StackName {
					names = append(names, member.Name)
				}
			}
		}
		break
	}
	raw, inspectErr := a.Docker.InspectContainersRaw(ctx, h.Endpoint, names...)
	if inspectErr != nil {
		return unconfiguredDataProtectionCheck(scopeType, scopeKey, 0, swarm, inspectErr)
	}
	var inspects []inspectContainer
	if err := json.Unmarshal(raw, &inspects); err != nil {
		return unconfiguredDataProtectionCheck(scopeType, scopeKey, 0, swarm, err)
	}
	return unconfiguredDataProtectionCheck(scopeType, scopeKey, eligiblePersistentMountCount(inspects), swarm, nil)
}

func (a *App) dataProtectionPreflight(ctx context.Context, req updateRequest) (PreflightCheck, bool) {
	profile, keys, ok := a.effectiveDataProtectionProfile(ctx, req.HostID, req.Container)
	if !ok {
		return a.unconfiguredDataProtectionPreflight(ctx, req), true
	}
	view, err := a.dataProtectionProfileView(ctx, req.HostID, req.Container, profile.ScopeType)
	if err != nil {
		return PreflightCheck{Key: "data_protection", Status: preflightRed, Title: "Data protection unavailable", Description: "Configured update data protection could not be inspected.", Detail: err.Error(), Source: "Vibewatch data protection", Blocking: true}, true
	}
	found := map[string]DataProtectionMount{}
	for _, m := range view.Mounts {
		found[m.Key] = m
	}
	external := 0
	unknown := 0
	for _, k := range keys {
		m, exists := found[k]
		if !exists {
			return PreflightCheck{Key: "data_protection", Status: preflightRed, Title: "Protected mount missing", Description: "A selected data mount is no longer present; update is blocked until the protection profile is corrected.", Detail: k, Source: "Vibewatch data protection", Blocking: true}, true
		}
		if m.StorageClass == "external" {
			external++
		}
		if m.StorageClass == "unknown" {
			unknown++
		}
	}
	status := preflightGreen
	title := "Data protection ready"
	detail := fmt.Sprintf("%d selected mount(s) · retention %d", len(keys), view.Retention)
	desc := "Selected persistent data will be cold-captured on the Docker host before the update."
	if external > 0 || unknown > 0 {
		status = preflightYellow
		title = "Data protection includes external storage"
		desc = "External or unknown storage can be captured, but Vibewatch cannot guarantee that writers outside this Docker host are stopped."
		detail = fmt.Sprintf("%d external · %d unknown", external, unknown)
	}
	return PreflightCheck{Key: "data_protection", Status: status, Title: title, Description: desc, Detail: detail, Source: "Vibewatch data protection", Blocking: false}, true
}

func (a *App) dataProtectionCaptureScope(ctx context.Context, hostID int64, container string) string {
	profile, _, ok := a.effectiveDataProtectionProfile(ctx, hostID, container)
	if !ok {
		return ""
	}
	return strings.TrimSpace(profile.ScopeType) + ":" + strings.TrimSpace(profile.ScopeKey)
}

func (a *App) selectedDataProtectionMounts(ctx context.Context, hostID int64, container string) (db.DataProtectionProfile, []DataProtectionMount, bool, error) {
	profile, keys, ok := a.effectiveDataProtectionProfile(ctx, hostID, container)
	if !ok {
		return db.DataProtectionProfile{}, nil, false, nil
	}
	view, err := a.dataProtectionProfileView(ctx, hostID, container, profile.ScopeType)
	if err != nil {
		return profile, nil, true, err
	}
	byKey := map[string]DataProtectionMount{}
	for _, m := range view.Mounts {
		byKey[m.Key] = m
	}
	mounts := make([]DataProtectionMount, 0, len(keys))
	for _, key := range keys {
		m, exists := byKey[key]
		if !exists {
			return profile, nil, true, fmt.Errorf("protected mount %s is no longer present", key)
		}
		mounts = append(mounts, m)
	}
	return profile, mounts, true, nil
}

func mountKeySet(mounts []DataProtectionMount) map[string]bool {
	out := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		out[m.Key] = true
	}
	return out
}

func (a *App) dataMountWriters(ctx context.Context, hostID int64, mounts []DataProtectionMount) ([]string, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return nil, err
	}
	cs, err := a.Docker.ListContainers(ctx, h.Endpoint)
	if err != nil {
		return nil, err
	}
	keys := mountKeySet(mounts)
	writers := []string{}
	for _, c := range cs {
		if managed, _ := a.systemManagedContainer(c.Name); managed {
			continue
		}
		ins, e := a.inspectOne(ctx, hostID, c.Name)
		if e != nil {
			return nil, fmt.Errorf("inspect mount writer %s: %w", c.Name, e)
		}
		if !ins.State.Running {
			continue
		}
		for _, m := range ins.Mounts {
			if !m.RW {
				continue
			}
			if keys[dataMountKey(m.Type, m.Name, m.Source)] {
				if !containsString(writers, c.Name) {
					writers = append(writers, c.Name)
				}
				break
			}
		}
	}
	sort.Strings(writers)
	return writers, nil
}

func (a *App) stopDataWriters(ctx context.Context, hostID int64, mounts []DataProtectionMount) ([]string, error) {
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return nil, err
	}
	writers, err := a.dataMountWriters(ctx, hostID, mounts)
	if err != nil {
		return nil, err
	}
	stopped := []string{}
	for _, name := range writers {
		if _, err := a.Docker.Run(ctx, h.Endpoint, "stop", "--time", "30", name); err != nil {
			_ = a.restartDataWriters(context.Background(), h.Endpoint, stopped)
			return stopped, fmt.Errorf("stop data writer %s: %w", name, err)
		}
		stopped = append(stopped, name)
	}
	return stopped, nil
}

func (a *App) restartDataWriters(ctx context.Context, endpoint string, names []string) error {
	pending := append([]string(nil), names...)
	lastErr := map[string]string{}
	for pass := 0; pass < 3 && len(pending) > 0; pass++ {
		next := []string{}
		for _, name := range pending {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if _, err := a.Docker.Run(ctx, endpoint, "start", name); err != nil {
				lastErr[name] = err.Error()
				next = append(next, name)
			} else {
				delete(lastErr, name)
			}
		}
		pending = next
		if len(pending) > 0 && pass < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if len(pending) > 0 {
		errs := make([]string, 0, len(pending))
		for _, name := range pending {
			errs = append(errs, fmt.Sprintf("%s: %s", name, firstNonEmpty(lastErr[name], "start failed")))
		}
		return fmt.Errorf("restart data writer(s): %s", strings.Join(errs, "; "))
	}
	return nil
}

func (a *App) ensureDataWritersRunning(ctx context.Context, hostID int64, names []string) error {
	if len(names) == 0 {
		return nil
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	pending := []string{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		ins, inspectErr := a.inspectOne(ctx, hostID, name)
		if inspectErr == nil && (ins.State.Running || ins.State.Restarting) {
			continue
		}
		pending = append(pending, name)
	}
	if len(pending) == 0 {
		return nil
	}
	return a.restartDataWriters(ctx, h.Endpoint, pending)
}

func helperMountArg(m DataProtectionMount, target string, readonly bool) (string, error) {
	if strings.Contains(m.Source, ",") || strings.Contains(m.Name, ",") {
		return "", fmt.Errorf("mount path/name containing comma is not supported for data protection: %s", m.Key)
	}
	ro := ""
	if readonly {
		ro = ",readonly"
	}
	if m.Type == "volume" {
		if strings.TrimSpace(m.Name) == "" {
			return "", fmt.Errorf("named volume is missing its Docker volume name")
		}
		return "type=volume,source=" + m.Name + ",target=" + target + ro, nil
	}
	if m.Type == "bind" {
		if strings.TrimSpace(m.Source) == "" {
			return "", fmt.Errorf("bind mount is missing its source path")
		}
		return "type=bind,source=" + m.Source + ",target=" + target + ro, nil
	}
	return "", fmt.Errorf("unsupported mount type %s", m.Type)
}

func dataRestoreBase(scopeType, scopeKey, snapshotID string) string {
	return "restore-points/" + sanitizeFilename(scopeType) + "/" + sanitizeFilename(scopeKey) + "/" + sanitizeFilename(snapshotID)
}

func (a *App) captureDataArchive(ctx context.Context, hostID int64, endpoint string, profile db.DataProtectionProfile, snapshotID string, mounts []DataProtectionMount) (dataArchiveManifest, error) {
	manifest := dataArchiveManifest{SchemaVersion: 1, HostID: hostID, ScopeType: profile.ScopeType, ScopeKey: profile.ScopeKey, SnapshotID: snapshotID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Entries: []dataArchiveEntry{}}
	if len(mounts) == 0 {
		return manifest, nil
	}
	if err := a.ensureRestoreVolume(ctx, endpoint); err != nil {
		return manifest, err
	}
	args := append([]string{"run"}, dataHelperArgs(hostID, "data-capture")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup")
	base := dataRestoreBase(profile.ScopeType, profile.ScopeKey, snapshotID)
	commands := []string{"set -eu", "mkdir -p /backup/" + base + "/data"}
	for i, m := range mounts {
		target := fmt.Sprintf("/source/%d", i)
		ma, err := helperMountArg(m, target, true)
		if err != nil {
			return manifest, err
		}
		args = append(args, "--mount", ma)
		name := dataMountArchiveName(m.Key)
		archive := base + "/data/" + name
		tmp := archive + ".tmp"
		commands = append(commands,
			fmt.Sprintf("tar -C %s -czf /backup/%s .", target, tmp),
			fmt.Sprintf("test -s /backup/%s", tmp),
			fmt.Sprintf("mv /backup/%s /backup/%s", tmp, archive),
			fmt.Sprintf("size=$(stat -c %%s /backup/%s); sum=$(sha256sum /backup/%s | awk '{print $1}'); echo ENTRY\\|%d\\|$size\\|$sum", archive, archive, i),
		)
		manifest.Entries = append(manifest.Entries, dataArchiveEntry{Key: m.Key, Type: m.Type, Name: m.Name, Source: m.Source, Destinations: m.Destinations, Owners: m.Owners, StorageClass: m.StorageClass, FSType: m.FSType, Archive: archive})
	}
	args = append(args, dataHelperImage, "sh", "-c", strings.Join(commands, "; "))
	out, err := a.Docker.Run(ctx, endpoint, args...)
	if err != nil {
		return manifest, err
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 4 || parts[0] != "ENTRY" {
			continue
		}
		i, _ := strconv.Atoi(parts[1])
		if i < 0 || i >= len(manifest.Entries) {
			continue
		}
		sz, _ := strconv.ParseInt(parts[2], 10, 64)
		manifest.Entries[i].SizeBytes = sz
		manifest.Entries[i].SHA256 = parts[3]
		manifest.TotalBytes += sz
	}
	for _, e := range manifest.Entries {
		if e.SizeBytes <= 0 || strings.TrimSpace(e.SHA256) == "" {
			return manifest, fmt.Errorf("data archive validation output is incomplete for %s", e.Key)
		}
	}
	return manifest, nil
}

func (a *App) removeDataRestoreArtifacts(ctx context.Context, hostID int64, manifest dataArchiveManifest) error {
	if manifest.SnapshotID == "" || manifest.ScopeKey == "" {
		return nil
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	if err := a.ensureRestoreVolume(ctx, h.Endpoint); err != nil {
		return err
	}
	base := dataRestoreBase(manifest.ScopeType, manifest.ScopeKey, manifest.SnapshotID)
	args := append([]string{"run"}, dataHelperArgs(hostID, "data-cleanup")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup", dataHelperImage, "sh", "-c", "rm -rf /backup/"+base)
	_, err = a.Docker.Run(ctx, h.Endpoint, args...)
	if err == nil {
		_ = a.Store.InvalidateHostStorageCache(context.Background(), hostID)
	}
	return err
}

func decodeDataManifest(raw string) (dataArchiveManifest, error) {
	var m dataArchiveManifest
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return m, err
	}
	return m, nil
}

func (a *App) validateDataArchive(ctx context.Context, rp db.RestorePoint) error {
	manifest, err := decodeDataManifest(rp.DataManifestJSON)
	if err != nil {
		return err
	}
	if len(manifest.Entries) == 0 {
		return nil
	}
	h, err := a.Store.Host(ctx, rp.HostID)
	if err != nil {
		return err
	}
	commands := []string{"set -eu"}
	for _, e := range manifest.Entries {
		if e.Archive == "" || e.SHA256 == "" {
			return fmt.Errorf("data archive metadata incomplete for %s", e.Key)
		}
		commands = append(commands, fmt.Sprintf("test -s /backup/%s", e.Archive), fmt.Sprintf("echo '%s  /backup/%s' | sha256sum -c - >/dev/null", e.SHA256, e.Archive))
	}
	args := append([]string{"run"}, dataHelperArgs(rp.HostID, "data-validate")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup,readonly", dataHelperImage, "sh", "-c", strings.Join(commands, "; "))
	_, err = a.Docker.Run(ctx, h.Endpoint, args...)
	return err
}

func (a *App) restoreDataArchivesWithSafety(ctx context.Context, rp db.RestorePoint) ([]string, dataArchiveManifest, error) {
	oldManifest, err := decodeDataManifest(rp.DataManifestJSON)
	if err != nil || len(oldManifest.Entries) == 0 {
		return nil, dataArchiveManifest{}, err
	}
	_, currentMounts, err := a.dataProtectionInventory(ctx, rp.HostID, rp.ContainerName, oldManifest.ScopeType)
	if err != nil {
		return nil, dataArchiveManifest{}, err
	}
	currentByKey := map[string]DataProtectionMount{}
	for _, m := range currentMounts {
		currentByKey[m.Key] = m
	}
	selected := make([]DataProtectionMount, 0, len(oldManifest.Entries))
	for _, e := range oldManifest.Entries {
		m, ok := currentByKey[e.Key]
		if !ok {
			// Use the retained source only when the current runtime no longer exposes
			// the mount. Docker --mount will fail safely if the source/volume vanished.
			m = DataProtectionMount{Key: e.Key, Type: e.Type, Name: e.Name, Source: e.Source, Destinations: e.Destinations, Owners: e.Owners, StorageClass: e.StorageClass, FSType: e.FSType, Writable: true}
		}
		selected = append(selected, m)
	}
	h, err := a.Store.Host(ctx, rp.HostID)
	if err != nil {
		return nil, dataArchiveManifest{}, err
	}
	stopped, err := a.stopDataWriters(ctx, rp.HostID, selected)
	if err != nil {
		return stopped, dataArchiveManifest{}, err
	}
	if err := a.ensureRestoreVolume(ctx, h.Endpoint); err != nil {
		_ = a.restartDataWriters(context.Background(), h.Endpoint, stopped)
		return stopped, dataArchiveManifest{}, err
	}
	safetyID := "safety-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	safety := dataArchiveManifest{SchemaVersion: 1, HostID: rp.HostID, ScopeType: oldManifest.ScopeType, ScopeKey: oldManifest.ScopeKey, SnapshotID: safetyID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Entries: []dataArchiveEntry{}}
	safetyBase := dataRestoreBase(oldManifest.ScopeType, oldManifest.ScopeKey, safetyID)
	args := append([]string{"run"}, dataHelperArgs(rp.HostID, "data-restore")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup")
	cmd := []string{"set -eu", "mkdir -p /backup/" + safetyBase + "/data"}
	// Verify every retained archive before touching current data.
	for _, e := range oldManifest.Entries {
		cmd = append(cmd, fmt.Sprintf("test -s /backup/%s", e.Archive), fmt.Sprintf("echo '%s  /backup/%s' | sha256sum -c - >/dev/null", e.SHA256, e.Archive))
	}
	for i, m := range selected {
		target := fmt.Sprintf("/source/%d", i)
		ma, e := helperMountArg(m, target, false)
		if e != nil {
			_ = a.restartDataWriters(context.Background(), h.Endpoint, stopped)
			return stopped, safety, e
		}
		args = append(args, "--mount", ma)
		name := dataMountArchiveName(m.Key)
		archive := safetyBase + "/data/" + name
		cmd = append(cmd, fmt.Sprintf("tar -C %s -czf /backup/%s .", target, archive), fmt.Sprintf("test -s /backup/%s", archive))
		safety.Entries = append(safety.Entries, dataArchiveEntry{Key: m.Key, Type: m.Type, Name: m.Name, Source: m.Source, Destinations: m.Destinations, Owners: m.Owners, StorageClass: m.StorageClass, FSType: m.FSType, Archive: archive})
	}
	// Capture safety metadata before restore, then restore old data. If any restore
	// step fails, put the just-captured current data back before returning failure.
	for i, e := range safety.Entries {
		cmd = append(cmd, fmt.Sprintf("size=$(stat -c %%s /backup/%s); sum=$(sha256sum /backup/%s | awk '{print $1}'); echo SAFE\\|%d\\|$size\\|$sum", e.Archive, e.Archive, i))
	}
	restoreParts := []string{}
	for i, e := range oldManifest.Entries {
		target := fmt.Sprintf("/source/%d", i)
		restoreParts = append(restoreParts, fmt.Sprintf("rm -rf %s/* %s/.[!.]* %s/..?* 2>/dev/null || true; tar -xzf /backup/%s -C %s", target, target, target, e.Archive, target))
	}
	recoverParts := []string{}
	for i, e := range safety.Entries {
		target := fmt.Sprintf("/source/%d", i)
		recoverParts = append(recoverParts, fmt.Sprintf("rm -rf %s/* %s/.[!.]* %s/..?* 2>/dev/null || true; tar -xzf /backup/%s -C %s", target, target, target, e.Archive, target))
	}
	cmd = append(cmd, "if ! ("+strings.Join(restoreParts, "; ")+"); then ("+strings.Join(recoverParts, "; ")+" || true); exit 42; fi")
	args = append(args, dataHelperImage, "sh", "-c", strings.Join(cmd, "; "))
	out, runErr := a.Docker.Run(ctx, h.Endpoint, args...)
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) == 4 && parts[0] == "SAFE" {
			i, _ := strconv.Atoi(parts[1])
			if i >= 0 && i < len(safety.Entries) {
				sz, _ := strconv.ParseInt(parts[2], 10, 64)
				safety.Entries[i].SizeBytes = sz
				safety.Entries[i].SHA256 = parts[3]
				safety.TotalBytes += sz
			}
		}
	}
	if runErr != nil {
		_ = a.restartDataWriters(context.Background(), h.Endpoint, stopped)
		return stopped, safety, runErr
	}
	return stopped, safety, nil
}

func (a *App) restoreSafetyData(ctx context.Context, hostID int64, manifest dataArchiveManifest) error {
	if len(manifest.Entries) == 0 {
		return nil
	}
	h, err := a.Store.Host(ctx, hostID)
	if err != nil {
		return err
	}
	mounts := make([]DataProtectionMount, 0, len(manifest.Entries))
	for _, e := range manifest.Entries {
		mounts = append(mounts, DataProtectionMount{Key: e.Key, Type: e.Type, Name: e.Name, Source: e.Source, Writable: true})
	}
	args := append([]string{"run"}, dataHelperArgs(hostID, "safety-restore")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup,readonly")
	cmd := []string{"set -eu"}
	for i, e := range manifest.Entries {
		target := fmt.Sprintf("/source/%d", i)
		ma, er := helperMountArg(mounts[i], target, false)
		if er != nil {
			return er
		}
		args = append(args, "--mount", ma)
		cmd = append(cmd, fmt.Sprintf("echo '%s  /backup/%s' | sha256sum -c - >/dev/null", e.SHA256, e.Archive), fmt.Sprintf("rm -rf %s/* %s/.[!.]* %s/..?* 2>/dev/null || true", target, target, target), fmt.Sprintf("tar -xzf /backup/%s -C %s", e.Archive, target))
	}
	args = append(args, dataHelperImage, "sh", "-c", strings.Join(cmd, "; "))
	_, err = a.Docker.Run(ctx, h.Endpoint, args...)
	return err
}

func (a *App) dataArchiveExists(ctx context.Context, rp db.RestorePoint) error {
	manifest, err := decodeDataManifest(rp.DataManifestJSON)
	if err != nil {
		return err
	}
	if len(manifest.Entries) == 0 {
		return nil
	}
	h, err := a.Store.Host(ctx, rp.HostID)
	if err != nil {
		return err
	}
	cmd := []string{"set -eu"}
	for _, e := range manifest.Entries {
		cmd = append(cmd, "test -s /backup/"+e.Archive)
	}
	args := append([]string{"run"}, dataHelperArgs(rp.HostID, "data-check")...)
	args = append(args, "--rm", "--network", "none", "--read-only", "--mount", "type=volume,source="+dataRestoreVolume+",target=/backup,readonly", dataHelperImage, "sh", "-c", strings.Join(cmd, "; "))
	_, err = a.Docker.Run(ctx, h.Endpoint, args...)
	return err
}

func dataMountsFromManifest(m dataArchiveManifest) []DataProtectionMount {
	out := make([]DataProtectionMount, 0, len(m.Entries))
	for _, e := range m.Entries {
		out = append(out, DataProtectionMount{Key: e.Key, Type: e.Type, Name: e.Name, Source: e.Source, Destinations: e.Destinations, Owners: e.Owners, Writable: true, StorageClass: e.StorageClass, FSType: e.FSType})
	}
	return out
}

func mergeNames(xs ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range xs {
		for _, x := range list {
			if x = strings.TrimSpace(x); x != "" && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	return out
}
