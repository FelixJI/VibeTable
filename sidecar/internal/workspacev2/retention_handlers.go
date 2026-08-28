package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/retention"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type productionRetention struct {
	store               *retention.Store
	production          *retention.Production
	source              *workspaceRetentionInventory
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	sweepMu             sync.Mutex
	backgroundIntegrity func(context.Context, time.Time) error
	backgroundSweep     func(context.Context) (retention.MaintenanceResult, error)
	logf                func(string, ...any)
}

type RetentionProtectionStatus struct {
	Quota       retention.QuotaState
	Integrity   retention.IntegrityState
	Maintenance retention.MaintenanceState
}

// RetentionProtectionStatus is a production status seam for the UI/RPC
// contract. Both quota pauses and integrity failures survive process restarts.
func (runtime *Runtime) RetentionProtectionStatus(
	ctx context.Context,
) (RetentionProtectionStatus, error) {
	if runtime == nil ||
		runtime.retention == nil ||
		runtime.retention.store == nil {
		return RetentionProtectionStatus{},
			errors.New("retention.production_unavailable")
	}
	quota, err := runtime.retention.store.QuotaState(ctx)
	if err != nil {
		return RetentionProtectionStatus{}, err
	}
	integrity, err := runtime.retention.store.IntegrityState(ctx)
	if err != nil {
		return RetentionProtectionStatus{}, err
	}
	maintenance, err := runtime.retention.store.MaintenanceState(ctx)
	if err != nil {
		return RetentionProtectionStatus{}, err
	}
	return RetentionProtectionStatus{
		Quota: quota, Integrity: integrity, Maintenance: maintenance,
	}, nil
}

func openProductionRetentionStore(path string) (*productionRetention, error) {
	store, err := retention.OpenStore(path)
	if err != nil {
		return nil, err
	}
	return &productionRetention{store: store}, nil
}

func (composition *productionRetention) bind(runtime *Runtime) error {
	if composition == nil ||
		composition.store == nil ||
		runtime == nil ||
		runtime.repository == nil ||
		runtime.catalog == nil ||
		runtime.history == nil ||
		runtime.state == nil ||
		runtime.coordinator == nil {
		return errors.New("retention.production_dependencies_required")
	}
	source := &workspaceRetentionInventory{
		runtime: runtime,
		store:   composition.store,
		now:     func() time.Time { return time.Now().UTC() },
	}
	maintainer := &workspaceRetentionMaintainer{
		repository: runtime.repository,
	}
	composition.source = source
	composition.production = retention.NewProduction(
		source,
		maintainer,
		composition.store,
		func() objectrepo.Authority {
			token, _ := runtime.coordinator.Current()
			return token.Authority()
		},
	)
	return nil
}

func (composition *productionRetention) close() error {
	if composition == nil || composition.store == nil {
		return nil
	}
	if composition.cancel != nil {
		composition.cancel()
		composition.wg.Wait()
		composition.cancel = nil
	}
	err := composition.store.Close()
	composition.store = nil
	composition.production = nil
	return err
}

func (composition *productionRetention) start() {
	if composition == nil || composition.production == nil ||
		composition.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	composition.cancel = cancel
	composition.wg.Add(1)
	go func() {
		defer composition.wg.Done()
		// Integrity cadence is durable. Startup and hourly ticks only run work
		// that is due, so restart never duplicates the daily/monthly check.
		composition.runBackgroundMaintenance(ctx, time.Now().UTC())
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				composition.runBackgroundMaintenance(ctx, now.UTC())
			}
		}
	}()
}

func (composition *productionRetention) runBackgroundMaintenance(
	ctx context.Context,
	now time.Time,
) {
	integrity := composition.runIntegrityIfDue
	if composition.backgroundIntegrity != nil {
		integrity = composition.backgroundIntegrity
	}
	if err := integrity(ctx, now); err != nil {
		if ctx.Err() == nil {
			composition.recordBackgroundFailure(
				ctx,
				retention.MaintenanceIntegrity,
				err,
				now,
			)
		}
		return
	}
	sweep := composition.sweep
	if composition.backgroundSweep != nil {
		sweep = composition.backgroundSweep
	}
	if _, err := sweep(ctx); err != nil {
		if ctx.Err() == nil {
			composition.recordBackgroundFailure(
				ctx,
				retention.MaintenanceSweep,
				err,
				now,
			)
		}
		return
	}
	if err := composition.store.RecordMaintenanceSuccess(
		context.WithoutCancel(ctx),
	); err != nil {
		composition.logBackground(
			"workspace retention background status clear failed: %v",
			err,
		)
	}
}

func (composition *productionRetention) recordBackgroundFailure(
	ctx context.Context,
	stage retention.MaintenanceStage,
	failure error,
	at time.Time,
) {
	persistErr := composition.store.RecordMaintenanceFailure(
		context.WithoutCancel(ctx),
		stage,
		failure.Error(),
		at,
	)
	if persistErr != nil {
		composition.logBackground(
			"workspace retention background %s failed: %v; "+
				"status persistence failed: %v",
			stage,
			failure,
			persistErr,
		)
		return
	}
	composition.logBackground(
		"workspace retention background %s failed: %v",
		stage,
		failure,
	)
}

func (composition *productionRetention) logBackground(
	format string,
	arguments ...any,
) {
	if composition.logf != nil {
		composition.logf(format, arguments...)
		return
	}
	log.Printf(format, arguments...)
}

func (composition *productionRetention) runIntegrityIfDue(
	ctx context.Context,
	now time.Time,
) error {
	composition.sweepMu.Lock()
	defer composition.sweepMu.Unlock()
	mode, due, err := composition.store.IntegrityDue(ctx, now)
	if err != nil || !due {
		return err
	}
	records, err := composition.source.runtime.catalog.List(
		ctx,
		composition.source.runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return err
	}
	state, err := composition.store.IntegrityState(ctx)
	if err != nil {
		return err
	}
	var latestSequence uint64
	for _, record := range records {
		if record.SnapshotSequence > latestSequence {
			latestSequence = record.SnapshotSequence
		}
		if mode == retention.IntegrityIncremental &&
			record.SnapshotSequence <= state.LastSnapshotSequence {
			continue
		}
		valid, verifyErr := composition.source.runtime.snapshotIntegrity(
			ctx,
			record,
		)
		if verifyErr != nil {
			if errors.Is(verifyErr, objectrepo.ErrCorrupt) ||
				errors.Is(verifyErr, objectrepo.ErrNotFound) {
				_ = composition.store.RecordIntegrityFailure(
					context.WithoutCancel(ctx),
					mode,
					verifyErr.Error(),
				)
			}
			return verifyErr
		}
		if !valid {
			corruptErr := errors.New("retention.integrity_corrupt")
			if recordErr := composition.store.RecordIntegrityFailure(
				context.WithoutCancel(ctx),
				mode,
				corruptErr.Error(),
			); recordErr != nil {
				return errors.Join(corruptErr, recordErr)
			}
			return corruptErr
		}
	}
	if mode == retention.IntegrityFull {
		inventory, inventoryErr := composition.source.Inventory(ctx)
		if inventoryErr != nil {
			return inventoryErr
		}
		if inventory.PendingPublication {
			return errors.New("retention.integrity_pending_publication")
		}
		if inventory.UnknownManifest || inventory.CorruptIndex {
			corruptErr := errors.New("retention.integrity_corrupt")
			if recordErr := composition.store.RecordIntegrityFailure(
				context.WithoutCancel(ctx),
				mode,
				corruptErr.Error(),
			); recordErr != nil {
				return errors.Join(corruptErr, recordErr)
			}
			return corruptErr
		}
	}
	return composition.store.RecordIntegritySuccess(
		context.WithoutCancel(ctx),
		mode,
		latestSequence,
		now,
	)
}

func (composition *productionRetention) register(runtime *Runtime) {
	runtime.dispatcher.Register(
		"retention.plan",
		protocolv2.WorkspaceScope,
		composition.plan,
	)
	runtime.dispatcher.Register(
		"retention.apply",
		protocolv2.WorkspaceScope,
		composition.apply,
	)
}

func (composition *productionRetention) plan(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("retention.request_invalid")
	}
	policy, _, err := composition.source.runtime.state.retention(ctx)
	if err != nil {
		return nil, err
	}
	result, err := composition.production.PlanWithReceipt(
		ctx,
		retentionPolicy(policy),
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"planId":           result.PlanID,
		"reclaimableBytes": result.ReclaimableBytes,
		"blockedReasons":   result.BlockedReasons,
	}, nil
}

type applyRetentionParams struct {
	PlanID string `json:"planId"`
}

func (composition *productionRetention) apply(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[applyRetentionParams](paramsRaw)
	if err != nil || !validUUID(params.PlanID) {
		return nil, errors.New("retention.request_invalid")
	}
	runtime := composition.source.runtime
	token, _ := runtime.coordinator.Current()
	var result retention.ApplyResult
	_, err = runtime.coordinator.Write(
		ctx,
		token,
		func(
			ctx context.Context,
			intent writecoordinator.WriteIntent,
		) error {
			var applyErr error
			result, applyErr = composition.production.ApplyWithReceipt(
				ctx,
				params.PlanID,
				retention.MutationIdentity{
					WorkspaceID:      intent.Token.WorkspaceID,
					MutationRevision: intent.MutationRevision,
					SessionEpoch:     intent.Token.SessionEpoch,
					FenceEpoch:       intent.Token.FenceEpoch,
					ClaimID:          intent.Token.ClaimID,
				},
			)
			return applyErr
		},
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"deletedObjects": result.DeletedObjects,
		"reclaimedBytes": result.ReclaimedBytes,
	}, nil
}

func (composition *productionRetention) sweep(
	ctx context.Context,
) (retention.MaintenanceResult, error) {
	composition.sweepMu.Lock()
	defer composition.sweepMu.Unlock()
	policy, _, err := composition.source.runtime.state.retention(ctx)
	if err != nil {
		return retention.MaintenanceResult{}, err
	}
	return composition.production.Sweep(ctx, retentionPolicy(policy))
}

func (composition *productionRetention) hasCommittedMutation(
	ctx context.Context,
	intent writecoordinator.WriteIntent,
) (bool, error) {
	return composition.store.HasCommittedMutation(
		ctx,
		retention.MutationIdentity{
			WorkspaceID:      intent.Token.WorkspaceID,
			MutationRevision: intent.MutationRevision,
			SessionEpoch:     intent.Token.SessionEpoch,
			FenceEpoch:       intent.Token.FenceEpoch,
			ClaimID:          intent.Token.ClaimID,
		},
	)
}

type workspaceRetentionMaintainer struct {
	repository objectrepo.RetentionMaintainer
}

func (maintainer *workspaceRetentionMaintainer) RetireAndMaintain(
	ctx context.Context,
	authority objectrepo.Authority,
	expectedRevision uint64,
	ids []objectrepo.ObjectID,
) (retention.MaintenanceResult, error) {
	result, err := maintainer.repository.RetireAndMaintain(
		ctx,
		objectrepo.RetentionMaintenanceRequest{
			Authority:        authority,
			ExpectedRevision: expectedRevision,
			ObjectIDs:        ids,
		},
	)
	if err != nil {
		return retention.MaintenanceResult{}, err
	}
	return retention.MaintenanceResult{
		DeletedObjects:  result.DeletedObjects,
		ReclaimedBytes:  result.ReclaimedBytes,
		BeforeRevision:  result.BeforeRevision,
		AfterRevision:   result.AfterRevision,
		VerificationRun: result.VerificationRun,
	}, nil
}

type workspaceRetentionInventory struct {
	runtime *Runtime
	store   *retention.Store
	now     func() time.Time
}

func (source *workspaceRetentionInventory) Inventory(
	ctx context.Context,
) (retention.Inventory, error) {
	repositoryInventory, err := source.runtime.repository.RetentionInventory(ctx)
	if err != nil {
		return retention.Inventory{}, err
	}
	result := retention.Inventory{
		Revision:           repositoryInventory.Revision,
		Nodes:              map[objectrepo.ObjectID]retention.Node{},
		PendingPublication: repositoryInventory.PendingPublication,
		UnknownManifest:    repositoryInventory.UnknownManifest,
		CorruptIndex:       repositoryInventory.CorruptIndex,
	}
	for _, object := range repositoryInventory.Objects {
		if object.ID == "" || object.Size < 0 {
			result.CorruptIndex = true
			continue
		}
		result.Nodes[object.ID] = retention.Node{
			ID: object.ID, Size: object.Size,
		}
	}
	allTombstones, err := source.store.AllTombstones(ctx)
	if err != nil {
		return retention.Inventory{}, err
	}
	tombstonesByID := make(
		map[objectrepo.ObjectID]retention.Tombstone,
		len(allTombstones),
	)
	for _, item := range allTombstones {
		tombstonesByID[item.ObjectID] = item
	}
	for _, completed := range repositoryInventory.CompletedRetirements {
		tombstone, found := tombstonesByID[completed.ID]
		if !found ||
			tombstone.Size != completed.Size ||
			completed.ID == "" ||
			completed.Size < 0 {
			result.UnknownManifest = true
			continue
		}
		if tombstone.MaintainedAt != nil {
			continue
		}
		if _, duplicate := result.Nodes[completed.ID]; duplicate {
			result.CorruptIndex = true
			continue
		}
		// A completed repository journal is the durable proof needed to
		// recover a crash between physical verification and marking the
		// logical tombstone maintained in retention.db.
		result.Nodes[completed.ID] = retention.Node{
			ID: completed.ID, Size: completed.Size,
		}
	}
	pinnedRoots, foreignPin := activeRetentionPinRoots(
		repositoryInventory.Pins,
		source.runtime.manifest.WorkspaceID,
		source.now(),
	)
	result.PinnedRoots = append(result.PinnedRoots, pinnedRoots...)
	result.UnknownManifest = result.UnknownManifest || foreignPin
	tombstonedSnapshots, err := source.store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		return retention.Inventory{}, err
	}
	records, err := source.runtime.catalog.List(
		ctx,
		source.runtime.manifest.WorkspaceID,
	)
	if err != nil {
		return retention.Inventory{}, err
	}
	recordByID := make(map[string]snapshot.Record, len(records))
	for _, record := range records {
		recordByID[record.SnapshotID] = record
		if _, tombstoned := tombstonedSnapshots[record.SnapshotID]; tombstoned {
			continue
		}
		root := record.ObjectMap["file-state-root"]
		if root == "" {
			result.UnknownManifest = true
			continue
		}
		node, exists := result.Nodes[root]
		if !exists {
			result.CorruptIndex = true
			continue
		}
		for _, child := range record.Objects {
			if child == root {
				continue
			}
			if _, exists := result.Nodes[child]; !exists {
				result.CorruptIndex = true
				continue
			}
			node.Children = appendUniqueObjectID(node.Children, child)
		}
		historyObjects, historyErr := snapshot.HistoryObjectIDs(
			ctx,
			source.runtime.repository,
			record,
		)
		if historyErr != nil {
			if errors.Is(historyErr, snapshot.ErrBundleInvalid) ||
				errors.Is(historyErr, objectrepo.ErrNotFound) ||
				errors.Is(historyErr, objectrepo.ErrCorrupt) {
				result.CorruptIndex = true
				continue
			}
			return retention.Inventory{}, historyErr
		}
		for _, child := range historyObjects {
			if child == root {
				continue
			}
			if _, exists := result.Nodes[child]; !exists {
				result.CorruptIndex = true
				continue
			}
			node.Children = appendUniqueObjectID(node.Children, child)
		}
		result.Nodes[root] = node
		result.Snapshots = append(result.Snapshots, retention.Snapshot{
			SnapshotID: record.SnapshotID,
			Root:       root,
			CreatedAt:  record.CreatedAt,
			Pinned:     record.Pinned,
		})
	}
	policy, _, err := source.runtime.state.retention(ctx)
	if err != nil {
		return retention.Inventory{}, err
	}
	fileRoots, err := fileHistoryRetentionRoots(
		source.runtime.history.List(),
		policy,
		source.now().UTC(),
	)
	if err != nil {
		return retention.Inventory{}, err
	}
	result.Roots = append(result.Roots, fileRoots...)
	planRoots, err := source.pendingPlanRoots(
		ctx,
		recordByID,
		tombstonedSnapshots,
	)
	if err != nil {
		return retention.Inventory{}, err
	}
	result.Roots = append(result.Roots, planRoots...)
	sortRetentionInventory(&result)
	return result, nil
}

func activeRetentionPinRoots(
	pins []objectrepo.RootPin,
	workspaceID string,
	now time.Time,
) ([]objectrepo.ObjectID, bool) {
	activeRoots := make([]objectrepo.ObjectID, 0)
	foreignPin := false
	now = now.UTC()
	for _, pin := range pins {
		if pin.WorkspaceID != workspaceID {
			foreignPin = true
			continue
		}
		if pin.ExpiresAt != nil && !now.Before(pin.ExpiresAt.UTC()) {
			continue
		}
		activeRoots = append(activeRoots, pin.Roots...)
	}
	return activeRoots, foreignPin
}

func (source *workspaceRetentionInventory) pendingPlanRoots(
	ctx context.Context,
	records map[string]snapshot.Record,
	tombstonedSnapshots map[string]struct{},
) ([]objectrepo.ObjectID, error) {
	now := source.now().UTC()
	var roots []objectrepo.ObjectID
	rows, err := source.runtime.state.db.QueryContext(ctx, `
		SELECT snapshot_id, expires_at FROM snapshot_restore_plans
		UNION ALL
		SELECT snapshot_id, expires_at FROM snapshot_extract_plans`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var snapshotID, expiresRaw string
		if err := rows.Scan(&snapshotID, &expiresRaw); err != nil {
			return nil, err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, expiresRaw)
		if err != nil {
			return nil, errors.New("retention.pending_plan_corrupt")
		}
		if !now.Before(expiresAt) {
			continue
		}
		if _, tombstoned := tombstonedSnapshots[snapshotID]; tombstoned {
			return nil, errors.New("retention.pending_plan_tombstoned")
		}
		record, found := records[snapshotID]
		if !found {
			return nil, errors.New("retention.pending_plan_snapshot_missing")
		}
		root := record.ObjectMap["file-state-root"]
		if root == "" {
			return nil, errors.New("retention.pending_plan_root_missing")
		}
		roots = appendUniqueObjectID(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	extractRows, err := source.runtime.state.db.QueryContext(ctx, `
		SELECT object_id, expires_at FROM snapshot_extract_plans`)
	if err != nil {
		return nil, err
	}
	defer extractRows.Close()
	for extractRows.Next() {
		var rawID, expiresRaw string
		if err := extractRows.Scan(&rawID, &expiresRaw); err != nil {
			return nil, err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, expiresRaw)
		if err != nil {
			return nil, errors.New("retention.pending_plan_corrupt")
		}
		if now.Before(expiresAt) {
			roots = appendUniqueObjectID(
				roots,
				objectrepo.ObjectID(rawID),
			)
		}
	}
	return roots, extractRows.Err()
}

func retentionPolicy(policy RetentionPolicy) retention.Policy {
	window := time.Duration(policy.SnapshotDays) * 24 * time.Hour
	result := retention.Policy{
		TrashGrace:    time.Duration(policy.TrashMonths) * 30 * 24 * time.Hour,
		MinimumRecent: int(policy.SnapshotCount),
	}
	for _, bucket := range policy.SnapshotBuckets {
		switch bucket {
		case "hourly":
			result.KeepHourlyFor = window
		case "daily":
			result.KeepDailyFor = window
		case "weekly":
			result.KeepWeeklyFor = window
		case "monthly":
			result.KeepMonthlyFor = window
		}
	}
	return result
}

func fileHistoryRetentionRoots(
	documents []filehistory.Document,
	_ RetentionPolicy,
	_ time.Time,
) ([]objectrepo.ObjectID, error) {
	// The published FileHistory root still names every immutable revision and
	// Open verifies that entire closure. Until retention can transactionally
	// publish a pruned replacement root, every revision in the live root is a
	// strong GC root regardless of the user's future pruning policy.
	var roots []objectrepo.ObjectID
	for _, document := range documents {
		for _, revision := range document.Revisions {
			if revision.ObjectID == "" {
				return nil, retention.ErrUnsafeInventory
			}
			roots = appendUniqueObjectID(roots, revision.ObjectID)
		}
	}
	sort.Slice(roots, func(left, right int) bool {
		return roots[left] < roots[right]
	})
	return roots, nil
}

func appendUniqueObjectID(
	values []objectrepo.ObjectID,
	value objectrepo.ObjectID,
) []objectrepo.ObjectID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortRetentionInventory(inventory *retention.Inventory) {
	sort.Slice(inventory.Roots, func(left, right int) bool {
		return inventory.Roots[left] < inventory.Roots[right]
	})
	sort.Slice(inventory.PinnedRoots, func(left, right int) bool {
		return inventory.PinnedRoots[left] < inventory.PinnedRoots[right]
	})
	sort.Slice(inventory.Snapshots, func(left, right int) bool {
		return inventory.Snapshots[left].SnapshotID <
			inventory.Snapshots[right].SnapshotID
	})
	for id, node := range inventory.Nodes {
		sort.Slice(node.Children, func(left, right int) bool {
			return node.Children[left] < node.Children[right]
		})
		inventory.Nodes[id] = node
	}
}
