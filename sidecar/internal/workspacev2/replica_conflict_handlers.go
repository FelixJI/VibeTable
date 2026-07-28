package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
)

type productionReplicaConflict struct {
	runtime   *Runtime
	manager   *replica.Manager
	conflicts *conflictresolution.Engine
	applier   *filehistory.ConflictApplier
}

func openProductionReplicaConflict(
	ctx context.Context,
	runtime *Runtime,
	paths workspacePaths,
	options Options,
) (_ *productionReplicaConflict, err error) {
	if options.ReplicaRemote == nil {
		return nil, nil
	}
	if runtime == nil ||
		runtime.history == nil ||
		runtime.headStore == nil ||
		runtime.repository == nil ||
		runtime.catalog == nil ||
		runtime.coordinator == nil {
		return nil, errors.New(
			"replica.production_dependencies_required",
		)
	}
	applier, err := filehistory.NewConflictApplier(
		runtime.history, runtime.headStore,
	)
	if err != nil {
		return nil, err
	}
	conflicts, err := conflictresolution.OpenEngine(
		joinCoordination(paths, "conflicts.db"),
	)
	if err != nil {
		return nil, err
	}
	result := &productionReplicaConflict{
		runtime:   runtime,
		conflicts: conflicts,
		applier:   applier,
	}
	defer func() {
		if err != nil {
			_ = result.close()
		}
	}()
	deviceID := options.ReplicaDeviceID
	if deviceID == "" {
		deviceID = options.ClaimID
	}
	result.manager, err = replica.OpenManager(
		ctx,
		replica.ManagerOptions{
			WorkspaceID: options.WorkspaceID,
			DeviceID:    deviceID,
			QueuePath: joinCoordination(
				paths, "replica-queue.db",
			),
			StatePath: joinCoordination(
				paths, "replica-state.db",
			),
			PublicationPath: joinCoordination(
				paths, "replica-publications.db",
			),
			PublicationKey: append([]byte(nil), options.ReplicaPublicationKey...),
			Remote:         options.ReplicaRemote,
			Catalog:        runtime.catalog,
			Repository:     runtime.repository,
			Authority:      runtimeReplicaAuthority{runtime: runtime},
			Conflicts:      conflicts,
		},
	)
	if err != nil {
		if errors.Is(err, replica.ErrReplicationUnavailable) {
			// Replica protection is optional. An unavailable or unverifiable
			// remote must remove the capability, not prevent the local
			// workspace from reopening and accepting durable local writes.
			_ = result.close()
			return nil, nil
		}
		return nil, err
	}
	if err := result.conflicts.Recover(
		ctx,
		&workspaceConflictAppender{owner: result},
	); err != nil {
		return nil, err
	}
	return result, nil
}

func joinCoordination(paths workspacePaths, name string) string {
	return filepath.Join(paths.coordination, name)
}

func (owner *productionReplicaConflict) register() {
	if owner == nil || owner.manager == nil ||
		owner.conflicts == nil || owner.applier == nil {
		return
	}
	methods := []struct {
		name    string
		handler protocolv2.Handler
	}{
		{"replica.status", owner.status},
		{"replica.synchronize", owner.synchronize},
		{"replica.forceTakeover", owner.forceTakeover},
	}
	if owner.manager.ConflictReady() {
		methods = append(methods,
			struct {
				name    string
				handler protocolv2.Handler
			}{"conflict.list", owner.listConflicts},
			struct {
				name    string
				handler protocolv2.Handler
			}{"conflict.inspect", owner.inspectConflict},
			struct {
				name    string
				handler protocolv2.Handler
			}{"conflict.preview", owner.previewConflict},
			struct {
				name    string
				handler protocolv2.Handler
			}{"conflict.apply", owner.applyConflict},
		)
	}
	for _, method := range methods {
		owner.runtime.dispatcher.Register(
			method.name,
			protocolv2.WorkspaceScope,
			method.handler,
		)
	}
}

func (owner *productionReplicaConflict) status(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("replica.request_invalid")
	}
	status, err := owner.manager.Status(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"coordinationStrength": string(status.CoordinationStrength),
		"syncState":            status.SyncState,
		"pendingSync":          status.PendingSync,
	}, nil
}

func (owner *productionReplicaConflict) synchronize(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("replica.request_invalid")
	}
	wire, err := decodeStrict[contractsv2.WorkspaceWireScope](wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	result := map[string]any{
		"operationId": wire.OperationID,
		"state":       "queued",
	}
	receipt, err := protocolv2.BuildContextOperationReceipt(
		ctx, result,
	)
	if err != nil {
		return nil, err
	}
	if err := owner.manager.QueueSynchronize(ctx, receipt); err != nil {
		return nil, err
	}
	return result, nil
}

type forceTakeoverParams struct {
	Mode string `json:"mode"`
}

func (owner *productionReplicaConflict) forceTakeover(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[forceTakeoverParams](paramsRaw)
	if err != nil ||
		(params.Mode != "writable" &&
			params.Mode != "provisional") {
		return nil, errors.New("replica.request_invalid")
	}
	claim, err := owner.manager.ForceTakeoverWithReceipt(
		ctx,
		replica.ClaimMode(params.Mode),
		func(
			claim replica.Claim,
		) (protocolv2.OperationReceipt, error) {
			return protocolv2.BuildContextOperationReceipt(
				ctx,
				map[string]any{
					"fenceEpoch": claim.FenceEpoch,
					"claimId":    claim.ClaimID,
					"mode":       string(claim.Mode),
				},
			)
		},
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"fenceEpoch": claim.FenceEpoch,
		"claimId":    claim.ClaimID,
		"mode":       string(claim.Mode),
	}, nil
}

type listConflictParams struct {
	Cursor *string `json:"cursor"`
	Limit  int     `json:"limit"`
}

func (owner *productionReplicaConflict) listConflicts(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[listConflictParams](paramsRaw)
	if err != nil || params.Limit < 1 || params.Limit > 200 {
		return nil, errors.New("conflict.request_invalid")
	}
	sets, next, err := owner.conflicts.List(
		ctx,
		owner.runtime.manifest.WorkspaceID,
		params.Cursor,
		params.Limit,
	)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(sets))
	for _, set := range sets {
		items = append(items, map[string]any{
			"conflictId": set.ConflictID,
			"state":      string(set.State),
			"createdAt": set.CreatedAt.UTC().Format(
				"2006-01-02T15:04:05.999999999Z07:00",
			),
			"itemCount": len(
				conflictresolution.BuildPlan(
					set.Base, set.Local, set.Replica,
				).Files,
			),
		})
	}
	return map[string]any{
		"conflicts":  items,
		"nextCursor": next,
	}, nil
}

type inspectConflictParams struct {
	ConflictID string `json:"conflictId"`
}

func (owner *productionReplicaConflict) inspectConflict(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[inspectConflictParams](paramsRaw)
	if err != nil || !validUUID(params.ConflictID) {
		return nil, errors.New("conflict.request_invalid")
	}
	set, err := owner.conflicts.Inspect(ctx, params.ConflictID)
	if err != nil {
		return nil, err
	}
	if set.WorkspaceID != owner.runtime.manifest.WorkspaceID {
		return nil, conflictresolution.ErrConflictNotFound
	}
	plan := conflictresolution.BuildPlan(
		set.Base, set.Local, set.Replica,
	)
	items := make([]map[string]any, 0, len(plan.Files))
	for _, item := range plan.Files {
		items = append(items, map[string]any{
			"documentId": item.DocumentID,
			"base":       item.Base,
			"local":      item.Local,
			"replica":    item.Replica,
		})
	}
	return map[string]any{
		"conflictId": set.ConflictID,
		"state":      string(set.State),
		"items":      items,
	}, nil
}

type previewConflictParams struct {
	ConflictID string                      `json:"conflictId"`
	Choices    []conflictresolution.Choice `json:"choices"`
}

func (owner *productionReplicaConflict) previewConflict(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[previewConflictParams](paramsRaw)
	if err != nil || !validUUID(params.ConflictID) {
		return nil, errors.New("conflict.request_invalid")
	}
	preview, err := owner.conflicts.PreviewWithReceipt(
		ctx,
		params.ConflictID,
		params.Choices,
		func(
			preview conflictresolution.Preview,
		) (protocolv2.OperationReceipt, error) {
			return protocolv2.BuildContextOperationReceipt(
				ctx,
				map[string]any{
					"planId":      preview.PlanID,
					"diagnostics": preview.Diagnostics,
					"valid":       preview.Valid,
				},
			)
		},
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"planId":      preview.PlanID,
		"diagnostics": preview.Diagnostics,
		"valid":       preview.Valid,
	}, nil
}

type applyConflictParams struct {
	PlanID string `json:"planId"`
}

func (owner *productionReplicaConflict) applyConflict(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[applyConflictParams](paramsRaw)
	if err != nil || !validUUID(params.PlanID) {
		return nil, errors.New("conflict.request_invalid")
	}
	operation, ok := protocolv2.OperationFromContext(ctx)
	if !ok {
		return nil, errors.New("workspace.operation_context_required")
	}
	appender := &workspaceConflictAppender{
		owner:   owner,
		context: ctx,
	}
	receipt, err := owner.conflicts.Apply(
		ctx,
		params.PlanID,
		operation.OperationID,
		appender,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"operationId":         receipt.OperationID,
		"state":               receipt.State,
		"recoverySnapshotIds": receipt.RecoverySnapshotIDs,
	}, nil
}

func (owner *productionReplicaConflict) loadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	loaders := []func(
		context.Context,
		string,
		string,
	) (protocolv2.OperationReceipt, bool, error){
		owner.manager.LoadOperationReceipt,
		owner.conflicts.LoadOperationReceipt,
		owner.applier.LoadOperationReceipt,
	}
	var result protocolv2.OperationReceipt
	foundAny := false
	for _, load := range loaders {
		receipt, found, err := load(
			ctx, workspaceID, operationID,
		)
		if err != nil {
			return protocolv2.OperationReceipt{}, false, err
		}
		if !found {
			continue
		}
		if foundAny && !sameOperationReceipt(result, receipt) {
			return protocolv2.OperationReceipt{}, false,
				protocolv2.ErrOperationConflict
		}
		result = receipt
		foundAny = true
	}
	return result, foundAny, nil
}

func (owner *productionReplicaConflict) close() error {
	if owner == nil {
		return nil
	}
	var errs []error
	if owner.manager != nil {
		errs = append(errs, owner.manager.Close())
		owner.manager = nil
	}
	if owner.conflicts != nil {
		errs = append(errs, owner.conflicts.Close())
		owner.conflicts = nil
	}
	return errors.Join(errs...)
}

type runtimeReplicaAuthority struct {
	runtime *Runtime
}

func (authority runtimeReplicaAuthority) CurrentAuthority() replica.AuthorityState {
	token, _ := authority.runtime.coordinator.Current()
	return replica.AuthorityState{
		WorkspaceID: token.WorkspaceID,
		FenceEpoch:  token.FenceEpoch,
		ClaimID:     token.ClaimID,
	}
}

func (authority runtimeReplicaAuthority) ApplyReplicaClaim(
	ctx context.Context,
	claim replica.Claim,
) error {
	token, _ := authority.runtime.coordinator.Current()
	if claim.WorkspaceID != token.WorkspaceID ||
		claim.FenceEpoch != token.FenceEpoch+1 ||
		!validUUID(claim.ClaimID) {
		return replica.ErrStaleClaim
	}
	oldAuthority := token.Authority()
	nextAuthority := objectrepo.Authority{
		WorkspaceID: claim.WorkspaceID,
		FenceEpoch:  claim.FenceEpoch,
		ClaimID:     claim.ClaimID,
	}
	err := authority.runtime.repository.AcceptAuthority(
		ctx, &oldAuthority, nextAuthority,
	)
	if errors.Is(err, objectrepo.ErrStaleAuthority) {
		// Recovery after repository authority advanced but before the
		// coordinator token was durably transferred.
		err = authority.runtime.repository.AcceptAuthority(
			ctx, &nextAuthority, nextAuthority,
		)
	}
	if err != nil {
		return err
	}
	next, err := authority.runtime.coordinator.TransferFence(
		context.WithoutCancel(ctx),
		token,
		claim.ClaimID,
	)
	if err != nil {
		return err
	}
	if next.FenceEpoch != claim.FenceEpoch ||
		next.ClaimID != claim.ClaimID {
		return replica.ErrStaleClaim
	}
	return nil
}

type workspaceConflictAppender struct {
	owner   *productionReplicaConflict
	context context.Context
}

func (appender *workspaceConflictAppender) Stage(
	ctx context.Context,
	operationID string,
	planID string,
	plan conflictresolution.Plan,
	changes []conflictresolution.ResolvedChange,
	_ conflictresolution.Candidate,
) (conflictresolution.ApplyStage, error) {
	if appender.owner == nil || appender.owner.applier == nil {
		return conflictresolution.ApplyStage{},
			conflictresolution.ErrApplyUnproven
	}
	selections := make(
		[]filehistory.ConflictSelection, 0, len(changes),
	)
	for _, change := range changes {
		selections = append(selections, filehistory.ConflictSelection{
			DocumentID:       change.DocumentID,
			ExpectedPath:     change.Previous.Path,
			ExpectedObjectID: objectrepo.ObjectID(change.Previous.ContentID),
			ExpectedDeleted:  change.Previous.Deleted,
			ChosenPath:       change.Chosen.Path,
			ChosenObjectID:   objectrepo.ObjectID(change.Chosen.ContentID),
			ChosenDeleted:    change.Chosen.Deleted,
		})
	}
	recoveryIDs := uniqueStrings([]string{
		plan.LocalSnapshot,
		plan.ReplicaSnapshot,
	})
	result := map[string]any{
		"operationId":         operationID,
		"state":               "applied",
		"recoverySnapshotIds": recoveryIDs,
	}
	receiptContext := appender.context
	if receiptContext == nil {
		receiptContext = ctx
	}
	receipt, err := protocolv2.BuildContextOperationReceipt(
		receiptContext, result,
	)
	if err != nil {
		return conflictresolution.ApplyStage{}, err
	}
	stage, err := appender.owner.applier.Prepare(
		ctx,
		filehistory.ConflictStage{
			PlanID:              planID,
			OperationID:         operationID,
			Selections:          selections,
			RecoverySnapshotIDs: recoveryIDs,
			OperationReceipt:    receipt,
		},
	)
	if err != nil {
		return conflictresolution.ApplyStage{}, err
	}
	return conflictresolution.ApplyStage{
		StageID:     stage.StageID,
		OperationID: stage.OperationID,
		PlanID:      stage.PlanID,
	}, nil
}

func (appender *workspaceConflictAppender) Commit(
	ctx context.Context,
	stage conflictresolution.ApplyStage,
) (conflictresolution.ApplyReceipt, error) {
	commit, err := appender.owner.applier.Commit(
		ctx, stage.StageID,
	)
	if err != nil {
		return conflictresolution.ApplyReceipt{}, err
	}
	return conflictresolution.ApplyReceipt{
		OperationID:         commit.OperationID,
		State:               "applied",
		RecoverySnapshotIDs: commit.RecoverySnapshotIDs,
		AuthorityRevision:   commit.AuthorityRevision,
	}, nil
}

func (appender *workspaceConflictAppender) Probe(
	ctx context.Context,
	operationID string,
) (conflictresolution.ApplyReceipt, bool, error) {
	receipt, found, err := appender.owner.applier.LoadOperationReceipt(
		ctx,
		appender.owner.runtime.manifest.WorkspaceID,
		operationID,
	)
	if err != nil || !found {
		return conflictresolution.ApplyReceipt{}, found, err
	}
	var result struct {
		OperationID         string   `json:"operationId"`
		State               string   `json:"state"`
		RecoverySnapshotIDs []string `json:"recoverySnapshotIds"`
	}
	if err := json.Unmarshal(receipt.Result, &result); err != nil {
		return conflictresolution.ApplyReceipt{}, false, err
	}
	return conflictresolution.ApplyReceipt{
		OperationID:         result.OperationID,
		State:               result.State,
		RecoverySnapshotIDs: result.RecoverySnapshotIDs,
		AuthorityRevision:   1,
	}, true, nil
}

func sameOperationReceipt(
	left protocolv2.OperationReceipt,
	right protocolv2.OperationReceipt,
) bool {
	return left.OperationID == right.OperationID &&
		left.WorkspaceID == right.WorkspaceID &&
		left.Method == right.Method &&
		left.Scope == right.Scope &&
		left.RequestHash == right.RequestHash &&
		string(left.Result) == string(right.Result)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
