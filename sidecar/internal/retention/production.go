package retention

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

const defaultPlanTTL = 10 * time.Minute

type MaintenanceResult struct {
	DeletedObjects  int
	ReclaimedBytes  int64
	BeforeRevision  uint64
	AfterRevision   uint64
	VerificationRun bool
}

type Maintainer interface {
	RetireAndMaintain(
		context.Context,
		objectrepo.Authority,
		uint64,
		[]objectrepo.ObjectID,
	) (MaintenanceResult, error)
}

type PlanResult struct {
	PlanID           string
	ReclaimableBytes int64
	BlockedReasons   []string
	ExpiresAt        time.Time
}

type ApplyResult struct {
	TombstonedObjects int
	DeletedObjects    int
	ReclaimedBytes    int64
}

type MutationIdentity struct {
	WorkspaceID      string
	MutationRevision uint64
	SessionEpoch     uint64
	FenceEpoch       uint64
	ClaimID          string
}

type Production struct {
	source     InventorySource
	maintainer Maintainer
	store      *Store
	authority  func() objectrepo.Authority
	now        func() time.Time
}

func NewProduction(
	source InventorySource,
	maintainer Maintainer,
	store *Store,
	authority func() objectrepo.Authority,
) *Production {
	return &Production{
		source: source, maintainer: maintainer, store: store,
		authority: authority,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (production *Production) Plan(
	ctx context.Context,
	policy Policy,
) (PlanResult, error) {
	return production.plan(ctx, policy, false)
}

// PlanWithReceipt persists the exact protocol result atomically with the
// newly-created cleanup plan. It must be called from a Dispatcher handler
// context.
func (production *Production) PlanWithReceipt(
	ctx context.Context,
	policy Policy,
) (PlanResult, error) {
	return production.plan(ctx, policy, true)
}

func (production *Production) plan(
	ctx context.Context,
	policy Policy,
	withReceipt bool,
) (PlanResult, error) {
	if err := production.validate(); err != nil {
		return PlanResult{}, err
	}
	if err := production.store.EnsureIntegrityHealthy(ctx); err != nil {
		return PlanResult{}, err
	}
	source := &tombstoneInventorySource{
		source: production.source,
		store:  production.store,
	}
	engine := New(source, noopCleaner{})
	engine.now = production.now
	plan, err := engine.Preview(ctx, policy)
	if err != nil {
		return PlanResult{}, err
	}
	planID := uuid.NewString()
	expiresAt := production.now().UTC().Add(defaultPlanTTL)
	result := PlanResult{
		PlanID:           planID,
		ReclaimableBytes: plan.ReclaimableBytes,
		BlockedReasons:   []string{},
		ExpiresAt:        expiresAt,
	}
	var receipts []protocolv2.OperationReceipt
	if withReceipt {
		operation, ok := protocolv2.OperationFromContext(ctx)
		if !ok ||
			operation.Method != "retention.plan" ||
			operation.Scope != protocolv2.WorkspaceScope ||
			operation.WorkspaceID == "" ||
			operation.Session.Epoch == 0 {
			return PlanResult{},
				errors.New("retention.operation_context_invalid")
		}
		receipt, err := protocolv2.BuildContextOperationReceipt(
			ctx,
			planWireResult(result),
		)
		if err != nil {
			return PlanResult{}, err
		}
		receipts = append(receipts, receipt)
	}
	if err := production.store.putPlan(
		ctx,
		planID,
		plan,
		policy,
		expiresAt,
		receipts...,
	); err != nil {
		return PlanResult{}, err
	}
	return result, nil
}

func (production *Production) Apply(
	ctx context.Context,
	planID string,
	mutations ...MutationIdentity,
) (ApplyResult, error) {
	return production.apply(ctx, planID, false, mutations...)
}

// ApplyWithReceipt commits the coordinator proof, logical tombstones,
// snapshot tombstones, and exact protocol result in one SQLite transaction.
func (production *Production) ApplyWithReceipt(
	ctx context.Context,
	planID string,
	mutation MutationIdentity,
) (ApplyResult, error) {
	return production.apply(ctx, planID, true, mutation)
}

func (production *Production) apply(
	ctx context.Context,
	planID string,
	withReceipt bool,
	mutations ...MutationIdentity,
) (ApplyResult, error) {
	if err := production.validate(); err != nil {
		return ApplyResult{}, err
	}
	if err := production.store.EnsureIntegrityHealthy(ctx); err != nil {
		return ApplyResult{}, err
	}
	var mutation *MutationIdentity
	if len(mutations) > 1 {
		return ApplyResult{}, errors.New("retention.mutation_identity_invalid")
	}
	if len(mutations) == 1 {
		mutation = &mutations[0]
		if err := validateMutationIdentity(*mutation); err != nil {
			return ApplyResult{}, err
		}
	}
	if _, err := uuid.Parse(planID); err != nil {
		return ApplyResult{}, errors.New("retention.plan_id_invalid")
	}
	stored, err := production.store.plan(ctx, planID)
	if err != nil {
		return ApplyResult{}, err
	}
	now := production.now().UTC()
	if stored.AppliedAt != nil {
		return ApplyResult{}, errors.New("retention.plan_already_applied")
	}
	if !now.Before(stored.ExpiresAt) {
		return ApplyResult{}, errors.New("retention.plan_expired")
	}
	source := &tombstoneInventorySource{
		source: production.source,
		store:  production.store,
	}
	current, err := source.Inventory(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{
		TombstonedObjects: len(stored.Plan.Tombstone),
		DeletedObjects:    len(stored.Plan.Tombstone),
		ReclaimedBytes:    0,
	}
	var receipt *protocolv2.OperationReceipt
	if withReceipt {
		operation, ok := protocolv2.OperationFromContext(ctx)
		if !ok ||
			operation.Method != "retention.apply" ||
			operation.Scope != protocolv2.WorkspaceScope ||
			mutation == nil ||
			operation.WorkspaceID != mutation.WorkspaceID ||
			operation.Session.Epoch != mutation.SessionEpoch {
			return ApplyResult{},
				errors.New("retention.operation_context_invalid")
		}
		value, receiptErr := protocolv2.BuildContextOperationReceipt(
			ctx,
			applyWireResult(result),
		)
		if receiptErr != nil {
			return ApplyResult{}, receiptErr
		}
		receipt = &value
	}
	cleaner := &durableTombstoneCleaner{
		store: production.store, planID: planID,
		inventory: current, now: now, mutation: mutation,
		receipt: receipt,
	}
	engine := New(&fixedInventorySource{inventory: current}, cleaner)
	engine.now = production.now
	if err := engine.Apply(ctx, stored.Plan, stored.Policy); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

func (production *Production) Sweep(
	ctx context.Context,
	policy Policy,
) (MaintenanceResult, error) {
	if err := production.validate(); err != nil {
		return MaintenanceResult{}, err
	}
	if err := production.store.EnsureIntegrityHealthy(ctx); err != nil {
		return MaintenanceResult{}, err
	}
	if err := validatePolicy(policy); err != nil {
		return MaintenanceResult{}, err
	}
	source := &tombstoneInventorySource{
		source: production.source,
		store:  production.store,
	}
	inventory, err := source.Inventory(ctx)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if err := validateInventory(inventory); err != nil {
		return MaintenanceResult{}, err
	}
	roots, _ := retentionRoots(inventory, policy, production.now().UTC())
	reachable := reachableSet(inventory.Nodes, roots)
	tombstones, err := production.store.tombstones(ctx, false)
	if err != nil {
		return MaintenanceResult{}, err
	}
	now := production.now().UTC()
	var due []objectrepo.ObjectID
	for _, item := range tombstones {
		if now.Before(item.GraceUntil) {
			continue
		}
		if _, ok := inventory.Nodes[item.ObjectID]; !ok {
			return MaintenanceResult{}, ErrInventoryChanged
		}
		if _, protected := reachable[item.ObjectID]; protected {
			return MaintenanceResult{}, ErrInventoryChanged
		}
		due = append(due, item.ObjectID)
	}
	sortIDs(due)
	if len(due) == 0 {
		return MaintenanceResult{
			BeforeRevision: inventory.Revision,
			AfterRevision:  inventory.Revision,
		}, nil
	}
	authority := production.authority()
	result, err := production.maintainer.RetireAndMaintain(
		ctx,
		authority,
		inventory.Revision,
		due,
	)
	if err != nil {
		return MaintenanceResult{}, err
	}
	if result.DeletedObjects != len(due) ||
		result.BeforeRevision != inventory.Revision ||
		result.AfterRevision <= result.BeforeRevision ||
		!result.VerificationRun ||
		result.ReclaimedBytes < 0 {
		return MaintenanceResult{},
			errors.New("retention.maintenance_receipt_invalid")
	}
	if err := production.store.markMaintained(ctx, due, now); err != nil {
		return MaintenanceResult{}, err
	}
	return result, nil
}

func (production *Production) validate() error {
	if production == nil ||
		production.source == nil ||
		production.maintainer == nil ||
		production.store == nil ||
		production.authority == nil ||
		production.now == nil {
		return errors.New("retention.production_dependencies_required")
	}
	authority := production.authority()
	if authority.WorkspaceID == "" ||
		authority.FenceEpoch == 0 ||
		authority.ClaimID == "" {
		return errors.New("retention.authority_invalid")
	}
	return nil
}

type fixedInventorySource struct {
	inventory Inventory
}

func (source *fixedInventorySource) Inventory(
	context.Context,
) (Inventory, error) {
	return source.inventory, nil
}

type noopCleaner struct{}

func (noopCleaner) Tombstone(
	context.Context,
	[]objectrepo.ObjectID,
	time.Duration,
) error {
	return nil
}

type durableTombstoneCleaner struct {
	store     *Store
	planID    string
	inventory Inventory
	now       time.Time
	mutation  *MutationIdentity
	receipt   *protocolv2.OperationReceipt
}

func (cleaner *durableTombstoneCleaner) Tombstone(
	ctx context.Context,
	ids []objectrepo.ObjectID,
	grace time.Duration,
) error {
	if grace <= 0 {
		return ErrPolicyInvalid
	}
	plan := CleanupPlan{
		Tombstone: append([]objectrepo.ObjectID(nil), ids...),
		Grace:     grace,
	}
	return cleaner.store.applyPlan(
		ctx,
		cleaner.planID,
		plan,
		cleaner.inventory,
		cleaner.now,
		cleaner.mutation,
		cleaner.receipt,
	)
}

func planWireResult(result PlanResult) map[string]any {
	return map[string]any{
		"planId":           result.PlanID,
		"reclaimableBytes": result.ReclaimableBytes,
		"blockedReasons":   result.BlockedReasons,
	}
}

func applyWireResult(result ApplyResult) map[string]any {
	return map[string]any{
		"deletedObjects": result.DeletedObjects,
		"reclaimedBytes": result.ReclaimedBytes,
	}
}

func validateMutationIdentity(identity MutationIdentity) error {
	if identity.WorkspaceID == "" ||
		identity.MutationRevision == 0 ||
		identity.SessionEpoch == 0 ||
		identity.FenceEpoch == 0 ||
		identity.ClaimID == "" {
		return errors.New("retention.mutation_identity_invalid")
	}
	return nil
}

type tombstoneInventorySource struct {
	source InventorySource
	store  *Store
}

func (source *tombstoneInventorySource) Inventory(
	ctx context.Context,
) (Inventory, error) {
	inventory, err := source.source.Inventory(ctx)
	if err != nil {
		return Inventory{}, err
	}
	tombstones, err := source.store.tombstones(ctx, false)
	if err != nil {
		return Inventory{}, err
	}
	for _, item := range tombstones {
		node, ok := inventory.Nodes[item.ObjectID]
		if !ok {
			return Inventory{}, ErrUnsafeInventory
		}
		timestamp := item.TombstonedAt
		node.TombstonedAt = &timestamp
		inventory.Nodes[item.ObjectID] = node
	}
	return inventory, nil
}
