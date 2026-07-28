package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	store      *retention.Store
	production *retention.Production
	source     *workspaceRetentionInventory
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	sweepMu    sync.Mutex
}

type RetentionProtectionStatus struct {
	Quota     retention.QuotaState
	Integrity retention.IntegrityState
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
	return RetentionProtectionStatus{
		Quota: quota, Integrity: integrity,
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
		if err := composition.runIntegrityIfDue(
			ctx, time.Now().UTC(),
		); err == nil {
			_, _ = composition.sweep(ctx)
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := composition.runIntegrityIfDue(
					ctx, now.UTC(),
				); err == nil {
					_, _ = composition.sweep(ctx)
				}
			}
		}
	}()
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
	for _, pin := range repositoryInventory.Pins {
		if pin.WorkspaceID != source.runtime.manifest.WorkspaceID {
			result.UnknownManifest = true
			continue
		}
		result.PinnedRoots = append(
			result.PinnedRoots,
			pin.Roots...,
		)
	}
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
	policy RetentionPolicy,
	now time.Time,
) ([]objectrepo.ObjectID, error) {
	var roots []objectrepo.ObjectID
	for _, document := range documents {
		children := make(map[string]int, len(document.Revisions))
		byID := make(
			map[string]filehistory.Revision,
			len(document.Revisions),
		)
		for _, revision := range document.Revisions {
			byID[revision.RevisionID] = revision
			if revision.ParentRevisionID != nil {
				children[*revision.ParentRevisionID]++
			}
		}
		selected := map[string]struct{}{}
		for _, revision := range document.Revisions {
			if revision.RevisionID == document.EffectiveRevisionID ||
				revision.FormalVersion != nil ||
				children[revision.RevisionID] == 0 ||
				children[revision.RevisionID] > 1 {
				selected[revision.RevisionID] = struct{}{}
			}
			if revision.RestoredFromRevisionID != nil {
				selected[*revision.RestoredFromRevisionID] = struct{}{}
			}
		}
		revisions := append(
			[]filehistory.Revision(nil),
			document.Revisions...,
		)
		sort.Slice(revisions, func(left, right int) bool {
			if revisions[left].CreatedAt.Equal(revisions[right].CreatedAt) {
				return revisions[left].RevisionID < revisions[right].RevisionID
			}
			return revisions[left].CreatedAt.After(revisions[right].CreatedAt)
		})
		for index := 0; index < len(revisions) &&
			index < int(policy.FileRevisionCount); index++ {
			selected[revisions[index].RevisionID] = struct{}{}
		}
		selectFileRevisionBuckets(
			selected,
			revisions,
			policy,
			now,
		)
		// Deletion time is not yet present in FileDocument v2. Retain every
		// revision of deleted documents rather than guessing the three-month
		// trash deadline and risking data loss.
		if document.Status == filehistory.DocumentDeleted {
			for _, revision := range revisions {
				selected[revision.RevisionID] = struct{}{}
			}
		}
		for revisionID := range selected {
			revision, found := byID[revisionID]
			if !found || revision.ObjectID == "" {
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

func selectFileRevisionBuckets(
	selected map[string]struct{},
	revisions []filehistory.Revision,
	policy RetentionPolicy,
	now time.Time,
) {
	window := time.Duration(policy.FileRevisionDays) * 24 * time.Hour
	for _, bucket := range policy.FileRevisionBuckets {
		seen := map[string]struct{}{}
		for _, revision := range revisions {
			age := now.Sub(revision.CreatedAt)
			if age < 0 || age > window {
				continue
			}
			var key string
			switch bucket {
			case "daily":
				key = revision.CreatedAt.UTC().Format("2006-01-02")
			case "weekly":
				year, week := revision.CreatedAt.UTC().ISOWeek()
				key = fmt.Sprintf("%04d-W%02d", year, week)
			case "monthly":
				key = revision.CreatedAt.UTC().Format("2006-01")
			default:
				continue
			}
			if _, found := seen[key]; found {
				continue
			}
			seen[key] = struct{}{}
			selected[revision.RevisionID] = struct{}{}
		}
	}
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
