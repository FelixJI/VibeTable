package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

// A decorated authority records every business mutation the real executor
// submits without changing any behaviour of the underlying authority.
type sourceSemanticsProbe struct {
	sourceimport.Authority
	requests []mutation.Request
}

func (probe *sourceSemanticsProbe) Mutate(ctx context.Context, request mutation.Request, commit func(core.App, mutation.Receipt) error) (mutation.Receipt, error) {
	probe.requests = append(probe.requests, request)
	return probe.Authority.Mutate(ctx, request, commit)
}

func sourceSemanticsKey(source sourceimport.Snapshot, table, field, record string) sourceimport.Key {
	return sourceimport.Key{Provider: source.Provider, ContainerID: source.ContainerID, TableID: table, FieldID: field, RecordID: record}
}

func sourceSemanticsMappings(t *testing.T, result sourceimport.Result) map[sourceimport.Key]sourceimport.Mapping {
	t.Helper()
	mappings := map[sourceimport.Key]sourceimport.Mapping{}
	for _, batch := range result.Batches {
		for _, mapping := range batch.Mappings {
			if _, exists := mappings[mapping.Source]; exists {
				t.Fatalf("duplicate composite mapping: %#v", mapping.Source)
			}
			mappings[mapping.Source] = mapping
		}
	}
	return mappings
}

func sourceSemanticsFieldBlocked(plan sourceimport.Plan, fieldID, code string) bool {
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.FieldID == fieldID && diagnostic.Code == code && diagnostic.Blocking {
			return true
		}
	}
	return false
}

// sourceSemanticsFanOutSnapshot builds the three-table topology for the >399
// fan-out regression: a single hub record relating to every target row through
// a reciprocal many pair, plus a one-cardinality detail pair.
func sourceSemanticsFanOutSnapshot(targets int) sourceimport.Snapshot {
	text := func(id string) sourceimport.Field {
		return sourceimport.Field{ID: id, Name: id, Kind: "text", ValueKind: v2.LogicalText}
	}
	relation := func(id, target, reverse, cardinality string) sourceimport.Field {
		return sourceimport.Field{ID: id, Name: id, Kind: "relation", ValueKind: v2.LogicalRelation,
			Relation: &sourceimport.Relation{TargetTableID: target, TargetFieldID: reverse, Cardinality: cardinality}}
	}
	refs := make([]string, targets)
	rows := make([]sourceimport.Record, targets)
	for index := 0; index < targets; index++ {
		id := fmt.Sprintf("t%04d", index+1)
		refs[index] = id
		rows[index] = sourceimport.Record{ID: id, Values: map[string]any{
			"title": fmt.Sprintf("目标 %d", index+1), "code": fmt.Sprintf("b-%04d", index+1), "ba": []string{"hub"},
		}}
	}
	return sourceimport.Snapshot{
		Contract: sourceimport.Contract, Provider: "synthetic", ContainerID: "fanout",
		DisplayName: "扇出回归", Version: "v1",
		ReadWindow: sourceimport.ReadWindow{StartedAt: "2026-10-04T08:00:00Z", FinishedAt: "2026-10-04T08:01:00Z", Consistency: "snapshot"},
		Tables: []sourceimport.Table{
			{ID: "a", Name: "扇出主表", Version: "a1", PrimaryFieldID: "title",
				Fields: []sourceimport.Field{text("title"), text("code"), relation("ab", "b", "ba", "many"), relation("ac", "c", "ca", "many")},
				Records: []sourceimport.Record{{ID: "hub", Values: map[string]any{
					"title": "中心记录", "code": "a-hub", "ab": refs, "ac": []string{"c1", "c2"},
				}}}},
			{ID: "b", Name: "扇出目标", Version: "b1", PrimaryFieldID: "title",
				Fields:  []sourceimport.Field{text("title"), text("code"), relation("ba", "a", "ab", "many")},
				Records: rows},
			{ID: "c", Name: "扇出明细", Version: "c1", PrimaryFieldID: "title",
				Fields: []sourceimport.Field{text("title"), text("code"), relation("ca", "a", "ac", "one")},
				Records: []sourceimport.Record{
					{ID: "c1", Values: map[string]any{"title": "明细一", "code": "c-0001", "ca": "hub"}},
					{ID: "c2", Values: map[string]any{"title": "明细二", "code": "c-0002", "ca": "hub"}},
				}},
		},
	}
}

func sourceSemanticsFanOutPreview(t *testing.T, source sourceimport.Snapshot) sourceimport.Plan {
	t.Helper()
	options := sourceimport.Options{SelectedTableIDs: []string{"a", "b", "c"}, ConfirmReverse: true,
		TargetNames: []sourceimport.TargetName{
			{TableID: "a", Name: "迁移主表"}, {TableID: "b", Name: "迁移目标"}, {TableID: "c", Name: "迁移明细"},
		}}
	plan, err := sourceimport.Preview(context.Background(), source, options, nil)
	if err != nil || !plan.CanApply {
		t.Fatalf("fan-out preview blocked: %v, %#v", err, plan.Diagnostics)
	}
	return plan
}

// sourceSemanticsAssertFanOutEdges re-reads the durable PocketBase facts
// through the mapping receipts, independent of the executor's identity maps.
func sourceSemanticsAssertFanOutEdges(t *testing.T, fixture *sourceImportAuthorityFixture, source sourceimport.Snapshot, result sourceimport.Result, targets int) {
	t.Helper()
	ctx := context.Background()
	mappings := sourceSemanticsMappings(t, result)
	collections := map[string]string{}
	definitions := map[string]map[string]v2.FieldDefinition{}
	table := func(sourceID string) (string, string, map[string]v2.FieldDefinition) {
		if collection, cached := collections[sourceID]; cached {
			return mappings[sourceSemanticsKey(source, sourceID, "", "")].LocalID, collection, definitions[sourceID]
		}
		localTable := mappings[sourceSemanticsKey(source, sourceID, "", "")].LocalID
		if localTable == "" {
			t.Fatalf("table mapping missing: %s", sourceID)
		}
		metadata, err := fixture.app.FindFirstRecordByData("vibetable_tables", "table_id", localTable)
		if err != nil {
			t.Fatalf("target mapping did not resolve: %s: %v", sourceID, err)
		}
		snapshot, err := fixture.authority.Describe(ctx, localTable)
		if err != nil {
			t.Fatal(err)
		}
		byLocal := map[string]v2.FieldDefinition{}
		for _, definition := range snapshot.Fields {
			byLocal[definition.Identity.FieldID] = definition
		}
		collections[sourceID] = metadata.GetString("collection_id")
		definitions[sourceID] = byLocal
		return localTable, collections[sourceID], byLocal
	}
	physical := func(sourceID, fieldID string) (string, string) {
		_, _, byLocal := table(sourceID)
		localField := mappings[sourceSemanticsKey(source, sourceID, fieldID, "")].LocalID
		if localField == "" {
			t.Fatalf("field mapping missing: %s/%s", sourceID, fieldID)
		}
		definition, found := byLocal[localField]
		if !found || definition.Relation == nil {
			t.Fatalf("relation definition missing: %s/%s", sourceID, fieldID)
		}
		return localField, definition.Identity.PhysicalName
	}
	edges := func(sourceID, recordID, fieldID string) []string {
		_, collection, _ := table(sourceID)
		localRecord := mappings[sourceSemanticsKey(source, sourceID, "", recordID)].LocalID
		if localRecord == "" {
			t.Fatalf("record mapping missing: %s/%s", sourceID, recordID)
		}
		record, err := fixture.app.FindRecordById(collection, localRecord)
		if err != nil {
			t.Fatalf("target record missing: %s/%s: %v", sourceID, recordID, err)
		}
		_, name := physical(sourceID, fieldID)
		actual := record.GetStringSlice(name)
		sort.Strings(actual)
		return actual
	}
	for sourceID, want := range map[string]int{"a": 1, "b": targets, "c": 2} {
		_, collection, _ := table(sourceID)
		rows, err := fixture.app.FindAllRecords(collection)
		if err != nil || len(rows) != want {
			t.Fatalf("target row count for %s = %d, want %d: %v", sourceID, len(rows), want, err)
		}
	}
	// The hub keeps every forward edge: a chunked write must never lose,
	// duplicate or reorder the complete target set.
	wantTargets := make([]string, targets)
	for index := 1; index <= targets; index++ {
		wantTargets[index-1] = mappings[sourceSemanticsKey(source, "b", "", fmt.Sprintf("t%04d", index))].LocalID
		if wantTargets[index-1] == "" {
			t.Fatalf("target record mapping missing: t%04d", index)
		}
	}
	sort.Strings(wantTargets)
	if actual := edges("a", "hub", "ab"); !reflect.DeepEqual(actual, wantTargets) {
		t.Fatalf("hub fan-out edge set incomplete: %d of %d edges", len(actual), targets)
	}
	// Every reciprocal row points back at exactly the hub.
	hubRow := mappings[sourceSemanticsKey(source, "a", "", "hub")].LocalID
	for index := 1; index <= targets; index++ {
		id := fmt.Sprintf("t%04d", index)
		if actual := edges("b", id, "ba"); len(actual) != 1 || actual[0] != hubRow {
			t.Fatalf("reciprocal edge for %s = %v, want [%s]", id, actual, hubRow)
		}
	}
	wantDetails := []string{
		mappings[sourceSemanticsKey(source, "c", "", "c1")].LocalID,
		mappings[sourceSemanticsKey(source, "c", "", "c2")].LocalID,
	}
	sort.Strings(wantDetails)
	if actual := edges("a", "hub", "ac"); !reflect.DeepEqual(actual, wantDetails) {
		t.Fatalf("hub detail edges = %v, want %v", actual, wantDetails)
	}
	for _, id := range []string{"c1", "c2"} {
		if actual := edges("c", id, "ca"); len(actual) != 1 || actual[0] != hubRow {
			t.Fatalf("detail edge for %s = %v, want [%s]", id, actual, hubRow)
		}
	}
}

func TestSourceImportSemanticsRelationFanOut801StaysBoundedAndKeepsEveryEdge(t *testing.T) {
	const targets = 801
	const jobID = "semantics-fanout-801"
	fixture := newSourceImportAuthorityFixture(t)
	source := sourceSemanticsFanOutSnapshot(targets)
	journal := sourceimport.NewJournal(fixture.app)
	probe := &sourceSemanticsProbe{Authority: fixture.authority}
	result, err := sourceimport.NewExecutor(probe, journal, nil).Execute(context.Background(), sourceSemanticsFanOutPreview(t, source), jobID, 7)
	if err != nil || result.State != "succeeded" || result.Stage != "settled" {
		t.Fatalf("fan-out migration outcome: %#v, %v", result, err)
	}
	if result.Created != targets+3 || result.NotSubmitted != 0 || result.UnknownRecords != 0 {
		t.Fatalf("fan-out outcome counts: created=%d pending=%d unknown=%d", result.Created, result.NotSubmitted, result.UnknownRecords)
	}
	// Every submitted mutation stays within the kernel's 1000-operation limit
	// including the reciprocal row writes its relation update triggers, and
	// within the executor's own 400-operation import reserve.
	seenKeys := map[string]bool{}
	cumulative := map[string]int{}
	submissions := map[string]int{}
	inserts := 0
	for _, request := range probe.requests {
		if request.IdempotencyKey == "" || seenKeys[request.IdempotencyKey] {
			t.Fatalf("relation batches must use distinct idempotency keys: %s", request.IdempotencyKey)
		}
		seenKeys[request.IdempotencyKey] = true
		if len(request.Operations) > 1000 {
			t.Fatalf("mutation request carried %d operations, kernel limit is 1000", len(request.Operations))
		}
		for _, operation := range request.Operations {
			switch operation.Kind {
			case mutation.OperationInsert:
				inserts++
			case mutation.OperationUpdate:
				if len(operation.Values) != 1 {
					t.Fatalf("relation update touched %d fields, want exactly the relation field", len(operation.Values))
				}
				for field, value := range operation.Values {
					refs, ok := value.([]string)
					if !ok {
						t.Fatalf("relation update value = %#T, want a string slice", value)
					}
					edge := *operation.RecordID + "|" + field
					added := len(refs) - cumulative[edge]
					if added <= 0 {
						t.Fatalf("relation chunk did not extend the cumulative edge set: %s grew by %d", edge, added)
					}
					cumulative[edge] = len(refs)
					submissions[edge]++
					if total := len(request.Operations) + added; total > 1000 {
						t.Fatalf("relation batch = %d ops + %d reciprocal writes exceeds the 1000-operation kernel limit",
							len(request.Operations), added)
					}
					if len(request.Operations)+added > 400 {
						t.Fatalf("relation batch = %d ops + %d reciprocal writes exceeds the 400-operation import reserve",
							len(request.Operations), added)
					}
				}
			default:
				t.Fatalf("unexpected operation kind in fan-out migration: %v", operation.Kind)
			}
		}
	}
	if inserts != targets+3 {
		t.Fatalf("fan-out migration submitted %d inserts, want %d", inserts, targets+3)
	}
	hubEdge, hubEdges := "", 0
	for edge, size := range cumulative {
		if size > hubEdges {
			hubEdge, hubEdges = edge, size
		}
	}
	if hubEdges != targets {
		t.Fatalf("largest submitted edge set carried %d of %d edges", hubEdges, targets)
	}
	// 801 targets cannot fit one bounded batch: the fan-out must have been
	// split across several submissions without dropping edges.
	if submissions[hubEdge] < 3 {
		t.Fatalf("%d-edge fan-out was submitted in %d batches, expected chunking", targets, submissions[hubEdge])
	}
	relationWrites := 0
	for _, batch := range result.Batches {
		relationWrites += batch.RelationWrites
	}
	if relationWrites != targets+2 {
		t.Fatalf("durable relation receipts recorded %d writes, want %d", relationWrites, targets+2)
	}
	sourceSemanticsAssertFanOutEdges(t, fixture, source, result, targets)
	// Close and reopen PocketBase, then verify the persisted receipts still
	// reproduce the complete edge set without the executor's identity maps.
	if err := fixture.app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	persisted, err := sourceimport.NewJournal(fixture.app).Read(context.Background(), jobID)
	if err != nil || !reflect.DeepEqual(result, persisted) {
		t.Fatalf("durable fan-out result differs: %v", err)
	}
	sourceSemanticsAssertFanOutEdges(t, fixture, source, persisted, targets)
}

// sourceSemanticsValuesSnapshot covers this path's date timezone, currency and
// percent number formats, and the lookup/system confirmation strategies. The
// zero/false/null/empty/code/precision cases already covered by the shared
// value suite are deliberately not repeated.
func sourceSemanticsValuesSnapshot() sourceimport.Snapshot {
	number := func(id string, format sourceimport.NumberFormat) sourceimport.Field {
		return sourceimport.Field{ID: id, Name: id, Kind: "number", ValueKind: v2.LogicalNumber, NumberFormat: &format}
	}
	return sourceimport.Snapshot{
		Contract: sourceimport.Contract, Provider: "synthetic", ContainerID: "semantics",
		DisplayName: "语义迁移", Version: "v1",
		ReadWindow: sourceimport.ReadWindow{StartedAt: "2026-10-04T08:00:00Z", FinishedAt: "2026-10-04T08:01:00Z", Consistency: "snapshot"},
		Tables: []sourceimport.Table{{
			ID: "sem", Name: "语义回归表", Version: "s1", PrimaryFieldID: "title",
			Fields: []sourceimport.Field{
				{ID: "title", Name: "标题", Kind: "text", ValueKind: v2.LogicalText},
				{ID: "d_local", Name: "本地日期", Kind: "date", ValueKind: v2.LogicalDate, Timezone: "Asia/Shanghai"},
				{ID: "dt_cn", Name: "上海时刻", Kind: "dateTime", ValueKind: v2.LogicalDateTime, Timezone: "Asia/Shanghai"},
				{ID: "dt_utc", Name: "UTC时刻", Kind: "dateTime", ValueKind: v2.LogicalDateTime, Timezone: "UTC"},
				number("cur", sourceimport.NumberFormat{DisplayScale: 2, ScaleMode: "fixed", UseGrouping: true,
					Currency: "EUR", PercentStorage: "ratio"}),
				number("pct", sourceimport.NumberFormat{DisplayScale: 1, ScaleMode: "max", TrimTrailingZeros: true,
					Currency: "USD", PercentStorage: "percent"}),
				number("int_only", sourceimport.NumberFormat{OnlyInt: true, DisplayScale: 0, ScaleMode: "fixed",
					UseGrouping: true, Currency: "CNY", PercentStorage: "ratio"}),
				{ID: "look", Name: "汇总", Kind: "lookup", ValueKind: v2.LogicalLookup, Definition: "SUM(orders.amount)"},
				{ID: "sys", Name: "系统列", Kind: "system", ValueKind: v2.LogicalJSON, Definition: "created_time"},
			},
			Records: []sourceimport.Record{
				{ID: "r1", Values: map[string]any{"title": "第一行", "d_local": "2026-03-01",
					"dt_cn": "2026-03-01T08:30:05+08:00", "dt_utc": "2026-03-01T00:30:05Z",
					"cur": json.Number("1234.5"), "pct": json.Number("0.15"), "int_only": json.Number("801"),
					"look": "1234.5", "sys": "unused"}},
				{ID: "r2", Values: map[string]any{"title": "第二行", "d_local": "2026-12-31",
					"dt_cn": "2026-12-31T23:59:59+08:00", "dt_utc": "2026-12-31T15:59:59Z",
					"cur": json.Number("-0.5"), "pct": json.Number("1"), "int_only": json.Number("0"),
					"look": "-0.5", "sys": "unused"}},
			},
		}},
	}
}

func TestSourceImportSemanticsExecuteTimezoneNumberFormatsAndConfirmedStrategies(t *testing.T) {
	ctx := context.Background()
	fixture := newSourceImportAuthorityFixture(t)
	source := sourceSemanticsValuesSnapshot()
	options := sourceimport.Options{SelectedTableIDs: []string{"sem"}}
	// Native lookup/system migration stays blocked on this path.
	blocked, err := sourceimport.Preview(ctx, source, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.CanApply ||
		!sourceSemanticsFieldBlocked(blocked, "look", "source_import.field_strategy") ||
		!sourceSemanticsFieldBlocked(blocked, "sys", "source_import.field_strategy") {
		t.Fatalf("native lookup/system migration must stay blocked: %#v", blocked.Diagnostics)
	}
	options.Decisions = []sourceimport.Decision{
		{TableID: "sem", FieldID: "look", Policy: sourceimport.PolicySnapshot, TargetKind: v2.LogicalText, Confirmed: true},
		{TableID: "sem", FieldID: "sys", Policy: sourceimport.PolicySkip, Confirmed: true},
	}
	plan, err := sourceimport.Preview(ctx, source, options, nil)
	if err != nil || !plan.CanApply {
		t.Fatalf("confirmed semantics strategies blocked the plan: %v, %#v", err, plan.Diagnostics)
	}
	// The frozen plan keeps the source offset form verbatim: zones are display
	// facts and never rewrite the reviewed source values.
	if plan.Tables[0].Records[0].Values["dt_cn"] != "2026-03-01T08:30:05+08:00" {
		t.Fatalf("planned dateTime was coerced: %#v", plan.Tables[0].Records[0].Values["dt_cn"])
	}
	result, err := sourceimport.NewExecutor(fixture.authority, sourceimport.NewJournal(fixture.app), nil).Execute(ctx, plan, "semantics-values", 7)
	if err != nil || result.State != "succeeded" || result.Stage != "settled" {
		t.Fatalf("semantics migration outcome: %#v, %v", result, err)
	}
	if result.Created != 2 || result.NotSubmitted != 0 {
		t.Fatalf("semantics migration counts: created=%d pending=%d", result.Created, result.NotSubmitted)
	}
	mappings := sourceSemanticsMappings(t, result)
	tableID := mappings[sourceSemanticsKey(source, "sem", "", "")].LocalID
	metadata, err := fixture.app.FindFirstRecordByData("vibetable_tables", "table_id", tableID)
	if err != nil {
		t.Fatal(err)
	}
	collection := metadata.GetString("collection_id")
	snapshot, err := fixture.authority.Describe(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Fields) != 8 {
		t.Fatalf("created %d fields, want the 8 non-skipped source fields", len(snapshot.Fields))
	}
	if _, created := mappings[sourceSemanticsKey(source, "sem", "sys", "")]; created {
		t.Fatal("confirmed-skip system field was created locally")
	}
	definitions := map[string]v2.FieldDefinition{}
	for _, definition := range snapshot.Fields {
		definitions[definition.Identity.FieldID] = definition
	}
	local := func(fieldID string) v2.FieldDefinition {
		id := mappings[sourceSemanticsKey(source, "sem", fieldID, "")].LocalID
		if id == "" {
			t.Fatalf("field mapping missing: %s", fieldID)
		}
		definition, found := definitions[id]
		if !found {
			t.Fatalf("created field missing from the authority: %s", fieldID)
		}
		return definition
	}
	// Temporal zones and currency/percent formats survive execution as the
	// durable display contract of the created fields.
	if day := local("d_local"); day.Display.Timezone != "Asia/Shanghai" {
		t.Fatalf("date timezone lost after execution: %#v", day.Display)
	}
	if when := local("dt_cn"); when.Display.Timezone != "Asia/Shanghai" {
		t.Fatalf("dateTime timezone lost after execution: %#v", when.Display)
	}
	if when := local("dt_utc"); when.Display.Timezone != "UTC" {
		t.Fatalf("UTC dateTime timezone lost after execution: %#v", when.Display)
	}
	if price := local("cur"); price.Display.Currency != "EUR" || price.Display.DisplayScale != 2 ||
		!price.Display.UseGrouping || price.Display.PercentStorage != "ratio" {
		t.Fatalf("currency format lost after execution: %#v", price.Display)
	}
	if rate := local("pct"); rate.Display.PercentStorage != "percent" || rate.Display.DisplayScale != 1 ||
		rate.Display.Currency != "USD" {
		t.Fatalf("percent format lost after execution: %#v", rate.Display)
	}
	if units := local("int_only"); !units.Storage.Options.OnlyInt || units.Display.DisplayScale != 0 ||
		units.Display.ScaleMode != "fixed" {
		t.Fatalf("integer format lost after execution: %#v", units.Storage)
	}
	// The shared FieldValueKernel is the final authority for the persisted
	// canonical values: every physical column must equal its normalized
	// product value, so zones and display formats never corrupt storage.
	// productrow.Project is the same provider-to-product projection the
	// mutation guards use, including presence companions.
	kernel := fieldvalue.New()
	planFields := map[string]sourceimport.FieldPlan{}
	for _, field := range plan.Tables[0].Fields {
		planFields[field.Source.ID] = field
	}
	for _, row := range source.Tables[0].Records {
		record, err := fixture.app.FindRecordById(collection, mappings[sourceSemanticsKey(source, "sem", "", row.ID)].LocalID)
		if err != nil {
			t.Fatalf("target record missing: %s: %v", row.ID, err)
		}
		projectedRow := productrow.Project(snapshot.Fields, record)
		for _, fieldID := range []string{"title", "d_local", "dt_cn", "dt_utc", "cur", "pct", "int_only", "look"} {
			definition := local(fieldID)
			canonical, err := sourceimport.CanonicalValue(planFields[fieldID], definition, row.Values[fieldID])
			if err != nil {
				t.Fatalf("%s/%s canonical value: %v", row.ID, fieldID, err)
			}
			normalized, err := kernel.NormalizeWrite(ctx, definition, fieldvalue.Insert,
				fieldvalue.Input{Supplied: true, Value: canonical})
			if err != nil || !normalized.Write {
				t.Fatalf("%s/%s kernel normalization: %#v, %v", row.ID, fieldID, normalized, err)
			}
			if projected := projectedRow[definition.Identity.PhysicalName]; !reflect.DeepEqual(projected, normalized.ProductValue) {
				t.Fatalf("%s/%s persisted %#v, want the shared kernel canonical %#v",
					row.ID, fieldID, projected, normalized.ProductValue)
			}
		}
	}
	// The offset source instant must be stored as its UTC canonical form.
	record, err := fixture.app.FindRecordById(collection, mappings[sourceSemanticsKey(source, "sem", "", "r1")].LocalID)
	if err != nil {
		t.Fatal(err)
	}
	definition := local("dt_cn")
	if projected := productrow.Project(snapshot.Fields, record)[definition.Identity.PhysicalName]; projected != "2026-03-01T00:30:05Z" {
		t.Fatalf("offset dateTime persisted as %#v, want the UTC canonical form", projected)
	}
	// Durable provenance keeps the confirmed strategies and source expression.
	summaries := map[string]sourceimport.FieldSummary{}
	for _, summary := range result.Fields {
		summaries[summary.Source.FieldID] = summary
	}
	if summaries["look"].Policy != sourceimport.PolicySnapshot || summaries["look"].Definition != "SUM(orders.amount)" {
		t.Fatalf("lookup provenance lost: %#v", summaries["look"])
	}
	if summaries["sys"].Policy != sourceimport.PolicySkip {
		t.Fatalf("system provenance lost: %#v", summaries["sys"])
	}
}
