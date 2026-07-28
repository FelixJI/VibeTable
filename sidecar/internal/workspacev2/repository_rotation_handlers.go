package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

const repositoryKeyRotationPlanTTL = 10 * time.Minute

func (runtime *Runtime) registerRepositoryRotationHandlers() {
	runtime.dispatcher.Register(
		"repository.previewKeyRotation",
		protocolv2.WorkspaceScope,
		runtime.previewRepositoryKeyRotation,
	)
	runtime.dispatcher.Register(
		"repository.applyKeyRotation",
		protocolv2.WorkspaceScope,
		runtime.applyRepositoryKeyRotation,
	)
}

func (runtime *Runtime) previewRepositoryKeyRotation(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("repository.key_rotation_request_invalid")
	}
	if objectrepo.EncryptionMode(runtime.manifest.EncryptionMode) !=
		objectrepo.EncryptionProtected {
		return nil, errors.New("repository.protected_mode_required")
	}
	if runtime.requestShutdown == nil {
		return nil, errors.New("repository.key_rotation_host_unavailable")
	}
	token, counters := runtime.coordinator.Current()
	last, found, err := runtime.catalog.Last(
		ctx,
		runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return nil, err
	}
	var catalogRevision uint64
	if found {
		catalogRevision = last.CatalogRevision
	}
	expiresAt := time.Now().UTC().Add(repositoryKeyRotationPlanTTL)
	plan := repositoryKeyRotationPlan{
		PlanID:           uuid.NewString(),
		SessionEpoch:     token.SessionEpoch,
		FenceEpoch:       token.FenceEpoch,
		ClaimID:          token.ClaimID,
		MutationRevision: counters.MutationRevision,
		CatalogRevision:  catalogRevision,
		ExpiresAt:        expiresAt.Format(time.RFC3339Nano),
	}
	result := map[string]any{
		"planId":             plan.PlanID,
		"expiresAt":          expiresAt.Format(time.RFC3339Nano),
		"protectionRequired": true,
	}
	var putErr error
	if operation, dispatched := protocolv2.OperationFromContext(ctx); dispatched {
		receipt, receiptErr :=
			protocolv2.BuildContextOperationReceipt(ctx, result)
		if receiptErr != nil {
			return nil, receiptErr
		}
		putErr = runtime.state.
			putRepositoryKeyRotationPlanWithOperationReceipt(
				ctx,
				plan,
				operation.Session,
				receipt,
			)
	} else {
		putErr = runtime.state.putRepositoryKeyRotationPlan(ctx, plan)
	}
	if putErr != nil {
		return nil, putErr
	}
	return result, nil
}

type applyRepositoryKeyRotationParams struct {
	PlanID    string `json:"planId"`
	Confirmed bool   `json:"confirmed"`
}

func (runtime *Runtime) applyRepositoryKeyRotation(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrictWorkspaceWire(wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[applyRepositoryKeyRotationParams](paramsRaw)
	if err != nil || !validUUID(params.PlanID) || !params.Confirmed {
		return nil, errors.New("repository.key_rotation_request_invalid")
	}
	if runtime.requestShutdown == nil {
		return nil, errors.New("repository.key_rotation_host_unavailable")
	}
	plan, err := runtime.state.repositoryKeyRotationPlan(ctx, params.PlanID)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil {
		return nil, errors.New("repository.key_rotation_plan_corrupt")
	}
	if !time.Now().UTC().Before(expiresAt) {
		_ = runtime.state.deleteRepositoryKeyRotationPlan(
			context.WithoutCancel(ctx),
			plan.PlanID,
		)
		return nil, errors.New("repository.key_rotation_plan_expired")
	}
	token, counters := runtime.coordinator.Current()
	if !tokenMatchesRotationPlan(token, plan) ||
		counters.MutationRevision != plan.MutationRevision {
		return nil, errors.New("repository.key_rotation_plan_stale")
	}
	last, found, err := runtime.catalog.Last(
		ctx,
		runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return nil, err
	}
	var catalogRevision uint64
	if found {
		catalogRevision = last.CatalogRevision
	}
	if catalogRevision != plan.CatalogRevision {
		return nil, errors.New("repository.key_rotation_plan_stale")
	}
	protection, err := runtime.protectionSnapshotForOperation(
		ctx,
		token,
		wire.OperationID,
		"repository.applyKeyRotation.protection",
	)
	if err != nil {
		return nil, err
	}
	if protection.SnapshotID == "" || !protection.Pinned {
		return nil, errors.New(
			"repository.key_rotation_protection_snapshot_failed",
		)
	}
	if valid, verifyErr := runtime.snapshotIntegrity(
		ctx,
		protection,
	); verifyErr != nil || !valid {
		return nil, errors.Join(
			errors.New(
				"repository.key_rotation_protection_snapshot_failed",
			),
			verifyErr,
		)
	}
	paths, err := resolvePaths(runtime.app.DataDir())
	if err != nil {
		return nil, err
	}
	intent := keyRotationHostIntent{
		FormatVersion:        1,
		OperationID:          wire.OperationID,
		PlanID:               plan.PlanID,
		WorkspaceID:          token.WorkspaceID,
		SessionEpoch:         token.SessionEpoch,
		FenceEpoch:           token.FenceEpoch,
		ClaimID:              token.ClaimID,
		ProtectionSnapshotID: protection.SnapshotID,
		State:                keyRotationIntentRequested,
		Sequence:             wire.Sequence,
	}
	result := map[string]any{
		"operationId":             wire.OperationID,
		"state":                   "hostRestartRequired",
		"newRecoveryKeyAvailable": false,
	}
	receipt, err := protocolv2.BuildOperationReceiptForSession(
		"repository.applyKeyRotation",
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
	intent.Method = receipt.Method
	intent.Scope = string(receipt.Scope)
	intent.RequestHash = receipt.RequestHash
	intent.Result = append(json.RawMessage(nil), receipt.Result...)
	if err := writeKeyRotationIntent(paths, intent); err != nil {
		return nil, err
	}
	_ = runtime.state.deleteRepositoryKeyRotationPlan(
		context.WithoutCancel(ctx),
		plan.PlanID,
	)
	runtime.dispatcher.SuspendWorkspace()
	runtime.requestShutdown()
	return result, nil
}
