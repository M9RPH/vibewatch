package devupdate

import (
	"archive/zip"
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	MaxUploadBytes       int64 = 64 << 20
	MaxUncompressedBytes int64 = 256 << 20
	// Development builds can temporarily consume several GiB in BuildKit even
	// though the final controller image is small. Keep a conservative floor so
	// source recovery and status writes still have headroom.
	MinDockerBuildFreeBytes int64  = 4 << 30
	MinWorkspaceFreeBytes   int64  = 512 << 20
	MinDataFreeBytes        int64  = 512 << 20
	BuildAbortFreeBytes     int64  = 768 << 20
	MinFreeInodes           uint64 = 2048
	statusReserveBytes             = 256 << 10
)

var RequiredSourceFiles = []string{
	"go.mod", "Dockerfile", "docker-compose.yml", "docker-compose.build.yml",
	"web/package.json", "cmd/devupdater/main.go", "web/public/developer-update.html", "VERSION",
}

type DiskSpace struct {
	Path       string `json:"path"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	FreeInodes uint64 `json:"free_inodes"`
}

type Status struct {
	ID                  string   `json:"id"`
	Filename            string   `json:"filename"`
	Version             string   `json:"version"`
	SHA256              string   `json:"sha256"`
	SizeBytes           int64    `json:"size_bytes"`
	State               string   `json:"state"`
	Stage               string   `json:"stage"`
	Percent             int      `json:"percent"`
	Message             string   `json:"message"`
	Error               string   `json:"error,omitempty"`
	StartedAt           string   `json:"started_at,omitempty"`
	UpdatedAt           string   `json:"updated_at"`
	FinishedAt          string   `json:"finished_at,omitempty"`
	LogTail             []string `json:"log_tail,omitempty"`
	CancelRequested     bool     `json:"cancel_requested,omitempty"`
	SwitchAttempted     bool     `json:"switch_attempted,omitempty"`
	DatabaseBackup      string   `json:"database_backup,omitempty"`
	SourceBackupVersion string   `json:"source_backup_version,omitempty"`
	PreviousImageID     string   `json:"previous_image_id,omitempty"`
	PreviousImageRef    string   `json:"previous_image_ref,omitempty"`
}

type Paths struct {
	Root    string
	Uploads string
	Staged  string
	Backups string
	States  string
	Logs    string
}

func PathsFor(dataDir string) Paths {
	root := filepath.Join(dataDir, "dev-updates")
	return Paths{
		Root:    root,
		Uploads: filepath.Join(root, "uploads"),
		Staged:  filepath.Join(root, "staged"),
		Backups: filepath.Join(root, "backups"),
		States:  filepath.Join(root, "states"),
		Logs:    filepath.Join(root, "logs"),
	}
}

func ensureDirs(p Paths) error {
	for _, dir := range []string{p.Root, p.Uploads, p.Staged, p.Backups, p.States, p.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func NewID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b), nil
}

func WriteStatus(dataDir string, st Status) error {
	p := PathsFor(dataDir)
	if err := ensureDirs(p); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if st.Percent < 0 {
		st.Percent = 0
	}
	if st.Percent > 100 {
		st.Percent = 100
	}
	st.LogTail = nil
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	write := func() error {
		tmp := filepath.Join(p.States, st.ID+".json.tmp")
		dst := filepath.Join(p.States, st.ID+".json")
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(b)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(tmp)
			return writeErr
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return closeErr
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if dir, err := os.Open(p.States); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
		return nil
	}
	if err := write(); err != nil {
		// Keep a small, preallocated emergency reserve for exactly the ENOSPC
		// failure mode that previously left rolling_back permanently active.
		if errors.Is(err, syscall.ENOSPC) {
			_ = os.Remove(filepath.Join(p.Root, ".status-reserve"))
			if retryErr := write(); retryErr == nil {
				return nil
			} else {
				return retryErr
			}
		}
		return err
	}
	ensureStatusReserve(p)
	return nil
}

func ensureStatusReserve(p Paths) {
	path := filepath.Join(p.Root, ".status-reserve")
	if st, err := os.Stat(path); err == nil && st.Size() >= statusReserveBytes {
		return
	}
	buf := make([]byte, statusReserveBytes)
	_ = os.WriteFile(path, buf, 0o600)
}

func ReadStatus(dataDir, id string) (Status, error) {
	if !validID(id) {
		return Status{}, errors.New("invalid development update id")
	}
	p := PathsFor(dataDir)
	b, err := os.ReadFile(filepath.Join(p.States, id+".json"))
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return Status{}, err
	}
	st.LogTail, _ = ReadLogTail(dataDir, id, 120)
	return st, nil
}

func LatestStatus(dataDir string) (Status, bool) {
	p := PathsFor(dataDir)
	entries, err := os.ReadDir(p.States)
	if err != nil {
		return Status{}, false
	}
	sort.Slice(entries, func(i, j int) bool {
		ai, _ := entries[i].Info()
		aj, _ := entries[j].Info()
		if ai == nil || aj == nil {
			return entries[i].Name() > entries[j].Name()
		}
		return ai.ModTime().After(aj.ModTime())
	})
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		st, err := ReadStatus(dataDir, id)
		if err == nil {
			return st, true
		}
	}
	return Status{}, false
}

func ActiveStatus(dataDir string) (Status, bool) {
	p := PathsFor(dataDir)
	entries, err := os.ReadDir(p.States)
	if err != nil {
		return Status{}, false
	}
	sort.Slice(entries, func(i, j int) bool {
		ai, _ := entries[i].Info()
		aj, _ := entries[j].Info()
		if ai == nil || aj == nil {
			return entries[i].Name() > entries[j].Name()
		}
		return ai.ModTime().After(aj.ModTime())
	})
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		st, err := ReadStatus(dataDir, id)
		if err != nil {
			continue
		}
		if IsActiveState(st.State) {
			// Active state is durable until the controller/helper explicitly
			// reconciles it. Time alone must never unlock a source update.
			return st, true
		}
	}
	return Status{}, false
}

func IsActiveState(state string) bool {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "queued", "preparing", "backing_up", "applying", "building", "switching", "verifying", "rolling_back", "cancel_requested":
		return true
	default:
		return false
	}
}

func validID(id string) bool {
	if len(id) < 8 || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func StageArchive(dataDir, filename string, r io.Reader) (Status, error) {
	p := PathsFor(dataDir)
	if err := ensureDirs(p); err != nil {
		return Status{}, err
	}
	id, err := NewID()
	if err != nil {
		return Status{}, err
	}
	cleanName := filepath.Base(strings.TrimSpace(filename))
	if cleanName == "." || cleanName == "" || !strings.EqualFold(filepath.Ext(cleanName), ".zip") {
		return Status{}, errors.New("development update must be a .zip file")
	}
	zipPath := filepath.Join(p.Uploads, id+".zip")
	f, err := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Status{}, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, MaxUploadBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(zipPath)
		return Status{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(zipPath)
		return Status{}, closeErr
	}
	if n > MaxUploadBytes {
		_ = os.Remove(zipPath)
		return Status{}, fmt.Errorf("ZIP exceeds %d MiB upload limit", MaxUploadBytes>>20)
	}
	if n == 0 {
		_ = os.Remove(zipPath)
		return Status{}, errors.New("ZIP is empty")
	}

	stageRoot := filepath.Join(p.Staged, id, "source")
	version, err := extractProjectZIP(zipPath, stageRoot)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(p.Staged, id))
		_ = os.Remove(zipPath)
		return Status{}, err
	}
	st := Status{ID: id, Filename: cleanName, Version: version, SHA256: hex.EncodeToString(h.Sum(nil)), SizeBytes: n, State: "uploaded", Stage: "Package ready", Percent: 0, Message: "ZIP validated and staged. Runtime data and environment files will be preserved."}
	if err := WriteStatus(dataDir, st); err != nil {
		return Status{}, err
	}
	return ReadStatus(dataDir, id)
}

func extractProjectZIP(zipPath, dst string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open ZIP: %w", err)
	}
	defer zr.Close()
	required := RequiredSourceFiles
	roots := map[string]bool{}
	total := int64(0)
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../") {
			return "", fmt.Errorf("unsafe ZIP path %q", f.Name)
		}
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return "", fmt.Errorf("unsupported ZIP entry %q", f.Name)
		}
		if f.UncompressedSize64 > uint64(MaxUncompressedBytes) {
			return "", fmt.Errorf("ZIP entry %q exceeds the uncompressed safety limit", f.Name)
		}
		total += int64(f.UncompressedSize64)
		if total > MaxUncompressedBytes {
			return "", fmt.Errorf("ZIP expands beyond %d MiB safety limit", MaxUncompressedBytes>>20)
		}
		if strings.HasSuffix(name, "/go.mod") {
			roots[strings.TrimSuffix(name, "go.mod")] = true
		}
		if name == "go.mod" {
			roots[""] = true
		}
	}
	var root string
	candidates := make([]string, 0, len(roots))
	for r := range roots {
		candidates = append(candidates, r)
	}
	sort.Strings(candidates)
	for _, candidate := range candidates {
		present := map[string]bool{}
		for _, f := range zr.File {
			name := strings.ReplaceAll(f.Name, "\\", "/")
			if strings.HasPrefix(name, candidate) {
				present[strings.TrimPrefix(name, candidate)] = true
			}
		}
		ok := true
		for _, req := range required {
			if !present[req] {
				ok = false
				break
			}
		}
		if ok {
			if root != "" || (root == "" && candidate == "" && len(candidates) > 1) { /* continue deterministic first */
			}
			root = candidate
			break
		}
	}
	if len(candidates) == 0 || (root == "" && !roots[""]) {
		return "", errors.New("ZIP does not contain a Vibewatch project root")
	}
	// Verify all required paths for the selected root.
	present := map[string]bool{}
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, root) {
			present[strings.TrimPrefix(name, root)] = true
		}
	}
	for _, req := range required {
		if !present[req] {
			return "", fmt.Errorf("ZIP is missing required development updater file %s", req)
		}
	}

	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, root) {
			continue
		}
		rel := strings.TrimPrefix(name, root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		cleanDst := filepath.Clean(dst) + string(os.PathSeparator)
		cleanTarget := filepath.Clean(target)
		if cleanTarget != filepath.Clean(dst) && !strings.HasPrefix(cleanTarget+string(os.PathSeparator), cleanDst) {
			return "", fmt.Errorf("unsafe extracted path %q", rel)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		mode &= 0o755
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, cpErr := io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		close1 := out.Close()
		close2 := rc.Close()
		if cpErr != nil {
			return "", cpErr
		}
		if close1 != nil {
			return "", close1
		}
		if close2 != nil {
			return "", close2
		}
	}
	vb, err := os.ReadFile(filepath.Join(dst, "VERSION"))
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(vb))
	if version == "" {
		return "", errors.New("VERSION is empty")
	}
	return version, nil
}

var preserveRoot = map[string]bool{".git": true, ".env": true, "data": true, "backups": true, "logs": true}

func SnapshotSource(workspace, backup string) (retErr error) {
	if err := ValidateSourceTree(workspace); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(backup)
		}
	}()
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if preserveRoot[e.Name()] || e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".vibewatch-source-write-") {
			continue
		}
		top := e.Name()
		if err := copyPath(filepath.Join(workspace, top), filepath.Join(backup, top), func(rel string) bool {
			rel = filepath.ToSlash(rel)
			if top == "web" {
				return rel != "node_modules" && !strings.HasPrefix(rel, "node_modules/") && rel != "dist" && !strings.HasPrefix(rel, "dist/")
			}
			if top == "scripts" && rel == ".env" {
				return false
			}
			return true
		}); err != nil {
			return err
		}
	}
	if err := ValidateSourceTree(backup); err != nil {
		return fmt.Errorf("source backup validation failed: %w", err)
	}
	return nil
}

// ApplySource performs a failure-safe source overlay. Existing files are never
// truncated in place: every replacement is written to a sibling temporary file,
// fsynced, and atomically renamed. Stale files are removed only after every
// staged source file is present and verified. Therefore ENOSPC during an apply
// leaves a retryable complete/mixed tree instead of deleting required files.
func ApplySource(source, workspace string) error {
	if err := validateWorkspaceRoot(workspace); err != nil {
		return err
	}
	if err := ValidateSourceTree(source); err != nil {
		return fmt.Errorf("staged source is invalid: %w", err)
	}
	desiredFiles := map[string]bool{}
	desiredDirs := map[string]bool{}
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		top := strings.Split(rel, "/")[0]
		if preserveRoot[top] || top == "node_modules" || (top == "web" && (rel == "web/node_modules" || strings.HasPrefix(rel, "web/node_modules/") || rel == "web/dist" || strings.HasPrefix(rel, "web/dist/"))) || (top == "scripts" && rel == "scripts/.env") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing source symlink %s", path)
		}
		dst := filepath.Join(workspace, filepath.FromSlash(rel))
		if info.IsDir() {
			desiredDirs[rel] = true
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		desiredFiles[rel] = true
		return copyFileAtomic(path, dst, info.Mode().Perm())
	}); err != nil {
		return err
	}
	if err := verifySourceFiles(source, workspace); err != nil {
		return fmt.Errorf("source apply verification failed before cleanup: %w", err)
	}
	// Only after all desired files are durable do we remove stale source. Root
	// runtime state and scripts/.env are explicitly preserved.
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, e := range entries {
		top := e.Name()
		if preserveRoot[top] || strings.HasPrefix(top, ".vibewatch-source-write-") {
			continue
		}
		if top == "scripts" && e.IsDir() {
			if err := removeStaleTree(filepath.Join(workspace, top), "scripts", desiredFiles, desiredDirs, map[string]bool{"scripts/.env": true}); err != nil {
				return err
			}
			continue
		}
		if !desiredFiles[top] && !desiredDirs[top] {
			if err := os.RemoveAll(filepath.Join(workspace, top)); err != nil {
				return err
			}
			continue
		}
		if e.IsDir() {
			if err := removeStaleTree(filepath.Join(workspace, top), top, desiredFiles, desiredDirs, nil); err != nil {
				return err
			}
		}
	}
	if err := ValidateSourceTree(workspace); err != nil {
		return fmt.Errorf("source tree invalid after apply: %w", err)
	}
	return verifySourceFiles(source, workspace)
}

func copyFileAtomic(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if mode == 0 {
		mode = 0o644
	}
	tmp := dst + fmt.Sprintf(".vibewatch-source-write-%d", os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	if cpErr == nil {
		cpErr = out.Sync()
	}
	closeErr := out.Close()
	if cpErr != nil {
		_ = os.Remove(tmp)
		return cpErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func verifySourceFiles(source, workspace string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		top := strings.Split(rel, "/")[0]
		if preserveRoot[top] || top == "node_modules" || (top == "web" && (rel == "web/node_modules" || strings.HasPrefix(rel, "web/node_modules/") || rel == "web/dist" || strings.HasPrefix(rel, "web/dist/"))) || (top == "scripts" && rel == "scripts/.env") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		srcHash, err := fileSHA256(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(workspace, filepath.FromSlash(rel))
		dstHash, err := fileSHA256(dst)
		if err != nil {
			return fmt.Errorf("%s missing after source apply: %w", rel, err)
		}
		if srcHash != dstHash {
			return fmt.Errorf("%s differs after source apply", rel)
		}
		return nil
	})
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func removeStaleTree(dir, prefix string, files, dirs map[string]bool, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		rel := filepath.ToSlash(filepath.Join(prefix, e.Name()))
		if keep != nil && keep[rel] {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if !dirs[rel] {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				continue
			}
			if err := removeStaleTree(path, rel, files, dirs, keep); err != nil {
				return err
			}
		} else if !files[rel] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func validateWorkspaceRoot(workspace string) error {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" || workspace == "." || workspace == string(os.PathSeparator) {
		return errors.New("unsafe project workspace")
	}
	st, err := os.Stat(workspace)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("project workspace is not a directory")
	}
	return nil
}

func ValidateSourceTree(root string) error {
	if err := validateWorkspaceRoot(root); err != nil {
		return err
	}
	for _, rel := range RequiredSourceFiles {
		st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || st.IsDir() || st.Size() == 0 {
			return fmt.Errorf("required source file %s is missing or empty", rel)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return errors.New("VERSION is missing or empty")
	}
	return nil
}

func SourceTreeVersion(root string) (string, error) {
	if err := ValidateSourceTree(root); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func DiskUsage(path string) (DiskSpace, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return DiskSpace{}, errors.New("disk usage path is empty")
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskSpace{}, err
	}
	block := int64(st.Bsize)
	return DiskSpace{Path: path, TotalBytes: int64(st.Blocks) * block, FreeBytes: int64(st.Bavail) * block, FreeInodes: st.Ffree}, nil
}

func SourceSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func clearDirExcept(dir string, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkspace(workspace string) error { return ValidateSourceTree(workspace) }

func copyPath(src, dst string, include func(rel string) bool) error {
	root := src
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if include != nil && !include(filepath.ToSlash(rel)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing source symlink %s", path)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}

func RestoreDatabaseBackup(backupPath, dataDir string) error {
	backupPath = filepath.Clean(strings.TrimSpace(backupPath))
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if backupPath == "" || dataDir == "" || dataDir == "." || dataDir == string(os.PathSeparator) {
		return errors.New("unsafe database rollback path")
	}
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("database backup unavailable: %w", err)
	}
	dst := filepath.Join(dataDir, "vibewatch.db")
	tmp := dst + ".dev-update-restore.tmp"
	if err := copyFile(backupPath, tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst + "-wal")
	_ = os.Remove(dst + "-shm")
	if key, err := os.ReadFile(backupPath + ".registry-key"); err == nil && len(key) == 32 {
		if err := os.WriteFile(filepath.Join(dataDir, "registry-credentials.key"), key, 0o600); err != nil {
			return fmt.Errorf("restore registry credential key: %w", err)
		}
	}
	return nil
}

func LogPath(dataDir, id string) string { return filepath.Join(PathsFor(dataDir).Logs, id+".log") }
func AppendLog(dataDir, id, line string) error {
	p := PathsFor(dataDir)
	if err := ensureDirs(p); err != nil {
		return err
	}
	f, err := os.OpenFile(LogPath(dataDir, id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), strings.TrimRight(line, "\r\n"))
	return err
}
func ReadLogTail(dataDir, id string, max int) ([]string, error) {
	if max <= 0 {
		return nil, nil
	}
	f, err := os.Open(LogPath(dataDir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	lines := make([]string, 0, max)
	s := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 1024*1024)
	for s.Scan() {
		lines = append(lines, s.Text())
		if len(lines) > max {
			copy(lines, lines[len(lines)-max:])
			lines = lines[:max]
		}
	}
	return lines, s.Err()
}

func CleanupOld(dataDir string, keep int) {
	if keep < 1 {
		keep = 1
	}
	p := PathsFor(dataDir)
	entries, err := os.ReadDir(p.States)
	if err != nil {
		return
	}
	type item struct {
		id  string
		mod time.Time
	}
	items := []item{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{strings.TrimSuffix(e.Name(), ".json"), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	if len(items) <= keep {
		return
	}
	for _, it := range items[keep:] {
		if st, err := ReadStatus(dataDir, it.id); err == nil && (IsActiveState(st.State) || strings.EqualFold(strings.TrimSpace(st.State), "recovery_required")) {
			continue
		}
		_ = os.Remove(filepath.Join(p.States, it.id+".json"))
		_ = os.Remove(filepath.Join(p.States, it.id+".cancel"))
		_ = os.Remove(filepath.Join(p.Uploads, it.id+".zip"))
		_ = os.RemoveAll(filepath.Join(p.Staged, it.id))
		_ = os.RemoveAll(filepath.Join(p.Backups, it.id))
		_ = os.Remove(filepath.Join(p.Logs, it.id+".log"))
	}
}

func CancelPath(dataDir, id string) string {
	return filepath.Join(PathsFor(dataDir).States, id+".cancel")
}

func RequestCancel(dataDir, id string) error {
	if !validID(id) {
		return errors.New("invalid development update id")
	}
	p := PathsFor(dataDir)
	if err := ensureDirs(p); err != nil {
		return err
	}
	return os.WriteFile(CancelPath(dataDir, id), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}

func CancelRequested(dataDir, id string) bool {
	if !validID(id) {
		return false
	}
	_, err := os.Stat(CancelPath(dataDir, id))
	return err == nil
}

func ClearCancel(dataDir, id string) { _ = os.Remove(CancelPath(dataDir, id)) }

func CleanupStateTemps(dataDir string) {
	p := PathsFor(dataDir)
	entries, err := os.ReadDir(p.States)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json.tmp") {
			continue
		}
		_ = os.Remove(filepath.Join(p.States, e.Name()))
	}
}
