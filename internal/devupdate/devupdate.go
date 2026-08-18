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
	"time"
)

const (
	MaxUploadBytes       int64 = 64 << 20
	MaxUncompressedBytes int64 = 256 << 20
)

type Status struct {
	ID         string   `json:"id"`
	Filename   string   `json:"filename"`
	Version    string   `json:"version"`
	SHA256     string   `json:"sha256"`
	SizeBytes  int64    `json:"size_bytes"`
	State      string   `json:"state"`
	Stage      string   `json:"stage"`
	Percent    int      `json:"percent"`
	Message    string   `json:"message"`
	Error      string   `json:"error,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
	FinishedAt string   `json:"finished_at,omitempty"`
	LogTail    []string `json:"log_tail,omitempty"`
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
	tmp := filepath.Join(p.States, st.ID+".json.tmp")
	dst := filepath.Join(p.States, st.ID+".json")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
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
			if t, err := time.Parse(time.RFC3339Nano, st.UpdatedAt); err == nil && time.Since(t) > 4*time.Hour {
				continue
			}
			return st, true
		}
	}
	return Status{}, false
}

func IsActiveState(state string) bool {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "queued", "preparing", "backing_up", "applying", "building", "switching", "verifying", "rolling_back":
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
	required := []string{"go.mod", "Dockerfile", "docker-compose.yml", "docker-compose.build.yml", "web/package.json", "cmd/devupdater/main.go", "web/public/developer-update.html", "VERSION"}
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

func SnapshotSource(workspace, backup string) error {
	if err := validateWorkspace(workspace); err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if preserveRoot[e.Name()] || e.Name() == "node_modules" {
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
	return nil
}

func ApplySource(source, workspace string) error {
	if err := validateWorkspace(workspace); err != nil {
		return err
	}
	if st, err := os.Stat(source); err != nil || !st.IsDir() {
		return errors.New("staged source is missing")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if preserveRoot[e.Name()] {
			continue
		}
		if e.Name() == "scripts" && e.IsDir() {
			// scripts/.env is runtime configuration. Never delete it as part of
			// source replacement, even transiently: a failed copy must not turn
			// the staged package's .env into the surviving environment.
			if err := clearDirExcept(filepath.Join(workspace, "scripts"), map[string]bool{".env": true}); err != nil {
				return err
			}
			continue
		}
		if err := os.RemoveAll(filepath.Join(workspace, e.Name())); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, e := range entries {
		top := e.Name()
		if preserveRoot[top] {
			continue
		}
		if err := copyPath(filepath.Join(source, top), filepath.Join(workspace, top), func(rel string) bool {
			rel = filepath.ToSlash(rel)
			if top == "scripts" && rel == ".env" {
				return false
			}
			if top == "web" {
				return rel != "node_modules" && !strings.HasPrefix(rel, "node_modules/") && rel != "dist" && !strings.HasPrefix(rel, "dist/")
			}
			return true
		}); err != nil {
			return err
		}
	}
	return nil
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

func validateWorkspace(workspace string) error {
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
	if _, err := os.Stat(filepath.Join(workspace, "docker-compose.yml")); err != nil {
		return errors.New("project workspace does not contain docker-compose.yml")
	}
	return nil
}

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
		_ = os.Remove(filepath.Join(p.States, it.id+".json"))
		_ = os.Remove(filepath.Join(p.Uploads, it.id+".zip"))
		_ = os.RemoveAll(filepath.Join(p.Staged, it.id))
		_ = os.RemoveAll(filepath.Join(p.Backups, it.id))
		_ = os.Remove(filepath.Join(p.Logs, it.id+".log"))
	}
}
