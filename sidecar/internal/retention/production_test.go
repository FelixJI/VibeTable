package retention

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type productionInventory struct {
	value Inventory
	err   error
}

func (source *productionInventory) Inventory(
	context.Context,
) (Inventory, error) {
	return source.value, source.err
}

type recordingMaintainer struct {
	source       *productionInventory
	requested    []objectrepo.ObjectID
	result       MaintenanceResult
	err          error
	expectedSeen uint64
}

func (maintainer *recordingMaintainer) RetireAndMaintain(
	_ context.Context,
	_ objectrepo.Authority,
	expected uint64,
	ids []objectrepo.ObjectID,
) (MaintenanceResult, error) {
	maintainer.expectedSeen = expected
	maintainer.requested = append([]objectrepo.ObjectID(nil), ids...)
	if maintainer.err != nil {
		return MaintenanceResult{}, maintainer.err
	}
	delete(maintainer.source.value.Nodes, "garbage")
	maintainer.source.value.Revision++
	result := maintainer.result
	if result.BeforeRevision == 0 {
		result.BeforeRevision = expected
		result.AfterRevision = expected + 1
		result.DeletedObjects = len(ids)
		result.ReclaimedBytes = 40
		result.VerificationRun = true
	}
	return result, nil
}

func TestProductionPlanApplyPersistsGraceAndSweepsAfterRestart(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retention.db")
	source := &productionInventory{value: Inventory{
		Revision: 7,
		Nodes: map[objectrepo.ObjectID]Node{
			"root":    {ID: "root", Size: 10},
			"garbage": {ID: "garbage", Size: 40},
		},
		Roots: []objectrepo.ObjectID{"root"},
	}}
	maintainer := &recordingMaintainer{source: source}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	production := newProductionFixture(source, maintainer, store, &now)
	policy := Policy{
		TrashGrace: time.Hour,
	}
	preview, err := production.Plan(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ReclaimableBytes != 40 || preview.PlanID == "" {
		t.Fatalf("preview = %#v", preview)
	}
	applied, err := production.Apply(ctx, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.TombstonedObjects != 1 ||
		applied.DeletedObjects != 1 ||
		len(maintainer.requested) != 0 {
		t.Fatalf("apply before grace = %#v, %#v", applied, maintainer.requested)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now = now.Add(2 * time.Hour)
	reopened := newProductionFixture(source, maintainer, store, &now)
	result, err := reopened.Sweep(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedObjects != 1 ||
		result.ReclaimedBytes != 40 ||
		maintainer.expectedSeen != 7 ||
		len(maintainer.requested) != 1 ||
		maintainer.requested[0] != "garbage" {
		t.Fatalf("maintenance = %#v, requested=%#v", result, maintainer.requested)
	}
	tombstones, err := store.tombstones(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tombstones) != 1 || tombstones[0].MaintainedAt == nil {
		t.Fatalf("tombstones = %#v", tombstones)
	}
}

func TestProductionApplyRejectsInventoryDriftAndExpiredPlan(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		mutate func(*productionInventory, *time.Time)
		want   error
	}{
		"revision": {
			mutate: func(source *productionInventory, _ *time.Time) {
				source.value.Revision++
			},
			want: ErrInventoryChanged,
		},
		"digest": {
			mutate: func(source *productionInventory, _ *time.Time) {
				source.value.Nodes["garbage"] = Node{
					ID: "garbage", Size: 41,
				}
			},
			want: ErrInventoryChanged,
		},
		"expired": {
			mutate: func(_ *productionInventory, now *time.Time) {
				*now = now.Add(defaultPlanTTL)
			},
			want: errors.New("retention.plan_expired"),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			source := &productionInventory{value: Inventory{
				Revision: 1,
				Nodes: map[objectrepo.ObjectID]Node{
					"garbage": {ID: "garbage", Size: 40},
				},
			}}
			store, err := OpenStore(filepath.Join(t.TempDir(), "retention.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
			production := newProductionFixture(
				source,
				&recordingMaintainer{source: source},
				store,
				&now,
			)
			preview, err := production.Plan(
				ctx,
				Policy{TrashGrace: time.Hour},
			)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(source, &now)
			_, err = production.Apply(ctx, preview.PlanID)
			if test.want == ErrInventoryChanged {
				if !errors.Is(err, test.want) {
					t.Fatalf("Apply error = %v", err)
				}
			} else if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("Apply error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSweepFailsClosedWhenTombstonedObjectBecomesReachable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &productionInventory{value: Inventory{
		Revision: 1,
		Nodes: map[objectrepo.ObjectID]Node{
			"garbage": {ID: "garbage", Size: 40},
		},
	}}
	store, err := OpenStore(filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	maintainer := &recordingMaintainer{source: source}
	production := newProductionFixture(source, maintainer, store, &now)
	policy := Policy{TrashGrace: time.Hour}
	preview, err := production.Plan(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := production.Apply(ctx, preview.PlanID); err != nil {
		t.Fatal(err)
	}
	source.value.Roots = []objectrepo.ObjectID{"garbage"}
	now = now.Add(2 * time.Hour)
	if _, err := production.Sweep(ctx, policy); !errors.Is(
		err,
		ErrInventoryChanged,
	) {
		t.Fatalf("Sweep error = %v", err)
	}
	if len(maintainer.requested) != 0 {
		t.Fatalf("unsafe maintenance request = %#v", maintainer.requested)
	}
}

func TestProductionFailsClosedForUnsafeRepositoryEvidence(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Inventory){
		"pending publication": func(value *Inventory) {
			value.PendingPublication = true
		},
		"unknown manifest": func(value *Inventory) {
			value.UnknownManifest = true
		},
		"corrupt index": func(value *Inventory) {
			value.CorruptIndex = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			source := &productionInventory{value: Inventory{
				Revision: 1,
				Nodes:    map[objectrepo.ObjectID]Node{},
			}}
			mutate(&source.value)
			store, err := OpenStore(filepath.Join(t.TempDir(), "retention.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now().UTC()
			production := newProductionFixture(
				source,
				&recordingMaintainer{source: source},
				store,
				&now,
			)
			if _, err := production.Plan(
				context.Background(),
				Policy{TrashGrace: time.Hour},
			); !errors.Is(err, ErrUnsafeInventory) {
				t.Fatalf("Plan error = %v", err)
			}
		})
	}
}

func TestIntegrityCadencePersistsAndCorruptionBlocksDestructiveWork(
	t *testing.T,
) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retention.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	mode, due, err := store.IntegrityDue(ctx, now)
	if err != nil || !due || mode != IntegrityFull {
		t.Fatalf("initial due=%v mode=%q err=%v", due, mode, err)
	}
	if err := store.RecordIntegritySuccess(
		ctx,
		IntegrityFull,
		9,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if mode, due, err = store.IntegrityDue(
		ctx,
		now.Add(time.Hour),
	); err != nil || due || mode != "" {
		t.Fatalf("restart duplicated due=%v mode=%q err=%v", due, mode, err)
	}
	if mode, due, err = store.IntegrityDue(
		ctx,
		now.Add(25*time.Hour),
	); err != nil || !due || mode != IntegrityIncremental {
		t.Fatalf("daily due=%v mode=%q err=%v", due, mode, err)
	}
	if err := store.RecordIntegrityFailure(
		ctx,
		IntegrityIncremental,
		"repository.corrupt",
	); err != nil {
		t.Fatal(err)
	}
	source := &productionInventory{value: Inventory{
		Revision: 1,
		Nodes:    map[objectrepo.ObjectID]Node{},
	}}
	production := newProductionFixture(
		source,
		&recordingMaintainer{source: source},
		store,
		&now,
	)
	if _, err := production.Plan(
		ctx,
		Policy{TrashGrace: time.Hour},
	); err == nil || err.Error() != "retention.integrity_corrupt" {
		t.Fatalf("corrupt retention Plan error = %v", err)
	}
	if err := store.RecordIntegritySuccess(
		ctx,
		IntegrityIncremental,
		10,
		now.Add(25*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := production.Plan(
		ctx,
		Policy{TrashGrace: time.Hour},
	); err != nil {
		t.Fatalf("verified retention remained blocked: %v", err)
	}
	if mode, due, err = store.IntegrityDue(
		ctx,
		now.AddDate(0, 1, 0),
	); err != nil || !due || mode != IntegrityFull {
		t.Fatalf("monthly due=%v mode=%q err=%v", due, mode, err)
	}
}

func TestQuotaPauseStatePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retention.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	limit := uint64(1024)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if err := store.RecordQuotaState(
		ctx,
		2048,
		&limit,
		true,
		"snapshot.repository_limit_reached",
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.QuotaState(ctx)
	if err != nil ||
		state.UsageBytes != 2048 ||
		state.LimitBytes == nil ||
		*state.LimitBytes != limit ||
		!state.AutomaticSnapshotsPaused ||
		state.Warning != "snapshot.repository_limit_reached" ||
		!state.UpdatedAt.Equal(now) {
		t.Fatalf("restarted quota state = %#v, %v", state, err)
	}
}

func TestApplyPersistsSnapshotTombstoneAndCoordinatorReceiptAtomically(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	source := &productionInventory{value: Inventory{
		Revision: 3,
		Nodes: map[objectrepo.ObjectID]Node{
			"snapshot-root": {ID: "snapshot-root", Size: 12},
		},
		Snapshots: []Snapshot{{
			SnapshotID: "snapshot-old",
			Root:       "snapshot-root",
			CreatedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
	}}
	store, err := OpenStore(filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	production := newProductionFixture(
		source,
		&recordingMaintainer{source: source},
		store,
		&now,
	)
	preview, err := production.Plan(
		ctx,
		Policy{TrashGrace: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	identity := MutationIdentity{
		WorkspaceID:      "workspace",
		MutationRevision: 9,
		SessionEpoch:     4,
		FenceEpoch:       2,
		ClaimID:          "claim",
	}
	if _, err := production.Apply(
		ctx,
		preview.PlanID,
		identity,
	); err != nil {
		t.Fatal(err)
	}
	tombstoned, err := store.TombstonedSnapshotIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := tombstoned["snapshot-old"]; !found {
		t.Fatalf("snapshot tombstones = %#v", tombstoned)
	}
	committed, err := store.HasCommittedMutation(ctx, identity)
	if err != nil || !committed {
		t.Fatalf("mutation receipt = %v, %v", committed, err)
	}
}

func newProductionFixture(
	source *productionInventory,
	maintainer *recordingMaintainer,
	store *Store,
	now *time.Time,
) *Production {
	result := NewProduction(
		source,
		maintainer,
		store,
		func() objectrepo.Authority {
			return objectrepo.Authority{
				WorkspaceID: "workspace",
				FenceEpoch:  1,
				ClaimID:     "claim",
			}
		},
	)
	result.now = func() time.Time { return now.UTC() }
	return result
}
