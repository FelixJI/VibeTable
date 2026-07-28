package workspacev2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type externalFileOperation struct {
	Receipt     protocolv2.OperationReceipt
	Session     protocolv2.Session
	Staging     string
	Target      string
	ContentHash string
	ContentSize int64
	State       string
}

func (store *stateStore) prepareExternalFileOperation(
	ctx context.Context,
	operation externalFileOperation,
) error {
	if err := validateExternalFileOperation(operation); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO external_file_operation_journal (
			workspace_id, operation_id, method, scope, request_hash,
			result_json, session_epoch, sequence, staging_path, target_path,
			content_hash, content_size, state
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared')`,
		operation.Receipt.WorkspaceID,
		operation.Receipt.OperationID,
		operation.Receipt.Method,
		string(operation.Receipt.Scope),
		operation.Receipt.RequestHash,
		[]byte(operation.Receipt.Result),
		operation.Session.Epoch,
		operation.Session.Sequence,
		operation.Staging,
		operation.Target,
		operation.ContentHash,
		operation.ContentSize,
	)
	return err
}

func (store *stateStore) completeExternalFileOperation(
	ctx context.Context,
	operation externalFileOperation,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		operation.Session,
		operation.Receipt,
		func(transaction *sql.Tx) error {
			result, err := transaction.ExecContext(ctx, `
				UPDATE external_file_operation_journal
				SET state = 'completed'
				WHERE workspace_id = ? AND operation_id = ?
				      AND state IN ('prepared', 'completed')`,
				operation.Receipt.WorkspaceID,
				operation.Receipt.OperationID,
			)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return errors.Join(
					errors.New("workspace.external_operation_missing"),
					err,
				)
			}
			return nil
		},
	)
}

func (store *stateStore) loadExternalFileOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	var (
		operation externalFileOperation
		scope     string
		raw       []byte
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT workspace_id, operation_id, method, scope, request_hash,
		       result_json, session_epoch, sequence, staging_path,
		       target_path, content_hash, content_size, state
		FROM external_file_operation_journal
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID,
		operationID,
	).Scan(
		&operation.Receipt.WorkspaceID,
		&operation.Receipt.OperationID,
		&operation.Receipt.Method,
		&scope,
		&operation.Receipt.RequestHash,
		&raw,
		&operation.Session.Epoch,
		&operation.Session.Sequence,
		&operation.Staging,
		&operation.Target,
		&operation.ContentHash,
		&operation.ContentSize,
		&operation.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocolv2.OperationReceipt{}, false, nil
	}
	if err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	operation.Receipt.Scope = protocolv2.ScopeKind(scope)
	operation.Receipt.Result = append(json.RawMessage(nil), raw...)
	operation.Session.WorkspaceID = operation.Receipt.WorkspaceID
	if err := validateExternalFileOperation(operation); err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	if operation.State == "prepared" {
		matches, verifyErr := externalFileMatches(
			operation.Target,
			operation.ContentHash,
			operation.ContentSize,
		)
		if verifyErr != nil {
			return protocolv2.OperationReceipt{}, false, verifyErr
		}
		if !matches {
			stagingMatches, stagingErr := externalFileMatches(
				operation.Staging,
				operation.ContentHash,
				operation.ContentSize,
			)
			if stagingErr != nil {
				return protocolv2.OperationReceipt{}, false, stagingErr
			}
			if !stagingMatches {
				return protocolv2.OperationReceipt{}, false,
					errors.New("workspace.external_operation_unrecoverable")
			}
			if err := validateExportTarget(operation.Target); err != nil {
				return protocolv2.OperationReceipt{}, false, err
			}
			if err := replaceGrantedFile(
				operation.Staging,
				operation.Target,
			); err != nil {
				return protocolv2.OperationReceipt{}, false, err
			}
		}
		if err := store.completeExternalFileOperation(
			context.WithoutCancel(ctx),
			operation,
		); err != nil {
			return protocolv2.OperationReceipt{}, false, err
		}
	}
	return operation.Receipt, true, nil
}

func externalFileMatches(
	path string,
	expectedHash string,
	expectedSize int64,
) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("workspace.external_operation_path_unsafe")
	}
	hash, size, err := hashRestoreFile(path)
	if err != nil {
		return false, err
	}
	return hash == expectedHash && size == expectedSize, nil
}

func validateExternalFileOperation(
	operation externalFileOperation,
) error {
	if operation.Receipt.WorkspaceID == "" ||
		operation.Receipt.OperationID == "" ||
		operation.Receipt.Method == "" ||
		operation.Receipt.RequestHash == "" ||
		!json.Valid(operation.Receipt.Result) ||
		operation.Receipt.Scope != protocolv2.WorkspaceScope ||
		operation.Session.WorkspaceID != operation.Receipt.WorkspaceID ||
		operation.Session.Epoch == 0 ||
		operation.Session.Sequence == 0 ||
		!filepath.IsAbs(operation.Staging) ||
		!filepath.IsAbs(operation.Target) ||
		filepath.Clean(operation.Staging) != operation.Staging ||
		filepath.Clean(operation.Target) != operation.Target ||
		filepath.Dir(operation.Staging) !=
			filepath.Dir(operation.Target) ||
		operation.Staging == operation.Target ||
		!strings.HasPrefix(
			filepath.Base(operation.Staging),
			"."+filepath.Base(operation.Target)+".",
		) ||
		!strings.HasSuffix(
			filepath.Base(operation.Staging),
			".tmp",
		) ||
		operation.ContentHash == "" ||
		operation.ContentSize < 0 ||
		(operation.State != "" &&
			operation.State != "prepared" &&
			operation.State != "completed") {
		return errors.New("workspace.external_operation_corrupt")
	}
	return nil
}
