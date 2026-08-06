package integration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestFormulaAndLookupCreateThroughFieldChangeV2(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	region, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"tbl_v2_computed_region", "t_v2_computed_region",
			[]schema.FieldDefinition{},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"tbl_v2_computed_target", "t_v2_computed_target",
			[]schema.FieldDefinition{},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable(
			"tbl_v2_computed_source", "t_v2_computed_source",
			[]schema.FieldDefinition{},
		),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		catalog, catalog, store, v2.NewIdentityAllocator(nil),
	)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}

	regionName := applyCreatedField(
		t, ctx, catalog, planner, executor, region.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Region name"),
		actor, "op_v2_region_name",
	)
	title := applyCreatedField(
		t, ctx, catalog, planner, executor, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		actor, "op_v2_target_title",
	)
	balance := applyCreatedField(
		t, ctx, catalog, planner, executor, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalNumber, "Balance"),
		actor, "op_v2_target_balance",
	)
	name := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"),
		actor, "op_v2_source_name",
	)
	regionRelationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Region")
	regionRelationDraft.Relation = &v2.RelationSpec{
		TargetTableID: region.TableID, Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: regionName.FieldID,
	}
	targetRevisions, err := catalog.Revisions(ctx, target.TableID)
	if err != nil {
		t.Fatal(err)
	}
	regionRelationPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: target.TableID,
		ExpectedSchemaRev: targetRevisions.Schema, Draft: &regionRelationDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Customers", ReciprocalCardinality: "many",
			SourceDisplayFieldID: title.FieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	regionRelation, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: regionRelationPlan.PlanID, PlanHash: regionRelationPlan.PlanHash,
		OperationID: "op_v2_region_relation", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	relationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Customer")
	relationDraft.Relation = &v2.RelationSpec{
		TargetTableID: target.TableID, Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: title.FieldID,
	}
	sourceRevisions, err := catalog.Revisions(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	relationPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: source.TableID,
		ExpectedSchemaRev: sourceRevisions.Schema, Draft: &relationDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Sources", ReciprocalCardinality: "many",
			SourceDisplayFieldID: name.FieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	relation, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: relationPlan.PlanID, PlanHash: relationPlan.PlanHash,
		OperationID: "op_v2_relation", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Upper name")
	formulaDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "SUM({Customer}.{Balance}) + 1.0",
	}
	formula := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		formulaDraft, actor, "op_v2_formula",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Customer title")
	lookupDraft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{
			{RelationFieldID: relation.FieldID},
			{RelationFieldID: regionRelation.FieldID},
		},
		TargetFieldID: regionName.FieldID,
	}
	lookup := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		lookupDraft, actor, "op_v2_lookup",
	)
	if formula.Definition == nil || formula.Definition.Formula == nil ||
		lookup.Definition == nil || lookup.Definition.Lookup == nil {
		t.Fatalf("computed v2 receipts were incomplete: %#v / %#v", formula, lookup)
	}
	legacy, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	formulaLegacy := integrationFieldByID(legacy, formula.FieldID)
	lookupLegacy := integrationFieldByID(legacy, lookup.FieldID)
	if formulaLegacy == nil || formulaLegacy.Formula == nil ||
		lookupLegacy == nil || lookupLegacy.Lookup == nil {
		t.Fatalf("legacy projections omitted computed specs: %#v", legacy.Fields)
	}
	if formula.Definition.Formula.ResultType != v2.LogicalNumber ||
		formula.Definition.Formula.Source == formulaDraft.Formula.Source ||
		formulaLegacy.Formula.ResultType != schema.DataTypeFloat ||
		formulaLegacy.Formula.Status != "backfilling" ||
		formulaLegacy.Formula.Source != "relationSum("+relation.Definition.Identity.PhysicalName+
			", \""+balance.Definition.Identity.PhysicalName+"\") + 1.0" {
		t.Fatalf("formula was not canonicalized and inferred: %#v / %#v", formula, formulaLegacy)
	}
	dependencies, err := app.FindRecordsByFilter(
		"vibetable_formula_dependencies",
		"source_table_id={:table} && formula_field_id={:field}",
		"", 10, 0,
		dbx.Params{"table": source.TableID, "field": formula.FieldID},
	)
	if err != nil || len(dependencies) != 1 ||
		dependencies[0].GetString("relation_field_id") != relation.FieldID ||
		dependencies[0].GetString("target_field_id") != balance.FieldID {
		t.Fatalf("v2 formula dependency metadata = %#v, err=%v", dependencies, err)
	}
	lookupMetadata, err := app.FindFirstRecordByFilter(
		"vibetable_lookups",
		"table_id={:table} && field_id={:field}",
		dbx.Params{"table": source.TableID, "field": lookup.FieldID},
	)
	if err != nil || lookupMetadata.GetString("target_field_id") != regionName.FieldID {
		t.Fatalf("v2 Lookup metadata = %#v, err=%v", lookupMetadata, err)
	}

	collection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	sourceRecord := core.NewRecord(collection)
	sourceRecord.Set(name.Definition.Identity.PhysicalName, "Scale order")
	sourceRecord.Set(formula.Definition.Identity.PhysicalName, 999.0)
	if err := app.Save(sourceRecord); err != nil {
		t.Fatal(err)
	}
	formulaDraft = v2.FieldDraft{
		DisplayName: formula.Definition.DisplayName,
		Help:        formula.Definition.Help,
		LogicalType: formula.Definition.LogicalType,
		Value:       formula.Definition.Value,
		Constraints: formula.Definition.Constraints,
		Storage:     formula.Definition.Storage,
		Display:     formula.Definition.Display,
		Formula: &v2.FormulaDraftSpec{
			Language: "cel-v1", Source: "SUM({Customer}.{Balance}) + 2.0",
		},
	}
	revisions, err := catalog.Revisions(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	updatePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: source.TableID, FieldID: formula.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &formulaDraft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updatePlan.CreatesMigration || len(updatePlan.Classes) != 1 ||
		updatePlan.Classes[0] != v2.ClassSchema {
		t.Fatalf("formula source update plan = %#v", updatePlan)
	}
	updatedFormula, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: updatePlan.PlanID, PlanHash: updatePlan.PlanHash,
		OperationID: "op_v2_formula_update", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedLegacy, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	updatedLegacyField := integrationFieldByID(updatedLegacy, formula.FieldID)
	if updatedLegacyField == nil || updatedLegacyField.Formula == nil ||
		updatedLegacyField.Formula.Status != "backfilling" ||
		updatedLegacyField.Formula.Version != 2 ||
		updatedFormula.Definition.Formula.ResultType != v2.LogicalNumber {
		t.Fatalf("updated formula lifecycle = %#v / %#v", updatedFormula, updatedLegacyField)
	}
	var cleared int
	query := fmt.Sprintf(
		"SELECT `%s` IS NULL FROM `%s` WHERE id={:id}",
		formula.Definition.Identity.PhysicalName, source.PhysicalName,
	)
	if err := app.DB().NewQuery(query).Bind(dbx.Params{"id": sourceRecord.Id}).Row(&cleared); err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatal("formula source update left a stale materialized value")
	}
}

func integrationFieldByID(
	definition schema.TableDefinition,
	fieldID string,
) *schema.FieldDefinition {
	for index := range definition.Fields {
		if definition.Fields[index].FieldID == fieldID {
			return &definition.Fields[index]
		}
	}
	return nil
}

func applyCreatedField(
	t *testing.T,
	ctx context.Context,
	catalog *fieldchange.Catalog,
	planner *fieldchange.Planner,
	executor *fieldchange.Executor,
	tableID string,
	draft v2.FieldDraft,
	actor v2.Actor,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	revisions, err := catalog.Revisions(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: tableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("create plan blocked: %#v", plan.Errors)
	}
	receipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: operationID, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
