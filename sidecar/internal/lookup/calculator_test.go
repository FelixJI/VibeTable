package lookup

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestCanonicalLookupValuePreservesOneOrManyCardinality(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		values []any
		want   any
	}{
		{name: "empty", values: []any{}, want: nil},
		{name: "one", values: []any{"Ada"}, want: "Ada"},
		{name: "many", values: []any{"Ada", "Grace"}, want: []any{"Ada", "Grace"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := canonicalLookupValue(testCase.values); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("canonical lookup value = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestLookupDefinitionRejectsRemovedAggregateField(t *testing.T) {
	var lookup v2.LookupSpec
	err := v2.StrictDecode(
		[]byte(`{"relationFieldId":"customer","targetFieldId":"name","aggregate":"sum"}`),
		&lookup,
	)
	if err == nil {
		t.Fatal("removed Lookup aggregate field was accepted")
	}
}

func TestRelationIDsAcceptProviderNeutralCollections(t *testing.T) {
	for _, testCase := range []struct {
		value any
		want  []string
	}{
		{value: nil, want: []string{}},
		{value: "one", want: []string{"one"}},
		{value: []string{"one", "two"}, want: []string{"one", "two"}},
		{value: [2]string{"one", "two"}, want: []string{"one", "two"}},
	} {
		if got := relationIDs(testCase.value); !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("relationIDs(%#v) = %#v, want %#v", testCase.value, got, testCase.want)
		}
	}
}

func TestDecodeLookupFieldValuePreservesV2OptionIdentity(t *testing.T) {
	record := core.NewRecord(core.NewBaseCollection("lookup_targets"))
	record.Set("f_status", "opt_in_progress")
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{PhysicalName: "f_status"},
		LogicalType: v2.LogicalSelect,
		Select: &v2.SelectSpec{Options: []v2.SelectOption{{
			OptionID: "opt_in_progress", Label: "进行中", State: v2.OptionActive,
		}}},
	}
	if got := decodeLookupFieldValue(field, record); got != "opt_in_progress" {
		t.Fatalf("decoded Lookup option = %#v", got)
	}
}

func TestLookupPageCollectorSlicesAcrossTraversalBranches(t *testing.T) {
	collector := lookupPageCollector{offset: 3, limit: 3}
	if start, end, stop := collector.rangeFor(2); start != 0 || end != 0 || stop {
		t.Fatalf("first branch range = %d:%d stop=%v", start, end, stop)
	}
	if start, end, stop := collector.rangeFor(4); start != 1 || end != 4 || stop {
		t.Fatalf("second branch range = %d:%d stop=%v", start, end, stop)
	}
	if start, end, stop := collector.rangeFor(10_001); start != 0 || end != 0 || !stop {
		t.Fatalf("after page branch range = %d:%d stop=%v", start, end, stop)
	}
	if collector.total != 10_007 {
		t.Fatalf("total = %d", collector.total)
	}
}

func TestMaterializationBudgetMeasuresBytesInsteadOfRecordCount(t *testing.T) {
	budget := &materializationBudget{remainingBytes: 8}
	if err := budget.consume("abc"); err != nil {
		t.Fatal(err)
	}
	if budget.remainingBytes != 3 {
		t.Fatalf("remaining bytes = %d", budget.remainingBytes)
	}
	err := budget.consume("toolarge")
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "lookup.value.too_expensive" {
		t.Fatalf("materialization error = %#v", err)
	}
}

func TestWalkLookupPageHonorsCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkLookupPage(
		ctx, nil, traversalNode{}, v2.FieldDefinition{},
		[]v2.LookupPathStep{{RelationFieldID: "relation"}}, 0,
		map[string]schemaexecution.Table{}, &lookupPageCollector{limit: 1},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled traversal error = %#v", err)
	}
}

func TestCalculateCellsBatchSharesPathAndKeepsOrderedDuplicateSources(t *testing.T) {
	definition := schemaexecution.Table{
		PhysicalName: "lookup_rows",
		Snapshot: v2.SchemaSnapshot{TableID: "rows", Fields: []v2.FieldDefinition{
			{Identity: v2.FieldIdentity{FieldID: "links", PhysicalName: "links"}, Relation: &v2.RelationSpec{TargetTableID: "rows"}},
			{Identity: v2.FieldIdentity{FieldID: "name", PhysicalName: "name"}},
			{Identity: v2.FieldIdentity{FieldID: "score", PhysicalName: "score"}},
			{Identity: v2.FieldIdentity{FieldID: "lookup_name", PhysicalName: "f_lookup_name"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "links"}, {RelationFieldID: "links"}}, TargetFieldID: "name"}},
			{Identity: v2.FieldIdentity{FieldID: "lookup_score", PhysicalName: "f_lookup_score"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: "links"}, {RelationFieldID: "links"}}, TargetFieldID: "score"}},
		}},
	}
	collection := core.NewBaseCollection("lookup_rows")
	var records []*core.Record
	for _, id := range []string{"root", "a", "b", "one", "two"} {
		record := core.NewRecord(collection)
		record.Id = id
		record.Set("name", id)
		record.Set("score", len(id))
		records = append(records, record)
	}
	records[0].Set("links", []string{"b", "a"})
	records[1].Set("links", []string{"one", "two"})
	records[2].Set("links", []string{"two", "one"})
	got, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	cell := got["root"]["f_lookup_name"]
	if !reflect.DeepEqual(cell.Value, []any{"two", "one", "one", "two"}) || cell.ProvenanceTotal != 4 || !cell.ProvenanceTotalKnown {
		t.Fatalf("ordered duplicate projection = %#v", cell)
	}
	for i, id := range []string{"two", "one", "one", "two"} {
		if cell.Provenance[i].ItemID != id || got["root"]["f_lookup_score"].Provenance[i].ItemID != id {
			t.Fatalf("source order diverged at %d", i)
		}
	}
	selected, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, records, map[string]bool{"lookup_score": true})
	if err != nil || len(selected["root"]) != 1 || selected["root"]["f_lookup_score"].State != "ok" {
		t.Fatalf("stable field selection = %#v, %v", selected, err)
	}
}

func TestCalculateCellsBatchHonorsCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewCalculator().CalculateCellsBatch(ctx, nil, schemaexecution.Table{}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled page error = %#v", err)
	}
}

func TestLookupProjectionReadsComputedTargetsThroughBatchReader(t *testing.T) {
	definition := schemaexecution.Table{
		PhysicalName: "rows",
		Snapshot: v2.SchemaSnapshot{TableID: "rows", Fields: []v2.FieldDefinition{
			{Identity: v2.FieldIdentity{FieldID: "links", PhysicalName: "links"}, Relation: &v2.RelationSpec{TargetTableID: "rows"}},
			{
				Identity:    v2.FieldIdentity{FieldID: "fld_minute", PhysicalName: "f_minute"},
				LogicalType: v2.LogicalFormula,
				Formula:     &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1"},
			},
		}},
	}
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "lookup", PhysicalName: "f_lookup"},
		LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{
			Path:          []v2.LookupPathStep{{RelationFieldID: "links"}},
			TargetFieldID: "fld_minute",
		},
	}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, field)
	source := core.NewRecord(core.NewBaseCollection("rows"))
	source.Id = "root"
	source.Set("links", []string{"leaf"})
	leaf := core.NewRecord(source.Collection())
	leaf.Id = "leaf"
	storedEnvelope := map[string]any{
		"state": "ready", "value": "06:59",
		"version": map[string]any{
			"definitionVersion": 1, "sourceDataRevision": 1,
			"dependencyWatermark": "sha256:00",
		},
	}
	leaf.Set("f_minute", storedEnvelope)
	definitions := map[string]schemaexecution.Table{"rows": definition}
	cache := map[string]map[string]*core.Record{"rows": {"leaf": leaf}}
	project := func(ctx context.Context) ([]lookupPathValue, error) {
		cursor := lookupBatchCursor{
			fields:    []v2.FieldDefinition{field},
			source:    traversalNode{definition: definition, record: source},
			collector: lookupPageCollector{limit: 100}, values: make([][]lookupPathValue, 1),
		}
		_, _, err := cursor.advance(
			ctx, nil, definitions, cache,
			&materializationBudget{remainingBytes: lookupMaterializationBytes},
		)
		if err != nil {
			return nil, err
		}
		return cursor.values[0], nil
	}

	// Without a reader a computed target fails closed: the stored envelope
	// must never leak through a projection as a value.
	_, err := project(context.Background())
	var guardErr *formula.Error
	if !errors.As(err, &guardErr) || guardErr.Code != "formula.dependency" {
		t.Fatalf("readerless projection error = %#v", err)
	}

	// The batch reader serves only fresh computed scalars.
	fresh := formula.WithComputedSourceReader(
		context.Background(),
		func(context.Context, core.App, string, []v2.FieldDefinition, v2.FieldDefinition, *core.Record) (any, error) {
			return "07:00", nil
		},
	)
	served, err := project(fresh)
	if err != nil || len(served) != 1 || served[0].value != "07:00" {
		t.Fatalf("reader projection = %#v, %v", served, err)
	}

	// A stale computed source fails closed instead of leaking an old value.
	stale := formula.WithComputedSourceReader(
		context.Background(),
		func(context.Context, core.App, string, []v2.FieldDefinition, v2.FieldDefinition, *core.Record) (any, error) {
			return nil, &formula.Error{
				ContractVersion: formula.ContractVersion,
				Code:            "formula.dependency",
				Message:         "computed source cell is stale for this evaluation",
			}
		},
	)
	_, err = project(stale)
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.dependency" {
		t.Fatalf("stale projection error = %#v", err)
	}
}

// Standalone Lookup entry points (grid batches, paged source details) run
// without the mutation composite, so they must install the shared freshness
// reader themselves: a stale stored computed source is an explicit rejection,
// a fresh one serves its scalar.
func TestStandaloneLookupEntriesRejectStaleComputedSources(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	formulas, err := app.FindCollectionByNameOrId("vibetable_formulas")
	if err != nil {
		t.Fatal(err)
	}
	metadata := core.NewRecord(formulas)
	metadata.Set("table_id", "tbl_rows")
	metadata.Set("field_id", "fld_minute")
	metadata.Set("source", "NOW()")
	metadata.Set("language", "cel-v1")
	metadata.Set("result_type", "text")
	metadata.Set("version", 1)
	metadata.Set("status", "ready")
	if err := app.Save(metadata); err != nil {
		t.Fatal(err)
	}
	collection := core.NewBaseCollection("rows")
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	collection.Fields.Add(&core.RelationField{Name: "links", CollectionId: collection.Id, MaxSelect: 1000})
	collection.Fields.Add(&core.NumberField{Name: relatedcomputation.RowRevisionField, OnlyInt: true})
	collection.Fields.Add(&core.JSONField{Name: "f_minute"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	definition := schemaexecution.Table{
		PhysicalName: "rows",
		Snapshot: v2.SchemaSnapshot{TableID: "tbl_rows", Fields: []v2.FieldDefinition{
			{
				Identity: v2.FieldIdentity{FieldID: "links", PhysicalName: "links"},
				Relation: &v2.RelationSpec{TargetTableID: "tbl_rows"},
			},
			{
				Identity:    v2.FieldIdentity{FieldID: "fld_minute", PhysicalName: "f_minute"},
				LogicalType: v2.LogicalFormula,
				Formula:     &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1"},
			},
		}},
	}
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "lookup", PhysicalName: "f_lookup"},
		LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{
			Path:          []v2.LookupPathStep{{RelationFieldID: "links"}},
			TargetFieldID: "fld_minute",
		},
	}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, field)
	source := core.NewRecord(collection)
	source.Id = "standalone00001"
	leaf := core.NewRecord(collection)
	leaf.Set(relatedcomputation.RowRevisionField, 3)
	leaf.Set("links", []string{})
	if err := app.Save(leaf); err != nil {
		t.Fatal(err)
	}
	source.Set("links", []string{leaf.Id})
	if err := app.Save(source); err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2024, 3, 10, 6, 59, 0, 0, time.UTC)
	ctx := formula.WithEvaluationTime(context.Background(), instant)
	store := func(envelope map[string]any) {
		stored, loadErr := app.FindRecordById(collection, leaf.Id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		stored.Set("f_minute", envelope)
		if err := app.Save(stored); err != nil {
			t.Fatal(err)
		}
	}
	staleEnvelope := map[string]any{
		"state": "ready", "value": "06:59",
		"version": map[string]any{
			"definitionVersion": 1, "sourceDataRevision": 3,
			"dependencyWatermark": "sha256:stale",
		},
	}
	expectation, err := relatedcomputation.ExpectationFor(
		ctx, app, "tbl_rows", definition.Snapshot.Fields, "fld_minute", 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	freshEnvelope := map[string]any{
		"state": "ready", "value": "06:59",
		"version": map[string]any{
			"definitionVersion":   expectation.DefinitionVersion,
			"sourceDataRevision":  expectation.SourceDataRevision,
			"dependencyWatermark": expectation.DependencyWatermark,
		},
	}

	store(staleEnvelope)
	_, err = NewCalculator().CalculateCellsBatch(
		ctx, app, definition, []*core.Record{source}, nil,
	)
	var staleErr *formula.Error
	if !errors.As(err, &staleErr) || staleErr.Code != "formula.dependency" {
		t.Fatalf("stale standalone batch error = %#v", err)
	}
	_, err = NewCalculator().CalculateFieldPage(ctx, app, definition, source, field, 0, 10)
	if !errors.As(err, &staleErr) || staleErr.Code != "formula.dependency" {
		t.Fatalf("stale standalone page error = %#v", err)
	}

	store(freshEnvelope)
	cells, err := NewCalculator().CalculateCellsBatch(
		ctx, app, definition, []*core.Record{source}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cell := cells[source.Id][field.Identity.PhysicalName]; cell.State != "ok" || cell.Value != "06:59" {
		t.Fatalf("fresh standalone batch cell = %#v", cell)
	}
	page, err := NewCalculator().CalculateFieldPage(ctx, app, definition, source, field, 0, 10)
	if err != nil || page.State != "ok" || page.Value != "06:59" {
		t.Fatalf("fresh standalone page = %#v, %v", page, err)
	}
}
