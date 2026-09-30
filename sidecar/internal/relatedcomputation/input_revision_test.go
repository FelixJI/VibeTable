package relatedcomputation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestInputRevisionPreservesSameRowAndTransactionBoundFreshness(t *testing.T) {
	app := computationTestApp(t)
	ctx := relatedcomputation.WithClockCache(context.Background())
	saveInternalRecord(t, app, "vibetable_tables", map[string]any{"table_id": "tbl_source", "collection_id": "source", "physical_name": "source", "display_name": "Source", "kind": "base", "schema_revision": 1, "data_revision": 9, "archive_policy": `{"mode":"none"}`})
	lookup := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup"}, LogicalType: v2.LogicalLookup,
		Lookup: &v2.LookupSpec{TargetFieldID: "fld_amount", Condition: &v2.LookupCondition{SourceTableID: "tbl_source"}}}
	for _, edge := range []struct{ table, field string }{{"tbl_source", "fld_amount"}, {"tbl_source", "__path__"}, {"tbl_current", "fld_key"}} {
		saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{"source_table_id": "tbl_current", "computed_field_id": "fld_lookup", "computed_kind": "lookup", "target_table_id": edge.table, "target_field_id": edge.field, "path_json": lookup.Lookup, "definition_version": 1, relatedcomputation.InputRevisionField: 9})
	}
	fields := []v2.FieldDefinition{{Identity: v2.FieldIdentity{FieldID: "fld_amount", PhysicalName: "amount"}}, {Identity: v2.FieldIdentity{FieldID: "fld_note", PhysicalName: "note"}}}
	check := func(revision int64) {
		t.Helper()
		got, err := relatedcomputation.ExpectationFor(ctx, app, "tbl_current", []v2.FieldDefinition{lookup}, "fld_lookup", 3)
		if err != nil || got.SourceDataRevision != 3 || got.DependencyWatermark != relatedcomputation.Watermark(map[string]int64{"tbl_source": revision}) {
			t.Fatalf("expectation revision %d: %+v, %v", revision, got, err)
		}
	}
	check(9)
	if err := relatedcomputation.AdvanceInputRevisions(ctx, app, "tbl_source", fields, map[string]any{"note": "a"}, map[string]any{"note": "b"}, "update", 10); err != nil {
		t.Fatal(err)
	}
	check(9)
	if err := relatedcomputation.AdvanceInputRevisions(ctx, app, "tbl_source", fields, map[string]any{"amount": 1}, map[string]any{"amount": 2}, "update", 10); err != nil {
		t.Fatal(err)
	}
	check(10) // Reused graph cache must not cache the numeric revision.
	rollback := errors.New("rollback")
	if err := app.RunInTransaction(func(tx core.App) error {
		if err := relatedcomputation.AdvanceInputRevisions(ctx, tx, "tbl_source", fields, map[string]any{"amount": 2}, map[string]any{"amount": 3}, "update", 11); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	check(10)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := relatedcomputation.AdvanceInputRevisions(canceled, app, "tbl_source", fields, nil, nil, "insert", 11); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Corrupt metadata must fail closed instead of accepting a stale scalar.
	if _, err := app.DB().NewQuery("UPDATE vibetable_computation_dependencies SET input_revision=-1 WHERE target_field_id='fld_amount'").Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := relatedcomputation.ExpectationFor(ctx, app, "tbl_current", []v2.FieldDefinition{lookup}, "fld_lookup", 3); err == nil {
		t.Fatal("negative input revision was accepted")
	}
	// The same corrupted edge must also fail the business write that would
	// advance it: AdvanceInputRevisions runs inside the write transaction.
	if err := relatedcomputation.AdvanceInputRevisions(ctx, app, "tbl_source", fields, map[string]any{"amount": 3}, map[string]any{"amount": 4}, "update", 11); err == nil {
		t.Fatal("business write advanced a corrupted dependency input revision")
	}
}

// TableInputRevision is the seed used by the schema API whenever it writes a
// new dependency edge. Pin its read contract at this package boundary.
func TestTableInputRevisionSeedsBusinessRevisionAndFailsClosed(t *testing.T) {
	app := computationTestApp(t)
	saveInternalRecord(t, app, "vibetable_tables", map[string]any{"table_id": "tbl_dep", "collection_id": "dep", "physical_name": "dep", "display_name": "Dep", "kind": "base", "schema_revision": 1, "data_revision": 9, "clock_revision": 2, "archive_policy": `{"mode":"none"}`})
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, item := range []struct {
		name    string
		ctx     context.Context
		table   string
		corrupt bool
		want    int64
		fails   bool
	}{
		{"business revision excludes clock-only transactions", ctx, "tbl_dep", false, 7, false},
		{"cancelled context", canceled, "tbl_dep", false, 0, true},
		{"missing table metadata", ctx, "tbl_missing", false, 0, true},
		{"corrupt counter fails closed", ctx, "tbl_dep", true, 0, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			if item.corrupt {
				if _, err := app.DB().NewQuery("UPDATE vibetable_tables SET clock_revision=1.5 WHERE table_id='tbl_dep'").Execute(); err != nil {
					t.Fatal(err)
				}
			}
			got, err := relatedcomputation.TableInputRevision(item.ctx, app, item.table)
			if item.fails {
				if err == nil {
					t.Fatalf("%s was accepted: revision %d", item.name, got)
				}
				return
			}
			if err != nil || got != item.want {
				t.Fatalf("%s: revision %d, %v; want %d", item.name, got, err, item.want)
			}
		})
	}
}

func TestInputChangesSeparateMembershipRelationAndComputedVersions(t *testing.T) {
	fields := []v2.FieldDefinition{
		{Identity: v2.FieldIdentity{FieldID: "fld_note", PhysicalName: "note"}},
		{Identity: v2.FieldIdentity{FieldID: "fld_link", PhysicalName: "link"}, Relation: &v2.RelationSpec{}},
		{Identity: v2.FieldIdentity{FieldID: "fld_total", PhysicalName: "total"}},
	}
	path := core.NewRecord(core.NewBaseCollection("edges"))
	path.Set("target_field_id", "__path__")
	before := map[string]any{"note": "a", "link": []string{"a"}, "total": relatedcomputation.Ready(3.0, relatedcomputation.CellVersion{1, 1, "old"})}
	after := map[string]any{"note": "b", "link": []string{"a"}, "total": relatedcomputation.Ready(3.0, relatedcomputation.CellVersion{1, 2, "new"})}
	changed := relatedcomputation.ChangedInputs(fields, before, after, "update")
	if !changed["fld_note"] || changed["fld_total"] || relatedcomputation.InputAffected(path, changed) {
		t.Fatalf("note/version-only update=%v", changed)
	}
	// A mixed insert/update event is classified from each operation, not its
	// aggregate update label. Even an empty inserted row changes COUNT(TABLE).
	membership := relatedcomputation.ChangedInputs(fields, nil, map[string]any{}, "insert")
	if !relatedcomputation.InputAffected(path, membership) {
		t.Fatal("empty insert did not invalidate membership")
	}
	for _, operation := range []string{"delete", "archive", "restore"} {
		if !relatedcomputation.InputAffected(path, relatedcomputation.ChangedInputs(fields, before, after, operation)) {
			t.Fatal(operation)
		}
	}
	after["link"] = []string{"b"}
	changed = relatedcomputation.ChangedInputs(fields, before, after, "update")
	if relatedcomputation.InputAffected(path, changed) {
		t.Fatal("relation edit changed whole-table membership")
	}
	path.Set("relation_field_id", "fld_root_link")
	path.Set("path_json", []v2.LookupPathStep{{RelationFieldID: "fld_root_link"}, {RelationFieldID: "fld_link"}})
	if !relatedcomputation.InputAffected(path, changed) {
		t.Fatal("relation-hop edit was ignored")
	}
	path.Set("path_json", []v2.LookupPathStep{{RelationFieldID: "fld_root_link"}, {RelationFieldID: "fld_another_link"}})
	if relatedcomputation.InputAffected(path, changed) {
		t.Fatal("unrelated relation edit invalidated the path")
	}
	path.Set("path_json", "corrupt")
	if !relatedcomputation.InputAffected(path, changed) {
		t.Fatal("corrupt stored relation path was treated as unaffected")
	}
}
