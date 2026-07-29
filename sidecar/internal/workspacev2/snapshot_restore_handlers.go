package workspacev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const snapshotRestorePlanTTL = 10 * time.Minute

func (runtime *Runtime) registerSnapshotRestoreHandlers() {
	runtime.dispatcher.Register(
		"snapshot.previewRestore",
		protocolv2.WorkspaceScope,
		runtime.previewSnapshotRestore,
	)
	runtime.dispatcher.Register(
		"snapshot.applyRestore",
		protocolv2.WorkspaceScope,
		runtime.applySnapshotRestore,
	)
}

type previewSnapshotRestoreParams struct {
	SnapshotID string `json:"snapshotId"`
	TargetMode string `json:"targetMode"`
}

func (runtime *Runtime) previewSnapshotRestore(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[previewSnapshotRestoreParams](paramsRaw)
	if err != nil || !validUUID(params.SnapshotID) {
		return nil, errors.New("restore.request_invalid")
	}
	if params.TargetMode == "newWorkspace" {
		return nil, errors.New("restore.new_workspace_broker_required")
	}
	if params.TargetMode != "currentWorkspace" {
		return nil, errors.New("restore.request_invalid")
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
	preview, err := runtime.buildSnapshotRestorePreview(ctx, record)
	if err != nil {
		return nil, err
	}
	_, counters := runtime.coordinator.Current()
	expiresAt := time.Now().UTC().Add(snapshotRestorePlanTTL)
	plan := snapshotRestorePlan{
		PlanID:           uuid.NewString(),
		SnapshotID:       record.SnapshotID,
		CatalogRevision:  record.CatalogRevision,
		MutationRevision: counters.MutationRevision,
		DiffHash:         preview.Hash,
		TargetMode:       params.TargetMode,
		ExpiresAt:        expiresAt.Format(time.RFC3339Nano),
	}
	result := map[string]any{
		"planId":             plan.PlanID,
		"protectionRequired": true,
		"changes":            preview.Changes,
	}
	var putErr error
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, result)
		if receiptErr != nil {
			return nil, receiptErr
		}
		putErr = runtime.state.
			putSnapshotRestorePlanWithOperationReceipt(
				ctx,
				plan,
				operation.Session,
				receipt,
			)
	} else {
		putErr = runtime.state.putSnapshotRestorePlan(ctx, plan)
	}
	if putErr != nil {
		return nil, putErr
	}
	return result, nil
}

type applySnapshotRestoreParams struct {
	PlanID    string `json:"planId"`
	Confirmed bool   `json:"confirmed"`
}

func (runtime *Runtime) applySnapshotRestore(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrictWorkspaceWire(wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[applySnapshotRestoreParams](paramsRaw)
	if err != nil || !validUUID(params.PlanID) || !params.Confirmed {
		return nil, errors.New("restore.request_invalid")
	}
	if runtime.requestShutdown == nil {
		return nil, errors.New("restore.shutdown_unavailable")
	}
	plan, err := runtime.state.snapshotRestorePlan(ctx, params.PlanID)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil {
		return nil, errors.New("restore.plan_corrupt")
	}
	if !time.Now().UTC().Before(expiresAt) {
		_ = runtime.state.deleteSnapshotRestorePlan(
			context.WithoutCancel(ctx),
			plan.PlanID,
		)
		return nil, errors.New("restore.plan_expired")
	}
	token, counters := runtime.coordinator.Current()
	if counters.MutationRevision != plan.MutationRevision {
		return nil, errors.New("restore.plan_stale")
	}
	record, err := runtime.snapshotRecord(ctx, plan.SnapshotID)
	if err != nil {
		return nil, err
	}
	if record.CatalogRevision != plan.CatalogRevision {
		return nil, errors.New("restore.plan_stale")
	}
	if valid, err := runtime.snapshotIntegrity(ctx, record); err != nil {
		return nil, err
	} else if !valid {
		return nil, errors.New("snapshot.integrity_failed")
	}
	preview, err := runtime.buildSnapshotRestorePreview(ctx, record)
	if err != nil {
		return nil, err
	}
	if preview.Hash != plan.DiffHash {
		return nil, errors.New("restore.plan_stale")
	}
	historyRoot := preview.HistoryRoot
	protection, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		wire.OperationID,
		"snapshot.applyRestore.protection",
	)
	if err != nil {
		return nil, err
	}
	if protection.SnapshotID == "" {
		return nil, errors.New("restore.protection_snapshot_failed")
	}
	result := map[string]any{
		"operationId": wire.OperationID,
		"state":       "prepared",
	}
	receipt, err := protocolv2.BuildOperationReceiptForSession(
		"snapshot.applyRestore",
		protocolv2.WorkspaceScope,
		token.WorkspaceID,
		wire.OperationID,
		wire.SessionEpoch,
		paramsRaw,
		result,
	)
	if err != nil {
		return nil, err
	}
	_, err = runtime.coordinator.Write(
		ctx,
		token,
		func(
			ctx context.Context,
			intent writecoordinator.WriteIntent,
		) error {
			staged, err := runtime.history.StageSnapshotRestore(
				ctx,
				intent,
				historyRoot,
			)
			if err != nil {
				return err
			}
			return runtime.stagePendingSnapshotRestore(
				ctx,
				wire.Sequence,
				receipt,
				record,
				protection,
				staged,
			)
		},
	)
	if err != nil {
		return nil, err
	}
	_ = runtime.state.deleteSnapshotRestorePlan(
		context.WithoutCancel(ctx),
		plan.PlanID,
	)
	runtime.dispatcher.SuspendWorkspace()
	runtime.requestShutdown()
	return result, nil
}

type snapshotRestorePreview struct {
	Changes     []string
	Hash        string
	HistoryRoot objectrepo.ManifestID
}

func (runtime *Runtime) buildSnapshotRestorePreview(
	ctx context.Context,
	record snapshot.Record,
) (snapshotRestorePreview, error) {
	if runtime == nil || runtime.frozenSource == nil ||
		runtime.history == nil {
		return snapshotRestorePreview{},
			errors.New("restore.preview_unavailable")
	}
	bundle, err := snapshot.LoadSnapshotBundle(
		ctx,
		runtime.repository,
		record,
	)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	var historyRoot objectrepo.ManifestID
	if bundle.HistoryRoot != nil {
		historyRoot = bundle.HistoryRoot.ID
	}
	fileDiff, err := runtime.history.PreviewSnapshotRestore(
		ctx,
		historyRoot,
	)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	database, err := runtime.frozenSource.snapshotDatabase(ctx)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	settings, err := runtime.frozenSource.workspaceSettings(ctx)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	currentDatabaseID := contentIDReplicaOneShot(database)
	currentSettingsID := contentIDReplicaOneShot(settings)
	targetDatabaseID := record.ObjectMap["database"]
	targetSettingsID := record.ObjectMap["workspace-settings"]
	if targetDatabaseID == "" || targetSettingsID == "" {
		return snapshotRestorePreview{},
			errors.New("restore.snapshot_incomplete")
	}
	changes := make([]string, 0, 4)
	if currentDatabaseID != targetDatabaseID {
		changes = append(changes, "database:replace")
	}
	if len(fileDiff.EffectivePointerPaths) != 0 {
		changes = append(
			changes,
			fmt.Sprintf(
				"files:effective-pointers:%d",
				len(fileDiff.EffectivePointerPaths),
			),
		)
	}
	if len(fileDiff.AddedAfterSnapshot) != 0 {
		changes = append(
			changes,
			fmt.Sprintf(
				"files:added-after-snapshot:%d",
				len(fileDiff.AddedAfterSnapshot),
			),
		)
	}
	targetSettings, err := runtime.readWorkspaceSettingsObject(
		ctx,
		targetSettingsID,
	)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	settingsChanged, err := workspaceSettingsDiffer(settings, targetSettings)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	if settingsChanged {
		changes = append(changes, "workspace-settings:retention")
	}
	sort.Strings(changes)
	binding := struct {
		SnapshotID            string                `json:"snapshotId"`
		CatalogRevision       uint64                `json:"catalogRevision"`
		TargetDatabaseID      objectrepo.ObjectID   `json:"targetDatabaseId"`
		CurrentDatabaseID     objectrepo.ObjectID   `json:"currentDatabaseId"`
		TargetHistoryRoot     objectrepo.ManifestID `json:"targetHistoryRoot"`
		CurrentHistoryRoot    objectrepo.ManifestID `json:"currentHistoryRoot"`
		EffectivePointerPaths []string              `json:"effectivePointerPaths"`
		AddedAfterSnapshot    []string              `json:"addedAfterSnapshot"`
		TargetSettingsID      objectrepo.ObjectID   `json:"targetSettingsId"`
		CurrentSettingsID     objectrepo.ObjectID   `json:"currentSettingsId"`
		Changes               []string              `json:"changes"`
	}{
		SnapshotID:            record.SnapshotID,
		CatalogRevision:       record.CatalogRevision,
		TargetDatabaseID:      targetDatabaseID,
		CurrentDatabaseID:     currentDatabaseID,
		TargetHistoryRoot:     historyRoot,
		CurrentHistoryRoot:    runtime.history.Root(),
		EffectivePointerPaths: fileDiff.EffectivePointerPaths,
		AddedAfterSnapshot:    fileDiff.AddedAfterSnapshot,
		TargetSettingsID:      targetSettingsID,
		CurrentSettingsID:     currentSettingsID,
		Changes:               changes,
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return snapshotRestorePreview{}, err
	}
	sum := sha256.Sum256(raw)
	return snapshotRestorePreview{
		Changes:     changes,
		Hash:        "sha256:" + hex.EncodeToString(sum[:]),
		HistoryRoot: historyRoot,
	}, nil
}
