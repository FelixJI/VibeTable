package integration_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

// planConditionalLookupFieldChange plans an update or convert against an
// existing field, mirroring the production planner wiring used by the shared
// create helpers.
func planConditionalLookupFieldChange(
	t *testing.T,
	ctx context.Context,
	app core.App,
	action v2.ChangeAction,
	tableID string,
	before v2.FieldDefinition,
	target v2.LogicalType,
	displayName string,
) (v2.FieldChangePlan, error) {
	t.Helper()
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	draft := fieldDraftForIntegration(t, target, displayName)
	return planner.Plan(ctx, v2.FieldChangeIntent{
		Action: action, TableID: tableID, FieldID: before.Identity.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft,
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
}

func applyConditionalLookupFieldPlan(
	t *testing.T,
	ctx context.Context,
	app core.App,
	plan v2.FieldChangePlan,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	executor := fieldchange.NewExecutor(app, fieldchange.NewPocketBasePlanStore(app))
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: operationID, Actor: v2.Actor{ID: "local-user", Kind: "user"},
		Confirmations: plan.Confirmations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

// conditionalLookupEdges snapshots the conditional-lookup dependency graph a
// fieldchange plan relies on, keyed by target table and stable field ID.
func conditionalLookupEdges(
	t *testing.T,
	app core.App,
	sourceTableID string,
) map[string]map[string]bool {
	t.Helper()
	records, err := app.FindRecordsByFilter(
		"vibetable_computation_dependencies",
		"source_table_id={:table} && computed_kind={:kind}",
		"", 0, 0,
		dbx.Params{"table": sourceTableID, "kind": "lookup"},
	)
	if err != nil {
		t.Fatal(err)
	}
	edges := map[string]map[string]bool{}
	for _, record := range records {
		// Conditional lookups carry an empty relation_field_id; path lookups
		// always name their first hop.
		if record.GetString("relation_field_id") != "" {
			continue
		}
		tableID := record.GetString("target_table_id")
		if edges[tableID] == nil {
			edges[tableID] = map[string]bool{}
		}
		edges[tableID][record.GetString("target_field_id")] = true
	}
	return edges
}

func setConditionalLookupValue(record *core.Record, field v2.ApplyReceipt, value any) {
	record.Set(field.Definition.Identity.PhysicalName, value)
	record.Set(field.Definition.Value.Presence.PhysicalName, true)
}

// TestConditionalLookupFieldTypeChangesBlockedButRenameStaysComputable pins the
// fieldchange contract around conditional-lookup condition fields: converting
// either the source comparison field or the current operand field is blocked by
// the plan's dependency diagnostics, while display-name renames stay applicable
// and the condition keeps computing through stable field IDs.
func TestConditionalLookupFieldTypeChangesBlockedButRenameStaysComputable(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()

	orders := createV2IntegrationTable(t, ctx, app, "当前订单", "cls_orders")
	materials := createV2IntegrationTable(t, ctx, app, "来源物料", "cls_materials")
	orderCode := createV2IntegrationField(
		t, ctx, app, orders.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "订单编码"), "cls_order_code",
	)
	sourceCode := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料编码"), "cls_source_code",
	)
	sourceTitle := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "物料名称"), "cls_source_title",
	)
	unrelated := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "无关字段"), "cls_unrelated",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "条件结果")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: sourceTitle.FieldID,
		Condition: &v2.LookupCondition{
			SourceTableID: materials.TableID, Match: "all",
			Rules: []v2.LookupConditionRule{{
				SourceFieldID: sourceCode.FieldID, Operator: "eq",
				Operand: &v2.LookupOperand{Kind: "field", FieldID: orderCode.FieldID},
			}},
		},
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, orders.TableID, lookupDraft, "cls_lookup",
	)

	// The dependency graph must cover both sides of the condition before any
	// plan can be trusted to block on it.
	edges := conditionalLookupEdges(t, app, orders.TableID)
	expected := map[string]map[string]bool{
		materials.TableID: {sourceCode.FieldID: true, sourceTitle.FieldID: true, "__path__": true},
		orders.TableID:    {orderCode.FieldID: true},
	}
	if !reflect.DeepEqual(edges, expected) {
		t.Fatalf("conditional dependency edges = %#v, want %#v", edges, expected)
	}

	// Converting the source comparison field or the current operand field is
	// blocked by the plan, not merely by the executor.
	for _, blocked := range []struct {
		name  string
		table v2IntegrationTable
		field v2.ApplyReceipt
	}{
		{"source comparison field", materials, sourceCode},
		{"current operand field", orders, orderCode},
	} {
		plan, err := planConditionalLookupFieldChange(
			t, ctx, app, v2.ActionConvert, blocked.table.TableID,
			*blocked.field.Definition, v2.LogicalEmail, "转换后",
		)
		if err != nil {
			t.Fatalf("%s: convert plan errored instead of blocking: %v", blocked.name, err)
		}
		if plan.CanApply {
			t.Fatalf("%s: convert plan stayed applicable: %#v", blocked.name, plan.Errors)
		}
		found := false
		for _, diagnostic := range plan.Errors {
			if diagnostic.Code == "lookup.condition.type_change_blocked" &&
				diagnostic.Path == "draft.logicalType" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: missing type-change block diagnostic: %#v", blocked.name, plan.Errors)
		}
	}

	// Control: converting an unreferenced field in the same source table stays
	// applicable, proving the block comes from the dependency, not the conversion.
	control, err := planConditionalLookupFieldChange(
		t, ctx, app, v2.ActionConvert, materials.TableID,
		*unrelated.Definition, v2.LogicalEmail, "无关字段",
	)
	if err != nil || !control.CanApply {
		t.Fatalf("unrelated conversion was blocked: %#v, %v", control.Errors, err)
	}

	// Display-name renames of both condition fields stay applicable and keep
	// the lookup computing through stable field IDs.
	for _, renamed := range []struct {
		name  string
		table v2IntegrationTable
		field v2.ApplyReceipt
		label string
	}{
		{"source comparison field", materials, sourceCode, "物料编码(改)"},
		{"current operand field", orders, orderCode, "订单编码(改)"},
	} {
		plan, err := planConditionalLookupFieldChange(
			t, ctx, app, v2.ActionUpdate, renamed.table.TableID,
			*renamed.field.Definition, v2.LogicalText, renamed.label,
		)
		if err != nil || !plan.CanApply {
			t.Fatalf("%s: rename plan was blocked: %#v, %v", renamed.name, plan.Errors, err)
		}
		applyConditionalLookupFieldPlan(t, ctx, app, plan, "cls_rename_"+renamed.field.FieldID)
	}

	if edges = conditionalLookupEdges(t, app, orders.TableID); !reflect.DeepEqual(edges, expected) {
		t.Fatalf("renames rewrote dependency identities: %#v", edges)
	}
	ordersDefinition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	lookupDefinition, found := ordersDefinition.Field(lookupField.FieldID)
	if !found || lookupDefinition.Lookup == nil {
		t.Fatalf("conditional lookup definition missing after renames: %#v", lookupDefinition)
	}
	rule := lookupDefinition.Lookup.Condition.Rules[0]
	if rule.SourceFieldID != sourceCode.FieldID || rule.Operand.FieldID != orderCode.FieldID {
		t.Fatalf("renames drifted stable rule identities: %#v", rule)
	}
	if _, err := queryschema.PrepareLookupCondition(
		ctx, app, ordersDefinition, *lookupDefinition.Lookup,
	); err != nil {
		t.Fatalf("renamed condition no longer prepares: %v", err)
	}

	// The renamed condition still computes against real rows.
	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(orderCollection)
	setConditionalLookupValue(row, orderCode, "MAT-1")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, material := range []struct{ code, title string }{
		{"MAT-1", "铝板"}, {"MAT-2", "钢板"},
	} {
		source := core.NewRecord(sourceCollection)
		setConditionalLookupValue(source, sourceCode, material.code)
		setConditionalLookupValue(source, sourceTitle, material.title)
		if err := app.Save(source); err != nil {
			t.Fatal(err)
		}
	}
	cells, err := lookup.NewCalculator().CalculateCells(ctx, app, ordersDefinition, row)
	if err != nil {
		t.Fatal(err)
	}
	cell := cells[lookupField.Definition.Identity.PhysicalName]
	if !reflect.DeepEqual(cell.Value, []any{"铝板"}) || cell.ProvenanceTotal != 1 {
		t.Fatalf("renamed condition computed = %#v", cell)
	}

	catalog := schemaapi.New(app)
	sourceRevision, err := catalog.GetRevision(ctx, materials.TableID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.DeleteTable(ctx, materials.TableID, sourceRevision)
	var referenced *schemaerror.ProductError
	if !errors.As(err, &referenced) || referenced.Code != "schema.table.referenced" {
		t.Fatalf("conditional source deletion was not blocked: %v", err)
	}
	cells, err = lookup.NewCalculator().CalculateCells(ctx, app, ordersDefinition, row)
	if err != nil || !reflect.DeepEqual(cells[lookupField.Definition.Identity.PhysicalName].Value, []any{"铝板"}) {
		t.Fatalf("rejected deletion changed source data: %#v, %v", cells, err)
	}
	currentRevision, err := catalog.GetRevision(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DeleteTable(ctx, orders.TableID, currentRevision); err != nil {
		t.Fatalf("deleting the lookup owner was blocked: %v", err)
	}
	if _, err := catalog.DeleteTable(ctx, materials.TableID, sourceRevision); err != nil {
		t.Fatalf("unreferenced source deletion was blocked: %v", err)
	}
}

// TestConditionalLookupSelectOperandMatchesByLabelAcrossDistinctOptionIDs pins
// the cross-table select comparison: the current and source select fields share
// no option ID, so matching must translate through the shared label.
func TestConditionalLookupSelectOperandMatchesByLabelAcrossDistinctOptionIDs(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()

	orders := createV2IntegrationTable(t, ctx, app, "当前任务", "clsel_orders")
	materials := createV2IntegrationTable(t, ctx, app, "来源任务", "clsel_materials")
	priorityDraft := fieldDraftForIntegration(t, v2.LogicalSelect, "优先级")
	priorityDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: "opt_clsel_cur_high", Label: "高", Color: "red", Order: 10, State: v2.OptionActive},
		{OptionID: "opt_clsel_cur_low", Label: "低", Color: "blue", Order: 20, State: v2.OptionActive},
	}}
	priority := createV2IntegrationField(
		t, ctx, app, orders.TableID, priorityDraft, "clsel_priority",
	)
	levelDraft := fieldDraftForIntegration(t, v2.LogicalSelect, "级别")
	levelDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: "opt_clsel_src_high", Label: "高", Color: "scarlet", Order: 10, State: v2.OptionActive},
		{OptionID: "opt_clsel_src_mid", Label: "中", Color: "amber", Order: 20, State: v2.OptionActive},
	}}
	level := createV2IntegrationField(
		t, ctx, app, materials.TableID, levelDraft, "clsel_level",
	)
	sourceTitle := createV2IntegrationField(
		t, ctx, app, materials.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "名称"), "clsel_title",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "条件结果")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{}, TargetFieldID: sourceTitle.FieldID,
		Condition: &v2.LookupCondition{
			SourceTableID: materials.TableID, Match: "all",
			Rules: []v2.LookupConditionRule{{
				SourceFieldID: level.FieldID, Operator: "eq",
				Operand: &v2.LookupOperand{Kind: "field", FieldID: priority.FieldID},
			}},
		},
	}
	lookupField := createV2IntegrationField(
		t, ctx, app, orders.TableID, lookupDraft, "clsel_lookup",
	)
	currentIDs := map[string]bool{}
	for _, option := range priority.Definition.Select.Options {
		currentIDs[option.OptionID] = true
	}
	for _, option := range level.Definition.Select.Options {
		if currentIDs[option.OptionID] {
			t.Fatalf("fixture must not share option IDs across tables: %s", option.OptionID)
		}
	}

	orderCollection, err := app.FindCollectionByNameOrId(orders.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(orderCollection)
	setConditionalLookupValue(row, priority, "opt_clsel_cur_high")
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(materials.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	for _, material := range []struct{ level, title string }{
		{"opt_clsel_src_high", "高优先级物料"}, {"opt_clsel_src_mid", "普通物料"},
	} {
		source := core.NewRecord(sourceCollection)
		setConditionalLookupValue(source, level, material.level)
		setConditionalLookupValue(source, sourceTitle, material.title)
		if err := app.Save(source); err != nil {
			t.Fatal(err)
		}
	}
	definition, err := schemaapi.New(app).Describe(ctx, orders.TableID)
	if err != nil {
		t.Fatal(err)
	}
	cells, err := lookup.NewCalculator().CalculateCells(ctx, app, definition, row)
	if err != nil {
		t.Fatal(err)
	}
	cell := cells[lookupField.Definition.Identity.PhysicalName]
	if !reflect.DeepEqual(cell.Value, []any{"高优先级物料"}) || cell.ProvenanceTotal != 1 {
		t.Fatalf("label-mapped select condition computed = %#v", cell)
	}
}
