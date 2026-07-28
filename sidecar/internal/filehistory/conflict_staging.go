package filehistory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type ConflictSelection struct {
	DocumentID       string              `json:"documentId"`
	ExpectedPath     string              `json:"expectedPath"`
	ExpectedObjectID objectrepo.ObjectID `json:"expectedObjectId"`
	ExpectedDeleted  bool                `json:"expectedDeleted"`
	ChosenPath       string              `json:"chosenPath"`
	ChosenObjectID   objectrepo.ObjectID `json:"chosenObjectId"`
	ChosenDeleted    bool                `json:"chosenDeleted"`
}

type ConflictStage struct {
	StageID             string                      `json:"stageId"`
	PlanID              string                      `json:"planId"`
	OperationID         string                      `json:"operationId"`
	Selections          []ConflictSelection         `json:"selections"`
	RecoverySnapshotIDs []string                    `json:"recoverySnapshotIds"`
	OperationReceipt    protocolv2.OperationReceipt `json:"operationReceipt"`
}

type ConflictCommit struct {
	OperationID         string
	AuthorityRevision   uint64
	RecoverySnapshotIDs []string
}

type ConflictApplier struct {
	service *Service
	store   *SQLiteHeadStore
}

func NewConflictApplier(
	service *Service,
	store *SQLiteHeadStore,
) (*ConflictApplier, error) {
	if service == nil || store == nil || store.db == nil ||
		service.headStore != store {
		return nil, errors.New("filehistory.conflict_dependencies_required")
	}
	if _, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS filehistory_conflict_stages (
			stage_id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE,
			plan_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('prepared','committed')),
			stage_json BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS filehistory_operation_receipts (
			operation_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL
		);
	`); err != nil {
		return nil, err
	}
	return &ConflictApplier{service: service, store: store}, nil
}

func (applier *ConflictApplier) Prepare(
	ctx context.Context,
	stage ConflictStage,
) (ConflictStage, error) {
	if applier == nil || applier.store == nil ||
		strings.TrimSpace(stage.PlanID) == "" ||
		strings.TrimSpace(stage.OperationID) == "" ||
		len(stage.Selections) == 0 ||
		stage.OperationReceipt.OperationID != stage.OperationID ||
		stage.OperationReceipt.WorkspaceID == "" ||
		stage.OperationReceipt.Method != "conflict.apply" ||
		stage.OperationReceipt.Scope != protocolv2.WorkspaceScope ||
		len(stage.OperationReceipt.Result) == 0 {
		return ConflictStage{}, errors.New(
			"filehistory.conflict_stage_invalid",
		)
	}
	if stage.StageID == "" {
		stage.StageID = uuid.NewString()
	}
	sort.Slice(stage.Selections, func(i, j int) bool {
		return stage.Selections[i].DocumentID <
			stage.Selections[j].DocumentID
	})
	for index, selection := range stage.Selections {
		if !validUUID(selection.DocumentID) ||
			selection.ExpectedPath == "" ||
			selection.ExpectedObjectID == "" ||
			selection.ChosenPath == "" ||
			selection.ChosenObjectID == "" {
			return ConflictStage{}, errors.New(
				"filehistory.conflict_selection_invalid",
			)
		}
		if index > 0 &&
			stage.Selections[index-1].DocumentID ==
				selection.DocumentID {
			return ConflictStage{}, errors.New(
				"filehistory.conflict_selection_duplicate",
			)
		}
	}
	applier.service.mu.RLock()
	for _, selection := range stage.Selections {
		document, exists := applier.service.documents[selection.DocumentID]
		if !exists {
			applier.service.mu.RUnlock()
			return ConflictStage{}, ErrDocumentNotFound
		}
		effective := revisionByID(
			document, document.EffectiveRevisionID,
		)
		if effective == nil ||
			document.RelativePath != selection.ExpectedPath ||
			effective.ObjectID != selection.ExpectedObjectID ||
			(document.Status == DocumentDeleted) !=
				selection.ExpectedDeleted {
			applier.service.mu.RUnlock()
			return ConflictStage{}, ErrRevisionConflict
		}
	}
	applier.service.mu.RUnlock()
	roots := make(
		[]objectrepo.ObjectID, 0, len(stage.Selections),
	)
	for _, selection := range stage.Selections {
		roots = append(roots, selection.ChosenObjectID)
	}
	report, err := applier.service.repository.Verify(ctx, roots)
	if err != nil {
		return ConflictStage{}, err
	}
	if !report.Valid {
		return ConflictStage{}, ErrStateCorrupt
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		return ConflictStage{}, err
	}
	_, err = applier.store.db.ExecContext(ctx, `
		INSERT INTO filehistory_conflict_stages(
			stage_id, operation_id, plan_id, state, stage_json
		) VALUES (?, ?, ?, 'prepared', ?)
		ON CONFLICT(operation_id) DO NOTHING`,
		stage.StageID,
		stage.OperationID,
		stage.PlanID,
		raw,
	)
	if err != nil {
		return ConflictStage{}, err
	}
	existing, err := applier.loadStage(ctx, stage.StageID)
	if err != nil {
		return ConflictStage{}, err
	}
	existingRaw, _ := json.Marshal(existing)
	if string(existingRaw) != string(raw) {
		return ConflictStage{}, errors.New(
			"filehistory.conflict_stage_conflict",
		)
	}
	return stage, nil
}

func (applier *ConflictApplier) Commit(
	ctx context.Context,
	stageID string,
) (ConflictCommit, error) {
	stage, err := applier.loadStage(ctx, stageID)
	if err != nil {
		return ConflictCommit{}, err
	}
	if receipt, found, err := applier.LoadOperationReceipt(
		ctx,
		stage.OperationReceipt.WorkspaceID,
		stage.OperationID,
	); err != nil {
		return ConflictCommit{}, err
	} else if found {
		return commitFromReceipt(stage, receipt)
	}
	service := applier.service
	token, _ := service.coordinator.Current()
	var (
		nextDocuments map[string]Document
		nextHead      CurrentHead
		stateLocked   bool
	)
	writeReceipt, err := service.coordinator.Write(
		ctx,
		token,
		func(
			ctx context.Context,
			intent writecoordinator.WriteIntent,
		) error {
			service.mu.Lock()
			stateLocked = true
			next := cloneDocuments(service.documents)
			for _, selection := range stage.Selections {
				document, exists := next[selection.DocumentID]
				if !exists {
					return ErrDocumentNotFound
				}
				effective := revisionByID(
					document,
					document.EffectiveRevisionID,
				)
				if effective == nil ||
					document.RelativePath != selection.ExpectedPath ||
					effective.ObjectID != selection.ExpectedObjectID ||
					(document.Status == DocumentDeleted) !=
						selection.ExpectedDeleted {
					return ErrRevisionConflict
				}
				chosenPath, err := normalizePath(selection.ChosenPath)
				if err != nil {
					return err
				}
				if err := pathAvailable(
					next, document.DocumentID, chosenPath,
				); err != nil {
					return err
				}
				reader, err := service.repository.Open(
					ctx, selection.ChosenObjectID,
				)
				if err != nil {
					return err
				}
				content, readErr := io.ReadAll(reader)
				closeErr := reader.Close()
				if err := errors.Join(readErr, closeErr); err != nil {
					return err
				}
				revisionID, err := service.newID()
				if err != nil || !validUUID(revisionID) {
					return errors.Join(
						errors.New(
							"filehistory.revision_id_invalid",
						),
						err,
					)
				}
				formalVersion := document.NextFormalVersion
				parent := document.EffectiveRevisionID
				revision := Revision{
					ContractVersion:  contractVersion,
					RevisionID:       revisionID,
					DocumentID:       document.DocumentID,
					ParentRevisionID: &parent,
					Kind:             RevisionFormal,
					RevisionOrdinal:  document.NextRevisionOrdinal,
					FormalVersion:    &formalVersion,
					ObjectID:         selection.ChosenObjectID,
					ContentHash:      contentHash(content),
					Size:             int64(len(content)),
					MimeType:         effective.MimeType,
					CreatedAt:        service.now().UTC(),
					CreatedBy:        "conflict-resolution",
					DeviceID:         intent.Token.ClaimID,
					Comment: optionalString(
						"resolved from replica conflict",
					),
				}
				document.NextRevisionOrdinal++
				document.NextFormalVersion++
				document.Revisions = append(
					document.Revisions, revision,
				)
				document.EffectiveRevisionID = revisionID
				pathChanged := document.RelativePath != chosenPath
				document.RelativePath = chosenPath
				if pathChanged ||
					document.Status == DocumentDeleted ||
					selection.ChosenDeleted {
					document.TopologyRevision++
				}
				if selection.ChosenDeleted {
					document.Status = DocumentDeleted
				} else {
					document.Status = DocumentActive
				}
				next[document.DocumentID] = document
			}
			if err := validatePaths(next); err != nil {
				return err
			}
			payload := rootPayload{
				FormatVersion: rootFormatVersion,
				WorkspaceID:   intent.Token.WorkspaceID,
				Documents:     sortedDocuments(next),
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			repositoryReceipt, err := service.repository.Commit(
				ctx,
				objectrepo.CommitRequest{
					Authority: intent.Token.Authority(),
					Manifests: []objectrepo.ManifestInput{{
						Name: "filehistory-root",
						Labels: map[string]string{
							"type":        "filehistory-root",
							"workspaceId": intent.Token.WorkspaceID,
						},
						Payload: raw,
					}},
				},
			)
			if err != nil {
				return err
			}
			root := repositoryReceipt.Manifests["filehistory-root"]
			if !repositoryReceipt.Durable || root == "" {
				return errors.New("filehistory.root_missing")
			}
			previous := CurrentHead{
				WorkspaceID:      intent.Token.WorkspaceID,
				Root:             service.root,
				Revision:         service.headRevision,
				MutationRevision: service.headMutationRevision,
				SessionEpoch:     service.headSessionEpoch,
				FenceEpoch:       service.headFenceEpoch,
				ClaimID:          service.headClaimID,
			}
			nextHead = CurrentHead{
				WorkspaceID:      intent.Token.WorkspaceID,
				Root:             root,
				Revision:         previous.Revision + 1,
				MutationRevision: intent.MutationRevision,
				SessionEpoch:     intent.Token.SessionEpoch,
				FenceEpoch:       intent.Token.FenceEpoch,
				ClaimID:          intent.Token.ClaimID,
			}
			auditPayload, err := json.Marshal(map[string]any{
				"type":                "conflict.resolved",
				"workspaceId":         intent.Token.WorkspaceID,
				"operationId":         stage.OperationID,
				"planId":              stage.PlanID,
				"mutationRevision":    intent.MutationRevision,
				"previousRoot":        previous.Root,
				"root":                nextHead.Root,
				"documentCount":       len(stage.Selections),
				"recoverySnapshotIds": stage.RecoverySnapshotIDs,
			})
			if err != nil {
				return err
			}
			envelope, err := auditledger.NewEnvelope(
				fmt.Sprintf(
					"conflict-resolution:%s",
					stage.OperationID,
				),
				"conflict:"+intent.Token.WorkspaceID,
				nextHead.Revision,
				fmt.Sprintf(
					"conflict:%s:%d:%d:%s:%d",
					intent.Token.WorkspaceID,
					intent.Token.SessionEpoch,
					intent.Token.FenceEpoch,
					intent.Token.ClaimID,
					intent.MutationRevision,
				),
				auditPayload,
				service.now().UTC(),
			)
			if err != nil {
				return err
			}
			if service.materializer != nil {
				if err := service.materializer.PrepareAndApply(
					ctx,
					intent,
					service.documents,
					next,
				); err != nil {
					return err
				}
			}
			if err := applier.store.publishConflict(
				ctx,
				previous,
				nextHead,
				envelope,
				stage,
			); err != nil {
				if service.materializer != nil {
					err = errors.Join(
						err,
						service.materializer.Rollback(
							intent.MutationRevision,
						),
					)
				}
				return err
			}
			nextDocuments = next
			return nil
		},
	)
	if stateLocked {
		defer service.mu.Unlock()
	}
	if err != nil {
		return ConflictCommit{}, err
	}
	service.documents = nextDocuments
	service.installHeadLocked(nextHead)
	if service.materializer != nil {
		_ = service.materializer.Finalize(
			writeReceipt.MutationRevision,
		)
	}
	return ConflictCommit{
		OperationID:         stage.OperationID,
		AuthorityRevision:   nextHead.Revision,
		RecoverySnapshotIDs: append([]string(nil), stage.RecoverySnapshotIDs...),
	}, nil
}

func (applier *ConflictApplier) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	var (
		receipt protocolv2.OperationReceipt
		scope   string
		result  []byte
	)
	err := applier.store.db.QueryRowContext(ctx, `
		SELECT operation_id, workspace_id, method, scope,
		       request_hash, result_json
		FROM filehistory_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID, operationID,
	).Scan(
		&receipt.OperationID,
		&receipt.WorkspaceID,
		&receipt.Method,
		&scope,
		&receipt.RequestHash,
		&result,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocolv2.OperationReceipt{}, false, nil
	}
	if err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	receipt.Scope = protocolv2.ScopeKind(scope)
	receipt.Result = append(json.RawMessage(nil), result...)
	return receipt, true, nil
}

func (applier *ConflictApplier) loadStage(
	ctx context.Context,
	stageID string,
) (ConflictStage, error) {
	var raw []byte
	err := applier.store.db.QueryRowContext(ctx, `
		SELECT stage_json FROM filehistory_conflict_stages
		WHERE stage_id = ?`,
		stageID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ConflictStage{}, errors.New(
			"filehistory.conflict_stage_not_found",
		)
	}
	if err != nil {
		return ConflictStage{}, err
	}
	var stage ConflictStage
	if err := decodeStrict(raw, &stage); err != nil {
		return ConflictStage{}, err
	}
	return stage, nil
}

func (store *SQLiteHeadStore) publishConflict(
	ctx context.Context,
	expected CurrentHead,
	next CurrentHead,
	envelope auditledger.Envelope,
	stage ConflictStage,
) error {
	if err := validateHeadTransition(expected, next); err != nil {
		return ErrStateCorrupt
	}
	normalized, err := auditledger.NewEnvelope(
		envelope.EventID,
		envelope.SourceEpoch,
		envelope.SourceSequence,
		envelope.MutationIdentity,
		envelope.Payload,
		envelope.OccurredAt,
	)
	if err != nil ||
		normalized.PayloadHash != envelope.PayloadHash {
		return errors.Join(auditledger.ErrPayloadMismatch, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE filehistory_heads
		SET root_manifest_id = ?, revision = ?, mutation_revision = ?,
		    session_epoch = ?, fence_epoch = ?, claim_id = ?
		WHERE workspace_id = ? AND root_manifest_id = ?
		      AND revision = ? AND mutation_revision = ?
		      AND session_epoch = ? AND fence_epoch = ?
		      AND claim_id = ?`,
		next.Root, next.Revision, next.MutationRevision,
		next.SessionEpoch, next.FenceEpoch, next.ClaimID,
		expected.WorkspaceID, expected.Root, expected.Revision,
		expected.MutationRevision, expected.SessionEpoch,
		expected.FenceEpoch, expected.ClaimID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrHeadConflict, err)
	}
	if _, err := tx.ExecContext(ctx, `
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
	); err != nil {
		return err
	}
	receipt := stage.OperationReceipt
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO filehistory_operation_receipts(
			operation_id, workspace_id, method, scope,
			request_hash, result_json
		) VALUES (?, ?, ?, ?, ?, ?)`,
		receipt.OperationID,
		receipt.WorkspaceID,
		receipt.Method,
		string(receipt.Scope),
		receipt.RequestHash,
		[]byte(receipt.Result),
	); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE filehistory_conflict_stages
		SET state = 'committed'
		WHERE stage_id = ? AND operation_id = ?
		      AND state = 'prepared'`,
		stage.StageID, stage.OperationID,
	)
	if err != nil {
		return err
	}
	affected, err = result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrHeadConflict, err)
	}
	return tx.Commit()
}

func commitFromReceipt(
	stage ConflictStage,
	receipt protocolv2.OperationReceipt,
) (ConflictCommit, error) {
	var result struct {
		OperationID         string   `json:"operationId"`
		State               string   `json:"state"`
		RecoverySnapshotIDs []string `json:"recoverySnapshotIds"`
	}
	if err := json.Unmarshal(receipt.Result, &result); err != nil ||
		result.OperationID != stage.OperationID ||
		result.State != "applied" {
		return ConflictCommit{}, errors.Join(
			errors.New("filehistory.conflict_receipt_invalid"),
			err,
		)
	}
	// The exact head revision is recovered from the current authoritative head.
	// It is not part of the public RPC result.
	return ConflictCommit{
		OperationID:         result.OperationID,
		AuthorityRevision:   1,
		RecoverySnapshotIDs: append([]string(nil), result.RecoverySnapshotIDs...),
	}, nil
}
