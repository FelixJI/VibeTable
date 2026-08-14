package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestRelationDisplayFieldBlocksTargetLifecycleChange(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(
		t, ctx, app, "Relation targets", "op_relation_target_table",
	)
	source := createV2IntegrationTable(
		t, ctx, app, "Relation sources", "op_relation_source_table",
	)
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
	sourceTitleDraft := fieldDraftForIntegration(t, v2.LogicalText, "Order number")
	sourceTitlePlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: source.TableID,
		ExpectedSchemaRev: source.SchemaRevision, Draft: &sourceTitleDraft, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceTitleReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: sourceTitlePlan.PlanID, PlanHash: sourceTitlePlan.PlanHash,
		OperationID: "op_create_relation_source_title", Actor: actor,
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
		ExpectedSchemaRev: sourceTitleReceipt.SchemaRevision,
		Draft:             &relationDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Orders", ReciprocalCardinality: "many",
			SourceDisplayFieldID: sourceTitleReceipt.FieldID,
		},
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
	if len(relationReceipt.Related) != 1 ||
		relationReceipt.Related[0].TableID != target.TableID ||
		relationReceipt.Related[0].Definition == nil ||
		relationReceipt.Definition == nil ||
		relationReceipt.Related[0].Definition.Relation.PairID !=
			relationReceipt.Definition.Relation.PairID {
		t.Fatalf("reciprocal relation was not applied atomically: %#v", relationReceipt)
	}

	targetRecordID := "relationtgt0001"
	sourceRecordID := "relationsrc0001"
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	if _, err := kernel.Apply(ctx, mutationRequest(
		target.TableID,
		relationReceipt.Related[0].SchemaRevision,
		"create-relation-target",
		mutation.Operation{
			Kind:     mutation.OperationInsert,
			RecordID: &targetRecordID,
			Values:   map[string]any{},
			RawValues: map[string]any{
				titleReceipt.FieldID: "Acme",
			},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		source.TableID,
		relationReceipt.SchemaRevision,
		"create-related-source",
		mutation.Operation{
			Kind:     mutation.OperationInsert,
			RecordID: &sourceRecordID,
			Values:   map[string]any{},
			RawValues: map[string]any{
				sourceTitleReceipt.FieldID: "SO-1001",
				relationReceipt.FieldID:    targetRecordID,
			},
		},
	)); err != nil {
		t.Fatal(err)
	}
	targetCollection, err := app.FindCollectionByNameOrId(target.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	targetRecord, err := app.FindRecordById(targetCollection, targetRecordID)
	if err != nil {
		t.Fatal(err)
	}
	reciprocalPhysicalName := relationReceipt.Related[0].Definition.Identity.PhysicalName
	if got := targetRecord.GetStringSlice(reciprocalPhysicalName); len(got) != 1 || got[0] != sourceRecordID {
		t.Fatalf("reciprocal relation after insert = %#v", got)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		source.TableID,
		relationReceipt.SchemaRevision,
		"clear-related-source",
		mutation.Operation{
			Kind:     mutation.OperationUpdate,
			RecordID: &sourceRecordID,
			Values: map[string]any{
				relationReceipt.Definition.Identity.PhysicalName: nil,
			},
		},
	)); err != nil {
		t.Fatal(err)
	}
	targetRecord, err = app.FindRecordById(targetCollection, targetRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetRecord.GetStringSlice(reciprocalPhysicalName); len(got) != 0 {
		t.Fatalf("reciprocal relation after clear = %#v", got)
	}
	faultKernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFaultInjector(func(point string) error {
			if point == "after_record" {
				return errors.New("forced reciprocal rollback")
			}
			return nil
		}),
	)
	if _, err := faultKernel.Apply(ctx, mutationRequest(
		source.TableID,
		relationReceipt.SchemaRevision,
		"rollback-related-source",
		mutation.Operation{
			Kind:     mutation.OperationUpdate,
			RecordID: &sourceRecordID,
			Values: map[string]any{
				relationReceipt.Definition.Identity.PhysicalName: targetRecordID,
			},
		},
	)); err == nil {
		t.Fatal("faulted reciprocal mutation unexpectedly committed")
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	sourceRecord, err := app.FindRecordById(sourceCollection, sourceRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceRecord.GetStringSlice(relationReceipt.Definition.Identity.PhysicalName); len(got) != 0 {
		t.Fatalf("source relation survived rolled-back transaction = %#v", got)
	}
	targetRecord, err = app.FindRecordById(targetCollection, targetRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetRecord.GetStringSlice(reciprocalPhysicalName); len(got) != 0 {
		t.Fatalf("reciprocal relation survived rolled-back transaction = %#v", got)
	}

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
		ExpectedSchemaRev: relationReceipt.Related[0].SchemaRevision, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retirePlan.CanApply || len(retirePlan.Impact.Dependencies) != 1 ||
		retirePlan.Impact.Dependencies[0].Kind != "relationDisplayField" {
		t.Fatalf("relation display dependency was not frozen: %#v", retirePlan)
	}
}

func TestRelationPairApplyRejectsTargetRevisionChangeWithoutCreatingEitherField(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	target := createV2IntegrationTable(t, ctx, app, "Pair targets", "op_pair_target_table")
	source := createV2IntegrationTable(t, ctx, app, "Pair sources", "op_pair_source_table")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	targetTitle := applyCreatedField(
		t, ctx, catalog, planner, executor, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Target title"),
		actor, "op_pair_target_title",
	)
	sourceTitle := applyCreatedField(
		t, ctx, catalog, planner, executor, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Source title"),
		actor, "op_pair_source_title",
	)
	relationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Target")
	relationDraft.Relation = &v2.RelationSpec{
		TargetTableID: target.TableID, Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: targetTitle.FieldID,
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: source.TableID,
		ExpectedSchemaRev: sourceTitle.SchemaRevision, Draft: &relationDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Sources", ReciprocalCardinality: "many",
			SourceDisplayFieldID: sourceTitle.FieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyCreatedField(
		t, ctx, catalog, planner, executor, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Concurrent field"),
		actor, "op_pair_target_concurrent",
	)
	_, err = executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "op_pair_stale_apply", Actor: actor,
	})
	var productErr *fieldchange.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "field.change.schema_conflict" {
		t.Fatalf("stale reciprocal apply error = %#v", err)
	}
	reloadedSource, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloadedSource.Field(plan.After.Identity.FieldID); exists {
		t.Fatalf(
			"failed reciprocal apply left main field behind: %#v",
			reloadedSource.Snapshot.Fields,
		)
	}
}

func TestSelfRelationBatchKeepsReciprocalStateAndAllowsSelfLinkedDelete(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(
		t, ctx, app, "Self relations", "op_self_relation_table",
	)
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	name := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"),
		actor, "op_self_relation_name",
	)
	relationDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Manager")
	relationDraft.Relation = &v2.RelationSpec{
		TargetTableID: table.TableID, Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: name.FieldID,
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: name.SchemaRevision, Draft: &relationDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Reports", ReciprocalCardinality: "many",
			SourceDisplayFieldID: name.FieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	relationReceipt, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID: "op_self_relation_pair", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relationReceipt.Definition == nil || len(relationReceipt.Related) != 1 ||
		relationReceipt.Related[0].Definition == nil {
		t.Fatalf("self relation pair receipt = %#v", relationReceipt)
	}

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	managerID := "selfmanager0001"
	reportID := "selfreport00001"
	for _, record := range []struct {
		id   string
		name string
	}{{managerID, "Manager"}, {reportID, "Report"}} {
		if _, err := kernel.Apply(ctx, mutationRequest(
			table.TableID, relationReceipt.SchemaRevision, "insert-"+record.id,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &record.id,
				Values: map[string]any{
					name.Definition.Identity.PhysicalName: record.name,
				},
			},
		)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		table.TableID, relationReceipt.SchemaRevision, "self-relation-batch",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &reportID,
			Values: map[string]any{name.Definition.Identity.PhysicalName: "Report 1"},
		},
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &managerID,
			Values: map[string]any{
				relationReceipt.Definition.Identity.PhysicalName: reportID,
			},
		},
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &reportID,
			Values: map[string]any{name.Definition.Identity.PhysicalName: "Report 2"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	report, err := app.FindRecordById(collection, reportID)
	if err != nil {
		t.Fatal(err)
	}
	reciprocalName := relationReceipt.Related[0].Definition.Identity.PhysicalName
	if got := report.GetStringSlice(reciprocalName); len(got) != 1 || got[0] != managerID {
		t.Fatalf("self relation reciprocal after batch = %#v", got)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		table.TableID, relationReceipt.SchemaRevision, "self-link-before-delete",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &managerID,
			Values: map[string]any{
				relationReceipt.Definition.Identity.PhysicalName: managerID,
			},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		table.TableID, relationReceipt.SchemaRevision, "delete-self-linked",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &managerID},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.FindRecordById(collection, managerID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("self-linked record was not deleted: %v", err)
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
