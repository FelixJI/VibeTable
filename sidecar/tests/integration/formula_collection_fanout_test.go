package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestCollectionFanoutConvergesAcrossSameTableComputedDAG(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	a := createV2IntegrationTable(t, ctx, app, "集合A", "cf_a")
	b := createV2IntegrationTable(t, ctx, app, "集合B", "cf_b")
	amount := createV2IntegrationField(t, ctx, app, a.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "金额"), "cf_amount")
	base := createV2IntegrationField(t, ctx, app, b.TableID, fieldDraftForIntegration(t, v2.LogicalNumber, "来源"), "cf_base")
	note := createV2IntegrationField(t, ctx, app, b.TableID, fieldDraftForIntegration(t, v2.LogicalText, "备注"), "cf_note")
	service := jobs.New(app, nil)
	defer service.Shutdown()
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(formula.NewCalculator(formula.NewAppCompiler(app)))),
		mutation.WithComputationInvalidator(service))
	service.SetKernel(kernel)
	apply := func(tableID, key string, operation mutation.Operation) mutation.Receipt {
		t.Helper()
		definition, err := schemaapi.New(app).Describe(ctx, tableID)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := kernel.Apply(ctx, mutationRequest(tableID, definition.Snapshot.SchemaRevision, key, operation))
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return receipt
	}
	aIDs := []string{"cfarow000000001", "cfarow000000002"}
	bID := "cfbrow000000001"
	for index := range aIDs {
		apply(a.TableID, fmt.Sprintf("cf_seed_a_%d", index), mutation.Operation{Kind: mutation.OperationInsert, RecordID: &aIDs[index], Values: map[string]any{amount.Definition.Identity.PhysicalName: float64(index + 1)}})
	}
	apply(b.TableID, "cf_seed_b", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &bID, Values: map[string]any{base.Definition.Identity.PhysicalName: 5.0}})
	addFormula := func(tableID, name, key, source string) v2.FieldDefinition {
		t.Helper()
		draft := fieldDraftForIntegration(t, v2.LogicalFormula, name)
		draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: source}
		created := createV2IntegrationFormula(t, ctx, app, tableID, draft, key)
		return *created.Definition
	}
	sum := addFormula(a.TableID, "合计", "cf_sum", fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", a.TableID, amount.Definition.Identity.PhysicalName))
	count := addFormula(a.TableID, "条数", "cf_count", fmt.Sprintf("COUNT(TABLE(%q))", a.TableID))
	f := addFormula(a.TableID, "上游", "cf_f", fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", b.TableID, base.Definition.Identity.PhysicalName))
	g := addFormula(a.TableID, "二级", "cf_g", fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", a.TableID, f.Identity.PhysicalName))
	refresh := func(key string) mutation.Receipt {
		t.Helper()
		definition, err := schemaapi.New(app).Describe(ctx, a.TableID)
		if err != nil {
			t.Fatal(err)
		}
		operations := []mutation.Operation{}
		for index := range aIDs {
			operations = append(operations, mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &aIDs[index], Values: map[string]any{}})
		}
		request := mutationRequest(a.TableID, definition.Snapshot.SchemaRevision, key, operations...)
		request.Actor = mutation.Actor{Type: "system", ID: "formula-fanout"}
		receipt, err := kernel.RecalculateComputed(ctx, request)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return receipt
	}
	drain := func() {
		t.Helper()
		for step := 0; step < 24; step++ {
			pending, err := app.FindRecordsByFilter("vibetable_jobs", "job_type='formula_fanout' && state='queued'", "+id", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) == 0 {
				return
			}
			for _, job := range pending {
				if err := service.Run(ctx, job.Id); err != nil {
					t.Fatalf("fanout: %v", err)
				}
			}
		}
		t.Fatal("fanout failed to converge")
	}
	assertValues := func(wantSum, wantF, wantG float64) {
		t.Helper()
		definition, err := schemaapi.New(app).Describe(ctx, a.TableID)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range aIDs {
			record, err := app.FindRecordById(a.PhysicalName, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []struct {
				field v2.FieldDefinition
				value float64
			}{{sum, wantSum}, {count, float64(len(aIDs))}, {f, wantF}, {g, wantG}} {
				envelope, valid := relatedcomputation.Decode(record.GetRaw(expected.field.Identity.PhysicalName))
				expectation, err := relatedcomputation.ExpectationFor(ctx, app, a.TableID, definition.Snapshot.Fields, expected.field.Identity.FieldID, int64(record.GetInt(relatedcomputation.RowRevisionField)))
				if err != nil || !valid || !envelope.Fresh(expectation) || envelope.Value != expected.value {
					t.Fatalf("%s/%s = %#v expectation=%#v err=%v", id, expected.field.Identity.FieldID, envelope, expectation, err)
				}
			}
		}
	}
	refresh("cf_initial")
	drain()
	assertValues(3, 5, 10)
	beforeHistory, err := app.CountRecords("vibetable_audit_events")
	if err != nil {
		t.Fatal(err)
	}
	if receipt := refresh("cf_noop"); len(receipt.EmittedEvents) != 0 {
		t.Fatalf("no-op emitted: %#v", receipt)
	}
	apply(a.TableID, "cf_update_a", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &aIDs[0], Values: map[string]any{amount.Definition.Identity.PhysicalName: 4.0}})
	drain()
	assertValues(6, 5, 10)
	apply(b.TableID, "cf_update_b", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &bID, Values: map[string]any{base.Definition.Identity.PhysicalName: 7.0}})
	definition, err := schemaapi.New(app).Describe(ctx, a.TableID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.FindRecordById(a.PhysicalName, aIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relatedcomputation.NewSourceReader().Read(ctx, app, a.TableID, definition.Snapshot.Fields, g, row); err == nil {
		t.Fatal("transitive source returned old scalar before fanout")
	}
	drain()
	assertValues(6, 7, 14)
	apply(b.TableID, "cf_unrelated_b", mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &bID, Values: map[string]any{note.Definition.Identity.PhysicalName: "unrelated"}})
	drain()
	assertValues(6, 7, 14)
	inserted := "cfarow000000003"
	apply(a.TableID, "cf_insert_a", mutation.Operation{Kind: mutation.OperationInsert, RecordID: &inserted, Values: map[string]any{amount.Definition.Identity.PhysicalName: 3.0}})
	aIDs = append(aIDs, inserted)
	drain()
	assertValues(9, 7, 21)
	apply(a.TableID, "cf_delete_a", mutation.Operation{Kind: mutation.OperationDelete, RecordID: &inserted})
	aIDs = aIDs[:2]
	drain()
	assertValues(6, 7, 14)
	afterHistory, err := app.CountRecords("vibetable_audit_events")
	if err != nil || afterHistory != beforeHistory+5 {
		t.Fatalf("derived writes created history: before=%d after=%d err=%v", beforeHistory, afterHistory, err)
	}

	// Recreate fanout from a non-clock derived outbox event. No ephemeral field
	// selection is needed, and the replay must settle without new cache writes.
	events, err := app.FindRecordsByFilter("vibetable_outbox", "topic='data.changed'", "+id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var derived mutation.DataChangedEvent
	for _, event := range events {
		var candidate mutation.DataChangedEvent
		raw, _ := json.Marshal(event.GetRaw("payload_json"))
		if err := mutation.DecodeStrict(raw, &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.ChangeSetID == nil && candidate.TableID == a.TableID {
			derived = candidate
		}
	}
	if derived.EventID == "" {
		t.Fatal("missing derived outbox event")
	}
	oldJobs, err := app.FindAllRecords("vibetable_jobs", dbx.HashExp{"source_event_id": derived.EventID})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range oldJobs {
		if err := app.Delete(job); err != nil {
			t.Fatal(err)
		}
	}
	restarted := jobs.New(app, kernel)
	defer restarted.Shutdown()
	restarted.ResumePending(ctx)
	recovered, err := app.FindAllRecords("vibetable_jobs", dbx.HashExp{"source_event_id": derived.EventID})
	if err != nil || len(recovered) == 0 {
		t.Fatalf("derived outbox recovery jobs=%v err=%v", recovered, err)
	}
	ids := make([]string, 0, len(recovered))
	for _, job := range recovered {
		ids = append(ids, job.Id)
		waitForJobState(t, restarted, job.Id, "complete")
	}
	replayed, err := restarted.EnqueueInvalidations(ctx, app, derived)
	if err != nil || len(replayed) != len(ids) || replayed[0] != ids[0] {
		t.Fatalf("derived replay duplicated jobs: %v %v %v", ids, replayed, err)
	}
	assertValues(6, 7, 14)
}

func TestCollectionClockRefreshResolvesSameTableAndCrossTableSources(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	instant := time.Date(2024, 1, 1, 23, 59, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), instant)
	source := createV2IntegrationTable(t, ctx, app, "时钟来源", "ct_source")
	target := createV2IntegrationTable(t, ctx, app, "时钟汇总", "ct_target")
	service := jobs.New(app, nil)
	defer service.Shutdown()
	kernel := mutation.New(app, mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(computed.New(formula.NewCalculator(formula.NewAppCompiler(app)))),
		mutation.WithComputationInvalidator(service))
	service.SetKernel(kernel)
	sourceIDs := []string{"ctsource0000001", "ctsource0000002"}
	targetID := "cttarget0000001"
	for index, table := range []v2IntegrationTable{source, target} {
		label := createV2IntegrationField(t, ctx, app, table.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), fmt.Sprintf("ct_label_%d", index))
		definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		ids := sourceIDs
		if index == 1 {
			ids = []string{targetID}
		}
		for _, id := range ids {
			_, err := kernel.Apply(ctx, mutationRequest(table.TableID, definition.Snapshot.SchemaRevision, "ct_seed_"+id,
				mutation.Operation{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{label.Definition.Identity.PhysicalName: id}}))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	addFormula := func(tableID, name, key, expression string) v2.FieldDefinition {
		t.Helper()
		draft := fieldDraftForIntegration(t, v2.LogicalFormula, name)
		draft.Formula = &v2.FormulaDraftSpec{Language: "cel-v2", Source: expression}
		created := createV2IntegrationFormula(t, ctx, app, tableID, draft, key)
		return *created.Definition
	}
	clock := addFormula(source.TableID, "日期", "ct_clock", `DAY(TODAY("UTC")) + DAY(NOW())`)
	expression := fmt.Sprintf("SUM(PROJECT(TABLE(%q), CurrentValue.%s))", source.TableID, clock.Identity.PhysicalName)
	localSum := addFormula(source.TableID, "同表合计", "ct_local_sum", expression)
	remoteSum := addFormula(target.TableID, "跨表合计", "ct_remote_sum", expression)
	drain := func() {
		t.Helper()
		for step := 0; step < 12; step++ {
			pending, err := app.FindRecordsByFilter("vibetable_jobs", "job_type='formula_fanout' && state='queued'", "+id", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) == 0 {
				return
			}
			for _, job := range pending {
				if err := service.Run(ctx, job.Id); err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Fatal("clock collection fanout failed to converge")
	}
	definition, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	operations := []mutation.Operation{}
	for _, id := range sourceIDs {
		operations = append(operations, mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &id, Values: map[string]any{}})
	}
	request := mutationRequest(source.TableID, definition.Snapshot.SchemaRevision, "ct_initial", operations...)
	request.Actor = mutation.Actor{Type: "system", ID: "formula-fanout"}
	if _, err := kernel.RecalculateComputed(ctx, request); err != nil {
		t.Fatal(err)
	}
	drain()
	assertValues := func(at time.Time, wantClock, wantSum float64) {
		t.Helper()
		readCtx := formula.WithEvaluationTime(context.Background(), at)
		for _, item := range []struct {
			table v2IntegrationTable
			row   string
			field v2.FieldDefinition
			want  float64
		}{
			{source, sourceIDs[0], clock, wantClock}, {source, sourceIDs[1], clock, wantClock},
			{source, sourceIDs[0], localSum, wantSum}, {source, sourceIDs[1], localSum, wantSum},
			{target, targetID, remoteSum, wantSum},
		} {
			definition, err := schemaapi.New(app).Describe(readCtx, item.table.TableID)
			if err != nil {
				t.Fatal(err)
			}
			row, err := app.FindRecordById(item.table.PhysicalName, item.row)
			if err != nil {
				t.Fatal(err)
			}
			value, err := relatedcomputation.NewSourceReader().Read(readCtx, app, item.table.TableID, definition.Snapshot.Fields, item.field, row)
			if err != nil || value != item.want {
				t.Fatalf("%s/%s value=%v want=%v err=%v", item.row, item.field.Identity.FieldID, value, item.want, err)
			}
		}
	}
	assertValues(instant, 2, 4)
	historyBefore, err := app.CountRecords("vibetable_audit_events")
	if err != nil {
		t.Fatal(err)
	}
	nextDay := instant.Add(time.Minute)
	ids, err := service.RefreshClock(context.Background(), nextDay)
	if err != nil || len(ids) != 1 {
		t.Fatalf("direct clock root jobs=%v err=%v", ids, err)
	}
	for _, id := range ids {
		if err := service.Run(context.Background(), id); err != nil {
			t.Fatalf("cross-day clock root: %v", err)
		}
	}
	drain()
	assertValues(nextDay, 4, 8)
	historyAfter, err := app.CountRecords("vibetable_audit_events")
	if err != nil || historyAfter != historyBefore {
		t.Fatalf("clock refresh changed row history: %d -> %d, %v", historyBefore, historyAfter, err)
	}
}
