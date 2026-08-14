package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type UpdateTransaction struct {
	ID             int64  `json:"id"`
	JobID          int64  `json:"job_id"`
	HostID         int64  `json:"host_id"`
	ContainerName  string `json:"container_name"`
	Trigger        string `json:"trigger"`
	Actor          string `json:"actor"`
	State          string `json:"state"`
	Status         string `json:"status"`
	SnapshotID     string `json:"snapshot_id"`
	RestorePointID int64  `json:"restore_point_id"`
	TargetDigest   string `json:"target_digest"`
	StartedAt      string `json:"started_at"`
	UpdatedAt      string `json:"updated_at"`
	FinishedAt     string `json:"finished_at"`
	Error          string `json:"error"`
	RecoveryAction string `json:"recovery_action"`
}

type UpdateTransactionEvent struct {
	ID            int64  `json:"id"`
	TransactionID int64  `json:"transaction_id"`
	TS            string `json:"ts"`
	FromState     string `json:"from_state"`
	ToState       string `json:"to_state"`
	Status        string `json:"status"`
	Message       string `json:"message"`
	DurationMS    int64  `json:"duration_ms"`
}

type OperationLease struct {
	ResourceKey   string `json:"resource_key"`
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	Owner         string `json:"owner"`
	OperationType string `json:"operation_type"`
	TransactionID int64  `json:"transaction_id"`
	JobID         int64  `json:"job_id"`
	AcquiredAt    string `json:"acquired_at"`
	HeartbeatAt   string `json:"heartbeat_at"`
	ExpiresAt     string `json:"expires_at"`
}

type VerificationHistory struct {
	ID            int64  `json:"id"`
	TS            string `json:"ts"`
	HostID        int64  `json:"host_id"`
	ContainerName string `json:"container_name"`
	Trigger       string `json:"trigger"`
	Actor         string `json:"actor"`
	JobID         int64  `json:"job_id"`
	TransactionID int64  `json:"transaction_id"`
	Status        string `json:"status"`
	ScopeType     string `json:"scope_type"`
	ScopeKey      string `json:"scope_key"`
	DurationMS    int64  `json:"duration_ms"`
	DetailsJSON   string `json:"details_json"`
	Error         string `json:"error"`
}

type RecoveryGCRun struct {
	ID                   int64  `json:"id"`
	TS                   string `json:"ts"`
	Status               string `json:"status"`
	RestorePointsChecked int    `json:"restore_points_checked"`
	Degraded             int    `json:"degraded"`
	Expired              int    `json:"expired"`
	ImagesRemoved        int    `json:"images_removed"`
	SnapshotsRemoved     int    `json:"snapshots_removed"`
	HelpersRemoved       int    `json:"helpers_removed"`
	UnusableRemoved      int    `json:"unusable_removed"`
	ErrorsJSON           string `json:"errors_json"`
}

func (s *Store) CreateUpdateTransaction(ctx context.Context, x UpdateTransaction) (int64, error) {
	ts := now()
	if strings.TrimSpace(x.State) == "" {
		x.State = "queued"
	}
	if strings.TrimSpace(x.Status) == "" {
		x.Status = "running"
	}
	return s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO update_transactions(job_id,host_id,container_name,trigger,actor,state,status,snapshot_id,restore_point_id,target_digest,started_at,updated_at,error,recovery_action) VALUES(%d,%d,%s,%s,%s,%s,%s,%s,%d,%s,%s,%s,%s,%s); SELECT last_insert_rowid();`, x.JobID, x.HostID, q(x.ContainerName), q(x.Trigger), q(x.Actor), q(x.State), q(x.Status), q(x.SnapshotID), x.RestorePointID, q(x.TargetDigest), q(ts), q(ts), q(x.Error), q(x.RecoveryAction)))
}

func (s *Store) UpdateTransaction(ctx context.Context, id int64) (UpdateTransaction, error) {
	var xs []UpdateTransaction
	err := s.query(ctx, fmt.Sprintf(`SELECT id,job_id,host_id,container_name,trigger,actor,state,status,snapshot_id,restore_point_id,target_digest,started_at,updated_at,finished_at,error,recovery_action FROM update_transactions WHERE id=%d LIMIT 1`, id), &xs)
	if err != nil {
		return UpdateTransaction{}, err
	}
	if len(xs) == 0 {
		return UpdateTransaction{}, fmt.Errorf("update transaction %d not found", id)
	}
	return xs[0], nil
}

func (s *Store) UpdateTransactionByJob(ctx context.Context, jobID int64) (UpdateTransaction, error) {
	var xs []UpdateTransaction
	err := s.query(ctx, fmt.Sprintf(`SELECT id,job_id,host_id,container_name,trigger,actor,state,status,snapshot_id,restore_point_id,target_digest,started_at,updated_at,finished_at,error,recovery_action FROM update_transactions WHERE job_id=%d ORDER BY id DESC LIMIT 1`, jobID), &xs)
	if err != nil {
		return UpdateTransaction{}, err
	}
	if len(xs) == 0 {
		return UpdateTransaction{}, fmt.Errorf("update transaction for job %d not found", jobID)
	}
	return xs[0], nil
}

func (s *Store) ActiveUpdateTransactions(ctx context.Context) ([]UpdateTransaction, error) {
	var xs []UpdateTransaction
	err := s.query(ctx, `SELECT id,job_id,host_id,container_name,trigger,actor,state,status,snapshot_id,restore_point_id,target_digest,started_at,updated_at,finished_at,error,recovery_action FROM update_transactions WHERE status IN ('running','recovering','recovery_required') ORDER BY id`, &xs)
	return xs, err
}

func (s *Store) UpdateTransactions(ctx context.Context, limit int) ([]UpdateTransaction, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	var xs []UpdateTransaction
	err := s.query(ctx, fmt.Sprintf(`SELECT id,job_id,host_id,container_name,trigger,actor,state,status,snapshot_id,restore_point_id,target_digest,started_at,updated_at,finished_at,error,recovery_action FROM update_transactions ORDER BY id DESC LIMIT %d`, limit), &xs)
	return xs, err
}

func (s *Store) TransitionUpdateTransaction(ctx context.Context, id int64, fromState, toState, status, message string, durationMS int64) error {
	ts := now()
	finished := ""
	if status == "success" || status == "failed" || status == "rolled_back" || status == "interrupted" {
		finished = ts
	}
	if strings.TrimSpace(status) == "" {
		status = "running"
	}
	if err := s.exec(ctx, fmt.Sprintf(`UPDATE update_transactions SET state=%s,status=%s,updated_at=%s,finished_at=CASE WHEN %s='' THEN finished_at ELSE %s END,error=CASE WHEN %s='failed' OR %s='interrupted' THEN %s ELSE error END WHERE id=%d;`, q(toState), q(status), q(ts), q(finished), q(finished), q(status), q(status), q(message), id)); err != nil {
		return err
	}
	return s.exec(ctx, fmt.Sprintf(`INSERT INTO update_transaction_events(transaction_id,ts,from_state,to_state,status,message,duration_ms) VALUES(%d,%s,%s,%s,%s,%s,%d)`, id, q(ts), q(fromState), q(toState), q(status), q(message), durationMS))
}

func (s *Store) SetUpdateTransactionRecovery(ctx context.Context, id int64, status, action, errText string) error {
	fin := ""
	if status == "failed" || status == "success" || status == "rolled_back" || status == "interrupted" {
		fin = now()
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_transactions SET status=%s,recovery_action=%s,error=%s,updated_at=%s,finished_at=CASE WHEN %s='' THEN finished_at ELSE %s END WHERE id=%d`, q(status), q(action), q(errText), q(now()), q(fin), q(fin), id))
}

func (s *Store) SetUpdateTransactionPrepared(ctx context.Context, id int64, snapshotID string, restorePointID int64, targetDigest string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_transactions SET snapshot_id=%s,restore_point_id=%d,target_digest=%s,updated_at=%s WHERE id=%d`, q(snapshotID), restorePointID, q(targetDigest), q(now()), id))
}

func (s *Store) UpdateTransactionEvents(ctx context.Context, txID int64) ([]UpdateTransactionEvent, error) {
	var xs []UpdateTransactionEvent
	err := s.query(ctx, fmt.Sprintf(`SELECT id,transaction_id,ts,from_state,to_state,status,message,duration_ms FROM update_transaction_events WHERE transaction_id=%d ORDER BY id`, txID), &xs)
	return xs, err
}

func leaseExpiry(ttl time.Duration) string { return time.Now().UTC().Add(ttl).Format(time.RFC3339Nano) }

func (s *Store) AcquireOperationLease(ctx context.Context, x OperationLease, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	ts := now()
	exp := leaseExpiry(ttl)
	// Leases are hierarchical. A host-scoped destructive operation (cleanup/GC)
	// conflicts with every container mutation on that host, while unrelated
	// containers may still update concurrently. This closes the race where image
	// cleanup could remove an object between preflight and restore-point creation.
	hostKey := fmt.Sprintf("host:%d", x.HostID)
	conflict := fmt.Sprintf("resource_key=%s", q(x.ResourceKey))
	if strings.HasPrefix(x.ResourceKey, "container:") {
		conflict = fmt.Sprintf("(resource_key=%s OR resource_key=%s)", q(x.ResourceKey), q(hostKey))
	} else if x.ResourceKey == hostKey {
		conflict = fmt.Sprintf("(resource_key=%s OR resource_key LIKE %s)", q(hostKey), q(fmt.Sprintf("container:%d:%%", x.HostID)))
	}
	sql := fmt.Sprintf(`DELETE FROM operation_leases WHERE expires_at < %s;
INSERT INTO operation_leases(resource_key,host_id,container_name,owner,operation_type,transaction_id,job_id,acquired_at,heartbeat_at,expires_at)
SELECT %s,%d,%s,%s,%s,%d,%d,%s,%s,%s
WHERE NOT EXISTS(SELECT 1 FROM operation_leases WHERE %s)
ON CONFLICT(resource_key) DO NOTHING;
SELECT changes();`, q(ts), q(x.ResourceKey), x.HostID, q(x.ContainerName), q(x.Owner), q(x.OperationType), x.TransactionID, x.JobID, q(ts), q(ts), q(exp), conflict)
	n, err := s.scalarInt(ctx, sql)
	return n > 0, err
}

func (s *Store) RenewOperationLease(ctx context.Context, resourceKey, owner string, transactionID int64, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE operation_leases SET heartbeat_at=%s,expires_at=%s,transaction_id=CASE WHEN %d>0 THEN %d ELSE transaction_id END WHERE resource_key=%s AND owner=%s`, q(now()), q(leaseExpiry(ttl)), transactionID, transactionID, q(resourceKey), q(owner)))
}

func (s *Store) ReleaseOperationLease(ctx context.Context, resourceKey, owner string) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM operation_leases WHERE resource_key=%s AND owner=%s`, q(resourceKey), q(owner)))
}
func (s *Store) ReleaseOperationLeaseByJob(ctx context.Context, jobID int64) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM operation_leases WHERE job_id=%d`, jobID))
}
func (s *Store) ExpireOperationLeases(ctx context.Context) error {
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM operation_leases WHERE expires_at < %s`, q(now())))
}
func (s *Store) OperationLeases(ctx context.Context) ([]OperationLease, error) {
	var xs []OperationLease
	err := s.query(ctx, `SELECT resource_key,host_id,container_name,owner,operation_type,transaction_id,job_id,acquired_at,heartbeat_at,expires_at FROM operation_leases ORDER BY host_id,container_name`, &xs)
	return xs, err
}

func (s *Store) AddVerificationHistory(ctx context.Context, x VerificationHistory) (int64, error) {
	if strings.TrimSpace(x.TS) == "" {
		x.TS = now()
	}
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO verification_history(ts,host_id,container_name,trigger,actor,job_id,transaction_id,status,scope_type,scope_key,duration_ms,details_json,error) VALUES(%s,%d,%s,%s,%s,%d,%d,%s,%s,%s,%d,%s,%s); SELECT last_insert_rowid();`, q(x.TS), x.HostID, q(x.ContainerName), q(x.Trigger), q(x.Actor), x.JobID, x.TransactionID, q(x.Status), q(x.ScopeType), q(x.ScopeKey), x.DurationMS, q(x.DetailsJSON), q(x.Error)))
	if err == nil {
		_ = s.exec(ctx, `DELETE FROM verification_history WHERE id NOT IN (SELECT id FROM verification_history ORDER BY id DESC LIMIT 5000);`)
	}
	return id, err
}

func (s *Store) VerificationHistory(ctx context.Context, hostID int64, container string, limit int) ([]VerificationHistory, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	where := []string{"1=1"}
	if hostID > 0 {
		where = append(where, fmt.Sprintf("host_id=%d", hostID))
	}
	if strings.TrimSpace(container) != "" {
		where = append(where, "container_name="+q(container))
	}
	var xs []VerificationHistory
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,host_id,container_name,trigger,actor,job_id,transaction_id,status,scope_type,scope_key,duration_ms,details_json,error FROM verification_history WHERE %s ORDER BY id DESC LIMIT %d`, strings.Join(where, " AND "), limit), &xs)
	return xs, err
}

func (s *Store) AddRecoveryGCRun(ctx context.Context, x RecoveryGCRun) (int64, error) {
	if strings.TrimSpace(x.TS) == "" {
		x.TS = now()
	}
	id, err := s.scalarInt(ctx, fmt.Sprintf(`INSERT INTO recovery_gc_runs(ts,status,restore_points_checked,degraded,expired,images_removed,snapshots_removed,helpers_removed,unusable_removed,errors_json) VALUES(%s,%s,%d,%d,%d,%d,%d,%d,%d,%s); SELECT last_insert_rowid();`, q(x.TS), q(x.Status), x.RestorePointsChecked, x.Degraded, x.Expired, x.ImagesRemoved, x.SnapshotsRemoved, x.HelpersRemoved, x.UnusableRemoved, q(x.ErrorsJSON)))
	if err == nil {
		_ = s.exec(ctx, `DELETE FROM recovery_gc_runs WHERE id NOT IN (SELECT id FROM recovery_gc_runs ORDER BY id DESC LIMIT 200);`)
	}
	return id, err
}
func (s *Store) RecoveryGCRuns(ctx context.Context, limit int) ([]RecoveryGCRun, error) {
	if limit < 1 || limit > 200 {
		limit = 20
	}
	var xs []RecoveryGCRun
	err := s.query(ctx, fmt.Sprintf(`SELECT id,ts,status,restore_points_checked,degraded,expired,images_removed,snapshots_removed,helpers_removed,unusable_removed,errors_json FROM recovery_gc_runs ORDER BY id DESC LIMIT %d`, limit), &xs)
	return xs, err
}

func (s *Store) FailActiveJobsWithoutTransaction(ctx context.Context, reason string) (int, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "operation interrupted"
	}
	n, err := s.scalarInt(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running') AND id NOT IN (SELECT job_id FROM update_transactions WHERE status IN ('running','recovering','recovery_required')) AND id NOT IN (SELECT job_id FROM update_chain_runs WHERE status IN ('queued','running','recovering','recovery_required') AND job_id>0);`)
	if err != nil || n == 0 {
		return int(n), err
	}
	err = s.exec(ctx, fmt.Sprintf(`UPDATE jobs SET status='failed',finished_at=%s,error=CASE WHEN error='' THEN %s ELSE error END WHERE status IN ('queued','running') AND id NOT IN (SELECT job_id FROM update_transactions WHERE status IN ('running','recovering','recovery_required')) AND id NOT IN (SELECT job_id FROM update_chain_runs WHERE status IN ('queued','running','recovering','recovery_required') AND job_id>0);`, q(now()), q(reason)))
	return int(n), err
}

func (s *Store) PruneReliabilityHistory(ctx context.Context, keepTransactions int) error {
	if keepTransactions < 100 {
		keepTransactions = 5000
	}
	// Never prune active transactions. Events are removed only for transactions
	// that are themselves outside the retained terminal history window.
	return s.exec(ctx, fmt.Sprintf(`DELETE FROM update_transaction_events WHERE transaction_id IN (
SELECT id FROM update_transactions WHERE status NOT IN ('running','recovering','recovery_required') AND id NOT IN (
SELECT id FROM update_transactions WHERE status NOT IN ('running','recovering','recovery_required') ORDER BY id DESC LIMIT %d));
DELETE FROM update_transactions WHERE status NOT IN ('running','recovering','recovery_required') AND id NOT IN (
SELECT id FROM update_transactions WHERE status NOT IN ('running','recovering','recovery_required') ORDER BY id DESC LIMIT %d);`, keepTransactions, keepTransactions))
}

func (s *Store) SetRestorePointIntegrity(ctx context.Context, id int64, status, details string) error {
	return s.exec(ctx, fmt.Sprintf(`UPDATE restore_points SET integrity_status=%s,integrity_checked_at=%s,integrity_details=%s,updated_at=%s WHERE id=%d`, q(status), q(now()), q(details), q(now()), id))
}

func (s *Store) HasRecoveryRequiredTransaction(ctx context.Context, hostID int64, container string) (bool, error) {
	n, err := s.scalarInt(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM update_transactions WHERE host_id=%d AND container_name=%s AND status='recovery_required'`, hostID, q(strings.TrimSpace(container))))
	return n > 0, err
}

func (s *Store) ResolveRecoveryRequiredTransactionsByRestorePoint(ctx context.Context, restorePointID int64, action string) error {
	if restorePointID <= 0 {
		return nil
	}
	if strings.TrimSpace(action) == "" {
		action = "manual_rollback"
	}
	return s.exec(ctx, fmt.Sprintf(`UPDATE update_transactions SET status='rolled_back',state='rolled_back',recovery_action=%s,error='',updated_at=%s,finished_at=CASE WHEN finished_at='' THEN %s ELSE finished_at END WHERE restore_point_id=%d AND status='recovery_required'`, q(action), q(now()), q(now()), restorePointID))
}
