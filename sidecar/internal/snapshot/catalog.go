package snapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	_ "modernc.org/sqlite"
)

type DurableCatalog struct {
	db *sql.DB
}

func OpenDurableCatalog(path string) (*DurableCatalog, error) {
	if path == "" {
		return nil, errors.New("snapshot.catalog_path_required")
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
		CREATE TABLE IF NOT EXISTS snapshot_catalog (
			snapshot_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			snapshot_sequence INTEGER NOT NULL,
			mutation_revision INTEGER NOT NULL,
			catalog_revision INTEGER NOT NULL,
			catalog_mutation_revision INTEGER NOT NULL,
			catalog_session_epoch INTEGER NOT NULL,
			catalog_fence_epoch INTEGER NOT NULL,
			catalog_claim_id TEXT NOT NULL,
			record_json BLOB NOT NULL,
			UNIQUE(workspace_id, snapshot_sequence)
		);
		CREATE INDEX IF NOT EXISTS snapshot_catalog_workspace
		ON snapshot_catalog(workspace_id, snapshot_sequence);
		CREATE TABLE IF NOT EXISTS snapshot_operation_receipts (
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
		return nil, fmt.Errorf("initialize snapshot catalog: %w", err)
	}
	return &DurableCatalog{db: db}, nil
}

func (catalog *DurableCatalog) Close() error {
	if catalog == nil || catalog.db == nil {
		return nil
	}
	_, checkpointErr := catalog.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_, journalErr := catalog.db.Exec("PRAGMA journal_mode=DELETE")
	closeErr := catalog.db.Close()
	catalog.db = nil
	return errors.Join(checkpointErr, journalErr, closeErr)
}

func (catalog *DurableCatalog) Publish(ctx context.Context, record Record) error {
	return catalog.publish(ctx, record, nil)
}

func (catalog *DurableCatalog) PublishWithOperationReceipt(
	ctx context.Context,
	record Record,
	receipt protocolv2.OperationReceipt,
) error {
	return catalog.publish(ctx, record, &receipt)
}

func (catalog *DurableCatalog) publish(
	ctx context.Context,
	record Record,
	receipt *protocolv2.OperationReceipt,
) error {
	if catalog == nil || catalog.db == nil {
		return errors.New("snapshot.catalog_closed")
	}
	if record.CatalogRevision == 0 {
		record.CatalogRevision = record.SnapshotSequence
	}
	if err := validateCatalogRecord(record); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	transaction, err := catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO snapshot_catalog (
			snapshot_id, workspace_id, snapshot_sequence,
			mutation_revision, catalog_revision, catalog_mutation_revision,
			catalog_session_epoch, catalog_fence_epoch, catalog_claim_id,
			record_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SnapshotID,
		record.WorkspaceID,
		record.SnapshotSequence,
		record.MutationRevision,
		record.CatalogRevision,
		record.CatalogMutationRevision,
		record.CatalogSessionEpoch,
		record.CatalogFenceEpoch,
		record.CatalogClaimID,
		raw,
	); err != nil {
		return err
	}
	if receipt != nil {
		if err := validateOperationReceipt(*receipt, record.WorkspaceID); err != nil {
			return err
		}
		if err := insertOperationReceipt(
			ctx,
			transaction,
			*receipt,
		); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (catalog *DurableCatalog) UpdatePinned(
	ctx context.Context,
	workspaceID string,
	snapshotID string,
	expectedCatalogRevision uint64,
	pinned bool,
	mutationRevision uint64,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
) (Record, error) {
	return catalog.UpdatePinnedWithRootPin(
		ctx,
		workspaceID,
		snapshotID,
		expectedCatalogRevision,
		pinned,
		"",
		mutationRevision,
		sessionEpoch,
		fenceEpoch,
		claimID,
	)
}

func (catalog *DurableCatalog) UpdatePinnedWithRootPin(
	ctx context.Context,
	workspaceID string,
	snapshotID string,
	expectedCatalogRevision uint64,
	pinned bool,
	replacementRootPinID string,
	mutationRevision uint64,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
) (Record, error) {
	return catalog.updatePinnedWithRootPin(
		ctx,
		workspaceID,
		snapshotID,
		expectedCatalogRevision,
		pinned,
		replacementRootPinID,
		mutationRevision,
		sessionEpoch,
		fenceEpoch,
		claimID,
		nil,
	)
}

func (catalog *DurableCatalog) UpdatePinnedWithRootPinAndOperationReceipt(
	ctx context.Context,
	workspaceID string,
	snapshotID string,
	expectedCatalogRevision uint64,
	pinned bool,
	replacementRootPinID string,
	mutationRevision uint64,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
	receipt protocolv2.OperationReceipt,
) (Record, error) {
	return catalog.updatePinnedWithRootPin(
		ctx,
		workspaceID,
		snapshotID,
		expectedCatalogRevision,
		pinned,
		replacementRootPinID,
		mutationRevision,
		sessionEpoch,
		fenceEpoch,
		claimID,
		&receipt,
	)
}

func (catalog *DurableCatalog) updatePinnedWithRootPin(
	ctx context.Context,
	workspaceID string,
	snapshotID string,
	expectedCatalogRevision uint64,
	pinned bool,
	replacementRootPinID string,
	mutationRevision uint64,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
	receipt *protocolv2.OperationReceipt,
) (Record, error) {
	if catalog == nil || catalog.db == nil {
		return Record{}, errors.New("snapshot.catalog_closed")
	}
	if workspaceID == "" || snapshotID == "" ||
		expectedCatalogRevision == 0 ||
		mutationRevision == 0 ||
		sessionEpoch == 0 ||
		fenceEpoch == 0 ||
		claimID == "" {
		return Record{}, errors.New("snapshot.catalog_update_invalid")
	}
	transaction, err := catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer transaction.Rollback()
	var raw []byte
	err = transaction.QueryRowContext(ctx, `
		SELECT record_json FROM snapshot_catalog
		WHERE workspace_id = ? AND snapshot_id = ?`,
		workspaceID,
		snapshotID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errors.New("snapshot.not_found")
	}
	if err != nil {
		return Record{}, err
	}
	record, err := decodeCatalogRecord(raw)
	if err != nil {
		return Record{}, err
	}
	if record.CatalogRevision != expectedCatalogRevision {
		return Record{}, errors.New("snapshot.catalog_revision_stale")
	}
	if record.CatalogRevision == ^uint64(0) {
		return Record{}, errors.New("snapshot.catalog_revision_exhausted")
	}
	record.Pinned = pinned
	if replacementRootPinID != "" {
		record.RootPinID = replacementRootPinID
	}
	record.CatalogRevision++
	record.CatalogMutationRevision = mutationRevision
	record.CatalogSessionEpoch = sessionEpoch
	record.CatalogFenceEpoch = fenceEpoch
	record.CatalogClaimID = claimID
	raw, err = json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE snapshot_catalog
		SET catalog_revision = ?, catalog_mutation_revision = ?,
		    catalog_session_epoch = ?, catalog_fence_epoch = ?,
		    catalog_claim_id = ?, record_json = ?
		WHERE workspace_id = ? AND snapshot_id = ?
		      AND catalog_revision = ?`,
		record.CatalogRevision,
		record.CatalogMutationRevision,
		record.CatalogSessionEpoch,
		record.CatalogFenceEpoch,
		record.CatalogClaimID,
		raw,
		workspaceID,
		snapshotID,
		expectedCatalogRevision,
	)
	if err != nil {
		return Record{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Record{}, err
	}
	if affected != 1 {
		return Record{}, errors.New("snapshot.catalog_revision_stale")
	}
	if receipt != nil {
		if err := validateOperationReceipt(*receipt, workspaceID); err != nil {
			return Record{}, err
		}
		if err := insertOperationReceipt(
			ctx,
			transaction,
			*receipt,
		); err != nil {
			return Record{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (catalog *DurableCatalog) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	if catalog == nil || catalog.db == nil {
		return protocolv2.OperationReceipt{}, false,
			errors.New("snapshot.catalog_closed")
	}
	var (
		receipt protocolv2.OperationReceipt
		scope   string
		raw     []byte
	)
	err := catalog.db.QueryRowContext(ctx, `
		SELECT workspace_id, operation_id, method, scope,
		       request_hash, result_json
		FROM snapshot_operation_receipts
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

func insertOperationReceipt(
	ctx context.Context,
	transaction *sql.Tx,
	receipt protocolv2.OperationReceipt,
) error {
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO snapshot_operation_receipts (
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
	return err
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
		return errors.New("snapshot.operation_receipt_invalid")
	}
	return nil
}

func (catalog *DurableCatalog) HasCommittedMutation(
	ctx context.Context,
	workspaceID string,
	mutationRevision uint64,
	sessionEpoch uint64,
	fenceEpoch uint64,
	claimID string,
) (bool, error) {
	if catalog == nil || catalog.db == nil {
		return false, errors.New("snapshot.catalog_closed")
	}
	var count uint64
	err := catalog.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM snapshot_catalog
		WHERE workspace_id = ? AND catalog_mutation_revision = ?
		      AND catalog_session_epoch = ? AND catalog_fence_epoch = ?
		      AND catalog_claim_id = ?`,
		workspaceID,
		mutationRevision,
		sessionEpoch,
		fenceEpoch,
		claimID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 1 {
		return false, errors.New("snapshot.catalog_mutation_ambiguous")
	}
	return count == 1, nil
}

func (catalog *DurableCatalog) Last(
	ctx context.Context,
	workspaceID string,
) (Record, bool, error) {
	if catalog == nil || catalog.db == nil {
		return Record{}, false, errors.New("snapshot.catalog_closed")
	}
	var raw []byte
	err := catalog.db.QueryRowContext(ctx, `
		SELECT record_json FROM snapshot_catalog
		WHERE workspace_id = ?
		ORDER BY snapshot_sequence DESC LIMIT 1`,
		workspaceID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	record, err := decodeCatalogRecord(raw)
	return record, err == nil, err
}

func (catalog *DurableCatalog) List(
	ctx context.Context,
	workspaceID string,
) ([]Record, error) {
	if catalog == nil || catalog.db == nil {
		return nil, errors.New("snapshot.catalog_closed")
	}
	rows, err := catalog.db.QueryContext(ctx, `
		SELECT record_json FROM snapshot_catalog
		WHERE workspace_id = ? ORDER BY snapshot_sequence`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		record, err := decodeCatalogRecord(raw)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func decodeCatalogRecord(raw []byte) (Record, error) {
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, errors.Join(
			errors.New("snapshot.catalog_corrupt"), err,
		)
	}
	if err := validateCatalogRecord(record); err != nil {
		return Record{}, errors.Join(
			errors.New("snapshot.catalog_corrupt"), err,
		)
	}
	return record, nil
}

func validateCatalogRecord(record Record) error {
	if record.SnapshotID == "" ||
		record.WorkspaceID == "" ||
		record.SnapshotSequence == 0 ||
		record.ManifestID == "" ||
		record.SealID == "" ||
		record.RootPinID == "" ||
		record.CatalogRevision == 0 ||
		record.ObjectMap["file-state-root"] == "" {
		return errors.New("snapshot.catalog_record_invalid")
	}
	return nil
}
