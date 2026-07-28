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
	ExpectedMimeType string              `json:"expectedMimeType,omitempty"`
	ExpectedDeleted  bool                `json:"expectedDeleted"`
	ChosenPath       string              `json:"chosenPath"`
	ChosenObjectID   objectrepo.ObjectID `json:"chosenObjectId"`
	ChosenMimeType   string              `json:"chosenMimeType,omitempty"`
	ChosenDeleted    bool                `json:"chosenDeleted"`
}

// ConflictCopy is a second, live workspace document created by the "both"
// conflict choice. The source document remains unchanged; the copied candidate
// starts a new linear history with its own durable identity.
type ConflictCopy struct {
	SourceDocumentID string              `json:"sourceDocumentId"`
	DocumentID       string              `json:"documentId"`
	ChosenPath       string              `json:"chosenPath"`
	ChosenObjectID   objectrepo.ObjectID `json:"chosenObjectId"`
	ChosenMimeType   string              `json:"chosenMimeType,omitempty"`
}

type ConflictStage struct {
	StageID             string                      `json:"stageId"`
	PlanID              string                      `json:"planId"`
	OperationID         string                      `json:"operationId"`
	Selections          []ConflictSelection         `json:"selections"`
	Copies              []ConflictCopy              `json:"copies"`
	RecoverySnapshotIDs []string                    `json:"recoverySnapshotIds"`
	OperationReceipt    protocolv2.OperationReceipt `json:"operationReceipt"`
	// External is an opaque, durable workspace-level publication plan. The
	// workspace adapter validates it during Prepare and replays it idempotently
	// inside the same coordinator mutation before file-history publication.
	External json.RawMessage `json:"external,omitempty"`
}

type ConflictCommit struct {
	OperationID         string
	AuthorityRevision   uint64
	RecoverySnapshotIDs []string
}

type ExternalApplyResult struct {
	Irreversible bool
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
		CREATE TABLE IF NOT EXISTS filehistory_external_conflict_proofs (
			mutation_revision INTEGER PRIMARY KEY,
			stage_id TEXT NOT NULL UNIQUE,
			operation_id TEXT NOT NULL UNIQUE
		);
		CREATE TABLE IF NOT EXISTS filehistory_external_conflict_journal (
			stage_id TEXT PRIMARY KEY,
			phase TEXT NOT NULL CHECK(phase IN ('started','committed'))
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
		len(stage.Selections)+len(stage.Copies) == 0 &&
			len(stage.External) == 0 ||
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
		stage.StageID = uuid.NewSHA1(
			uuid.NameSpaceOID,
			[]byte("vibetable-conflict-stage:"+stage.OperationID),
		).String()
	}
	sort.Slice(stage.Selections, func(i, j int) bool {
		return stage.Selections[i].DocumentID <
			stage.Selections[j].DocumentID
	})
	sort.Slice(stage.Copies, func(i, j int) bool {
		return stage.Copies[i].DocumentID < stage.Copies[j].DocumentID
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
	for index, copy := range stage.Copies {
		if !validUUID(copy.SourceDocumentID) ||
			!validUUID(copy.DocumentID) ||
			copy.SourceDocumentID == copy.DocumentID ||
			copy.ChosenPath == "" ||
			copy.ChosenObjectID == "" {
			return ConflictStage{}, errors.New(
				"filehistory.conflict_copy_invalid",
			)
		}
		if index > 0 &&
			stage.Copies[index-1].DocumentID == copy.DocumentID {
			return ConflictStage{}, errors.New(
				"filehistory.conflict_copy_duplicate",
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
			selection.ExpectedMimeType != "" &&
				effective.MimeType != selection.ExpectedMimeType ||
			(document.Status == DocumentDeleted) !=
				selection.ExpectedDeleted {
			applier.service.mu.RUnlock()
			return ConflictStage{}, ErrRevisionConflict
		}
	}
	preview := cloneDocuments(applier.service.documents)
	for _, copy := range stage.Copies {
		if _, exists := preview[copy.DocumentID]; exists {
			applier.service.mu.RUnlock()
			return ConflictStage{}, ErrRevisionConflict
		}
		source, exists := preview[copy.SourceDocumentID]
		if !exists || source.Status != DocumentActive {
			applier.service.mu.RUnlock()
			return ConflictStage{}, ErrDocumentNotFound
		}
		chosenPath, err := normalizePath(copy.ChosenPath)
		if err != nil {
			applier.service.mu.RUnlock()
			return ConflictStage{}, err
		}
		if err := pathAvailable(preview, copy.DocumentID, chosenPath); err != nil {
			applier.service.mu.RUnlock()
			return ConflictStage{}, err
		}
		preview[copy.DocumentID] = Document{
			DocumentID:   copy.DocumentID,
			RelativePath: chosenPath,
			Status:       DocumentActive,
		}
	}
	applier.service.mu.RUnlock()
	roots := make(
		[]objectrepo.ObjectID, 0, len(stage.Selections)+len(stage.Copies),
	)
	for _, selection := range stage.Selections {
		roots = append(roots, selection.ChosenObjectID)
	}
	for _, copy := range stage.Copies {
		roots = append(roots, copy.ChosenObjectID)
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
	existing, err := applier.loadStageByOperationID(
		ctx, stage.OperationID,
	)
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
	return applier.CommitWith(ctx, stageID, nil)
}

// CommitWith publishes a durable conflict stage and invokes externalApply
// inside the same coordinator mutation. externalApply must be idempotent:
// coordinator recovery or an applying conflict plan may replay it after a
// crash before the file-history receipt became visible.
func (applier *ConflictApplier) CommitWith(
	ctx context.Context,
	stageID string,
	externalApply func(
		context.Context,
		writecoordinator.WriteIntent,
		ConflictStage,
	) (ExternalApplyResult, error),
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
	externalIrreversible := false
	apply := func(
		ctx context.Context,
		intent writecoordinator.WriteIntent,
	) (applyErr error) {
		defer func() {
			if applyErr != nil && externalIrreversible {
				applyErr = errors.Join(
					writecoordinator.ErrExternalCommitted,
					applyErr,
				)
			}
		}()
		service.mu.Lock()
		stateLocked = true
		preflight, err := applier.preflightCurrentStageLocked(
			ctx, stage,
		)
		if err != nil {
			return err
		}
		if externalApply != nil {
			result, err := externalApply(ctx, intent, stage)
			externalIrreversible = result.Irreversible
			if err != nil {
				return err
			}
		}
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
			chosenPath := preflight.paths[selection.DocumentID]
			content := preflight.contents[selection.ChosenObjectID]
			revisionID := preflight.revisionIDs[selection.DocumentID]
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
				MimeType: optionalMimeType(
					selection.ChosenMimeType,
					effective.MimeType,
				),
				CreatedAt: service.now().UTC(),
				CreatedBy: "conflict-resolution",
				DeviceID:  intent.Token.ClaimID,
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
		for _, copy := range stage.Copies {
			if _, exists := next[copy.DocumentID]; exists {
				return ErrRevisionConflict
			}
			source, exists := next[copy.SourceDocumentID]
			if !exists || source.Status != DocumentActive {
				return ErrDocumentNotFound
			}
			sourceLeaf := revisionByID(
				source, source.EffectiveRevisionID,
			)
			if sourceLeaf == nil {
				return ErrStateCorrupt
			}
			chosenPath := preflight.paths[copy.DocumentID]
			content := preflight.contents[copy.ChosenObjectID]
			revisionID := preflight.revisionIDs[copy.DocumentID]
			formalVersion := uint64(1)
			revision := Revision{
				ContractVersion: contractVersion,
				RevisionID:      revisionID,
				DocumentID:      copy.DocumentID,
				Kind:            RevisionFormal,
				RevisionOrdinal: 1,
				FormalVersion:   &formalVersion,
				ObjectID:        copy.ChosenObjectID,
				ContentHash:     contentHash(content),
				Size:            int64(len(content)),
				MimeType: optionalMimeType(
					copy.ChosenMimeType,
					sourceLeaf.MimeType,
				),
				CreatedAt: service.now().UTC(),
				CreatedBy: "conflict-resolution",
				DeviceID:  intent.Token.ClaimID,
				Comment: optionalString(
					"kept as second document from replica conflict",
				),
			}
			next[copy.DocumentID] = Document{
				ContractVersion:     contractVersion,
				WorkspaceID:         intent.Token.WorkspaceID,
				DocumentID:          copy.DocumentID,
				RelativePath:        chosenPath,
				Status:              DocumentActive,
				TopologyRevision:    1,
				EffectiveRevisionID: revisionID,
				NextRevisionOrdinal: 2,
				NextFormalVersion:   2,
				Revisions:           []Revision{revision},
			}
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
			"documentCount":       len(stage.Selections) + len(stage.Copies),
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
	}
	recovery := service.coordinator.RecoveryState()
	var writeReceipt writecoordinator.WriteReceipt
	if recovery.PendingMutationRevision != 0 {
		writeReceipt, err = service.coordinator.ResumePreparedMutation(
			ctx,
			token,
			recovery.PendingMutationRevision,
			apply,
		)
	} else {
		writeReceipt, err = service.coordinator.Write(
			ctx, token, apply,
		)
	}
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

type conflictStagePreflight struct {
	contents    map[objectrepo.ObjectID][]byte
	paths       map[string]string
	revisionIDs map[string]string
}

func (applier *ConflictApplier) preflightCurrentStageLocked(
	ctx context.Context,
	stage ConflictStage,
) (conflictStagePreflight, error) {
	service := applier.service
	result := conflictStagePreflight{
		contents:    map[objectrepo.ObjectID][]byte{},
		paths:       map[string]string{},
		revisionIDs: map[string]string{},
	}
	preview := cloneDocuments(service.documents)
	for _, selection := range stage.Selections {
		document, exists := preview[selection.DocumentID]
		if !exists {
			return conflictStagePreflight{}, ErrDocumentNotFound
		}
		effective := revisionByID(
			document, document.EffectiveRevisionID,
		)
		if effective == nil ||
			document.RelativePath != selection.ExpectedPath ||
			effective.ObjectID != selection.ExpectedObjectID ||
			selection.ExpectedMimeType != "" &&
				effective.MimeType != selection.ExpectedMimeType ||
			(document.Status == DocumentDeleted) !=
				selection.ExpectedDeleted {
			return conflictStagePreflight{}, ErrRevisionConflict
		}
		chosenPath, err := normalizePath(selection.ChosenPath)
		if err != nil {
			return conflictStagePreflight{}, err
		}
		if err := pathAvailable(
			preview, selection.DocumentID, chosenPath,
		); err != nil {
			return conflictStagePreflight{}, err
		}
		previewDocument := document
		previewDocument.RelativePath = chosenPath
		if selection.ChosenDeleted {
			previewDocument.Status = DocumentDeleted
		} else {
			previewDocument.Status = DocumentActive
		}
		preview[selection.DocumentID] = previewDocument
		result.paths[selection.DocumentID] = chosenPath
		if _, exists := result.contents[selection.ChosenObjectID]; !exists {
			content, err := readConflictObjectContent(
				ctx, service.repository, selection.ChosenObjectID,
			)
			if err != nil {
				return conflictStagePreflight{}, err
			}
			result.contents[selection.ChosenObjectID] = content
		}
		revisionID, err := service.newID()
		if err != nil || !validUUID(revisionID) {
			return conflictStagePreflight{}, errors.Join(
				errors.New("filehistory.revision_id_invalid"), err,
			)
		}
		result.revisionIDs[selection.DocumentID] = revisionID
	}
	for _, copy := range stage.Copies {
		if _, exists := preview[copy.DocumentID]; exists {
			return conflictStagePreflight{}, ErrRevisionConflict
		}
		source, exists := preview[copy.SourceDocumentID]
		if !exists || source.Status != DocumentActive {
			return conflictStagePreflight{}, ErrDocumentNotFound
		}
		if revisionByID(source, source.EffectiveRevisionID) == nil {
			return conflictStagePreflight{}, ErrStateCorrupt
		}
		chosenPath, err := normalizePath(copy.ChosenPath)
		if err != nil {
			return conflictStagePreflight{}, err
		}
		if err := pathAvailable(
			preview, copy.DocumentID, chosenPath,
		); err != nil {
			return conflictStagePreflight{}, err
		}
		preview[copy.DocumentID] = Document{
			DocumentID:   copy.DocumentID,
			RelativePath: chosenPath,
			Status:       DocumentActive,
		}
		result.paths[copy.DocumentID] = chosenPath
		if _, exists := result.contents[copy.ChosenObjectID]; !exists {
			content, err := readConflictObjectContent(
				ctx, service.repository, copy.ChosenObjectID,
			)
			if err != nil {
				return conflictStagePreflight{}, err
			}
			result.contents[copy.ChosenObjectID] = content
		}
		revisionID, err := service.newID()
		if err != nil || !validUUID(revisionID) {
			return conflictStagePreflight{}, errors.Join(
				errors.New("filehistory.revision_id_invalid"), err,
			)
		}
		result.revisionIDs[copy.DocumentID] = revisionID
	}
	if err := validatePaths(preview); err != nil {
		return conflictStagePreflight{}, err
	}
	return result, nil
}

func readConflictObjectContent(
	ctx context.Context,
	repository objectrepo.Repository,
	id objectrepo.ObjectID,
) ([]byte, error) {
	reader, err := repository.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return content, nil
}

func (applier *ConflictApplier) LoadStage(
	ctx context.Context,
	stageID string,
) (ConflictStage, error) {
	return applier.loadStage(ctx, stageID)
}

func optionalMimeType(chosen string, fallback string) string {
	if strings.TrimSpace(chosen) != "" {
		return chosen
	}
	return fallback
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

func (applier *ConflictApplier) loadStageByOperationID(
	ctx context.Context,
	operationID string,
) (ConflictStage, error) {
	var raw []byte
	err := applier.store.db.QueryRowContext(ctx, `
		SELECT stage_json FROM filehistory_conflict_stages
		WHERE operation_id = ?`,
		operationID,
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
	var result sql.Result
	if expected.Revision == 0 {
		result, err = tx.ExecContext(ctx, `
			INSERT INTO filehistory_heads (
				workspace_id, root_manifest_id, revision,
				mutation_revision, session_epoch, fence_epoch, claim_id
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id) DO NOTHING`,
			next.WorkspaceID, next.Root, next.Revision,
			next.MutationRevision, next.SessionEpoch,
			next.FenceEpoch, next.ClaimID,
		)
	} else {
		result, err = tx.ExecContext(ctx, `
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
	}
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
	if len(stage.External) != 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO filehistory_external_conflict_proofs(
				mutation_revision, stage_id, operation_id
			) VALUES (?, ?, ?)`,
			next.MutationRevision,
			stage.StageID,
			stage.OperationID,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO filehistory_external_conflict_journal(
				stage_id, phase
			) VALUES (?, 'committed')
			ON CONFLICT(stage_id) DO UPDATE SET phase = 'committed'`,
			stage.StageID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (applier *ConflictApplier) ExternalStarted(
	ctx context.Context,
	stageID string,
) (bool, error) {
	var phase string
	err := applier.store.db.QueryRowContext(ctx, `
		SELECT phase FROM filehistory_external_conflict_journal
		WHERE stage_id = ?`,
		stageID,
	).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return phase == "started" || phase == "committed", nil
}

func (applier *ConflictApplier) MarkExternalStarted(
	ctx context.Context,
	stageID string,
) error {
	result, err := applier.store.db.ExecContext(ctx, `
		INSERT INTO filehistory_external_conflict_journal(
			stage_id, phase
		) VALUES (?, 'started')
		ON CONFLICT(stage_id) DO NOTHING`,
		stageID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var phase string
	if err := applier.store.db.QueryRowContext(ctx, `
		SELECT phase FROM filehistory_external_conflict_journal
		WHERE stage_id = ?`,
		stageID,
	).Scan(&phase); err != nil {
		return err
	}
	if phase != "started" && phase != "committed" {
		return ErrStateCorrupt
	}
	return nil
}

func (store *SQLiteHeadStore) HasExternalConflictProof(
	ctx context.Context,
	mutationRevision uint64,
) (bool, error) {
	if store == nil || store.db == nil || mutationRevision == 0 {
		return false, nil
	}
	var tableCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table'
		  AND name = 'filehistory_external_conflict_proofs'`,
	).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount == 0 {
		return false, nil
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM filehistory_external_conflict_proofs
		WHERE mutation_revision = ?`,
		mutationRevision,
	).Scan(&count); err != nil {
		return false, err
	}
	if count < 0 || count > 1 {
		return false, ErrStateCorrupt
	}
	return count == 1, nil
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
