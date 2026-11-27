package relatedcomputation_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
)

// sqlCounter records every statement both dbx pools run so a test can prove a
// dependency-graph entry was reused instead of read again.
type sqlCounter struct {
	mu         sync.Mutex
	statements []string
}

func (c *sqlCounter) log(_ context.Context, _ time.Duration, statement string, _ *sql.Rows, _ error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, statement)
}

func (c *sqlCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = nil
}

func (c *sqlCounter) count(substring string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, statement := range c.statements {
		if strings.Contains(statement, substring) {
			total++
		}
	}
	return total
}

// clockViewFixture serves the two policies of one transaction:
//   - tbl_clock: a same-row chain (label reads now) and a static formula;
//   - tbl_other: a lookup whose stored inputs point at tbl_clock;
//   - tbl_edge: a cel-v1 formula f with a stored same-table edge to g (inputs
//     set, tables empty).
type clockViewFixture struct {
	app         *pocketbase.PocketBase
	sql         *sqlCounter
	fields      []v2.FieldDefinition
	lookupField v2.FieldDefinition
	edges       []v2.FieldDefinition
}

func newClockViewFixture(t *testing.T) *clockViewFixture {
	t.Helper()
	app := computationTestApp(t)
	f := &clockViewFixture{app: app, sql: &sqlCounter{}}
	for _, pool := range []dbx.Builder{app.ConcurrentDB(), app.NonconcurrentDB()} {
		pool.(*dbx.DB).QueryLogFunc = f.sql.log
	}
	f.fields = []v2.FieldDefinition{
		{Identity: v2.FieldIdentity{FieldID: "fld_now", PhysicalName: "now_value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1", ResultType: v2.LogicalDateTime}},
		{Identity: v2.FieldIdentity{FieldID: "fld_label", PhysicalName: "label"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: `TEXT(now_value, "HH:mm")`, Language: "cel-v1", ResultType: v2.LogicalText}},
		{Identity: v2.FieldIdentity{FieldID: "fld_static", PhysicalName: "static_value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "7", Language: "cel-v1", ResultType: v2.LogicalNumber}},
	}
	for _, field := range f.fields {
		saveFormula(t, app, "tbl_clock", field)
	}
	f.lookupField = v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_lookup", PhysicalName: "lookup_value"}, LogicalType: v2.LogicalLookup, Lookup: &v2.LookupSpec{}}
	for index, target := range []string{"fld_label", "fld_now", "__path__"} {
		saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{"source_table_id": "tbl_other", "computed_field_id": "fld_lookup", "computed_kind": "lookup", "relation_field_id": fmt.Sprintf("fld_relation%d", index), "target_table_id": "tbl_clock", "target_field_id": target, "path_json": []any{map[string]any{"relationFieldId": "relation"}}, "definition_version": 1})
	}
	f.edges = []v2.FieldDefinition{
		{Identity: v2.FieldIdentity{FieldID: "f", PhysicalName: "edge_value"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "7", Language: "cel-v1", ResultType: v2.LogicalNumber}},
		{Identity: v2.FieldIdentity{FieldID: "g", PhysicalName: "edge_operand"}, LogicalType: v2.LogicalFormula, Formula: &v2.FormulaSpec{Source: "NOW()", Language: "cel-v1", ResultType: v2.LogicalDateTime}},
	}
	for _, field := range f.edges {
		saveFormula(t, app, "tbl_edge", field)
	}
	saveInternalRecord(t, app, "vibetable_computation_dependencies", map[string]any{"source_table_id": "tbl_edge", "computed_field_id": "f", "computed_kind": "formula", "relation_field_id": "relation", "target_table_id": "tbl_edge", "target_field_id": "g", "path_json": []any{map[string]any{"relationFieldId": "relation"}}, "definition_version": 1})
	return f
}

func saveFormula(t *testing.T, app core.App, table string, field v2.FieldDefinition) {
	t.Helper()
	saveInternalRecord(t, app, "vibetable_formulas", map[string]any{"table_id": table, "field_id": field.Identity.FieldID, "source": field.Formula.Source, "language": "cel-v1", "result_type": string(field.Formula.ResultType), "version": 1, "status": "ready"})
	saveInternalRecord(t, app, "vibetable_fields", map[string]any{"table_id": table, "field_id": field.Identity.FieldID, "physical_name": field.Identity.PhysicalName, "display_name": field.Identity.PhysicalName, "kind": "formula", "data_type": "text", "storage_type": "json", "schema_model_version": 2, "lifecycle_state": "active", "definition_v2_json": field})
}

func (f *clockViewFixture) context() context.Context {
	return formula.WithEvaluationTime(context.Background(), time.Date(2024, 1, 1, 12, 1, 59, 0, time.UTC))
}

func (f *clockViewFixture) label(ctx context.Context) (relatedcomputation.Expectation, error) {
	return relatedcomputation.ExpectationFor(ctx, f.app, "tbl_clock", f.fields, "fld_label", 2)
}

func (f *clockViewFixture) lookup(ctx context.Context) ([]formula.ClockReference, error) {
	return relatedcomputation.ClockReferencesFor(ctx, f.app, "tbl_other", []v2.FieldDefinition{f.lookupField}, "fld_lookup")
}

func (f *clockViewFixture) edge(ctx context.Context) ([]formula.ClockReference, error) {
	return relatedcomputation.ClockReferencesFor(ctx, f.app, "tbl_edge", f.edges, "f")
}

// The view stores what it proves same-row into the full cache: the description
// that follows reads nothing back for it, keeps the same answer, and the
// lookup the view refused is still resolved by the full policy on its own.
func TestClockCacheViewSharesProvenSameRowEntriesWithTheFullCache(t *testing.T) {
	f := newClockViewFixture(t)
	view := relatedcomputation.WithSameRowClockCacheView(f.context())
	scoped, err := f.label(view)
	if err != nil {
		t.Fatal(err)
	}
	f.sql.reset()
	full := relatedcomputation.WithClockCacheAfterSameRow(view)
	if refs, err := relatedcomputation.ClockReferencesFor(full, f.app, "tbl_clock", f.fields, "fld_label"); err != nil || len(refs) != 1 || refs[0].Function != "NOW" {
		t.Fatalf("described references=%#v %v", refs, err)
	}
	if described, err := f.label(full); err != nil || described != scoped {
		t.Fatalf("described expectation=%+v, want %+v, err=%v", described, scoped, err)
	}
	if reads := f.sql.count("vibetable_computation_dependencies") + f.sql.count("vibetable_fields"); reads != 0 {
		t.Fatalf("description re-read a proven same-row entry: %d statements", reads)
	}
	// The view's policy is unchanged: stored relationship edges stay refused,
	// the refusal is stored nowhere, and the full policy still resolves them.
	if _, err := f.lookup(view); err == nil || !strings.Contains(err.Error(), "not same-row") {
		t.Fatalf("same-row view followed a lookup dependency: %v", err)
	}
	f.sql.reset()
	if refs, err := f.lookup(full); err != nil || len(refs) != 1 {
		t.Fatalf("full policy after a view refusal: %#v %v", refs, err)
	}
	if f.sql.count("vibetable_computation_dependencies") == 0 {
		t.Fatal("the refused lookup leaked into the full cache")
	}
}

// A full entry is taken over by the view only without any stored edge: neither
// a related lookup (tables set) nor a same-table edge (inputs set) crosses
// into the restricted policy, while a clean entry is reused without a read.
func TestClockCacheViewRejectsLegacyRelationWithoutStoredInputs(t *testing.T) {
	f := newClockViewFixture(t)
	saveInternalRecord(t, f.app, "vibetable_formula_dependencies", map[string]any{
		"source_table_id": "tbl_clock", "formula_field_id": "fld_static",
		"relation_field_id": "fld_relation", "target_table_id": "tbl_other",
		"target_field_id": "fld_lookup", "dependency_kind": "relation",
	})
	full := relatedcomputation.WithClockCache(f.context())
	read := func(ctx context.Context) error {
		_, err := relatedcomputation.ClockReferencesFor(ctx, f.app, "tbl_clock", f.fields, "fld_static")
		return err
	}
	if err := read(full); err != nil {
		t.Fatalf("full traversal of a legacy relation: %v", err)
	}
	if err := read(relatedcomputation.WithSameRowClockCacheView(full)); err == nil || !strings.Contains(err.Error(), "not same-row") {
		t.Fatalf("same-row traversal admitted a legacy cross-table dependency: %v", err)
	}
}

func TestClockCacheViewNeverTakesStoredEdgesBackFromTheFullCache(t *testing.T) {
	f := newClockViewFixture(t)
	base := relatedcomputation.WithClockCache(f.context())
	if refs, err := f.lookup(base); err != nil || len(refs) != 1 {
		t.Fatalf("full lookup: %#v %v", refs, err)
	}
	if refs, err := f.edge(base); err != nil || len(refs) != 1 || refs[0].Function != "NOW" {
		t.Fatalf("full edge: %#v %v", refs, err)
	}
	plain, err := f.label(base)
	if err != nil {
		t.Fatal(err)
	}
	view := relatedcomputation.WithSameRowClockCacheView(base)
	f.sql.reset()
	if _, err := f.lookup(view); err == nil || !strings.Contains(err.Error(), "not same-row") {
		t.Fatalf("view accepted a lookup with stored cross-table inputs: %v", err)
	}
	if _, err := f.edge(view); err == nil || !strings.Contains(err.Error(), "not same-row") {
		t.Fatalf("view accepted a full entry with stored inputs: %v", err)
	}
	// Same-row definition versions are still read the view's own way; the
	// dependency graph of a clean entry (fields and stored edges) is not.
	f.sql.reset()
	if reused, err := f.label(view); err != nil || reused != plain {
		t.Fatalf("clean full entry was not reused: %+v vs %+v, %v", reused, plain, err)
	}
	// Same-row definition versions are still read the view's own way; the
	// dependency graph itself (fields and stored edges) is not read again.
	if reads := f.sql.count("vibetable_computation_dependencies") + f.sql.count("vibetable_fields"); reads != 0 {
		t.Fatalf("reusing a clean entry read %d statements", reads)
	}
	// The description keeps the very cache the view was opened over.
	same := relatedcomputation.WithClockCacheAfterSameRow(view)
	f.sql.reset()
	if refs, err := f.lookup(same); err != nil || len(refs) != 1 || f.sql.count("vibetable_computation_dependencies") != 0 {
		t.Fatalf("description lost the transaction cache: %#v %v", refs, err)
	}
	// A view opened over a legacy same-row cache starts its own full cache.
	legacy := relatedcomputation.WithSameRowClockCache(f.context())
	if _, err := f.label(legacy); err != nil {
		t.Fatal(err)
	}
	fresh := relatedcomputation.WithSameRowClockCacheView(legacy)
	f.sql.reset()
	if _, err := f.label(fresh); err != nil || f.sql.count("vibetable_computation_dependencies") == 0 {
		t.Fatalf("view over a legacy cache inherited its entries: %v", err)
	}
}

// Several projections of one transaction describe over the same full cache,
// and every caller that is not the snapshot view keeps a fresh cache.
func TestClockCacheAfterSameRowSharesOneCacheAcrossProjectionsAndLeavesOthersFresh(t *testing.T) {
	f := newClockViewFixture(t)
	view := relatedcomputation.WithSameRowClockCacheView(f.context())
	if _, err := f.label(view); err != nil {
		t.Fatal(err)
	}
	first := relatedcomputation.WithClockCacheAfterSameRow(view)
	if _, err := relatedcomputation.ClockReferencesFor(first, f.app, "tbl_clock", f.fields, "fld_label"); err != nil {
		t.Fatal(err)
	}
	second := relatedcomputation.WithClockCacheAfterSameRow(view)
	f.sql.reset()
	if refs, err := relatedcomputation.ClockReferencesFor(second, f.app, "tbl_clock", f.fields, "fld_static"); err != nil || len(refs) != 0 {
		t.Fatalf("second projection: %#v %v", refs, err)
	}
	if f.sql.count("vibetable_fields") != 0 || f.sql.count("vibetable_computation_dependencies") != 1 {
		t.Fatalf("second projection re-described the table: fields=%d dependencies=%d", f.sql.count("vibetable_fields"), f.sql.count("vibetable_computation_dependencies"))
	}
	third := relatedcomputation.WithClockCacheAfterSameRow(view)
	f.sql.reset()
	if refs, err := relatedcomputation.ClockReferencesFor(third, f.app, "tbl_clock", f.fields, "fld_static"); err != nil || len(refs) != 0 || f.sql.count("vibetable_computation_dependencies")+f.sql.count("vibetable_fields") != 0 {
		t.Fatalf("third projection re-read a resolved entry: %#v %v", refs, err)
	}
	// Plain and legacy callers: one fresh cache per description, as before.
	for name, ctx := range map[string]context.Context{
		"plain":  f.context(),
		"legacy": relatedcomputation.WithSameRowClockCache(f.context()),
	} {
		if _, err := f.label(ctx); err != nil && name == "legacy" {
			t.Fatal(err)
		}
		a := relatedcomputation.WithClockCacheAfterSameRow(ctx)
		if _, err := f.label(a); err != nil {
			t.Fatal(err)
		}
		b := relatedcomputation.WithClockCacheAfterSameRow(ctx)
		f.sql.reset()
		if _, err := f.label(b); err != nil || f.sql.count("vibetable_computation_dependencies") == 0 {
			t.Fatalf("%s description shared a cache: %v", name, err)
		}
	}
}
