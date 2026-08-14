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
	HostID                 int64  `json:"host_id"`
	ContainerName          string `json:"container_name"`
	Mode                   string `json:"mode"`
	CheckIntervalMinutes   int    `json:"check_interval_minutes"`
	ReleaseRepo            string `json:"release_repo"`
	AllowPreflightWarnings Bool   `json:"allow_preflight_warnings"`
	LastCheckedAt          string `json:"last_checked_at,omitempty"`
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
	UpdateKind      string `json:"update_kind"`
	SecurityUpdate  Bool   `json:"security_update"`
	ReleaseRepo     string `json:"release_repo"`
	PublishedAt     string `json:"published_at"`
	CheckedAt       string `json:"checked_at"`
	Error           string `json:"error"`
}

type Automation struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Cron              string `json:"cron"`
	TargetType        string `json:"target_type"`
	TargetID          int64  `json:"target_id"`
	Kind              string `json:"kind"`
	CleanupImages     Bool   `json:"cleanup_images"`
	CleanupNetworks   Bool   `json:"cleanup_networks"`
	CleanupBuildCache Bool   `json:"cleanup_build_cache"`
	CleanupVolumes    Bool   `json:"cleanup_volumes"`
	Enabled           Bool   `json:"enabled"`
	LastRunAt         string `json:"last_run_at"`
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
	ID                  int64  `json:"id"`
	TS                  string `json:"ts"`
	HostID              int64  `json:"host_id"`
	ContainerName       string `json:"container_name"`
	Action              string `json:"action"`
	Trigger             string `json:"trigger"`
	Actor               string `json:"actor"`
	Status              string `json:"status"`
	FromVersion         string `json:"from_version"`
	ToVersion           string `json:"to_version"`
	FromImageRef        string `json:"from_image_ref"`
	ToImageRef          string `json:"to_image_ref"`
	FromDigest          string `json:"from_digest"`
	ToDigest            string `json:"to_digest"`
	SnapshotID          string `json:"snapshot_id"`
	RestorePointID      int64  `json:"restore_point_id"`
	DurationMS          int64  `json:"duration_ms"`
	Error               string `json:"error"`
	DependencyCount     int    `json:"dependency_count"`
	DependencyStatus    string `json:"dependency_status"`
	DependencyDetails   string `json:"dependency_details"`
	PreflightStatus     string `json:"preflight_status"`
	PreflightDetails    string `json:"preflight_details"`
	VerificationStatus  string `json:"verification_status"`
	VerificationDetails string `json:"verification_details"`
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
	IntegrityStatus     string `json:"integrity_status"`
	IntegrityCheckedAt  string `json:"integrity_checked_at"`
	IntegrityDetails    string `json:"integrity_details"`
	DataManifestJSON    string `json:"-"`
	DataBytes           int64  `json:"data_bytes"`
}

type DataProtectionProfile struct {
	HostID     int64  `json:"host_id"`
	ScopeType  string `json:"scope_type"`
	ScopeKey   string `json:"scope_key"`
	Enabled    Bool   `json:"enabled"`
	MountsJSON string `json:"mounts_json"`
	UpdatedAt  string `json:"updated_at"`
}

type DataMountCache struct {
	HostID        int64  `json:"host_id"`
	MountKey      string `json:"mount_key"`
	StorageClass  string `json:"storage_class"`
	FSType        string `json:"fs_type"`
	CheckedAt     string `json:"checked_at"`
	Error         string `json:"error"`
	SizeBytes     int64  `json:"size_bytes"`
	SizeCheckedAt string `json:"size_checked_at"`
	SizeError     string `json:"size_error"`
}

type HostStorageCache struct {
	HostID            int64  `json:"host_id"`
	HostTotalBytes    int64  `json:"host_total_bytes"`
	HostFreeBytes     int64  `json:"host_free_bytes"`
	RestoreTotalBytes int64  `json:"restore_total_bytes"`
	RestoreFreeBytes  int64  `json:"restore_free_bytes"`
	CheckedAt         string `json:"checked_at"`
	Error             string `json:"error"`
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

type VerificationProfile struct {
	HostID               int64  `json:"host_id"`
	ScopeType            string `json:"scope_type"`
	ScopeKey             string `json:"scope_key"`
	Enabled              Bool   `json:"enabled"`
	StartDelaySeconds    int    `json:"start_delay_seconds"`
	RetryCount           int    `json:"retry_count"`
	RetryIntervalSeconds int    `json:"retry_interval_seconds"`
	ChecksJSON           string `json:"checks_json"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type VerificationState struct {
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	ScopeType     string `json:"scope_type,omitempty"`
	ScopeKey      string `json:"scope_key,omitempty"`
	Status        string `json:"status"`
	DetailsJSON   string `json:"details_json"`
	CheckedAt     string `json:"checked_at"`
	Error         string `json:"error"`
}

type VerificationScopeState struct {
	HostID      int64  `json:"host_id"`
	ScopeType   string `json:"scope_type"`
	ScopeKey    string `json:"scope_key"`
	Status      string `json:"status"`
	DetailsJSON string `json:"details_json"`
	CheckedAt   string `json:"checked_at"`
	Error       string `json:"error"`
}

type UpdateChain struct {
	ID                     int64  `json:"id"`
	Name                   string `json:"name"`
	HostID                 int64  `json:"host_id"`
	AutomationID           int64  `json:"automation_id"`
	ScopeType              string `json:"scope_type"`
	ScopeKey               string `json:"scope_key"`
	PolicyMode             string `json:"policy_mode"`
	AllowPreflightWarnings Bool   `json:"allow_preflight_warnings"`
	StopOnFailure          Bool   `json:"stop_on_failure"`
	RollbackCompleted      Bool   `json:"rollback_completed"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	LastRunAt              string `json:"last_run_at"`
	LastStatus             string `json:"last_status"`
}

type UpdateChainStep struct {
	ID            int64  `json:"id"`
	ChainID       int64  `json:"chain_id"`
	Position      int    `json:"position"`
	ContainerName string `json:"container_name"`
	CurrentAction string `json:"current_action"`
	WaitSeconds   int    `json:"wait_seconds"`
}

type UpdateChainRun struct {
	ID             int64  `json:"id"`
	ChainID        int64  `json:"chain_id"`
	ChainName      string `json:"chain_name"`
	HostID         int64  `json:"host_id"`
	JobID          int64  `json:"job_id"`
	Trigger        string `json:"trigger"`
	Actor          string `json:"actor"`
	Status         string `json:"status"`
	RecoveryAction string `json:"recovery_action"`
	RecoveredAt    string `json:"recovered_at"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at"`
	Error          string `json:"error"`
}

type UpdateChainRunStep struct {
	ID            int64  `json:"id"`
	RunID         int64  `json:"run_id"`
	Position      int    `json:"position"`
	ContainerName string `json:"container_name"`
	Status        string `json:"status"`
	JobID         int64  `json:"job_id"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	Error         string `json:"error"`
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
CREATE TABLE IF NOT EXISTS policies (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'manual', check_interval_minutes INTEGER NOT NULL DEFAULT 30, release_repo TEXT NOT NULL DEFAULT '', allow_preflight_warnings INTEGER NOT NULL DEFAULT 0, last_checked_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS container_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, image TEXT NOT NULL DEFAULT '', image_id TEXT NOT NULL DEFAULT '', current_digest TEXT NOT NULL DEFAULT '', latest_digest TEXT NOT NULL DEFAULT '', update_available INTEGER NOT NULL DEFAULT 0, first_detected_at TEXT NOT NULL DEFAULT '', snoozed_digest TEXT NOT NULL DEFAULT '', snoozed_at TEXT NOT NULL DEFAULT '', last_checked_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, trigger TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '', summary_json TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS job_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, ts TEXT NOT NULL, level TEXT NOT NULL, source TEXT NOT NULL, message TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron TEXT NOT NULL, action TEXT NOT NULL, host_id INTEGER NOT NULL, containers TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0, container_name TEXT NOT NULL DEFAULT '', details TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS version_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, installed_version TEXT NOT NULL DEFAULT '', installed_source TEXT NOT NULL DEFAULT '', latest_version TEXT NOT NULL DEFAULT '', latest_source TEXT NOT NULL DEFAULT '', update_kind TEXT NOT NULL DEFAULT '', security_update INTEGER NOT NULL DEFAULT 0, release_repo TEXT NOT NULL DEFAULT '', published_at TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS automations (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, cron TEXT NOT NULL, target_type TEXT NOT NULL DEFAULT 'host', target_id INTEGER NOT NULL DEFAULT 0, kind TEXT NOT NULL DEFAULT 'policy', cleanup_images INTEGER NOT NULL DEFAULT 0, cleanup_networks INTEGER NOT NULL DEFAULT 0, cleanup_build_cache INTEGER NOT NULL DEFAULT 0, cleanup_volumes INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT NOT NULL DEFAULT '');
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
CREATE TABLE IF NOT EXISTS data_protection_profiles (host_id INTEGER NOT NULL, scope_type TEXT NOT NULL, scope_key TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0, mounts_json TEXT NOT NULL DEFAULT '[]', updated_at TEXT NOT NULL, PRIMARY KEY(host_id,scope_type,scope_key));
CREATE TABLE IF NOT EXISTS data_mount_cache (host_id INTEGER NOT NULL, mount_key TEXT NOT NULL, storage_class TEXT NOT NULL DEFAULT 'unknown', fs_type TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0, size_checked_at TEXT NOT NULL DEFAULT '', size_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,mount_key));
CREATE TABLE IF NOT EXISTS host_storage_cache (host_id INTEGER PRIMARY KEY, host_total_bytes INTEGER NOT NULL DEFAULT 0, host_free_bytes INTEGER NOT NULL DEFAULT 0, restore_total_bytes INTEGER NOT NULL DEFAULT 0, restore_free_bytes INTEGER NOT NULL DEFAULT 0, checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS config_drift_cache (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'not_checked', details_json TEXT NOT NULL DEFAULT '[]', baseline_at TEXT NOT NULL DEFAULT '', baseline_json TEXT NOT NULL DEFAULT '', baseline_source TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS registry_credentials (id INTEGER PRIMARY KEY AUTOINCREMENT, registry TEXT NOT NULL UNIQUE COLLATE NOCASE, username TEXT NOT NULL DEFAULT '', secret_enc TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS verification_profiles (host_id INTEGER NOT NULL, scope_type TEXT NOT NULL, scope_key TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, start_delay_seconds INTEGER NOT NULL DEFAULT 0, retry_count INTEGER NOT NULL DEFAULT 2, retry_interval_seconds INTEGER NOT NULL DEFAULT 3, checks_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(host_id,scope_type,scope_key));
CREATE TABLE IF NOT EXISTS verification_state (host_id INTEGER NOT NULL, container_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'not_configured', details_json TEXT NOT NULL DEFAULT '[]', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,container_name));
CREATE TABLE IF NOT EXISTS verification_scope_state (host_id INTEGER NOT NULL, scope_type TEXT NOT NULL, scope_key TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'not_configured', details_json TEXT NOT NULL DEFAULT '[]', checked_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', PRIMARY KEY(host_id,scope_type,scope_key));
CREATE TABLE IF NOT EXISTS update_chains (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, host_id INTEGER NOT NULL, automation_id INTEGER NOT NULL DEFAULT 0, scope_type TEXT NOT NULL DEFAULT 'custom', scope_key TEXT NOT NULL DEFAULT '', policy_mode TEXT NOT NULL DEFAULT 'inherit', allow_preflight_warnings INTEGER NOT NULL DEFAULT 0, stop_on_failure INTEGER NOT NULL DEFAULT 1, rollback_completed INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, last_run_at TEXT NOT NULL DEFAULT '', last_status TEXT NOT NULL DEFAULT 'never');
CREATE TABLE IF NOT EXISTS update_chain_steps (id INTEGER PRIMARY KEY AUTOINCREMENT, chain_id INTEGER NOT NULL, position INTEGER NOT NULL, container_name TEXT NOT NULL, current_action TEXT NOT NULL DEFAULT 'skip', wait_seconds INTEGER NOT NULL DEFAULT 0, UNIQUE(chain_id,position));
CREATE TABLE IF NOT EXISTS update_chain_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, chain_id INTEGER NOT NULL, chain_name TEXT NOT NULL, host_id INTEGER NOT NULL, job_id INTEGER NOT NULL DEFAULT 0, trigger TEXT NOT NULL, actor TEXT NOT NULL, status TEXT NOT NULL, recovery_action TEXT NOT NULL DEFAULT '', recovered_at TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS update_chain_run_steps (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, position INTEGER NOT NULL, container_name TEXT NOT NULL, status TEXT NOT NULL, job_id INTEGER NOT NULL DEFAULT 0, started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS update_transactions (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL UNIQUE, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'queued', status TEXT NOT NULL DEFAULT 'running', snapshot_id TEXT NOT NULL DEFAULT '', restore_point_id INTEGER NOT NULL DEFAULT 0, target_digest TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, updated_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', recovery_action TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS update_transaction_events (id INTEGER PRIMARY KEY AUTOINCREMENT, transaction_id INTEGER NOT NULL, ts TEXT NOT NULL, from_state TEXT NOT NULL DEFAULT '', to_state TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'running', message TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS operation_leases (resource_key TEXT PRIMARY KEY, host_id INTEGER NOT NULL, container_name TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL, operation_type TEXT NOT NULL, transaction_id INTEGER NOT NULL DEFAULT 0, job_id INTEGER NOT NULL DEFAULT 0, acquired_at TEXT NOT NULL, heartbeat_at TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS verification_history (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, host_id INTEGER NOT NULL, container_name TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', job_id INTEGER NOT NULL DEFAULT 0, transaction_id INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, scope_type TEXT NOT NULL DEFAULT '', scope_key TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL DEFAULT 0, details_json TEXT NOT NULL DEFAULT '[]', error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS recovery_gc_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, status TEXT NOT NULL, restore_points_checked INTEGER NOT NULL DEFAULT 0, degraded INTEGER NOT NULL DEFAULT 0, expired INTEGER NOT NULL DEFAULT 0, images_removed INTEGER NOT NULL DEFAULT 0, snapshots_removed INTEGER NOT NULL DEFAULT 0, helpers_removed INTEGER NOT NULL DEFAULT 0, unusable_removed INTEGER NOT NULL DEFAULT 0, errors_json TEXT NOT NULL DEFAULT '[]');
CREATE INDEX IF NOT EXISTS idx_update_transactions_status ON update_transactions(status,id); CREATE INDEX IF NOT EXISTS idx_update_transactions_container ON update_transactions(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_update_transaction_events_tx ON update_transaction_events(transaction_id,id); CREATE INDEX IF NOT EXISTS idx_operation_leases_expiry ON operation_leases(expires_at); CREATE INDEX IF NOT EXISTS idx_verification_history_container ON verification_history(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_verification_state_container ON verification_state(host_id,container_name); CREATE INDEX IF NOT EXISTS idx_verification_scope_state ON verification_scope_state(host_id,scope_type,scope_key); CREATE INDEX IF NOT EXISTS idx_update_chains_host ON update_chains(host_id,id); CREATE INDEX IF NOT EXISTS idx_update_chain_runs_chain ON update_chain_runs(chain_id,id DESC); CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(id DESC); CREATE INDEX IF NOT EXISTS idx_update_history_id ON update_history(id DESC); CREATE INDEX IF NOT EXISTS idx_restore_points_container ON restore_points(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_restore_points_snapshot ON restore_points(host_id,snapshot_id); CREATE INDEX IF NOT EXISTS idx_update_history_container ON update_history(host_id,container_name,id DESC); CREATE INDEX IF NOT EXISTS idx_notification_deliveries_id ON notification_deliveries(id DESC); CREATE INDEX IF NOT EXISTS idx_job_logs_job ON job_logs(job_id,id); CREATE INDEX IF NOT EXISTS idx_group_members_host ON host_group_members(host_id); CREATE INDEX IF NOT EXISTS idx_user_hosts_host ON user_hosts(host_id);`
	if err := s.exec(ctx, schema); err != nil {
		return err
	}
	// Backfill shared stack state from the newest scope-aware history entry.
	// This preserves existing successful stack verifications when upgrading from
	// the older per-container state model.
	if err := s.exec(ctx, `INSERT OR IGNORE INTO verification_scope_state(host_id,scope_type,scope_key,status,details_json,checked_at,error)
SELECT h.host_id,h.scope_type,h.scope_key,h.status,h.details_json,h.ts,h.error
FROM verification_history h
JOIN (SELECT host_id,scope_type,scope_key,MAX(id) AS id FROM verification_history WHERE scope_type='stack' AND scope_key<>'' GROUP BY host_id,scope_type,scope_key) latest ON latest.id=h.id;`); err != nil {
		return err
	}
	// Safe migrations for databases created by older Vibewatch releases.
	if err := s.exec(ctx, "ALTER TABLE version_cache ADD COLUMN latest_source TEXT NOT NULL DEFAULT '';"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}

	for _, stmt := range []string{
		"ALTER TABLE version_cache ADD COLUMN update_kind TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE version_cache ADD COLUMN security_update INTEGER NOT NULL DEFAULT 0;",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
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
	for _, stmt := range []string{
		"ALTER TABLE update_chains ADD COLUMN scope_type TEXT NOT NULL DEFAULT 'custom';",
		"ALTER TABLE update_chains ADD COLUMN scope_key TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_chains ADD COLUMN policy_mode TEXT NOT NULL DEFAULT 'inherit';",
		"ALTER TABLE update_chains ADD COLUMN allow_preflight_warnings INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE policies ADD COLUMN allow_preflight_warnings INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE update_chain_steps ADD COLUMN current_action TEXT NOT NULL DEFAULT 'skip';",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	for _, stmt := range []string{
		"ALTER TABLE update_history ADD COLUMN preflight_status TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_history ADD COLUMN preflight_details TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_history ADD COLUMN verification_status TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_history ADD COLUMN verification_details TEXT NOT NULL DEFAULT '';",
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
	for _, stmt := range []string{
		"ALTER TABLE restore_points ADD COLUMN integrity_status TEXT NOT NULL DEFAULT 'not_checked';",
		"ALTER TABLE restore_points ADD COLUMN integrity_checked_at TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE restore_points ADD COLUMN integrity_details TEXT NOT NULL DEFAULT '';",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	for _, stmt := range []string{
		"ALTER TABLE automations ADD COLUMN kind TEXT NOT NULL DEFAULT 'policy';",
		"ALTER TABLE automations ADD COLUMN cleanup_images INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE automations ADD COLUMN cleanup_networks INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE automations ADD COLUMN cleanup_build_cache INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE automations ADD COLUMN cleanup_volumes INTEGER NOT NULL DEFAULT 0;",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	for _, stmt := range []string{
		"ALTER TABLE restore_points ADD COLUMN data_manifest_json TEXT NOT NULL DEFAULT '{}';",
		"ALTER TABLE restore_points ADD COLUMN data_bytes INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE data_mount_cache ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE data_mount_cache ADD COLUMN size_checked_at TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE data_mount_cache ADD COLUMN size_error TEXT NOT NULL DEFAULT '';",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	for _, stmt := range []string{
		"ALTER TABLE update_chain_runs ADD COLUMN job_id INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE update_chain_runs ADD COLUMN recovery_action TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE update_chain_runs ADD COLUMN recovered_at TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE recovery_gc_runs ADD COLUMN helpers_removed INTEGER NOT NULL DEFAULT 0;",
		"ALTER TABLE recovery_gc_runs ADD COLUMN unusable_removed INTEGER NOT NULL DEFAULT 0;",
	} {
		if err := s.exec(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
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
	"verification_profiles", "verification_state", "verification_scope_state", "verification_history", "update_chains", "update_chain_steps", "update_chain_runs", "update_chain_run_steps",
	"update_transactions", "update_transaction_events", "operation_leases", "recovery_gc_runs", "data_protection_profiles", "data_mount_cache", "host_storage_cache",
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

func (s *Store) tableColumns(ctx context.Context, table string) ([]string, error) {
	var rows []struct {
		Name string `json:"name"`
	}
	if err := s.query(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, q(table)), &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Name) != "" {
			out = append(out, row.Name)
		}
	}
	return out, nil
}

func quoteIdent(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
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
		srcCols, colErr := s.tableColumns(ctx, table)
		if colErr != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("read source columns for %s: %w", table, colErr)
		}
		dstCols, colErr := fresh.tableColumns(ctx, table)
		if colErr != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("read recovery columns for %s: %w", table, colErr)
		}
		dstSet := map[string]bool{}
		for _, col := range dstCols {
			dstSet[col] = true
		}
		common := make([]string, 0, len(srcCols))
		for _, col := range srcCols {
			if dstSet[col] {
				common = append(common, col)
			}
		}
		if len(common) == 0 {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("no common columns available while recovering %s", table)
		}
		quoted := make([]string, 0, len(common))
		for _, col := range common {
			quoted = append(quoted, quoteIdent(col))
		}
		cols := strings.Join(quoted, ",")
		script.WriteString("INSERT INTO " + quoteIdent(table) + " (" + cols + ") SELECT " + cols + " FROM damaged." + quoteIdent(table) + ";\n")
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
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM policies WHERE host_id=%d; DELETE FROM container_cache WHERE host_id=%d; DELETE FROM version_cache WHERE host_id=%d; DELETE FROM schedules WHERE host_id=%d; DELETE FROM host_group_members WHERE host_id=%d; DELETE FROM user_hosts WHERE host_id=%d; DELETE FROM notification_state WHERE host_id=%d; DELETE FROM config_drift_cache WHERE host_id=%d; DELETE FROM verification_profiles WHERE host_id=%d; DELETE FROM verification_state WHERE host_id=%d; DELETE FROM verification_scope_state WHERE host_id=%d; DELETE FROM verification_history WHERE host_id=%d; DELETE FROM operation_leases WHERE host_id=%d; DELETE FROM update_transaction_events WHERE transaction_id IN (SELECT id FROM update_transactions WHERE host_id=%d); DELETE FROM update_transactions WHERE host_id=%d; DELETE FROM update_chain_steps WHERE chain_id IN (SELECT id FROM update_chains WHERE host_id=%d); DELETE FROM update_chains WHERE host_id=%d; DELETE FROM automations WHERE target_type='host' AND target_id=%d; DELETE FROM restore_points WHERE host_id=%d; DELETE FROM data_protection_profiles WHERE host_id=%d; DELETE FROM data_mount_cache WHERE host_id=%d; DELETE FROM host_storage_cache WHERE host_id=%d; DELETE FROM hosts WHERE id=%d;`, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id))
}

func (s *Store) Policy(ctx context.Context, hostID int64, name string) (Policy, error) {
	var x []Policy
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,mode,check_interval_minutes,release_repo,allow_preflight_warnings,last_checked_at FROM policies WHERE host_id=%d AND container_name=%s`, hostID, q(name)), &x)
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
	err := s.query(ctx, `SELECT host_id,container_name,mode,check_interval_minutes,release_repo,allow_preflight_warnings,last_checked_at FROM policies ORDER BY host_id,container_name`, &x)
	return x, err
}
func (s *Store) SavePolicy(ctx context.Context, p Policy) error {
	if p.CheckIntervalMinutes < 1 {
		p.CheckIntervalMinutes = 30
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO policies(host_id,container_name,mode,check_interval_minutes,release_repo,allow_preflight_warnings,last_checked_at) VALUES(%d,%s,%s,%d,%s,%d,COALESCE((SELECT last_checked_at FROM policies WHERE host_id=%d AND container_name=%s),'')) ON CONFLICT(host_id,container_name) DO UPDATE SET mode=excluded.mode,check_interval_minutes=excluded.check_interval_minutes,release_repo=excluded.release_repo,allow_preflight_warnings=excluded.allow_preflight_warnings;`, p.HostID, q(p.ContainerName), q(p.Mode), p.CheckIntervalMinutes, q(p.ReleaseRepo), b(p.AllowPreflightWarnings), p.HostID, q(p.ContainerName)))
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
	return s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status='running',started_at=%s WHERE id=%d AND status!='cancelled';`, q(now()), id))
}

// ClaimQueuedJob atomically transitions a queued job to running. This is used
// by asynchronous workers so a user cancellation that wins the race cannot be
// overwritten by a later StartJob call from a request already sitting in an
// in-memory queue.
func (s *Store) ClaimQueuedJob(ctx context.Context, id int64) (bool, error) {
	n, err := s.scalarInt(ctx, fmt.Sprintf(`UPDATE jobs SET status='running',started_at=%s WHERE id=%d AND status='queued'; SELECT changes();`, q(now()), id))
	return n > 0, err
}

// CancelQueuedJob only succeeds while the operation has not started. Running
// jobs are deliberately not interrupted because Docker update/rollback work is
// transactional and killing it mid-flight could leave the service in an
// ambiguous state.
func (s *Store) CancelQueuedJob(ctx context.Context, id int64, reason string) (bool, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled before execution"
	}
	n, err := s.scalarInt(ctx, fmt.Sprintf(`UPDATE jobs SET status='cancelled',finished_at=%s,summary_json=%s,error='' WHERE id=%d AND status='queued'; SELECT changes();`, q(now()), q(reason), id))
	return n > 0, err
}
func (s *Store) FinishJob(ctx context.Context, id int64, status, summary, errMsg string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status=%s,finished_at=%s,summary_json=%s,error=%s WHERE id=%d;`, q(status), q(now()), q(summary), q(errMsg), id))
}
func (s *Store) Jobs(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 5000 {
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
func (s *Store) FailActiveJobs(ctx context.Context, reason string) (int, error) {
	n, err := s.scalarInt(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running');`)
	if err != nil || n == 0 {
		return int(n), err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "operation interrupted"
	}
	err = s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status='failed',finished_at=%s,error=CASE WHEN error='' THEN %s ELSE error END WHERE status IN ('queued','running');`, q(now()), q(reason)))
	return int(n), err
}

func (s *Store) FailActiveUpdateChainRuns(ctx context.Context, reason string) (int, error) {
	n, err := s.scalarInt(ctx, `SELECT COUNT(*) FROM update_chain_runs WHERE status IN ('queued','running');`)
	if err != nil || n == 0 {
		return int(n), err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "update chain interrupted"
	}
	stamp := now()
	if err := s.exec(ctx, fmt.Sprintf(`UPDATE update_chains SET last_status='failed' WHERE id IN (SELECT chain_id FROM update_chain_runs WHERE status IN ('queued','running'));`)); err != nil {
		return int(n), err
	}
	if err := s.exec(ctx, fmt.Sprintf(`UPDATE update_chain_run_steps SET status='failed',finished_at=CASE WHEN finished_at='' THEN %s ELSE finished_at END,error=CASE WHEN error='' THEN %s ELSE error END WHERE run_id IN (SELECT id FROM update_chain_runs WHERE status IN ('queued','running')) AND status NOT IN ('success','failed','skipped_current');`, q(stamp), q(reason))); err != nil {
		return int(n), err
	}
	if err := s.exec(ctx, fmt.Sprintf(`UPDATE update_chain_runs SET status='failed',finished_at=%s,error=CASE WHEN error='' THEN %s ELSE error END WHERE status IN ('queued','running');`, q(stamp), q(reason))); err != nil {
		return int(n), err
	}
	return int(n), nil
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
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_history(ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details,preflight_status,preflight_details,verification_status,verification_details) VALUES(%s,%d,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%s,%d,%s,%s,%s,%s,%s,%s); SELECT last_insert_rowid();`,
		q(now()), x.HostID, q(x.ContainerName), q(x.Action), q(x.Trigger), q(x.Actor), q(x.Status), q(x.FromVersion), q(x.ToVersion), q(x.FromImageRef), q(x.ToImageRef), q(x.FromDigest), q(x.ToDigest), q(x.SnapshotID), x.RestorePointID, x.DurationMS, q(x.Error), x.DependencyCount, q(x.DependencyStatus), q(x.DependencyDetails), q(x.PreflightStatus), q(x.PreflightDetails), q(x.VerificationStatus), q(x.VerificationDetails)))
	if err == nil {
		_ = s.exec(ctx, `DELETE FROM update_history WHERE id NOT IN (SELECT id FROM update_history ORDER BY id DESC LIMIT 5000);`)
	}
	return id, err
}
func (s *Store) UpdateHistory(ctx context.Context, limit int, hostID int64, container string) ([]UpdateHistory, error) {
	if limit < 1 || limit > 5000 {
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
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details,preflight_status,preflight_details,verification_status,verification_details FROM update_history WHERE %s ORDER BY id DESC LIMIT %d`, strings.Join(where, " AND "), limit), &x)
	return x, err
}
func (s *Store) UpdateHistoryEntry(ctx context.Context, id int64) (UpdateHistory, error) {
	var x []UpdateHistory
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,container_name,action,trigger,actor,status,from_version,to_version,from_image_ref,to_image_ref,from_digest,to_digest,snapshot_id,restore_point_id,duration_ms,error,dependency_count,dependency_status,dependency_details,preflight_status,preflight_details,verification_status,verification_details FROM update_history WHERE id=%d LIMIT 1`, id), &x)
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
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO restore_points(created_at,updated_at,host_id,container_name,snapshot_id,reason,trigger,status,image_ref,image_id,original_image_ref,original_image_id,target_digest,from_version,unit_kind,unit_key,stack_type,writable_layer,config_protected,volume_data_protected,volume_count,bind_count,restore_count,last_restored_at,last_error,dependency_count,dependencies_json,data_manifest_json,data_bytes) VALUES(%s,%s,%d,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%d,%d,%d,%d,%s,%s,%d,%s,%s,%d); SELECT last_insert_rowid();`,
		q(ts), q(ts), x.HostID, q(x.ContainerName), q(x.SnapshotID), q(x.Reason), q(x.Trigger), q(x.Status), q(x.ImageRef), q(x.ImageID), q(x.OriginalImageRef), q(x.OriginalImageID), q(x.TargetDigest), q(x.FromVersion), q(x.UnitKind), q(x.UnitKey), q(x.StackType), b(x.WritableLayer), b(x.ConfigProtected), b(x.VolumeDataProtected), x.VolumeCount, x.BindCount, x.RestoreCount, q(x.LastRestoredAt), q(x.LastError), x.DependencyCount, q(x.DependenciesJSON), q(x.DataManifestJSON), x.DataBytes))
	if err == nil {
		// Only prune already-expired metadata here. Ready/degraded Restore Points may
		// own host-local images and data archives that must be expired through the
		// recovery retention pipeline before their database row disappears.
		_ = s.exec(ctx, `DELETE FROM restore_points WHERE status='expired' AND id NOT IN (SELECT id FROM restore_points ORDER BY id DESC LIMIT 5000);`)
	}
	return id, err
}

func restorePointSelect() string {
	return `id,created_at,updated_at,host_id,container_name,snapshot_id,reason,trigger,status,image_ref,image_id,original_image_ref,original_image_id,target_digest,from_version,unit_kind,unit_key,stack_type,writable_layer,config_protected,volume_data_protected,volume_count,bind_count,restore_count,last_restored_at,last_error,dependency_count,dependencies_json,integrity_status,integrity_checked_at,integrity_details,data_manifest_json,data_bytes`
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

func (s *Store) DeleteRestorePoint(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM restore_points WHERE id=%d`, id))
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

func (s *Store) DataProtectionProfile(ctx context.Context, hostID int64, scopeType, scopeKey string) (DataProtectionProfile, error) {
	var xs []DataProtectionProfile
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,scope_type,scope_key,enabled,mounts_json,updated_at FROM data_protection_profiles WHERE host_id=%d AND scope_type=%s AND scope_key=%s LIMIT 1`, hostID, q(scopeType), q(scopeKey)), &xs)
	if err != nil {
		return DataProtectionProfile{}, err
	}
	if len(xs) == 0 {
		return DataProtectionProfile{HostID: hostID, ScopeType: scopeType, ScopeKey: scopeKey, MountsJSON: "[]"}, nil
	}
	return xs[0], nil
}
func (s *Store) DataProtectionProfiles(ctx context.Context, hostID int64) ([]DataProtectionProfile, error) {
	var xs []DataProtectionProfile
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,scope_type,scope_key,enabled,mounts_json,updated_at FROM data_protection_profiles WHERE host_id=%d ORDER BY scope_type,scope_key`, hostID), &xs)
	if xs == nil {
		xs = []DataProtectionProfile{}
	}
	return xs, err
}
func (s *Store) SaveDataProtectionProfile(ctx context.Context, x DataProtectionProfile) error {
	if strings.TrimSpace(x.MountsJSON) == "" {
		x.MountsJSON = "[]"
	}
	ts := now()
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO data_protection_profiles(host_id,scope_type,scope_key,enabled,mounts_json,updated_at) VALUES(%d,%s,%s,%d,%s,%s) ON CONFLICT(host_id,scope_type,scope_key) DO UPDATE SET enabled=excluded.enabled,mounts_json=excluded.mounts_json,updated_at=excluded.updated_at`, x.HostID, q(x.ScopeType), q(x.ScopeKey), b(x.Enabled), q(x.MountsJSON), q(ts)))
}
func (s *Store) DeleteDataProtectionProfile(ctx context.Context, hostID int64, scopeType, scopeKey string) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM data_protection_profiles WHERE host_id=%d AND scope_type=%s AND scope_key=%s`, hostID, q(scopeType), q(scopeKey)))
}
func (s *Store) DataMountCache(ctx context.Context, hostID int64, mountKey string) (DataMountCache, error) {
	var xs []DataMountCache
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,mount_key,storage_class,fs_type,checked_at,error,size_bytes,size_checked_at,size_error FROM data_mount_cache WHERE host_id=%d AND mount_key=%s LIMIT 1`, hostID, q(mountKey)), &xs)
	if err != nil {
		return DataMountCache{}, err
	}
	if len(xs) == 0 {
		return DataMountCache{HostID: hostID, MountKey: mountKey, StorageClass: "unknown"}, nil
	}
	return xs[0], nil
}
func (s *Store) SaveDataMountCache(ctx context.Context, x DataMountCache) error {
	if x.StorageClass == "" {
		x.StorageClass = "unknown"
	}
	if x.CheckedAt == "" {
		x.CheckedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO data_mount_cache(host_id,mount_key,storage_class,fs_type,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s) ON CONFLICT(host_id,mount_key) DO UPDATE SET storage_class=excluded.storage_class,fs_type=excluded.fs_type,checked_at=excluded.checked_at,error=excluded.error`, x.HostID, q(x.MountKey), q(x.StorageClass), q(x.FSType), q(x.CheckedAt), q(x.Error)))
}
func (s *Store) SaveDataMountSize(ctx context.Context, hostID int64, mountKey string, sizeBytes int64, checkedAt, sizeError string) error {
	if checkedAt == "" {
		checkedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO data_mount_cache(host_id,mount_key,storage_class,fs_type,checked_at,error,size_bytes,size_checked_at,size_error) VALUES(%d,%s,'unknown','','','',%d,%s,%s) ON CONFLICT(host_id,mount_key) DO UPDATE SET size_bytes=excluded.size_bytes,size_checked_at=excluded.size_checked_at,size_error=excluded.size_error`, hostID, q(mountKey), sizeBytes, q(checkedAt), q(sizeError)))
}
func (s *Store) InvalidateHostStorageCache(ctx context.Context, hostID int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM host_storage_cache WHERE host_id=%d`, hostID))
}
func (s *Store) HostStorageCache(ctx context.Context, hostID int64) (HostStorageCache, error) {
	var xs []HostStorageCache
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,host_total_bytes,host_free_bytes,restore_total_bytes,restore_free_bytes,checked_at,error FROM host_storage_cache WHERE host_id=%d LIMIT 1`, hostID), &xs)
	if err != nil {
		return HostStorageCache{}, err
	}
	if len(xs) == 0 {
		return HostStorageCache{HostID: hostID}, nil
	}
	return xs[0], nil
}
func (s *Store) SaveHostStorageCache(ctx context.Context, x HostStorageCache) error {
	if x.CheckedAt == "" {
		x.CheckedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO host_storage_cache(host_id,host_total_bytes,host_free_bytes,restore_total_bytes,restore_free_bytes,checked_at,error) VALUES(%d,%d,%d,%d,%d,%s,%s) ON CONFLICT(host_id) DO UPDATE SET host_total_bytes=excluded.host_total_bytes,host_free_bytes=excluded.host_free_bytes,restore_total_bytes=excluded.restore_total_bytes,restore_free_bytes=excluded.restore_free_bytes,checked_at=excluded.checked_at,error=excluded.error`, x.HostID, x.HostTotalBytes, x.HostFreeBytes, x.RestoreTotalBytes, x.RestoreFreeBytes, q(x.CheckedAt), q(x.Error)))
}
func (s *Store) VerificationProfile(ctx context.Context, hostID int64, scopeType, scopeKey string) (VerificationProfile, error) {
	var x []VerificationProfile
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,scope_type,scope_key,enabled,start_delay_seconds,retry_count,retry_interval_seconds,checks_json,created_at,updated_at FROM verification_profiles WHERE host_id=%d AND scope_type=%s AND scope_key=%s LIMIT 1`, hostID, q(scopeType), q(scopeKey)), &x)
	if err != nil {
		return VerificationProfile{}, err
	}
	if len(x) == 0 {
		return VerificationProfile{}, fmt.Errorf("verification profile not found")
	}
	return x[0], nil
}
func (s *Store) VerificationProfiles(ctx context.Context, hostID int64) ([]VerificationProfile, error) {
	var x []VerificationProfile
	where := "1=1"
	if hostID > 0 {
		where = fmt.Sprintf("host_id=%d", hostID)
	}
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,scope_type,scope_key,enabled,start_delay_seconds,retry_count,retry_interval_seconds,checks_json,created_at,updated_at FROM verification_profiles WHERE %s ORDER BY host_id,scope_type,scope_key`, where), &x)
	return x, err
}
func (s *Store) SaveVerificationProfile(ctx context.Context, x VerificationProfile) error {
	if strings.TrimSpace(x.ChecksJSON) == "" {
		x.ChecksJSON = "[]"
	}
	ts := now()
	if x.CreatedAt == "" {
		x.CreatedAt = ts
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO verification_profiles(host_id,scope_type,scope_key,enabled,start_delay_seconds,retry_count,retry_interval_seconds,checks_json,created_at,updated_at) VALUES(%d,%s,%s,%d,%d,%d,%d,%s,%s,%s) ON CONFLICT(host_id,scope_type,scope_key) DO UPDATE SET updated_at=CASE WHEN verification_profiles.enabled=excluded.enabled AND verification_profiles.start_delay_seconds=excluded.start_delay_seconds AND verification_profiles.retry_count=excluded.retry_count AND verification_profiles.retry_interval_seconds=excluded.retry_interval_seconds AND verification_profiles.checks_json=excluded.checks_json THEN verification_profiles.updated_at ELSE excluded.updated_at END,enabled=excluded.enabled,start_delay_seconds=excluded.start_delay_seconds,retry_count=excluded.retry_count,retry_interval_seconds=excluded.retry_interval_seconds,checks_json=excluded.checks_json`, x.HostID, q(x.ScopeType), q(x.ScopeKey), b(x.Enabled), x.StartDelaySeconds, x.RetryCount, x.RetryIntervalSeconds, q(x.ChecksJSON), q(x.CreatedAt), q(ts)))
}
func (s *Store) DeleteVerificationProfile(ctx context.Context, hostID int64, scopeType, scopeKey string) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM verification_profiles WHERE host_id=%d AND scope_type=%s AND scope_key=%s`, hostID, q(scopeType), q(scopeKey)))
}
func (s *Store) VerificationState(ctx context.Context, hostID int64, container string) (VerificationState, error) {
	var x []VerificationState
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,status,details_json,checked_at,error FROM verification_state WHERE host_id=%d AND container_name=%s LIMIT 1`, hostID, q(container)), &x)
	if err != nil {
		return VerificationState{}, err
	}
	if len(x) == 0 {
		return VerificationState{HostID: hostID, ContainerName: container, Status: "not_configured", DetailsJSON: "[]"}, nil
	}
	return x[0], nil
}
func (s *Store) SaveVerificationState(ctx context.Context, x VerificationState) error {
	if x.Status == "" {
		x.Status = "not_configured"
	}
	if x.DetailsJSON == "" {
		x.DetailsJSON = "[]"
	}
	if x.CheckedAt == "" {
		x.CheckedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO verification_state(host_id,container_name,status,details_json,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s) ON CONFLICT(host_id,container_name) DO UPDATE SET status=excluded.status,details_json=excluded.details_json,checked_at=excluded.checked_at,error=excluded.error`, x.HostID, q(x.ContainerName), q(x.Status), q(x.DetailsJSON), q(x.CheckedAt), q(x.Error)))
}
func (s *Store) VerificationScopeState(ctx context.Context, hostID int64, scopeType, scopeKey string) (VerificationScopeState, error) {
	var x []VerificationScopeState
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,scope_type,scope_key,status,details_json,checked_at,error FROM verification_scope_state WHERE host_id=%d AND scope_type=%s AND scope_key=%s LIMIT 1`, hostID, q(scopeType), q(scopeKey)), &x)
	if err != nil {
		return VerificationScopeState{}, err
	}
	if len(x) == 0 {
		return VerificationScopeState{HostID: hostID, ScopeType: scopeType, ScopeKey: scopeKey, Status: "not_configured", DetailsJSON: "[]"}, nil
	}
	return x[0], nil
}
func (s *Store) SaveVerificationScopeState(ctx context.Context, x VerificationScopeState) error {
	if x.Status == "" {
		x.Status = "not_configured"
	}
	if x.DetailsJSON == "" {
		x.DetailsJSON = "[]"
	}
	if x.CheckedAt == "" {
		x.CheckedAt = now()
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO verification_scope_state(host_id,scope_type,scope_key,status,details_json,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s,%s) ON CONFLICT(host_id,scope_type,scope_key) DO UPDATE SET status=excluded.status,details_json=excluded.details_json,checked_at=excluded.checked_at,error=excluded.error`, x.HostID, q(x.ScopeType), q(x.ScopeKey), q(x.Status), q(x.DetailsJSON), q(x.CheckedAt), q(x.Error)))
}
func (s *Store) DeleteVerificationScopeState(ctx context.Context, hostID int64, scopeType, scopeKey string) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM verification_scope_state WHERE host_id=%d AND scope_type=%s AND scope_key=%s`, hostID, q(scopeType), q(scopeKey)))
}
func (s *Store) UpdateChains(ctx context.Context) ([]UpdateChain, error) {
	var x []UpdateChain
	err := s.query(ctx, `SELECT id,name,host_id,automation_id,scope_type,scope_key,policy_mode,allow_preflight_warnings,stop_on_failure,rollback_completed,created_at,updated_at,last_run_at,last_status FROM update_chains ORDER BY name,id`, &x)
	return x, err
}
func (s *Store) UpdateChain(ctx context.Context, id int64) (UpdateChain, error) {
	var x []UpdateChain
	err := s.query(ctx, fmt.Sprintf(`SELECT id,name,host_id,automation_id,scope_type,scope_key,policy_mode,allow_preflight_warnings,stop_on_failure,rollback_completed,created_at,updated_at,last_run_at,last_status FROM update_chains WHERE id=%d LIMIT 1`, id), &x)
	if err != nil {
		return UpdateChain{}, err
	}
	if len(x) == 0 {
		return UpdateChain{}, fmt.Errorf("update chain not found")
	}
	return x[0], nil
}
func (s *Store) SaveUpdateChain(ctx context.Context, x UpdateChain, steps []UpdateChainStep) (int64, error) {
	if strings.TrimSpace(x.ScopeType) == "" {
		x.ScopeType = "custom"
	}
	if strings.TrimSpace(x.PolicyMode) == "" {
		x.PolicyMode = "inherit"
	}
	ts := now()
	var id = x.ID
	var err error
	if id == 0 {
		id, err = s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_chains(name,host_id,automation_id,scope_type,scope_key,policy_mode,allow_preflight_warnings,stop_on_failure,rollback_completed,created_at,updated_at) VALUES(%s,%d,%d,%s,%s,%s,%d,%d,%d,%s,%s); SELECT last_insert_rowid();`, q(x.Name), x.HostID, x.AutomationID, q(x.ScopeType), q(x.ScopeKey), q(x.PolicyMode), b(x.AllowPreflightWarnings), b(x.StopOnFailure), b(x.RollbackCompleted), q(ts), q(ts)))
	} else {
		err = s.exec(ctx, fmt.Sprintf(`UPDATE update_chains SET name=%s,host_id=%d,automation_id=%d,scope_type=%s,scope_key=%s,policy_mode=%s,allow_preflight_warnings=%d,stop_on_failure=%d,rollback_completed=%d,updated_at=%s WHERE id=%d`, q(x.Name), x.HostID, x.AutomationID, q(x.ScopeType), q(x.ScopeKey), q(x.PolicyMode), b(x.AllowPreflightWarnings), b(x.StopOnFailure), b(x.RollbackCompleted), q(ts), id))
	}
	if err != nil {
		return 0, err
	}
	if err = s.exec(ctx, fmt.Sprintf(`DELETE FROM update_chain_steps WHERE chain_id=%d`, id)); err != nil {
		return 0, err
	}
	for i, st := range steps {
		pos := st.Position
		if pos <= 0 {
			pos = i + 1
		}
		currentAction := strings.TrimSpace(st.CurrentAction)
		if currentAction == "" {
			currentAction = "skip"
		}
		if err = s.exec(ctx, fmt.Sprintf(`INSERT INTO update_chain_steps(chain_id,position,container_name,current_action,wait_seconds) VALUES(%d,%d,%s,%s,%d)`, id, pos, q(st.ContainerName), q(currentAction), st.WaitSeconds)); err != nil {
			return 0, err
		}
	}
	return id, nil
}
func (s *Store) DeleteUpdateChain(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM update_chain_steps WHERE chain_id=%d; DELETE FROM update_chains WHERE id=%d`, id, id))
}
func (s *Store) UpdateChainSteps(ctx context.Context, chainID int64) ([]UpdateChainStep, error) {
	var x []UpdateChainStep
	err := s.query(ctx, fmt.Sprintf(`SELECT id,chain_id,position,container_name,current_action,wait_seconds FROM update_chain_steps WHERE chain_id=%d ORDER BY position,id`, chainID), &x)
	return x, err
}
func (s *Store) TouchUpdateChain(ctx context.Context, id int64, status string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chains SET last_run_at=%s,last_status=%s,updated_at=%s WHERE id=%d`, q(now()), q(status), q(now()), id))
}
func (s *Store) CreateUpdateChainRun(ctx context.Context, x UpdateChainRun) (int64, error) {
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_chain_runs(chain_id,chain_name,host_id,job_id,trigger,actor,status,recovery_action,recovered_at,started_at) VALUES(%d,%s,%d,%d,%s,%s,%s,%s,%s,%s); SELECT last_insert_rowid();`, x.ChainID, q(x.ChainName), x.HostID, x.JobID, q(x.Trigger), q(x.Actor), q(x.Status), q(x.RecoveryAction), q(x.RecoveredAt), q(now())))
}
func (s *Store) FinishUpdateChainRun(ctx context.Context, id int64, status, errText string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chain_runs SET status=%s,finished_at=%s,error=%s WHERE id=%d`, q(status), q(now()), q(errText), id))
}
func (s *Store) SetUpdateChainRunRecovery(ctx context.Context, id int64, status, action, errText string, finished bool) error {
	fin := ""
	recovered := ""
	if finished {
		fin = now()
		recovered = fin
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chain_runs SET status=%s,recovery_action=%s,recovered_at=CASE WHEN %s='' THEN recovered_at ELSE %s END,finished_at=CASE WHEN %s='' THEN finished_at ELSE %s END,error=%s WHERE id=%d`, q(status), q(action), q(recovered), q(recovered), q(fin), q(fin), q(errText), id))
}
func (s *Store) UpdateChainRun(ctx context.Context, id int64) (UpdateChainRun, error) {
	var x []UpdateChainRun
	err := s.query(ctx, fmt.Sprintf(`SELECT id,chain_id,chain_name,host_id,job_id,trigger,actor,status,recovery_action,recovered_at,started_at,finished_at,error FROM update_chain_runs WHERE id=%d LIMIT 1`, id), &x)
	if err != nil {
		return UpdateChainRun{}, err
	}
	if len(x) == 0 {
		return UpdateChainRun{}, fmt.Errorf("update chain run %d not found", id)
	}
	return x[0], nil
}

func (s *Store) UpdateChainRuns(ctx context.Context, chainID int64, limit int) ([]UpdateChainRun, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	where := "1=1"
	if chainID > 0 {
		where = fmt.Sprintf("chain_id=%d", chainID)
	}
	var x []UpdateChainRun
	err := s.query(ctx, fmt.Sprintf(`SELECT id,chain_id,chain_name,host_id,job_id,trigger,actor,status,recovery_action,recovered_at,started_at,finished_at,error FROM update_chain_runs WHERE %s ORDER BY id DESC LIMIT %d`, where, limit), &x)
	return x, err
}
func (s *Store) ActiveUpdateChainRuns(ctx context.Context) ([]UpdateChainRun, error) {
	var x []UpdateChainRun
	err := s.query(ctx, `SELECT id,chain_id,chain_name,host_id,job_id,trigger,actor,status,recovery_action,recovered_at,started_at,finished_at,error FROM update_chain_runs WHERE status IN ('queued','running','recovering','recovery_required') ORDER BY id`, &x)
	return x, err
}
func (s *Store) AddUpdateChainRunStep(ctx context.Context, x UpdateChainRunStep) (int64, error) {
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_chain_run_steps(run_id,position,container_name,status,job_id,started_at,finished_at,error) VALUES(%d,%d,%s,%s,%d,%s,%s,%s); SELECT last_insert_rowid();`, x.RunID, x.Position, q(x.ContainerName), q(x.Status), x.JobID, q(x.StartedAt), q(x.FinishedAt), q(x.Error)))
}
func (s *Store) UpdateChainRunStep(ctx context.Context, id int64, status string, jobID int64, errText string, finished bool) error {
	fin := ""
	if finished {
		fin = now()
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chain_run_steps SET status=%s,job_id=%d,finished_at=%s,error=%s WHERE id=%d`, q(status), jobID, q(fin), q(errText), id))
}
func (s *Store) UpdateChainRunSteps(ctx context.Context, runID int64) ([]UpdateChainRunStep, error) {
	var x []UpdateChainRunStep
	err := s.query(ctx, fmt.Sprintf(`SELECT id,run_id,position,container_name,status,job_id,started_at,finished_at,error FROM update_chain_run_steps WHERE run_id=%d ORDER BY position,id`, runID), &x)
	return x, err
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

const auditRetention = 5000

func (s *Store) Audit(ctx context.Context, actor, action string, hostID int64, container, details string) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO audit_events(ts,actor,action,host_id,container_name,details) VALUES(%s,%s,%s,%d,%s,%s); DELETE FROM audit_events WHERE id NOT IN (SELECT id FROM audit_events ORDER BY id DESC LIMIT %d);`, q(now()), q(actor), q(action), hostID, q(container), q(details), auditRetention))
}
func (s *Store) Audits(ctx context.Context, limit int) ([]Audit, error) {
	if limit < 1 || limit > 5000 {
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
	if limit < 1 || limit > 5000 {
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
	err := s.query(ctx, fmt.Sprintf(`SELECT host_id,container_name,installed_version,installed_source,latest_version,latest_source,update_kind,security_update,release_repo,published_at,checked_at,error FROM version_cache WHERE host_id=%d AND container_name=%s`, hostID, q(name)), &x)
	if err != nil {
		return VersionInfo{}, err
	}
	if len(x) == 0 {
		return VersionInfo{HostID: hostID, ContainerName: name}, nil
	}
	return x[0], nil
}
func (s *Store) SaveVersion(ctx context.Context, v VersionInfo) error {
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO version_cache(host_id,container_name,installed_version,installed_source,latest_version,latest_source,update_kind,security_update,release_repo,published_at,checked_at,error) VALUES(%d,%s,%s,%s,%s,%s,%s,%d,%s,%s,%s,%s) ON CONFLICT(host_id,container_name) DO UPDATE SET installed_version=excluded.installed_version,installed_source=excluded.installed_source,latest_version=excluded.latest_version,latest_source=excluded.latest_source,update_kind=excluded.update_kind,security_update=excluded.security_update,release_repo=excluded.release_repo,published_at=excluded.published_at,checked_at=excluded.checked_at,error=excluded.error;`, v.HostID, q(v.ContainerName), q(v.Installed), q(v.InstalledSource), q(v.Latest), q(v.LatestSource), q(v.UpdateKind), b(v.SecurityUpdate), q(v.ReleaseRepo), q(v.PublishedAt), q(v.CheckedAt), q(v.Error)))
}

func (s *Store) Automations(ctx context.Context) ([]Automation, error) {
	var x []Automation
	err := s.query(ctx, `SELECT id,name,cron,target_type,target_id,kind,cleanup_images,cleanup_networks,cleanup_build_cache,cleanup_volumes,enabled,last_run_at FROM automations ORDER BY name`, &x)
	return x, err
}
func (s *Store) Automation(ctx context.Context, id int64) (Automation, error) {
	var x []Automation
	err := s.query(ctx, fmt.Sprintf(`SELECT id,name,cron,target_type,target_id,kind,cleanup_images,cleanup_networks,cleanup_build_cache,cleanup_volumes,enabled,last_run_at FROM automations WHERE id=%d LIMIT 1`, id), &x)
	if err != nil {
		return Automation{}, err
	}
	if len(x) == 0 {
		return Automation{}, fmt.Errorf("automation not found")
	}
	return x[0], nil
}
func (s *Store) SaveAutomation(ctx context.Context, x Automation) (int64, error) {
	if strings.TrimSpace(x.Kind) == "" {
		x.Kind = "policy"
	}
	if x.ID == 0 {
		return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO automations(name,cron,target_type,target_id,kind,cleanup_images,cleanup_networks,cleanup_build_cache,cleanup_volumes,enabled,last_run_at) VALUES(%s,%s,%s,%d,%s,%d,%d,%d,%d,%d,%s); SELECT last_insert_rowid();`, q(x.Name), q(x.Cron), q(x.TargetType), x.TargetID, q(x.Kind), b(x.CleanupImages), b(x.CleanupNetworks), b(x.CleanupBuildCache), b(x.CleanupVolumes), b(x.Enabled), q(x.LastRunAt)))
	}
	err := s.exec(ctx, fmt.Sprintf(`UPDATE automations SET name=%s,cron=%s,target_type=%s,target_id=%d,kind=%s,cleanup_images=%d,cleanup_networks=%d,cleanup_build_cache=%d,cleanup_volumes=%d,enabled=%d WHERE id=%d`, q(x.Name), q(x.Cron), q(x.TargetType), x.TargetID, q(x.Kind), b(x.CleanupImages), b(x.CleanupNetworks), b(x.CleanupBuildCache), b(x.CleanupVolumes), b(x.Enabled), x.ID))
	return x.ID, err
}
func (s *Store) DeleteAutomation(ctx context.Context, id int64) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chains SET automation_id=0,updated_at=%s WHERE automation_id=%d; DELETE FROM automations WHERE id=%d`, q(now()), id, id))
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
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_chains SET automation_id=0,updated_at=%s WHERE automation_id IN (SELECT id FROM automations WHERE target_type='group' AND target_id=%d); DELETE FROM user_groups WHERE group_id=%d; DELETE FROM host_group_members WHERE group_id=%d; DELETE FROM automations WHERE target_type='group' AND target_id=%d; DELETE FROM host_groups WHERE id=%d;`, q(now()), id, id, id, id, id))
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
	if limit <= 0 || limit > 5000 {
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
