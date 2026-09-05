package workspacedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
)

// ImportedCompletionsSchema stores business completion facts without any
// authority epoch, claim or recovery revision.
const ImportedCompletionsSchema = `CREATE TABLE IF NOT EXISTS workspace_v2_imported_completions (
    workspace_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    identity TEXT NOT NULL,
    PRIMARY KEY(workspace_id, kind, identity)
)`

type sqliteSerializer interface{ Serialize() ([]byte, error) }

// PrepareImportedSnapshot creates a private database image for a new workspace.
// The caller must first validate the source bundle, including verifiedPrefix's
// chain and anchor. Historical audit remains in that immutable prefix; source
// runtime receipts must not become the new workspace's recovery authority.
func PrepareImportedSnapshot(ctx context.Context, raw []byte, sourceWorkspaceID, targetWorkspaceID string, verifiedPrefix auditledger.Prefix) ([]byte, error) {
	database, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return nil, err
	}
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if err := connection.Raw(func(driver any) error {
		deserializer, ok := driver.(sqliteDeserializer)
		if !ok {
			return errors.New("workspace.sqlite_deserialize_unavailable")
		}
		return deserializer.Deserialize(raw)
	}); err != nil {
		return nil, err
	}
	var triggers int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'trigger' AND lower(tbl_name) IN
		('workspace_v2_mutation_receipts', 'vibetable_audit_outbox', 'workspace_v2_imported_completions')`).Scan(&triggers); err != nil {
		return nil, err
	}
	if triggers != 0 {
		return nil, fmt.Errorf("%w: import runtime tables have triggers", ErrSnapshotDatabaseInvalid)
	}
	proof := make(map[string]auditledger.Envelope, len(verifiedPrefix.Records))
	for _, record := range verifiedPrefix.Records {
		proof[record.Envelope.EventID] = record.Envelope
	}
	if err := verifyImportedOutbox(ctx, connection, proof); err != nil {
		return nil, err
	}
	if err := verifyImportedReceipts(ctx, connection, sourceWorkspaceID, proof); err != nil {
		return nil, err
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, ImportedCompletionsSchema); err != nil {
		return nil, err
	}
	var foreignCompletions int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_v2_imported_completions WHERE workspace_id != ?`, sourceWorkspaceID).Scan(&foreignCompletions); err != nil {
		return nil, err
	}
	if foreignCompletions != 0 {
		return nil, fmt.Errorf("%w: imported completion workspace mismatch", ErrSnapshotDatabaseInvalid)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workspace_v2_imported_completions SET workspace_id = ?`, targetWorkspaceID); err != nil {
		return nil, err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_v2_imported_completions (workspace_id, kind, identity)
		SELECT ?, kind, identity FROM workspace_v2_mutation_receipts`, targetWorkspaceID); err != nil {
		return nil, err
	}
	for _, table := range []string{"workspace_v2_mutation_receipts", "vibetable_audit_outbox"} {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	// Repack the private image so retired records do not survive in free pages.
	if _, err := connection.ExecContext(ctx, "VACUUM"); err != nil {
		return nil, err
	}
	var prepared []byte
	if err := connection.Raw(func(driver any) error {
		serializer, ok := driver.(sqliteSerializer)
		if !ok {
			return errors.New("workspace.sqlite_serialize_unavailable")
		}
		prepared, err = serializer.Serialize()
		return err
	}); err != nil {
		return nil, err
	}
	return prepared, nil
}

func verifyImportedOutbox(ctx context.Context, connection *sql.Conn, proof map[string]auditledger.Envelope) error {
	rows, err := connection.QueryContext(ctx, `SELECT event_id, source_epoch, source_sequence, mutation_identity, payload_hash, payload_json, status FROM vibetable_audit_outbox`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event, epoch, identity, digest, status string
		var sequence uint64
		var payload []byte
		if err := rows.Scan(&event, &epoch, &sequence, &identity, &digest, &payload, &status); err != nil {
			return err
		}
		envelope, exists := proof[event]
		if !exists || status != "drained" || envelope.SourceEpoch != epoch || envelope.SourceSequence != sequence || envelope.MutationIdentity != identity || envelope.PayloadHash != digest || !bytes.Equal(envelope.Payload, payload) {
			return fmt.Errorf("%w: import outbox is not settled in source audit", ErrSnapshotDatabaseInvalid)
		}
	}
	return rows.Err()
}

func verifyImportedReceipts(ctx context.Context, connection *sql.Conn, sourceWorkspaceID string, proof map[string]auditledger.Envelope) error {
	rows, err := connection.QueryContext(ctx, `SELECT mutation_revision, workspace_id, session_epoch, fence_epoch, claim_id, kind, identity, audit_source_sequence FROM workspace_v2_mutation_receipts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var revision, session, fence, sequence uint64
		var workspace, claim, kind, identity string
		if err := rows.Scan(&revision, &workspace, &session, &fence, &claim, &kind, &identity, &sequence); err != nil {
			return err
		}
		envelope, exists := proof[fmt.Sprintf("workspace-business:%s:%d", workspace, revision)]
		var payload struct {
			Type             string `json:"type"`
			WorkspaceID      string `json:"workspaceId"`
			SessionEpoch     uint64 `json:"sessionEpoch"`
			FenceEpoch       uint64 `json:"fenceEpoch"`
			ClaimID          string `json:"claimId"`
			MutationRevision uint64 `json:"mutationRevision"`
			Kind             string `json:"kind"`
			Identity         string `json:"identity"`
		}
		if !exists || workspace != sourceWorkspaceID || revision == 0 || json.Unmarshal(envelope.Payload, &payload) != nil ||
			envelope.SourceSequence != sequence || envelope.MutationIdentity != identity || payload.Type != "workspace.v2.business-mutation" || payload.WorkspaceID != workspace || payload.SessionEpoch != session || payload.FenceEpoch != fence || payload.ClaimID != claim || payload.MutationRevision != revision || payload.Kind != kind || payload.Identity != identity {
			return fmt.Errorf("%w: import receipt has no source audit proof", ErrSnapshotDatabaseInvalid)
		}
	}
	return rows.Err()
}
