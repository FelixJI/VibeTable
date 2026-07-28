package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
)

type productionReplicaConflict struct {
	runtime               *Runtime
	managerMu             sync.RWMutex
	manager               *replica.Manager
	managerOptions        replica.ManagerOptions
	remoteFactory         func(context.Context) (replica.VerifiedRemote, error)
	conflicts             *conflictresolution.Engine
	applier               *filehistory.ConflictApplier
	pendingPath           string
	selectedRoot          string
	activityFilesRoot     string
	selectedFilesBasePath string
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
	wake                  chan struct{}
}

func openProductionReplicaConflict(
	ctx context.Context,
	runtime *Runtime,
	paths workspacePaths,
	options Options,
) (_ *productionReplicaConflict, err error) {
	if options.ReplicaRemote == nil &&
		options.ReplicaRemoteFactory == nil {
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
		pendingPath: joinCoordination(
			paths,
			"replica-pending.json",
		),
		remoteFactory:     options.ReplicaRemoteFactory,
		selectedRoot:      options.ReplicaRoot,
		activityFilesRoot: paths.files,
		selectedFilesBasePath: joinCoordination(
			paths,
			"replica-selected-files-base.json",
		),
		wake: make(chan struct{}, 1),
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
	result.managerOptions = replica.ManagerOptions{
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
		PublicationKey:    append([]byte(nil), options.ReplicaPublicationKey...),
		Remote:            options.ReplicaRemote,
		Catalog:           runtime.catalog,
		Repository:        runtime.repository,
		Authority:         runtimeReplicaAuthority{runtime: runtime},
		Conflicts:         conflicts,
		DependencyScanner: options.ReplicaDependencyScanner,
		DestructiveSafe: func(ctx context.Context) error {
			return runtime.retention.store.EnsureIntegrityHealthy(ctx)
		},
	}
	if options.ReplicaRemote != nil {
		result.managerOptions.Remote = options.ReplicaRemote
		result.manager, err = replica.OpenManager(ctx, result.managerOptions)
		if err != nil && !errors.Is(err, replica.ErrReplicationUnavailable) {
			return nil, err
		}
	}
	if result.manager == nil {
		if err := result.markPending("replica.remote_unavailable"); err != nil {
			return nil, err
		}
	} else {
		status, statusErr := result.manager.Status(ctx)
		if statusErr != nil {
			if err := result.markPending(statusErr.Error()); err != nil {
				return nil, err
			}
		} else if err := result.persistStatus(
			ctx,
			status.CoordinationStrength,
			status.SyncState,
			status.PendingSync,
		); err != nil {
			return nil, err
		}
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

func (owner *productionReplicaConflict) startWorker() {
	if owner == nil || owner.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	owner.cancel = cancel
	owner.wg.Add(1)
	go owner.runWorker(ctx)
	owner.signalWorker()
}

func (owner *productionReplicaConflict) runWorker(ctx context.Context) {
	defer owner.wg.Done()
	delay := time.Second
	for {
		err := owner.synchronizeOnce(ctx)
		if err == nil {
			delay = time.Second
		} else {
			_ = owner.markPending(err.Error())
			if delay < time.Minute {
				delay *= 2
				if delay > time.Minute {
					delay = time.Minute
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-owner.wake:
			if !timer.Stop() {
				<-timer.C
			}
			delay = time.Second
		case <-timer.C:
		}
	}
}

func (owner *productionReplicaConflict) synchronizeOnce(
	ctx context.Context,
) error {
	manager, err := owner.ensureManager(ctx)
	if err != nil {
		return err
	}
	if err := owner.persistCurrentStatus(
		context.WithoutCancel(ctx),
		"syncing",
		true,
	); err != nil {
		return err
	}
	if err := owner.scanSelectedFiles(ctx); err != nil {
		return err
	}
	if err := manager.Synchronize(ctx); err != nil {
		return err
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if err := owner.clearPending(); err != nil {
		return err
	}
	return owner.persistStatus(
		context.WithoutCancel(ctx),
		status.CoordinationStrength,
		status.SyncState,
		status.PendingSync,
	)
}

func (owner *productionReplicaConflict) ensureManager(
	ctx context.Context,
) (*replica.Manager, error) {
	owner.managerMu.RLock()
	manager := owner.manager
	owner.managerMu.RUnlock()
	if manager != nil {
		return manager, nil
	}
	if owner.remoteFactory == nil {
		return nil, replica.ErrReplicationUnavailable
	}
	remote, err := owner.remoteFactory(ctx)
	if err != nil {
		return nil, err
	}
	options := owner.managerOptions
	options.Remote = remote
	opened, err := replica.OpenManager(ctx, options)
	if err != nil {
		return nil, err
	}
	owner.managerMu.Lock()
	if owner.manager == nil {
		owner.manager = opened
		manager = opened
	} else {
		manager = owner.manager
	}
	owner.managerMu.Unlock()
	if manager != opened {
		_ = opened.Close()
	}
	return manager, nil
}

func (owner *productionReplicaConflict) withManager(
	fn func(*replica.Manager) error,
) error {
	if owner == nil {
		return replica.ErrReplicationUnavailable
	}
	owner.managerMu.RLock()
	defer owner.managerMu.RUnlock()
	if owner.manager == nil {
		return replica.ErrRemoteUnavailable
	}
	return fn(owner.manager)
}

func (owner *productionReplicaConflict) signalWorker() {
	if owner == nil || owner.wake == nil {
		return
	}
	select {
	case owner.wake <- struct{}{}:
	default:
	}
}

func (owner *productionReplicaConflict) queuePublishedSnapshots(
	ctx context.Context,
) error {
	err := owner.withManager(func(manager *replica.Manager) error {
		return manager.QueuePublishedSnapshots(ctx)
	})
	if errors.Is(err, replica.ErrRemoteUnavailable) {
		if markerErr := owner.markPending(
			"replica.remote_unavailable",
		); markerErr != nil {
			return markerErr
		}
		owner.signalWorker()
		return nil
	}
	if err != nil {
		return err
	}
	if err := owner.persistCurrentStatus(
		context.WithoutCancel(ctx),
		"pending",
		true,
	); err != nil {
		return err
	}
	owner.signalWorker()
	return nil
}

func (owner *productionReplicaConflict) markPending(reason string) error {
	if owner == nil || owner.pendingPath == "" {
		return replica.ErrReplicationUnavailable
	}
	if err := owner.persistCurrentStatus(
		context.Background(),
		"failed",
		true,
	); err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Pending   bool      `json:"pending"`
		Reason    string    `json:"reason"`
		UpdatedAt time.Time `json:"updatedAt"`
	}{
		Pending: true, Reason: reason, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	temp := filepath.Join(
		filepath.Dir(owner.pendingPath),
		"."+filepath.Base(owner.pendingPath)+"."+uuid.NewString()+".tmp",
	)
	file, err := os.OpenFile(
		temp,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temp)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceGrantedFile(temp, owner.pendingPath); err != nil {
		return err
	}
	remove = false
	return nil
}

func (owner *productionReplicaConflict) markLocalMutationPending() error {
	if err := owner.markPending(
		"replica.local_mutation_not_snapshotted",
	); err != nil {
		return err
	}
	// markPending uses "failed" for actual remote failures. A newly committed
	// local mutation is healthy but necessarily pending until a snapshot is
	// captured and its authenticated checkpoint is reopened.
	return owner.persistCurrentStatus(
		context.Background(),
		"pending",
		true,
	)
}

func (owner *productionReplicaConflict) clearPending() error {
	if owner == nil || owner.pendingPath == "" {
		return nil
	}
	err := os.Remove(owner.pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (owner *productionReplicaConflict) register() {
	if owner == nil ||
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
	if owner.remoteFactory != nil ||
		(owner.manager != nil && owner.manager.ConflictReady()) {
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
	status, found, err := owner.runtime.state.replicaStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("replica.status_projection_missing")
	}
	if _, markerErr := os.Stat(owner.pendingPath); markerErr == nil &&
		(!status.PendingSync || status.SyncState == "replicated") {
		status.PendingSync = true
		status.SyncState = "failed"
		status.UpdatedAt = time.Now().UTC()
		if err := owner.runtime.state.putReplicaStatus(ctx, status); err != nil {
			return nil, err
		}
	} else if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return nil, markerErr
	}
	return map[string]any{
		"coordinationStrength": status.CoordinationStrength,
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
	token, _ := owner.runtime.coordinator.Current()
	if err := owner.runtime.state.commitOperationReceipt(
		context.WithoutCancel(ctx),
		protocolv2.Session{
			WorkspaceID: token.WorkspaceID,
			Epoch:       token.SessionEpoch,
			Sequence:    wire.Sequence,
		},
		receipt,
	); err != nil {
		return nil, err
	}
	err = owner.withManager(func(manager *replica.Manager) error {
		return manager.QueueSynchronize(ctx, receipt)
	})
	if errors.Is(err, replica.ErrRemoteUnavailable) {
		if err := owner.markPending("replica.remote_unavailable"); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := owner.persistCurrentStatus(
		context.WithoutCancel(ctx),
		"pending",
		true,
	); err != nil {
		return nil, err
	}
	owner.signalWorker()
	return result, nil
}

func (owner *productionReplicaConflict) persistStatus(
	ctx context.Context,
	strength replica.CoordinationStrength,
	syncState string,
	pending bool,
) error {
	if owner == nil || owner.runtime == nil || owner.runtime.state == nil {
		return errors.New("replica.status_projection_unavailable")
	}
	if strength != replica.Strong && strength != replica.Advisory {
		return errors.New("replica.status_projection_invalid")
	}
	return owner.runtime.state.putReplicaStatus(
		ctx,
		replicaStatusProjection{
			CoordinationStrength: string(strength),
			SyncState:            syncState,
			PendingSync:          pending,
			UpdatedAt:            time.Now().UTC(),
		},
	)
}

func (owner *productionReplicaConflict) persistCurrentStatus(
	ctx context.Context,
	syncState string,
	pending bool,
) error {
	strength := replica.Advisory
	if owner != nil && owner.runtime != nil && owner.runtime.state != nil {
		current, found, err := owner.runtime.state.replicaStatus(ctx)
		if err != nil {
			return err
		}
		if found {
			strength = replica.CoordinationStrength(
				current.CoordinationStrength,
			)
		}
	}
	return owner.persistStatus(ctx, strength, syncState, pending)
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
	var claim replica.Claim
	err = owner.withManager(func(manager *replica.Manager) error {
		var takeoverErr error
		claim, takeoverErr = manager.ForceTakeoverWithReceipt(
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
		return takeoverErr
	})
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

type conflictSummaryProjection struct {
	ConflictID string `json:"conflictId"`
	State      string `json:"state"`
	CreatedAt  string `json:"createdAt"`
	ItemCount  int    `json:"itemCount"`
}

type conflictItemProjection struct {
	ConflictID     string   `json:"conflictId"`
	ItemID         string   `json:"itemId"`
	Kind           string   `json:"kind"`
	Path           string   `json:"path"`
	State          string   `json:"state"`
	LocalSummary   string   `json:"localSummary"`
	ReplicaSummary string   `json:"replicaSummary"`
	BaseSummary    string   `json:"baseSummary"`
	Dependencies   []string `json:"dependencies"`
	Selected       *string  `json:"selected"`
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
	items := make([]conflictSummaryProjection, 0, len(sets))
	for _, set := range sets {
		plan := conflictresolution.BuildPlan(
			set.Base, set.Local, set.Replica,
		)
		items = append(items, conflictSummaryProjection{
			ConflictID: set.ConflictID,
			State:      conflictProjectionState(set.State),
			CreatedAt: set.CreatedAt.UTC().Format(
				time.RFC3339Nano,
			),
			ItemCount: len(plan.Files) + len(plan.Tables),
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
	items := make(
		[]conflictItemProjection,
		0,
		len(plan.Files)+len(plan.Tables),
	)
	for _, item := range plan.Files {
		items = append(items, conflictItemProjection{
			ConflictID:     set.ConflictID,
			ItemID:         item.DocumentID,
			Kind:           string(conflictresolution.FileItem),
			Path:           conflictFilePath(item),
			State:          conflictProjectionState(set.State),
			LocalSummary:   conflictFileSummary(item.Local),
			ReplicaSummary: conflictFileSummary(item.Replica),
			BaseSummary:    conflictFileSummary(item.Base),
			Dependencies: append(
				[]string(nil),
				set.Dependencies.Edges[item.DocumentID]...,
			),
			Selected: nil,
		})
	}
	for _, item := range plan.Tables {
		items = append(items, conflictItemProjection{
			ConflictID:     set.ConflictID,
			ItemID:         item.TableID,
			Kind:           string(conflictresolution.TableItem),
			Path:           conflictTableName(item),
			State:          conflictProjectionState(set.State),
			LocalSummary:   conflictTableSummary(item.Local),
			ReplicaSummary: conflictTableSummary(item.Replica),
			BaseSummary:    conflictTableSummary(item.Base),
			Dependencies: append(
				[]string(nil),
				set.Dependencies.Edges[item.TableID]...,
			),
			Selected: nil,
		})
	}
	return map[string]any{
		"conflictId": set.ConflictID,
		"state":      conflictProjectionState(set.State),
		"items":      items,
	}, nil
}

func conflictProjectionState(state conflictresolution.State) string {
	switch state {
	case conflictresolution.StatePending:
		return "pending"
	case conflictresolution.StateApplying:
		return "validating"
	case conflictresolution.StateApplied:
		return "ready"
	default:
		return "failed"
	}
}

func conflictFilePath(item conflictresolution.FileConflict) string {
	for _, path := range []string{
		item.Local.Path, item.Replica.Path, item.Base.Path,
	} {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	return item.DocumentID
}

func conflictFileSummary(state conflictresolution.FileState) string {
	if state.Deleted {
		return "deleted"
	}
	if strings.TrimSpace(state.Path) == "" {
		return "missing"
	}
	return state.Path + " · " + state.ContentID
}

func conflictTableName(item conflictresolution.TableConflict) string {
	for _, name := range []string{
		item.Local.DisplayName,
		item.Replica.DisplayName,
		item.Base.DisplayName,
	} {
		if strings.TrimSpace(name) != "" {
			return name
		}
	}
	return item.TableID
}

func conflictTableSummary(state conflictresolution.TableState) string {
	if state.Deleted {
		return "deleted"
	}
	if strings.TrimSpace(state.DisplayName) == "" {
		return "missing"
	}
	return state.DisplayName + " · schema/records/views/attachments"
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
		owner.conflicts.LoadOperationReceipt,
		owner.applier.LoadOperationReceipt,
	}
	owner.managerMu.RLock()
	if owner.manager != nil {
		loaders = append(
			[]func(
				context.Context,
				string,
				string,
			) (protocolv2.OperationReceipt, bool, error){
				owner.manager.LoadOperationReceipt,
			},
			loaders...,
		)
	}
	defer owner.managerMu.RUnlock()
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
	if owner.cancel != nil {
		owner.cancel()
		owner.wg.Wait()
		owner.cancel = nil
	}
	owner.managerMu.Lock()
	if owner.manager != nil {
		errs = append(errs, owner.manager.Close())
		owner.manager = nil
	}
	owner.managerMu.Unlock()
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
		kind := change.Kind
		if kind == "" && change.DocumentID != "" {
			kind = conflictresolution.FileItem
		}
		if kind != conflictresolution.FileItem {
			// Whole-table publication needs the PocketBase candidate database
			// adapter. Until that production adapter can prove an atomic
			// schema/records/views/attachments install, fail closed.
			return conflictresolution.ApplyStage{},
				conflictresolution.ErrApplyUnproven
		}
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
