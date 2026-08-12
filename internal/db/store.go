package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Store struct {
	Path        string
	EventPath   string
	mu          sync.Mutex
	eventMu     sync.Mutex
	eventWrites int
}

type Bool bool

func (v *Bool) UnmarshalJSON(data []byte) error {
	t := strings.TrimSpace(string(data))
	*v = Bool(t == "true" || t == "1" || t == "\"1\"")
	return nil
}
func (v Bool) MarshalJSON() ([]byte, error) {
	if v {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

type Host struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Enabled     Bool   `json:"enabled"`
	WorkerToken string `json:"-"`
	CreatedAt   string `json:"created_at"`
}

// UnmarshalJSON keeps worker_token available to the controller while the
// struct tag above deliberately prevents it from ever being serialized back
// through the public HTTP API. sqlite3 -json returns snake_case column names.
func (h *Host) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Endpoint    string `json:"endpoint"`
		Enabled     Bool   `json:"enabled"`
		WorkerToken string `json:"worker_token"`
		CreatedAt   string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	h.ID = wire.ID
	h.Name = wire.Name
	h.Endpoint = wire.Endpoint
	h.Enabled = wire.Enabled
	h.WorkerToken = wire.WorkerToken
	h.CreatedAt = wire.CreatedAt
	return nil
}

type Policy struct {
	HostID               int64  `json:"host_id"`
	ContainerName        string `json:"container_name"`
	Mode                 string `json:"mode"`
	CheckIntervalMinutes int    `json:"check_interval_minutes"`
	ReleaseRepo          string `json:"release_repo"`
	LastCheckedAt        string `json:"last_checked_at,omitempty"`
}

type Cache struct {
	HostID          int64  `json:"host_id"`
	ContainerName   string `json:"container_name"`
	Image           string `json:"image"`
	ImageID         string `json:"image_id"`
	CurrentDigest   string `json:"current_digest"`
	LatestDigest    string `json:"latest_digest"`
	UpdateAvailable Bool   `json:"update_available"`
	FirstDetectedAt string `json:"first_detected_at"`
	SnoozedDigest   string `json:"snoozed_digest"`
	SnoozedAt       string `json:"snoozed_at"`
	LastCheckedAt   string `json:"last_checked_at"`
	LastError       string `json:"last_error"`
}

type Job struct {
	ID            int64  `json:"id"`
	Type          string `json:"type"`
	Trigger       string `json:"trigger"`
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	SummaryJSON   string `json:"summary_json"`
	Error         string `json:"error"`
}

type JobLog struct {
	ID      int64  `json:"id"`
	JobID   int64  `json:"job_id"`
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
}
type Schedule struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	Action     string `json:"action"`
	HostID     int64  `json:"host_id"`
	Containers string `json:"containers"`
	Enabled    Bool   `json:"enabled"`
	LastRunAt  string `json:"last_run_at"`
}
type Audit struct {
	ID            int64  `json:"id"`
	TS            string `json:"ts"`
	Actor         string `json:"actor"`
	Action        string `json:"action"`
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	Details       string `json:"details"`
}

type VersionInfo struct {
	HostID          int64  `json:"host_id"`
	ContainerName   string `json:"container_name"`
	Installed       string `json:"installed_version"`
	InstalledSource string `json:"installed_source"`
	Latest          string `json:"latest_version"`
	LatestSource    string `json:"latest_source"`
	ReleaseRepo     string `json:"release_repo"`
	PublishedAt     string `json:"published_at"`
	CheckedAt       string `json:"checked_at"`
	Error           string `json:"error"`
}

type Automation struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Enabled    Bool   `json:"enabled"`
	LastRunAt  string `json:"last_run_at"`
}

type HostGroup struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	HostIDs     []int64 `json:"host_ids"`
	CreatedAt   string  `json:"created_at"`
}

type User struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	PasswordHash string  `json:"-"`
	Role         string  `json:"role"`
	Enabled      Bool    `json:"enabled"`
	HostIDs      []int64 `json:"host_ids"`
	GroupIDs     []int64 `json:"group_ids"`
	CreatedAt    string  `json:"created_at"`
}

func (u *User) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID           int64  `json:"id"`
		Username     string `json:"username"`
		PasswordHash string `json:"password_hash"`
		Role         string `json:"role"`
		Enabled      Bool   `json:"enabled"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	u.ID = wire.ID
	u.Username = wire.Username
	u.PasswordHash = wire.PasswordHash
	u.Role = wire.Role
	u.Enabled = wire.Enabled
	u.CreatedAt = wire.CreatedAt
	return nil
}

type NotificationSettings struct {
	UserID                int64  `json:"user_id"`
	PushoverAppToken      string `json:"-"`
	PushoverUserKey       string `json:"pushover_user_key"`
	NotifyAutoUpdates     Bool   `json:"notify_auto_updates"`
	NotifyManualAvailable Bool   `json:"notify_manual_available"`
	NotifyManualUpdates   Bool   `json:"notify_manual_updates"`
	UpdatedAt             string `json:"updated_at"`
}

// UnmarshalJSON keeps the per-account Pushover App Token available to
// backend notification delivery while json:"-" above prevents the secret
// from ever being serialized through the public API/support bundle.
func (n *NotificationSettings) UnmarshalJSON(data []byte) error {
	var wire struct {
		UserID                int64  `json:"user_id"`
		PushoverAppToken      string `json:"pushover_app_token"`
		PushoverUserKey       string `json:"pushover_user_key"`
		NotifyAutoUpdates     Bool   `json:"notify_auto_updates"`
		NotifyManualAvailable Bool   `json:"notify_manual_available"`
		NotifyManualUpdates   Bool   `json:"notify_manual_updates"`
		UpdatedAt             string `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	n.UserID = wire.UserID
	n.PushoverAppToken = wire.PushoverAppToken
	n.PushoverUserKey = wire.PushoverUserKey
	n.NotifyAutoUpdates = wire.NotifyAutoUpdates
	n.NotifyManualAvailable = wire.NotifyManualAvailable
	n.NotifyManualUpdates = wire.NotifyManualUpdates
	n.UpdatedAt = wire.UpdatedAt
	return nil
}

type NotificationTarget struct {
	UserID           int64  `json:"user_id"`
	Username         string `json:"username"`
	Role             string `json:"role"`
	PushoverAppToken string `json:"-"`
	PushoverUserKey  string `json:"-"`
}

type NotificationDelivery struct {
	ID            int64  `json:"id"`
	TS            string `json:"ts"`
	UserID        int64  `json:"user_id"`
	Username      string `json:"username"`
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	Event         string `json:"event"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Error         string `json:"error"`
}

type UpdateHistory struct {
	ID                int64  `json:"id"`
	TS                string `json:"ts"`
	HostID            int64  `json:"host_id"`
	ContainerName     string `json:"container_name"`
	Action            string `json:"action"`
	Trigger           string `json:"trigger"`
	Actor             string `json:"actor"`
	Status            string `json:"status"`
	FromVersion       string `json:"from_version"`
	ToVersion         string `json:"to_version"`
	FromImageRef      string `json:"from_image_ref"`
	ToImageRef        string `json:"to_image_ref"`
	FromDigest        string `json:"from_digest"`
	ToDigest          string `json:"to_digest"`
	SnapshotID        string `json:"snapshot_id"`
	RestorePointID    int64  `json:"restore_point_id"`
	DurationMS        int64  `json:"duration_ms"`
	Error             string `json:"error"`
	DependencyCount   int    `json:"dependency_count"`
	DependencyStatus  string `json:"dependency_status"`
	DependencyDetails string `json:"dependency_details"`
}

type RestorePoint struct {
	ID                  int64  `json:"id"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
	HostID              int64  `json:"host_id"`
	ContainerName       string `json:"container_name"`
	SnapshotID          string `json:"snapshot_id"`
	Reason              string `json:"reason"`
	Trigger             string `json:"trigger"`
	Status              string `json:"status"`
	ImageRef            string `json:"image_ref"`
	ImageID             string `json:"image_id"`
	OriginalImageRef    string `json:"original_image_ref"`
	OriginalImageID     string `json:"original_image_id"`
	TargetDigest        string `json:"target_digest"`
	FromVersion         string `json:"from_version"`
	UnitKind            string `json:"unit_kind"`
	UnitKey             string `json:"unit_key"`
	StackType           string `json:"stack_type"`
	WritableLayer       Bool   `json:"writable_layer"`
	ConfigProtected     Bool   `json:"config_protected"`
	VolumeDataProtected Bool   `json:"volume_data_protected"`
	VolumeCount         int    `json:"volume_count"`
	BindCount           int    `json:"bind_count"`
	RestoreCount        int    `json:"restore_count"`
	LastRestoredAt      string `json:"last_restored_at"`
	LastError           string `json:"last_error"`
	DependencyCount     int    `json:"dependency_count"`
	DependenciesJSON    string `json:"-"`
}

type ConfigDriftState struct {
	HostID         int64  `json:"host_id"`
	ContainerName  string `json:"container_name"`
	Status         string `json:"status"`
	DetailsJSON    string `json:"details_json"`
	BaselineAt     string `json:"baseline_at"`
	BaselineJSON   string `json:"baseline_json,omitempty"`
	BaselineSource string `json:"baseline_source,omitempty"`
	CheckedAt      string `json:"checked_at"`
	Error          string `json:"error"`
}

type RegistryCredential struct {
	ID        int64  `json:"id"`
	Registry  string `json:"registry"`
	Username  string `json:"username"`
	SecretEnc string `json:"-"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r *RegistryCredential) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        int64  `json:"id"`
		Registry  string `json:"registry"`
		Username  string `json:"username"`
		SecretEnc string `json:"secret_enc"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.ID, r.Registry, r.Username, r.SecretEnc, r.CreatedAt, r.UpdatedAt =
		wire.ID, wire.Registry, wire.Username, wire.SecretEnc, wire.CreatedAt, wire.UpdatedAt
	return nil
}

type DockerEvent struct {
	ID      int64  `json:"id"`
	TS      string `json:"ts"`
	HostID  int64  `json:"host_id"`
	RawJSON string `json:"raw_json"`
}

func New(path string) *Store {
	return &Store{
		Path:      path,
		EventPath: filepath.Join(filepath.Dir(path), "logs", "docker-events.jsonl"),
	}
}

func (s *Store) Init(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.EventPath), 0o755); err != nil {
		return err
	}
	schema := `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000;
CREATE TABLE IF NOT EXISTS hosts (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, endpoint TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1, worker_token TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS policies (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'manual', check_interval_minutes INTEGER NOT NULL DEFAULT 30, release_repo TEXT NOT NULL DEFAULT '', last_checked_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS container_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, image TEXT NOT NULL DEFAULT '', image_id TEXT NOT NULL DEFAULT '', current_digest TEXT NOT NULL DEFAULT '', latest_digest TEXT NOT NULL DEFAULT '', update_available INTEGER NOT NULL DEFAULT 0, first_detected_at TEXT NOT NULL DEFAULT '', snoozed_digest TEXT NOT NULL DEFAULT '', snoozed_at TEXT NOT NULL DEFAULT '', last_checked_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, trigger TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '', summary_json TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS job_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, ts TEXT NOT NULL, level TEXT NOT NULL, source TEXT NOT NULL, message TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron TEXT NOT NULL, action TEXT NOT NULL, host_id INTEGER NOT NULL, containers TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0, container_name TEXT NOT NULL DEFAULT '', details TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS version_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, installed_version TEXT NOT NULL DEFAULT '', installed_source TEXT NOT NULL DEFAULT '', latest_version TEXT NOT NULL DEFAULT '', latest_source TEXT NOT NULL DEFAULT '', release_repo TEXT NOT NULL DEFAULT '', published_at TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS automations (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron TEXT NOT NULL, target_type TEXT NOT NULL DEFAULT 'host', target_id INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS host_groups (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS host_group_members (group_id INTEGER NOT NULL, host_id INTEGER NOT NULL, PRIMARY KEY(group_id,host_id));
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'user', enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS user_hosts (user_id INTEGER NOT NULL, host_id INTEGER NOT NULL, PRIMARY KEY(user_id,host_id));
CREATE TABLE IF NOT EXISTS user_groups (user_id INTEGER NOT NULL, group_id INTEGER NOT NULL, PRIMARY KEY(user_id,group_id));
CREATE TABLE IF NOT EXISTS notification_settings (user_id INTEGER PRIMARY KEY, pushover_app_token TEXT NOT NULL DEFAULT '', pushover_user_key TEXT NOT NULL DEFAULT '', notify_auto_updates INTEGER NOT NULL DEFAULT 1, notify_manual_available INTEGER NOT NULL DEFAULT 1, notify_manual_updates INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS notification_state (user_id INTEGER NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, event TEXT NOT NULL, fingerprint TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(user_id,host_id,container_name,event));
CREATE TABLE IF NOT EXISTS notification_deliveries (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, user_id INTEGER NOT NULL, username TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0, container_name TEXT NOT NULL DEFAULT '', event TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS update_history (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, action TEXT NOT NULL DEFAULT 'update', trigger TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, from_version TEXT NOT NULL DEFAULT '', to_version TEXT NOT NULL DEFAULT '', from_image_ref TEXT NOT NULL DEFAULT '', to_image_ref TEXT NOT NULL DEFAULT '', from_digest TEXT NOT NULL DEFAULT '', to_digest TEXT NOT NULL DEFAULT '', snapshot_id TEXT NOT NULL DEFAULT '', restore_point_id INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', dependency_count INTEGER NOT NULL DEFAULT 0, dependency_status TEXT NOT NULL DEFAULT '', dependency_details TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS restore_points (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, snapshot_id TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', trigger TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'ready', image_ref TEXT NOT NULL DEFAULT '', image_id TEXT NOT NULL DEFAULT '', original_image_ref TEXT NOT NULL DEFAULT '', original_image_id TEXT NOT NULL DEFAULT '', target_digest TEXT NOT NULL DEFAULT '', from_version TEXT NOT NULL DEFAULT '', unit_kind TEXT NOT NULL DEFAULT '', unit_key TEXT NOT NULL DEFAULT '', stack_type TEXT NOT NULL DEFAULT '', writable_layer INTEGER NOT NULL DEFAULT 0, config_protected INTEGER NOT NULL DEFAULT 1, volume_data_protected INTEGER NOT NULL DEFAULT 0, volume_count INTEGER NOT NULL DEFAULT 0, bind_count INTEGER NOT NULL DEFAULT 0, restore_count INTEGER NOT NULL DEFAULT 0, last_restored_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', dependency_count INTEGER NOT NULL DEFAULT 0, dependencies_json TEXT NOT NULL DEFAULT '[]');
CREATE TABLE IF NOT EXISTS config_drift_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'not_checked', details_json TEXT NOT NULL DEFAULT '[]', baseline_at TEXT NOT NULL DEFAULT '', baseline_json TEXT NOT NULL DEFAULT '', baseline_source TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS registry_credentials (id INTEGER PRIMARY KEY AUTOINCREMENT, registry TEXT NOT NULL UNIQUE COLLATE NOCASE, username TEXT NOT NULL DEFAULT '', secret_enc TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(id DESC); CREATE INDEX IF NOT EXISTS idx_update_history_id ON update_history(id DESC); CREATE INDEX IF NOT EXISTS idx_restore_points_container ON restore_points(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_restore_points_snapshot ON restore_points(host_id,snapshot_id); CREATE INDEX IF NOT EXISTS idx_update_history_container ON update_history(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_notification_deliveries_id ON notification_deliveries(id DESC); CREATE INDEX IF NOT EXISTS idx_job_logs_job ON job_logs(job_id,id); CREATE INDEX IF NOT EXISTS idx_group_members_host ON host_group_members(host_id); CREATE INDEX IF NOT EXISTS idx_user_hosts_host ON user_hosts(host_id);`
	if err := s.exec(ctx, schema); err != nil {
		return err
	}
	// Safe migrations for databases created by older Vibewatch releases.
	if err := s.exec(ctx, "ALTER TABLE version_cache ADD COLUMN latest_source TEXT NOT NULL DEFAULT '';"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	if err := s.exec(ctx, "ALTER TABLE notification_settings ADD COLUMN pushover_app_token TEXT NOT NULL DEFAULT '';"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	if err := s.exec(ctx, "ALTER TABLE notification_settings ADD COLUMN notify_manual_updates INTEGER NOT NULL DEFAULT 1;"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	for _, stmt := range []string{
		"ALTER TABLE container_cache ADD COLUMN first_detected_at TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE container_cache ADD COLUMN snoozed_digest TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE container_cache ADD COLUMN snoozed_at TEXT NOT NULL DEFAULT '';",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	if err := s.exec(ctx, "ALTER TABLE update_history ADD COLUMN restore_point_id INTEGER NOT NULL DEFAULT 0;"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	for _, stmt := range []string{
		"ALTER TABLE update_history ADD COLUMN dependency_count INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE update_history ADD COLUMN dependency_status TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_history ADD COLUMN dependency_details TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE restore_points ADD COLUMN dependency_count INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE restore_points ADD COLUMN dependencies_json TEXT NOT NULL DEFAULT '[]';",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	if err := s.exec(ctx, "ALTER TABLE config_drift_cache ADD COLUMN baseline_json TEXT NOT NULL DEFAULT '';"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	if err := s.exec(ctx, "ALTER TABLE config_drift_cache ADD COLUMN baseline_source TEXT NOT NULL DEFAULT '';"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	return nil
}

func q(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
func b(v Bool) int {
	if v {
		return 1
	}
	return 0
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

const sqliteBusyTimeoutMS = 10000

// Every database operation is serialized inside the controller process. The
// application uses the sqlite3 CLI for a deliberately dependency-light V0.1
// runtime; without this guard, Docker event logging, the worker supervisor and
// scheduled jobs can spawn competing sqlite3 processes and hit SQLITE_BUSY.
// WAL remains enabled for durability/read behaviour, while .timeout also makes
// the database tolerant of short-lived locks from external sqlite readers.
func (s *Store) exec(ctx context.Context, sql string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execLocked(ctx, sql)
}

func (s *Store) execLocked(ctx context.Context, sql string) error {
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), s.Path)
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sqlite: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s *Store) query(ctx context.Context, sql string, dst any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), "-json", s.Path, sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sqlite query: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		out = []byte("[]")
	}
	return json.Unmarshal(out, dst)
}

func (s *Store) scalarInt(ctx context.Context, sql string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), s.Path, sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("sqlite scalar: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		return 0, fmt.Errorf("sqlite scalar returned no value")
	}
	return strconv.ParseInt(lines[len(lines)-1], 10, 64)
}

// IntegrityCheck runs SQLite's read-only integrity check for diagnostics.
// It never mutates or repairs the database.
func (s *Store) IntegrityCheck(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), s.Path, "PRAGMA integrity_check;")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("sqlite integrity check: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

var recoverableCoreTables = []string{
	"hosts", "policies", "container_cache", "jobs", "job_logs", "schedules",
	"audit_events", "settings", "version_cache", "automations", "host_groups",
	"host_group_members", "users", "user_hosts", "user_groups",
	"notification_settings", "notification_state", "notification_deliveries", "update_history", "restore_points", "config_drift_cache", "registry_credentials",
}

// RepairDockerEventCorruption is a narrowly-scoped startup recovery for the
// V0.5.0/V0.5.1 failure mode where the high-volume docker_events B-tree/index
// became malformed while the rest of Vibewatch remained readable. It first
// proves every core table passes a partial SQLite integrity check, preserves a
// raw copy of the damaged DB (+ WAL/SHM sidecars), reconstructs a fresh primary
// DB without docker_events, verifies it, and only then atomically replaces the
// damaged primary file. No automatic repair is attempted for broader damage.
func (s *Store) RepairDockerEventCorruption(ctx context.Context, backupDir string) (bool, string, error) {
	result, integrityErr := s.IntegrityCheck(ctx)
	if integrityErr == nil && strings.TrimSpace(result) == "ok" {
		return false, "", nil
	}
	low := strings.ToLower(result)
	knownEventMarker := strings.Contains(low, "idx_events_host") || strings.Contains(low, "docker_events")
	if integrityErr != nil {
		lowErr := strings.ToLower(integrityErr.Error())
		if !strings.Contains(lowErr, "malformed") && !strings.Contains(lowErr, "corrupt") {
			return false, "", integrityErr
		}
		// Some sqlite3 builds return SQLITE_CORRUPT as the command status instead
		// of printing the detailed integrity rows. Only continue if the legacy
		// event objects actually exist; the per-table checks below still have to
		// prove every core table healthy before any rebuild is attempted.
		n, err := s.scalarInt(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('docker_events','idx_events_host');`)
		if err != nil || n == 0 {
			return false, "", integrityErr
		}
		knownEventMarker = true
		result = integrityErr.Error()
	}
	if !knownEventMarker {
		return false, "", fmt.Errorf("database integrity check failed outside the known Docker-event failure mode: %s", result)
	}
	tables := make([]string, 0, len(recoverableCoreTables))
	for _, table := range recoverableCoreTables {
		exists, existsErr := s.tableExists(ctx, table)
		if existsErr != nil {
			return false, "", fmt.Errorf("core table %s existence check failed: %w", table, existsErr)
		}
		// Newer Vibewatch releases add core tables over time. Repair runs before
		// additive schema migration, so an older damaged database legitimately
		// does not contain every table known by this binary.
		if !exists {
			continue
		}
		ok, checkErr := s.partialIntegrityCheck(ctx, table)
		if checkErr != nil {
			return false, "", fmt.Errorf("core table %s could not be verified: %w", table, checkErr)
		}
		if !ok {
			return false, "", fmt.Errorf("database corruption is not isolated to Docker events; core table %s failed integrity check", table)
		}
		tables = append(tables, table)
	}
	backup, err := s.rebuildPrimaryWithoutDockerEvents(ctx, backupDir, tables)
	if err != nil {
		return false, "", err
	}
	return true, backup, nil
}

func (s *Store) tableExists(ctx context.Context, table string) (bool, error) {
	n, err := s.scalarInt(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=%s;`, q(table)))
	return n > 0, err
}

func (s *Store) partialIntegrityCheck(ctx context.Context, table string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), s.Path, fmt.Sprintf("PRAGMA integrity_check(%s);", q(table)))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("sqlite partial integrity check: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)) == "ok", nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (s *Store) rebuildPrimaryWithoutDockerEvents(ctx context.Context, backupDir string, tables []string) (string, error) {
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, "corrupt-docker-events-"+stamp+".db")
	if err := copyFile(s.Path, backup); err != nil {
		return "", fmt.Errorf("preserve corrupt database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(s.Path + suffix); err == nil {
			_ = copyFile(s.Path+suffix, backup+suffix)
		}
	}

	tmp := s.Path + ".recovering"
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	fresh := New(tmp)
	if err := fresh.Init(ctx); err != nil {
		return "", fmt.Errorf("initialize recovery database: %w", err)
	}
	var script strings.Builder
	script.WriteString("PRAGMA foreign_keys=OFF;\n")
	script.WriteString("ATTACH DATABASE " + q(s.Path) + " AS damaged;\nBEGIN IMMEDIATE;\n")
	for _, table := range tables {
		script.WriteString("INSERT INTO " + table + " SELECT * FROM damaged." + table + ";\n")
	}
	script.WriteString("COMMIT;\nDETACH DATABASE damaged;\n")
	cmd := exec.CommandContext(ctx, "sqlite3", "-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS), tmp)
	cmd.Stdin = strings.NewReader(script.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("copy healthy tables into recovery database: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// Init uses WAL mode. Flush every reconstructed row into the main temp DB
	// before renaming it; otherwise a temp-name WAL sidecar could be left behind.
	if err := fresh.exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("checkpoint recovery database: %w", err)
	}
	check, err := fresh.IntegrityCheck(ctx)
	if err != nil || strings.TrimSpace(check) != "ok" {
		_ = os.Remove(tmp)
		if err != nil {
			return "", fmt.Errorf("verify recovery database: %w", err)
		}
		return "", fmt.Errorf("recovery database integrity check failed: %s", check)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(tmp + suffix)
		_ = os.Remove(s.Path + suffix)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return "", fmt.Errorf("activate recovery database: %w", err)
	}
	return filepath.Base(backup), nil
}

func (s *Store) Hosts(ctx context.Context) ([]Host, error) {
	var x []Host
	err := s.query(ctx, `SELECT id,name,endpoint,enabled,worker_token,created_at FROM hosts ORDER BY name`, &x)
	return x, err
}
func (s *Store) Host(ctx context.Context, id int64) (Host, error) {
	var x []Host
	err := s.query(ctx, fmt.Sprintf(`SELECT id,name,endpoint,enabled,worker_token,created_at FROM hosts WHERE id=%d`, id), &x)
	if err != nil {
		return Host{}, err
	}
	if len(x) == 0 {
		return Host{}, os.ErrNotExist
	}
	return x[0], nil
}
func (s *Store) CreateHost(ctx context.Context, name, endpoint, token string, enabled Bool) (int64, error) {
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO hosts(name,endpoint,enabled,worker_token,created_at) VALUES(%s,%s,%d,%s,%s); SELECT last_insert_rowid();`, q(name), q(endpoint), b(enabled), q(token), q(now())))
}
func (s *Store) RenameHost(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if id <= 0 || name == "" {
		return fmt.Errorf("valid host id and name are required")
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE hosts SET name=%s WHERE id=%d`, q(name), id))
}
func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM policies WHERE host_id=%d; DELETE FROM container_cache WHERE host_id=%d; DELETE FROM version_cache WHERE host_id=%d; DELETE FROM schedules WHERE host_id=%d; DELETE FROM host_group_members WHERE host_id=%d; DELETE FROM user_hosts WHERE host_id=%d; DELETE FROM automations WHERE target_type='host' AND target_id=%d; DELETE FROM notification_state WHERE host_id=%d; DELETE FROM config_drift_cache WHERE host_id=%d; DELETE FROM restore_points WHERE host_id=%d; DELETE FROM hosts WHERE id=%d;`, id, id, id, id, id, id, id, id, id, id, id))
}

func (s *Store) Policy(ctx context.Context, hostID int64, name string) (Policy, error) {
	var x []Policy
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,mode,check_interval_minutes,release_repo,last_checked_at FROM policies WHERE host_id=%d AND container_name=%s`, hostID, q(name)), &x)
	if err != nil {
		return Policy{}, err
	}
	if len(x) == 0 {
		return Policy{HostID: hostID, ContainerName: name, Mode: "manual", CheckIntervalMinutes: 30}, nil
	}
	return x[0], nil
}
func (s *Store) Policies(ctx context.Context) ([]Policy, error) {
	var x []Policy
	err := s.query(ctx, `SELECT host_id,container_name,mode,check_interval_minutes,release_repo,last_checked_at FROM policies ORDER BY host_id,container_name`, &x)
	return x, err
}
func (s *Store) SavePolicy(ctx context.Context, p Policy) error {
	if p.CheckIntervalMinutes < 1 {
		p.CheckIntervalMinutes = 30
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO policies(host_id,container_name,mode,check_interval_minutes,release_repo,last_checked_at) VALUES(%d,%s,%s,%d,%s,COALESCE((SELECT last_checked_at FROM policies WHERE host_id=%d AND container_name=%s),'')) ON CONFLICT(host_id,container_name) DO UPDATE SET mode=excluded.mode,check_interval_minutes=excluded.check_interval_minutes,release_repo=excluded.release_repo;`, p.HostID, q(p.ContainerName), q(p.Mode), p.CheckIntervalMinutes, q(p.ReleaseRepo), p.HostID, q(p.ContainerName)))
}
func (s *Store) TouchPolicy(ctx context.Context, hostID int64, name string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE policies SET last_checked_at=%s WHERE host_id=%d AND container_name=%s;`, q(now()), hostID, q(name)))
}

func (s *Store) Cache(ctx context.Context, hostID int64, name string) (Cache, error) {
	var x []Cache
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,image,image_id,current_digest,latest_digest,update_available,first_detected_at,snoozed_digest,snoozed_at,last_checked_at,last_error FROM container_cache WHERE host_id=%d AND container_name=%s`, hostID, q(name)), &x)
	if err != nil {
		return Cache{}, err
	}
	if len(x) == 0 {
		return Cache{HostID: hostID, ContainerName: name}, nil
	}
	return x[0], nil
}
func (s *Store) SaveCache(ctx context.Context, c Cache) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO container_cache(host_id,container_name,image,image_id,current_digest,latest_digest,update_available,first_detected_at,snoozed_digest,snoozed_at,last_checked_at,last_error) VALUES(%d,%s,%s,%s,%s,%s,%d,%s,%s,%s,%s,%s) ON CONFLICT(host_id,container_name) DO UPDATE SET image=excluded.image,image_id=excluded.image_id,current_digest=excluded.current_digest,latest_digest=excluded.latest_digest,update_available=excluded.update_available,first_detected_at=excluded.first_detected_at,snoozed_digest=excluded.snoozed_digest,snoozed_at=excluded.snoozed_at,last_checked_at=excluded.last_checked_at,last_error=excluded.last_error;`, c.HostID, q(c.ContainerName), q(c.Image), q(c.ImageID), q(c.CurrentDigest), q(c.LatestDigest), b(c.UpdateAvailable), q(c.FirstDetectedAt), q(c.SnoozedDigest), q(c.SnoozedAt), q(c.LastCheckedAt), q(c.LastError)))
}

func (s *Store) CreateJob(ctx context.Context, typ, trigger string, hostID int64, container, status string) (int64, error) {
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO jobs(type,trigger,host_id,container_name,status) VALUES(%s,%s,%d,%s,%s); SELECT last_insert_rowid();`, q(typ), q(trigger), hostID, q(container), q(status)))
}
func (s *Store) StartJob(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status='running',started_at=%s WHERE id=%d;`, q(now()), id))
}
func (s *Store) FinishJob(ctx context.Context, id int64, status, summary, errMsg string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status=%s,finished_at=%s,summary_json=%s,error=%s WHERE id=%d;`, q(status), q(now()), q(summary), q(errMsg), id))
}
func (s *Store) Jobs(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var x []Job
	err := s.query(ctx, fmt.Sprintf(`SELECT id,type,trigger,host_id,container_name,status,started_at,finished_at,summary_json,error FROM jobs ORDER BY id DESC LIMIT %d`, limit), &x)
	return x, err
}
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	var x []Job
	err := s.query(ctx, fmt.Sprintf(`SELECT id,type,trigger,host_id,container_name,status,started_at,finished_at,summary_json,error FROM jobs WHERE id=%d LIMIT 1`, id), &x)
	if err != nil {
		return Job{}, err
	}
	if len(x) == 0 {
		return Job{}, fmt.Errorf("job %d not found", id)
	}
	return x[0], nil
}
func (s *Store) HasActiveJob(ctx context.Context, hostID int64, name string) (bool, error) {
	n, err := s.scalarInt(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM jobs WHERE host_id=%d AND container_name=%s AND status IN ('queued','running');`, hostID, q(name)))
	return n > 0, err
}
func (s *Store) AddJobLog(ctx context.Context, jobID int64, level, source, message string) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO job_logs(job_id,ts,level,source,message) VALUES(%d,%s,%s,%s,%s);`, jobID, q(now()), q(level), q(source), q(message)))
}
func (s *Store) JobLogs(ctx context.Context, jobID int64) ([]JobLog, error) {
	var x []JobLog
	err := s.query(ctx, fmt.Sprintf(`SELECT id,job_id,ts,level,source,message FROM job_logs WHERE job_id=%d ORDER BY id`, jobID), &x)
	return x, err
}

func (s *Store) AddUpdateHistory(ctx context.Context, x UpdateHistory) (int64, error) {
	if strings.TrimSpace(x.Action) == "" {
		x.Action = "update"
	}
	if strings.TrimSpace(x.Status) == "" {
		x.Status = "unknown"
	}
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_history(ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details) VALUES(%s,%d,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%s,%d,%s,%s); SELECT last_insert_rowid();`,
		q(now()), x.HostID, q(x.ContainerName), q(x.Action), q(x.Trigger), q(x.Actor), q(x.Status), q(x.FromVersion), q(x.ToVersion), q(x.FromImageRef), q(x.ToImageRef), q(x.FromDigest), q(x.ToDigest), q(x.SnapshotID), x.RestorePointID, x.DurationMS, q(x.Error), x.DependencyCount, q(x.DependencyStatus), q(x.DependencyDetails)))
	if err == nil {
		_ = s.exec(ctx, `DELETE FROM update_history WHERE id NOT IN (SELECT id FROM update_history ORDER BY id DESC LIMIT 5000);`)
	}
	return id, err
}
func (s *Store) UpdateHistory(ctx context.Context, limit int, hostID int64, container string) ([]UpdateHistory, error) {
	if limit < 1 || limit > 1000 {
		limit = 250
	}
	where := []string{"1=1"}
	if hostID > 0 {
		where = append(where, fmt.Sprintf("host_id=%d", hostID))
	}
	if strings.TrimSpace(container) != "" {
		where = append(where, "container_name="+q(strings.TrimSpace(container)))
	}
	var x []UpdateHistory
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details FROM update_history WHERE %s ORDER BY id DESC LIMIT %d`, strings.Join(where, " AND "), limit), &x)
	return x, err
}
func (s *Store) UpdateHistoryEntry(ctx context.Context, id int64) (UpdateHistory, error) {
	var x []UpdateHistory
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details FROM update_history WHERE id=%d LIMIT 1`, id), &x)
	if err != nil {
		return UpdateHistory{}, err
	}
	if len(x) == 0 {
		return UpdateHistory{}, fmt.Errorf("update history entry %d not found", id)
	}
	return x[0], nil
}

func (s *Store) AddRestorePoint(ctx context.Context, x RestorePoint) (int64, error) {
	if strings.TrimSpace(x.Status) == "" {
		x.Status = "ready"
	}
	ts := now()
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO restore_points(created_at,updated_at,host_id,container_name,snapshot_id,reason,trigger,status,image_ref,image_id,original_image_ref,original_image_id,target_digest,from_version,unit_kind,unit_key,stack_type,writable_layer,config_protected,volume_data_protected,volume_count,bind_count,restore_count,last_restored_at,last_error,dependency_count,dependencies_json) VALUES(%s,%s,%d,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%d,%d,%d,%d,%s,%s,%d,%s); SELECT last_insert_rowid();`,
		q(ts), q(ts), x.HostID, q(x.ContainerName), q(x.SnapshotID), q(x.Reason), q(x.Trigger), q(x.Status), q(x.ImageRef), q(x.ImageID), q(x.OriginalImageRef), q(x.OriginalImageID), q(x.TargetDigest), q(x.FromVersion), q(x.UnitKind), q(x.UnitKey), q(x.StackType), b(x.WritableLayer), b(x.ConfigProtected), b(x.VolumeDataProtected), x.VolumeCount, x.BindCount, x.RestoreCount, q(x.LastRestoredAt), q(x.LastError), x.DependencyCount, q(x.DependenciesJSON)))
	if err == nil {
		_ = s.exec(ctx, `DELETE FROM restore_points WHERE id NOT IN (SELECT id FROM restore_points ORDER BY id DESC LIMIT 5000);`)
	}
	return id, err
}

func restorePointSelect() string {
	return `id,created_at,updated_at,host_id,container_name,snapshot_id,reason,trigger,status,image_ref,image_id,original_image_ref,original_image_id,target_digest,from_version,unit_kind,unit_key,stack_type,writable_layer,config_protected,volume_data_protected,volume_count,bind_count,restore_count,last_restored_at,last_error,dependency_count,dependencies_json`
}

func (s *Store) RestorePoint(ctx context.Context, id int64) (RestorePoint, error) {
	var xs []RestorePoint
	err := s.query(ctx, fmt.Sprintf(`SELECT %s FROM restore_points WHERE id=%d LIMIT 1`, restorePointSelect(), id), &xs)
	if err != nil {
		return RestorePoint{}, err
	}
	if len(xs) == 0 {
		return RestorePoint{}, fmt.Errorf("restore point %d not found", id)
	}
	return xs[0], nil
}

func (s *Store) RestorePoints(ctx context.Context, limit int, hostID int64, container string) ([]RestorePoint, error) {
	if limit < 1 || limit > 2000 {
		limit = 500
	}
	where := []string{"1=1"}
	if hostID > 0 {
		where = append(where, fmt.Sprintf("host_id=%d", hostID))
	}
	if strings.TrimSpace(container) != "" {
		where = append(where, "container_name="+q(strings.TrimSpace(container)))
	}
	var xs []RestorePoint
	err := s.query(ctx, fmt.Sprintf(`SELECT %s FROM restore_points WHERE %s ORDER BY id DESC LIMIT %d`, restorePointSelect(), strings.Join(where, " AND "), limit), &xs)
	return xs, err
}

func (s *Store) LatestRestorePointsForHost(ctx context.Context, hostID int64) (map[string]RestorePoint, error) {
	xs, err := s.RestorePoints(ctx, 2000, hostID, "")
	if err != nil {
		return nil, err
	}
	// Prefer the newest actually usable full-container restore point. A newer
	// degraded/config-only row must not hide an older retained full restore
	// point from the Containers page. If no full point exists, keep the newest
	// non-expired row so the UI can still explain the protection state.
	out := map[string]RestorePoint{}
	fallback := map[string]RestorePoint{}
	for _, x := range xs {
		if x.Status == "expired" || x.Status == "failed" {
			continue
		}
		if _, ok := fallback[x.ContainerName]; !ok {
			fallback[x.ContainerName] = x
		}
		if _, ok := out[x.ContainerName]; ok {
			continue
		}
		if bool(x.WritableLayer) && strings.TrimSpace(x.ImageRef) != "" && x.Status != "degraded" && x.Status != "config_only" {
			out[x.ContainerName] = x
		}
	}
	for name, x := range fallback {
		if _, ok := out[name]; !ok {
			out[name] = x
		}
	}
	return out, nil
}

func (s *Store) SetRestorePointStatus(ctx context.Context, id int64, status, lastError string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE restore_points SET status=%s,last_error=%s,updated_at=%s WHERE id=%d`, q(status), q(lastError), q(now()), id))
}

func (s *Store) MarkRestorePointRestored(ctx context.Context, id int64, lastError string) error {
	status := "restored"
	if strings.TrimSpace(lastError) != "" {
		status = "degraded"
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE restore_points SET status=%s,restore_count=restore_count+1,last_restored_at=%s,last_error=%s,updated_at=%s WHERE id=%d`, q(status), q(now()), q(lastError), q(now()), id))
}

func (s *Store) ExpireRestorePointsBySnapshot(ctx context.Context, hostID int64, snapshotID string) ([]RestorePoint, error) {
	var xs []RestorePoint
	err := s.query(ctx, fmt.Sprintf(`SELECT %s FROM restore_points WHERE host_id=%d AND snapshot_id=%s AND status!='expired'`, restorePointSelect(), hostID, q(snapshotID)), &xs)
	if err != nil {
		return nil, err
	}
	if len(xs) > 0 {
		if err := s.exec(ctx, fmt.Sprintf(`UPDATE restore_points SET status='expired',updated_at=%s WHERE host_id=%d AND snapshot_id=%s`, q(now()), hostID, q(snapshotID))); err != nil {
			return nil, err
		}
	}
	return xs, nil
}

func (s *Store) ConfigDrift(ctx context.Context, hostID int64, container string) (ConfigDriftState, error) {
	var x []ConfigDriftState
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,status,details_json,baseline_at,baseline_json,baseline_source,checked_at,error FROM config_drift_cache WHERE host_id=%d AND container_name=%s LIMIT 1`, hostID, q(container)), &x)
	if err != nil {
		return ConfigDriftState{}, err
	}
	if len(x) == 0 {
		return ConfigDriftState{HostID: hostID, ContainerName: container, Status: "not_checked", DetailsJSON: "[]"}, nil
	}
	return x[0], nil
}
func (s *Store) SaveConfigDrift(ctx context.Context, x ConfigDriftState) error {
	if strings.TrimSpace(x.Status) == "" {
		x.Status = "not_checked"
	}
	if strings.TrimSpace(x.DetailsJSON) == "" {
		x.DetailsJSON = "[]"
	}
	if strings.TrimSpace(x.CheckedAt) == "" {
		x.CheckedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO config_drift_cache(host_id,container_name,status,details_json,baseline_at,baseline_json,baseline_source,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s,%s,%s,%s) ON CONFLICT(host_id,container_name) DO UPDATE SET status=excluded.status,details_json=excluded.details_json,baseline_at=excluded.baseline_at,baseline_json=excluded.baseline_json,baseline_source=excluded.baseline_source,checked_at=excluded.checked_at,error=excluded.error;`,
		x.HostID, q(x.ContainerName), q(x.Status), q(x.DetailsJSON), q(x.BaselineAt), q(x.BaselineJSON), q(x.BaselineSource), q(x.CheckedAt), q(x.Error)))
}

func (s *Store) RegistryCredentials(ctx context.Context) ([]RegistryCredential, error) {
	var x []RegistryCredential
	err := s.query(ctx, `SELECT id,registry,username,secret_enc,created_at,updated_at FROM registry_credentials ORDER BY registry`, &x)
	return x, err
}
func (s *Store) RegistryCredential(ctx context.Context, registry string) (RegistryCredential, error) {
	var x []RegistryCredential
	err := s.query(ctx, fmt.Sprintf(`SELECT id,registry,username,secret_enc,created_at,updated_at FROM registry_credentials WHERE registry=%s COLLATE NOCASE LIMIT 1`, q(strings.TrimSpace(registry))), &x)
	if err != nil {
		return RegistryCredential{}, err
	}
	if len(x) == 0 {
		return RegistryCredential{}, fmt.Errorf("registry credential not found")
	}
	return x[0], nil
}
func (s *Store) SaveRegistryCredential(ctx context.Context, registry, username, secretEnc string) (int64, error) {
	registry, username = strings.TrimSpace(registry), strings.TrimSpace(username)
	if registry == "" || secretEnc == "" {
		return 0, fmt.Errorf("registry and secret are required")
	}
	ts := now()
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO registry_credentials(registry,username,secret_enc,created_at,updated_at) VALUES(%s,%s,%s,%s,%s) ON CONFLICT(registry) DO UPDATE SET username=excluded.username,secret_enc=excluded.secret_enc,updated_at=excluded.updated_at; SELECT id FROM registry_credentials WHERE registry=%s COLLATE NOCASE;`,
		q(registry), q(username), q(secretEnc), q(ts), q(ts), q(registry)))
}
func (s *Store) DeleteRegistryCredential(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM registry_credentials WHERE id=%d`, id))
}

func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	var x []Schedule
	err := s.query(ctx, `SELECT id,name,cron,action,host_id,containers,enabled,last_run_at FROM schedules ORDER BY name`, &x)
	return x, err
}
func (s *Store) SaveSchedule(ctx context.Context, x Schedule) (int64, error) {
	if x.ID == 0 {
		return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO schedules(name,cron,action,host_id,containers,enabled) VALUES(%s,%s,%s,%d,%s,%d); SELECT last_insert_rowid();`, q(x.Name), q(x.Cron), q(x.Action), x.HostID, q(x.Containers), b(x.Enabled)))
	}
	return x.ID, s.exec(ctx, fmt.Sprintf(`UPDATE schedules SET name=%s,cron=%s,action=%s,host_id=%d,containers=%s,enabled=%d WHERE id=%d;`, q(x.Name), q(x.Cron), q(x.Action), x.HostID, q(x.Containers), b(x.Enabled), x.ID))
}
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM schedules WHERE id=%d;`, id))
}
func (s *Store) TouchSchedule(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE schedules SET last_run_at=%s WHERE id=%d;`, q(now()), id))
}

func (s *Store) Audit(ctx context.Context, actor, action string, hostID int64, container, details string) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO audit_events(ts,actor,action,host_id,container_name,details) VALUES(%s,%s,%s,%d,%s,%s);`, q(now()), q(actor), q(action), hostID, q(container), q(details)))
}
func (s *Store) Audits(ctx context.Context, limit int) ([]Audit, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var x []Audit
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,actor,action,host_id,container_name,details FROM audit_events ORDER BY id DESC LIMIT %d`, limit), &x)
	return x, err
}

const dockerEventRetention = 5000

// Docker events are intentionally stored outside the primary Vibewatch SQLite
// database. Docker's event stream can generate tens of thousands of short-lived
// healthcheck/exec records, and that write-heavy diagnostic data must never be
// able to damage hosts, users, policies, jobs or notification state. JSONL is a
// natural append-only format for this bounded diagnostic history.
func (s *Store) AddDockerEvent(ctx context.Context, hostID int64, raw string) error {
	_ = ctx
	if hostID <= 0 || strings.TrimSpace(raw) == "" || noisyDockerEvent(raw) {
		return nil
	}
	e := DockerEvent{ID: time.Now().UnixNano(), TS: now(), HostID: hostID, RawJSON: raw}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.EventPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.EventPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	s.eventWrites++
	if s.eventWrites%250 == 0 {
		return s.trimDockerEventsLocked(dockerEventRetention)
	}
	return nil
}

func noisyDockerEvent(raw string) bool {
	var v struct {
		Action string `json:"Action"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return false
	}
	action := strings.ToLower(strings.TrimSpace(v.Action))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(v.Status))
	}
	switch action {
	case "exec_create", "exec_start", "exec_die":
		return true
	default:
		return false
	}
}

func (s *Store) trimDockerEventsLocked(keep int) error {
	b, err := os.ReadFile(s.EventPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) <= keep {
		return nil
	}
	trimmed := strings.Join(lines[len(lines)-keep:], "\n") + "\n"
	tmp := s.EventPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(trimmed), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.EventPath)
}

func (s *Store) DockerEvents(ctx context.Context, hostID int64, limit int) ([]DockerEvent, error) {
	_ = ctx
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	b, err := os.ReadFile(s.EventPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []DockerEvent{}, nil
		}
		return []DockerEvent{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	out := make([]DockerEvent, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var e DockerEvent
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			continue
		}
		if hostID > 0 && e.HostID != hostID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// MigrateLegacyDockerEvents copies a small tail of the old SQLite event table
// into the bounded JSONL history on healthy upgrades. A malformed legacy event
// table is simply ignored because events are diagnostic data, not application
// state.
func (s *Store) MigrateLegacyDockerEvents(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > dockerEventRetention {
		limit = 500
	}
	if st, err := os.Stat(s.EventPath); err == nil && st.Size() > 0 {
		return 0, nil
	}
	var legacy []DockerEvent
	if err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,raw_json FROM docker_events ORDER BY id DESC LIMIT %d`, limit), &legacy); err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "no such table") || strings.Contains(low, "malformed") {
			return 0, nil
		}
		return 0, err
	}
	if len(legacy) == 0 {
		return 0, nil
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.EventPath), 0o755); err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	written := 0
	for i := len(legacy) - 1; i >= 0; i-- {
		if noisyDockerEvent(legacy[i].RawJSON) {
			continue
		}
		b, err := json.Marshal(legacy[i])
		if err != nil {
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
		written++
	}
	if buf.Len() == 0 {
		return 0, nil
	}
	if err := os.WriteFile(s.EventPath, buf.Bytes(), 0o640); err != nil {
		return 0, err
	}
	return written, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO settings(key,value) VALUES(%s,%s) ON CONFLICT(key) DO UPDATE SET value=excluded.value;`, q(key), q(value)))
}
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var x []struct {
		Value string `json:"value"`
	}
	if s.query(ctx, fmt.Sprintf(`SELECT value FROM settings WHERE key=%s`, q(key)), &x) != nil || len(x) == 0 {
		return def
	}
	return x[0].Value
}

// Backup creates a transactionally consistent standalone SQLite copy. The
// destination should live on the persistent /data mount so it survives image
// replacement and controller recreation. VACUUM INTO also folds WAL content
// into the backup instead of copying only the main database file.
func (s *Store) Backup(ctx context.Context, dest string) error {
	dest = filepath.Clean(dest)
	if dest == "." || dest == "" {
		return fmt.Errorf("backup destination is empty")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.execLocked(ctx, fmt.Sprintf("VACUUM INTO %s;", q(dest)))
}

func (s *Store) Version(ctx context.Context, hostID int64, name string) (VersionInfo, error) {
	var x []VersionInfo
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,installed_version,installed_source,latest_version,latest_source,release_repo,published_at,checked_at,error FROM version_cache WHERE host_id=%d AND container_name=%s`, hostID, q(name)), &x)
	if err != nil {
		return VersionInfo{}, err
	}
	if len(x) == 0 {
		return VersionInfo{HostID: hostID, ContainerName: name}, nil
	}
	return x[0], nil
}
func (s *Store) SaveVersion(ctx context.Context, v VersionInfo) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO version_cache(host_id,container_name,installed_version,installed_source,latest_version,latest_source,release_repo,published_at,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s,%s,%s,%s,%s) ON CONFLICT(host_id,container_name) DO UPDATE SET installed_version=excluded.installed_version,installed_source=excluded.installed_source,latest_version=excluded.latest_version,latest_source=excluded.latest_source,release_repo=excluded.release_repo,published_at=excluded.published_at,checked_at=excluded.checked_at,error=excluded.error;`, v.HostID, q(v.ContainerName), q(v.Installed), q(v.InstalledSource), q(v.Latest), q(v.LatestSource), q(v.ReleaseRepo), q(v.PublishedAt), q(v.CheckedAt), q(v.Error)))
}

func (s *Store) Automations(ctx context.Context) ([]Automation, error) {
	var x []Automation
	err := s.query(ctx, `SELECT id,name,cron,target_type,target_id,enabled,last_run_at FROM automations ORDER BY name`, &x)
	return x, err
}
func (s *Store) SaveAutomation(ctx context.Context, x Automation) (int64, error) {
	if x.ID == 0 {
		return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO automations(name,cron,target_type,target_id,enabled,last_run_at) VALUES(%s,%s,%s,%d,%d,%s); SELECT last_insert_rowid();`, q(x.Name), q(x.Cron), q(x.TargetType), x.TargetID, b(x.Enabled), q(x.LastRunAt)))
	}
	err := s.exec(ctx, fmt.Sprintf(`UPDATE automations SET name=%s,cron=%s,target_type=%s,target_id=%d,enabled=%d WHERE id=%d`, q(x.Name), q(x.Cron), q(x.TargetType), x.TargetID, b(x.Enabled), x.ID))
	return x.ID, err
}
func (s *Store) DeleteAutomation(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM automations WHERE id=%d`, id))
}
func (s *Store) TouchAutomation(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE automations SET last_run_at=%s WHERE id=%d`, q(now()), id))
}

func (s *Store) HostGroups(ctx context.Context) ([]HostGroup, error) {
	var x []HostGroup
	if err := s.query(ctx, `SELECT id,name,description,created_at FROM host_groups ORDER BY name`, &x); err != nil {
		return nil, err
	}
	for i := range x {
		x[i].HostIDs = make([]int64, 0)
		var rows []struct {
			HostID int64 `json:"host_id"`
		}
		if err := s.query(ctx, fmt.Sprintf(`SELECT host_id FROM host_group_members WHERE group_id=%d ORDER BY host_id`, x[i].ID), &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			x[i].HostIDs = append(x[i].HostIDs, r.HostID)
		}
	}
	return x, nil
}
func (s *Store) SaveHostGroup(ctx context.Context, g HostGroup) (int64, error) {
	var id int64
	var err error
	if g.ID == 0 {
		id, err = s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO host_groups(name,description,created_at) VALUES(%s,%s,%s); SELECT last_insert_rowid();`, q(g.Name), q(g.Description), q(now())))
	} else {
		id = g.ID
		err = s.exec(ctx, fmt.Sprintf(`UPDATE host_groups SET name=%s,description=%s WHERE id=%d`, q(g.Name), q(g.Description), id))
	}
	if err != nil {
		return 0, err
	}
	parts := []string{fmt.Sprintf(`DELETE FROM host_group_members WHERE group_id=%d`, id)}
	for _, hostID := range g.HostIDs {
		if hostID > 0 {
			parts = append(parts, fmt.Sprintf(`INSERT OR IGNORE INTO host_group_members(group_id,host_id) VALUES(%d,%d)`, id, hostID))
		}
	}
	return id, s.exec(ctx, strings.Join(parts, ";")+";")
}
func (s *Store) DeleteHostGroup(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM user_groups WHERE group_id=%d; DELETE FROM host_group_members WHERE group_id=%d; DELETE FROM automations WHERE target_type='group' AND target_id=%d; DELETE FROM host_groups WHERE id=%d;`, id, id, id, id))
}
func (s *Store) HostsForGroup(ctx context.Context, groupID int64) ([]int64, error) {
	var rows []struct {
		HostID int64 `json:"host_id"`
	}
	if err := s.query(ctx, fmt.Sprintf(`SELECT host_id FROM host_group_members WHERE group_id=%d ORDER BY host_id`, groupID), &rows); err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.HostID)
	}
	return out, nil
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	var x []User
	if err := s.query(ctx, `SELECT id,username,password_hash,role,enabled,created_at FROM users ORDER BY username`, &x); err != nil {
		return nil, err
	}
	for i := range x {
		if err := s.fillUserAssignments(ctx, &x[i]); err != nil {
			return nil, err
		}
	}
	return x, nil
}
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var x []User
	if err := s.query(ctx, fmt.Sprintf(`SELECT id,username,password_hash,role,enabled,created_at FROM users WHERE id=%d`, id), &x); err != nil {
		return User{}, err
	}
	if len(x) == 0 {
		return User{}, os.ErrNotExist
	}
	if err := s.fillUserAssignments(ctx, &x[0]); err != nil {
		return User{}, err
	}
	return x[0], nil
}
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var x []User
	if err := s.query(ctx, fmt.Sprintf(`SELECT id,username,password_hash,role,enabled,created_at FROM users WHERE username=%s COLLATE NOCASE`, q(username)), &x); err != nil {
		return User{}, err
	}
	if len(x) == 0 {
		return User{}, os.ErrNotExist
	}
	if err := s.fillUserAssignments(ctx, &x[0]); err != nil {
		return User{}, err
	}
	return x[0], nil
}
func (s *Store) fillUserAssignments(ctx context.Context, u *User) error {
	u.HostIDs = make([]int64, 0)
	u.GroupIDs = make([]int64, 0)
	var hs []struct {
		HostID int64 `json:"host_id"`
	}
	if err := s.query(ctx, fmt.Sprintf(`SELECT host_id FROM user_hosts WHERE user_id=%d ORDER BY host_id`, u.ID), &hs); err != nil {
		return err
	}
	for _, r := range hs {
		u.HostIDs = append(u.HostIDs, r.HostID)
	}
	var gs []struct {
		GroupID int64 `json:"group_id"`
	}
	if err := s.query(ctx, fmt.Sprintf(`SELECT group_id FROM user_groups WHERE user_id=%d ORDER BY group_id`, u.ID), &gs); err != nil {
		return err
	}
	for _, r := range gs {
		u.GroupIDs = append(u.GroupIDs, r.GroupID)
	}
	return nil
}
func (s *Store) SaveUser(ctx context.Context, u User) (int64, error) {
	var id int64
	var err error
	if u.ID == 0 {
		id, err = s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO users(username,password_hash,role,enabled,created_at) VALUES(%s,%s,%s,%d,%s); SELECT last_insert_rowid();`, q(u.Username), q(u.PasswordHash), q(u.Role), b(u.Enabled), q(now())))
	} else {
		id = u.ID
		if u.PasswordHash != "" {
			err = s.exec(ctx, fmt.Sprintf(`UPDATE users SET username=%s,password_hash=%s,role=%s,enabled=%d WHERE id=%d`, q(u.Username), q(u.PasswordHash), q(u.Role), b(u.Enabled), id))
		} else {
			err = s.exec(ctx, fmt.Sprintf(`UPDATE users SET username=%s,role=%s,enabled=%d WHERE id=%d`, q(u.Username), q(u.Role), b(u.Enabled), id))
		}
	}
	if err != nil {
		return 0, err
	}
	parts := []string{fmt.Sprintf(`DELETE FROM user_hosts WHERE user_id=%d`, id), fmt.Sprintf(`DELETE FROM user_groups WHERE user_id=%d`, id)}
	for _, v := range u.HostIDs {
		if v > 0 {
			parts = append(parts, fmt.Sprintf(`INSERT OR IGNORE INTO user_hosts(user_id,host_id) VALUES(%d,%d)`, id, v))
		}
	}
	for _, v := range u.GroupIDs {
		if v > 0 {
			parts = append(parts, fmt.Sprintf(`INSERT OR IGNORE INTO user_groups(user_id,group_id) VALUES(%d,%d)`, id, v))
		}
	}
	return id, s.exec(ctx, strings.Join(parts, ";")+";")
}
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM notification_state WHERE user_id=%d; DELETE FROM notification_settings WHERE user_id=%d; DELETE FROM user_hosts WHERE user_id=%d; DELETE FROM user_groups WHERE user_id=%d; DELETE FROM users WHERE id=%d;`, id, id, id, id, id))
}
func (s *Store) AllowedHostIDs(ctx context.Context, userID int64) ([]int64, error) {
	var rows []struct {
		HostID int64 `json:"host_id"`
	}
	qv := fmt.Sprintf(`SELECT host_id FROM user_hosts WHERE user_id=%d UNION SELECT hgm.host_id FROM user_groups ug JOIN host_group_members hgm ON hgm.group_id=ug.group_id WHERE ug.user_id=%d ORDER BY host_id`, userID, userID)
	if err := s.query(ctx, qv, &rows); err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.HostID)
	}
	return out, nil
}

func (s *Store) NotificationSettings(ctx context.Context, userID int64) (NotificationSettings, error) {
	var xs []NotificationSettings
	err := s.query(ctx, fmt.Sprintf("SELECT user_id,pushover_app_token,pushover_user_key,notify_auto_updates,notify_manual_available,notify_manual_updates,updated_at FROM notification_settings WHERE user_id=%d", userID), &xs)
	if err != nil {
		return NotificationSettings{}, err
	}
	if len(xs) == 0 {
		return NotificationSettings{UserID: userID, NotifyAutoUpdates: true, NotifyManualAvailable: true, NotifyManualUpdates: true}, nil
	}
	return xs[0], nil
}
func (s *Store) SaveNotificationSettings(ctx context.Context, x NotificationSettings) error {
	return s.exec(ctx, fmt.Sprintf("INSERT INTO notification_settings(user_id,pushover_app_token,pushover_user_key,notify_auto_updates,notify_manual_available,notify_manual_updates,updated_at) VALUES(%d,%s,%s,%d,%d,%d,%s) ON CONFLICT(user_id) DO UPDATE SET pushover_app_token=excluded.pushover_app_token,pushover_user_key=excluded.pushover_user_key,notify_auto_updates=excluded.notify_auto_updates,notify_manual_available=excluded.notify_manual_available,notify_manual_updates=excluded.notify_manual_updates,updated_at=excluded.updated_at", x.UserID, q(x.PushoverAppToken), q(x.PushoverUserKey), b(x.NotifyAutoUpdates), b(x.NotifyManualAvailable), b(x.NotifyManualUpdates), q(now())))
}
func (s *Store) NotificationFingerprint(ctx context.Context, userID, hostID int64, container, event string) string {
	var xs []struct {
		Fingerprint string `json:"fingerprint"`
	}
	if s.query(ctx, fmt.Sprintf("SELECT fingerprint FROM notification_state WHERE user_id=%d AND host_id=%d AND container_name=%s AND event=%s", userID, hostID, q(container), q(event)), &xs) != nil || len(xs) == 0 {
		return ""
	}
	return xs[0].Fingerprint
}
func (s *Store) SaveNotificationFingerprint(ctx context.Context, userID, hostID int64, container, event, fingerprint string) error {
	return s.exec(ctx, fmt.Sprintf("INSERT INTO notification_state(user_id,host_id,container_name,event,fingerprint,updated_at) VALUES(%d,%d,%s,%s,%s,%s) ON CONFLICT(user_id,host_id,container_name,event) DO UPDATE SET fingerprint=excluded.fingerprint,updated_at=excluded.updated_at", userID, hostID, q(container), q(event), q(fingerprint), q(now())))
}
func (s *Store) NotificationTargets(ctx context.Context, hostID int64, event string) ([]NotificationTarget, error) {
	users, err := s.Users(ctx)
	if err != nil {
		return nil, err
	}
	out := []NotificationTarget{}
	for _, u := range users {
		if !bool(u.Enabled) {
			continue
		}
		allowed := u.Role == "admin"
		if !allowed {
			ids, _ := s.AllowedHostIDs(ctx, u.ID)
			for _, id := range ids {
				if id == hostID {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			continue
		}
		ns, _ := s.NotificationSettings(ctx, u.ID)
		enabled := event == "auto" && bool(ns.NotifyAutoUpdates) || event == "manual" && bool(ns.NotifyManualAvailable) || event == "manual_update" && bool(ns.NotifyManualUpdates)
		if enabled && strings.TrimSpace(ns.PushoverAppToken) != "" && strings.TrimSpace(ns.PushoverUserKey) != "" {
			out = append(out, NotificationTarget{UserID: u.ID, Username: u.Username, Role: u.Role, PushoverAppToken: ns.PushoverAppToken, PushoverUserKey: ns.PushoverUserKey})
		}
	}
	// The environment-backed owner uses reserved user_id 0 and has global host access.
	ns, _ := s.NotificationSettings(ctx, 0)
	ownerEnabled := event == "auto" && bool(ns.NotifyAutoUpdates) || event == "manual" && bool(ns.NotifyManualAvailable) || event == "manual_update" && bool(ns.NotifyManualUpdates)
	if ownerEnabled && strings.TrimSpace(ns.PushoverAppToken) != "" && strings.TrimSpace(ns.PushoverUserKey) != "" {
		out = append(out, NotificationTarget{UserID: 0, Username: "admin", Role: "owner", PushoverAppToken: ns.PushoverAppToken, PushoverUserKey: ns.PushoverUserKey})
	}
	return out, nil
}

func (s *Store) AddNotificationDelivery(ctx context.Context, x NotificationDelivery) error {
	if strings.TrimSpace(x.TS) == "" {
		x.TS = now()
	}
	return s.exec(ctx, fmt.Sprintf("INSERT INTO notification_deliveries(ts,user_id,username,host_id,container_name,event,title,status,error) VALUES(%s,%d,%s,%d,%s,%s,%s,%s,%s); DELETE FROM notification_deliveries WHERE id NOT IN (SELECT id FROM notification_deliveries ORDER BY id DESC LIMIT 5000);", q(x.TS), x.UserID, q(x.Username), x.HostID, q(x.ContainerName), q(x.Event), q(x.Title), q(x.Status), q(x.Error)))
}

func (s *Store) NotificationDeliveries(ctx context.Context, userID *int64, limit int) ([]NotificationDelivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	where := ""
	if userID != nil {
		where = fmt.Sprintf(" WHERE user_id=%d", *userID)
	}
	xs := []NotificationDelivery{}
	err := s.query(ctx, fmt.Sprintf("SELECT id,ts,user_id,username,host_id,container_name,event,title,status,error FROM notification_deliveries%s ORDER BY id DESC LIMIT %d", where, limit), &xs)
	return xs, err
}
