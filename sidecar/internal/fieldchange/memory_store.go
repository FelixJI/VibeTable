package fieldchange

import (
	"context"
	"sync"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type MemoryPlanStore struct {
	mu         sync.Mutex
	plans      map[string]v2.FieldChangePlan
	intentPlan map[string]string
}

func NewMemoryPlanStore() *MemoryPlanStore {
	return &MemoryPlanStore{
		plans: map[string]v2.FieldChangePlan{}, intentPlan: map[string]string{},
	}
}

func (store *MemoryPlanStore) FindActive(
	ctx context.Context,
	intentHash string,
	now time.Time,
) (*v2.FieldChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	planID := store.intentPlan[intentHash]
	plan, exists := store.plans[planID]
	if !exists || !planActive(plan, now) {
		return nil, nil
	}
	result := plan
	return &result, nil
}

func (store *MemoryPlanStore) Save(
	ctx context.Context,
	intentHash string,
	now time.Time,
	plan v2.FieldChangePlan,
) (v2.FieldChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return v2.FieldChangePlan{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingID := store.intentPlan[intentHash]; existingID != "" {
		if existing, ok := store.plans[existingID]; ok &&
			planActive(existing, now) {
			return existing, nil
		}
	}
	store.plans[plan.PlanID] = plan
	store.intentPlan[intentHash] = plan.PlanID
	return plan, nil
}

func (store *MemoryPlanStore) Load(
	ctx context.Context,
	planID string,
) (*v2.FieldChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.plans[planID]
	if !exists {
		return nil, nil
	}
	result := plan
	return &result, nil
}

func (store *MemoryPlanStore) MarkApplied(
	ctx context.Context,
	planID string,
	operationID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.plans[planID]
	if !exists {
		return ErrFieldNotFound
	}
	plan.CanApply = false
	store.plans[planID] = plan
	return nil
}

func planActive(plan v2.FieldChangePlan, now time.Time) bool {
	if !plan.CanApply {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	return err == nil && expires.After(now)
}
