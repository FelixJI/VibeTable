package integration_test

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
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestConditionalLookupWithoutRelationPreservesFullValuesAndSourcePages(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	orders := createV2IntegrationTable(t, ctx, app, "订单", "condition_orders")
	materials := createV2IntegrationTable(t, ctx, app, "物料", "condition_materials")
	code := createV2IntegrationField(t, ctx, app, orders.TableID, fieldDraftForIntegration(t, v2.LogicalText, "编码(当前)"), "condition_order_code")
	sourceCode := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "编码\"来源\""), "condition_source_code")
	enabled := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalBool, "启用"), "condition_enabled")
	title := createV2IntegrationField(t, ctx, app, materials.TableID, fieldDraftForIntegration(t, v2.LogicalText, "名称"), "condition_title")
	draft := fieldDraftForIntegration(t, v2.LogicalLookup, "条件结果")
	draft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{}, TargetFieldID: title.FieldID, Condition: &v2.LookupCondition{
		SourceTableID: materials.TableID, Match: "all", Rules: []v2.LookupConditionRule{
			{SourceFieldID: sourceCode.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "field", FieldID: code.FieldID}},
			{SourceFieldID: enabled.FieldID, Operator: "eq", Operand: &v2.LookupOperand{Kind: "constant", Value: true}},
		},
	}}
	lookupField := createV2IntegrationField(t, ctx, app, orders.TableID, draft, "condition_lookup")
	set := func(record *core.Record, field v2.ApplyReceipt, value any) {
		record.Set(field.Definition.Identity.PhysicalName, value)
		record.Set(field.Definition.Value.Presence.PhysicalName, true)
	}
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(orderCollection)
	set(row, code, "中文")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	definition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	calculator := lookup.NewCalculator()
	cells, err := calculator.CalculateCells(ctx, app, definition, row)
	if err != nil {
		t.Fatal(err)
	}
	cell := cells[lookupField.Definition.Identity.PhysicalName]
	if !reflect.DeepEqual(cell.Value, []any{}) || cell.ProvenanceTotal != 0 {
		t.Fatalf("zero match = %#v", cell)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 151 {
		record := core.NewRecord(sourceCollection)
		record.Id = fmt.Sprintf("material%07d", i)
		set(record, sourceCode, "中文")
		set(record, enabled, true)
		set(record, title, fmt.Sprintf("名称%d", i%2))
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	cells, err = calculator.CalculateCells(ctx, app, definition, row)
	if err != nil {
		t.Fatal(err)
	}
	cell = cells[lookupField.Definition.Identity.PhysicalName]
	if len(cell.Value.([]any)) != 151 || len(cell.Provenance) != 100 || cell.ProvenanceTotal != 151 || !cell.ProvenanceHasMore {
		t.Fatalf("full values/page = %#v", cell)
	}
	second, err := calculator.CalculateFieldPage(ctx, app, definition, row, *lookupField.Definition, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Value.([]any)) != 151 || len(second.Provenance) != 51 || second.ProvenanceHasMore || second.Provenance[0].ItemID != "material0000100" {
		t.Fatalf("second page = %#v", second)
	}
	distinct := *lookupField.Definition
	distinct.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{}, TargetFieldID: title.FieldID, Condition: &v2.LookupCondition{
		SourceTableID: materials.TableID, Match: "all", Rules: draft.Lookup.Condition.Rules, Distinct: true,
	}}
	deduped, err := calculator.CalculateFieldPage(ctx, app, definition, row, distinct, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deduped.Value, []any{"名称0", "名称1"}) || deduped.ProvenanceTotal != 151 {
		t.Fatalf("distinct lost match provenance: %#v", deduped)
	}
	// Sixty-five distinct operands cross the 64-group boundary without a scan per row.
	currentRows := make([]*core.Record, 65)
	for i := range currentRows {
		currentRows[i] = core.NewRecord(orderCollection)
		currentRows[i].Id = fmt.Sprintf("current%08d", i)
		set(currentRows[i], code, fmt.Sprintf("other%d", i))
	}
	set(currentRows[0], code, "中文")
	matchQueries := 0
	database := app.ConcurrentDB().(*dbx.DB)
	previous := database.QueryLogFunc
	database.QueryLogFunc = func(ctx context.Context, elapsed time.Duration, statement string, rows *sql.Rows, err error) {
		if strings.Contains(statement, " AS matches FROM ") {
			matchQueries++
		}
		if previous != nil {
			previous(ctx, elapsed, statement, rows, err)
		}
	}
	defer func() { database.QueryLogFunc = previous }()
	batch, err := calculator.CalculateCellsBatch(ctx, app, definition, currentRows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if matchQueries != 2 || len(batch[currentRows[0].Id][lookupField.Definition.Identity.PhysicalName].Value.([]any)) != 151 {
		t.Fatalf("bounded match groups: queries=%d, error=%v", matchQueries, err)
	}
	for _, current := range currentRows[1:] {
		if len(batch[current.Id][lookupField.Definition.Identity.PhysicalName].Value.([]any)) != 0 {
			t.Fatal("unmatched row gained values")
		}
	}
	// Shared source scans must not hide the serialized cost of repeated output cells.
	sourceCollection.Fields.GetByName(title.Definition.Identity.PhysicalName).(*core.TextField).Max = 2 << 20
	if err := app.Save(sourceCollection); err != nil {
		t.Fatal(err)
	}
	large, err := app.FindRecordById(sourceCollection, "material0000000")
	if err != nil {
		t.Fatal(err)
	}
	set(large, title, strings.Repeat("x", 1<<20))
	if err := app.Save(large); err != nil {
		t.Fatal(err)
	}
	if _, err := calculator.CalculateCells(ctx, app, definition, row); err != nil {
		t.Fatal(err)
	}
	for _, current := range currentRows {
		set(current, code, "中文")
	}
	_, err = calculator.CalculateCellsBatch(ctx, app, definition, currentRows, nil)
	var budgetError *mutation.ProductError
	if !errors.As(err, &budgetError) || budgetError.Code != "lookup.value.too_expensive" {
		t.Fatalf("repeated cells bypassed the materialization budget: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := calculator.CalculateCells(cancelled, app, definition, row); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
}
