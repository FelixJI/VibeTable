package fieldchange

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type PocketBasePlanStore struct {
	app core.App
	mu  sync.Mutex
}

func NewPocketBasePlanStore(app core.App) *PocketBasePlanStore {
	return &PocketBasePlanStore{app: app}
}

func (store *PocketBasePlanStore) FindActive(
	ctx context.Context,
	intentHash string,
	now time.Time,
) (*v2.FieldChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := store.app.FindRecordsByFilter(
		"vibetable_schema_change_plans",
		"intent_hash={:hash} && status='planned' && expires_at>{:now}",
		"-expires_at",
		1,
		0,
		dbx.Params{"hash": intentHash, "now": now.UTC()},
	)
	if err != nil {
		return nil, fmt.Errorf("find active field plan: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return decodeStoredPlan(records[0])
}

func (store *PocketBasePlanStore) Save(
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
	if existing, err := store.FindActive(ctx, intentHash, now); err != nil {
		return v2.FieldChangePlan{}, err
	} else if existing != nil {
		return *existing, nil
	}
	collection, err := store.app.FindCollectionByNameOrId(
		"vibetable_schema_change_plans",
	)
	if err != nil {
		return v2.FieldChangePlan{}, fmt.Errorf("find field plan collection: %w", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return v2.FieldChangePlan{}, fmt.Errorf("encode field plan: %w", err)
	}
	actorRaw, err := json.Marshal(plan.Intent.Actor)
	if err != nil {
		return v2.FieldChangePlan{}, fmt.Errorf("encode field plan actor: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, plan.ExpiresAt)
	if err != nil {
		return v2.FieldChangePlan{}, fmt.Errorf("parse field plan expiry: %w", err)
	}
	record := core.NewRecord(collection)
	record.Set("plan_id", plan.PlanID)
	record.Set("intent_hash", intentHash)
	record.Set("plan_hash", plan.PlanHash)
	record.Set("table_id", plan.Intent.TableID)
	record.Set("field_id", plan.Intent.FieldID)
	record.Set("action", plan.Intent.Action)
	record.Set("expected_schema_revision", plan.ExpectedSchemaRev)
	if plan.ExpectedDataRevision != nil {
		record.Set("expected_data_revision", *plan.ExpectedDataRevision)
	}
	record.Set("plan_json", types.JSONRaw(raw))
	record.Set("actor_json", types.JSONRaw(actorRaw))
	record.Set("status", "planned")
	record.Set("expires_at", expires)
	if err := store.app.Save(record); err != nil {
		return v2.FieldChangePlan{}, fmt.Errorf("save field plan: %w", err)
	}
	return plan, nil
}

func (store *PocketBasePlanStore) Load(
	ctx context.Context,
	planID string,
) (*v2.FieldChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := store.app.FindFirstRecordByFilter(
		"vibetable_schema_change_plans",
		"plan_id={:plan}",
		dbx.Params{"plan": planID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load field plan: %w", err)
	}
	return decodeStoredPlan(record)
}

func (store *PocketBasePlanStore) MarkApplied(
	ctx context.Context,
	planID string,
	operationID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := store.app.FindFirstRecordByFilter(
		"vibetable_schema_change_plans",
		"plan_id={:plan}",
		dbx.Params{"plan": planID},
	)
	if err != nil {
		return fmt.Errorf("load applied field plan: %w", err)
	}
	existing := record.GetString("applied_operation_id")
	if existing != "" && existing != operationID {
		return productError(
			"field.change.operation_conflict", "operationId",
			"plan was already applied by another operation", nil,
		)
	}
	record.Set("status", "applied")
	record.Set("applied_operation_id", operationID)
	if err := store.app.Save(record); err != nil {
		return fmt.Errorf("mark field plan applied: %w", err)
	}
	return nil
}

func decodeStoredPlan(record *core.Record) (*v2.FieldChangePlan, error) {
	raw, err := json.Marshal(record.GetRaw("plan_json"))
	if err != nil {
		return nil, fmt.Errorf("encode stored field plan: %w", err)
	}
	var plan v2.FieldChangePlan
	if err := v2.StrictDecode(raw, &plan); err != nil {
		return nil, fmt.Errorf("decode stored field plan: %w", err)
	}
	if plan.PlanID != record.GetString("plan_id") ||
		plan.PlanHash != record.GetString("plan_hash") {
		return nil, errors.New("stored field plan identity mismatch")
	}
	return &plan, nil
}
