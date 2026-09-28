package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type clockFaultCalculator struct {
	inner   mutation.FormulaCalculator
	failID  string
	invalid bool
	cancel  context.CancelFunc
}

func (calculator clockFaultCalculator) Calculate(ctx context.Context, app core.App, table schemaexecution.Table, record *core.Record) (map[string]any, error) {
	if calculator.cancel != nil {
		calculator.cancel()
		return map[string]any{}, nil
	}
	if calculator.invalid {
		return map[string]any{"id": "cannot change business inputs"}, nil
	}
	if record.Id == calculator.failID {
		return nil, errors.New("source unavailable")
	}
	return calculator.inner.Calculate(ctx, app, table, record)
}

func TestFormulaClockMaterializationAtomicity(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	instant := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), instant)
	table := createV2IntegrationTable(t, ctx, app, "Atomic clock", "atomic_clock_table")
	draft := fieldDraftForIntegration(t, v2.LogicalFormula, "Now")
	draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: "NOW()"}
	created := createV2IntegrationFormula(t, ctx, app, table.TableID, draft, "atomic_clock_formula")
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	field := integrationFieldByID(definition, created.FieldID)
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"atomicclock0001", "atomicclock0002"}
	for _, id := range ids {
		record := core.NewRecord(collection)
		record.Id = id
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	calculator := computed.New(formula.NewCalculator(formula.NewAppCompiler(app)))
	kernel := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator))
	service := jobs.New(app, kernel)
	defer service.Shutdown()
	job, err := service.StartFormulaBackfill(ctx, table.TableID, definition.Snapshot.SchemaRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx, job.JobID); err != nil {
		t.Fatal(err)
	}
	clockCtx := formula.WithEvaluationFields(formula.WithEvaluationTime(context.Background(), instant.Add(time.Minute)), map[string]bool{})
	request := mutationRequest(table.TableID, definition.Snapshot.SchemaRevision, "atomic_clock_refresh",
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &ids[0], Values: map[string]any{}},
		mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &ids[1], Values: map[string]any{}})
	request.Actor = mutation.Actor{Type: "system", ID: "formula-backfill"}
	assertOld := func() {
		t.Helper()
		for _, id := range ids {
			r, e := app.FindRecordById(collection, id)
			if e != nil {
				t.Fatal(e)
			}
			if got := relatedcomputation.ProjectStored(r.GetRaw(field.Identity.PhysicalName)); got != "2024-01-01T12:00:00Z" {
				t.Fatalf("partial refresh persisted: %#v", got)
			}
		}
	}
	cases := []struct {
		name    string
		target  *mutation.Kernel
		context context.Context
		request mutation.Request
	}{
		{"missing clock context", kernel, ctx, request},
		{"missing calculator", mutation.New(app, mutation.MetadataSchemaSource{}), clockCtx, request},
		{"malformed request", kernel, clockCtx, mutation.Request{}},
		{"second row failure", mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(clockFaultCalculator{inner: calculator, failID: ids[1]})), clockCtx, request},
		{"non-computed write", mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(clockFaultCalculator{inner: calculator, invalid: true})), clockCtx, request},
		{"invalidation failure", mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator), mutation.WithComputationInvalidator(failingComputationInvalidator{})), clockCtx, request},
	}
	stale := request
	stale.SchemaRevision = "schema_9999"
	cases = append(cases, struct {
		name    string
		target  *mutation.Kernel
		context context.Context
		request mutation.Request
	}{"stale schema", kernel, clockCtx, stale})
	input := request
	input.Operations = append([]mutation.Operation(nil), request.Operations...)
	input.Operations[0].Values = map[string]any{"fake": "input"}
	cases = append(cases, struct {
		name    string
		target  *mutation.Kernel
		context context.Context
		request mutation.Request
	}{"input change", kernel, clockCtx, input})
	cancelled, cancel := context.WithCancel(clockCtx)
	cancel()
	cases = append(cases, struct {
		name    string
		target  *mutation.Kernel
		context context.Context
		request mutation.Request
	}{"cancelled", kernel, cancelled, request})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.target.RecalculateClock(test.context, test.request); err == nil {
				t.Fatal("invalid cache refresh succeeded")
			}
			assertOld()
		})
	}
	// Storage failures must roll back both the cache columns and the table
	// revision/outbox, regardless of which write rejects the transaction.
	for _, target := range []string{collection.Name, "vibetable_tables", "vibetable_outbox"} {
		t.Run("storage failure "+target, func(t *testing.T) {
			operation := "UPDATE"
			if target == "vibetable_outbox" {
				operation = "INSERT"
			}
			if _, err := app.DB().NewQuery("CREATE TRIGGER clock_write_failure BEFORE " + operation + " ON `" + target + "` BEGIN SELECT RAISE(ABORT, 'injected clock storage failure'); END").Execute(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := app.DB().NewQuery("DROP TRIGGER clock_write_failure").Execute(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := kernel.RecalculateClock(clockCtx, request); err == nil {
				t.Fatal("failed storage transaction succeeded")
			}
			assertOld()
		})
	}
	t.Run("cancel between rows", func(t *testing.T) {
		betweenRows, stop := context.WithCancel(clockCtx)
		defer stop()
		cancelling := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(clockFaultCalculator{cancel: stop}))
		if _, err := cancelling.RecalculateClock(betweenRows, request); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel between rows = %v", err)
		}
		assertOld()
	})
	for _, corrupt := range []float64{-1, 1e6} {
		t.Run(fmt.Sprintf("invalid clock counter %g", corrupt), func(t *testing.T) {
			meta, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID})
			if err != nil {
				t.Fatal(err)
			}
			previous := meta.GetRaw(relatedcomputation.ClockRevisionField)
			if _, err := app.DB().NewQuery("UPDATE vibetable_tables SET clock_revision={:counter} WHERE id={:id}").Bind(dbx.Params{"counter": corrupt, "id": meta.Id}).Execute(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				meta.Set(relatedcomputation.ClockRevisionField, previous)
				if err := app.Save(meta); err != nil {
					t.Error(err)
				}
			}()
			if _, err := kernel.RecalculateClock(clockCtx, request); err == nil {
				t.Fatal("corrupt clock counter accepted")
			}
			assertOld()
		})
	}
	t.Run("revision exhausted", func(t *testing.T) {
		meta, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": table.TableID})
		if err != nil {
			t.Fatal(err)
		}
		previous := meta.GetRaw("data_revision")
		meta.Set("data_revision", int64(9007199254740991))
		if err := app.Save(meta); err != nil {
			t.Fatal(err)
		}
		defer func() {
			meta.Set("data_revision", previous)
			if err := app.Save(meta); err != nil {
				t.Error(err)
			}
		}()
		if _, err := kernel.RecalculateClock(clockCtx, request); err == nil {
			t.Fatal("exhausted revision was incremented")
		}
		assertOld()
	})
	// Live delivery failure must leave the committed event recoverable, while
	// computation/invalidation failures above roll the complete batch back.
	notifying := mutation.New(app, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(calculator), mutation.WithPublisher(failingLiveDataPublisher{}), mutation.WithComputationInvalidator(service))
	receipt, err := notifying.RecalculateClock(clockCtx, request)
	if err != nil || len(receipt.Warnings) != 1 || len(receipt.EmittedEvents) != 1 {
		t.Fatalf("durable publish fallback: %#v %v", receipt, err)
	}
	for _, id := range ids {
		r, e := app.FindRecordById(collection, id)
		if e != nil {
			t.Fatal(e)
		}
		if got := relatedcomputation.ProjectStored(r.GetRaw(field.Identity.PhysicalName)); got != "2024-01-01T12:01:00Z" {
			t.Fatalf("refresh missing: %#v", got)
		}
	}
}
