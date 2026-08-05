package integration_test

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestFormulaAndLookupCreateThroughFieldChangeV2(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
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

	title := applyCreatedField(
		t, ctx, catalog, planner, executor, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		actor, "op_v2_target_title",
	)
	name := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"),
		actor, "op_v2_source_name",
	)
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
	formulaDraft.Formula = &v2.FormulaSpec{
		Language: "cel-v1", Source: "upper(" + name.Definition.Identity.PhysicalName + ")",
		ResultType: v2.LogicalText,
	}
	formula := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		formulaDraft, actor, "op_v2_formula",
	)
	lookupDraft := fieldDraftForIntegration(t, v2.LogicalLookup, "Customer title")
	lookupDraft.Lookup = &v2.LookupSpec{
		RelationFieldID: relation.FieldID, TargetFieldID: title.FieldID,
		Aggregate: "first", ResultType: v2.LogicalText,
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
