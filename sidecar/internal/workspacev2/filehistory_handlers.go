package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

func (runtime *Runtime) registerFileHistoryHandlers() {
	runtime.dispatcher.Register(
		"fileHistory.import",
		protocolv2.WorkspaceScope,
		runtime.importFileDocument,
	)
	runtime.dispatcher.Register(
		"fileHistory.listDocuments",
		protocolv2.WorkspaceScope,
		runtime.listFileDocuments,
	)
	runtime.dispatcher.Register(
		"fileHistory.listPendingChanges",
		protocolv2.WorkspaceScope,
		runtime.listPendingFileChanges,
	)
	runtime.dispatcher.Register(
		"fileHistory.applyPendingChange",
		protocolv2.WorkspaceScope,
		runtime.applyPendingFileChange,
	)
	runtime.dispatcher.Register(
		"fileHistory.relink",
		protocolv2.WorkspaceScope,
		runtime.relinkFileDocument,
	)
	runtime.dispatcher.Register(
		"fileHistory.unlink",
		protocolv2.WorkspaceScope,
		runtime.unlinkFileDocument,
	)
	runtime.dispatcher.Register(
		"fileHistory.restore",
		protocolv2.WorkspaceScope,
		runtime.restoreFileRevision,
	)
	runtime.dispatcher.Register(
		"fileHistory.upgrade",
		protocolv2.WorkspaceScope,
		runtime.upgradeFileRevision,
	)
	runtime.dispatcher.Register(
		"fileHistory.activateLeaf",
		protocolv2.WorkspaceScope,
		runtime.activateFileLeaf,
	)
}

func (runtime *Runtime) listPendingFileChanges(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("file_history.request_invalid")
	}
	changes, err := runtime.state.listPendingFileChanges(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"changes": changes}, nil
}

type applyPendingFileChangeParams struct {
	ChangeID                  string  `json:"changeId"`
	Action                    string  `json:"action"`
	DocumentID                *string `json:"documentId"`
	ExpectedEffectiveRevision *string `json:"expectedEffectiveRevisionId"`
}

func (runtime *Runtime) applyPendingFileChange(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[applyPendingFileChangeParams](paramsRaw)
	if err != nil || !validUUID(params.ChangeID) {
		return nil, errors.New("file_history.request_invalid")
	}
	pending, err := runtime.state.pendingFileChange(
		ctx,
		params.ChangeID,
	)
	if err != nil {
		return nil, err
	}
	if params.Action == "dismiss" {
		if params.DocumentID != nil ||
			params.ExpectedEffectiveRevision != nil {
			return nil, errors.New("file_history.request_invalid")
		}
		result := map[string]any{
			"changeId": params.ChangeID,
			"state":    "dismissed",
			"document": nil,
		}
		var deleteErr error
		if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
			receipt, receiptErr :=
				protocolv2.BuildContextOperationReceipt(ctx, result)
			if receiptErr != nil {
				return nil, receiptErr
			}
			deleteErr =
				runtime.state.deletePendingFileChangeWithOperationReceipt(
					ctx,
					params.ChangeID,
					operation.Session,
					receipt,
				)
		} else {
			deleteErr = runtime.state.deletePendingFileChange(
				ctx,
				params.ChangeID,
			)
		}
		if deleteErr != nil {
			return nil, deleteErr
		}
		return result, nil
	}
	var content []byte
	if pending.Missing {
		if _, readErr := runtime.watcher.ReadStable(
			ctx,
			pending.RelativePath,
		); readErr == nil || !errors.Is(readErr, os.ErrNotExist) {
			return nil, errors.New(
				"file_history.pending_change_stale",
			)
		}
	} else {
		content, err = runtime.watcher.ReadStable(
			ctx,
			pending.RelativePath,
		)
		if err != nil ||
			int64(len(content)) != pending.ObservedSize ||
			digestBytes(content) != pending.ObservedHash {
			return nil, errors.Join(
				errors.New("file_history.pending_change_stale"),
				err,
			)
		}
	}
	token, _ := runtime.coordinator.Current()
	var document filehistory.Document
	switch params.Action {
	case "new":
		if pending.Missing || params.DocumentID != nil ||
			params.ExpectedEffectiveRevision != nil {
			return nil, errors.New("file_history.request_invalid")
		}
		documentID := uuid.NewString()
		mutationContext, bindErr := pendingChangeReceiptContext(
			ctx,
			params.ChangeID,
			documentID,
		)
		if bindErr != nil {
			return nil, bindErr
		}
		result, applyErr := runtime.ingestor.Ingest(
			mutationContext,
			filehistory.ExternalChange{
				Token: token, Kind: filehistory.ExternalStableSave,
				DocumentID: documentID,
				TargetPath: pending.RelativePath,
				Content:    content, ContentProvided: true,
				RevisionKind: filehistory.RevisionAutosave,
				MimeType:     pendingMimeType(pending.RelativePath),
				CreatedBy:    "pending-change:" + params.ChangeID,
				DeviceID:     token.ClaimID,
				Comment:      "Confirmed as a new external file",
			},
		)
		if applyErr != nil || result.Save == nil {
			return nil, errors.Join(
				errors.New("file_history.pending_change_apply_failed"),
				applyErr,
			)
		}
		document = result.Save.Document
	case "move", "copy", "delete":
		if params.DocumentID == nil ||
			params.ExpectedEffectiveRevision == nil ||
			!validUUID(*params.DocumentID) ||
			!validUUID(*params.ExpectedEffectiveRevision) ||
			!containsString(
				pending.CandidateDocumentIDs,
				*params.DocumentID,
			) {
			return nil, errors.New("file_history.request_invalid")
		}
		source, inspectErr := runtime.history.Inspect(
			*params.DocumentID,
		)
		if inspectErr != nil ||
			source.EffectiveRevisionID !=
				*params.ExpectedEffectiveRevision {
			return nil, errors.Join(
				filehistory.ErrRevisionConflict,
				inspectErr,
			)
		}
		if params.Action == "delete" {
			if !pending.Missing {
				return nil, errors.New("file_history.request_invalid")
			}
			mutationContext, bindErr := pendingChangeReceiptContext(
				ctx,
				params.ChangeID,
				source.DocumentID,
			)
			if bindErr != nil {
				return nil, bindErr
			}
			result, applyErr := runtime.history.Delete(
				mutationContext,
				token,
				source.DocumentID,
				params.ExpectedEffectiveRevision,
			)
			if applyErr != nil {
				return nil, applyErr
			}
			document = result.Document
			break
		}
		if pending.Missing {
			return nil, errors.New("file_history.request_invalid")
		}
		var effectiveHash string
		for index := range source.Revisions {
			if source.Revisions[index].RevisionID ==
				source.EffectiveRevisionID {
				effectiveHash = source.Revisions[index].ContentHash
				break
			}
		}
		if effectiveHash == "" ||
			effectiveHash != pending.ObservedHash {
			return nil, errors.New(
				"file_history.pending_change_identity_mismatch",
			)
		}
		if params.Action == "move" {
			mutationContext, bindErr := pendingChangeReceiptContext(
				ctx,
				params.ChangeID,
				source.DocumentID,
			)
			if bindErr != nil {
				return nil, bindErr
			}
			result, applyErr := runtime.history.Rename(
				mutationContext,
				token,
				source.DocumentID,
				pending.RelativePath,
				params.ExpectedEffectiveRevision,
			)
			if applyErr != nil {
				return nil, applyErr
			}
			document = result.Document
		} else {
			newDocumentID := uuid.NewString()
			mutationContext, bindErr := pendingChangeReceiptContext(
				ctx,
				params.ChangeID,
				newDocumentID,
			)
			if bindErr != nil {
				return nil, bindErr
			}
			result, applyErr := runtime.ingestor.Ingest(
				mutationContext,
				filehistory.ExternalChange{
					Token: token, Kind: filehistory.ExternalCopy,
					SourceDocumentID: source.DocumentID,
					NewDocumentID:    newDocumentID,
					SourcePath:       source.RelativePath,
					TargetPath:       pending.RelativePath,
					Content:          content,
					ContentProvided:  true,
					MimeType:         pendingMimeType(pending.RelativePath),
					CreatedBy:        "pending-change:" + params.ChangeID,
					DeviceID:         token.ClaimID,
					Comment:          "Confirmed as an external copy",
				},
			)
			if applyErr != nil || result.Save == nil {
				return nil, errors.Join(
					errors.New(
						"file_history.pending_change_apply_failed",
					),
					applyErr,
				)
			}
			document = result.Save.Document
		}
	default:
		return nil, errors.New("file_history.request_invalid")
	}
	_ = runtime.state.deletePendingFileChange(
		context.WithoutCancel(ctx),
		params.ChangeID,
	)
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	projected, err := projectFileDocument(document)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"changeId": params.ChangeID,
		"state":    "applied",
		"document": projected,
	}, nil
}

func pendingMimeType(relative string) string {
	value := mime.TypeByExtension(filepath.Ext(relative))
	if value == "" {
		return "application/octet-stream"
	}
	return value
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type relinkFileDocumentParams struct {
	DocumentID                string `json:"documentId"`
	ExpectedEffectiveRevision string `json:"expectedEffectiveRevisionId"`
	PathGrant                 string `json:"pathGrant"`
}

func (runtime *Runtime) relinkFileDocument(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, params, err := decodeFileHistoryRequest[relinkFileDocumentParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.ExpectedEffectiveRevision) ||
		params.PathGrant == "" {
		return nil, errors.New("file_history.request_invalid")
	}
	sourcePath, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"fileHistory.relink",
		wire.OperationID,
		"file-relink",
	)
	if err != nil {
		return nil, err
	}
	document, err := runtime.history.Inspect(params.DocumentID)
	if err != nil {
		return nil, err
	}
	if document.Status != filehistory.DocumentActive ||
		document.EffectiveRevisionID !=
			params.ExpectedEffectiveRevision {
		return nil, filehistory.ErrRevisionConflict
	}
	content, err := readFileBounded(sourcePath, maxSnapshotWorkingSet)
	if err != nil {
		return nil, err
	}
	mimeType := mime.TypeByExtension(
		filepath.Ext(document.RelativePath),
	)
	if mimeType == "" {
		for index := range document.Revisions {
			if document.Revisions[index].RevisionID ==
				document.EffectiveRevisionID {
				mimeType = document.Revisions[index].MimeType
				break
			}
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	token, _ := runtime.coordinator.Current()
	mutationContext, err := fileDocumentReceiptContext(
		ctx,
		document.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.Save(mutationContext, filehistory.SaveRequest{
		Token:                     token,
		DocumentID:                document.DocumentID,
		Path:                      document.RelativePath,
		ExpectedEffectiveRevision: &params.ExpectedEffectiveRevision,
		Kind:                      filehistory.RevisionFormal,
		Content:                   content,
		MimeType:                  mimeType,
		CreatedBy:                 "operation:" + wire.OperationID,
		DeviceID:                  token.ClaimID,
		Comment:                   "Relinked external file",
	})
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return projectFileDocument(result.Document)
}

type listFileDocumentsParams struct {
	IncludeDeleted *bool `json:"includeDeleted"`
}

func (runtime *Runtime) listFileDocuments(
	_ context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[listFileDocumentsParams](paramsRaw)
	if err != nil || params.IncludeDeleted == nil {
		return nil, errors.New("file_history.request_invalid")
	}
	const maximumDocuments = 10_000
	source := runtime.history.List()
	if len(source) > maximumDocuments {
		return nil, errors.New("file_history.document_limit")
	}
	documents := make([]contractsv2.FileDocument, 0, len(source))
	for _, document := range source {
		if document.Status == filehistory.DocumentDeleted &&
			!*params.IncludeDeleted {
			continue
		}
		projection, err := projectFileDocument(document)
		if err != nil {
			return nil, errors.Join(filehistory.ErrStateCorrupt, err)
		}
		documents = append(documents, projection)
	}
	return map[string]any{"documents": documents}, nil
}

type importFileDocumentParams struct {
	PathGrant    string `json:"pathGrant"`
	RelativePath string `json:"relativePath"`
	MimeType     string `json:"mimeType"`
}

func (runtime *Runtime) importFileDocument(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, params, err := decodeFileHistoryRequest[importFileDocumentParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil || params.PathGrant == "" ||
		params.RelativePath == "" || params.MimeType == "" {
		return nil, errors.New("file_history.request_invalid")
	}
	sourcePath, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"fileHistory.import",
		wire.OperationID,
		"file-import",
	)
	if err != nil {
		return nil, err
	}
	content, err := readFileBounded(sourcePath, maxSnapshotWorkingSet)
	if err != nil {
		return nil, err
	}
	token, _ := runtime.coordinator.Current()
	documentID := uuid.NewString()
	mutationContext, err := fileDocumentReceiptContext(ctx, documentID)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.Save(mutationContext, filehistory.SaveRequest{
		Token:      token,
		DocumentID: documentID,
		Path:       params.RelativePath,
		Kind:       filehistory.RevisionFormal,
		Content:    content,
		MimeType:   params.MimeType,
		CreatedBy:  "operation:" + wire.OperationID,
		DeviceID:   token.ClaimID,
		Comment:    "Imported into workspace",
	})
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return projectFileDocument(result.Document)
}

type unlinkFileDocumentParams struct {
	DocumentID                string `json:"documentId"`
	ExpectedEffectiveRevision string `json:"expectedEffectiveRevisionId"`
}

func (runtime *Runtime) unlinkFileDocument(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	_, params, err := decodeFileHistoryRequest[unlinkFileDocumentParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.ExpectedEffectiveRevision) {
		return nil, errors.New("file_history.request_invalid")
	}
	token, _ := runtime.coordinator.Current()
	mutationContext, err := fileDocumentReceiptContext(
		ctx,
		params.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.Delete(
		mutationContext,
		token,
		params.DocumentID,
		&params.ExpectedEffectiveRevision,
	)
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return projectFileDocument(result.Document)
}

type restoreFileRevisionParams struct {
	DocumentID                string `json:"documentId"`
	ExpectedEffectiveRevision string `json:"expectedEffectiveRevisionId"`
	HistoricalRevisionID      string `json:"historicalRevisionId"`
}

func (runtime *Runtime) restoreFileRevision(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, params, err := decodeFileHistoryRequest[restoreFileRevisionParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.ExpectedEffectiveRevision) ||
		!validUUID(params.HistoricalRevisionID) {
		return nil, errors.New("file_history.request_invalid")
	}
	token, _ := runtime.coordinator.Current()
	mutationContext, err := fileRevisionReceiptContext(
		ctx,
		params.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.Restore(
		mutationContext,
		filehistory.RestoreRequest{
			Token:                     token,
			DocumentID:                params.DocumentID,
			TargetRevisionID:          params.HistoricalRevisionID,
			ExpectedEffectiveRevision: &params.ExpectedEffectiveRevision,
			CreatedBy:                 "operation:" + wire.OperationID,
			DeviceID:                  token.ClaimID,
			Comment:                   "Restored from file history",
		},
	)
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return fileRevisionResult(result.Revision)
}

type upgradeFileRevisionParams struct {
	DocumentID string `json:"documentId"`
	RevisionID string `json:"revisionId"`
	PathGrant  string `json:"pathGrant"`
}

func (runtime *Runtime) upgradeFileRevision(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, params, err := decodeFileHistoryRequest[upgradeFileRevisionParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.RevisionID) ||
		params.PathGrant == "" {
		return nil, errors.New("file_history.request_invalid")
	}
	sourcePath, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"fileHistory.upgrade",
		wire.OperationID,
		"file-upgrade",
	)
	if err != nil {
		return nil, err
	}
	document, err := runtime.history.Inspect(params.DocumentID)
	if err != nil {
		return nil, err
	}
	var sourceRevision *filehistory.Revision
	for index := range document.Revisions {
		if document.Revisions[index].RevisionID == params.RevisionID {
			sourceRevision = &document.Revisions[index]
			break
		}
	}
	if sourceRevision == nil {
		return nil, filehistory.ErrRevisionNotFound
	}
	content, err := readFileBounded(sourcePath, maxSnapshotWorkingSet)
	if err != nil {
		return nil, err
	}
	mimeType := mime.TypeByExtension(filepath.Ext(sourcePath))
	if mimeType == "" {
		mimeType = sourceRevision.MimeType
	}
	token, _ := runtime.coordinator.Current()
	mutationContext, err := fileRevisionReceiptContext(
		ctx,
		document.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.Save(mutationContext, filehistory.SaveRequest{
		Token:                     token,
		DocumentID:                document.DocumentID,
		Path:                      document.RelativePath,
		ParentRevisionID:          params.RevisionID,
		ExpectedEffectiveRevision: &document.EffectiveRevisionID,
		Kind:                      filehistory.RevisionFormal,
		Content:                   content,
		MimeType:                  mimeType,
		CreatedBy:                 "operation:" + wire.OperationID,
		DeviceID:                  token.ClaimID,
		Comment:                   "Upgraded to a formal file revision",
	})
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return fileRevisionResult(result.Revision)
}

type activateFileLeafParams struct {
	DocumentID                string `json:"documentId"`
	ExpectedEffectiveRevision string `json:"expectedEffectiveRevisionId"`
	TargetLeafRevisionID      string `json:"targetLeafRevisionId"`
}

func (runtime *Runtime) activateFileLeaf(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	_, params, err := decodeFileHistoryRequest[activateFileLeafParams](
		wireRaw,
		paramsRaw,
	)
	if err != nil ||
		!validUUID(params.DocumentID) ||
		!validUUID(params.ExpectedEffectiveRevision) ||
		!validUUID(params.TargetLeafRevisionID) {
		return nil, errors.New("file_history.request_invalid")
	}
	token, _ := runtime.coordinator.Current()
	mutationContext, err := fileHistoryReceiptContext(
		ctx,
		func(publication filehistory.Publication) (any, error) {
			document, err := publicationDocument(
				publication,
				params.DocumentID,
			)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"revisionId": params.TargetLeafRevisionID,
				"effective": document.EffectiveRevisionID ==
					params.TargetLeafRevisionID,
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	result, err := runtime.history.ActivateLeaf(
		mutationContext,
		token,
		params.DocumentID,
		params.TargetLeafRevisionID,
		&params.ExpectedEffectiveRevision,
	)
	if err != nil {
		return nil, err
	}
	runtime.tryDrainFileHistoryAudit(context.WithoutCancel(ctx))
	return map[string]any{
		"revisionId": params.TargetLeafRevisionID,
		"effective": result.Document.EffectiveRevisionID ==
			params.TargetLeafRevisionID,
	}, nil
}

func decodeFileHistoryRequest[T any](
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (contractsv2.WorkspaceWireScope, T, error) {
	wire, err := decodeStrict[contractsv2.WorkspaceWireScope](wireRaw)
	if err != nil {
		var zero T
		return contractsv2.WorkspaceWireScope{}, zero, err
	}
	params, err := decodeStrict[T](paramsRaw)
	return wire, params, err
}

func fileRevisionResult(revision filehistory.Revision) (map[string]any, error) {
	if revision.RevisionOrdinal == 0 {
		if revision.LocalSequence == nil ||
			*revision.LocalSequence == 0 ||
			revision.FormalVersion != nil {
			return nil, errors.New("file_history.provisional_revision_invalid")
		}
	} else if revision.FormalVersion == nil {
		return nil, errors.New("file_history.formal_revision_required")
	}
	var formalVersion any
	if revision.FormalVersion != nil {
		formalVersion = *revision.FormalVersion
	}
	var localSequence any
	if revision.LocalSequence != nil {
		localSequence = *revision.LocalSequence
	}
	return map[string]any{
		"revisionId":      revision.RevisionID,
		"revisionOrdinal": revision.RevisionOrdinal,
		"localSequence":   localSequence,
		"formalVersion":   formalVersion,
	}, nil
}

func fileHistoryReceiptContext(
	ctx context.Context,
	project func(filehistory.Publication) (any, error),
) (context.Context, error) {
	return filehistory.WithOperationReceiptBuilder(
		ctx,
		func(
			publication filehistory.Publication,
		) (protocolv2.OperationReceipt, error) {
			result, err := project(publication)
			if err != nil {
				return protocolv2.OperationReceipt{}, err
			}
			return protocolv2.BuildContextOperationReceipt(ctx, result)
		},
	)
}

func fileDocumentReceiptContext(
	ctx context.Context,
	documentID string,
) (context.Context, error) {
	return fileHistoryReceiptContext(
		ctx,
		func(publication filehistory.Publication) (any, error) {
			document, err := publicationDocument(publication, documentID)
			if err != nil {
				return nil, err
			}
			return projectFileDocument(document)
		},
	)
}

func fileRevisionReceiptContext(
	ctx context.Context,
	documentID string,
) (context.Context, error) {
	return fileHistoryReceiptContext(
		ctx,
		func(publication filehistory.Publication) (any, error) {
			document, err := publicationDocument(publication, documentID)
			if err != nil {
				return nil, err
			}
			for index := range document.Revisions {
				revision := document.Revisions[index]
				if revision.RevisionID == document.EffectiveRevisionID {
					return fileRevisionResult(revision)
				}
			}
			return nil, errors.New("filehistory.state_corrupt")
		},
	)
}

func pendingChangeReceiptContext(
	ctx context.Context,
	changeID string,
	documentID string,
) (context.Context, error) {
	return fileHistoryReceiptContext(
		ctx,
		func(publication filehistory.Publication) (any, error) {
			document, err := publicationDocument(publication, documentID)
			if err != nil {
				return nil, err
			}
			projected, err := projectFileDocument(document)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"changeId": changeID,
				"state":    "applied",
				"document": projected,
			}, nil
		},
	)
}

func publicationDocument(
	publication filehistory.Publication,
	documentID string,
) (filehistory.Document, error) {
	for _, document := range publication.Documents {
		if document.DocumentID == documentID {
			return document, nil
		}
	}
	return filehistory.Document{}, filehistory.ErrDocumentNotFound
}

func projectFileDocument(
	document filehistory.Document,
) (contractsv2.FileDocument, error) {
	var effective *string
	if document.EffectiveRevisionID != "" {
		value := document.EffectiveRevisionID
		effective = &value
	}
	projection := contractsv2.FileDocument{
		ContractVersion:     contractsv2.ContractVersion,
		DocumentID:          document.DocumentID,
		WorkspaceID:         document.WorkspaceID,
		RelativePath:        document.RelativePath,
		Status:              string(document.Status),
		EffectiveRevisionID: effective,
		NextRevisionOrdinal: document.NextRevisionOrdinal,
		NextFormalVersion:   document.NextFormalVersion,
	}
	return projection, projection.Validate()
}
