package fieldchange

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// commitProjectionScheduler records enqueue and start calls so tests can prove
// that migration work is enqueued inside the business transaction but only
// started after the commit succeeds.
type commitProjectionScheduler struct {
	enqueued []string
	started  []string
}

func (scheduler *commitProjectionScheduler) Enqueue(
	_ context.Context,
	_ core.App,
	_ v2.FieldChangePlan,
	operationID string,
) (string, error) {
	scheduler.enqueued = append(scheduler.enqueued, operationID)
	return "job-" + operationID, nil
}

func (scheduler *commitProjectionScheduler) Start(jobID string) bool {
	scheduler.started = append(scheduler.started, jobID)
	return true
}

// writeFieldProjectionMarker persists a projection through the app handed to
// the commit callback; a same-transaction app commits or rolls back the marker
// together with the field change.
func writeFieldProjectionMarker(app core.App, operationID string) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_idempotency_keys")
	if err != nil {
		return fmt.Errorf("load projection marker collection: %w", err)
	}
	record := core.NewRecord(collection)
	record.Set("key", "commit-projection:"+operationID)
	record.Set("request_hash", "commit-projection")
	record.Set("status", "projected")
	record.Set("receipt_json", types.JSONRaw([]byte(`{}`)))
	record.Set("expires_at", time.Now().Add(time.Hour))
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save projection marker: %w", err)
	}
	return nil
}

func assertCommitProjectionAbsent(t *testing.T, app core.App, operationID string) {
	t.Helper()
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": "field-v2:" + operationID},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("replay receipt survived the projection rollback: %v", findErr)
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": "commit-projection:" + operationID},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("projection marker committed despite the rollback: %v", findErr)
	}
}

func assertPlanStillPlanned(t *testing.T, app core.App, planID string) {
	t.Helper()
	record, err := app.FindFirstRecordByFilter(
		"vibetable_schema_change_plans", "plan_id={:plan}",
		dbx.Params{"plan": planID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.GetString("status") != "planned" ||
		record.GetString("applied_operation_id") != "" {
		t.Fatalf("plan was marked applied by a rolled back apply: %#v", record)
	}
}

func TestExecutorApplyWithCommitProjectsMigrationEnqueueAtomically(t *testing.T) {
	app, plan, _ := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	ctx := context.Background()
	if _, err := store.Save(ctx, "intent-commit-projection-migration", time.Now(), plan); err != nil {
		t.Fatal(err)
	}
	scheduler := &commitProjectionScheduler{}
	executor := NewExecutor(app, store, WithMigrationScheduler(scheduler))
	request := v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-migration-projection-rollback",
		Actor:       plan.Intent.Actor,
	}
	projectionErr := errors.New("migration projection failed")
	calls := 0
	_, err := executor.ApplyWithCommit(ctx, request, func(tx core.App, receipt v2.ApplyReceipt) error {
		calls++
		if receipt.MigrationJobID == "" {
			return errors.New("projection did not receive the enqueued migration job")
		}
		if len(scheduler.started) != 0 {
			return errors.New("migration work started before the business commit")
		}
		if err := writeFieldProjectionMarker(tx, receipt.OperationID); err != nil {
			return err
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("ApplyWithCommit() error = %v, want %v", err, projectionErr)
	}
	// The projection failure rolled the whole apply back together with its
	// idempotent receipt and plan marker, and never started async work.
	assertCommitProjectionAbsent(t, app, request.OperationID)
	assertPlanStillPlanned(t, app, plan.PlanID)
	if len(scheduler.enqueued) == 0 {
		t.Fatal("migration enqueue did not run in the business transaction")
	}
	if len(scheduler.started) != 0 {
		t.Fatal("migration work started for a rolled back apply")
	}
	// The existing failure audit semantics are preserved, not swallowed.
	if failed, findErr := app.FindFirstRecordByFilter(
		"vibetable_schema_audit", "operation_id={:operation} && outcome='failed'",
		dbx.Params{"operation": request.OperationID},
	); findErr != nil || failed == nil {
		t.Fatalf("failed schema audit was not preserved: %v", findErr)
	}
	// A fresh operation commits the schema queue and the projection together.
	committed, err := executor.ApplyWithCommit(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-migration-projection-commit", Actor: plan.Intent.Actor,
	}, func(tx core.App, receipt v2.ApplyReceipt) error {
		calls++
		return writeFieldProjectionMarker(tx, receipt.OperationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("projection calls = %d, want the failed attempt plus one committed run", calls)
	}
	if committed.MigrationJobID == "" || len(scheduler.started) != 1 ||
		scheduler.started[0] != committed.MigrationJobID {
		t.Fatalf("committed migration job = %q, started = %v", committed.MigrationJobID, scheduler.started)
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": "commit-projection:" + committed.OperationID},
	); findErr != nil {
		t.Fatalf("projection marker missing after the commit: %v", findErr)
	}
	// Repeating the committed operation must replay without re-projecting or
	// re-enqueueing migration work. Re-running Start on the replayed receipt
	// is the scheduler's existing post-commit behavior and stays unchanged.
	if _, err := executor.ApplyWithCommit(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-migration-projection-commit", Actor: plan.Intent.Actor,
	}, func(core.App, v2.ApplyReceipt) error {
		return errors.New("replayed apply must not re-project")
	}); err != nil {
		t.Fatalf("replayed ApplyWithCommit() failed: %v", err)
	}
	if calls != 2 || len(scheduler.enqueued) != 2 {
		t.Fatalf("replay re-projected or re-enqueued work: calls = %d, enqueued = %v", calls, scheduler.enqueued)
	}
}

func TestExecutorApplyWithCommitProjectsSyncSchemaChangeAtomically(t *testing.T) {
	app, plan, _ := migrationFixture(t)
	store := NewPocketBasePlanStore(app)
	ctx := context.Background()
	// Turn the fixture conversion into a synchronous retire of the same field.
	retired := *plan.Before
	retiredAt := "2026-10-05T12:00:00Z"
	retired.Lifecycle.State = v2.LifecycleRetired
	retired.Lifecycle.RetiredAt = &retiredAt
	plan.PlanID = "plan_commit_projection_sync"
	plan.Intent.Action = v2.ActionRetire
	plan.Classes = []v2.ChangeClass{v2.ClassSchema}
	plan.CreatesMigration = false
	plan.After = &retired
	plan.PlanHash, _ = hashPlan(plan)
	if _, err := store.Save(ctx, "intent-commit-projection-sync", time.Now(), plan); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(app, store)
	request := v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-sync-projection-rollback",
		Actor:       plan.Intent.Actor,
	}
	projectionErr := errors.New("sync projection failed")
	_, err := executor.ApplyWithCommit(ctx, request, func(tx core.App, receipt v2.ApplyReceipt) error {
		// The uncommitted schema change is only visible on the transaction.
		if receipt.SchemaRevision != v2.FormatSchemaRevision(2) {
			return fmt.Errorf("projection received unexpected revision %q", receipt.SchemaRevision)
		}
		field, findErr := tx.FindFirstRecordByFilter(
			"vibetable_fields", "table_id={:table} && field_id={:field} && lifecycle_state='retired'",
			dbx.Params{"table": plan.Intent.TableID, "field": retired.Identity.FieldID},
		)
		if findErr != nil {
			return fmt.Errorf("projection cannot observe the uncommitted retire: %w", findErr)
		}
		if field == nil {
			return errors.New("projection observed no retired field")
		}
		if err := writeFieldProjectionMarker(tx, receipt.OperationID); err != nil {
			return err
		}
		return projectionErr
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("ApplyWithCommit() error = %v, want %v", err, projectionErr)
	}
	// The projection failure rolled back the schema change, replay receipt,
	// plan marker and projection write together.
	table, findErr := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id='tbl_orders'",
	)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if table.GetInt("schema_revision") != 1 {
		t.Fatalf("schema revision = %d, want the rolled back value 1", table.GetInt("schema_revision"))
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_fields", "table_id={:table} && field_id={:field} && lifecycle_state='retired'",
		dbx.Params{"table": plan.Intent.TableID, "field": retired.Identity.FieldID},
	); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("retired field metadata survived the rollback: %v", findErr)
	}
	assertCommitProjectionAbsent(t, app, request.OperationID)
	assertPlanStillPlanned(t, app, plan.PlanID)
	if failed, findErr := app.FindFirstRecordByFilter(
		"vibetable_schema_audit", "operation_id={:operation} && outcome='failed'",
		dbx.Params{"operation": request.OperationID},
	); findErr != nil || failed == nil {
		t.Fatalf("failed schema audit was not preserved: %v", findErr)
	}
	// A fresh operation commits the schema change and the projection together.
	committed, err := executor.ApplyWithCommit(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-sync-projection-commit", Actor: plan.Intent.Actor,
	}, func(tx core.App, receipt v2.ApplyReceipt) error {
		return writeFieldProjectionMarker(tx, receipt.OperationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.SchemaRevision != v2.FormatSchemaRevision(2) {
		t.Fatalf("committed receipt revision = %q", committed.SchemaRevision)
	}
	if _, findErr := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys", "key={:key}",
		dbx.Params{"key": "commit-projection:" + committed.OperationID},
	); findErr != nil {
		t.Fatalf("projection marker missing after the commit: %v", findErr)
	}
	// Repeating the committed operation must replay without re-projecting.
	replayed, err := executor.ApplyWithCommit(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-sync-projection-commit", Actor: plan.Intent.Actor,
	}, func(core.App, v2.ApplyReceipt) error {
		return errors.New("replayed apply must not re-project")
	})
	if err != nil {
		t.Fatalf("replayed ApplyWithCommit() failed: %v", err)
	}
	if replayed.OperationID != committed.OperationID ||
		replayed.SchemaRevision != committed.SchemaRevision {
		t.Fatalf("replayed receipt %#v differs from committed receipt %#v", replayed, committed)
	}
}
