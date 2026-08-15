package integration_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func TestQueryDigestGuardsSchemaV2RowsWithEmptyMultiValues(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	table := createV2IntegrationTable(t, ctx, app, "V2 digest rows", "v2_digest_rows")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}

	title := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		actor, "op_v2_digest_title",
	)
	tagsDraft := fieldDraftForIntegration(t, v2.LogicalMultiSelect, "Tags")
	tagsDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{{
		OptionID: "opt_01JALPHA01", Label: "Alpha", Color: "blue", State: v2.OptionActive,
	}}}
	_ = applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		tagsDraft, actor, "op_v2_digest_tags",
	)
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}

	recordID := "v2digestrow0001"
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	if _, err := kernel.Apply(ctx, mutationRequest(
		table.TableID, revisions.Schema, "v2-digest-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "before"},
		},
	)); err != nil {
		t.Fatalf("insert v2 row: %#v", err)
	}
	source, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(app, source).QueryPage(ctx, table.TableID, query.TableQuery{Limit: 10})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("query v2 row: page=%#v err=%v", page, err)
	}
	digest, ok := page.Rows[0]["__vibetableDigest"].(string)
	if !ok || digest == "" {
		t.Fatalf("query digest = %#v", page.Rows[0]["__vibetableDigest"])
	}
	update := mutationRequest(
		table.TableID, revisions.Schema, "v2-digest-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "after"},
		},
	)
	update.ExpectedDigest = &digest
	if _, err := kernel.Apply(ctx, update); err != nil {
		t.Fatalf("QueryPort digest was rejected by MutationKernel: %#v", err)
	}
}

func TestQueryDigestGuardsSchemaV2RowsWhileFormulaIsBackfilling(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()
	table := createV2IntegrationTable(t, ctx, app, "V2 formula digest", "v2_formula_digest")
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(
		app, store,
		fieldchange.WithFormulaBackfillScheduler(&atomicFormulaScheduler{}),
	)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	title := applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		actor, "op_v2_formula_digest_title",
	)
	formulaDraft := fieldDraftForIntegration(t, v2.LogicalFormula, "Upper title")
	formulaDraft.Formula = &v2.FormulaDraftSpec{
		Language: "cel-v1", Source: "upper(" + title.Definition.Identity.PhysicalName + ")",
	}
	_ = applyCreatedField(
		t, ctx, catalog, planner, executor, table.TableID,
		formulaDraft, actor, "op_v2_formula_digest_computed",
	)
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	recordID := "v2formuladigest"
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	if _, err := kernel.Apply(ctx, mutationRequest(
		table.TableID, revisions.Schema, "v2-formula-digest-insert",
		mutation.Operation{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{}},
	)); err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(app, source).QueryPage(ctx, table.TableID, query.TableQuery{Limit: 10})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("query formula row: page=%#v err=%v", page, err)
	}
	digest, ok := page.Rows[0]["__vibetableDigest"].(string)
	if !ok || digest == "" {
		t.Fatalf("query digest = %#v", page.Rows[0]["__vibetableDigest"])
	}
	update := mutationRequest(
		table.TableID, revisions.Schema, "v2-formula-digest-update",
		mutation.Operation{
			Kind: mutation.OperationUpdate, RecordID: &recordID,
			Values: map[string]any{title.Definition.Identity.PhysicalName: "after"},
		},
	)
	update.ExpectedDigest = &digest
	if _, err := kernel.Apply(ctx, update); err != nil {
		t.Fatalf("QueryPort formula digest was rejected by MutationKernel: %#v", err)
	}
}

func TestSelectOptionIDsRoundTripThroughRealPocketBase(t *testing.T) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	table := createV2IntegrationTable(t, ctx, app, "Typed enums", "typed_enums_table")
	maxOne := 1
	statusDraft := fieldDraftForIntegration(t, v2.LogicalSelect, "Status")
	statusDraft.Constraints.Selection.Max = &maxOne
	statusDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: "opt_number_one", Label: "Number one", State: v2.OptionActive},
		{OptionID: "opt_string_one", Label: "String one", State: v2.OptionActive},
		{OptionID: "opt_enabled_one", Label: "Enabled", State: v2.OptionActive},
	}}
	status := createV2IntegrationField(
		t, ctx, app, table.TableID, statusDraft, "typed_enums_status",
	)
	maxTwo := 2
	flagsDraft := fieldDraftForIntegration(t, v2.LogicalMultiSelect, "Flags")
	flagsDraft.Constraints.Selection.Max = &maxTwo
	flagsDraft.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{OptionID: "opt_enabled_two", Label: "Enabled", State: v2.OptionActive},
		{OptionID: "opt_number_two", Label: "Number two", State: v2.OptionActive},
		{OptionID: "opt_string_two", Label: "String two", State: v2.OptionActive},
	}}
	flags := createV2IntegrationField(
		t, ctx, app, table.TableID, flagsDraft, "typed_enums_flags",
	)
	catalog := schemaapi.New(app)
	definition, err := catalog.Describe(ctx, table.TableID)
	if err != nil {
		t.Fatalf("describe typed enum table: %#v", err)
	}
	if status.Definition == nil || flags.Definition == nil {
		t.Fatal("V2 enum fixture omitted field definitions")
	}
	statusName := status.Definition.Identity.PhysicalName
	flagsName := flags.Definition.Identity.PhysicalName
	statusOptions := integrationFieldByID(definition, status.FieldID).Select.Options
	if len(statusOptions) != 3 || statusOptions[0].OptionID != "opt_number_one" ||
		statusOptions[1].OptionID != "opt_string_one" ||
		statusOptions[2].OptionID != "opt_enabled_one" {
		t.Fatalf("canonical select options = %#v", statusOptions)
	}

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	rows := []struct {
		id     string
		key    string
		status string
		flags  []any
	}{
		{"typedenum000001", "option-number", "opt_number_one", []any{"opt_enabled_two", "opt_number_two"}},
		{"typedenum000002", "option-string", "opt_string_one", []any{"opt_string_two"}},
		{"typedenum000003", "option-enabled", "opt_enabled_one", []any{"opt_enabled_two"}},
	}
	for _, row := range rows {
		if _, err := kernel.Apply(ctx, mutationRequest(
			definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, row.key,
			mutation.Operation{
				Kind: mutation.OperationInsert, RecordID: &row.id,
				Values: map[string]any{statusName: row.status, flagsName: row.flags},
			},
		)); err != nil {
			t.Fatalf("insert %s: %#v", row.key, err)
		}
	}
	collection, err := app.FindCollectionByNameOrId(definition.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	numberRecord, _ := app.FindRecordById(collection, rows[0].id)
	stringRecord, _ := app.FindRecordById(collection, rows[1].id)
	if numberRecord.GetString(statusName) == stringRecord.GetString(statusName) {
		t.Fatalf("stable option ids collapsed in storage: %q", numberRecord.GetString(statusName))
	}

	source, err := queryschema.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(
		app,
		source)

	page, err := port.QueryPage(ctx, definition.Snapshot.TableID, query.TableQuery{
		Sorts: []query.SortCondition{{Field: "id", Direction: query.SortAscending}},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("query typed enums: %v", err)
	}
	if len(page.Rows) != 3 {
		t.Fatalf("typed enum rows = %d", len(page.Rows))
	}
	if page.Rows[0][statusName] != "opt_number_one" ||
		!reflect.DeepEqual(page.Rows[0][flagsName], []any{"opt_enabled_two", "opt_number_two"}) ||
		page.Rows[1][statusName] != "opt_string_one" ||
		!reflect.DeepEqual(page.Rows[1][flagsName], []any{"opt_string_two"}) ||
		page.Rows[2][statusName] != "opt_enabled_one" {
		t.Fatalf("select option rows = %#v", page.Rows)
	}

	for _, testCase := range []struct {
		name  string
		value any
		id    string
	}{
		{"number", "opt_number_one", rows[0].id},
		{"string", "opt_string_one", rows[1].id},
		{"enabled", "opt_enabled_one", rows[2].id},
	} {
		t.Run("filter_"+testCase.name, func(t *testing.T) {
			filtered, err := port.QueryPage(ctx, definition.Snapshot.TableID, query.TableQuery{
				Filters: []query.FilterExpression{{
					Field: statusName, Operator: query.OperatorEqual, Value: testCase.value,
				}},
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("filter %s: %v", testCase.name, err)
			}
			if len(filtered.Rows) != 1 || filtered.Rows[0]["id"] != testCase.id {
				t.Fatalf("filter %s rows = %#v", testCase.name, filtered.Rows)
			}
		})
	}
}

func TestMutationKernelAppliesDefaultsAndValidatesClosedFieldSemantics(
	t *testing.T,
) {
	dataDir := queryTempDir(t)
	app := bootstrapApp(t, dataDir)
	defer resetApp(t, app)
	ctx := context.Background()

	table := createV2IntegrationTable(t, ctx, app, "Semantic rows", "semantic_rows_table")
	countDraft := fieldDraftForIntegration(t, v2.LogicalNumber, "Count")
	countDraft.Storage.Options.OnlyInt = true
	countDraft.Value.Required = true
	countDraft.Value.Default = v2.DefaultSpec{
		Enabled: true, Value: 3, Source: v2.DefaultUser,
		DefaultsVersion: v2.DefaultsVersion,
	}
	count := createV2IntegrationField(
		t, ctx, app, table.TableID, countDraft, "semantic_rows_count",
	)
	timeField := createV2IntegrationField(
		t, ctx, app, table.TableID,
		fieldDraftForIntegration(t, v2.LogicalTime, "Starts at"),
		"semantic_rows_time",
	)
	jsonDraft := fieldDraftForIntegration(t, v2.LogicalJSON, "Metadata")
	jsonDraft.JSON = &v2.JSONSpec{
		RootType: "object", MaxSize: 1024 * 1024,
		Schema: map[string]any{
			"type": "object", "required": []any{"count"},
			"properties": map[string]any{
				"count": map[string]any{"type": "integer", "minimum": 1},
			},
		},
	}
	jsonField := createV2IntegrationField(
		t, ctx, app, table.TableID, jsonDraft, "semantic_rows_json",
	)
	definition, err := schemaapi.New(app).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatalf("describe semantic table: %#v", err)
	}
	if count.Definition == nil || timeField.Definition == nil || jsonField.Definition == nil {
		t.Fatal("V2 semantic fixture omitted field definitions")
	}
	countName := count.Definition.Identity.PhysicalName
	timeName := timeField.Definition.Identity.PhysicalName
	jsonName := jsonField.Definition.Identity.PhysicalName

	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	recordID := "semanticrow0001"
	_, err = kernel.Apply(ctx, mutationRequest(
		definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "semantic-valid",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &recordID,
			Values: map[string]any{
				timeName: "23:59:59.125",
				jsonName: map[string]any{"count": float64(2)},
			},
		},
	))
	if err != nil {
		t.Fatalf("valid semantic mutation: %#v", err)
	}
	collection, _ := app.FindCollectionByNameOrId(table.PhysicalName)
	record, err := app.FindRecordById(collection, recordID)
	if err != nil || record.GetInt(countName) != 3 {
		t.Fatalf("default was not applied: record=%#v err=%v", record, err)
	}

	invalid := []struct {
		name  string
		field string
		value any
	}{
		{name: "time", field: timeName, value: "24:00:00"},
		{name: "jsonSchema", field: jsonName, value: map[string]any{"count": "wrong"}},
	}
	for index, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			badID := []string{"invalidrow00001", "invalidrow00002"}[index]
			_, err := kernel.Apply(ctx, mutationRequest(
				definition.Snapshot.TableID, definition.Snapshot.SchemaRevision, "semantic-"+testCase.name,
				mutation.Operation{
					Kind: mutation.OperationInsert, RecordID: &badID,
					Values: map[string]any{
						timeName:       "23:59:59",
						jsonName:       map[string]any{"count": float64(2)},
						testCase.field: testCase.value,
					},
				},
			))
			var productErr *mutation.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "mutation.field.invalid_value" ||
				productErr.Path == nil ||
				*productErr.Path != "operations[0].values."+testCase.field {
				t.Fatalf("invalid %s error = %#v", testCase.name, err)
			}
		})
	}
}

func TestRestrictDeletePolicyRejectsReferencedTarget(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	users := createV2IntegrationTable(t, ctx, app, "Restrict users", "restrict_users_table")
	posts := createV2IntegrationTable(t, ctx, app, "Restrict posts", "restrict_posts_table")
	name := createV2IntegrationField(
		t, ctx, app, users.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Name"),
		"restrict_users_name",
	)
	title := createV2IntegrationField(
		t, ctx, app, posts.TableID,
		fieldDraftForIntegration(t, v2.LogicalText, "Title"),
		"restrict_posts_title",
	)
	ownerDraft := fieldDraftForIntegration(t, v2.LogicalRelation, "Owner")
	ownerDraft.Relation = &v2.RelationSpec{
		TargetTableID: users.TableID, Cardinality: "one",
		DeletePolicy: "restrict", DisplayField: name.FieldID,
	}
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(app, store)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	ownerPlan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: posts.TableID,
		ExpectedSchemaRev: title.SchemaRevision, Draft: &ownerDraft, Actor: actor,
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "Posts", ReciprocalCardinality: "many",
			SourceDisplayFieldID: title.FieldID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := executor.Apply(ctx, v2.ApplyRequest{
		PlanID: ownerPlan.PlanID, PlanHash: ownerPlan.PlanHash,
		OperationID: "restrict_posts_owner", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if name.Definition == nil || title.Definition == nil || owner.Definition == nil {
		t.Fatal("V2 restrict fixture omitted field definitions")
	}
	if len(owner.Related) != 1 || owner.Related[0].Definition == nil {
		t.Fatalf("V2 restrict relation omitted reciprocal receipt: %#v", owner)
	}
	namePhysical := name.Definition.Identity.PhysicalName
	titlePhysical := title.Definition.Identity.PhysicalName
	ownerPhysical := owner.Definition.Identity.PhysicalName
	kernel := mutation.New(app, mutation.MetadataSchemaSource{})
	userID := "restrictusr0001"
	// PocketBase IDs are collection-local. A source row may legitimately have
	// the same ID as its target and must still count as a cross-table reference.
	postID := userID
	if _, err := kernel.Apply(ctx, mutationRequest(
		users.TableID, owner.Related[0].SchemaRevision, "restrict-user-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &userID,
			Values: map[string]any{namePhysical: "Ada"},
		},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Apply(ctx, mutationRequest(
		posts.TableID, owner.SchemaRevision, "restrict-post-insert",
		mutation.Operation{
			Kind: mutation.OperationInsert, RecordID: &postID,
			Values: map[string]any{titlePhysical: "Referenced", ownerPhysical: userID},
		},
	)); err != nil {
		t.Fatal(err)
	}

	_, err = kernel.Apply(ctx, mutationRequest(
		users.TableID, owner.Related[0].SchemaRevision, "restrict-user-delete",
		mutation.Operation{Kind: mutation.OperationDelete, RecordID: &userID},
	))
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "mutation.relation.restricted" {
		t.Fatalf("restricted delete error = %#v", err)
	}
	userCollection, _ := app.FindCollectionByNameOrId(users.PhysicalName)
	if _, findErr := app.FindRecordById(userCollection, userID); findErr != nil {
		t.Fatalf("restricted target was deleted: %v", findErr)
	}
}
