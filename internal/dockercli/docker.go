package dockercli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/watchtower-ui/watchtower-ui/internal/db"
)

type Client struct {
	Binary        string
	Logger        *slog.Logger
	WorkerImage   string
	WorkerNetwork string
	WorkerPort    string
	WorkerVersion string
	labelMu       sync.Mutex
	labelCache    map[string]cachedLabels
}

type cachedLabels struct {
	Labels map[string]string
	At     time.Time
}

type Container struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Image        string `json:"image"`
	ImageID      string `json:"image_id"`
	State        string `json:"state"`
	Status       string `json:"status"`
	Ports        string `json:"ports"`
	Networks     string `json:"networks"`
	CreatedAt    string `json:"created_at"`
	StackName    string `json:"stack_name"`
	StackService string `json:"stack_service"`
	StackType    string `json:"stack_type"`
}

type ImagePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	ImageID      string `json:"image_id"`
}

type ImageSummary struct {
	ID                string   `json:"id"`
	RepoTags          []string `json:"repo_tags"`
	SizeBytes         int64    `json:"size_bytes"`
	Created           string   `json:"created"`
	InUse             bool     `json:"in_use"`
	Unused            bool     `json:"unused"`
	Dangling          bool     `json:"dangling"`
	RollbackProtected bool     `json:"rollback_protected,omitempty"`
}

type NetworkSummary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Driver            string `json:"driver"`
	Scope             string `json:"scope"`
	CreatedAt         string `json:"created_at"`
	Internal          bool   `json:"internal"`
	Ingress           bool   `json:"ingress"`
	InUse             bool   `json:"in_use"`
	RefCount          int    `json:"ref_count"`
	System            bool   `json:"system"`
	RollbackProtected bool   `json:"rollback_protected,omitempty"`
	Unused            bool   `json:"unused"`
}

type VolumeSummary struct {
	Name                string            `json:"name"`
	Driver              string            `json:"driver"`
	Scope               string            `json:"scope"`
	Mountpoint          string            `json:"mountpoint"`
	CreatedAt           string            `json:"created_at"`
	Labels              map[string]string `json:"labels,omitempty"`
	InUse               bool              `json:"in_use"`
	RefCount            int               `json:"ref_count"`
	ReferenceContainers []string          `json:"reference_containers,omitempty"`
	UsageKnown          bool              `json:"usage_known"`
	Anonymous           bool              `json:"anonymous"`
	Unused              bool              `json:"unused"`
	RollbackProtected   bool              `json:"rollback_protected,omitempty"`
	RetainedSnapshots   int               `json:"retained_snapshots,omitempty"`
	InspectError        string            `json:"inspect_error,omitempty"`
}

type VolumePruneResult struct {
	RemovedVolumes        []string `json:"removed_volumes"`
	ProtectedVolumes      int      `json:"protected_volumes"`
	FailedVolumes         []string `json:"failed_volumes,omitempty"`
	BeforeUnusedAnonymous int      `json:"before_unused_anonymous"`
	AfterUnusedAnonymous  int      `json:"after_unused_anonymous"`
}

type HostOverview struct {
	Name                    string         `json:"name"`
	DockerVersion           string         `json:"docker_version"`
	OperatingSystem         string         `json:"operating_system"`
	OSType                  string         `json:"os_type"`
	Architecture            string         `json:"architecture"`
	KernelVersion           string         `json:"kernel_version"`
	StorageDriver           string         `json:"storage_driver"`
	DockerRootDir           string         `json:"docker_root_dir"`
	CPUs                    int            `json:"cpus"`
	MemoryTotalBytes        int64          `json:"memory_total_bytes"`
	MemorySource            string         `json:"memory_source,omitempty"`
	MemoryDiagnostic        string         `json:"memory_diagnostic,omitempty"`
	ContainersTotal         int            `json:"containers_total"`
	ContainersRunning       int            `json:"containers_running"`
	ContainersStopped       int            `json:"containers_stopped"`
	ImagesTotal             int            `json:"images_total"`
	ImagesInUse             int            `json:"images_in_use"`
	ImagesUnused            int            `json:"images_unused"`
	ImagesDangling          int            `json:"images_dangling"`
	ImageDiskBytes          int64          `json:"image_disk_bytes"`
	ImageReclaimableBytes   int64          `json:"image_reclaimable_bytes"`
	ImageDiskExact          bool           `json:"image_disk_exact"`
	ImagesRollbackProtected int            `json:"images_rollback_protected"`
	ImagesCleanupEligible   int            `json:"images_cleanup_eligible"`
	BuildCacheBytes         int64          `json:"build_cache_bytes"`
	BuildCacheReclaimable   int64          `json:"build_cache_reclaimable_bytes"`
	ContainerCPUPercent     float64        `json:"container_cpu_percent"`
	ContainerMemoryBytes    int64          `json:"container_memory_bytes"`
	ContainerMemoryPercent  float64        `json:"container_memory_percent"`
	ContainerStatsAvailable bool           `json:"container_stats_available"`
	ContainerStatsError     string         `json:"container_stats_error,omitempty"`
	Images                  []ImageSummary `json:"images,omitempty"`
	CollectedAt             string         `json:"collected_at"`
}

type ImagePruneResult struct {
	RemovedImages     int          `json:"removed_images"`
	FailedImages      int          `json:"failed_images"`
	ProtectedImages   int          `json:"protected_images"`
	ReclaimedBytes    int64        `json:"reclaimed_bytes"`
	BeforeUnused      int          `json:"before_unused"`
	AfterUnused       int          `json:"after_unused"`
	BeforeReclaimable int64        `json:"before_reclaimable_bytes"`
	AfterReclaimable  int64        `json:"after_reclaimable_bytes"`
	Overview          HostOverview `json:"overview"`
}

type NetworkPruneResult struct {
	RemovedNetworks   []string `json:"removed_networks"`
	FailedNetworks    []string `json:"failed_networks"`
	ProtectedNetworks int      `json:"protected_networks"`
}

type BuildCachePruneResult struct {
	ReclaimedBytes    int64 `json:"reclaimed_bytes"`
	BeforeBytes       int64 `json:"before_bytes"`
	BeforeReclaimable int64 `json:"before_reclaimable_bytes"`
	AfterBytes        int64 `json:"after_bytes"`
	AfterReclaimable  int64 `json:"after_reclaimable_bytes"`
}

type EventWatcher struct {
	mu     sync.Mutex
	cancel map[int64]context.CancelFunc
}

func New(logger *slog.Logger) *Client {
	return &Client{Binary: "docker", Logger: logger, WorkerImage: "nickfedor/watchtower:latest", WorkerNetwork: "vibewatch-internal", WorkerPort: "8080", WorkerVersion: "0.4.5", labelCache: map[string]cachedLabels{}}
}

func (c *Client) cmd(ctx context.Context, endpoint string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if endpoint != "" {
		full = append(full, "--host", endpoint)
	}
	full = append(full, args...)
	return exec.CommandContext(ctx, c.Binary, full...)
}
func (c *Client) run(ctx context.Context, endpoint string, args ...string) (string, error) {
	cmd := c.cmd(ctx, endpoint, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(safeArgs(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Run executes a Docker CLI operation against a configured endpoint. It is
// intentionally limited to internal controller code and retains the same
// redaction/error handling as all other Docker operations.
func (c *Client) Run(ctx context.Context, endpoint string, args ...string) (string, error) {
	return c.run(ctx, endpoint, args...)
}

func (c *Client) ImageExists(ctx context.Context, endpoint, ref string) bool {
	_, err := c.run(ctx, endpoint, "image", "inspect", ref)
	return err == nil
}

func (c *Client) PullRemoteImage(ctx context.Context, endpoint, ref string) error {
	_, err := c.run(ctx, endpoint, "pull", ref)
	return err
}

func safeArgs(args []string) []string {
	out := append([]string(nil), args...)
	redactNext := false
	for i := 0; i < len(out); i++ {
		if redactNext {
			out[i] = "REDACTED"
			redactNext = false
			continue
		}
		switch out[i] {
		case "--env", "-e":
			redactNext = true
			continue
		}
		upper := strings.ToUpper(out[i])
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "PASSWORD=") || strings.Contains(upper, "SECRET=") || strings.Contains(upper, "KEY=") {
			if eq := strings.Index(out[i], "="); eq >= 0 {
				out[i] = out[i][:eq+1] + "REDACTED"
			} else {
				out[i] = "REDACTED"
			}
		}
	}
	return out
}

func (c *Client) Ping(ctx context.Context, endpoint string) (string, error) {
	return c.run(ctx, endpoint, "version", "--format", "{{.Server.Version}}")
}

var dockerSizePattern = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([kmgtpe]?i?b)`)

func parseDockerBytes(v string) int64 {
	v = strings.TrimSpace(v)
	m := dockerSizePattern.FindStringSubmatch(v)
	if len(m) != 3 {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	u := strings.ToUpper(m[2])
	mults := map[string]float64{
		"B":  1,
		"KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15, "EB": 1e18,
		"KIB": 1024, "MIB": 1024 * 1024, "GIB": 1024 * 1024 * 1024, "TIB": 1024 * 1024 * 1024 * 1024,
	}
	return int64(n * mults[u])
}

func parsePercent(v string) float64 {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "%"))
	n, _ := strconv.ParseFloat(v, 64)
	return n
}

func parseMemoryValue(v string) int64 {
	v = strings.TrimSpace(strings.Trim(v, `"`))
	if v == "" || v == "<no value>" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return n
	}
	return parseDockerBytes(v)
}

func chooseMemoryTotal(infoBytes, statsLimitBytes int64) (int64, string, string) {
	parts := make([]string, 0, 2)
	if infoBytes > 0 {
		parts = append(parts, fmt.Sprintf("docker-info=%d", infoBytes))
	}
	if statsLimitBytes > 0 {
		parts = append(parts, fmt.Sprintf("docker-stats-limit=%d", statsLimitBytes))
	}
	// Docker daemon MemTotal is the canonical host capacity. Container stats
	// limits are only a fallback: unlimited containers can expose cgroup sentinel
	// values or other limits that are much larger than physical host RAM.
	if infoBytes > 0 {
		return infoBytes, "docker-info", strings.Join(parts, ", ")
	}
	// Reject obviously non-physical cgroup sentinel values when Docker info is
	// unavailable. 1 PiB is far beyond the hosts Vibewatch targets and protects
	// the UI from displaying values such as 8 EiB for an unlimited container.
	if statsLimitBytes > 0 && statsLimitBytes < 1<<50 {
		return statsLimitBytes, "docker-stats-limit", strings.Join(parts, ", ")
	}
	if statsLimitBytes >= 1<<50 {
		parts = append(parts, "stats-limit-rejected-as-sentinel")
	}
	return 0, "unavailable", strings.Join(parts, ", ")
}

func (c *Client) imageInventory(ctx context.Context, endpoint string) ([]ImageSummary, error) {
	out, err := c.run(ctx, endpoint, "image", "ls", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ids := make([]string, 0)
	for _, id := range strings.Fields(out) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []ImageSummary{}, nil
	}
	args := append([]string{"image", "inspect"}, ids...)
	inspectOut, err := c.run(ctx, endpoint, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Size     int64    `json:"Size"`
		Created  string   `json:"Created"`
	}
	if err := json.Unmarshal([]byte(inspectOut), &raw); err != nil {
		return nil, fmt.Errorf("decode image inventory: %w", err)
	}

	referenced := map[string]bool{}
	containerIDsOut, _ := c.run(ctx, endpoint, "ps", "-aq", "--no-trunc")
	containerIDs := strings.Fields(containerIDsOut)
	if len(containerIDs) > 0 {
		cargs := append([]string{"inspect", "--format", "{{.Image}}"}, containerIDs...)
		if refsOut, e := c.run(ctx, endpoint, cargs...); e == nil {
			for _, ref := range strings.Fields(refsOut) {
				referenced[strings.TrimSpace(ref)] = true
			}
		}
	}

	result := make([]ImageSummary, 0, len(raw))
	for _, img := range raw {
		tags := make([]string, 0, len(img.RepoTags))
		for _, tag := range img.RepoTags {
			if tag != "" && tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		sort.Strings(tags)
		inUse := referenced[img.ID]
		result = append(result, ImageSummary{ID: img.ID, RepoTags: tags, SizeBytes: img.Size, Created: img.Created, InUse: inUse, Unused: !inUse, Dangling: len(tags) == 0})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Unused != result[j].Unused {
			return result[i].Unused
		}
		li, lj := result[i].ID, result[j].ID
		if len(result[i].RepoTags) > 0 {
			li = result[i].RepoTags[0]
		}
		if len(result[j].RepoTags) > 0 {
			lj = result[j].RepoTags[0]
		}
		return li < lj
	})
	return result, nil
}

func (c *Client) ImageInventory(ctx context.Context, endpoint string) ([]ImageSummary, error) {
	return c.imageInventory(ctx, endpoint)
}

func (c *Client) imageDiskUsage(ctx context.Context, endpoint string) (total, reclaimable int64, exact bool) {
	out, err := c.run(ctx, endpoint, "system", "df", "--format", "{{json .}}")
	if err != nil || strings.TrimSpace(out) == "" {
		return 0, 0, false
	}
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		var row struct {
			Type        string `json:"Type"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(scan.Text()), &row) == nil && strings.EqualFold(strings.TrimSpace(row.Type), "Images") {
			return parseDockerBytes(row.Size), parseDockerBytes(row.Reclaimable), true
		}
	}
	return 0, 0, false
}

func (c *Client) buildCacheDiskUsage(ctx context.Context, endpoint string) (total, reclaimable int64) {
	out, err := c.run(ctx, endpoint, "system", "df", "--format", "{{json .}}")
	if err != nil || strings.TrimSpace(out) == "" {
		return 0, 0
	}
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		var row struct {
			Type        string `json:"Type"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(scan.Text()), &row) == nil && strings.EqualFold(strings.TrimSpace(row.Type), "Build Cache") {
			return parseDockerBytes(row.Size), parseDockerBytes(row.Reclaimable)
		}
	}
	return 0, 0
}

func (c *Client) aggregateContainerStats(ctx context.Context, endpoint string) (cpu float64, mem int64, maxLimit int64, available bool, errText string) {
	out, err := c.run(ctx, endpoint, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return 0, 0, 0, false, err.Error()
	}
	available = true
	if strings.TrimSpace(out) == "" {
		return 0, 0, 0, true, ""
	}
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		var row struct {
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
		}
		if json.Unmarshal([]byte(scan.Text()), &row) != nil {
			continue
		}
		cpu += parsePercent(row.CPUPerc)
		parts := strings.SplitN(row.MemUsage, "/", 2)
		if len(parts) > 0 {
			mem += parseDockerBytes(strings.TrimSpace(parts[0]))
		}
		if len(parts) == 2 {
			if limit := parseDockerBytes(strings.TrimSpace(parts[1])); limit > maxLimit {
				maxLimit = limit
			}
		}
	}
	return cpu, mem, maxLimit, available, ""
}

var anonymousVolumeNamePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func volumeIsAnonymous(name string, labels map[string]string) bool {
	if labels != nil {
		if _, ok := labels["com.docker.volume.anonymous"]; ok {
			return true
		}
	}
	// Older engines may not expose the daemon's anonymous marker through every
	// CLI/API combination. Docker-generated anonymous volume names are 64-hex
	// identifiers, so retain this conservative compatibility fallback.
	return anonymousVolumeNamePattern.MatchString(strings.TrimSpace(strings.ToLower(name)))
}

type volumeInspectRow struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
}

type containerMountInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Mounts []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
}

const dockerInspectBatchSize = 48

func (c *Client) VolumeInventory(ctx context.Context, endpoint string) ([]VolumeSummary, error) {
	out, err := c.run(ctx, endpoint, "volume", "ls", "-q")
	if err != nil {
		return nil, err
	}
	seenNames := map[string]bool{}
	names := make([]string, 0)
	for _, name := range strings.Fields(out) {
		name = strings.TrimSpace(name)
		if name != "" && !seenNames[name] {
			seenNames[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return []VolumeSummary{}, nil
	}

	inspected := map[string]volumeInspectRow{}
	inspectErrors := map[string]string{}
	for start := 0; start < len(names); start += dockerInspectBatchSize {
		end := start + dockerInspectBatchSize
		if end > len(names) {
			end = len(names)
		}
		batch := names[start:end]
		args := append([]string{"volume", "inspect"}, batch...)
		inspectOut, batchErr := c.run(ctx, endpoint, args...)
		if batchErr == nil {
			var rows []volumeInspectRow
			if jsonErr := json.Unmarshal([]byte(inspectOut), &rows); jsonErr == nil {
				for _, row := range rows {
					if strings.TrimSpace(row.Name) != "" {
						inspected[row.Name] = row
					}
				}
				continue
			}
		}
		// A stale plugin-backed volume or a daemon-specific inspect failure must
		// not hide the rest of this host's inventory. Retry this batch one volume
		// at a time and retain a placeholder for names that still cannot be read.
		for _, name := range batch {
			oneOut, oneErr := c.run(ctx, endpoint, "volume", "inspect", name)
			if oneErr != nil {
				inspectErrors[name] = oneErr.Error()
				continue
			}
			var rows []volumeInspectRow
			if jsonErr := json.Unmarshal([]byte(oneOut), &rows); jsonErr != nil || len(rows) == 0 {
				if jsonErr != nil {
					inspectErrors[name] = "decode volume inspect: " + jsonErr.Error()
				} else {
					inspectErrors[name] = "volume inspect returned no data"
				}
				continue
			}
			inspected[name] = rows[0]
		}
	}

	refs := map[string]int{}
	refContainers := map[string]map[string]bool{}
	usageKnown := map[string]bool{}
	for _, name := range names {
		usageKnown[name] = true
		refContainers[name] = map[string]bool{}
	}

	usageScanComplete := true
	containerIDsOut, psErr := c.run(ctx, endpoint, "ps", "-aq", "--no-trunc")
	if psErr != nil {
		usageScanComplete = false
	} else {
		containerIDs := strings.Fields(containerIDsOut)
		consume := func(containers []containerMountInspect) {
			for _, ctr := range containers {
				seen := map[string]bool{}
				containerName := strings.TrimPrefix(strings.TrimSpace(ctr.Name), "/")
				if containerName == "" {
					containerName = strings.TrimSpace(ctr.ID)
				}
				for _, m := range ctr.Mounts {
					volumeName := strings.TrimSpace(m.Name)
					if m.Type == "volume" && volumeName != "" && !seen[volumeName] {
						refs[volumeName]++
						if _, ok := refContainers[volumeName]; !ok {
							refContainers[volumeName] = map[string]bool{}
						}
						if containerName != "" {
							refContainers[volumeName][containerName] = true
						}
						seen[volumeName] = true
					}
				}
			}
		}
		for start := 0; start < len(containerIDs); start += dockerInspectBatchSize {
			end := start + dockerInspectBatchSize
			if end > len(containerIDs) {
				end = len(containerIDs)
			}
			batch := containerIDs[start:end]
			args := append([]string{"inspect"}, batch...)
			mountsOut, batchErr := c.run(ctx, endpoint, args...)
			if batchErr == nil {
				var containers []containerMountInspect
				if jsonErr := json.Unmarshal([]byte(mountsOut), &containers); jsonErr == nil {
					consume(containers)
					continue
				}
			}
			for _, id := range batch {
				oneOut, oneErr := c.run(ctx, endpoint, "inspect", id)
				if oneErr != nil {
					usageScanComplete = false
					continue
				}
				var containers []containerMountInspect
				if jsonErr := json.Unmarshal([]byte(oneOut), &containers); jsonErr != nil || len(containers) == 0 {
					usageScanComplete = false
					continue
				}
				consume(containers)
			}
		}
	}

	// If even one container inspect is incomplete, a missing reference could in
	// theory belong to any volume. Instead of marking the whole host unknown,
	// ask Docker's own volume filter for each volume. The calls are bounded and
	// concurrent so remote hosts remain responsive while each result stays
	// independently verifiable.
	if !usageScanComplete {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, volumeName := range names {
			volumeName := volumeName
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					mu.Lock()
					usageKnown[volumeName] = false
					mu.Unlock()
					return
				}
				filterOut, filterErr := c.run(ctx, endpoint, "ps", "-a", "--no-trunc", "--filter", "volume="+volumeName, "--format", "{{.ID}}\t{{.Names}}")
				if filterErr != nil {
					mu.Lock()
					usageKnown[volumeName] = false
					mu.Unlock()
					return
				}
				ids := map[string]bool{}
				containers := map[string]bool{}
				for _, line := range strings.Split(filterOut, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					parts := strings.SplitN(line, "\t", 2)
					id := strings.TrimSpace(parts[0])
					if id != "" {
						ids[id] = true
					}
					if len(parts) > 1 {
						if name := strings.TrimSpace(parts[1]); name != "" {
							containers[name] = true
						}
					}
				}
				mu.Lock()
				refs[volumeName] = len(ids)
				refContainers[volumeName] = containers
				usageKnown[volumeName] = true
				mu.Unlock()
			}()
		}
		wg.Wait()
	}

	result := make([]VolumeSummary, 0, len(names))
	for _, name := range names {
		v, ok := inspected[name]
		if !ok {
			v.Name = name
		}
		refCount := refs[name]
		anon := volumeIsAnonymous(name, v.Labels)
		knownForVolume := usageKnown[name]
		inUse := refCount > 0
		unused := knownForVolume && refCount == 0
		containers := make([]string, 0, len(refContainers[name]))
		for containerName := range refContainers[name] {
			containers = append(containers, containerName)
		}
		sort.Strings(containers)
		result = append(result, VolumeSummary{Name: name, Driver: v.Driver, Scope: v.Scope, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, Labels: v.Labels, InUse: inUse, RefCount: refCount, ReferenceContainers: containers, UsageKnown: knownForVolume, Anonymous: anon, Unused: unused, InspectError: inspectErrors[name]})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UsageKnown != result[j].UsageKnown {
			return !result[i].UsageKnown
		}
		if result[i].Unused != result[j].Unused {
			return result[i].Unused
		}
		if result[i].Anonymous != result[j].Anonymous {
			return result[i].Anonymous
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (c *Client) PruneUnusedAnonymousVolumes(ctx context.Context, endpoint string, protected map[string]bool) (VolumePruneResult, error) {
	before, err := c.VolumeInventory(ctx, endpoint)
	if err != nil {
		return VolumePruneResult{}, err
	}
	beforeCount := 0
	protectedCount := 0
	removed := []string{}
	failed := []string{}
	for _, v := range before {
		if !v.Unused || !v.Anonymous {
			continue
		}
		beforeCount++
		if protected != nil && protected[v.Name] {
			protectedCount++
			continue
		}
		// Do not use `docker volume prune`: it cannot exclude retained rollback
		// volumes. Delete only the inventory items Vibewatch has verified as
		// unused anonymous and not protected.
		if _, removeErr := c.run(ctx, endpoint, "volume", "rm", v.Name); removeErr != nil {
			failed = append(failed, v.Name)
			continue
		}
		removed = append(removed, v.Name)
	}
	if len(failed) > 0 && len(removed) == 0 {
		return VolumePruneResult{}, fmt.Errorf("failed to remove %d unused anonymous volume(s)", len(failed))
	}
	after, err := c.VolumeInventory(ctx, endpoint)
	if err != nil {
		return VolumePruneResult{}, err
	}
	afterCount := 0
	for _, v := range after {
		if v.Unused && v.Anonymous {
			afterCount++
		}
	}
	sort.Strings(removed)
	sort.Strings(failed)
	return VolumePruneResult{RemovedVolumes: removed, ProtectedVolumes: protectedCount, FailedVolumes: failed, BeforeUnusedAnonymous: beforeCount, AfterUnusedAnonymous: afterCount}, nil
}

func (c *Client) RemoveUnusedNamedVolume(ctx context.Context, endpoint, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("volume name is required")
	}
	vols, err := c.VolumeInventory(ctx, endpoint)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.Name != name {
			continue
		}
		if !v.UsageKnown {
			return fmt.Errorf("volume %s usage could not be verified; refusing deletion", name)
		}
		if strings.TrimSpace(v.InspectError) != "" {
			return fmt.Errorf("volume %s metadata could not be inspected; refusing deletion", name)
		}
		if v.InUse {
			return fmt.Errorf("volume %s is still referenced by %d container(s)", name, v.RefCount)
		}
		if v.Anonymous {
			return fmt.Errorf("volume %s is anonymous; use anonymous volume cleanup instead", name)
		}
		_, err := c.run(ctx, endpoint, "volume", "rm", name)
		return err
	}
	return fmt.Errorf("volume %s not found", name)
}

func (c *Client) InspectContainersRaw(ctx context.Context, endpoint string, names ...string) ([]byte, error) {
	clean := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			clean = append(clean, strings.TrimSpace(name))
		}
	}
	if len(clean) == 0 {
		return []byte("[]"), nil
	}
	out, err := c.run(ctx, endpoint, append([]string{"inspect"}, clean...)...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (c *Client) InspectImagesRaw(ctx context.Context, endpoint string, refs ...string) ([]byte, error) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref != "" && !seen[ref] {
			seen[ref] = true
			clean = append(clean, ref)
		}
	}
	if len(clean) == 0 {
		return []byte("[]"), nil
	}
	out, err := c.run(ctx, endpoint, append([]string{"image", "inspect"}, clean...)...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (c *Client) HostOverview(ctx context.Context, endpoint string, includeImages bool) (HostOverview, error) {
	out, err := c.run(ctx, endpoint, "info", "--format", "{{json .}}")
	if err != nil {
		return HostOverview{}, err
	}
	var info struct {
		Name              string `json:"Name"`
		ServerVersion     string `json:"ServerVersion"`
		OperatingSystem   string `json:"OperatingSystem"`
		OSType            string `json:"OSType"`
		Architecture      string `json:"Architecture"`
		KernelVersion     string `json:"KernelVersion"`
		Driver            string `json:"Driver"`
		DockerRootDir     string `json:"DockerRootDir"`
		NCPU              int    `json:"NCPU"`
		MemTotal          int64  `json:"MemTotal"`
		Containers        int    `json:"Containers"`
		ContainersRunning int    `json:"ContainersRunning"`
		ContainersStopped int    `json:"ContainersStopped"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return HostOverview{}, fmt.Errorf("decode docker info: %w", err)
	}
	// Query the numeric daemon capacity values directly as an authoritative
	// fallback/override. This avoids CLI JSON-template variations where MemTotal
	// can otherwise be missing or represented inconsistently.
	if raw, e := c.run(ctx, endpoint, "info", "--format", "{{json .MemTotal}}"); e == nil {
		if v := parseMemoryValue(raw); v > 0 {
			info.MemTotal = v
		}
	}
	if raw, e := c.run(ctx, endpoint, "info", "--format", "{{.NCPU}}"); e == nil {
		if v, pe := strconv.Atoi(strings.TrimSpace(raw)); pe == nil && v > 0 {
			info.NCPU = v
		}
	}
	images, err := c.imageInventory(ctx, endpoint)
	if err != nil {
		return HostOverview{}, err
	}
	o := HostOverview{
		Name: info.Name, DockerVersion: info.ServerVersion, OperatingSystem: info.OperatingSystem, OSType: info.OSType,
		Architecture: info.Architecture, KernelVersion: info.KernelVersion, StorageDriver: info.Driver, DockerRootDir: info.DockerRootDir,
		CPUs: info.NCPU, ContainersTotal: info.Containers, ContainersRunning: info.ContainersRunning,
		ContainersStopped: info.ContainersStopped, ImagesTotal: len(images), CollectedAt: time.Now().UTC().Format(time.RFC3339),
	}
	var virtualTotal, virtualUnused int64
	for _, img := range images {
		virtualTotal += img.SizeBytes
		if img.InUse {
			o.ImagesInUse++
		}
		if img.Unused {
			o.ImagesUnused++
			virtualUnused += img.SizeBytes
		}
		if img.Dangling {
			o.ImagesDangling++
		}
	}
	if total, reclaimable, exact := c.imageDiskUsage(ctx, endpoint); exact {
		o.ImageDiskBytes, o.ImageReclaimableBytes, o.ImageDiskExact = total, reclaimable, true
	} else {
		o.ImageDiskBytes, o.ImageReclaimableBytes, o.ImageDiskExact = virtualTotal, virtualUnused, false
	}
	o.BuildCacheBytes, o.BuildCacheReclaimable = c.buildCacheDiskUsage(ctx, endpoint)
	cpu, usedMem, statsLimit, statsAvailable, statsErr := c.aggregateContainerStats(ctx, endpoint)
	o.MemoryTotalBytes, o.MemorySource, o.MemoryDiagnostic = chooseMemoryTotal(info.MemTotal, statsLimit)
	o.ContainerCPUPercent, o.ContainerMemoryBytes, o.ContainerStatsAvailable, o.ContainerStatsError = cpu, usedMem, statsAvailable, statsErr
	if o.MemoryTotalBytes > 0 {
		o.ContainerMemoryPercent = float64(o.ContainerMemoryBytes) / float64(o.MemoryTotalBytes) * 100
	}
	if includeImages {
		o.Images = images
	}
	return o, nil
}

func parsePruneReclaimed(output string) int64 {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Total reclaimed space:") {
			return parseDockerBytes(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Total reclaimed space:")))
		}
	}
	return 0
}

func (c *Client) PruneUnusedImages(ctx context.Context, endpoint string, protected map[string]bool) (ImagePruneResult, error) {
	before, err := c.HostOverview(ctx, endpoint, true)
	if err != nil {
		return ImagePruneResult{}, err
	}
	result := ImagePruneResult{BeforeUnused: before.ImagesUnused, BeforeReclaimable: before.ImageReclaimableBytes}
	for _, img := range before.Images {
		if !img.Unused {
			continue
		}
		if protected != nil && protected[img.ID] {
			result.ProtectedImages++
			continue
		}
		if _, err := c.run(ctx, endpoint, "image", "rm", "-f", img.ID); err != nil {
			result.FailedImages++
			if c.Logger != nil {
				c.Logger.Warn("unused image cleanup skipped image", "endpoint", endpoint, "image", img.ID, "error", err)
			}
			continue
		}
		result.RemovedImages++
	}
	after, err := c.HostOverview(ctx, endpoint, false)
	if err != nil {
		return ImagePruneResult{}, err
	}
	if before.ImageReclaimableBytes > after.ImageReclaimableBytes {
		result.ReclaimedBytes = before.ImageReclaimableBytes - after.ImageReclaimableBytes
	}
	result.AfterUnused = after.ImagesUnused
	result.AfterReclaimable = after.ImageReclaimableBytes
	result.Overview = after
	return result, nil
}

func (c *Client) NetworkInventory(ctx context.Context, endpoint string) ([]NetworkSummary, error) {
	out, err := c.run(ctx, endpoint, "network", "ls", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	result := make([]NetworkSummary, 0, len(ids))
	for _, id := range ids {
		inspectOut, e := c.run(ctx, endpoint, "network", "inspect", id)
		if e != nil {
			if c.Logger != nil {
				c.Logger.Warn("network inspect failed", "endpoint", endpoint, "network", id, "error", e)
			}
			continue
		}
		var rows []struct {
			ID         string                     `json:"Id"`
			Name       string                     `json:"Name"`
			Driver     string                     `json:"Driver"`
			Scope      string                     `json:"Scope"`
			Created    string                     `json:"Created"`
			Internal   bool                       `json:"Internal"`
			Ingress    bool                       `json:"Ingress"`
			Containers map[string]json.RawMessage `json:"Containers"`
		}
		if json.Unmarshal([]byte(inspectOut), &rows) != nil || len(rows) == 0 {
			continue
		}
		r := rows[0]
		name := strings.TrimSpace(r.Name)
		system := name == "bridge" || name == "host" || name == "none" || name == "docker_gwbridge" || name == c.WorkerNetwork || name == "watchtower-ui-internal" || r.Ingress || strings.EqualFold(strings.TrimSpace(r.Scope), "swarm")
		refs := len(r.Containers)
		result = append(result, NetworkSummary{
			ID: idOr(r.ID, id), Name: name, Driver: r.Driver, Scope: r.Scope, CreatedAt: r.Created,
			Internal: r.Internal, Ingress: r.Ingress, InUse: refs > 0, RefCount: refs, System: system, Unused: refs == 0 && !system,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].System != result[j].System {
			return !result[i].System
		}
		if result[i].Unused != result[j].Unused {
			return result[i].Unused
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func idOr(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func (c *Client) PruneUnusedNetworks(ctx context.Context, endpoint string, protected map[string]bool) (NetworkPruneResult, error) {
	networks, err := c.NetworkInventory(ctx, endpoint)
	if err != nil {
		return NetworkPruneResult{}, err
	}
	result := NetworkPruneResult{RemovedNetworks: []string{}, FailedNetworks: []string{}}
	for _, n := range networks {
		if !n.Unused || n.System {
			continue
		}
		if protected != nil && protected[n.Name] {
			result.ProtectedNetworks++
			continue
		}
		if _, e := c.run(ctx, endpoint, "network", "rm", n.ID); e != nil {
			result.FailedNetworks = append(result.FailedNetworks, n.Name)
			if c.Logger != nil {
				c.Logger.Warn("unused network cleanup skipped network", "endpoint", endpoint, "network", n.Name, "error", e)
			}
			continue
		}
		result.RemovedNetworks = append(result.RemovedNetworks, n.Name)
	}
	return result, nil
}

func (c *Client) PruneBuildCache(ctx context.Context, endpoint string) (BuildCachePruneResult, error) {
	before, beforeReclaimable := c.buildCacheDiskUsage(ctx, endpoint)
	out, err := c.run(ctx, endpoint, "builder", "prune", "-a", "-f")
	if err != nil {
		return BuildCachePruneResult{}, err
	}
	after, afterReclaimable := c.buildCacheDiskUsage(ctx, endpoint)
	reclaimed := parsePruneReclaimed(out)
	if reclaimed == 0 && before > after {
		reclaimed = before - after
	}
	return BuildCachePruneResult{ReclaimedBytes: reclaimed, BeforeBytes: before, BeforeReclaimable: beforeReclaimable, AfterBytes: after, AfterReclaimable: afterReclaimable}, nil
}

func (c *Client) ListContainers(ctx context.Context, endpoint string) ([]Container, error) {
	out, err := c.run(ctx, endpoint, "ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return []Container{}, nil
	}
	var result []Container
	scan := bufio.NewScanner(strings.NewReader(out))
	for scan.Scan() {
		var row map[string]string
		if err := json.Unmarshal([]byte(scan.Text()), &row); err != nil {
			return nil, err
		}
		result = append(result, Container{ID: row["ID"], Name: row["Names"], Image: row["Image"], State: row["State"], Status: row["Status"], Ports: row["Ports"], Networks: row["Networks"], CreatedAt: row["CreatedAt"]})
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		args := []string{"inspect", "--format", "{{json .}}"}
		for _, c := range result {
			args = append(args, c.ID)
		}
		if details, e := c.run(ctx, endpoint, args...); e == nil {
			type inspected struct {
				Image  string
				Labels map[string]string
			}
			byID := map[string]inspected{}
			ds := bufio.NewScanner(strings.NewReader(details))
			for ds.Scan() {
				var row struct {
					ID     string `json:"Id"`
					Image  string `json:"Image"`
					Config struct {
						Labels map[string]string `json:"Labels"`
					} `json:"Config"`
				}
				if json.Unmarshal([]byte(ds.Text()), &row) == nil {
					byID[row.ID] = inspected{Image: row.Image, Labels: row.Config.Labels}
				}
			}
			for i := range result {
				meta := byID[result[i].ID]
				result[i].ImageID = meta.Image
				result[i].StackName, result[i].StackService, result[i].StackType = StackMetadata(meta.Labels)
			}
		}
	}
	return result, nil
}

func StackMetadata(labels map[string]string) (name, service, stackType string) {
	if len(labels) == 0 {
		return "", "", ""
	}
	if project := strings.TrimSpace(labels["com.docker.compose.project"]); project != "" {
		return project, strings.TrimSpace(labels["com.docker.compose.service"]), "compose"
	}
	if namespace := strings.TrimSpace(labels["com.docker.stack.namespace"]); namespace != "" {
		service = strings.TrimSpace(labels["com.docker.swarm.service.name"])
		if strings.HasPrefix(service, namespace+"_") {
			service = strings.TrimPrefix(service, namespace+"_")
		}
		return namespace, service, "swarm"
	}
	if swarmService := strings.TrimSpace(labels["com.docker.swarm.service.name"]); swarmService != "" {
		// Docker Swarm service names generated by a stack conventionally use
		// <stack>_<service>. Only infer the stack when that relationship is visible.
		if parts := strings.SplitN(swarmService, "_", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], "swarm"
		}
	}
	return "", "", ""
}

// ImagePlatform returns the platform and immutable config digest of a local
// Docker image. Docker image IDs are image-config digests and can therefore be
// compared with the config digest of the matching remote platform manifest.
func (c *Client) ImagePlatform(ctx context.Context, endpoint, image string) (ImagePlatform, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return ImagePlatform{}, fmt.Errorf("image reference is empty")
	}
	out, err := c.run(ctx, endpoint, "image", "inspect", image, "--format", "{{json .}}")
	if err != nil {
		return ImagePlatform{}, err
	}
	var raw struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string `json:"Architecture"`
		Variant      string `json:"Variant"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return ImagePlatform{}, fmt.Errorf("decode image platform: %w", err)
	}
	if strings.TrimSpace(raw.ID) == "" {
		return ImagePlatform{}, fmt.Errorf("local image has no image ID")
	}
	if strings.TrimSpace(raw.OS) == "" || strings.TrimSpace(raw.Architecture) == "" {
		return ImagePlatform{}, fmt.Errorf("local image platform is unavailable")
	}
	return ImagePlatform{OS: strings.TrimSpace(raw.OS), Architecture: strings.TrimSpace(raw.Architecture), Variant: strings.TrimSpace(raw.Variant), ImageID: strings.TrimSpace(raw.ID)}, nil
}

func normalizeRepoName(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	if at := strings.Index(v, "@"); at >= 0 {
		v = v[:at]
	}
	lastSlash := strings.LastIndex(v, "/")
	lastColon := strings.LastIndex(v, ":")
	if lastColon > lastSlash {
		v = v[:lastColon]
	}
	for _, prefix := range []string{"registry-1.docker.io/", "index.docker.io/", "docker.io/"} {
		v = strings.TrimPrefix(v, prefix)
	}
	if !strings.Contains(v, "/") && v != "" {
		v = "library/" + v
	}
	return v
}

// ImageRepoDigest returns the repository digest associated with the locally
// installed image without pulling anything. image is the inspect reference
// (normally the image ID), repositoryRef is the container's configured image
// name and is used to select the matching RepoDigest when several exist.
func (c *Client) ImageRepoDigest(ctx context.Context, endpoint, image, repositoryRef string) (string, error) {
	out, err := c.run(ctx, endpoint, "image", "inspect", image, "--format", "{{json .RepoDigests}}")
	if err != nil {
		return "", err
	}
	var refs []string
	if strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "null" {
		if err := json.Unmarshal([]byte(out), &refs); err != nil {
			return "", err
		}
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("local image has no repository digest")
	}
	want := normalizeRepoName(repositoryRef)
	fallback := ""
	for _, ref := range refs {
		parts := strings.SplitN(strings.TrimSpace(ref), "@", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			continue
		}
		if fallback == "" {
			fallback = strings.TrimSpace(parts[1])
		}
		if want != "" && normalizeRepoName(parts[0]) == want {
			return strings.TrimSpace(parts[1]), nil
		}
	}
	if fallback != "" && len(refs) == 1 {
		return fallback, nil
	}
	if fallback != "" && want == "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no local repository digest matches %s", repositoryRef)
}

func (c *Client) ImageLabels(ctx context.Context, endpoint, image string) (map[string]string, error) {
	key := endpoint + "\x00" + image
	c.labelMu.Lock()
	if hit, ok := c.labelCache[key]; ok && time.Since(hit.At) < 5*time.Minute {
		copy := make(map[string]string, len(hit.Labels))
		for k, v := range hit.Labels {
			copy[k] = v
		}
		c.labelMu.Unlock()
		return copy, nil
	}
	c.labelMu.Unlock()
	out, err := c.run(ctx, endpoint, "image", "inspect", image, "--format", "{{json .Config.Labels}}")
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	if out != "null" && out != "" {
		if err := json.Unmarshal([]byte(out), &labels); err != nil {
			return nil, err
		}
	}
	c.labelMu.Lock()
	c.labelCache[key] = cachedLabels{Labels: labels, At: time.Now()}
	c.labelMu.Unlock()
	copy := make(map[string]string, len(labels))
	for k, v := range labels {
		copy[k] = v
	}
	return copy, nil
}

func InstalledVersion(image string, labels map[string]string) (string, string) {
	for _, key := range []string{"org.opencontainers.image.version", "org.label-schema.version", "version", "VERSION"} {
		if v := strings.TrimSpace(labels[key]); v != "" {
			return strings.TrimPrefix(v, "v"), key
		}
	}
	base := strings.Split(image, "@")[0]
	lastSlash := strings.LastIndex(base, "/")
	colon := strings.LastIndex(base, ":")
	if colon > lastSlash {
		tag := strings.TrimSpace(base[colon+1:])
		low := strings.ToLower(tag)
		if tag != "" && low != "latest" && low != "stable" && low != "release" && low != "main" && low != "master" && low != "edge" && low != "nightly" {
			return strings.TrimPrefix(tag, "v"), "image-tag"
		}
	}
	return "", ""
}

func workerName(id int64) string       { return fmt.Sprintf("vibewatch-worker-%d", id) }
func legacyWorkerName(id int64) string { return fmt.Sprintf("watchtower-ui-worker-%d", id) }

func (c *Client) ensureNetwork(ctx context.Context) error {
	if _, err := c.run(ctx, "", "network", "inspect", c.WorkerNetwork); err == nil {
		return nil
	}
	_, err := c.run(ctx, "", "network", "create", c.WorkerNetwork)
	return err
}

func (c *Client) EnsureWorker(ctx context.Context, host db.Host) (string, error) {
	if strings.TrimSpace(host.WorkerToken) == "" {
		return "", fmt.Errorf("worker API token is empty for host %d", host.ID)
	}
	if err := c.ensureNetwork(ctx); err != nil {
		return "", err
	}
	name := workerName(host.ID)
	// V0.3.9 runtime-name migration: remove the legacy worker for this host
	// before ensuring the Vibewatch-named worker. Host endpoint/token are read
	// from persistent state, so the recreated worker stays bound to the same host.
	_, _ = c.run(ctx, "", "rm", "-f", legacyWorkerName(host.ID))
	inspectFmt := `{{.State.Running}} {{index .Config.Labels "io.vibewatch.worker-version"}}`
	if out, err := c.run(ctx, "", "inspect", "-f", inspectFmt, name); err == nil {
		fields := strings.Fields(strings.TrimSpace(out))
		if len(fields) >= 2 && fields[0] == "true" && fields[1] == c.WorkerVersion {
			return "http://" + name + ":" + c.WorkerPort, nil
		}
	}
	_, _ = c.run(ctx, "", "rm", "-f", name)
	args := []string{"run", "-d", "--name", name, "--network", c.WorkerNetwork, "--restart", "unless-stopped",
		"--label", "com.centurylinklabs.watchtower.enable=false",
		"--label", "io.vibewatch.worker-version=" + c.WorkerVersion,
	}
	// Remote workers live on the controller Docker daemon but operate against a
	// different DOCKER_HOST. Give those sibling worker containers a Watchtower
	// scope label without enabling a scope filter inside the remote worker. The
	// local-host (unscoped) Watchtower instance therefore will not treat scoped
	// remote workers as competing unscoped instances, while remote workers still
	// scan the complete target host normally.
	if !strings.HasPrefix(host.Endpoint, "unix://") {
		args = append(args, "--label", fmt.Sprintf("com.centurylinklabs.watchtower.scope=vibewatch-worker-%d", host.ID))
	}
	args = append(args,
		"-e", "WATCHTOWER_HTTP_API_TOKEN="+host.WorkerToken,
		"-e", "WATCHTOWER_HTTP_API_PORT="+c.WorkerPort,
		"-e", "WATCHTOWER_HTTP_API_ENDPOINTS=health,check,update,history,containers,config,images,metrics",
		"-e", "WATCHTOWER_HTTP_API_CHECK_TIMEOUT=10m",
		"-e", "WATCHTOWER_HTTP_API_UPDATE_TIMEOUT=30m",
		"-e", "WATCHTOWER_HTTP_API_PERIODIC_POLLS=false",
		"-e", "WATCHTOWER_UPDATE_ON_START=false",
		"-e", "WATCHTOWER_CLEANUP=false",
	)
	if strings.HasPrefix(host.Endpoint, "unix://") {
		args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	} else {
		args = append(args, "-e", "DOCKER_HOST="+host.Endpoint)
	}
	args = append(args, c.WorkerImage)
	if _, err := c.run(ctx, "", args...); err != nil {
		return "", err
	}
	return "http://" + name + ":" + c.WorkerPort, nil
}

type WorkerState struct {
	Name       string `json:"name"`
	Exists     bool   `json:"exists"`
	Running    bool   `json:"running"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Restarting bool   `json:"restarting"`
	Error      string `json:"error,omitempty"`
}

func (c *Client) WorkerState(ctx context.Context, hostID int64) WorkerState {
	name := workerName(hostID)
	state := WorkerState{Name: name}
	out, err := c.run(ctx, "", "inspect", "-f", "{{json .State}}", name)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	state.Exists = true
	var raw struct {
		Running    bool   `json:"Running"`
		Status     string `json:"Status"`
		ExitCode   int    `json:"ExitCode"`
		Restarting bool   `json:"Restarting"`
		Error      string `json:"Error"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		state.Error = err.Error()
		return state
	}
	state.Running = raw.Running
	state.Status = raw.Status
	state.ExitCode = raw.ExitCode
	state.Restarting = raw.Restarting
	if raw.Error != "" {
		state.Error = raw.Error
	}
	return state
}

func (c *Client) WorkerLogsRecent(ctx context.Context, hostID int64, tail int) (string, error) {
	if tail < 1 {
		tail = 100
	}
	cmd := c.cmd(ctx, "", "logs", "--tail", fmt.Sprintf("%d", tail), workerName(hostID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) RemoveWorker(ctx context.Context, hostID int64) error {
	_, err := c.run(ctx, "", "rm", "-f", workerName(hostID))
	_, _ = c.run(ctx, "", "rm", "-f", legacyWorkerName(hostID))
	return err
}

// RemoveManagedWorkers removes every dynamically created Vibewatch/legacy
// worker. Dynamic workers are intentionally not Compose services, so the
// controller performs this cleanup during graceful shutdown.
func (c *Client) RemoveManagedWorkers(ctx context.Context) (int, error) {
	out, err := c.run(ctx, "", "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return 0, err
	}
	names := make([]string, 0)
	for _, name := range strings.Fields(out) {
		if strings.HasPrefix(name, "vibewatch-worker-") || strings.HasPrefix(name, "watchtower-ui-worker-") {
			names = append(names, name)
		}
	}
	removed := 0
	for _, name := range names {
		if _, e := c.run(ctx, "", "rm", "-f", name); e != nil {
			return removed, e
		}
		removed++
	}
	return removed, nil
}

// CleanupMigrationContainer removes the one-shot Compose migration helper
// after the controller has successfully started. Compose will recreate it only
// when a future `up` needs to evaluate the migration dependency again.
func (c *Client) CleanupMigrationContainer(ctx context.Context) {
	_, _ = c.run(ctx, "", "rm", "-f", "vibewatch-runtime-migrate")
}
func (c *Client) WorkerLogs(ctx context.Context, hostID int64, since time.Time) (string, error) {
	cmd := c.cmd(ctx, "", "logs", "--since", since.UTC().Format(time.RFC3339), workerName(hostID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func NewEventWatcher() *EventWatcher { return &EventWatcher{cancel: map[int64]context.CancelFunc{}} }
func (w *EventWatcher) Start(parent context.Context, c *Client, store *db.Store, host db.Host) {
	w.mu.Lock()
	if _, ok := w.cancel[host.ID]; ok {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel[host.ID] = cancel
	w.mu.Unlock()
	go func() {
		defer func() { w.mu.Lock(); delete(w.cancel, host.ID); w.mu.Unlock() }()
		for ctx.Err() == nil {
			cmd := c.cmd(ctx, host.Endpoint, "events", "--format", "{{json .}}")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				c.Logger.Warn("docker event stream start failed", "host", host.Name, "error", err)
				time.Sleep(10 * time.Second)
				continue
			}
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				_ = store.AddDockerEvent(context.Background(), host.ID, scanner.Text())
			}
			_ = cmd.Wait()
			if ctx.Err() == nil {
				c.Logger.Warn("docker event stream disconnected", "host", host.Name, "stderr", strings.TrimSpace(stderr.String()))
				time.Sleep(10 * time.Second)
			}
		}
	}()
}
func (w *EventWatcher) Stop(hostID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.cancel[hostID]; ok {
		c()
		delete(w.cancel, hostID)
	}
}

// PullImage refreshes an image on the controller Docker host and reports whether
// its image ID changed. Worker containers themselves always live on this host.
func (c *Client) PullImage(ctx context.Context, image string) (bool, string, string, error) {
	before, _ := c.run(ctx, "", "image", "inspect", image, "--format", "{{.Id}}")
	if _, err := c.run(ctx, "", "pull", image); err != nil {
		return false, before, "", err
	}
	after, err := c.run(ctx, "", "image", "inspect", image, "--format", "{{.Id}}")
	if err != nil {
		return false, before, "", err
	}
	return strings.TrimSpace(before) != strings.TrimSpace(after), strings.TrimSpace(before), strings.TrimSpace(after), nil
}

// LaunchSelfUpdate starts a short-lived Watchtower helper on the controller's
// local Docker socket. The helper targets only the controller container. This
// lets Watchtower preserve the existing container configuration when recreating
// it from a newer registry image.
func (c *Client) LaunchSelfUpdate(ctx context.Context, controllerName string) error {
	controllerName = strings.TrimSpace(controllerName)
	if controllerName == "" {
		return fmt.Errorf("controller container name is empty")
	}
	name := "vibewatch-self-updater"
	_, _ = c.run(ctx, "", "rm", "-f", name)
	args := []string{"run", "-d", "--name", name, "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock", c.WorkerImage, "--run-once", "--cleanup", controllerName}
	_, err := c.run(ctx, "", args...)
	return err
}

// CleanupLegacyRuntime removes Docker artifacts whose names predate the
// Vibewatch runtime rebrand. It is best-effort and never touches persistent data.
func (c *Client) CleanupLegacyRuntime(ctx context.Context) {
	_, _ = c.run(ctx, "", "rm", "-f", "vibewatch-runtime-migrate")
	_, _ = c.run(ctx, "", "rm", "-f", "watchtower-ui-self-updater")
	// The old network can only be removed after every legacy worker/controller
	// detached from it. Failure is harmless and will be retried on next start.
	_, _ = c.run(ctx, "", "network", "rm", "watchtower-ui-internal")
}

// ContainerMount returns the Docker mount backing a destination inside a
// controller container. It is used to verify that mutable Vibewatch state
// is not stored only in the writable container layer.
type MountInfo struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

func (c *Client) ContainerMount(ctx context.Context, containerName, destination string) (MountInfo, bool, error) {
	out, err := c.run(ctx, "", "inspect", containerName, "--format", "{{json .Mounts}}")
	if err != nil {
		return MountInfo{}, false, err
	}
	var mounts []MountInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
		return MountInfo{}, false, fmt.Errorf("decode container mounts: %w", err)
	}
	for _, m := range mounts {
		if filepath.Clean(m.Destination) == filepath.Clean(destination) {
			return m, true, nil
		}
	}
	return MountInfo{}, false, nil
}
