package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	"github.com/vibetable/vibetable/sidecar/internal/retention"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func (runtime *Runtime) registerHandlers() {
	runtime.registerFileHistoryHandlers()
	runtime.registerSnapshotExportHandler()
	runtime.registerSnapshotExtractHandlers()
	runtime.registerSnapshotRestoreHandlers()
	runtime.registerRepositoryRotationHandlers()
	if runtime.retention != nil {
		runtime.retention.register(runtime)
	}
	if runtime.replicaConflict != nil {
		runtime.replicaConflict.register()
	}
	runtime.dispatcher.Register(
		"snapshot.request",
		protocolv2.WorkspaceScope,
		runtime.requestSnapshot,
	)
	runtime.dispatcher.Register(
		"snapshot.list",
		protocolv2.WorkspaceScope,
		runtime.listSnapshots,
	)
	runtime.dispatcher.Register(
		"snapshot.inspect",
		protocolv2.WorkspaceScope,
		runtime.inspectSnapshot,
	)
	runtime.dispatcher.Register(
		"snapshot.update",
		protocolv2.WorkspaceScope,
		runtime.updateSnapshot,
	)
	runtime.dispatcher.Register(
		"fileHistory.readTree",
		protocolv2.WorkspaceScope,
		runtime.readFileTree,
	)
	runtime.dispatcher.Register(
		"retention.get",
		protocolv2.WorkspaceScope,
		runtime.getRetention,
	)
	runtime.dispatcher.Register(
		"retention.status",
		protocolv2.WorkspaceScope,
		runtime.getRetentionStatus,
	)
	runtime.dispatcher.Register(
		"retention.update",
		protocolv2.WorkspaceScope,
		runtime.updateRetention,
	)
}

type updateSnapshotParams struct {
	SnapshotID              string `json:"snapshotId"`
	Action                  string `json:"action"`
	ExpectedCatalogRevision uint64 `json:"expectedCatalogRevision"`
}

func (runtime *Runtime) updateSnapshot(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[updateSnapshotParams](paramsRaw)
	if err != nil ||
		!validUUID(params.SnapshotID) ||
		(params.Action != "pin" && params.Action != "unpin") ||
		params.ExpectedCatalogRevision == 0 {
		return nil, errors.New("snapshot.request_invalid")
	}
	token, _ := runtime.coordinator.Current()
	current, err := runtime.snapshotRecord(ctx, params.SnapshotID)
	if err != nil {
		return nil, err
	}
	valid, err := runtime.snapshotIntegrity(ctx, current)
	if err != nil {
		return nil, err
	}
	state, integrity := "ready", "verified"
	if !valid {
		state, integrity = "corrupt", "corrupt"
	}
	result := map[string]any{
		"snapshotId": current.SnapshotID,
		"state":      state,
		"integrity":  integrity,
	}
	var operationReceipt *protocolv2.OperationReceipt
	if _, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr := protocolv2.BuildContextOperationReceipt(
			ctx,
			result,
		)
		if receiptErr != nil {
			return nil, receiptErr
		}
		operationReceipt = &receipt
	}
	var pinExpiry *time.Time
	if params.Action == "unpin" {
		expiry := time.Now().UTC().Add(24 * time.Hour)
		pinExpiry = &expiry
	}
	replacementPin, err := runtime.repository.Pin(
		ctx,
		token.Authority(),
		current.Objects,
		"snapshot:"+current.SnapshotID,
		pinExpiry,
	)
	if err != nil {
		return nil, err
	}
	keepReplacement := false
	defer func() {
		if !keepReplacement {
			_ = runtime.repository.ReleasePin(
				context.WithoutCancel(ctx),
				token.Authority(),
				replacementPin.PinID,
			)
		}
	}()
	var updated snapshot.Record
	_, err = runtime.coordinator.Write(
		ctx,
		token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			var updateErr error
			if operationReceipt != nil {
				updated, updateErr =
					runtime.catalog.UpdatePinnedWithRootPinAndOperationReceipt(
						ctx,
						runtime.manifest.WorkspaceID,
						params.SnapshotID,
						params.ExpectedCatalogRevision,
						params.Action == "pin",
						replacementPin.PinID,
						intent.MutationRevision,
						intent.Token.SessionEpoch,
						intent.Token.FenceEpoch,
						intent.Token.ClaimID,
						*operationReceipt,
					)
			} else {
				updated, updateErr =
					runtime.catalog.UpdatePinnedWithRootPin(
						ctx,
						runtime.manifest.WorkspaceID,
						params.SnapshotID,
						params.ExpectedCatalogRevision,
						params.Action == "pin",
						replacementPin.PinID,
						intent.MutationRevision,
						intent.Token.SessionEpoch,
						intent.Token.FenceEpoch,
						intent.Token.ClaimID,
					)
			}
			return updateErr
		},
	)
	if err != nil {
		return nil, err
	}
	keepReplacement = true
	// Publishing the replacement pin before the catalog CAS makes every crash
	// point safe: pre-CAS leaves only an orphan protection pin, post-CAS leaves
	// both old and new roots protected until this best-effort cleanup succeeds.
	_ = runtime.repository.ReleasePin(
		context.WithoutCancel(ctx),
		token.Authority(),
		current.RootPinID,
	)
	result["snapshotId"] = updated.SnapshotID
	return result, nil
}

type requestSnapshotParams struct {
	Trigger string `json:"trigger"`
	Urgency string `json:"urgency"`
}

func (runtime *Runtime) requestSnapshot(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrict[contractsv2.WorkspaceWireScope](wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[requestSnapshotParams](paramsRaw)
	if err != nil {
		return nil, errors.New("workspace.request_invalid")
	}
	var trigger snapshot.Trigger
	switch params.Trigger {
	case "manual":
		trigger = snapshot.TriggerManual
	case "automatic":
		trigger = snapshot.TriggerAutomatic
	case "protection":
		trigger = snapshot.TriggerProtection
	default:
		return nil, errors.New("workspace.request_invalid")
	}
	if trigger == snapshot.TriggerAutomatic {
		if err := runtime.ensureAutomaticSnapshotWithinLimit(ctx); err != nil {
			return nil, err
		}
	}
	if params.Urgency != "foreground" && params.Urgency != "background" {
		return nil, errors.New("workspace.request_invalid")
	}
	token, _ := runtime.coordinator.Current()
	result := map[string]any{
		"operationId": wire.OperationID,
		"state":       "ready",
	}
	captureContext := ctx
	if _, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		captureContext, err = snapshot.WithOperationReceiptBuilder(
			ctx,
			func(
				snapshot.Record,
			) (protocolv2.OperationReceipt, error) {
				return protocolv2.BuildContextOperationReceipt(ctx, result)
			},
		)
		if err != nil {
			return nil, err
		}
	}
	record, _, err := runtime.snapshots.Capture(
		captureContext,
		snapshot.CaptureRequest{
			WorkspaceID: runtime.manifest.WorkspaceID,
			Authority:   token.Authority(),
			Trigger:     trigger,
			Pinned:      trigger != snapshot.TriggerAutomatic,
		},
	)
	if err != nil {
		return nil, err
	}
	if runtime.scheduler != nil {
		runtime.scheduler.Succeeded(
			time.Now().UTC(),
			record.MutationRevision,
		)
	}
	runtime.enqueueReplicaSnapshots(ctx)
	return result, nil
}

type listSnapshotParams struct {
	Cursor *string `json:"cursor"`
	Limit  int     `json:"limit"`
}

func (runtime *Runtime) listSnapshots(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[listSnapshotParams](paramsRaw)
	if err != nil || params.Limit < 1 || params.Limit > 200 {
		return nil, errors.New("workspace.request_invalid")
	}
	var before uint64
	if params.Cursor != nil {
		before, err = strconv.ParseUint(*params.Cursor, 10, 64)
		if err != nil || before == 0 {
			return nil, errors.New("workspace.request_invalid")
		}
	}
	records, err := runtime.catalog.List(ctx, runtime.manifest.WorkspaceID)
	if err != nil {
		return nil, err
	}
	tombstoned, err := runtime.retention.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		return nil, err
	}
	policy, _, err := runtime.state.retention(ctx)
	if err != nil {
		return nil, err
	}
	inventory, err := runtime.retention.source.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	retentionReasons := retention.SnapshotRetentionReasons(
		inventory,
		retentionPolicy(policy),
		time.Now().UTC(),
	)
	items := make([]map[string]any, 0, params.Limit)
	var nextCursor *string
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if _, deleted := tombstoned[record.SnapshotID]; deleted {
			continue
		}
		if before != 0 && record.SnapshotSequence >= before {
			continue
		}
		if len(items) == params.Limit {
			value := strconv.FormatUint(
				records[index+1].SnapshotSequence,
				10,
			)
			nextCursor = &value
			break
		}
		projection, err := runtime.snapshotProjection(
			ctx,
			record,
			retentionReasons[record.SnapshotID],
		)
		if err != nil {
			return nil, err
		}
		items = append(items, projection)
	}
	return map[string]any{
		"snapshots":  items,
		"nextCursor": nextCursor,
	}, nil
}

type inspectSnapshotParams struct {
	SnapshotID string `json:"snapshotId"`
}

func (runtime *Runtime) inspectSnapshot(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[inspectSnapshotParams](paramsRaw)
	if err != nil || !validUUID(params.SnapshotID) {
		return nil, errors.New("workspace.request_invalid")
	}
	records, err := runtime.catalog.List(ctx, runtime.manifest.WorkspaceID)
	if err != nil {
		return nil, err
	}
	tombstoned, err := runtime.retention.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if _, deleted := tombstoned[record.SnapshotID]; deleted {
			continue
		}
		if record.SnapshotID == params.SnapshotID {
			valid, err := runtime.snapshotIntegrity(ctx, record)
			if err != nil {
				return nil, err
			}
			state, integrity := "ready", "verified"
			if !valid {
				state, integrity = "corrupt", "corrupt"
			}
			return map[string]any{
				"snapshotId": record.SnapshotID,
				"state":      state,
				"integrity":  integrity,
			}, nil
		}
	}
	return nil, errors.New("snapshot.not_found")
}

func (runtime *Runtime) snapshotProjection(
	ctx context.Context,
	record snapshot.Record,
	retentionReasons []string,
) (map[string]any, error) {
	valid, err := runtime.snapshotIntegrity(ctx, record)
	if err != nil {
		return nil, err
	}
	trigger := string(record.Trigger)
	switch record.Trigger {
	case snapshot.TriggerSwitch:
		trigger = "protection"
	case snapshot.TriggerImport:
		trigger = "import"
	case snapshot.TriggerRestore:
		trigger = "restore"
	}
	syncState := "localOnly"
	if runtime.replicaConflict != nil {
		err = runtime.replicaConflict.withManager(
			func(manager *replica.Manager) error {
				var stateErr error
				syncState, stateErr = manager.SnapshotSyncState(
					ctx,
					record,
				)
				return stateErr
			},
		)
		if errors.Is(err, replica.ErrRemoteUnavailable) {
			syncState, err = "pending", nil
		}
		if err != nil {
			return nil, err
		}
	}
	state, integrity := "ready", "verified"
	if !valid {
		state, integrity = "corrupt", "corrupt"
	}
	return map[string]any{
		"snapshotId": record.SnapshotID,
		"createdAt":  record.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"state":      state,
		"trigger":    trigger,
		"integrity":  integrity,
		"syncState":  syncState,
		"pinned":     record.Pinned,
		"retentionReasons": append(
			[]string(nil),
			retentionReasons...,
		),
		"logicalSize":     record.LogicalSize,
		"physicalSize":    record.PhysicalSize,
		"note":            nil,
		"catalogRevision": record.CatalogRevision,
	}, nil
}

func (runtime *Runtime) snapshotIntegrity(
	ctx context.Context,
	record snapshot.Record,
) (bool, error) {
	report, err := runtime.repository.Verify(ctx, record.Objects)
	if err != nil {
		return false, err
	}
	if !report.Valid {
		return false, nil
	}
	manifestRecord, err := runtime.repository.GetManifest(
		ctx,
		record.ManifestID,
	)
	if errors.Is(err, objectrepo.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sealRecord, err := runtime.repository.GetManifest(ctx, record.SealID)
	if errors.Is(err, objectrepo.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	manifest, err := decodeStrict[snapshot.Manifest](manifestRecord.Payload)
	if err != nil {
		return false, nil
	}
	seal, err := decodeStrict[snapshot.Seal](sealRecord.Payload)
	if err != nil {
		return false, nil
	}
	return manifestRecord.Name == "snapshot" &&
		sealRecord.Name == "snapshot-seal" &&
		manifest.SnapshotID == record.SnapshotID &&
		manifest.WorkspaceID == record.WorkspaceID &&
		manifest.FenceEpoch == record.FenceEpoch &&
		manifest.ClaimID == record.ClaimID &&
		manifest.SnapshotSequence == record.SnapshotSequence &&
		seal.SnapshotID == record.SnapshotID &&
		seal.ManifestHash == digestBytes(manifestRecord.Payload) &&
		seal.FenceEpoch == record.FenceEpoch &&
		seal.ClaimID == record.ClaimID &&
		seal.SnapshotSequence == record.SnapshotSequence &&
		seal.Verified, nil
}

type readFileTreeParams struct {
	DocumentID string `json:"documentId"`
}

func (runtime *Runtime) readFileTree(
	_ context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[readFileTreeParams](paramsRaw)
	if err != nil || !validUUID(params.DocumentID) {
		return nil, errors.New("workspace.request_invalid")
	}
	document, err := runtime.history.Inspect(params.DocumentID)
	if err != nil {
		return nil, err
	}
	revisions := make([]map[string]any, 0, len(document.Revisions))
	for _, revision := range document.Revisions {
		revisions = append(revisions, map[string]any{
			"contractVersion":        contractsv2.ContractVersion,
			"revisionId":             revision.RevisionID,
			"documentId":             revision.DocumentID,
			"parentRevisionId":       revision.ParentRevisionID,
			"revisionOrdinal":        revision.RevisionOrdinal,
			"formalVersion":          revision.FormalVersion,
			"kind":                   revision.Kind,
			"objectId":               revision.ObjectID,
			"contentHash":            revision.ContentHash,
			"size":                   revision.Size,
			"mimeType":               revision.MimeType,
			"createdAt":              revision.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			"createdBy":              revision.CreatedBy,
			"deviceId":               revision.DeviceID,
			"comment":                revision.Comment,
			"restoredFromRevisionId": revision.RestoredFromRevisionID,
		})
	}
	var effective *string
	if document.EffectiveRevisionID != "" {
		value := document.EffectiveRevisionID
		effective = &value
	}
	return map[string]any{
		"documentId":          document.DocumentID,
		"effectiveRevisionId": effective,
		"revisions":           revisions,
	}, nil
}

func (runtime *Runtime) getRetention(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("workspace.request_invalid")
	}
	policy, _, err := runtime.state.retention(ctx)
	return policy, err
}

func (runtime *Runtime) getRetentionStatus(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("workspace.request_invalid")
	}
	status, err := runtime.RetentionProtectionStatus(ctx)
	if err != nil {
		return nil, err
	}
	result := contractsv2.RetentionStatus{
		RepositoryUsageBytes:     status.Quota.UsageBytes,
		RepositoryLimitBytes:     status.Quota.LimitBytes,
		AutomaticSnapshotsPaused: status.Quota.AutomaticSnapshotsPaused,
		IntegrityStatus:          status.Integrity.Status,
	}
	if status.Quota.Warning != "" {
		code := status.Quota.Warning
		if separator := strings.IndexByte(code, ':'); separator >= 0 {
			code = code[:separator]
		}
		result.WarningCode = &code
	}
	if status.Integrity.Failure != "" {
		failure := status.Integrity.Failure
		result.IntegrityFailure = &failure
	}
	if status.Integrity.LastIncrementalAt != nil {
		value := status.Integrity.LastIncrementalAt.UTC().Format(
			time.RFC3339Nano,
		)
		result.LastIncrementalCheckAt = &value
	}
	if status.Integrity.LastFullAt != nil {
		value := status.Integrity.LastFullAt.UTC().Format(time.RFC3339Nano)
		result.LastFullCheckAt = &value
	}
	if err := result.Validate(); err != nil {
		return nil, errors.Join(
			errors.New("retention.status_invalid"),
			err,
		)
	}
	return result, nil
}

type updateRetentionParams struct {
	ExpectedRevision     uint64   `json:"expectedRevision"`
	SnapshotDays         uint64   `json:"snapshotDays"`
	SnapshotCount        uint64   `json:"snapshotCount"`
	SnapshotBuckets      []string `json:"snapshotBuckets"`
	FileRevisionDays     uint64   `json:"fileRevisionDays"`
	FileRevisionCount    uint64   `json:"fileRevisionCount"`
	FileRevisionBuckets  []string `json:"fileRevisionBuckets"`
	RepositoryLimitBytes *uint64  `json:"repositoryLimitBytes"`
}

func (runtime *Runtime) updateRetention(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[updateRetentionParams](paramsRaw)
	if err != nil ||
		params.ExpectedRevision == 0 ||
		params.SnapshotDays == 0 ||
		params.SnapshotCount == 0 ||
		params.FileRevisionDays == 0 ||
		params.FileRevisionCount == 0 ||
		!validRetentionBuckets(params.SnapshotBuckets) ||
		!validRetentionBuckets(params.FileRevisionBuckets) ||
		(params.RepositoryLimitBytes != nil &&
			*params.RepositoryLimitBytes == 0) {
		return nil, errors.New("workspace.request_invalid")
	}
	current, _, err := runtime.state.retention(ctx)
	if err != nil {
		return nil, err
	}
	if current.PolicyRevision != params.ExpectedRevision {
		return nil, errors.New("retention.policy_revision_stale")
	}
	next := current
	next.PolicyRevision++
	next.SnapshotDays = params.SnapshotDays
	next.SnapshotCount = params.SnapshotCount
	next.SnapshotBuckets = append([]string(nil), params.SnapshotBuckets...)
	next.FileRevisionDays = params.FileRevisionDays
	next.FileRevisionCount = params.FileRevisionCount
	next.FileRevisionBuckets = append(
		[]string(nil),
		params.FileRevisionBuckets...,
	)
	next.RepositoryLimitBytes = params.RepositoryLimitBytes
	token, _ := runtime.coordinator.Current()
	var (
		operation *protocolv2.OperationContext
		receipt   *protocolv2.OperationReceipt
	)
	if currentOperation, dispatched :=
		protocolv2.OperationFromContext(ctx); dispatched {
		built, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, next)
		if receiptErr != nil {
			return nil, receiptErr
		}
		operation = &currentOperation
		receipt = &built
	}
	_, err = runtime.coordinator.Write(
		ctx,
		token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			if receipt != nil {
				return runtime.state.updateRetentionWithOperationReceipt(
					ctx,
					params.ExpectedRevision,
					next,
					intent.MutationRevision,
					operation.Session,
					*receipt,
				)
			}
			return runtime.state.updateRetention(
				ctx,
				params.ExpectedRevision,
				next,
				intent.MutationRevision,
			)
		},
	)
	if err != nil {
		return nil, err
	}
	return next, nil
}

func decodeStrict[T any](raw []byte) (T, error) {
	var result T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, errors.New("trailing JSON")
	}
	return result, nil
}
