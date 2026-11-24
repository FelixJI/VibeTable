package fieldchange

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func schemaAuditRows(t *testing.T, app core.App, filter string, params dbx.Params) []*core.Record {
	t.Helper()
	rows, err := app.FindRecordsByFilter("vibetable_schema_audit", filter, "+id", 0, 0, params)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Record ids are random, so rows are compared as sorted id lists.
func auditIDs(rows []*core.Record) string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func syncRetirePlan(t *testing.T, app core.App, plan v2.FieldChangePlan, planID string) v2.FieldChangePlan {
	t.Helper()
	retired := *plan.Before
	retiredAt := "2026-10-05T12:00:00Z"
	retired.Lifecycle.State = v2.LifecycleRetired
	retired.Lifecycle.RetiredAt = &retiredAt
	plan.PlanID = planID
	plan.Intent.Action = v2.ActionRetire
	plan.Classes = []v2.ChangeClass{v2.ClassSchema}
	plan.CreatesMigration = false
	plan.After = &retired
	plan.PlanHash, _ = hashPlan(plan)
	if _, err := NewPocketBasePlanStore(app).Save(context.Background(), "intent-"+planID, time.Now(), plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

// The schema audit is append-only under a unique operation_id. A failure
// receipt keeps its number exactly as written, a repeated application under
// that number is rejected by the unique constraint, and nothing is rewritten,
// retired or added on the rejected path. Callers that need a genuine new
// attempt supply a distinct operation id; the executor does not derive one.
func TestExecutorFailureReceiptKeepsOperationIDAndBlocksReuse(t *testing.T) {
	app, base, _ := migrationFixture(t)
	plan := syncRetirePlan(t, app, base, "plan_failed_attempt_receipt")
	executor := NewExecutor(app, NewPocketBasePlanStore(app))
	ctx := context.Background()
	request := v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "operation-failed-receipt", Actor: plan.Intent.Actor,
	}
	projectionErr := errors.New("projection failed once")
	_, err := executor.ApplyWithCommit(ctx, request, func(core.App, v2.ApplyReceipt) error {
		return projectionErr
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("first ApplyWithCommit() error = %v, want %v", err, projectionErr)
	}
	failed := schemaAuditRows(t, app, "operation_id={:operation}", dbx.Params{"operation": request.OperationID})
	if len(failed) != 1 || failed[0].GetString("outcome") != "failed" ||
		failed[0].GetString("error_code") != "field.change.apply_failed" ||
		failed[0].GetString("plan_id") != plan.PlanID || failed[0].GetString("action") != string(v2.ActionRetire) ||
		failed[0].GetString("table_id") != plan.Intent.TableID {
		t.Fatalf("failure receipt missing or incomplete: %+v", failed)
	}
	assertCommitProjectionAbsent(t, app, request.OperationID)
	assertPlanStillPlanned(t, app, plan.PlanID)

	_, err = executor.ApplyWithCommit(ctx, request, func(tx core.App, receipt v2.ApplyReceipt) error {
		return writeFieldProjectionMarker(tx, receipt.OperationID)
	})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("repeated application under the failure receipt's operation id returned %v", err)
	}
	after := schemaAuditRows(t, app, "operation_id={:operation}", dbx.Params{"operation": request.OperationID})
	if len(after) != 1 || after[0].Id != failed[0].Id || after[0].GetString("operation_id") != request.OperationID ||
		after[0].GetString("outcome") != "failed" || after[0].GetString("error_code") != "field.change.apply_failed" ||
		after[0].GetString("plan_id") != plan.PlanID ||
		!after[0].GetDateTime("occurred_at").Time().Equal(failed[0].GetDateTime("occurred_at").Time()) {
		t.Fatalf("rejected retry changed the failure receipt: %+v", after)
	}
	if rows := schemaAuditRows(t, app, "plan_id={:plan}", dbx.Params{"plan": plan.PlanID}); len(rows) != 1 {
		t.Fatalf("rejected retry changed the plan history: %+v", rows)
	}
	assertCommitProjectionAbsent(t, app, request.OperationID)

	// A genuine new attempt is a new operation id: it is applied once and the
	// failure receipt stays next to it under its original number.
	retry := request
	retry.OperationID = request.OperationID + "#" + plan.PlanID
	committed, err := executor.ApplyWithCommit(ctx, retry, func(tx core.App, receipt v2.ApplyReceipt) error {
		return writeFieldProjectionMarker(tx, receipt.OperationID)
	})
	if err != nil {
		t.Fatalf("new attempt under a distinct operation id was rejected: %v", err)
	}
	applied := schemaAuditRows(t, app, "operation_id={:operation}", dbx.Params{"operation": retry.OperationID})
	if len(applied) != 1 || applied[0].GetString("outcome") != "applied" ||
		applied[0].GetString("plan_id") != plan.PlanID ||
		applied[0].GetString("field_id") != committed.FieldID {
		t.Fatalf("applied audit differs: %+v", applied)
	}
	history := schemaAuditRows(t, app, "plan_id={:plan}", dbx.Params{"plan": plan.PlanID})
	if len(history) != 2 || auditIDs(history) != auditIDs(append(failed, applied...)) {
		t.Fatalf("plan history = %+v, want the untouched failure and the application", history)
	}
	table, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id='tbl_orders'", dbx.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if table.GetInt("schema_revision") != 2 {
		t.Fatalf("schema revision = %d, want one application", table.GetInt("schema_revision"))
	}

	// Repeating the committed operation replays the receipt without re-projecting
	// or touching either audit row.
	markers, err := app.CountRecords("vibetable_idempotency_keys", dbx.HashExp{"key": "commit-projection:" + retry.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := executor.ApplyWithCommit(ctx, retry, func(core.App, v2.ApplyReceipt) error {
		return errors.New("replayed apply must not re-project")
	})
	if err != nil {
		t.Fatalf("replayed ApplyWithCommit() failed: %v", err)
	}
	if replayed.SchemaRevision != committed.SchemaRevision {
		t.Fatalf("replayed receipt %#v differs from committed receipt %#v", replayed, committed)
	}
	after = schemaAuditRows(t, app, "plan_id={:plan}", dbx.Params{"plan": plan.PlanID})
	if len(after) != 2 || auditIDs(after) != auditIDs(history) {
		t.Fatalf("replay changed the audit history: %+v", after)
	}
	if count, err := app.CountRecords("vibetable_idempotency_keys", dbx.HashExp{"key": "commit-projection:" + retry.OperationID}); err != nil || count != markers || count != 1 {
		t.Fatalf("replay re-projected: markers=%d err=%v", count, err)
	}
}

// An applied or queued row, including one that a field migration later marked
// failed, keeps the operation id and still rejects a second application of the
// same operation.
func TestExecutorAppliedOperationIDIsNeverReused(t *testing.T) {
	for _, scenario := range []string{"queued", "migration_failed"} {
		t.Run(scenario, func(t *testing.T) {
			app, plan, _ := migrationFixture(t)
			plan.PlanID = "plan_applied_twice_" + scenario
			plan.PlanHash, _ = hashPlan(plan)
			store := NewPocketBasePlanStore(app)
			if _, err := store.Save(context.Background(), "intent-applied-twice", time.Now(), plan); err != nil {
				t.Fatal(err)
			}
			scheduler := &commitProjectionScheduler{}
			executor := NewExecutor(app, store, WithMigrationScheduler(scheduler))
			request := v2.ApplyRequest{
				PlanID: plan.PlanID, PlanHash: plan.PlanHash,
				OperationID: "operation-applied-twice", Actor: plan.Intent.Actor,
			}
			if _, err := executor.Apply(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			before := schemaAuditRows(t, app, "operation_id={:operation}", dbx.Params{"operation": request.OperationID})
			if len(before) != 1 || before[0].GetString("outcome") != "queued" {
				t.Fatalf("queued audit missing: %+v", before)
			}
			if scenario == "migration_failed" {
				before[0].Set("outcome", "failed")
				before[0].Set("error_code", "field.migration.conversion_failed")
				if err := app.Save(before[0]); err != nil {
					t.Fatal(err)
				}
			}
			// Drop the replay receipt so the repeated apply reaches the audit insert.
			if _, err := app.DB().NewQuery("DELETE FROM vibetable_idempotency_keys WHERE key={:key}").
				Bind(dbx.Params{"key": "field-v2:" + request.OperationID}).Execute(); err != nil {
				t.Fatal(err)
			}
			_, err := executor.Apply(context.Background(), request)
			// The unique operation_id still decides; no product code replaces it.
			if err == nil || !strings.Contains(err.Error(), "must be unique") {
				t.Fatalf("second application of an applied operation returned %v", err)
			}
			after := schemaAuditRows(t, app, "operation_id={:operation} || operation_id~{:prefix}", dbx.Params{
				"operation": request.OperationID, "prefix": request.OperationID + "#",
			})
			if len(after) != 1 || after[0].Id != before[0].Id || after[0].GetString("operation_id") != request.OperationID ||
				after[0].GetString("outcome") != before[0].GetString("outcome") ||
				after[0].GetString("migration_job_id") != before[0].GetString("migration_job_id") {
				t.Fatalf("applied audit was rewritten or duplicated: %+v", after)
			}
			if len(scheduler.enqueued) != 2 || len(scheduler.started) != 1 {
				t.Fatalf("rejected second application started migration work: enqueued=%v started=%v", scheduler.enqueued, scheduler.started)
			}
			// The failure path found the existing row and added nothing.
			if rows, err := app.FindRecordsByFilter("vibetable_schema_audit", "1=1", "+id", 0, 0, dbx.Params{}); err != nil || len(rows) != 1 {
				t.Fatalf("rejected second application changed the audit history: %+v %v", rows, err)
			}
		})
	}
}
