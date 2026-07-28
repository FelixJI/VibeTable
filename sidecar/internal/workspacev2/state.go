package workspacev2

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	_ "modernc.org/sqlite"
)

type RetentionPolicy struct {
	ContractVersion      string   `json:"contractVersion"`
	PolicyRevision       uint64   `json:"policyRevision"`
	SnapshotDays         uint64   `json:"snapshotDays"`
	SnapshotCount        uint64   `json:"snapshotCount"`
	SnapshotBuckets      []string `json:"snapshotBuckets"`
	FileRevisionDays     uint64   `json:"fileRevisionDays"`
	FileRevisionCount    uint64   `json:"fileRevisionCount"`
	FileRevisionBuckets  []string `json:"fileRevisionBuckets"`
	TrashMonths          uint64   `json:"trashMonths"`
	RepositoryLimitBytes *uint64  `json:"repositoryLimitBytes"`
}

func defaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		ContractVersion:     "2.0",
		PolicyRevision:      1,
		SnapshotDays:        30,
		SnapshotCount:       50,
		SnapshotBuckets:     []string{"hourly", "daily", "weekly", "monthly"},
		FileRevisionDays:    30,
		FileRevisionCount:   100,
		FileRevisionBuckets: []string{"daily", "weekly", "monthly"},
		TrashMonths:         3,
	}
}

type stateStore struct {
	db *sql.DB
}

func openStateStore(path string) (*stateStore, error) {
	if path == "" {
		return nil, errors.New("workspace.v2.state_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &stateStore{db: db}
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		CREATE TABLE IF NOT EXISTS rpc_sequence (
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			high_watermark INTEGER NOT NULL,
			PRIMARY KEY(workspace_id, session_epoch)
		);
		CREATE TABLE IF NOT EXISTS rpc_operation_receipts (
			workspace_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id, operation_id)
		);
		CREATE TABLE IF NOT EXISTS external_file_operation_journal (
			workspace_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL,
			session_epoch INTEGER NOT NULL,
			sequence INTEGER NOT NULL,
			staging_path TEXT NOT NULL,
			target_path TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			content_size INTEGER NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('prepared', 'completed')),
			PRIMARY KEY(workspace_id, operation_id)
		);
		CREATE TABLE IF NOT EXISTS retention_policy (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			policy_revision INTEGER NOT NULL,
			snapshot_days INTEGER NOT NULL,
			snapshot_count INTEGER NOT NULL,
			snapshot_buckets_json TEXT NOT NULL,
			file_revision_days INTEGER NOT NULL,
			file_revision_count INTEGER NOT NULL,
			file_revision_buckets_json TEXT NOT NULL,
			repository_limit_bytes INTEGER,
			mutation_revision INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS pending_file_changes (
			change_id TEXT PRIMARY KEY,
			relative_path TEXT NOT NULL UNIQUE,
			missing INTEGER NOT NULL,
			observed_hash TEXT NOT NULL,
			observed_size INTEGER NOT NULL,
			reason TEXT NOT NULL,
			candidate_document_ids_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS snapshot_extract_plans (
			plan_id TEXT PRIMARY KEY,
			snapshot_id TEXT NOT NULL,
			catalog_revision INTEGER NOT NULL,
			document_id TEXT NOT NULL,
			revision_id TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			object_id TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			content_size INTEGER NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS snapshot_restore_plans (
			plan_id TEXT PRIMARY KEY,
			snapshot_id TEXT NOT NULL,
			catalog_revision INTEGER NOT NULL,
			mutation_revision INTEGER NOT NULL,
			diff_hash TEXT NOT NULL,
			target_mode TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS snapshot_import_plans (
			plan_id TEXT PRIMARY KEY,
			source_path TEXT NOT NULL,
			source_hash TEXT NOT NULL,
			source_size INTEGER NOT NULL,
			source_identity TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS repository_key_rotation_plans (
			plan_id TEXT PRIMARY KEY,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			mutation_revision INTEGER NOT NULL,
			catalog_revision INTEGER NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS replica_status_projection (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			coordination_strength TEXT NOT NULL
				CHECK(coordination_strength IN ('strong', 'advisory')),
			sync_state TEXT NOT NULL
				CHECK(sync_state IN (
					'localOnly', 'pending', 'syncing',
					'replicated', 'failed'
				)),
			pending_sync INTEGER NOT NULL CHECK(pending_sync IN (0, 1)),
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize workspace v2 state: %w", err)
	}
	// Upgrade pre-bucket Workspace v2 state in place. SQLite does not support
	// ADD COLUMN IF NOT EXISTS, so inspect first and fail closed on any other
	// schema error.
	if err := ensureRetentionColumn(
		db,
		"snapshot_buckets_json",
		`TEXT NOT NULL DEFAULT '["hourly","daily","weekly","monthly"]'`,
	); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSnapshotRestoreDiffHashColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSnapshotImportBindingColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureRetentionColumn(
		db,
		"file_revision_buckets_json",
		`TEXT NOT NULL DEFAULT '["daily","weekly","monthly"]'`,
	); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
		INSERT INTO retention_policy (
			singleton, policy_revision, snapshot_days, snapshot_count,
			snapshot_buckets_json, file_revision_days, file_revision_count,
			file_revision_buckets_json, repository_limit_bytes, mutation_revision
		) VALUES (
			1, 1, 30, 50, '["hourly","daily","weekly","monthly"]',
			30, 100, '["daily","weekly","monthly"]', NULL, 0
		)
		ON CONFLICT(singleton) DO NOTHING
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

type replicaStatusProjection struct {
	CoordinationStrength string
	SyncState            string
	PendingSync          bool
	UpdatedAt            time.Time
}

func (store *stateStore) putReplicaStatus(
	ctx context.Context,
	status replicaStatusProjection,
) error {
	if store == nil || store.db == nil ||
		(status.CoordinationStrength != "strong" &&
			status.CoordinationStrength != "advisory") ||
		(status.SyncState != "localOnly" &&
			status.SyncState != "pending" &&
			status.SyncState != "syncing" &&
			status.SyncState != "replicated" &&
			status.SyncState != "failed") ||
		status.UpdatedAt.IsZero() {
		return errors.New("replica.status_projection_invalid")
	}
	pending := 0
	if status.PendingSync {
		pending = 1
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO replica_status_projection (
			singleton, coordination_strength, sync_state,
			pending_sync, updated_at
		) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			coordination_strength = excluded.coordination_strength,
			sync_state = excluded.sync_state,
			pending_sync = excluded.pending_sync,
			updated_at = excluded.updated_at
	`, status.CoordinationStrength, status.SyncState, pending,
		status.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (store *stateStore) replicaStatus(
	ctx context.Context,
) (replicaStatusProjection, bool, error) {
	if store == nil || store.db == nil {
		return replicaStatusProjection{}, false,
			errors.New("workspace.v2.state_unavailable")
	}
	var result replicaStatusProjection
	var pending int
	var updatedAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT coordination_strength, sync_state,
			pending_sync, updated_at
		FROM replica_status_projection
		WHERE singleton = 1
	`).Scan(
		&result.CoordinationStrength,
		&result.SyncState,
		&pending,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return replicaStatusProjection{}, false, nil
	}
	if err != nil {
		return replicaStatusProjection{}, false, err
	}
	result.PendingSync = pending == 1
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil ||
		(result.CoordinationStrength != "strong" &&
			result.CoordinationStrength != "advisory") ||
		(result.SyncState != "localOnly" &&
			result.SyncState != "pending" &&
			result.SyncState != "syncing" &&
			result.SyncState != "replicated" &&
			result.SyncState != "failed") ||
		(pending != 0 && pending != 1) {
		return replicaStatusProjection{}, false,
			errors.New("replica.status_projection_corrupt")
	}
	return result, true, nil
}

type repositoryKeyRotationPlan struct {
	PlanID           string
	SessionEpoch     uint64
	FenceEpoch       uint64
	ClaimID          string
	MutationRevision uint64
	CatalogRevision  uint64
	ExpiresAt        string
}

func (store *stateStore) putRepositoryKeyRotationPlan(
	ctx context.Context,
	plan repositoryKeyRotationPlan,
) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO repository_key_rotation_plans (
			plan_id, session_epoch, fence_epoch, claim_id,
			mutation_revision, catalog_revision, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, plan.PlanID, plan.SessionEpoch, plan.FenceEpoch, plan.ClaimID,
		plan.MutationRevision, plan.CatalogRevision, plan.ExpiresAt)
	return err
}

func (store *stateStore) repositoryKeyRotationPlan(
	ctx context.Context,
	planID string,
) (repositoryKeyRotationPlan, error) {
	var plan repositoryKeyRotationPlan
	err := store.db.QueryRowContext(ctx, `
		SELECT plan_id, session_epoch, fence_epoch, claim_id,
		       mutation_revision, catalog_revision, expires_at
		  FROM repository_key_rotation_plans
		 WHERE plan_id = ?
	`, planID).Scan(
		&plan.PlanID,
		&plan.SessionEpoch,
		&plan.FenceEpoch,
		&plan.ClaimID,
		&plan.MutationRevision,
		&plan.CatalogRevision,
		&plan.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return repositoryKeyRotationPlan{},
			errors.New("repository.key_rotation_plan_missing")
	}
	return plan, err
}

func (store *stateStore) deleteRepositoryKeyRotationPlan(
	ctx context.Context,
	planID string,
) error {
	_, err := store.db.ExecContext(ctx, `
		DELETE FROM repository_key_rotation_plans WHERE plan_id = ?
	`, planID)
	return err
}

func (store *stateStore) loadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	var (
		receipt protocolv2.OperationReceipt
		scope   string
		raw     []byte
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, workspace_id, method, scope, request_hash,
		       result_json
		FROM rpc_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID,
		operationID,
	).Scan(
		&receipt.OperationID,
		&receipt.WorkspaceID,
		&receipt.Method,
		&scope,
		&receipt.RequestHash,
		&raw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocolv2.OperationReceipt{}, false, nil
	}
	if err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	receipt.Scope = protocolv2.ScopeKind(scope)
	receipt.Result = append(json.RawMessage(nil), raw...)
	if !validUUID(receipt.OperationID) ||
		receipt.WorkspaceID != workspaceID ||
		(receipt.Scope != protocolv2.GlobalScope &&
			receipt.Scope != protocolv2.WorkspaceScope) ||
		receipt.Method == "" ||
		receipt.RequestHash == "" ||
		!json.Valid(receipt.Result) {
		return protocolv2.OperationReceipt{}, false,
			errors.New("workspace.operation_receipt_corrupt")
	}
	return receipt, true, nil
}

func (store *stateStore) commitOperationReceipt(
	ctx context.Context,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		nil,
	)
}

func (store *stateStore) withOperationReceiptTransaction(
	ctx context.Context,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
	mutate func(*sql.Tx) error,
) error {
	if receipt.WorkspaceID != session.WorkspaceID ||
		!validUUID(receipt.OperationID) ||
		receipt.Method == "" ||
		(receipt.Scope != protocolv2.GlobalScope &&
			receipt.Scope != protocolv2.WorkspaceScope) ||
		receipt.RequestHash == "" ||
		len(receipt.Result) == 0 ||
		len(receipt.Result) > 16<<20 ||
		!json.Valid(receipt.Result) {
		return errors.New("workspace.operation_receipt_invalid")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if mutate != nil {
		if err := mutate(transaction); err != nil {
			return err
		}
	}
	var (
		method      string
		scope       string
		requestHash string
		result      []byte
	)
	err = transaction.QueryRowContext(ctx, `
		SELECT method, scope, request_hash, result_json
		FROM rpc_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		receipt.WorkspaceID,
		receipt.OperationID,
	).Scan(&method, &scope, &requestHash, &result)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO rpc_operation_receipts (
				workspace_id, operation_id, method, scope, request_hash,
				result_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			receipt.WorkspaceID,
			receipt.OperationID,
			receipt.Method,
			string(receipt.Scope),
			receipt.RequestHash,
			[]byte(receipt.Result),
			time.Now().UTC().Format(time.RFC3339Nano),
		)
	case err == nil:
		if method != receipt.Method ||
			scope != string(receipt.Scope) ||
			requestHash != receipt.RequestHash ||
			!bytes.Equal(result, receipt.Result) {
			return protocolv2.ErrOperationConflict
		}
	default:
		return err
	}
	if err != nil {
		return err
	}
	if receipt.Scope == protocolv2.WorkspaceScope {
		result, err := transaction.ExecContext(ctx, `
			INSERT INTO rpc_sequence (
				workspace_id, session_epoch, high_watermark
			) VALUES (?, ?, ?)
			ON CONFLICT(workspace_id, session_epoch) DO UPDATE
			SET high_watermark = excluded.high_watermark
			WHERE rpc_sequence.high_watermark < excluded.high_watermark`,
			session.WorkspaceID,
			session.Epoch,
			session.Sequence,
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			var current uint64
			if err := transaction.QueryRowContext(ctx, `
				SELECT high_watermark FROM rpc_sequence
				WHERE workspace_id = ? AND session_epoch = ?`,
				session.WorkspaceID,
				session.Epoch,
			).Scan(&current); err != nil {
				return err
			}
			if current != session.Sequence {
				return errors.New("workspace.sequence_stale")
			}
		}
	}
	return transaction.Commit()
}

type snapshotImportPlan struct {
	PlanID         string
	SourcePath     string
	SourceHash     string
	SourceSize     int64
	SourceIdentity string
	ExpiresAt      string
}

func (store *stateStore) putSnapshotImportPlan(
	ctx context.Context,
	plan snapshotImportPlan,
) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO snapshot_import_plans (
			plan_id, source_path, source_hash, source_size,
			source_identity, expires_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		plan.PlanID,
		plan.SourcePath,
		plan.SourceHash,
		plan.SourceSize,
		plan.SourceIdentity,
		plan.ExpiresAt,
	)
	return err
}

func (store *stateStore) snapshotImportPlan(
	ctx context.Context,
	planID string,
) (snapshotImportPlan, error) {
	var plan snapshotImportPlan
	err := store.db.QueryRowContext(ctx, `
		SELECT plan_id, source_path, source_hash, source_size,
		       source_identity, expires_at
		FROM snapshot_import_plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&plan.PlanID,
		&plan.SourcePath,
		&plan.SourceHash,
		&plan.SourceSize,
		&plan.SourceIdentity,
		&plan.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotImportPlan{}, errors.New("snapshot.import_plan_not_found")
	}
	return plan, err
}

func (store *stateStore) deleteSnapshotImportPlan(
	ctx context.Context,
	planID string,
) error {
	_, err := store.db.ExecContext(
		ctx,
		`DELETE FROM snapshot_import_plans WHERE plan_id = ?`,
		planID,
	)
	return err
}

type snapshotRestorePlan struct {
	PlanID           string
	SnapshotID       string
	CatalogRevision  uint64
	MutationRevision uint64
	DiffHash         string
	TargetMode       string
	ExpiresAt        string
}

func (store *stateStore) putSnapshotRestorePlan(
	ctx context.Context,
	plan snapshotRestorePlan,
) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO snapshot_restore_plans (
			plan_id, snapshot_id, catalog_revision, mutation_revision,
			diff_hash, target_mode, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		plan.PlanID,
		plan.SnapshotID,
		plan.CatalogRevision,
		plan.MutationRevision,
		plan.DiffHash,
		plan.TargetMode,
		plan.ExpiresAt,
	)
	return err
}

func (store *stateStore) snapshotRestorePlan(
	ctx context.Context,
	planID string,
) (snapshotRestorePlan, error) {
	var plan snapshotRestorePlan
	err := store.db.QueryRowContext(ctx, `
		SELECT plan_id, snapshot_id, catalog_revision, mutation_revision,
		       diff_hash, target_mode, expires_at
		FROM snapshot_restore_plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&plan.PlanID,
		&plan.SnapshotID,
		&plan.CatalogRevision,
		&plan.MutationRevision,
		&plan.DiffHash,
		&plan.TargetMode,
		&plan.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotRestorePlan{}, errors.New("restore.plan_not_found")
	}
	return plan, err
}

func (store *stateStore) deleteSnapshotRestorePlan(
	ctx context.Context,
	planID string,
) error {
	_, err := store.db.ExecContext(
		ctx,
		`DELETE FROM snapshot_restore_plans WHERE plan_id = ?`,
		planID,
	)
	return err
}

type snapshotExtractPlan struct {
	PlanID          string
	SnapshotID      string
	CatalogRevision uint64
	DocumentID      string
	RevisionID      string
	RelativePath    string
	ObjectID        string
	ContentHash     string
	ContentSize     int64
	ExpiresAt       string
}

func (store *stateStore) putSnapshotExtractPlan(
	ctx context.Context,
	plan snapshotExtractPlan,
) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO snapshot_extract_plans (
			plan_id, snapshot_id, catalog_revision, document_id, revision_id,
			relative_path, object_id, content_hash, content_size, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.PlanID,
		plan.SnapshotID,
		plan.CatalogRevision,
		plan.DocumentID,
		plan.RevisionID,
		plan.RelativePath,
		plan.ObjectID,
		plan.ContentHash,
		plan.ContentSize,
		plan.ExpiresAt,
	)
	return err
}

func (store *stateStore) snapshotExtractPlan(
	ctx context.Context,
	planID string,
) (snapshotExtractPlan, error) {
	var plan snapshotExtractPlan
	err := store.db.QueryRowContext(ctx, `
		SELECT plan_id, snapshot_id, catalog_revision, document_id, revision_id,
		       relative_path, object_id, content_hash, content_size, expires_at
		FROM snapshot_extract_plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&plan.PlanID,
		&plan.SnapshotID,
		&plan.CatalogRevision,
		&plan.DocumentID,
		&plan.RevisionID,
		&plan.RelativePath,
		&plan.ObjectID,
		&plan.ContentHash,
		&plan.ContentSize,
		&plan.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotExtractPlan{}, errors.New("snapshot.extract_plan_not_found")
	}
	return plan, err
}

func (store *stateStore) deleteSnapshotExtractPlan(
	ctx context.Context,
	planID string,
) error {
	_, err := store.db.ExecContext(
		ctx,
		`DELETE FROM snapshot_extract_plans WHERE plan_id = ?`,
		planID,
	)
	return err
}

func ensureRetentionColumn(db *sql.DB, name string, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(retention_policy)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			index      int
			columnName string
			columnType string
			notNull    int
			defaultSQL sql.NullString
			primaryKey int
		)
		if err := rows.Scan(
			&index,
			&columnName,
			&columnType,
			&notNull,
			&defaultSQL,
			&primaryKey,
		); err != nil {
			return err
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(
		`ALTER TABLE retention_policy ADD COLUMN ` + name + ` ` + definition,
	)
	return err
}

func ensureSnapshotRestoreDiffHashColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(snapshot_restore_plans)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			index      int
			columnName string
			columnType string
			notNull    int
			defaultSQL sql.NullString
			primaryKey int
		)
		if err := rows.Scan(
			&index,
			&columnName,
			&columnType,
			&notNull,
			&defaultSQL,
			&primaryKey,
		); err != nil {
			return err
		}
		if columnName == "diff_hash" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(
		`ALTER TABLE snapshot_restore_plans
		 ADD COLUMN diff_hash TEXT NOT NULL DEFAULT 'legacy-unbound'`,
	)
	return err
}

func ensureSnapshotImportBindingColumns(db *sql.DB) error {
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(snapshot_import_plans)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			index      int
			columnName string
			columnType string
			notNull    int
			defaultSQL sql.NullString
			primaryKey int
		)
		if err := rows.Scan(
			&index,
			&columnName,
			&columnType,
			&notNull,
			&defaultSQL,
			&primaryKey,
		); err != nil {
			_ = rows.Close()
			return err
		}
		columns[columnName] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if !columns["source_size"] {
		if _, err := db.Exec(`
			ALTER TABLE snapshot_import_plans
			ADD COLUMN source_size INTEGER NOT NULL DEFAULT -1
		`); err != nil {
			return err
		}
	}
	if !columns["source_identity"] {
		if _, err := db.Exec(`
			ALTER TABLE snapshot_import_plans
			ADD COLUMN source_identity TEXT NOT NULL DEFAULT ''
		`); err != nil {
			return err
		}
	}
	return nil
}

func (store *stateStore) sequence(
	ctx context.Context,
	workspaceID string,
	sessionEpoch uint64,
) (uint64, error) {
	var value uint64
	err := store.db.QueryRowContext(ctx, `
		SELECT high_watermark FROM rpc_sequence
		WHERE workspace_id = ? AND session_epoch = ?`,
		workspaceID,
		sessionEpoch,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return value, err
}

func (store *stateStore) commitSequence(
	ctx context.Context,
	workspaceID string,
	sessionEpoch uint64,
	sequence uint64,
) error {
	result, err := store.db.ExecContext(ctx, `
		INSERT INTO rpc_sequence (
			workspace_id, session_epoch, high_watermark
		) VALUES (?, ?, ?)
		ON CONFLICT(workspace_id, session_epoch) DO UPDATE
		SET high_watermark = excluded.high_watermark
		WHERE rpc_sequence.high_watermark < excluded.high_watermark`,
		workspaceID,
		sessionEpoch,
		sequence,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("workspace.sequence_stale")
	}
	return nil
}

func (store *stateStore) retention(ctx context.Context) (RetentionPolicy, uint64, error) {
	policy := defaultRetentionPolicy()
	var (
		limit            sql.NullInt64
		mutationRevision uint64
		snapshotBuckets  string
		fileBuckets      string
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT policy_revision, snapshot_days, snapshot_count,
		       snapshot_buckets_json, file_revision_days, file_revision_count,
		       file_revision_buckets_json,
		       repository_limit_bytes, mutation_revision
		FROM retention_policy WHERE singleton = 1`,
	).Scan(
		&policy.PolicyRevision,
		&policy.SnapshotDays,
		&policy.SnapshotCount,
		&snapshotBuckets,
		&policy.FileRevisionDays,
		&policy.FileRevisionCount,
		&fileBuckets,
		&limit,
		&mutationRevision,
	)
	if err != nil {
		return RetentionPolicy{}, 0, err
	}
	if json.Unmarshal([]byte(snapshotBuckets), &policy.SnapshotBuckets) != nil ||
		json.Unmarshal([]byte(fileBuckets), &policy.FileRevisionBuckets) != nil ||
		!validRetentionBuckets(policy.SnapshotBuckets) ||
		!validRetentionBuckets(policy.FileRevisionBuckets) {
		return RetentionPolicy{}, 0, errors.New("retention.policy_corrupt")
	}
	if limit.Valid {
		value := uint64(limit.Int64)
		policy.RepositoryLimitBytes = &value
	}
	return policy, mutationRevision, nil
}

func (store *stateStore) updateRetention(
	ctx context.Context,
	expectedRevision uint64,
	next RetentionPolicy,
	mutationRevision uint64,
) error {
	var limit any
	if next.RepositoryLimitBytes != nil {
		limit = *next.RepositoryLimitBytes
	}
	snapshotBuckets, err := json.Marshal(next.SnapshotBuckets)
	if err != nil {
		return err
	}
	fileBuckets, err := json.Marshal(next.FileRevisionBuckets)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE retention_policy
		SET policy_revision = ?, snapshot_days = ?, snapshot_count = ?,
		    snapshot_buckets_json = ?,
		    file_revision_days = ?, file_revision_count = ?,
		    file_revision_buckets_json = ?,
		    repository_limit_bytes = ?, mutation_revision = ?
		WHERE singleton = 1 AND policy_revision = ?`,
		next.PolicyRevision,
		next.SnapshotDays,
		next.SnapshotCount,
		string(snapshotBuckets),
		next.FileRevisionDays,
		next.FileRevisionCount,
		string(fileBuckets),
		limit,
		mutationRevision,
		expectedRevision,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("retention.policy_revision_stale")
	}
	return nil
}

func validRetentionBuckets(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		switch value {
		case "hourly", "daily", "weekly", "monthly":
		default:
			return false
		}
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func (store *stateStore) close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, closeErr)
}
