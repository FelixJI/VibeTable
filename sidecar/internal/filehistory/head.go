package filehistory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	_ "modernc.org/sqlite"
)

var ErrHeadConflict = errors.New("filehistory.head_conflict")

type CurrentHead struct {
	WorkspaceID      string
	Root             objectrepo.ManifestID
	Revision         uint64
	MutationRevision uint64
	SessionEpoch     uint64
	FenceEpoch       uint64
	ClaimID          string
}

// HeadStore is the durable publication boundary for the current topology
// root. CompareAndSwap must never overwrite a head that differs from expected.
type HeadStore interface {
	Load(context.Context, string) (CurrentHead, bool, error)
	CompareAndSwap(
		context.Context,
		CurrentHead,
		CurrentHead,
	) (CurrentHead, error)
}

type AuditedHeadStore interface {
	HeadStore
	CompareAndSwapWithAudit(
		context.Context,
		CurrentHead,
		CurrentHead,
		auditledger.Envelope,
	) (CurrentHead, error)
	auditledger.OutboxStore
}

// OperationReceiptHeadStore publishes a topology head, its audit outbox item,
// and the exact RPC receipt in one SQLite transaction.
type OperationReceiptHeadStore interface {
	AuditedHeadStore
	CompareAndSwapWithAuditAndReceipt(
		context.Context,
		CurrentHead,
		CurrentHead,
		auditledger.Envelope,
		protocolv2.OperationReceipt,
	) (CurrentHead, error)
	LoadOperationReceipt(
		context.Context,
		string,
		string,
	) (protocolv2.OperationReceipt, bool, error)
}

type memoryHeadStore struct {
	mu       sync.Mutex
	heads    map[string]CurrentHead
	receipts map[string]protocolv2.OperationReceipt
}

func newMemoryHeadStore() *memoryHeadStore {
	return &memoryHeadStore{
		heads:    map[string]CurrentHead{},
		receipts: map[string]protocolv2.OperationReceipt{},
	}
}

func (store *memoryHeadStore) Load(
	_ context.Context,
	workspaceID string,
) (CurrentHead, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	head, found := store.heads[workspaceID]
	return head, found, nil
}

func (store *memoryHeadStore) CompareAndSwap(
	_ context.Context,
	expected CurrentHead,
	next CurrentHead,
) (CurrentHead, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateHeadTransition(expected, next); err != nil {
		return CurrentHead{}, ErrStateCorrupt
	}
	current, found := store.heads[expected.WorkspaceID]
	if found != (expected.Revision != 0) ||
		(found && current != expected) {
		return CurrentHead{}, ErrHeadConflict
	}
	store.heads[expected.WorkspaceID] = next
	return next, nil
}

type SQLiteHeadStore struct {
	db *sql.DB
}

func OpenPersistentHeadStore(path string) (*SQLiteHeadStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("filehistory.head_path_required")
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
		CREATE TABLE IF NOT EXISTS filehistory_heads (
			workspace_id TEXT PRIMARY KEY,
			root_manifest_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			mutation_revision INTEGER NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS filehistory_audit_outbox (
			event_id TEXT PRIMARY KEY,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pending', 'drained')),
			UNIQUE(source_epoch, source_sequence)
		);
		CREATE TABLE IF NOT EXISTS filehistory_operation_receipts (
			workspace_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, operation_id)
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteHeadStore{db: db}, nil
}

func (store *SQLiteHeadStore) Load(
	ctx context.Context,
	workspaceID string,
) (CurrentHead, bool, error) {
	if store == nil || store.db == nil {
		return CurrentHead{}, false, errors.New("filehistory.head_store_closed")
	}
	if !validUUID(workspaceID) {
		return CurrentHead{}, false, ErrStateCorrupt
	}
	var head CurrentHead
	err := store.db.QueryRowContext(ctx, `
		SELECT workspace_id, root_manifest_id, revision, mutation_revision,
		       session_epoch, fence_epoch, claim_id
		FROM filehistory_heads WHERE workspace_id = ?`,
		workspaceID,
	).Scan(
		&head.WorkspaceID,
		&head.Root,
		&head.Revision,
		&head.MutationRevision,
		&head.SessionEpoch,
		&head.FenceEpoch,
		&head.ClaimID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CurrentHead{}, false, nil
	}
	if err != nil {
		return CurrentHead{}, false, err
	}
	if head.WorkspaceID != workspaceID ||
		head.Root == "" ||
		head.Revision == 0 ||
		head.MutationRevision == 0 ||
		head.SessionEpoch == 0 ||
		head.FenceEpoch == 0 ||
		strings.TrimSpace(head.ClaimID) == "" {
		return CurrentHead{}, false, ErrStateCorrupt
	}
	return head, true, nil
}

func (store *SQLiteHeadStore) CompareAndSwap(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
) (CurrentHead, error) {
	return store.compareAndSwap(ctx, expected, next, nil)
}

func (store *SQLiteHeadStore) CompareAndSwapWithAudit(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
	envelope auditledger.Envelope,
) (CurrentHead, error) {
	return store.compareAndSwap(ctx, expected, next, &envelope)
}

func (store *SQLiteHeadStore) CompareAndSwapWithAuditAndReceipt(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
	envelope auditledger.Envelope,
	receipt protocolv2.OperationReceipt,
) (CurrentHead, error) {
	return store.compareAndSwapWithReceipt(
		ctx,
		expected,
		next,
		&envelope,
		&receipt,
	)
}

func (store *SQLiteHeadStore) compareAndSwap(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
	envelope *auditledger.Envelope,
) (CurrentHead, error) {
	return store.compareAndSwapWithReceipt(
		ctx,
		expected,
		next,
		envelope,
		nil,
	)
}

func (store *SQLiteHeadStore) compareAndSwapWithReceipt(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
	envelope *auditledger.Envelope,
	receipt *protocolv2.OperationReceipt,
) (CurrentHead, error) {
	if store == nil || store.db == nil {
		return CurrentHead{}, errors.New("filehistory.head_store_closed")
	}
	if err := validateHeadTransition(expected, next); err != nil {
		return CurrentHead{}, ErrStateCorrupt
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return CurrentHead{}, err
	}
	defer transaction.Rollback()
	var (
		result  sql.Result
		execErr error
	)
	if expected.Revision == 0 {
		if expected.Root != "" ||
			expected.MutationRevision != 0 ||
			expected.SessionEpoch != 0 ||
			expected.FenceEpoch != 0 ||
			expected.ClaimID != "" {
			return CurrentHead{}, ErrHeadConflict
		}
		result, execErr = transaction.ExecContext(ctx, `
			INSERT INTO filehistory_heads (
				workspace_id, root_manifest_id, revision, mutation_revision,
				session_epoch, fence_epoch, claim_id
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id) DO NOTHING`,
			next.WorkspaceID,
			next.Root,
			next.Revision,
			next.MutationRevision,
			next.SessionEpoch,
			next.FenceEpoch,
			next.ClaimID,
		)
	} else {
		result, execErr = transaction.ExecContext(ctx, `
			UPDATE filehistory_heads
			SET root_manifest_id = ?, revision = ?, mutation_revision = ?,
			    session_epoch = ?, fence_epoch = ?, claim_id = ?
			WHERE workspace_id = ? AND root_manifest_id = ?
			      AND revision = ? AND mutation_revision = ?
			      AND session_epoch = ? AND fence_epoch = ?
			      AND claim_id = ?`,
			next.Root,
			next.Revision,
			next.MutationRevision,
			next.SessionEpoch,
			next.FenceEpoch,
			next.ClaimID,
			expected.WorkspaceID,
			expected.Root,
			expected.Revision,
			expected.MutationRevision,
			expected.SessionEpoch,
			expected.FenceEpoch,
			expected.ClaimID,
		)
	}
	if execErr != nil {
		return CurrentHead{}, execErr
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return CurrentHead{}, err
	}
	if affected != 1 {
		return CurrentHead{}, ErrHeadConflict
	}
	if envelope != nil {
		normalized, err := auditledger.NewEnvelope(
			envelope.EventID,
			envelope.SourceEpoch,
			envelope.SourceSequence,
			envelope.MutationIdentity,
			envelope.Payload,
			envelope.OccurredAt,
		)
		if err != nil || normalized.PayloadHash != envelope.PayloadHash {
			return CurrentHead{}, errors.Join(
				auditledger.ErrPayloadMismatch, err,
			)
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO filehistory_audit_outbox (
				event_id, source_epoch, source_sequence, mutation_identity,
				payload_hash, payload_json, occurred_at, status
			) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending')`,
			normalized.EventID,
			normalized.SourceEpoch,
			normalized.SourceSequence,
			normalized.MutationIdentity,
			normalized.PayloadHash,
			[]byte(normalized.Payload),
			normalized.OccurredAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return CurrentHead{}, err
		}
	}
	if receipt != nil {
		if err := validateOperationReceipt(
			*receipt,
			next.WorkspaceID,
		); err != nil {
			return CurrentHead{}, err
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO filehistory_operation_receipts (
				workspace_id, operation_id, method, scope,
				request_hash, result_json
			) VALUES (?, ?, ?, ?, ?, ?)`,
			receipt.WorkspaceID,
			receipt.OperationID,
			receipt.Method,
			string(receipt.Scope),
			receipt.RequestHash,
			[]byte(receipt.Result),
		)
		if err != nil {
			return CurrentHead{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return CurrentHead{}, err
	}
	return next, nil
}

func (store *SQLiteHeadStore) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	if store == nil || store.db == nil {
		return protocolv2.OperationReceipt{}, false,
			errors.New("filehistory.head_store_closed")
	}
	var (
		receipt protocolv2.OperationReceipt
		scope   string
		raw     []byte
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT workspace_id, operation_id, method, scope,
		       request_hash, result_json
		FROM filehistory_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID,
		operationID,
	).Scan(
		&receipt.WorkspaceID,
		&receipt.OperationID,
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
	receipt.Result = append([]byte(nil), raw...)
	if err := validateOperationReceipt(receipt, workspaceID); err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	return receipt, true, nil
}

func validateOperationReceipt(
	receipt protocolv2.OperationReceipt,
	workspaceID string,
) error {
	if receipt.WorkspaceID != workspaceID ||
		receipt.OperationID == "" ||
		receipt.Method == "" ||
		(receipt.Scope != protocolv2.WorkspaceScope &&
			receipt.Scope != protocolv2.GlobalScope) ||
		receipt.RequestHash == "" ||
		!json.Valid(receipt.Result) {
		return errors.New("filehistory.operation_receipt_invalid")
	}
	return nil
}

func (store *SQLiteHeadStore) Pending(
	ctx context.Context,
	limit int,
) ([]auditledger.Envelope, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("filehistory.head_store_closed")
	}
	if limit <= 0 {
		return nil, errors.New("audit.outbox_limit_invalid")
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT event_id, source_epoch, source_sequence, mutation_identity,
		       payload_hash, payload_json, occurred_at
		FROM filehistory_audit_outbox
		WHERE status = 'pending'
		ORDER BY source_sequence
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []auditledger.Envelope
	for rows.Next() {
		var (
			envelope auditledger.Envelope
			payload  []byte
			occurred string
		)
		if err := rows.Scan(
			&envelope.EventID,
			&envelope.SourceEpoch,
			&envelope.SourceSequence,
			&envelope.MutationIdentity,
			&envelope.PayloadHash,
			&payload,
			&occurred,
		); err != nil {
			return nil, err
		}
		envelope.Payload = append([]byte(nil), payload...)
		envelope.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, ErrStateCorrupt
		}
		normalized, err := auditledger.NewEnvelope(
			envelope.EventID,
			envelope.SourceEpoch,
			envelope.SourceSequence,
			envelope.MutationIdentity,
			envelope.Payload,
			envelope.OccurredAt,
		)
		if err != nil || normalized.PayloadHash != envelope.PayloadHash {
			return nil, errors.Join(ErrStateCorrupt, err)
		}
		result = append(result, normalized)
	}
	return result, rows.Err()
}

func (store *SQLiteHeadStore) Acknowledge(
	ctx context.Context,
	eventID string,
	sourceEpoch string,
	sourceSequence uint64,
) error {
	if store == nil || store.db == nil {
		return errors.New("filehistory.head_store_closed")
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE filehistory_audit_outbox SET status = 'drained'
		WHERE event_id = ? AND source_epoch = ? AND source_sequence = ?
		      AND status = 'pending'`,
		eventID,
		sourceEpoch,
		sourceSequence,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(auditledger.ErrSourceConflict, err)
	}
	return nil
}

func validateHeadTransition(expected, next CurrentHead) error {
	if !validUUID(expected.WorkspaceID) ||
		expected.Revision == math.MaxUint64 ||
		next.WorkspaceID != expected.WorkspaceID ||
		next.Root == "" ||
		next.Revision != expected.Revision+1 ||
		next.MutationRevision == 0 ||
		next.SessionEpoch == 0 ||
		next.FenceEpoch == 0 ||
		strings.TrimSpace(next.ClaimID) == "" {
		return ErrStateCorrupt
	}
	if expected.Revision == 0 {
		if expected.Root != "" ||
			expected.MutationRevision != 0 ||
			expected.SessionEpoch != 0 ||
			expected.FenceEpoch != 0 ||
			expected.ClaimID != "" {
			return ErrStateCorrupt
		}
		return nil
	}
	if expected.Root == "" ||
		expected.MutationRevision == 0 ||
		expected.SessionEpoch == 0 ||
		expected.FenceEpoch == 0 ||
		strings.TrimSpace(expected.ClaimID) == "" {
		return ErrStateCorrupt
	}
	return nil
}

func (store *SQLiteHeadStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	_, checkpointErr := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := store.db.Close()
	store.db = nil
	return errors.Join(checkpointErr, closeErr)
}
