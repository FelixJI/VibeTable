package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

// Keep dispatch deterministic while using the real transactional enqueue path.
// Run the persisted job through a new service, as startup would after a crash.
type deferredComputedBackfill struct {
	*jobs.Service
	started []string
}

func (scheduler *deferredComputedBackfill) Start(jobID string) bool {
	scheduler.started = append(scheduler.started, jobID)
	return true
}

func TestLookupSchemaChangesBackfillExistingRows(t *testing.T) {
	for _, withFormula := range []bool{false, true} {
		t.Run(fmt.Sprintf("withCollectionFormula=%t", withFormula), func(t *testing.T) {
			app := bootstrapApp(t, queryTempDir(t))
			defer resetApp(t, app)
			ctx := context.Background()
			owner := createV2IntegrationTable(t, ctx, app, "Orders", "lb_orders")
			source := createV2IntegrationTable(t, ctx, app, "Materials", "lb_materials")
			code := createV2IntegrationField(t, ctx, app, owner.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Code"), "lb_code")
			sourceCode := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, v2.LogicalText, "Code"), "lb_source_code")
			amount := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "Amount"), "lb_amount")
			alternate := createV2IntegrationField(t, ctx, app, source.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "Alternate"), "lb_alternate")
			kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(computed.New(
				formula.NewCalculator(formula.NewAppCompiler(app)), lookup.NewCalculator(),
			)))
			seed := func(tableID, rowID string, values map[string]any) {
				t.Helper()
				definition, err := schemaapi.New(app).Describe(ctx, tableID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := kernel.Apply(ctx, mutationRequest(tableID, definition.Snapshot.SchemaRevision, "seed_"+rowID, mutation.Operation{
					Kind: mutation.OperationInsert, RecordID: &rowID, Values: values,
				})); err != nil {
					t.Fatal(err)
				}
			}
			for i, value := range []float64{7, 3} {
				seed(source.TableID, fmt.Sprintf("lbsource%07d", i), map[string]any{
					sourceCode.Definition.Identity.PhysicalName: []string{"A", "B"}[i],
					amount.Definition.Identity.PhysicalName:     value, alternate.Definition.Identity.PhysicalName: value * 10,
				})
			}
			for i, value := range []string{"A", "B", "C"} {
				seed(owner.TableID, fmt.Sprintf("lbowner%08d", i), map[string]any{code.Definition.Identity.PhysicalName: value})
			}
			enqueuer := jobs.New(app, kernel)
			defer enqueuer.Shutdown()
			scheduler := &deferredComputedBackfill{Service: enqueuer}
			catalog := fieldchange.NewCatalog(app)
			store := fieldchange.NewPocketBasePlanStore(app)
			planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
			executor := fieldchange.NewExecutor(app, store, fieldchange.WithFormulaBackfillScheduler(scheduler))
			actor := v2.Actor{ID: "local-user", Kind: "user"}
			runLast := func(previous int) {
				t.Helper()
				if len(scheduler.started) != previous+1 {
					t.Fatalf("schema change did not enqueue a durable backfill: before=%d after=%d", previous, len(scheduler.started))
				}
				jobID := scheduler.started[previous]
				restarted := jobs.New(app, kernel)
				defer restarted.Shutdown()
				queued, err := restarted.Get(ctx, jobID)
				if err != nil || queued.State != "queued" || queued.Progress.Total != 3 {
					t.Fatalf("persisted backfill = %#v, %v", queued, err)
				}
				existing, err := restarted.StartFormulaBackfill(ctx, owner.TableID, queued.SchemaRevision)
				if err != nil || existing.JobID != jobID {
					t.Fatalf("lookup backfill recovery did not reuse durable job: %#v, %v", existing, err)
				}
				if err := restarted.Run(ctx, jobID); err != nil {
					t.Fatal(err)
				}
				done, err := restarted.Get(ctx, jobID)
				if err != nil || done.State != "complete" || done.Progress.Completed != 3 {
					t.Fatalf("finished backfill = %#v, %v", done, err)
				}
			}
			formulaName := ""
			if withFormula {
				draft := fieldDraftForIntegration(t, v2.LogicalFormula, "Source total")
				draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", source.TableID, amount.Definition.Identity.PhysicalName)}
				created := applyCreatedField(t, ctx, catalog, planner, executor, owner.TableID, draft, actor, "lb_formula")
				formulaName = created.Definition.Identity.PhysicalName
				runLast(0)
			}
			draft := fieldDraftForIntegration(t, v2.LogicalLookup, "Matched total")
			draft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{}, TargetFieldID: amount.FieldID, Aggregation: v2.LookupAggregationSum,
				Condition: &v2.LookupCondition{SourceTableID: source.TableID, Match: "all", Rules: []v2.LookupConditionRule{{
					SourceFieldID: sourceCode.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "field", FieldID: code.FieldID},
				}}},
			}
			previous := len(scheduler.started)
			created := applyCreatedField(t, ctx, catalog, planner, executor, owner.TableID, draft, actor, "lb_lookup")
			querySource, err := queryschema.New(app.DataDir())
			if err != nil {
				t.Fatal(err)
			}
			port := query.NewPort(app, querySource)
			assertPending := func() {
				t.Helper()
				page, err := port.QueryPage(ctx, owner.TableID, query.TableQuery{Limit: 10})
				if err != nil || len(page.Rows) != 3 {
					t.Fatalf("pending query = %#v, %v", page, err)
				}
				for _, row := range page.Rows {
					cell, ok := row[created.Definition.Identity.PhysicalName].(map[string]any)
					if !ok || cell["state"] != "updating" {
						t.Fatalf("unmaterialized lookup exposed stale value: %#v", row)
					}
				}
			}
			assertValues := func(want []float64) {
				t.Helper()
				page, err := port.QueryPage(ctx, owner.TableID, query.TableQuery{Limit: 10})
				if err != nil || len(page.Rows) != 3 {
					t.Fatalf("query = %#v, %v", page, err)
				}
				for i, row := range page.Rows {
					if got := row[created.Definition.Identity.PhysicalName]; got != want[i] {
						t.Fatalf("row %d lookup = %#v, want %v", i, got, want[i])
					}
					if withFormula && row[formulaName] != float64(10) {
						t.Fatalf("collection formula changed: %#v", row[formulaName])
					}
					stored, err := app.FindRecordById(owner.PhysicalName, fmt.Sprint(row["id"]))
					if err != nil {
						t.Fatal(err)
					}
					if envelope, ok := relatedcomputation.Decode(stored.GetRaw(created.Definition.Identity.PhysicalName)); !ok || envelope.Value != want[i] {
						t.Fatalf("stored lookup = %#v", envelope)
					}
				}
			}
			assertPending()
			runLast(previous)
			assertValues([]float64{7, 3, 0})
			// Target projection and condition edits use the same schema-change entry point.
			for i := range 2 {
				draft.Lookup.TargetFieldID = alternate.FieldID
				want := []float64{70, 30, 0}
				if i == 1 {
					draft.Lookup.Condition.Rules[0].Operand = &v2.LookupOperand{Kind: "constant", Value: "B"}
					want = []float64{30, 30, 30}
				}
				revisions, err := catalog.Revisions(ctx, owner.TableID)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := planner.Plan(ctx, v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: owner.TableID, FieldID: created.FieldID,
					ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor})
				if err != nil || !plan.CanApply {
					t.Fatalf("update plan = %#v, %v", plan, err)
				}
				unavailable := fieldchange.NewExecutor(app, store)
				_, applyErr := unavailable.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: fmt.Sprintf("lb_unavailable_%d", i), Actor: actor})
				var productErr *fieldchange.ProductError
				if !errors.As(applyErr, &productErr) || productErr.Code != "formula.backfill.unavailable" {
					t.Fatalf("nonempty lookup edit committed without scheduler: %v", applyErr)
				}
				rolledBack, err := catalog.Revisions(ctx, owner.TableID)
				if err != nil || rolledBack.Schema != revisions.Schema {
					t.Fatalf("failed enqueue committed schema: %#v, %v", rolledBack, err)
				}
				previous = len(scheduler.started)
				if _, err := executor.Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: fmt.Sprintf("lb_update_%d", i), Actor: actor}); err != nil {
					t.Fatal(err)
				}
				assertPending()
				runLast(previous)
				assertValues(want)
			}
		})
	}
}
