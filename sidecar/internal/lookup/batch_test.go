package lookup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func batchCursorFixture(pathLength int) (schemaexecution.Table, *core.Record, v2.FieldDefinition) {
	definition := schemaexecution.Table{
		PhysicalName: "rows",
		Snapshot: v2.SchemaSnapshot{TableID: "rows", Fields: []v2.FieldDefinition{
			{Identity: v2.FieldIdentity{FieldID: "links", PhysicalName: "links"}, Relation: &v2.RelationSpec{TargetTableID: "rows"}},
			{Identity: v2.FieldIdentity{FieldID: "name", PhysicalName: "name"}},
		}},
	}
	field := v2.FieldDefinition{
		Identity:    v2.FieldIdentity{FieldID: "lookup", PhysicalName: "f_lookup"},
		LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{TargetFieldID: "name"},
	}
	for range pathLength {
		field.Lookup.Path = append(field.Lookup.Path, v2.LookupPathStep{RelationFieldID: "links"})
	}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, field)
	record := core.NewRecord(core.NewBaseCollection("rows"))
	record.Id = "root"
	return definition, record, field
}

func TestBatchCursorDefersMissingLaterBranchAndDoesNotReadTerminalRowsBeyondPage(t *testing.T) {
	definition, source, field := batchCursorFixture(2)
	source.Set("links", []string{"first", "unvisited", "later"})
	cursor := lookupBatchCursor{
		fields:    []v2.FieldDefinition{field},
		source:    traversalNode{definition: definition, record: source},
		collector: lookupPageCollector{limit: 100}, values: make([][]lookupPathValue, 1),
	}
	definitions := map[string]schemaexecution.Table{"rows": definition}
	cache := map[string]map[string]*core.Record{"rows": {"unvisited": nil}}
	budget := materializationBudget{remainingBytes: lookupMaterializationBytes}
	_, ids, err := cursor.advance(context.Background(), nil, definitions, cache, &budget)
	if err != nil || !reflect.DeepEqual(ids, []string{"first", "later"}) {
		t.Fatalf("intermediate frontier = %v, %v", ids, err)
	}
	first := core.NewRecord(source.Collection())
	first.Id = "first"
	links := make([]string, 101)
	for index := range links {
		links[index] = "leaf"
	}
	links[100] = "unvisited_leaf"
	first.Set("links", links)
	cache["rows"]["first"] = first
	_, ids, err = cursor.advance(context.Background(), nil, definitions, cache, &budget)
	if err != nil || len(ids) != 100 {
		t.Fatalf("terminal frontier = %v, %v", ids, err)
	}
	for _, id := range ids {
		if id != "leaf" {
			t.Fatalf("loaded beyond the visible page: %s", id)
		}
	}
	leaf := core.NewRecord(source.Collection())
	leaf.Id = "leaf"
	leaf.Set("name", "shown")
	cache["rows"]["leaf"] = leaf
	_, ids, err = cursor.advance(context.Background(), nil, definitions, cache, &budget)
	if err != nil || len(ids) != 0 || !cursor.complete || !cursor.stopped ||
		len(cursor.values[0]) != 100 || cursor.collector.total != 101 {
		t.Fatalf("completed cursor = %#v, pending=%v, err=%v", cursor, ids, err)
	}
}

func TestBatchCursorBoundsIntermediateFrontier(t *testing.T) {
	definition, source, field := batchCursorFixture(2)
	ids := make([]string, 10_000)
	for index := range ids {
		ids[index] = fmt.Sprintf("middle%d", index)
	}
	source.Set("links", ids)
	cursor := lookupBatchCursor{
		fields: []v2.FieldDefinition{field}, source: traversalNode{definition: definition, record: source},
		collector: lookupPageCollector{limit: 100}, values: make([][]lookupPathValue, 1),
	}
	_, pending, err := cursor.advance(context.Background(), nil,
		map[string]schemaexecution.Table{"rows": definition}, map[string]map[string]*core.Record{},
		&materializationBudget{remainingBytes: lookupMaterializationBytes})
	if err != nil || !reflect.DeepEqual(pending, ids[:lookupTraversalBatch]) {
		t.Fatalf("bounded frontier = %v, %v", pending, err)
	}
}

func TestBatchInvalidTargetSchemaRemainsAnError(t *testing.T) {
	definition, source, field := batchCursorFixture(1)
	invalid := field
	invalid.Identity = v2.FieldIdentity{FieldID: "invalid", PhysicalName: "f_invalid"}
	invalid.Lookup = &v2.LookupSpec{Path: field.Lookup.Path, TargetFieldID: "removed"}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, invalid)
	leaf := core.NewRecord(source.Collection())
	leaf.Id = "leaf"
	leaf.Set("name", "shown")
	source.Set("links", []string{"leaf"})
	_, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, []*core.Record{source, leaf}, nil)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.lookup.schema_invalid" {
		t.Fatalf("target schema error was reclassified as missing source: %v", err)
	}
}

func TestCalculateCellsBatchBatchesSparseFrontiersAtEveryHop(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	collection := core.NewBaseCollection("rows")
	collection.Fields.Add(&core.TextField{Name: "name"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	collection.Fields.Add(&core.RelationField{Name: "links", CollectionId: collection.Id, MaxSelect: 1000})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	var leaves []*core.Record
	for index := range 100 {
		record := core.NewRecord(collection)
		record.Set("name", fmt.Sprintf("leaf %d", index))
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		leaves = append(leaves, record)
	}
	ids := make([]string, len(leaves))
	for index, leaf := range leaves {
		ids[index] = leaf.Id
	}
	var firstLevelIDs []string
	// Three sparse intermediate levels ensure batching is not only a special
	// case for direct paths or the first hop.
	for level := range 3 {
		for index, id := range ids {
			record := core.NewRecord(collection)
			record.Set("links", []string{id})
			if err := app.Save(record); err != nil {
				t.Fatal(err)
			}
			ids[index] = record.Id
		}
		if level == 0 {
			firstLevelIDs = append([]string{}, ids...)
		}
	}
	definition, _, field := batchCursorFixture(4)
	secondField := field
	secondField.Identity = v2.FieldIdentity{FieldID: "lookup_2", PhysicalName: "f_lookup_2"}
	definition.Snapshot.Fields = append(definition.Snapshot.Fields, secondField)
	var sources []*core.Record
	for index := range 16 {
		source := core.NewRecord(collection)
		source.Id = fmt.Sprintf("source%09d", index)
		source.Set("links", ids)
		sources = append(sources, source)
	}
	reads := 0
	var cancelAfterRead context.CancelFunc
	database := app.ConcurrentDB().(*dbx.DB)
	previous := database.QueryLogFunc
	database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
		if strings.Contains(statement, "FROM `rows`") || strings.Contains(statement, "FROM \"rows\"") {
			reads++
			if cancelAfterRead != nil {
				cancelAfterRead()
			}
		}
		if previous != nil {
			previous(ctx, elapsed, statement, rows, err)
		}
	}
	defer func() { database.QueryLogFunc = previous }()
	for _, sourceCount := range []int{1, 16} {
		reads = 0
		result, err := NewCalculator().CalculateCellsBatch(context.Background(), app, definition, sources[:sourceCount], nil)
		if err != nil {
			t.Fatal(err)
		}
		if reads != 4 {
			t.Fatalf("%d sources × 2 fields × 4 hops: SELECTs = %d, want 4 (one per frontier)", sourceCount, reads)
		}
		for _, source := range sources[:sourceCount] {
			for _, name := range []string{"f_lookup", "f_lookup_2"} {
				cell := result[source.Id][name]
				if len(cell.Provenance) != 100 || cell.ProvenanceHasMore || !cell.ProvenanceTotalKnown {
					t.Fatalf("sparse path page = %#v", cell)
				}
				for index, leaf := range leaves {
					if cell.Provenance[index].ItemID != leaf.Id || cell.Provenance[index].Value != fmt.Sprintf("leaf %d", index) {
						t.Fatalf("sparse path source %d = %#v", index, cell.Provenance[index])
					}
				}
			}
		}
	}
	t.Run("details_offset_and_sparse_frontier", func(t *testing.T) {
		detailDefinition, _, detailField := batchCursorFixture(2)
		source := core.NewRecord(collection)
		source.Id = "detail_source"
		// Repeated paths must remain visible even though their record reads
		// are deduplicated. SetRaw preserves this provider-level fixture.
		source.SetRaw("links", append(append([]string{}, firstLevelIDs...), firstLevelIDs...))
		for _, testCase := range []struct {
			offset, limit, total, reads int
			known                       bool
		}{
			{offset: 0, limit: 100, total: 101, reads: 2},
			{offset: 25, limit: 50, total: 76, reads: 2},
			{offset: 100, limit: 100, total: 200, reads: 2, known: true},
			{offset: 90, limit: 20, total: 111, reads: 2},
			{offset: 0, limit: 500, total: 200, reads: 2, known: true},
			{offset: 150, limit: 100, total: 200, reads: 2, known: true},
			{offset: 200, limit: 100, total: 200, reads: 1, known: true},
		} {
			reads = 0
			cell, err := NewCalculator().CalculateFieldPage(context.Background(), app, detailDefinition, source, detailField, testCase.offset, testCase.limit)
			if err != nil {
				t.Fatal(err)
			}
			if reads != testCase.reads || len(cell.Provenance) != min(testCase.limit, 200-testCase.offset) ||
				cell.ProvenanceOffset != testCase.offset || cell.ProvenanceLimit != testCase.limit ||
				cell.ProvenanceTotal != testCase.total || cell.ProvenanceTotalKnown != testCase.known ||
				cell.ProvenanceHasMore == testCase.known {
				t.Fatalf("details offset=%d limit=%d: SELECTs=%d cell=%#v", testCase.offset, testCase.limit, reads, cell)
			}
			for index, provenance := range cell.Provenance {
				expected := (testCase.offset + index) % len(leaves)
				if provenance.ItemID != leaves[expected].Id || provenance.Value != fmt.Sprintf("leaf %d", expected) {
					t.Fatalf("details order at %d = %#v", index, provenance)
				}
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelAfterRead = cancel
		defer func() { cancelAfterRead = nil }()
		reads = 0
		_, err := NewCalculator().CalculateFieldPage(ctx, app, detailDefinition, source, detailField, 100, 100)
		if !errors.Is(err, context.Canceled) || reads != 1 {
			t.Fatalf("details cancellation = %v, SELECTs=%d", err, reads)
		}
	})
	t.Run("details_checks_read_budget_on_intermediate_hop", func(t *testing.T) {
		collection.Fields.Add(&core.TextField{Name: "padding", Max: lookupMaterializationBytes + 1})
		if err := app.Save(collection); err != nil {
			t.Fatal(err)
		}
		middle, err := app.FindRecordById(collection, firstLevelIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		middle.Set("padding", strings.Repeat("x", lookupMaterializationBytes))
		if err := app.Save(middle); err != nil {
			t.Fatal(err)
		}
		detailDefinition, _, detailField := batchCursorFixture(2)
		source := core.NewRecord(collection)
		source.Id = "budget_source"
		source.Set("links", []string{middle.Id})
		reads = 0
		_, err = NewCalculator().CalculateFieldPage(context.Background(), app, detailDefinition, source, detailField, 0, 1)
		var productErr *mutation.ProductError
		if !errors.As(err, &productErr) || productErr.Code != "lookup.value.too_expensive" || reads != 1 {
			t.Fatalf("intermediate read budget = %v, SELECTs=%d", err, reads)
		}
	})
}

func TestCalculateFieldPageEnforcesMaterializationBudget(t *testing.T) {
	definition, source, field := batchCursorFixture(1)
	source.Set("links", []string{source.Id})
	source.Set("name", strings.Repeat("x", lookupMaterializationBytes))
	_, err := NewCalculator().CalculateFieldPage(context.Background(), nil, definition, source, field, 0, 1)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "lookup.value.too_expensive" {
		t.Fatalf("details materialization budget = %v", err)
	}
}

func TestBatchCursorEnforcesValueBudgetAndCancellationBetweenFrontiers(t *testing.T) {
	for _, cancelBeforeResume := range []bool{false, true} {
		definition, source, field := batchCursorFixture(1)
		source.Set("links", []string{"leaf"})
		cursor := lookupBatchCursor{
			fields:    []v2.FieldDefinition{field},
			source:    traversalNode{definition: definition, record: source},
			collector: lookupPageCollector{limit: 100}, values: make([][]lookupPathValue, 1),
		}
		definitions := map[string]schemaexecution.Table{"rows": definition}
		cache := map[string]map[string]*core.Record{"rows": {}}
		budget := materializationBudget{remainingBytes: 8}
		ctx, cancel := context.WithCancel(context.Background())
		_, ids, err := cursor.advance(ctx, nil, definitions, cache, &budget)
		if err != nil || !reflect.DeepEqual(ids, []string{"leaf"}) {
			t.Fatalf("pending frontier = %v, %v", ids, err)
		}
		leaf := core.NewRecord(source.Collection())
		leaf.Id = "leaf"
		leaf.Set("name", "1234567") // JSON encoding consumes 9 bytes.
		cache["rows"]["leaf"] = leaf
		if cancelBeforeResume {
			cancel()
		}
		_, _, err = cursor.advance(ctx, nil, definitions, cache, &budget)
		cancel()
		var productErr *mutation.ProductError
		if cancelBeforeResume {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}
		} else if !errors.As(err, &productErr) || productErr.Code != "lookup.value.too_expensive" {
			t.Fatalf("byte budget error = %v", err)
		}
	}
}

func TestBatchDirectPageHasKnownTotalAndSkipsUnrequestedSources(t *testing.T) {
	definition, source, _ := batchCursorFixture(1)
	leaf := core.NewRecord(source.Collection())
	leaf.Id = "leaf"
	leaf.Set("name", "shown")
	links := make([]string, 101)
	for index := range links {
		links[index] = "leaf"
	}
	links[100] = "outside_page"
	source.Set("links", links)
	result, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, []*core.Record{source, leaf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cell := result[source.Id]["f_lookup"]
	if len(cell.Provenance) != 100 || cell.ProvenanceTotal != 101 || !cell.ProvenanceTotalKnown || !cell.ProvenanceHasMore {
		t.Fatalf("direct page = %#v", cell)
	}
}

func TestCalculateCellsBatchEmptySelectionSkipsInvalidLookup(t *testing.T) {
	definition, source, _ := batchCursorFixture(1)
	definition.Snapshot.Fields[len(definition.Snapshot.Fields)-1].Lookup.Path = nil
	result, err := NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, []*core.Record{source}, map[string]bool{})
	if err != nil || len(result) != 1 || len(result[source.Id]) != 0 {
		t.Fatalf("empty projection = %#v, %v", result, err)
	}
	_, err = NewCalculator().CalculateCellsBatch(context.Background(), nil, definition, []*core.Record{source}, nil)
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "mutation.lookup.schema_invalid" {
		t.Fatalf("all-fields projection must reject invalid path: %v", err)
	}
}
