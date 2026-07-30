package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

var (
	ErrInventoryChanged = errors.New("retention.inventory_changed")
	ErrUnsafeInventory  = errors.New("retention.inventory_unsafe")
	ErrPolicyInvalid    = errors.New("retention.policy_invalid")
)

type Policy struct {
	KeepHourlyFor  time.Duration `json:"keepHourlyFor"`
	KeepDailyFor   time.Duration `json:"keepDailyFor"`
	KeepWeeklyFor  time.Duration `json:"keepWeeklyFor"`
	KeepMonthlyFor time.Duration `json:"keepMonthlyFor"`
	TrashGrace     time.Duration `json:"trashGrace"`
	MinimumRecent  int           `json:"minimumRecent"`
}

func DefaultPolicy() Policy {
	const snapshotWindow = 30 * 24 * time.Hour
	return Policy{
		KeepHourlyFor:  snapshotWindow,
		KeepDailyFor:   snapshotWindow,
		KeepWeeklyFor:  snapshotWindow,
		KeepMonthlyFor: snapshotWindow,
		TrashGrace:     90 * 24 * time.Hour,
		MinimumRecent:  50,
	}
}

type Node struct {
	ID           objectrepo.ObjectID
	Children     []objectrepo.ObjectID
	Size         int64
	CreatedAt    time.Time
	TombstonedAt *time.Time
}

type Snapshot struct {
	SnapshotID string
	Root       objectrepo.ObjectID
	CreatedAt  time.Time
	Pinned     bool
}

type Inventory struct {
	Revision           uint64
	Nodes              map[objectrepo.ObjectID]Node
	Roots              []objectrepo.ObjectID
	PinnedRoots        []objectrepo.ObjectID
	Snapshots          []Snapshot
	PendingPublication bool
	UnknownManifest    bool
	CorruptIndex       bool
}

type CleanupPlan struct {
	InventoryRevision uint64                `json:"inventoryRevision"`
	InventoryDigest   string                `json:"inventoryDigest"`
	Reachable         []objectrepo.ObjectID `json:"reachable"`
	Tombstone         []objectrepo.ObjectID `json:"tombstone"`
	RetainedSnapshots []string              `json:"retainedSnapshots"`
	ReclaimableBytes  int64                 `json:"reclaimableBytes"`
	Grace             time.Duration         `json:"grace"`
	CreatedAt         time.Time             `json:"createdAt"`
}

type InventorySource interface {
	Inventory(context.Context) (Inventory, error)
}

type Cleaner interface {
	// Tombstone performs only the logical transition. Physical repository
	// maintenance must wait until grace has elapsed and revalidate inventory.
	Tombstone(context.Context, []objectrepo.ObjectID, time.Duration) error
}

type Engine struct {
	source  InventorySource
	cleaner Cleaner
	now     func() time.Time
}

func New(source InventorySource, cleaner Cleaner) *Engine {
	return &Engine{
		source: source, cleaner: cleaner,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (engine *Engine) Preview(
	ctx context.Context,
	policy Policy,
) (CleanupPlan, error) {
	if engine.source == nil || engine.cleaner == nil {
		return CleanupPlan{}, errors.New("retention.dependencies_required")
	}
	if err := validatePolicy(policy); err != nil {
		return CleanupPlan{}, err
	}
	inventory, err := engine.source.Inventory(ctx)
	if err != nil {
		return CleanupPlan{}, err
	}
	if err := validateInventory(inventory); err != nil {
		return CleanupPlan{}, err
	}
	now := engine.now().UTC()
	roots, retained := retentionRoots(inventory, policy, now)
	reachable := reachableSet(inventory.Nodes, roots)
	digest, err := inventoryDigest(inventory)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan := CleanupPlan{
		InventoryRevision: inventory.Revision,
		InventoryDigest:   digest,
		RetainedSnapshots: retained,
		Grace:             policy.TrashGrace,
		CreatedAt:         now,
	}
	for id, node := range inventory.Nodes {
		if _, ok := reachable[id]; ok {
			plan.Reachable = append(plan.Reachable, id)
			continue
		}
		if node.TombstonedAt != nil {
			continue
		}
		plan.Tombstone = append(plan.Tombstone, id)
		plan.ReclaimableBytes += node.Size
	}
	sortIDs(plan.Reachable)
	sortIDs(plan.Tombstone)
	return plan, nil
}

func (engine *Engine) Apply(
	ctx context.Context,
	plan CleanupPlan,
	policy Policy,
) error {
	if engine.source == nil || engine.cleaner == nil {
		return errors.New("retention.dependencies_required")
	}
	if err := validatePolicy(policy); err != nil {
		return err
	}
	if plan.Grace != policy.TrashGrace {
		return ErrInventoryChanged
	}
	current, err := engine.source.Inventory(ctx)
	if err != nil {
		return err
	}
	if err := validateInventory(current); err != nil {
		return err
	}
	digest, err := inventoryDigest(current)
	if err != nil {
		return err
	}
	if current.Revision != plan.InventoryRevision ||
		digest != plan.InventoryDigest {
		return ErrInventoryChanged
	}
	roots, retained := retentionRoots(current, policy, engine.now().UTC())
	if !equalStrings(retained, plan.RetainedSnapshots) {
		return ErrInventoryChanged
	}
	freshReachable := reachableSet(current.Nodes, roots)
	for _, id := range plan.Tombstone {
		node, exists := current.Nodes[id]
		if !exists || node.TombstonedAt != nil {
			return ErrInventoryChanged
		}
		if _, protected := freshReachable[id]; protected {
			return ErrInventoryChanged
		}
	}
	return engine.cleaner.Tombstone(
		ctx,
		append([]objectrepo.ObjectID(nil), plan.Tombstone...),
		policy.TrashGrace,
	)
}

func validatePolicy(policy Policy) error {
	if policy.KeepHourlyFor < 0 ||
		policy.KeepDailyFor < 0 ||
		policy.KeepWeeklyFor < 0 ||
		policy.KeepMonthlyFor < 0 ||
		policy.TrashGrace <= 0 ||
		policy.MinimumRecent < 0 {
		return ErrPolicyInvalid
	}
	return nil
}

func validateInventory(inventory Inventory) error {
	if inventory.PendingPublication ||
		inventory.UnknownManifest ||
		inventory.CorruptIndex ||
		inventory.Nodes == nil {
		return ErrUnsafeInventory
	}
	for id, node := range inventory.Nodes {
		if id == "" || node.ID != id || node.Size < 0 {
			return ErrUnsafeInventory
		}
		for _, child := range node.Children {
			if _, exists := inventory.Nodes[child]; !exists {
				return fmt.Errorf(
					"%w: missing child %s from %s",
					ErrUnsafeInventory, child, id,
				)
			}
		}
	}
	allRoots := append([]objectrepo.ObjectID(nil), inventory.Roots...)
	allRoots = append(allRoots, inventory.PinnedRoots...)
	for _, snapshot := range inventory.Snapshots {
		if snapshot.SnapshotID == "" ||
			snapshot.Root == "" ||
			snapshot.CreatedAt.IsZero() {
			return ErrUnsafeInventory
		}
		allRoots = append(allRoots, snapshot.Root)
	}
	for _, root := range allRoots {
		if _, exists := inventory.Nodes[root]; !exists {
			return fmt.Errorf(
				"%w: missing root %s", ErrUnsafeInventory, root,
			)
		}
	}
	return nil
}

func retentionRoots(
	inventory Inventory,
	policy Policy,
	now time.Time,
) ([]objectrepo.ObjectID, []string) {
	rootSet := make(map[objectrepo.ObjectID]struct{})
	for _, root := range inventory.Roots {
		rootSet[root] = struct{}{}
	}
	for _, root := range inventory.PinnedRoots {
		rootSet[root] = struct{}{}
	}
	selected, _ := retentionSnapshotSelections(
		inventory.Snapshots,
		policy,
		now,
	)
	retained := make([]string, 0, len(selected))
	for id, snapshot := range selected {
		retained = append(retained, id)
		rootSet[snapshot.Root] = struct{}{}
	}
	sort.Strings(retained)
	roots := make([]objectrepo.ObjectID, 0, len(rootSet))
	for root := range rootSet {
		roots = append(roots, root)
	}
	sortIDs(roots)
	return roots, retained
}

func SnapshotRetentionReasons(
	inventory Inventory,
	policy Policy,
	now time.Time,
) map[string][]string {
	_, reasonSets := retentionSnapshotSelections(
		inventory.Snapshots,
		policy,
		now,
	)
	order := []string{
		"pinned", "recent", "hourly", "daily", "weekly", "monthly",
	}
	result := make(map[string][]string, len(reasonSets))
	for snapshotID, reasons := range reasonSets {
		for _, reason := range order {
			if _, exists := reasons[reason]; exists {
				result[snapshotID] = append(
					result[snapshotID],
					reason,
				)
			}
		}
	}
	return result
}

func retentionSnapshotSelections(
	values []Snapshot,
	policy Policy,
	now time.Time,
) (map[string]Snapshot, map[string]map[string]struct{}) {
	snapshots := append([]Snapshot(nil), values...)
	sort.Slice(snapshots, func(left, right int) bool {
		if snapshots[left].CreatedAt.Equal(snapshots[right].CreatedAt) {
			return snapshots[left].SnapshotID < snapshots[right].SnapshotID
		}
		return snapshots[left].CreatedAt.After(snapshots[right].CreatedAt)
	})
	selected := map[string]Snapshot{}
	reasons := map[string]map[string]struct{}{}
	add := func(snapshot Snapshot, reason string) {
		selected[snapshot.SnapshotID] = snapshot
		if reasons[snapshot.SnapshotID] == nil {
			reasons[snapshot.SnapshotID] = map[string]struct{}{}
		}
		reasons[snapshot.SnapshotID][reason] = struct{}{}
	}
	for _, snapshot := range snapshots {
		if snapshot.Pinned {
			add(snapshot, "pinned")
		}
	}
	for index := 0; index < len(snapshots) && index < policy.MinimumRecent; index++ {
		add(snapshots[index], "recent")
	}
	selectBuckets(add, snapshots, now, policy.KeepHourlyFor, hourlyBucket, "hourly")
	selectBuckets(add, snapshots, now, policy.KeepDailyFor, dailyBucket, "daily")
	selectBuckets(add, snapshots, now, policy.KeepWeeklyFor, weeklyBucket, "weekly")
	selectBuckets(add, snapshots, now, policy.KeepMonthlyFor, monthlyBucket, "monthly")
	return selected, reasons
}

func selectBuckets(
	add func(Snapshot, string),
	snapshots []Snapshot,
	now time.Time,
	window time.Duration,
	bucket func(time.Time) string,
	reason string,
) {
	if window == 0 {
		return
	}
	seen := map[string]struct{}{}
	for _, snapshot := range snapshots {
		age := now.Sub(snapshot.CreatedAt)
		if age < 0 || age > window {
			continue
		}
		key := bucket(snapshot.CreatedAt.UTC())
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		add(snapshot, reason)
	}
}

func hourlyBucket(value time.Time) string {
	return value.Format("2006-01-02T15")
}

func dailyBucket(value time.Time) string {
	return value.Format("2006-01-02")
}

func weeklyBucket(value time.Time) string {
	year, week := value.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

func monthlyBucket(value time.Time) string {
	return value.Format("2006-01")
}

func reachableSet(
	nodes map[objectrepo.ObjectID]Node,
	roots []objectrepo.ObjectID,
) map[objectrepo.ObjectID]struct{} {
	result := map[objectrepo.ObjectID]struct{}{}
	stack := append([]objectrepo.ObjectID(nil), roots...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, seen := result[id]; seen {
			continue
		}
		node := nodes[id]
		result[id] = struct{}{}
		stack = append(stack, node.Children...)
	}
	return result
}

func inventoryDigest(inventory Inventory) (string, error) {
	type canonicalNode struct {
		ID           objectrepo.ObjectID   `json:"id"`
		Children     []objectrepo.ObjectID `json:"children"`
		Size         int64                 `json:"size"`
		CreatedAt    time.Time             `json:"createdAt"`
		TombstonedAt *time.Time            `json:"tombstonedAt,omitempty"`
	}
	nodes := make([]canonicalNode, 0, len(inventory.Nodes))
	for _, node := range inventory.Nodes {
		children := append([]objectrepo.ObjectID(nil), node.Children...)
		sortIDs(children)
		nodes = append(nodes, canonicalNode{
			ID: node.ID, Children: children, Size: node.Size,
			CreatedAt: node.CreatedAt, TombstonedAt: node.TombstonedAt,
		})
	}
	sort.Slice(nodes, func(left, right int) bool {
		return nodes[left].ID < nodes[right].ID
	})
	roots := append([]objectrepo.ObjectID(nil), inventory.Roots...)
	pinnedRoots := append([]objectrepo.ObjectID(nil), inventory.PinnedRoots...)
	sortIDs(roots)
	sortIDs(pinnedRoots)
	snapshots := append([]Snapshot(nil), inventory.Snapshots...)
	sort.Slice(snapshots, func(left, right int) bool {
		return snapshots[left].SnapshotID < snapshots[right].SnapshotID
	})
	raw, err := json.Marshal(struct {
		Revision    uint64                `json:"revision"`
		Nodes       []canonicalNode       `json:"nodes"`
		Roots       []objectrepo.ObjectID `json:"roots"`
		PinnedRoots []objectrepo.ObjectID `json:"pinnedRoots"`
		Snapshots   []Snapshot            `json:"snapshots"`
	}{
		Revision: inventory.Revision, Nodes: nodes, Roots: roots,
		PinnedRoots: pinnedRoots, Snapshots: snapshots,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortIDs(ids []objectrepo.ObjectID) {
	sort.Slice(ids, func(left, right int) bool {
		return ids[left] < ids[right]
	})
}
