package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	formulapkg "github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	lookuppkg "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type atomicFormulaScheduler struct {
	enqueued []string
	started  []string
	fail     bool
}

type failingComputationInvalidator struct{}

func (failingComputationInvalidator) EnqueueInvalidations(
	context.Context,
	core.App,
	mutation.DataChangedEvent,
) ([]string, error) {
	return nil, errors.New("recalculation queue unavailable")
}

func (scheduler *atomicFormulaScheduler) EnqueueFormulaBackfill(
	_ context.Context,
	_ core.App,
	tableID string,
	schemaRevision string,
) (string, error) {
	scheduler.enqueued = append(scheduler.enqueued, tableID+":"+schemaRevision)
	if scheduler.fail {
		return "", errors.New("formula enqueue failed")
	}
	return fmt.Sprintf("formula-job-%d", len(scheduler.enqueued)), nil
}

func (scheduler *atomicFormulaScheduler) Start(jobID string) bool {
	scheduler.started = append(scheduler.started, jobID)
	return true
}

func TestLookupConfigurationUpdatePersistsAndChangesQuery(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	source := createV2IntegrationTable(t, ctx, app, "Sources", "lookup_update_source")
	target := createV2IntegrationTable(t, ctx, app, "Targets", "lookup_update_target")
	name := createV2IntegrationField(t, ctx, app, source.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"), "lookup_update_name")
	title := createV2IntegrationField(t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"), "lookup_update_title")
	code := createV2IntegrationField(t, ctx, app, target.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Code"), "lookup_update_code")
	first := createV2IntegrationRelation(t, ctx, app, source.TableID, name.FieldID,
		target.TableID, title.FieldID, "First", "First sources", "one", "lookup_update_first")
	second := createV2IntegrationRelation(t, ctx, app, source.TableID, name.FieldID,
		target.TableID, title.FieldID, "Second", "Second sources", "one", "lookup_update_second")
	draft := fieldDraftForIntegration(t, v2.LogicalLookup, "Selected value")
	draft.Lookup = &v2.LookupSpec{
		Path: []v2.LookupPathStep{{RelationFieldID: first.FieldID}}, TargetFieldID: title.FieldID,
	}
	lookup := createV2IntegrationField(t, ctx, app, source.TableID, draft, "lookup_update_lookup")
	targetCollection, err := app.FindCollectionByNameOrId(target.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	var targets []*core.Record
	for index := range 2 {
		record := core.NewRecord(targetCollection)
		record.Set(title.Definition.Identity.PhysicalName, fmt.Sprintf("Title %d", index+1))
		record.Set(title.Definition.Value.Presence.PhysicalName, true)
		record.Set(code.Definition.Identity.PhysicalName, fmt.Sprintf("CODE-%d", index+1))
		record.Set(code.Definition.Value.Presence.PhysicalName, true)
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, record)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(sourceCollection)
	for index, link := range []v2.ApplyReceipt{first, second} {
		row.Set(link.Definition.Identity.PhysicalName, targets[index].Id)
		row.Set(link.Definition.Value.Presence.PhysicalName, true)
	}
	if err := app.Save(row); err != nil {
		t.Fatal(err)
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	service := relation.New(app, query.NewPort(app, querySource), mutation.New(app, mutation.MetadataSchemaSource{}))
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, nil)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	check := func(link v2.ApplyReceipt, valueField v2.ApplyReceipt, want string, revision int) {
		t.Helper()
		execution, err := schemaapi.New(app).Describe(ctx, source.TableID)
		if err != nil {
			t.Fatal(err)
		}
		field := integrationFieldByID(execution, lookup.FieldID)
		wantSpec := &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: link.FieldID}}, TargetFieldID: valueField.FieldID}
		if field == nil || field.Identity != lookup.Definition.Identity || !reflect.DeepEqual(field.Lookup, wantSpec) {
			t.Fatalf("stored lookup definition = %#v", field)
		}
		metadata, err := app.FindFirstRecordByFilter("vibetable_lookups",
			"table_id={:table} && field_id={:field}", dbx.Params{"table": source.TableID, "field": lookup.FieldID})
		if err != nil {
			t.Fatal(err)
		}
		var pathConfig struct {
			RelationFieldID string              `json:"relationFieldId"`
			TargetFieldID   string              `json:"targetFieldId"`
			Path            []v2.LookupPathStep `json:"path"`
		}
		if err := json.Unmarshal([]byte(metadata.GetString("path_json")), &pathConfig); err != nil {
			t.Fatal(err)
		}
		if metadata.GetInt("revision") != revision || metadata.GetString("target_field_id") != valueField.FieldID ||
			metadata.GetString("relation_field_id") != link.FieldID || pathConfig.RelationFieldID != link.FieldID ||
			pathConfig.TargetFieldID != valueField.FieldID || !reflect.DeepEqual(pathConfig.Path, wantSpec.Path) {
			t.Fatalf("lookup metadata revision=%d path=%#v", metadata.GetInt("revision"), pathConfig)
		}
		dependencies, err := app.FindRecordsByFilter("vibetable_computation_dependencies",
			"source_table_id={:table} && computed_field_id={:field}", "", 0, 0,
			dbx.Params{"table": source.TableID, "field": lookup.FieldID})
		if err != nil || len(dependencies) != 1 {
			t.Fatalf("lookup dependencies = %#v, %v", dependencies, err)
		}
		dependency := dependencies[0]
		var dependencyPath []v2.LookupPathStep
		if err := json.Unmarshal([]byte(dependency.GetString("path_json")), &dependencyPath); err != nil {
			t.Fatal(err)
		}
		if dependency.GetString("target_table_id") != target.TableID || dependency.GetString("target_field_id") != valueField.FieldID ||
			dependency.GetString("relation_field_id") != link.FieldID || dependency.GetInt("definition_version") != revision ||
			!reflect.DeepEqual(dependencyPath, wantSpec.Path) {
			t.Fatalf("lookup dependency = %#v", dependency.PublicExport())
		}
		page, err := service.QueryLookups(ctx, relation.LookupQueryRequest{
			TableID: source.TableID, SchemaRevision: execution.Snapshot.SchemaRevision, Query: query.TableQuery{Limit: 50},
		})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("lookup query = %#v, %v", page, err)
		}
		cell, ok := page.Rows[0][lookup.Definition.Identity.PhysicalName].(lookuppkg.CellValue)
		if !ok || cell.Value != want {
			t.Fatalf("lookup query value = %#v, want %q", cell, want)
		}
	}
	check(first, title, "Title 1", 1)
	for index, link := range []v2.ApplyReceipt{first, second} {
		draft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: link.FieldID}}, TargetFieldID: code.FieldID}
		before, err := catalog.Revisions(ctx, source.TableID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
			Action: v2.ActionUpdate, TableID: source.TableID, FieldID: lookup.FieldID,
			ExpectedSchemaRev: before.Schema, Draft: &draft, Actor: actor,
		})
		if err != nil || !plan.CanApply || len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassSchema {
			t.Fatalf("lookup update %d = %#v, %v", index, plan, err)
		}
		if _, err := executor.Apply(ctx, v2.ApplyRequest{
			PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: fmt.Sprintf("lookup_update_apply_%d", index), Actor: actor,
		}); err != nil {
			t.Fatal(err)
		}
		after, err := catalog.Revisions(ctx, source.TableID)
		if err != nil || after.Schema == before.Schema {
			t.Fatalf("lookup schema revision did not advance: %#v / %#v, %v", before, after, err)
		}
		check(link, code, fmt.Sprintf("CODE-%d", index+1), index+2)
	}
	revisions, err := catalog.Revisions(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	intent := v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: source.TableID, FieldID: lookup.FieldID,
		ExpectedSchemaRev: revisions.Schema, Draft: &draft, Actor: actor}
	if _, err := planner.Plan(ctx, intent); err == nil {
		t.Fatal("unchanged lookup was accepted")
	} else {
		var productErr *fieldchange.ProductError
		if !errors.As(err, &productErr) || productErr.Code != "field.change.noop" {
			t.Fatalf("unchanged lookup error = %v", err)
		}
	}
	draft.Lookup = &v2.LookupSpec{Path: []v2.LookupPathStep{{RelationFieldID: second.FieldID}}, TargetFieldID: "fld_missing_target"}
	invalid, err := planner.Plan(ctx, intent)
	if err != nil || invalid.CanApply || len(invalid.Errors) != 1 ||
		invalid.Errors[0].Code != "field.lookup.target_invalid" || invalid.Errors[0].Path != "draft.lookup.targetFieldId" {
		t.Fatalf("invalid lookup target = %#v, %v", invalid, err)
	}
	check(second, code, "CODE-2", 3)
}

func TestFormulaPlanAcceptsFreshNumberPhysicalNameTimesFloatLiteral(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(
		t, ctx, app, "Fresh number formula", "op_v2_fresh_number_formula_table",
	)
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		catalog, catalog, store, v2.NewIdentityAllocator(nil),
	)
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	numberDraft := fieldDraftForIntegration(t, v2.LogicalNumber, "Value")
	numberDraft.Storage.Options.OnlyInt = true
	numberDraft.Display.DisplayScale = 0
	number := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		numberDraft,
		actor, "op_v2_fresh_number_formula_value",
	)
	if number.Definition == nil {
		t.Fatal("number field receipt omitted its Schema V2 definition")
	}
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Doubled")
	formulaDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1",
		Source:   number.Definition.Identity.PhysicalName + " * 2.0",
	}
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		ExpectedSchemaRev: revisions.Schema, Draft: &formulaDraft, Actor: actor,
	})
	if err != nil {
		t.Fatalf("plan fresh number formula: %#v", err)
	}
	if plan.After == nil || plan.After.Formula == nil {
		t.Fatalf("formula plan omitted its normalized definition: %#v", plan)
	}
	if plan.After.Formula.Source != formulaDraft.Formula.Source ||
		plan.After.Formula.ResultType != v2.LogicalNumber ||
		plan.After.Storage.Options.OnlyInt {
		t.Fatalf("normalized formula definition = %#v", plan.After)
	}
}

func TestFormulaAndLookupCreateThroughFieldChangeV2(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	region := createV2IntegrationTable(
		t, ctx, app, "Computed regions", "op_v2_computed_region",
	)
	target := createV2IntegrationTable(
		t, ctx, app, "Computed targets", "op_v2_computed_target",
	)
	source := createV2IntegrationTable(
		t, ctx, app, "Computed sources", "op_v2_computed_source",
	)
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		catalog, catalog, store, v2.NewIdentityAllocator(nil),
	)
	formulaScheduler := &atomicFormulaScheduler{}
	executor := fieldchange.NewExecutor(
		app,
		store,
		fieldchange.WithFormulaBackfillScheduler(formulaScheduler),
	)
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
	execution, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	formulaExecution := integrationFieldByID(execution, formula.FieldID)
	lookupExecution := integrationFieldByID(execution, lookup.FieldID)
	if formulaExecution == nil || formulaExecution.Formula == nil ||
		lookupExecution == nil || lookupExecution.Lookup == nil {
		t.Fatalf("execution snapshot omitted computed specs: %#v", execution.Snapshot.Fields)
	}
	formulaRuntime := execution.FormulaRuntime[formula.FieldID]
	if formula.Definition.Formula.ResultType != v2.LogicalNumber ||
		formula.Definition.Formula.Source == formulaDraft.Formula.Source ||
		formulaExecution.Formula.ResultType != v2.LogicalNumber ||
		formulaRuntime.Status != "backfilling" ||
		formulaExecution.Formula.Source != "relationSum("+relation.Definition.Identity.PhysicalName+
			", \""+balance.Definition.Identity.PhysicalName+"\") + 1.0" {
		t.Fatalf("formula was not canonicalized and inferred: %#v / %#v", formula, formulaExecution)
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

	regionCollection, err := app.FindCollectionByNameOrId(region.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	regionRecord := core.NewRecord(regionCollection)
	regionRecord.Set("id", "regionnorth0000")
	regionRecord.Set(regionName.Definition.Identity.PhysicalName, "North")
	if err := app.Save(regionRecord); err != nil {
		t.Fatal(err)
	}
	targetCollection, err := app.FindCollectionByNameOrId(target.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	targetRecord := core.NewRecord(targetCollection)
	targetRecord.Set("id", "customerone0001")
	targetRecord.Set(title.Definition.Identity.PhysicalName, "Customer one")
	targetRecord.Set(balance.Definition.Identity.PhysicalName, 41.0)
	targetRecord.Set(regionRelation.Definition.Identity.PhysicalName, regionRecord.Id)
	if err := app.Save(targetRecord); err != nil {
		t.Fatal(err)
	}
	calculator := computed.New(
		lookuppkg.NewCalculator(),
		formulapkg.NewCalculator(formulapkg.NewCompiler(formulapkg.DefaultLimits())),
	)
	kernel := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithFormulaCalculator(calculator),
	)
	computedRecordID := "sourceversion01"
	computedReceipt, err := kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "request-computed-versioned", IdempotencyKey: "idem-computed-versioned",
		TableID: source.TableID, SchemaRevision: execution.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationInsert, RecordID: &computedRecordID,
			Values: map[string]any{
				name.Definition.Identity.PhysicalName:     "Versioned order",
				relation.Definition.Identity.PhysicalName: targetRecord.Id,
			},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := computedReceipt.ComputedFields[computedRecordID][formula.Definition.Identity.PhysicalName]; got != 42.0 {
		t.Fatalf("formula receipt value = %#v", got)
	}
	if got := computedReceipt.ComputedFields[computedRecordID][lookup.Definition.Identity.PhysicalName]; got != "North" {
		t.Fatalf("lookup receipt value = %#v", got)
	}
	sourceCollection, err := app.FindCollectionByNameOrId(source.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	storedComputed, err := app.FindRecordById(sourceCollection, computedRecordID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []v2.ApplyReceipt{formula, lookup} {
		envelope, ok := relatedcomputation.Decode(
			storedComputed.GetRaw(field.Definition.Identity.PhysicalName),
		)
		if !ok || envelope.State != "ready" ||
			envelope.Version.SourceDataRevision != 1 {
			t.Fatalf("stored computed envelope for %s = %#v", field.FieldID, envelope)
		}
	}
	querySource, err := queryschema.New(app.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := querySource.DescribeQueryTable(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	lookupFilter := query.TableQuery{
		Filters: []query.FilterExpression{{
			Field:    lookup.Definition.Identity.PhysicalName,
			Operator: query.OperatorContains, Value: "North",
		}},
		Limit: 50,
	}
	compiled, err := query.Compile(descriptor, lookupFilter)
	if err != nil {
		t.Fatal(err)
	}
	var freshCount int
	if err := app.DB().NewQuery(compiled.CountSQL).Bind(compiled.Params).Row(&freshCount); err != nil {
		t.Fatal(err)
	}
	if freshCount != 1 {
		envelope, _ := relatedcomputation.Decode(
			storedComputed.GetRaw(lookup.Definition.Identity.PhysicalName),
		)
		t.Fatalf(
			"fresh lookup filter count = %d; rowRevision=%#v envelope=%#v descriptor=%#v sql=%s params=%#v",
			freshCount,
			storedComputed.GetRaw(relatedcomputation.RowRevisionField),
			envelope,
			descriptor.Fields[lookup.Definition.Identity.PhysicalName],
			compiled.CountSQL,
			compiled.Params,
		)
	}
	targetDefinition, err := schemaapi.New(app).Describe(ctx, target.TableID)
	if err != nil {
		t.Fatal(err)
	}
	jobService := jobs.New(app, nil)
	defer jobService.Shutdown()
	targetReceipt, err := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithComputationInvalidator(jobService),
	).Apply(
		ctx,
		mutation.Request{
			ContractVersion: mutation.ContractVersion,
			RequestID:       "request-target-change", IdempotencyKey: "idem-target-change",
			TableID: target.TableID, SchemaRevision: targetDefinition.Snapshot.SchemaRevision,
			Operations: []mutation.Operation{{
				Kind: mutation.OperationUpdate, RecordID: &targetRecord.Id,
				Values: map[string]any{balance.Definition.Identity.PhysicalName: 42.0},
			}},
			Actor: mutation.Actor{Type: "user", ID: "local-user"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(targetReceipt.EmittedEvents) != 1 {
		t.Fatalf("target mutation events = %#v", targetReceipt.EmittedEvents)
	}
	queued, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_event_id={:event}",
		dbx.Params{"event": targetReceipt.EmittedEvents[0]},
	)
	if err != nil || queued.GetString("state") != "queued" {
		t.Fatalf("transactional recalculation job = %#v, err=%v", queued, err)
	}
	failedRequestID := "request-target-change-rollback"
	_, applyErr := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithComputationInvalidator(failingComputationInvalidator{}),
	).Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       failedRequestID, IdempotencyKey: "idem-target-change-rollback",
		TableID: target.TableID, SchemaRevision: targetDefinition.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationUpdate, RecordID: &targetRecord.Id,
			Values: map[string]any{balance.Definition.Identity.PhysicalName: 43.0},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	})
	if applyErr == nil {
		t.Fatal("target mutation committed without its durable recalculation job")
	}
	reloadedTarget, err := app.FindRecordById(targetCollection, targetRecord.Id)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedTarget.GetFloat(balance.Definition.Identity.PhysicalName) != 42.0 {
		t.Fatalf(
			"failed invalidation enqueue changed target balance: %#v",
			reloadedTarget.GetRaw(balance.Definition.Identity.PhysicalName),
		)
	}
	descriptor, err = querySource.DescribeQueryTable(ctx, app, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err = query.Compile(descriptor, lookupFilter)
	if err != nil {
		t.Fatal(err)
	}
	var staleCount int
	if err := app.DB().NewQuery(compiled.CountSQL).Bind(compiled.Params).Row(&staleCount); err != nil {
		t.Fatal(err)
	}
	if staleCount != 0 {
		t.Fatalf("stale lookup participated in filtering: count=%d", staleCount)
	}
	regionDefinition, err := schemaapi.New(app).Describe(ctx, region.TableID)
	if err != nil {
		t.Fatal(err)
	}
	regionReceipt, err := mutation.New(
		app,
		mutation.MetadataSchemaSource{},
		mutation.WithComputationInvalidator(jobService),
	).Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion,
		RequestID:       "request-lookup-only-change", IdempotencyKey: "idem-lookup-only-change",
		TableID: region.TableID, SchemaRevision: regionDefinition.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationUpdate, RecordID: &regionRecord.Id,
			Values: map[string]any{regionName.Definition.Identity.PhysicalName: "North updated"},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lookupOnlyJob, err := app.FindFirstRecordByFilter(
		"vibetable_jobs",
		"job_type='formula_fanout' && source_event_id={:event}",
		dbx.Params{"event": regionReceipt.EmittedEvents[0]},
	)
	if err != nil || lookupOnlyJob.GetString("state") != "queued" {
		t.Fatalf("lookup-only invalidation job = %#v, err=%v", lookupOnlyJob, err)
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
	formulaScheduler.fail = true
	if _, applyErr := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: updatePlan.PlanID, PlanHash: updatePlan.PlanHash,
		OperationID: "op_v2_formula_update_failed", Actor: actor,
	}); applyErr == nil {
		t.Fatal("formula schema committed without its durable backfill job")
	}
	rolledBack, err := catalog.Field(ctx, source.TableID, formula.FieldID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Formula.Source != formula.Definition.Formula.Source {
		t.Fatalf("failed enqueue changed formula authority: %#v", rolledBack.Formula)
	}
	formulaScheduler.fail = false
	updatedFormula, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: updatePlan.PlanID, PlanHash: updatePlan.PlanHash,
		OperationID: "op_v2_formula_update", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedExecution, err := schemaapi.New(app).Describe(ctx, source.TableID)
	if err != nil {
		t.Fatal(err)
	}
	updatedExecutionField := integrationFieldByID(updatedExecution, formula.FieldID)
	updatedRuntime := updatedExecution.FormulaRuntime[formula.FieldID]
	if updatedExecutionField == nil || updatedExecutionField.Formula == nil ||
		updatedRuntime.Status != "backfilling" ||
		updatedRuntime.Version != 2 ||
		updatedFormula.Definition.Formula.ResultType != v2.LogicalNumber {
		t.Fatalf(
			"updated formula lifecycle: status=%q version=%d resultType=%q field=%#v",
			updatedRuntime.Status, updatedRuntime.Version, updatedFormula.Definition.Formula.ResultType,
			updatedExecutionField,
		)
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
	if len(formulaScheduler.enqueued) != 3 || len(formulaScheduler.started) != 2 {
		t.Fatalf(
			"formula job lifecycle = enqueued %#v, started %#v",
			formulaScheduler.enqueued,
			formulaScheduler.started,
		)
	}
}

func integrationFieldByID(
	definition schemaexecution.Table,
	fieldID string,
) *v2.FieldDefinition {
	for index := range definition.Snapshot.Fields {
		if definition.Snapshot.Fields[index].Identity.FieldID == fieldID {
			return &definition.Snapshot.Fields[index]
		}
	}
	return nil
}

type v2IntegrationTable struct {
	TableID        string
	PhysicalName   string
	SchemaRevision string
}

func createV2IntegrationTable(
	t *testing.T,
	ctx context.Context,
	app core.App,
	displayName string,
	operationID string,
) v2IntegrationTable {
	t.Helper()
	lifecycle, err := schemacore.NewTableLifecycle(app)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: displayName,
		OperationID: operationID,
		Actor:       v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": receipt.TableID},
	)
	if err != nil {
		t.Fatal(err)
	}
	return v2IntegrationTable{
		TableID: receipt.TableID, PhysicalName: metadata.GetString("physical_name"),
		SchemaRevision: receipt.SchemaRevision,
	}
}

func createV2IntegrationTableWithField(
	t *testing.T,
	ctx context.Context,
	app core.App,
	tableDisplayName string,
	fieldDisplayName string,
	operationPrefix string,
) (v2IntegrationTable, v2.ApplyReceipt) {
	t.Helper()
	table := createV2IntegrationTable(
		t, ctx, app, tableDisplayName, operationPrefix+"_table",
	)
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	field := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, fieldDisplayName),
		v2.Actor{ID: "local-user", Kind: "user"}, operationPrefix+"_field",
	)
	return table, field
}

func createV2IntegrationField(
	t *testing.T,
	ctx context.Context,
	app core.App,
	tableID string,
	draft v2.FieldDraft,
	operationID string,
) v2.ApplyReceipt {
	t.Helper()
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	return applyCreatedField(
		t, ctx, catalog, planner, executor, tableID, draft,
		v2.Actor{ID: "local-user", Kind: "user"}, operationID,
	)
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
