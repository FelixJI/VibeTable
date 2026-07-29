package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestRelationDisplayFieldBlocksTargetLifecycleChange(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition:       baseTable("tbl_relation_target", "t_relation_target", []schema.FieldDefinition{}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition:       baseTable("tbl_relation_source", "t_relation_source", []schema.FieldDefinition{}),
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

	titleDraft := fieldDraftForIntegration(t, v2.LogicalText, "Title")
	titlePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: target.TableID,
		ExpectedSchemaRev: target.SchemaRevision, Draft: &titleDraft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	titleReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: titlePlan.PlanID, PlanHash: titlePlan.PlanHash,
		OperationID: "op_create_relation_title", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}

	relationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Customer")
	relationDraft.Relation = &v2.RelationSpec{
		TargetTableID: target.TableID,
		Cardinality:   "one",
		DeletePolicy:  "setNull",
		DisplayField:  titleReceipt.FieldID,
	}
	relationPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: source.TableID,
		ExpectedSchemaRev: source.SchemaRevision, Draft: &relationDraft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !relationPlan.CanApply {
		t.Fatalf("valid relation plan = %#v", relationPlan.Errors)
	}
	relationReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: relationPlan.PlanID, PlanHash: relationPlan.PlanHash,
		OperationID: "op_create_relation_display_dependency", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}

	sourceRecordID := "relationsrc0001"
	_, err = mutation.New(app, mutation.MetadataSchemaSource{}).Preview(
		ctx,
		mutationRequest(
			source.TableID,
			relationReceipt.SchemaRevision,
			"v2-relation-missing-target",
			mutation.Operation{
				Kind:     mutation.OperationInsert,
				RecordID: &sourceRecordID,
				Values:   map[string]any{},
				RawValues: map[string]any{
					relationReceipt.FieldID: "missingtarget01",
				},
			},
		),
	)
	var relationErr *mutation.ProductError
	if !errors.As(err, &relationErr) ||
		relationErr.Code != "mutation.relation.target_not_found" {
		t.Fatalf("missing v2 relation target error = %#v", err)
	}

	retirePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionRetire, TableID: target.TableID,
		FieldID:           titleReceipt.FieldID,
		ExpectedSchemaRev: titleReceipt.SchemaRevision, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retirePlan.CanApply || len(retirePlan.Impact.Dependencies) != 1 ||
		retirePlan.Impact.Dependencies[0].Kind != "relationDisplayField" {
		t.Fatalf("relation display dependency was not frozen: %#v", retirePlan)
	}
}

func fieldDraftForIntegration(
	t *testing.T,
	logicalType v2.LogicalType,
	name string,
) v2.FieldDraft {
	t.Helper()
	recommended, err := v2.RecommendedDefaults(logicalType)
	if err != nil {
		t.Fatal(err)
	}
	return v2.FieldDraft{
		DisplayName: name, LogicalType: logicalType,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		File: recommended.File, JSON: recommended.JSON,
	}
}
