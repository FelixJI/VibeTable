package workspacev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

const snapshotExtractPlanTTL = 10 * time.Minute

func (runtime *Runtime) registerSnapshotExtractHandlers() {
	runtime.dispatcher.Register(
		"snapshot.previewExtract",
		protocolv2.WorkspaceScope,
		runtime.previewSnapshotExtract,
	)
	runtime.dispatcher.Register(
		"snapshot.applyExtract",
		protocolv2.WorkspaceScope,
		runtime.applySnapshotExtract,
	)
	runtime.dispatcher.Register(
		"repository.verify",
		protocolv2.WorkspaceScope,
		runtime.verifyRepository,
	)
}

type previewSnapshotExtractParams struct {
	SnapshotID string `json:"snapshotId"`
	DocumentID string `json:"documentId"`
}

func (runtime *Runtime) previewSnapshotExtract(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[previewSnapshotExtractParams](paramsRaw)
	if err != nil ||
		!validUUID(params.SnapshotID) ||
		!validUUID(params.DocumentID) {
		return nil, errors.New("snapshot.request_invalid")
	}
	record, err := runtime.snapshotRecord(ctx, params.SnapshotID)
	if err != nil {
		return nil, err
	}
	if valid, err := runtime.snapshotIntegrity(ctx, record); err != nil {
		return nil, err
	} else if !valid {
		return nil, errors.New("snapshot.integrity_failed")
	}
	document, revision, err := runtime.snapshotFileDocument(
		ctx,
		record.ObjectMap,
		params.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(snapshotExtractPlanTTL)
	plan := snapshotExtractPlan{
		PlanID:          uuid.NewString(),
		SnapshotID:      record.SnapshotID,
		CatalogRevision: record.CatalogRevision,
		DocumentID:      document.DocumentID,
		RevisionID:      revision.RevisionID,
		RelativePath:    document.RelativePath,
		ObjectID:        string(revision.ObjectID),
		ContentHash:     revision.ContentHash,
		ContentSize:     revision.Size,
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
	}
	result := map[string]any{
		"planId":      plan.PlanID,
		"displayName": filepath.Base(plan.RelativePath),
		"size":        plan.ContentSize,
		"expiresAt":   expiresAt.Format(time.RFC3339Nano),
	}
	var putErr error
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, result)
		if receiptErr != nil {
			return nil, receiptErr
		}
		putErr = runtime.state.
			putSnapshotExtractPlanWithOperationReceipt(
				ctx,
				plan,
				operation.Session,
				receipt,
			)
	} else {
		putErr = runtime.state.putSnapshotExtractPlan(ctx, plan)
	}
	if putErr != nil {
		return nil, putErr
	}
	return result, nil
}

type applySnapshotExtractParams struct {
	PlanID    string `json:"planId"`
	PathGrant string `json:"pathGrant"`
}

func (runtime *Runtime) applySnapshotExtract(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrictWorkspaceWire(wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[applySnapshotExtractParams](paramsRaw)
	if err != nil || !validUUID(params.PlanID) || params.PathGrant == "" {
		return nil, errors.New("snapshot.request_invalid")
	}
	target, err := consumePathGrant(
		ctx,
		params.PathGrant,
		"snapshot.applyExtract",
		wire.OperationID,
		"snapshot-extract",
	)
	if err != nil {
		return nil, err
	}
	if err := validateExportTarget(target); err != nil {
		return nil, err
	}
	plan, err := runtime.state.snapshotExtractPlan(ctx, params.PlanID)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil {
		return nil, errors.New("snapshot.extract_plan_corrupt")
	}
	if !time.Now().UTC().Before(expiresAt) {
		_ = runtime.state.deleteSnapshotExtractPlan(
			context.WithoutCancel(ctx),
			plan.PlanID,
		)
		return nil, errors.New("snapshot.extract_plan_expired")
	}
	record, err := runtime.snapshotRecord(ctx, plan.SnapshotID)
	if err != nil {
		return nil, err
	}
	if record.CatalogRevision != plan.CatalogRevision {
		return nil, errors.New("snapshot.extract_plan_stale")
	}
	document, revision, err := runtime.snapshotFileDocument(
		ctx,
		record.ObjectMap,
		plan.DocumentID,
	)
	if err != nil {
		return nil, err
	}
	if document.RelativePath != plan.RelativePath ||
		revision.RevisionID != plan.RevisionID ||
		string(revision.ObjectID) != plan.ObjectID ||
		revision.ContentHash != plan.ContentHash ||
		revision.Size != plan.ContentSize {
		return nil, errors.New("snapshot.extract_plan_stale")
	}
	reader, err := runtime.repository.Open(
		ctx,
		objectrepo.ObjectID(plan.ObjectID),
	)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	output, err := os.CreateTemp(
		filepath.Dir(target),
		"."+filepath.Base(target)+".*.tmp",
	)
	if err != nil {
		return nil, err
	}
	outputPath := output.Name()
	defer os.Remove(outputPath)
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		return nil, err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(output, hasher),
		io.LimitReader(reader, plan.ContentSize+1),
	)
	if copyErr != nil ||
		written != plan.ContentSize ||
		"sha256:"+hex.EncodeToString(hasher.Sum(nil)) != plan.ContentHash {
		_ = output.Close()
		return nil, errors.Join(
			errors.New("snapshot.extract_content_invalid"),
			copyErr,
		)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return nil, err
	}
	if err := output.Close(); err != nil {
		return nil, err
	}
	result := map[string]any{
		"operationId": wire.OperationID,
		"state":       "completed",
	}
	var externalOperation *externalFileOperation
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, result)
		if receiptErr != nil {
			return nil, receiptErr
		}
		prepared := externalFileOperation{
			Receipt:     receipt,
			Session:     operation.Session,
			Staging:     filepath.Clean(outputPath),
			Target:      filepath.Clean(target),
			ContentHash: plan.ContentHash,
			ContentSize: plan.ContentSize,
		}
		if err := runtime.state.prepareExternalFileOperation(
			ctx,
			prepared,
		); err != nil {
			return nil, err
		}
		externalOperation = &prepared
	}
	if err := replaceGrantedFile(outputPath, target); err != nil {
		return nil, err
	}
	if externalOperation != nil {
		externalOperation.State = "prepared"
		if err := runtime.state.completeExternalFileOperation(
			context.WithoutCancel(ctx),
			*externalOperation,
		); err != nil {
			return nil, err
		}
	}
	_ = runtime.state.deleteSnapshotExtractPlan(
		context.WithoutCancel(ctx),
		plan.PlanID,
	)
	return result, nil
}

func (runtime *Runtime) snapshotFileDocument(
	ctx context.Context,
	objectMap map[string]objectrepo.ObjectID,
	documentID string,
) (filehistory.Document, filehistory.Revision, error) {
	historyRoot, err := runtime.snapshotFileHistoryRoot(ctx, objectMap)
	if err != nil {
		return filehistory.Document{}, filehistory.Revision{}, err
	}
	source, err := filehistory.Open(
		ctx,
		runtime.repository,
		runtime.coordinator,
		historyRoot,
	)
	if err != nil {
		return filehistory.Document{}, filehistory.Revision{}, err
	}
	document, err := source.Inspect(documentID)
	if err != nil {
		return filehistory.Document{}, filehistory.Revision{}, err
	}
	if document.Status != filehistory.DocumentActive ||
		document.EffectiveRevisionID == "" {
		return filehistory.Document{}, filehistory.Revision{},
			errors.New("snapshot.file_not_extractable")
	}
	for _, revision := range document.Revisions {
		if revision.RevisionID == document.EffectiveRevisionID {
			if objectMap["file:"+document.RelativePath] !=
				revision.ObjectID {
				return filehistory.Document{}, filehistory.Revision{},
					errors.New("snapshot.file_state_mismatch")
			}
			return document, revision, nil
		}
	}
	return filehistory.Document{}, filehistory.Revision{},
		errors.New("snapshot.file_state_mismatch")
}

func (runtime *Runtime) snapshotFileHistoryRoot(
	ctx context.Context,
	objectMap map[string]objectrepo.ObjectID,
) (objectrepo.ManifestID, error) {
	fileStateID := objectMap["file-state-root"]
	if fileStateID == "" {
		return "", errors.New("snapshot.file_root_missing")
	}
	reader, err := runtime.repository.Open(ctx, fileStateID)
	if err != nil {
		return "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", err
	}
	var root struct {
		FormatVersion int                   `json:"formatVersion"`
		SourceRoot    objectrepo.ManifestID `json:"sourceRoot"`
	}
	if err := json.Unmarshal(raw, &root); err != nil ||
		root.FormatVersion != 1 ||
		root.SourceRoot == "" {
		return "", errors.New("snapshot.file_root_invalid")
	}
	head, err := runtime.repository.GetManifest(ctx, root.SourceRoot)
	if err != nil {
		return "", err
	}
	if head.Name != "file-state-head" ||
		head.Labels["type"] != "file-state-head" {
		return "", errors.New("snapshot.file_root_invalid")
	}
	var sourceHead struct {
		FormatVersion int                   `json:"formatVersion"`
		WorkspaceID   string                `json:"workspaceId"`
		HistoryRoot   objectrepo.ManifestID `json:"historyRoot"`
	}
	if err := json.Unmarshal(head.Payload, &sourceHead); err != nil ||
		sourceHead.FormatVersion != 1 ||
		sourceHead.WorkspaceID != runtime.manifest.WorkspaceID ||
		sourceHead.HistoryRoot == "" {
		return "", errors.New("snapshot.file_root_invalid")
	}
	return sourceHead.HistoryRoot, nil
}

func (runtime *Runtime) verifyRepository(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	var params struct{}
	if paramsRaw == nil {
		return nil, errors.New("snapshot.request_invalid")
	}
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("snapshot.request_invalid")
	}
	_ = params
	records, err := runtime.catalog.List(
		ctx,
		runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return nil, err
	}
	tombstoned, err := runtime.retention.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		return nil, err
	}
	corrupt := make([]string, 0)
	objects := map[objectrepo.ObjectID]struct{}{}
	snapshotCount := 0
	for _, record := range records {
		if _, deleted := tombstoned[record.SnapshotID]; deleted {
			continue
		}
		snapshotCount++
		reachabilityRoots, reachabilityErr :=
			snapshot.ReachabilityObjectIDs(
				ctx,
				runtime.repository,
				record,
			)
		if reachabilityErr != nil {
			return nil, reachabilityErr
		}
		for _, id := range reachabilityRoots {
			objects[id] = struct{}{}
		}
		valid, verifyErr := runtime.snapshotIntegrity(ctx, record)
		if verifyErr != nil {
			return nil, verifyErr
		}
		if !valid {
			corrupt = append(corrupt, record.SnapshotID)
		}
	}
	state := "verified"
	if len(corrupt) != 0 {
		state = "corrupt"
	}
	return map[string]any{
		"state":              state,
		"snapshotCount":      snapshotCount,
		"objectCount":        len(objects),
		"corruptSnapshotIds": corrupt,
	}, nil
}
