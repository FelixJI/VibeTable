package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

// TestFormulaClockSourcesThroughRelationLookupAndAggregates pins the shared
// source-reading boundary: a downstream formula may only observe a stored
// computed source (here NOW()/TODAY() driven) as a fresh scalar of its own
// evaluation period. An unrefreshed source is an explicit formula.dependency
// rejection, never an old value, null or envelope JSON; unrelated stale cells
// never block ordinary relations, SQL aggregates or user JSON.
func TestFormulaClockSourcesThroughRelationLookupAndAggregates(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	t0 := time.Date(2024, 3, 10, 6, 59, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), t0)

	source := createV2IntegrationTable(t, ctx, app, "Clock sources", "clock_sources_table")
	label := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Label"), "clock_sources_label",
	)
	amount := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "Amount"), "clock_sources_amount",
	)
	payload := createV2IntegrationField(
		t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalJSON, "Payload"), "clock_sources_payload",
	)
	minuteDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Minute")
	minuteDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `TEXT(NOW(), "HH:mm")`}
	minute := createV2IntegrationFormula(t, ctx, app, source.TableID, minuteDraft, "clock_sources_minute")
	dayDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Day")
	dayDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `DAY(TODAY("UTC"))`}
	day := createV2IntegrationFormula(t, ctx, app, source.TableID, dayDraft, "clock_sources_day")

	relTable := createV2IntegrationTable(t, ctx, app, "Clock relation readers", "clock_relation_readers_table")
	relNote := createV2IntegrationField(
		t, ctx, app, relTable.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Note"), "clock_relation_note",
	)
	src := createV2IntegrationRelation(
		t, ctx, app, relTable.TableID, relNote.FieldID, source.TableID, minute.FieldID,
		"Src", "Readers", "one", "clock_relation_src",
	)
	minuteLabelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Source minute")
	minuteLabelDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Src}.{Minute}, "")`}
	minuteLabel := createV2IntegrationFormula(
		t, ctx, app, relTable.TableID, minuteLabelDraft, "clock_relation_minute",
	)
	plainLabelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Plain label")
	plainLabelDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Src}.{Label}, "")`}
	plainLabel := createV2IntegrationFormula(
		t, ctx, app, relTable.TableID, plainLabelDraft, "clock_relation_plain",
	)
	payloadSizeDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Payload size")
	payloadSizeDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `size({Src}.{Payload})`}
	payloadSize := createV2IntegrationFormula(
		t, ctx, app, relTable.TableID, payloadSizeDraft, "clock_relation_payload",
	)

	lkpTable := createV2IntegrationTable(t, ctx, app, "Clock lookup readers", "clock_lookup_readers_table")
	lkpNote := createV2IntegrationField(
		t, ctx, app, lkpTable.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Note"), "clock_lookup_note",
	)
	lkpSrc := createV2IntegrationRelation(
		t, ctx, app, lkpTable.TableID, lkpNote.FieldID, source.TableID, minute.FieldID,
		"Src", "Readers", "one", "clock_lookup_src",
	)
	lkpDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Clock")
	lkpDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: lkpSrc.FieldID}},
		TargetFieldID: minute.FieldID,
	}
	clockLookup := createV2IntegrationField(
		t, ctx, app, lkpTable.TableID, lkpDraft, "clock_lookup_field",
	)
	plainLookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Plain source")
	plainLookupDraft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: lkpSrc.FieldID}}, TargetFieldID: label.FieldID}
	plainLookup := createV2IntegrationField(t, ctx, app, lkpTable.TableID, plainLookupDraft, "clock_lookup_plain")
	lkpLabelDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Lookup minute")
	lkpLabelDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `concat({Clock}, "")`}
	lkpLabel := createV2IntegrationFormula(
		t, ctx, app, lkpTable.TableID, lkpLabelDraft, "clock_lookup_minute",
	)

	aggTable := createV2IntegrationTable(t, ctx, app, "Clock aggregates", "clock_aggregates_table")
	aggNote := createV2IntegrationField(
		t, ctx, app, aggTable.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Note"), "clock_aggregate_note",
	)
	lines := createV2IntegrationRelation(
		t, ctx, app, aggTable.TableID, aggNote.FieldID, source.TableID, minute.FieldID,
		"Lines", "Rows", "many", "clock_aggregate_lines",
	)
	sumDayDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Day sum")
	sumDayDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `SUM({Lines}.{Day})`}
	sumDay := createV2IntegrationFormula(
		t, ctx, app, aggTable.TableID, sumDayDraft, "clock_aggregate_day",
	)
	sumAmountDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Amount sum")
	sumAmountDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `SUM({Lines}.{Amount})`}
	sumAmount := createV2IntegrationFormula(
		t, ctx, app, aggTable.TableID, sumAmountDraft, "clock_aggregate_amount",
	)
	countDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Line count")
	countDraft.Formula = &v2.FormulaDraftSpec{Language: "cel-v1", Source: `COUNT({Lines})`}
	lineCount := createV2IntegrationFormula(
		t, ctx, app, aggTable.TableID, countDraft, "clock_aggregate_count",
	)
	for _, receipt := range []v2.ApplyReceipt{
		label, amount, payload, minute, day, relNote, src, minuteLabel, plainLabel,
		payloadSize, lkpNote, lkpSrc, clockLookup, lkpLabel, aggNote, lines,
		sumDay, sumAmount, lineCount,
	} {
		if receipt.Definition == nil {
			t.Fatalf("V2 clock source fixture omitted a field definition: %#v", receipt)
		}
	}

	payloadLookalike := map[string]any{
		"state": "ready",
		"value": "imposter",
		"version": map[string]any{
			"definitionVersion":   1,
			"sourceDataRevision":  1,
			"dependencyWatermark": "sha256:00",
		},
	}
	if _, decodes := relatedcomputation.Decode(payloadLookalike); !decodes {
		t.Fatal("payload fixture must decode as an envelope lookalike")
	}
	seed := func(table v2IntegrationTable, recordID string, values map[string]any) {
		t.Helper()
		collection, findErr := app.FindCollectionByNameOrId(table.PhysicalName)
		if findErr != nil {
			t.Fatal(findErr)
		}
		record := core.NewRecord(collection)
		record.Id = recordID
		for name, value := range values {
			record.Set(name, value)
		}
		if saveErr := app.Save(record); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	seed(source, "clocksrc0000001", map[string]any{
		label.Definition.Identity.PhysicalName:   "src",
		amount.Definition.Identity.PhysicalName:  10,
		payload.Definition.Identity.PhysicalName: payloadLookalike,
	})
	seed(source, "clocksrc0000002", map[string]any{
		label.Definition.Identity.PhysicalName:  "src",
		amount.Definition.Identity.PhysicalName: 10,
	})
	seed(relTable, "clockrel0000001", map[string]any{
		relNote.Definition.Identity.PhysicalName: "first",
		src.Definition.Identity.PhysicalName:     "clocksrc0000001",
	})
	seed(lkpTable, "clocklkp0000001", map[string]any{
		lkpNote.Definition.Identity.PhysicalName: "first",
		lkpSrc.Definition.Identity.PhysicalName:  "clocksrc0000001",
	})
	seed(aggTable, "clockagg0000001", map[string]any{
		aggNote.Definition.Identity.PhysicalName: "first",
		lines.Definition.Identity.PhysicalName:   []string{"clocksrc0000001", "clocksrc0000002"},
	})

	kernel := mutation.New(
		app, mutation.MetadataSchemaSource{},
		mutation.WithClock(func() time.Time { return t0 }),
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formula.NewCalculator(formula.NewAppCompiler(app)),
		)),
	)
	service := jobs.New(app, kernel, jobs.WithClock(func() time.Time { return t0 }))
	defer service.Shutdown()
	// Backfills run source-first so downstream readers observe fresh envelopes.
	for _, target := range []struct{ name, tableID string }{
		{"source", source.TableID}, {"relation", relTable.TableID},
		{"lookup", lkpTable.TableID}, {"aggregate", aggTable.TableID},
	} {
		tableID := target.tableID
		definition, describeErr := schemaapi.New(app).Describe(ctx, tableID)
		if describeErr != nil {
			t.Fatal(describeErr)
		}
		job, startErr := service.StartFormulaBackfill(
			ctx, tableID, definition.Snapshot.SchemaRevision,
		)
		if startErr != nil {
			t.Fatal(startErr)
		}
		if runErr := service.Run(ctx, job.JobID); runErr != nil {
			t.Fatalf("backfill %s: %v", target.name, runErr)
		}
	}

	stored := func(table v2IntegrationTable, recordID, physicalName string) any {
		t.Helper()
		collection, findErr := app.FindCollectionByNameOrId(table.PhysicalName)
		if findErr != nil {
			t.Fatal(findErr)
		}
		record, loadErr := app.FindRecordById(collection, recordID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return relatedcomputation.ProjectStored(record.GetRaw(physicalName))
	}
	if got := stored(relTable, "clockrel0000001", minuteLabel.Definition.Identity.PhysicalName); got != "06:59" {
		t.Fatalf("relation minute = %#v", got)
	}
	if got := stored(relTable, "clockrel0000001", plainLabel.Definition.Identity.PhysicalName); got != "src" {
		t.Fatalf("plain relation label = %#v", got)
	}
	if got := stored(lkpTable, "clocklkp0000001", lkpLabel.Definition.Identity.PhysicalName); got != "06:59" {
		t.Fatalf("lookup minute = %#v", got)
	}
	if got := stored(aggTable, "clockagg0000001", sumDay.Definition.Identity.PhysicalName); got != 20.0 {
		t.Fatalf("computed day aggregate = %#v", got)
	}
	if got := stored(aggTable, "clockagg0000001", sumAmount.Definition.Identity.PhysicalName); got != 20.0 {
		t.Fatalf("plain SQL aggregate = %#v", got)
	}
	if got := stored(aggTable, "clockagg0000001", lineCount.Definition.Identity.PhysicalName); got != float64(2) {
		t.Fatalf("relation count = %#v", got)
	}
	// The envelope-shaped user JSON keeps its stored form: its projected size
	// is the stored document's, never the unwrapped "imposter" scalar.
	if got := stored(relTable, "clockrel0000001", payloadSize.Definition.Identity.PhysicalName); got == 8.0 {
		t.Fatalf("payload lookalike was unwrapped: %#v", got)
	}

	applies := 0
	applyAt := func(at time.Time, table v2IntegrationTable, recordID, noteValue string) (mutation.Receipt, error) {
		t.Helper()
		definition, describeErr := schemaapi.New(app).Describe(
			formula.WithEvaluationTime(context.Background(), at), table.TableID,
		)
		if describeErr != nil {
			t.Fatal(describeErr)
		}
		applies++
		noteField := relNote
		if table.TableID == lkpTable.TableID {
			noteField = lkpNote
		} else if table.TableID == aggTable.TableID {
			noteField = aggNote
		}
		return kernel.Apply(
			formula.WithEvaluationTime(context.Background(), at),
			mutationRequest(
				table.TableID, definition.Snapshot.SchemaRevision,
				fmt.Sprintf("clock-sources-%s-%d", table.TableID, applies),
				mutation.Operation{
					Kind: mutation.OperationUpdate, RecordID: &recordID,
					Values: map[string]any{
						noteField.Definition.Identity.PhysicalName: noteValue,
					},
				},
			),
		)
	}
	rejection := func(name string, err error) {
		t.Helper()
		var formulaErr *formula.Error
		if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.dependency" {
			t.Fatalf("%s error = %#v, want formula.dependency", name, err)
		}
		if strings.Contains(formulaErr.Message, "stale") == false &&
			strings.Contains(formulaErr.Message, "envelope") == false {
			t.Fatalf("%s message = %q", name, formulaErr.Message)
		}
	}

	// Same period: the stored scalar keeps serving downstream reads.
	samePeriod, err := applyAt(t0.Add(30*time.Second), relTable, "clockrel0000001", "same period")
	if err != nil {
		t.Fatalf("same-period relation read failed: %v", err)
	}
	if got := samePeriod.ComputedFields["clockrel0000001"][minuteLabel.Definition.Identity.PhysicalName]; got != "06:59" {
		t.Fatalf("same-period minute = %#v", got)
	}
	if _, err := applyAt(t0.Add(30*time.Second), lkpTable, "clocklkp0000001", "same period"); err != nil {
		t.Fatalf("same-period lookup read failed: %v", err)
	}

	// Next minute without a source refresh: explicit rejection, and the stored
	// downstream cell is left untouched rather than aged into null.
	t1 := t0.Add(time.Minute)
	_, err = applyAt(t1, relTable, "clockrel0000001", "next minute")
	rejection("relation reader", err)
	_, err = applyAt(t1, lkpTable, "clocklkp0000001", "next minute")
	rejection("lookup reader", err)
	if got := stored(relTable, "clockrel0000001", minuteLabel.Definition.Identity.PhysicalName); got != "06:59" {
		t.Fatalf("rejected write changed the stored cell: %#v", got)
	}
	// The aggregate reader does not reference the stale minute cell, so its
	// day-period sources stay usable and the SQL aggregate never regresses.
	nextPeriodAggregates, err := applyAt(t1, aggTable, "clockagg0000001", "minute period")
	if err != nil {
		t.Fatalf("unreferenced stale cell blocked the aggregate reader: %v", err)
	}
	if got := nextPeriodAggregates.ComputedFields["clockagg0000001"][sumDay.Definition.Identity.PhysicalName]; got != 20.0 {
		t.Fatalf("day aggregate inside its period = %#v", got)
	}
	if got := nextPeriodAggregates.ComputedFields["clockagg0000001"][sumAmount.Definition.Identity.PhysicalName]; got != 20.0 {
		t.Fatalf("SQL aggregate inside its period = %#v", got)
	}

	// Refreshing the source clock makes the same downstream reads succeed.
	ids, refreshErr := service.RefreshClock(context.Background(), t1)
	if refreshErr != nil {
		t.Fatal(refreshErr)
	}
	if len(ids) != 1 {
		t.Fatalf("clock refresh jobs = %v", ids)
	}
	for _, id := range ids {
		if runErr := service.Run(context.Background(), id); runErr != nil {
			t.Fatal(runErr)
		}
	}
	if got := stored(source, "clocksrc0000001", minute.Definition.Identity.PhysicalName); got != "07:00" {
		t.Fatalf("refreshed source minute = %#v", got)
	}
	refreshedRelation, err := applyAt(t1, relTable, "clockrel0000001", "after refresh")
	if err != nil {
		t.Fatalf("relation read after source refresh failed: %v", err)
	}
	if got := refreshedRelation.ComputedFields["clockrel0000001"][minuteLabel.Definition.Identity.PhysicalName]; got != "07:00" {
		t.Fatalf("relation minute after refresh = %#v", got)
	}
	refreshedLookup, err := applyAt(t1, lkpTable, "clocklkp0000001", "after refresh")
	if err != nil {
		t.Fatalf("lookup read after source refresh failed: %v", err)
	}
	if got := refreshedLookup.ComputedFields["clocklkp0000001"][lkpLabel.Definition.Identity.PhysicalName]; got != "07:00" {
		t.Fatalf("lookup minute after refresh = %#v", got)
	}
	// The user JSON lookalike still passes through unchanged after refresh.
	if got := refreshedRelation.ComputedFields["clockrel0000001"][payloadSize.Definition.Identity.PhysicalName]; got == 8.0 {
		t.Fatalf("payload lookalike was unwrapped after refresh: %#v", got)
	}

	// Next day: the TODAY-driven aggregate source must refresh before use.
	t2 := time.Date(2024, 3, 11, 7, 0, 0, 0, time.UTC)
	_, err = applyAt(t2, aggTable, "clockagg0000001", "next day")
	rejection("day aggregate reader", err)
	ids, refreshErr = service.RefreshClock(context.Background(), t2)
	if refreshErr != nil || len(ids) != 1 {
		t.Fatalf("day refresh jobs = %v, err=%v", ids, refreshErr)
	}
	for _, id := range ids {
		if runErr := service.Run(context.Background(), id); runErr != nil {
			t.Fatal(runErr)
		}
	}
	refreshedAggregates, err := applyAt(t2, aggTable, "clockagg0000001", "next day")
	if err != nil {
		t.Fatalf("day aggregate after source refresh failed: %v", err)
	}
	if got := refreshedAggregates.ComputedFields["clockagg0000001"][sumDay.Definition.Identity.PhysicalName]; got != 22.0 {
		t.Fatalf("day aggregate after refresh = %#v", got)
	}
	if got := refreshedAggregates.ComputedFields["clockagg0000001"][lineCount.Definition.Identity.PhysicalName]; got != int64(2) && got != float64(2) {
		t.Fatalf("relation count after refresh = %#v", got)
	}
	// Two cache transactions model adjacent source backfill batches. Refresh
	// downstream consumers after the first, then prove the second cannot age
	// those results (or unrelated static values) back into pending.
	t3 := t2.Add(time.Minute)
	clockContext := formula.WithEvaluationFields(formula.WithEvaluationTime(context.Background(), t3), map[string]bool{})
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(app, querySource)
	readScalar := func(tableID, field string) any {
		t.Helper()
		page, err := port.QueryPage(clockContext, tableID, query.TableQuery{Limit: 10})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("read computed batch: %#v %v", page, err)
		}
		return page.Rows[0][field]
	}
	refreshRow := func(recordID string) {
		t.Helper()
		definition, err := schemaapi.New(app).Describe(clockContext, source.TableID)
		if err != nil {
			t.Fatal(err)
		}
		request := mutationRequest(source.TableID, definition.Snapshot.SchemaRevision, "clock_partial_"+recordID,
			mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &recordID, Values: map[string]any{}})
		request.Actor = mutation.Actor{Type: "system", ID: "formula-backfill"}
		receipt, err := kernel.RecalculateClock(clockContext, request)
		if err != nil || len(receipt.EmittedEvents) != 1 {
			t.Fatalf("partial source refresh: %#v %v", receipt, err)
		}
		outbox, err := app.FindFirstRecordByFilter("vibetable_outbox", "event_id={:id}", dbx.Params{"id": receipt.EmittedEvents[0]})
		if err != nil {
			t.Fatal(err)
		}
		var event mutation.DataChangedEvent
		if err := json.Unmarshal([]byte(outbox.GetString("payload_json")), &event); err != nil {
			t.Fatal(err)
		}
		ids, err := service.EnqueueInvalidations(clockContext, app, event)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if err := service.Run(clockContext, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, recordID := range []string{"clocksrc0000001", "clocksrc0000002"} {
		refreshRow(recordID)
		for _, target := range []struct{ table, field, want string }{
			{relTable.TableID, plainLabel.Definition.Identity.PhysicalName, "src"},
			{lkpTable.TableID, plainLookup.Definition.Identity.PhysicalName, "src"},
			{relTable.TableID, minuteLabel.Definition.Identity.PhysicalName, "07:01"},
			{lkpTable.TableID, lkpLabel.Definition.Identity.PhysicalName, "07:01"},
		} {
			if got := readScalar(target.table, target.field); got != target.want {
				t.Fatalf("batch %s left %s pending/old: %#v", recordID, target.field, got)
			}
		}
	}

}

// lookupInstantSentinel can never be a legitimately pinned batch instant; it
// only marks a context that reached the view query without one.
var lookupInstantSentinel = time.Date(1999, 12, 31, 23, 59, 0, 0, time.UTC)

type lookupInstantGuardPort struct {
	query.QueryPort
	sawUnpinnedViewQuery bool
}

func (port *lookupInstantGuardPort) ExecuteViewQuery(
	ctx context.Context, tableID string, input query.ViewQuery,
) (query.ViewResult, error) {
	// EnsureEvaluationTimeAt only fills the sentinel when the caller left the
	// instant unpinned; an entry that pins its own instant keeps it.
	if instant := formula.EvaluationTime(
		formula.EnsureEvaluationTimeAt(ctx, lookupInstantSentinel),
	); instant.Equal(lookupInstantSentinel) {
		port.sawUnpinnedViewQuery = true
	}
	return port.QueryPort.ExecuteViewQuery(ctx, tableID, input)
}

// TestQueryLookupsPinsOneEvaluationInstantForViewAndCells guards the relation
// service entry: the paged view query and Lookup recomputation must share
// one evaluation instant even when the caller provides a bare context.
func TestQueryLookupsPinsOneEvaluationInstantForViewAndCells(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	t0 := time.Date(2024, 3, 10, 6, 59, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), t0)

	sources := createV2IntegrationTable(t, ctx, app, "Lookup query sources", "lookup_query_sources_table")
	name := createV2IntegrationField(
		t, ctx, app, sources.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "lookup_query_source_name",
	)
	readers := createV2IntegrationTable(t, ctx, app, "Lookup query readers", "lookup_query_readers_table")
	note := createV2IntegrationField(
		t, ctx, app, readers.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Note"), "lookup_query_reader_note",
	)
	src := createV2IntegrationRelation(
		t, ctx, app, readers.TableID, note.FieldID, sources.TableID, name.FieldID,
		"Src", "Sources", "one", "lookup_query_relation",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Source")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path:          []v2.LookupPathStep{{RelationFieldID: src.FieldID}},
		TargetFieldID: name.FieldID,
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, readers.TableID, lookupDraft, "lookup_query_field",
	)
	if name.Definition == nil || lookupField.Definition == nil {
		t.Fatalf("V2 lookup query fixture omitted definitions: %#v %#v", name, lookupField)
	}
	targetCollection, err := app.FindCollectionByNameOrId(sources.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	target := core.NewRecord(targetCollection)
	target.Id = "lookupquerytgt1"
	target.Set(name.Definition.Identity.PhysicalName, "Ada")
	if err := app.Save(target); err != nil {
		t.Fatal(err)
	}
	readerCollection, err := app.FindCollectionByNameOrId(readers.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	reader := core.NewRecord(readerCollection)
	reader.Id = "lookupqueryrow1"
	reader.Set(src.Definition.Identity.PhysicalName, target.Id)
	if err := app.Save(reader); err != nil {
		t.Fatal(err)
	}

	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	guard := &lookupInstantGuardPort{QueryPort: query.NewPort(app, querySource)}
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	relService := relation.New(app, guard, kernel)
	definition, err := schemaapi.New(app).Describe(ctx, readers.TableID)
	if err != nil {
		t.Fatal(err)
	}
	// No explicit evaluation instant: the QueryLookups entry itself must pin
	// one before the view query and the Lookup cells share the context.
	result, err := relService.QueryLookups(context.Background(), relation.LookupQueryRequest{
		TableID:        readers.TableID,
		SchemaRevision: definition.Snapshot.SchemaRevision,
		Query:          query.TableQuery{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("QueryLookups rows = %#v", result.Rows)
	}
	cell, ok := result.Rows[0][lookupField.Definition.Identity.PhysicalName].(lookup.CellValue)
	if !ok || cell.State != "ok" || cell.Value != "Ada" {
		t.Fatalf("QueryLookups lookup cell = %#v", result.Rows[0][lookupField.Definition.Identity.PhysicalName])
	}
	if guard.sawUnpinnedViewQuery {
		t.Fatal("QueryLookups executed a view query without an entry-pinned evaluation instant")
	}
}
