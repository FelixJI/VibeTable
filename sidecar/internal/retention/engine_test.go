package retention

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/quick"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type fakeInventory struct{ value Inventory }

func (source *fakeInventory) Inventory(context.Context) (Inventory, error) { return source.value, nil }

type fakeCleaner struct {
	ids   []objectrepo.ObjectID
	grace time.Duration
}

func (cleaner *fakeCleaner) Tombstone(_ context.Context, ids []objectrepo.ObjectID, grace time.Duration) error {
	cleaner.ids = ids
	cleaner.grace = grace
	return nil
}

func TestReachabilityPreservesAllDescendantsAndPlansOnlyGarbage(t *testing.T) {
	source := &fakeInventory{value: Inventory{
		Revision: 4,
		Nodes: map[objectrepo.ObjectID]Node{
			"snapshot":       {ID: "snapshot", Children: []objectrepo.ObjectID{"database", "effective-leaf"}},
			"database":       {ID: "database", Size: 10},
			"effective-leaf": {ID: "effective-leaf", Children: []objectrepo.ObjectID{"branch-point"}, Size: 20},
			"branch-point":   {ID: "branch-point", Size: 30},
			"garbage":        {ID: "garbage", Size: 40},
		},
		Roots: []objectrepo.ObjectID{"snapshot"},
	}}
	cleaner := &fakeCleaner{}
	engine := New(source, cleaner)
	plan, err := engine.Preview(context.Background(), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tombstone) != 1 || plan.Tombstone[0] != "garbage" || plan.ReclaimableBytes != 40 {
		t.Fatalf("unsafe plan: %#v", plan)
	}
	if err := engine.Apply(context.Background(), plan, DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if len(cleaner.ids) != 1 || cleaner.ids[0] != "garbage" {
		t.Fatalf("unexpected cleanup: %#v", cleaner.ids)
	}
	if cleaner.grace != DefaultPolicy().TrashGrace {
		t.Fatalf("cleanup grace = %s, want %s", cleaner.grace, DefaultPolicy().TrashGrace)
	}
}

func TestApplyRejectsStalePlanAndUnsafeInventory(t *testing.T) {
	source := &fakeInventory{value: Inventory{Revision: 1, Nodes: map[objectrepo.ObjectID]Node{}}}
	engine := New(source, &fakeCleaner{})
	plan, err := engine.Preview(context.Background(), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	source.value.Revision = 2
	if !errors.Is(engine.Apply(context.Background(), plan, DefaultPolicy()), ErrInventoryChanged) {
		t.Fatal("expected stale-plan rejection")
	}
	source.value.PendingPublication = true
	if _, err := engine.Preview(context.Background(), DefaultPolicy()); !errors.Is(err, ErrUnsafeInventory) {
		t.Fatalf("expected fail closed, got %v", err)
	}
}

func TestPreviewFailsClosedForMissingRootOrChild(t *testing.T) {
	tests := map[string]Inventory{
		"missing root": {
			Revision: 1,
			Nodes:    map[objectrepo.ObjectID]Node{},
			Roots:    []objectrepo.ObjectID{"missing"},
		},
		"missing child": {
			Revision: 1,
			Nodes: map[objectrepo.ObjectID]Node{
				"root": {ID: "root", Children: []objectrepo.ObjectID{"missing"}},
			},
			Roots: []objectrepo.ObjectID{"root"},
		},
		"missing snapshot root": {
			Revision: 1,
			Nodes:    map[objectrepo.ObjectID]Node{},
			Snapshots: []Snapshot{{
				SnapshotID: "snapshot-1",
				Roots:      []objectrepo.ObjectID{"missing"},
				CreatedAt:  time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
			}},
		},
	}
	for name, inventory := range tests {
		t.Run(name, func(t *testing.T) {
			engine := New(&fakeInventory{value: inventory}, &fakeCleaner{})
			if _, err := engine.Preview(context.Background(), DefaultPolicy()); !errors.Is(err, ErrUnsafeInventory) {
				t.Fatalf("expected fail-closed inventory error, got %v", err)
			}
		})
	}
}

func TestPolicyRetainsPinnedRecentAndTimeBuckets(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 30, 0, 0, time.UTC)
	snapshots := []Snapshot{
		{SnapshotID: "recent-new", Roots: []objectrepo.ObjectID{"recent-new"}, CreatedAt: now.Add(-10 * time.Minute)},
		{SnapshotID: "recent-second", Roots: []objectrepo.ObjectID{"recent-second"}, CreatedAt: now.Add(-20 * time.Minute)},
		{SnapshotID: "same-hour-old", Roots: []objectrepo.ObjectID{"same-hour-old"}, CreatedAt: now.Add(-30 * time.Minute)},
		{SnapshotID: "hour-previous", Roots: []objectrepo.ObjectID{"hour-previous"}, CreatedAt: now.Add(-90 * time.Minute)},
		{SnapshotID: "day-previous", Roots: []objectrepo.ObjectID{"day-previous"}, CreatedAt: now.Add(-25 * time.Hour)},
		{SnapshotID: "same-day-old", Roots: []objectrepo.ObjectID{"same-day-old"}, CreatedAt: now.Add(-26 * time.Hour)},
		{SnapshotID: "week-previous", Roots: []objectrepo.ObjectID{"week-previous"}, CreatedAt: now.Add(-7 * 24 * time.Hour)},
		{SnapshotID: "same-week-old", Roots: []objectrepo.ObjectID{"same-week-old"}, CreatedAt: now.Add(-8 * 24 * time.Hour)},
		{SnapshotID: "month-previous", Roots: []objectrepo.ObjectID{"month-previous"}, CreatedAt: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)},
		{SnapshotID: "same-month-old", Roots: []objectrepo.ObjectID{"same-month-old"}, CreatedAt: time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)},
		{SnapshotID: "pinned-old", Roots: []objectrepo.ObjectID{"pinned-old"}, CreatedAt: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC), Pinned: true},
		{SnapshotID: "stale", Roots: []objectrepo.ObjectID{"stale"}, CreatedAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)},
	}
	nodes := make(map[objectrepo.ObjectID]Node, len(snapshots))
	for _, snapshot := range snapshots {
		nodes[snapshot.Roots[0]] = Node{ID: snapshot.Roots[0], Size: 1}
	}
	source := &fakeInventory{value: Inventory{
		Revision:  1,
		Nodes:     nodes,
		Snapshots: snapshots,
	}}
	engine := New(source, &fakeCleaner{})
	engine.now = func() time.Time { return now }
	plan, err := engine.Preview(context.Background(), Policy{
		KeepHourlyFor:  3 * time.Hour,
		KeepDailyFor:   3 * 24 * time.Hour,
		KeepWeeklyFor:  15 * 24 * time.Hour,
		KeepMonthlyFor: 60 * 24 * time.Hour,
		TrashGrace:     30 * 24 * time.Hour,
		MinimumRecent:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRetained := []string{
		"day-previous",
		"hour-previous",
		"month-previous",
		"pinned-old",
		"recent-new",
		"recent-second",
		"week-previous",
	}
	if !equalStrings(plan.RetainedSnapshots, wantRetained) {
		t.Fatalf("retained snapshots = %#v, want %#v", plan.RetainedSnapshots, wantRetained)
	}
	wantTombstone := []objectrepo.ObjectID{
		"same-day-old",
		"same-hour-old",
		"same-month-old",
		"same-week-old",
		"stale",
	}
	if len(plan.Tombstone) != len(wantTombstone) {
		t.Fatalf("tombstone = %#v, want %#v", plan.Tombstone, wantTombstone)
	}
	for index := range wantTombstone {
		if plan.Tombstone[index] != wantTombstone[index] {
			t.Fatalf("tombstone = %#v, want %#v", plan.Tombstone, wantTombstone)
		}
	}
}

func TestApplyRejectsSameRevisionInventoryDigestDrift(t *testing.T) {
	source := &fakeInventory{value: Inventory{
		Revision: 1,
		Nodes: map[objectrepo.ObjectID]Node{
			"garbage": {ID: "garbage", Size: 10},
		},
	}}
	engine := New(source, &fakeCleaner{})
	plan, err := engine.Preview(context.Background(), DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	source.value.Nodes["garbage"] = Node{ID: "garbage", Size: 11}
	if err := engine.Apply(context.Background(), plan, DefaultPolicy()); !errors.Is(err, ErrInventoryChanged) {
		t.Fatalf("expected digest CAS rejection, got %v", err)
	}
}

func TestReachabilityPropertyNeverTombstonesProtectedClosure(t *testing.T) {
	property := func(seed uint64) bool {
		protectedCount := int(seed%31) + 1
		garbageCount := int((seed>>8)%31) + 1
		nodes := make(map[objectrepo.ObjectID]Node)
		protected := make(map[objectrepo.ObjectID]struct{}, protectedCount)
		var previous objectrepo.ObjectID
		for index := range protectedCount {
			id := objectrepo.ObjectID(fmt.Sprintf("protected-%02d", index))
			node := Node{ID: id, Size: int64(index + 1)}
			if previous != "" {
				parent := nodes[previous]
				parent.Children = append(parent.Children, id)
				nodes[previous] = parent
			}
			nodes[id] = node
			protected[id] = struct{}{}
			previous = id
		}
		for index := range garbageCount {
			id := objectrepo.ObjectID(fmt.Sprintf("garbage-%02d", index))
			nodes[id] = Node{ID: id, Size: int64(index + 1)}
		}
		source := &fakeInventory{value: Inventory{
			Revision: 1,
			Nodes:    nodes,
			Roots:    []objectrepo.ObjectID{"protected-00"},
		}}
		plan, err := New(source, &fakeCleaner{}).Preview(
			context.Background(),
			DefaultPolicy(),
		)
		if err != nil || len(plan.Tombstone) != garbageCount {
			return false
		}
		for _, id := range plan.Tombstone {
			if _, unsafe := protected[id]; unsafe {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRetentionReasonsMatchActualRuleUnion(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	inventory := Inventory{
		Snapshots: []Snapshot{
			{
				SnapshotID: "newest",
				Roots:      []objectrepo.ObjectID{"newest"},
				CreatedAt:  now.Add(-time.Hour),
			},
			{
				SnapshotID: "pinned-old",
				Roots:      []objectrepo.ObjectID{"pinned-old"},
				CreatedAt:  now.Add(-90 * 24 * time.Hour),
				Pinned:     true,
			},
		},
	}
	policy := Policy{
		KeepDailyFor:  30 * 24 * time.Hour,
		MinimumRecent: 1,
		TrashGrace:    time.Hour,
	}
	reasons := SnapshotRetentionReasons(inventory, policy, now)
	if got := reasons["newest"]; !equalStrings(
		got,
		[]string{"recent", "daily"},
	) {
		t.Fatalf("newest reasons = %#v", got)
	}
	if got := reasons["pinned-old"]; !equalStrings(
		got,
		[]string{"pinned"},
	) {
		t.Fatalf("pinned reasons = %#v", got)
	}
}

func TestSnapshotRootMembershipIsBoundToCleanupPlan(t *testing.T) {
	ctx := context.Background()
	source := &fakeInventory{value: Inventory{
		Revision: 1,
		Nodes: map[objectrepo.ObjectID]Node{
			"shared":   {ID: "shared", Size: 1},
			"database": {ID: "database", Size: 2},
			"other":    {ID: "other", Size: 3},
		},
		Snapshots: []Snapshot{{SnapshotID: "retained", Roots: []objectrepo.ObjectID{"shared", "database"},
			CreatedAt: time.Now().UTC(), Pinned: true}},
	}}
	engine := New(source, &fakeCleaner{})
	policy := DefaultPolicy()
	plan, err := engine.Preview(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	source.value.Snapshots[0].Roots = []objectrepo.ObjectID{"database", "shared"}
	if err := engine.Apply(ctx, plan, policy); err != nil {
		t.Fatalf("root ordering changed semantic inventory: %v", err)
	}
	source.value.Snapshots[0].Roots = []objectrepo.ObjectID{"shared", "other"}
	if err := engine.Apply(ctx, plan, policy); !errors.Is(err, ErrInventoryChanged) {
		t.Fatalf("changed snapshot membership did not reject stale plan: %v", err)
	}
	source.value.Snapshots[0].Roots = []objectrepo.ObjectID{"shared", "missing"}
	if _, err := engine.Preview(ctx, policy); !errors.Is(err, ErrUnsafeInventory) {
		t.Fatalf("missing secondary snapshot root was accepted: %v", err)
	}
	source.value.Snapshots[0].Roots = nil
	if _, err := engine.Preview(ctx, policy); !errors.Is(err, ErrUnsafeInventory) {
		t.Fatalf("empty snapshot closure was accepted: %v", err)
	}
}
