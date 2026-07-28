package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	_ "modernc.org/sqlite"
)

type storedPlan struct {
	PlanID    string
	Plan      CleanupPlan
	Policy    Policy
	ExpiresAt time.Time
	AppliedAt *time.Time
}

type Tombstone struct {
	ObjectID     objectrepo.ObjectID
	PlanID       string
	Size         int64
	TombstonedAt time.Time
	GraceUntil   time.Time
	MaintainedAt *time.Time
}

type IntegrityMode string

const (
	IntegrityIncremental IntegrityMode = "incremental"
	IntegrityFull        IntegrityMode = "full"
)

type IntegrityState struct {
	LastIncrementalAt    *time.Time
	LastFullAt           *time.Time
	LastSnapshotSequence uint64
	Status               string
	Failure              string
}

type QuotaState struct {
	UsageBytes               uint64
	LimitBytes               *uint64
	AutomaticSnapshotsPaused bool
	Warning                  string
	UpdatedAt                time.Time
}

type MaintenanceStage string

const (
	MaintenanceIntegrity MaintenanceStage = "integrity"
	MaintenanceSweep     MaintenanceStage = "sweep"
)

type MaintenanceState struct {
	Failure  string
	Stage    MaintenanceStage
	FailedAt *time.Time
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("retention.store_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS retention_plans (
			plan_id TEXT PRIMARY KEY,
			inventory_revision INTEGER NOT NULL,
			inventory_digest TEXT NOT NULL,
			plan_json BLOB NOT NULL,
			policy_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			applied_at TEXT
		);
		CREATE TABLE IF NOT EXISTS retention_tombstones (
			object_id TEXT PRIMARY KEY,
			plan_id TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			tombstoned_at TEXT NOT NULL,
			grace_until TEXT NOT NULL,
			maintained_at TEXT,
			FOREIGN KEY(plan_id) REFERENCES retention_plans(plan_id)
		);
		CREATE INDEX IF NOT EXISTS retention_tombstones_due
		ON retention_tombstones(maintained_at, grace_until, object_id);
		CREATE TABLE IF NOT EXISTS retention_snapshot_tombstones (
			snapshot_id TEXT PRIMARY KEY,
			plan_id TEXT NOT NULL,
			tombstoned_at TEXT NOT NULL,
			FOREIGN KEY(plan_id) REFERENCES retention_plans(plan_id)
		);
		CREATE TABLE IF NOT EXISTS retention_mutation_receipts (
			workspace_id TEXT NOT NULL,
			mutation_revision INTEGER NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			plan_id TEXT NOT NULL UNIQUE,
			PRIMARY KEY (
				workspace_id, mutation_revision, session_epoch,
				fence_epoch, claim_id
			),
			FOREIGN KEY(plan_id) REFERENCES retention_plans(plan_id)
		);
		CREATE TABLE IF NOT EXISTS retention_operation_receipts (
			workspace_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (workspace_id, operation_id)
		);
		CREATE TABLE IF NOT EXISTS retention_integrity_state (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			last_incremental_at TEXT,
			last_full_at TEXT,
			last_snapshot_sequence INTEGER NOT NULL,
			status TEXT NOT NULL CHECK(status IN (
				'unknown', 'verified', 'corrupt'
			)),
			failure TEXT NOT NULL
		);
		INSERT OR IGNORE INTO retention_integrity_state (
			singleton, last_incremental_at, last_full_at,
			last_snapshot_sequence, status, failure
		) VALUES (1, NULL, NULL, 0, 'unknown', '');
		CREATE TABLE IF NOT EXISTS retention_quota_state (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			usage_bytes INTEGER NOT NULL CHECK(usage_bytes >= 0),
			limit_bytes INTEGER CHECK(limit_bytes >= 0),
			automatic_snapshots_paused INTEGER NOT NULL CHECK(
				automatic_snapshots_paused IN (0, 1)
			),
			warning TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT OR IGNORE INTO retention_quota_state (
			singleton, usage_bytes, limit_bytes,
			automatic_snapshots_paused, warning, updated_at
		) VALUES (1, 0, NULL, 0, '', '1970-01-01T00:00:00Z');
		CREATE TABLE IF NOT EXISTS retention_maintenance_state (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			failure TEXT NOT NULL,
			stage TEXT NOT NULL CHECK(stage IN (
				'', 'integrity', 'sweep'
			)),
			failed_at TEXT
		);
		INSERT OR IGNORE INTO retention_maintenance_state (
			singleton, failure, stage, failed_at
		) VALUES (1, '', '', NULL);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize retention store: %w", err)
	}
	return &Store{db: db}, nil
}

func (store *Store) QuotaState(ctx context.Context) (QuotaState, error) {
	if store == nil || store.db == nil {
		return QuotaState{}, errors.New("retention.store_closed")
	}
	var (
		result  QuotaState
		usage   int64
		limit   sql.NullInt64
		paused  int
		updated string
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT usage_bytes, limit_bytes, automatic_snapshots_paused,
		       warning, updated_at
		FROM retention_quota_state WHERE singleton = 1`,
	).Scan(
		&usage,
		&limit,
		&paused,
		&result.Warning,
		&updated,
	)
	if err != nil || usage < 0 ||
		(limit.Valid && limit.Int64 < 0) ||
		(paused != 0 && paused != 1) {
		if err != nil {
			return QuotaState{}, err
		}
		return QuotaState{}, errors.New("retention.quota_state_corrupt")
	}
	at, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return QuotaState{}, errors.New("retention.quota_state_corrupt")
	}
	result.UsageBytes = uint64(usage)
	if limit.Valid {
		value := uint64(limit.Int64)
		result.LimitBytes = &value
	}
	result.AutomaticSnapshotsPaused = paused == 1
	result.UpdatedAt = at
	return result, nil
}

func (store *Store) RecordQuotaState(
	ctx context.Context,
	usage uint64,
	limit *uint64,
	paused bool,
	warning string,
	at time.Time,
) error {
	if store == nil || store.db == nil ||
		usage > math.MaxInt64 ||
		(limit != nil && *limit > math.MaxInt64) ||
		at.IsZero() {
		return errors.New("retention.quota_result_invalid")
	}
	const maximumWarningBytes = 4 << 10
	if len(warning) > maximumWarningBytes {
		warning = warning[:maximumWarningBytes]
	}
	var storedLimit any
	if limit != nil {
		storedLimit = int64(*limit)
	}
	pausedValue := 0
	if paused {
		pausedValue = 1
	}
	_, err := store.db.ExecContext(ctx, `
		UPDATE retention_quota_state
		SET usage_bytes = ?, limit_bytes = ?,
		    automatic_snapshots_paused = ?, warning = ?,
		    updated_at = ?
		WHERE singleton = 1`,
		int64(usage),
		storedLimit,
		pausedValue,
		warning,
		at.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (store *Store) IntegrityState(
	ctx context.Context,
) (IntegrityState, error) {
	if store == nil || store.db == nil {
		return IntegrityState{}, errors.New("retention.store_closed")
	}
	var (
		result         IntegrityState
		incrementalRaw sql.NullString
		fullRaw        sql.NullString
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT last_incremental_at, last_full_at,
		       last_snapshot_sequence, status, failure
		FROM retention_integrity_state WHERE singleton = 1`,
	).Scan(
		&incrementalRaw,
		&fullRaw,
		&result.LastSnapshotSequence,
		&result.Status,
		&result.Failure,
	)
	if err != nil {
		return IntegrityState{}, err
	}
	if incrementalRaw.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, incrementalRaw.String)
		if parseErr != nil {
			return IntegrityState{}, errors.New(
				"retention.integrity_state_corrupt",
			)
		}
		result.LastIncrementalAt = &value
	}
	if fullRaw.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, fullRaw.String)
		if parseErr != nil {
			return IntegrityState{}, errors.New(
				"retention.integrity_state_corrupt",
			)
		}
		result.LastFullAt = &value
	}
	return result, nil
}

func (store *Store) IntegrityDue(
	ctx context.Context,
	now time.Time,
) (IntegrityMode, bool, error) {
	state, err := store.IntegrityState(ctx)
	if err != nil {
		return "", false, err
	}
	now = now.UTC()
	if state.LastFullAt == nil ||
		!now.Before(state.LastFullAt.AddDate(0, 1, 0)) {
		return IntegrityFull, true, nil
	}
	if state.LastIncrementalAt == nil ||
		!now.Before(state.LastIncrementalAt.Add(24*time.Hour)) {
		return IntegrityIncremental, true, nil
	}
	return "", false, nil
}

func (store *Store) RecordIntegritySuccess(
	ctx context.Context,
	mode IntegrityMode,
	snapshotSequence uint64,
	at time.Time,
) error {
	if store == nil || store.db == nil ||
		(mode != IntegrityIncremental && mode != IntegrityFull) {
		return errors.New("retention.integrity_result_invalid")
	}
	atRaw := at.UTC().Format(time.RFC3339Nano)
	fullRaw := any(nil)
	if mode == IntegrityFull {
		fullRaw = atRaw
	}
	_, err := store.db.ExecContext(ctx, `
		UPDATE retention_integrity_state
		SET last_incremental_at = ?,
		    last_full_at = COALESCE(?, last_full_at),
		    last_snapshot_sequence = MAX(
				last_snapshot_sequence, ?
			),
		    status = 'verified',
		    failure = ''
		WHERE singleton = 1`,
		atRaw,
		fullRaw,
		snapshotSequence,
	)
	return err
}

func (store *Store) RecordIntegrityFailure(
	ctx context.Context,
	mode IntegrityMode,
	failure string,
) error {
	if store == nil || store.db == nil ||
		(mode != IntegrityIncremental && mode != IntegrityFull) ||
		failure == "" {
		return errors.New("retention.integrity_result_invalid")
	}
	const maximumFailureBytes = 4 << 10
	if len(failure) > maximumFailureBytes {
		failure = failure[:maximumFailureBytes]
	}
	_, err := store.db.ExecContext(ctx, `
		UPDATE retention_integrity_state
		SET status = 'corrupt', failure = ?
		WHERE singleton = 1`,
		failure,
	)
	return err
}

func (store *Store) EnsureIntegrityHealthy(ctx context.Context) error {
	state, err := store.IntegrityState(ctx)
	if err != nil {
		return err
	}
	if state.Status == "corrupt" {
		return errors.New("retention.integrity_corrupt")
	}
	return nil
}

func (store *Store) MaintenanceState(
	ctx context.Context,
) (MaintenanceState, error) {
	if store == nil || store.db == nil {
		return MaintenanceState{}, errors.New("retention.store_closed")
	}
	var (
		result      MaintenanceState
		stage       string
		failedAtRaw sql.NullString
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT failure, stage, failed_at
		FROM retention_maintenance_state WHERE singleton = 1`,
	).Scan(&result.Failure, &stage, &failedAtRaw)
	if err != nil {
		return MaintenanceState{}, err
	}
	result.Stage = MaintenanceStage(stage)
	if result.Failure == "" {
		if result.Stage != "" || failedAtRaw.Valid {
			return MaintenanceState{},
				errors.New("retention.maintenance_state_corrupt")
		}
		return result, nil
	}
	if (result.Stage != MaintenanceIntegrity &&
		result.Stage != MaintenanceSweep) ||
		!failedAtRaw.Valid {
		return MaintenanceState{},
			errors.New("retention.maintenance_state_corrupt")
	}
	failedAt, parseErr := time.Parse(time.RFC3339Nano, failedAtRaw.String)
	if parseErr != nil {
		return MaintenanceState{},
			errors.New("retention.maintenance_state_corrupt")
	}
	result.FailedAt = &failedAt
	return result, nil
}

func (store *Store) RecordMaintenanceFailure(
	ctx context.Context,
	stage MaintenanceStage,
	failure string,
	at time.Time,
) error {
	if store == nil || store.db == nil ||
		(stage != MaintenanceIntegrity && stage != MaintenanceSweep) ||
		failure == "" ||
		at.IsZero() {
		return errors.New("retention.maintenance_result_invalid")
	}
	const maximumFailureBytes = 4 << 10
	if len(failure) > maximumFailureBytes {
		failure = failure[:maximumFailureBytes]
	}
	_, err := store.db.ExecContext(ctx, `
		UPDATE retention_maintenance_state
		SET failure = ?, stage = ?, failed_at = ?
		WHERE singleton = 1`,
		failure,
		string(stage),
		at.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (store *Store) RecordMaintenanceSuccess(ctx context.Context) error {
	if store == nil || store.db == nil {
		return errors.New("retention.store_closed")
	}
	_, err := store.db.ExecContext(ctx, `
		UPDATE retention_maintenance_state
		SET failure = '', stage = '', failed_at = NULL
		WHERE singleton = 1`,
	)
	return err
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, closeErr)
}

func (store *Store) putPlan(
	ctx context.Context,
	planID string,
	plan CleanupPlan,
	policy Policy,
	expiresAt time.Time,
	receipts ...protocolv2.OperationReceipt,
) error {
	if store == nil || store.db == nil {
		return errors.New("retention.store_closed")
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if len(receipts) > 1 {
		return errors.New("retention.operation_receipt_invalid")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO retention_plans (
			plan_id, inventory_revision, inventory_digest, plan_json,
			policy_json, created_at, expires_at, applied_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		planID,
		plan.InventoryRevision,
		plan.InventoryDigest,
		planJSON,
		policyJSON,
		plan.CreatedAt.Format(time.RFC3339Nano),
		expiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	if len(receipts) == 1 {
		if err := putOperationReceipt(
			ctx,
			transaction,
			receipts[0],
			expiresAt.Add(-defaultPlanTTL),
		); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (store *Store) plan(
	ctx context.Context,
	planID string,
) (storedPlan, error) {
	if store == nil || store.db == nil {
		return storedPlan{}, errors.New("retention.store_closed")
	}
	var (
		result     storedPlan
		planJSON   []byte
		policyJSON []byte
		expiresAt  string
		appliedAt  sql.NullString
		revision   uint64
		digest     string
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT plan_id, inventory_revision, inventory_digest, plan_json,
		       policy_json, expires_at, applied_at
		FROM retention_plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&result.PlanID,
		&revision,
		&digest,
		&planJSON,
		&policyJSON,
		&expiresAt,
		&appliedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedPlan{}, errors.New("retention.plan_not_found")
	}
	if err != nil {
		return storedPlan{}, err
	}
	if err := json.Unmarshal(planJSON, &result.Plan); err != nil {
		return storedPlan{}, errors.Join(
			errors.New("retention.plan_corrupt"), err,
		)
	}
	if err := json.Unmarshal(policyJSON, &result.Policy); err != nil {
		return storedPlan{}, errors.Join(
			errors.New("retention.plan_corrupt"), err,
		)
	}
	result.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil ||
		result.Plan.InventoryRevision != revision ||
		result.Plan.InventoryDigest != digest {
		return storedPlan{}, errors.New("retention.plan_corrupt")
	}
	if appliedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, appliedAt.String)
		if parseErr != nil {
			return storedPlan{}, errors.New("retention.plan_corrupt")
		}
		result.AppliedAt = &value
	}
	return result, nil
}

func (store *Store) applyPlan(
	ctx context.Context,
	planID string,
	plan CleanupPlan,
	inventory Inventory,
	appliedAt time.Time,
	mutation *MutationIdentity,
	receipt *protocolv2.OperationReceipt,
) error {
	if store == nil || store.db == nil {
		return errors.New("retention.store_closed")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
		UPDATE retention_plans SET applied_at = ?
		WHERE plan_id = ? AND applied_at IS NULL`,
		appliedAt.UTC().Format(time.RFC3339Nano),
		planID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("retention.plan_already_applied")
	}
	for _, id := range plan.Tombstone {
		node, ok := inventory.Nodes[id]
		if !ok || node.Size < 0 {
			return ErrInventoryChanged
		}
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO retention_tombstones (
				object_id, plan_id, size_bytes, tombstoned_at,
				grace_until, maintained_at
			) VALUES (?, ?, ?, ?, ?, NULL)`,
			string(id),
			planID,
			node.Size,
			appliedAt.UTC().Format(time.RFC3339Nano),
			appliedAt.UTC().Add(plan.Grace).Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
	}
	retainedSnapshots := make(
		map[string]struct{},
		len(plan.RetainedSnapshots),
	)
	for _, snapshotID := range plan.RetainedSnapshots {
		retainedSnapshots[snapshotID] = struct{}{}
	}
	tombstonedObjects := make(
		map[objectrepo.ObjectID]struct{},
		len(plan.Tombstone),
	)
	for _, id := range plan.Tombstone {
		tombstonedObjects[id] = struct{}{}
	}
	for _, snapshot := range inventory.Snapshots {
		if _, retained := retainedSnapshots[snapshot.SnapshotID]; retained {
			continue
		}
		if _, tombstoned := tombstonedObjects[snapshot.Root]; !tombstoned {
			continue
		}
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO retention_snapshot_tombstones (
				snapshot_id, plan_id, tombstoned_at
			) VALUES (?, ?, ?)`,
			snapshot.SnapshotID,
			planID,
			appliedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
	}
	if mutation != nil {
		if err := validateMutationIdentity(*mutation); err != nil {
			return err
		}
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO retention_mutation_receipts (
				workspace_id, mutation_revision, session_epoch,
				fence_epoch, claim_id, plan_id
			) VALUES (?, ?, ?, ?, ?, ?)`,
			mutation.WorkspaceID,
			mutation.MutationRevision,
			mutation.SessionEpoch,
			mutation.FenceEpoch,
			mutation.ClaimID,
			planID,
		)
		if err != nil {
			return err
		}
	}
	if receipt != nil {
		if err := putOperationReceipt(
			ctx,
			transaction,
			*receipt,
			appliedAt,
		); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (store *Store) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	if store == nil || store.db == nil {
		return protocolv2.OperationReceipt{}, false,
			errors.New("retention.store_closed")
	}
	var (
		receipt protocolv2.OperationReceipt
		scope   string
		raw     []byte
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, workspace_id, method, scope, request_hash,
		       result_json
		FROM retention_operation_receipts
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
	if err := validateOperationReceipt(receipt); err != nil ||
		receipt.WorkspaceID != workspaceID ||
		receipt.OperationID != operationID {
		return protocolv2.OperationReceipt{}, false,
			errors.New("retention.operation_receipt_corrupt")
	}
	return receipt, true, nil
}

func putOperationReceipt(
	ctx context.Context,
	transaction *sql.Tx,
	receipt protocolv2.OperationReceipt,
	createdAt time.Time,
) error {
	if err := validateOperationReceipt(receipt); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		INSERT INTO retention_operation_receipts (
			workspace_id, operation_id, method, scope, request_hash,
			result_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, operation_id) DO NOTHING`,
		receipt.WorkspaceID,
		receipt.OperationID,
		receipt.Method,
		string(receipt.Scope),
		receipt.RequestHash,
		[]byte(receipt.Result),
		createdAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return protocolv2.ErrOperationConflict
	}
	return nil
}

func validateOperationReceipt(receipt protocolv2.OperationReceipt) error {
	if receipt.OperationID == "" ||
		receipt.WorkspaceID == "" ||
		receipt.Method == "" ||
		(receipt.Scope != protocolv2.GlobalScope &&
			receipt.Scope != protocolv2.WorkspaceScope) ||
		receipt.RequestHash == "" ||
		len(receipt.Result) == 0 ||
		len(receipt.Result) > 16<<20 ||
		!json.Valid(receipt.Result) {
		return errors.New("retention.operation_receipt_invalid")
	}
	return nil
}

func (store *Store) TombstonedSnapshotIDs(
	ctx context.Context,
) (map[string]struct{}, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("retention.store_closed")
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT snapshot_id FROM retention_snapshot_tombstones
		ORDER BY snapshot_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var snapshotID string
		if err := rows.Scan(&snapshotID); err != nil {
			return nil, err
		}
		if snapshotID == "" {
			return nil, errors.New("retention.snapshot_tombstone_corrupt")
		}
		result[snapshotID] = struct{}{}
	}
	return result, rows.Err()
}

// AllTombstones exposes the durable logical state to production inventory
// composition. Completed repository retirement receipts are only trusted when
// they match one of these authoritative tombstones exactly.
func (store *Store) AllTombstones(
	ctx context.Context,
) ([]Tombstone, error) {
	return store.tombstones(ctx, true)
}

func (store *Store) HasCommittedMutation(
	ctx context.Context,
	identity MutationIdentity,
) (bool, error) {
	if store == nil || store.db == nil {
		return false, errors.New("retention.store_closed")
	}
	if err := validateMutationIdentity(identity); err != nil {
		return false, err
	}
	var count uint64
	err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM retention_mutation_receipts
		WHERE workspace_id = ? AND mutation_revision = ?
		      AND session_epoch = ? AND fence_epoch = ? AND claim_id = ?`,
		identity.WorkspaceID,
		identity.MutationRevision,
		identity.SessionEpoch,
		identity.FenceEpoch,
		identity.ClaimID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 1 {
		return false, errors.New("retention.mutation_receipt_ambiguous")
	}
	return count == 1, nil
}

func (store *Store) tombstones(
	ctx context.Context,
	includeMaintained bool,
) ([]Tombstone, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("retention.store_closed")
	}
	query := `
		SELECT object_id, plan_id, size_bytes, tombstoned_at,
		       grace_until, maintained_at
		FROM retention_tombstones`
	if !includeMaintained {
		query += ` WHERE maintained_at IS NULL`
	}
	query += ` ORDER BY object_id`
	rows, err := store.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Tombstone
	for rows.Next() {
		var (
			item         Tombstone
			id           string
			tombstonedAt string
			graceUntil   string
			maintainedAt sql.NullString
		)
		if err := rows.Scan(
			&id,
			&item.PlanID,
			&item.Size,
			&tombstonedAt,
			&graceUntil,
			&maintainedAt,
		); err != nil {
			return nil, err
		}
		item.ObjectID = objectrepo.ObjectID(id)
		item.TombstonedAt, err = time.Parse(
			time.RFC3339Nano,
			tombstonedAt,
		)
		if err != nil {
			return nil, errors.New("retention.tombstone_corrupt")
		}
		item.GraceUntil, err = time.Parse(time.RFC3339Nano, graceUntil)
		if err != nil {
			return nil, errors.New("retention.tombstone_corrupt")
		}
		if maintainedAt.Valid {
			value, parseErr := time.Parse(
				time.RFC3339Nano,
				maintainedAt.String,
			)
			if parseErr != nil {
				return nil, errors.New("retention.tombstone_corrupt")
			}
			item.MaintainedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) markMaintained(
	ctx context.Context,
	ids []objectrepo.ObjectID,
	at time.Time,
) error {
	if len(ids) == 0 {
		return nil
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, id := range ids {
		result, err := transaction.ExecContext(ctx, `
			UPDATE retention_tombstones SET maintained_at = ?
			WHERE object_id = ? AND maintained_at IS NULL`,
			at.UTC().Format(time.RFC3339Nano),
			string(id),
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return errors.New("retention.tombstone_state_changed")
		}
	}
	return transaction.Commit()
}
