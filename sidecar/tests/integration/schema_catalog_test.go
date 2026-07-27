package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

var expectedInternalCollections = []string{
	"vibetable_tables", "vibetable_fields", "vibetable_formulas", "vibetable_relations",
	"vibetable_lookups", "vibetable_audit_events", "vibetable_idempotency_keys",
	"vibetable_outbox", "vibetable_jobs", "vibetable_attachment_meta",
	"vibetable_attachment_versions",
	"vibetable_shared_settings", "vibetable_dashboards", "vibetable_panels",
	"vibetable_presets", "vibetable_identifier_mappings", "vibetable_content_versions",
	"vibetable_workspace_index",
}

func TestSchemaCatalogFreshMigrationApplyAlterConflictAndRestart(t *testing.T) {
	dataDir := t.TempDir()
	app := bootstrapApp(t, dataDir)
	defer func() {
		if app != nil {
			resetApp(t, app)
		}
	}()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("second RunAllMigrations(): %v", err)
	}
	for _, name := range expectedInternalCollections {
		if !app.HasTable(name) {
			t.Fatalf("internal collection %q was not migrated", name)
		}
	}
	fieldsMeta, err := app.FindCollectionByNameOrId("vibetable_fields")
	if err != nil {
		t.Fatal(err)
	}
	if fieldsMeta.GetIndex("uniq_vibetable_fields_table_id_field_id") == "" {
		t.Fatal("composite field metadata uniqueness index is missing")
	}

	catalog := schemaapi.New(app)
	ctx := context.Background()
	definition := baseTable("orders", "orders", []schema.FieldDefinition{
		field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
	})
	definition.Indexes = []schema.IndexDefinition{{
		Name: "idx_orders_title", FieldIDs: []string{"title"},
	}}
	created, err := catalog.ApplyChange(ctx, schemaapi.Change{Definition: definition, ExpectedRevision: 0})
	if err != nil {
		t.Fatalf("ApplyChange(create): %#v", err)
	}
	if created.SchemaRevision != "schema_0001" {
		t.Fatalf("created revision = %q, want schema_0001", created.SchemaRevision)
	}
	collection, err := app.FindCollectionByNameOrId("orders")
	if err != nil {
		t.Fatalf("create people: %#v", err)
	}
	if collection.GetIndex("idx_orders_title") == "" {
		t.Fatal("compiled index is missing")
	}

	definition = created
	definition.Fields = append(definition.Fields, field("note", "note", schema.FieldKindScalar, schema.DataTypeLongText))
	altered, err := catalog.ApplyChange(ctx, schemaapi.Change{Definition: definition, ExpectedRevision: 1})
	if err != nil {
		t.Fatalf("ApplyChange(alter): %v", err)
	}
	if altered.SchemaRevision != "schema_0002" {
		t.Fatalf("altered revision = %q, want schema_0002", altered.SchemaRevision)
	}
	_, err = catalog.ApplyChange(ctx, schemaapi.Change{Definition: definition, ExpectedRevision: 1})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "schema.revision_conflict" {
		t.Fatalf("stale apply error = %#v, want revision conflict", err)
	}

	resetApp(t, app)
	app = nil
	app = bootstrapApp(t, dataDir)
	described, err := schemaapi.New(app).Describe(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if described.SchemaRevision != "schema_0002" || len(described.Fields) != 2 {
		t.Fatalf("restart definition mismatch: %#v", described)
	}
}

func TestSchemaCatalogAutoDateRolesRequireAnEmptyTableAndRemainStable(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	ctx := context.Background()
	catalog := schemaapi.New(app)

	created, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("autodate_notes", "autodate_notes", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId("autodate_notes")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("title", "existing")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	withCreatedAt := created
	withCreatedAt.Fields = append(
		append([]schema.FieldDefinition(nil), created.Fields...),
		autoDateField("created_at", schema.AutoDateRoleCreatedAt),
	)
	_, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: withCreatedAt, ExpectedRevision: 1,
	})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.autodate_backfill_required" ||
		productErr.Details["recordCount"] != int64(1) {
		t.Fatalf("non-empty autoDate alter = %#v", err)
	}
	current, describeErr := catalog.Describe(ctx, created.TableID)
	if describeErr != nil || current.SchemaRevision != "schema_0001" ||
		len(current.Fields) != 1 {
		t.Fatalf("failed alter left schema state: %#v, err=%v", current, describeErr)
	}
	collection, err = app.FindCollectionByNameOrId("autodate_notes")
	if err != nil {
		t.Fatal(err)
	}
	if collection.Fields.GetByName("created_at") != nil {
		t.Fatal("failed alter left a PocketBase field behind")
	}

	if err := app.Delete(record); err != nil {
		t.Fatal(err)
	}
	applied, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: withCreatedAt, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("empty-table autoDate alter: %v", err)
	}
	if applied.SchemaRevision != "schema_0002" {
		t.Fatalf("schema revision = %q", applied.SchemaRevision)
	}
	collection, err = app.FindCollectionByNameOrId("autodate_notes")
	if err != nil {
		t.Fatal(err)
	}
	pbField, ok := collection.Fields.GetByName("created_at").(*core.AutodateField)
	if !ok || !pbField.OnCreate || pbField.OnUpdate || pbField.System {
		t.Fatalf("compiled createdAt = %#v", pbField)
	}

	switched := applied
	switched.Fields = append([]schema.FieldDefinition(nil), applied.Fields...)
	switched.Fields[1].AutoDate = &schema.AutoDateSpec{
		Role: schema.AutoDateRoleUpdatedAt,
	}
	_, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: switched, ExpectedRevision: 2,
	})
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.autodate_role_immutable" {
		t.Fatalf("role switch = %#v", err)
	}

	diagnostics, err := catalog.InspectAutoDates(ctx)
	if err != nil || len(diagnostics) != 1 ||
		diagnostics[0].Status != "configured" ||
		diagnostics[0].SuggestedRole != schema.AutoDateRoleCreatedAt {
		t.Fatalf("configured diagnostics = %#v, err=%v", diagnostics, err)
	}
	legacy := applied
	legacy.Fields = append([]schema.FieldDefinition(nil), applied.Fields...)
	legacy.Fields[1].AutoDate = nil
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id='autodate_notes'",
	)
	if err != nil {
		t.Fatal(err)
	}
	meta.Set("definition_json", types.JSONRaw(raw))
	if err := app.Save(meta); err != nil {
		t.Fatal(err)
	}
	diagnostics, err = catalog.InspectAutoDates(ctx)
	if err != nil || len(diagnostics) != 1 ||
		diagnostics[0].Status != "legacy" ||
		diagnostics[0].DeclaredRole != "" ||
		diagnostics[0].SuggestedRole != schema.AutoDateRoleCreatedAt {
		t.Fatalf("legacy diagnostics = %#v, err=%v", diagnostics, err)
	}
	completed, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: applied, ExpectedRevision: 2,
	})
	if err != nil || completed.SchemaRevision != "schema_0003" {
		t.Fatalf("clean legacy metadata completion = %#v, err=%v", completed, err)
	}

	collection, err = app.FindCollectionByNameOrId("autodate_notes")
	if err != nil {
		t.Fatal(err)
	}
	pbField = collection.Fields.GetByName("created_at").(*core.AutodateField)
	for _, testCase := range []struct {
		name       string
		onCreate   bool
		onUpdate   bool
		wantStatus string
	}{
		{name: "true true conflict", onCreate: true, onUpdate: true, wantStatus: "conflict"},
		{name: "false true legacy", onCreate: false, onUpdate: true, wantStatus: "legacyUpdateOnly"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pbField.OnCreate = testCase.onCreate
			pbField.OnUpdate = testCase.onUpdate
			if err := app.Save(collection); err != nil {
				t.Fatal(err)
			}
			report, inspectErr := catalog.InspectAutoDates(ctx)
			if inspectErr != nil || len(report) != 1 ||
				report[0].Status != testCase.wantStatus {
				t.Fatalf("diagnostics = %#v, err=%v", report, inspectErr)
			}
		})
	}
	_, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: completed, ExpectedRevision: 3,
	})
	var conflict *schema.ProductError
	if !errors.As(err, &conflict) ||
		conflict.Code != "schema.field.autodate_role_conflict" {
		t.Fatalf("conflicting physical switches were silently repaired: %#v", err)
	}

	collection.Fields.Add(&core.AutodateField{
		Name: "untracked_clock", OnCreate: true, OnUpdate: false, System: false,
	})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	report, err := catalog.InspectAutoDates(ctx)
	foundUntracked := false
	for _, diagnostic := range report {
		if diagnostic.PhysicalName == "untracked_clock" &&
			diagnostic.Status == "untrackedPhysicalField" &&
			diagnostic.SuggestedRole == schema.AutoDateRoleCreatedAt {
			foundUntracked = true
		}
	}
	if err != nil || !foundUntracked {
		t.Fatalf("untracked physical autoDate report = %#v, err=%v", report, err)
	}
}

func TestSchemaCatalogProducerGateBlocksOnlyNewAutoDateMetadata(t *testing.T) {
	app := bootstrapApp(t, queryTempDir(t))
	defer resetApp(t, app)
	ctx := context.Background()
	t.Setenv("VIBETABLE_AUTODATE_FIELDS_ENABLED", "true")
	created, err := schemaapi.New(app).ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("producer_gate", "producer_gate", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
			autoDateField("created_at", schema.AutoDateRoleCreatedAt),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("VIBETABLE_AUTODATE_FIELDS_ENABLED", "false")
	disabled := schemaapi.New(app)
	unrelated := created
	unrelated.Fields = append(unrelated.Fields, field(
		"notes", "notes", schema.FieldKindScalar, schema.DataTypeShortText,
	))
	updated, err := disabled.ApplyChange(ctx, schemaapi.Change{
		Definition: unrelated, ExpectedRevision: 1,
	})
	if err != nil || updated.SchemaRevision != "schema_0002" {
		t.Fatalf("reader-preserving unrelated edit = %#v, err=%v", updated, err)
	}

	withNewRole := updated
	withNewRole.Fields = append(
		withNewRole.Fields,
		autoDateField("updated_at", schema.AutoDateRoleUpdatedAt),
	)
	_, err = disabled.ApplyChange(ctx, schemaapi.Change{
		Definition: withNewRole, ExpectedRevision: 2,
	})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.autodate_producer_disabled" {
		t.Fatalf("disabled producer accepted new autoDate = %#v", err)
	}
}

func TestSchemaCatalogOperationIDDurableReplayConflictAndExpiry(
	t *testing.T,
) {
	dataDir := t.TempDir()
	app := bootstrapApp(t, dataDir)
	defer func() {
		if app != nil {
			resetApp(t, app)
		}
	}()
	ctx := context.Background()
	change := schemaapi.Change{
		Definition: baseTable(
			"durable_orders",
			"durable_orders",
			[]schema.FieldDefinition{
				field(
					"title",
					"title",
					schema.FieldKindScalar,
					schema.DataTypeShortText,
				),
			},
		),
		ExpectedRevision: 0,
		OperationID:      "create-durable-orders",
	}
	applied, err := schemaapi.New(app).ApplyChange(ctx, change)
	if err != nil || applied.SchemaRevision != "schema_0001" {
		t.Fatalf("initial apply = %#v, err=%v", applied, err)
	}
	idempotency, err := app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys",
		"key='schema:create-durable-orders'",
	)
	if err != nil || idempotency.GetString("status") != "applied" {
		t.Fatalf("stored schema idempotency = %#v, err=%v", idempotency, err)
	}

	resetApp(t, app)
	app = nil
	app = bootstrapApp(t, dataDir)
	replayed, err := schemaapi.New(app).ApplyChange(ctx, change)
	if err != nil ||
		replayed.SchemaRevision != applied.SchemaRevision ||
		replayed.TableID != applied.TableID {
		t.Fatalf("durable replay = %#v, err=%v", replayed, err)
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_tables",
		"table_id='durable_orders'",
		"",
		0,
		0,
	)
	if err != nil || len(records) != 1 ||
		records[0].GetInt("schema_revision") != 1 {
		t.Fatalf("table after replay = %#v, err=%v", records, err)
	}
	alteredDefinition := applied
	alteredDefinition.DisplayName = "Durable orders v2"
	altered, err := schemaapi.New(app).ApplyChange(
		ctx,
		schemaapi.Change{
			Definition:       alteredDefinition,
			ExpectedRevision: 1,
			OperationID:      "alter-durable-orders",
		},
	)
	if err != nil || altered.SchemaRevision != "schema_0002" {
		t.Fatalf("later schema apply = %#v, err=%v", altered, err)
	}
	replayed, err = schemaapi.New(app).ApplyChange(ctx, change)
	if err != nil || replayed.SchemaRevision != "schema_0001" {
		t.Fatalf("historical durable replay = %#v, err=%v", replayed, err)
	}
	current, err := schemaapi.New(app).Describe(ctx, "durable_orders")
	if err != nil || current.SchemaRevision != "schema_0002" ||
		current.DisplayName != "Durable orders v2" {
		t.Fatalf("schema changed by historical replay = %#v, err=%v", current, err)
	}

	conflict := change
	conflict.Definition.DisplayName = "Different request"
	_, err = schemaapi.New(app).ApplyChange(ctx, conflict)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.idempotency_conflict" {
		t.Fatalf("operation id conflict = %#v", err)
	}

	idempotency, err = app.FindFirstRecordByFilter(
		"vibetable_idempotency_keys",
		"key='schema:create-durable-orders'",
	)
	if err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := json.Marshal(
		idempotency.GetRaw("receipt_json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	idempotency.Set("status", "pending")
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	_, err = schemaapi.New(app).ApplyChange(ctx, change)
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.storage.failed" {
		t.Fatalf("invalid idempotency status error = %#v", err)
	}

	idempotency.Set("status", "applied")
	idempotency.Set("receipt_json", types.JSONRaw(`{}`))
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	_, err = schemaapi.New(app).ApplyChange(ctx, change)
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.storage.failed" {
		t.Fatalf("invalid idempotency receipt error = %#v", err)
	}

	idempotency.Set("receipt_json", types.JSONRaw(originalReceipt))
	idempotency.Set("expires_at", time.Now().UTC().Add(-time.Minute))
	if err := app.Save(idempotency); err != nil {
		t.Fatal(err)
	}
	_, err = schemaapi.New(app).ApplyChange(ctx, change)
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.revision_conflict" {
		t.Fatalf("expired operation replay error = %#v", err)
	}
}

func TestSchemaCatalogDeleteChecksRevisionReferencesAndCleansMetadata(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	ctx := context.Background()
	target, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("customers", "customers", []schema.FieldDefinition{
			field("name", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceDefinition := baseTable("orders", "orders", []schema.FieldDefinition{
		field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
	})
	relationField := field(
		"customer", "customer", schema.FieldKindRelation, schema.DataTypeRelation,
	)
	relationField.Relation = &schema.RelationSpec{
		TargetTableID: "customers", Cardinality: "one", DeletePolicy: "setNull",
	}
	sourceDefinition.Fields = append(sourceDefinition.Fields, relationField)
	if _, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: sourceDefinition, ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = catalog.DeleteTable(ctx, target.TableID, 0)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "schema.revision_conflict" {
		t.Fatalf("stale delete error = %#v", err)
	}
	_, err = catalog.DeleteTable(ctx, target.TableID, 1)
	if !errors.As(err, &productErr) || productErr.Code != "schema.table.referenced" {
		t.Fatalf("referenced delete error = %#v", err)
	}

	source, err := catalog.Describe(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	source.Fields = source.Fields[:1]
	if _, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: source, ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := catalog.DeleteTable(ctx, target.TableID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Deleted || result.TableID != "customers" {
		t.Fatalf("unexpected delete result: %#v", result)
	}
	if _, err := app.FindCollectionByNameOrId("customers"); err == nil {
		t.Fatal("physical collection still exists")
	}
	if _, err := catalog.Describe(ctx, "customers"); err == nil {
		t.Fatal("catalog metadata still exists")
	}
}

func TestSchemaCatalogRenamePreservesPocketBaseFieldIDAndRecordValue(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	ctx := context.Background()
	created, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("people", "people", []schema.FieldDefinition{
			field("stable_name_id", "old_name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, _ := app.FindCollectionByNameOrId("people")
	oldFieldID := collection.Fields.GetByName("old_name").GetId()
	record := core.NewRecord(collection)
	record.Set("old_name", "Ada")
	if err := app.Save(record); err != nil {
		t.Fatalf("save record before rename: %v", err)
	}

	created.Fields[0].PhysicalName = "new_name"
	renamed, err := catalog.ApplyChange(ctx, schemaapi.Change{Definition: created, ExpectedRevision: 1})
	if err != nil {
		t.Fatalf("rename field: %v", err)
	}
	collection, _ = app.FindCollectionByNameOrId("people")
	newField := collection.Fields.GetByName("new_name")
	if newField == nil || newField.GetId() != oldFieldID {
		t.Fatalf("PB field id changed: old=%q new=%v", oldFieldID, newField)
	}
	reloaded, err := app.FindRecordById(collection, record.Id)
	if err != nil {
		t.Fatalf("reload renamed record: %v", err)
	}
	if got := reloaded.GetString("new_name"); got != "Ada" {
		t.Fatalf("renamed value = %q, want Ada; definition=%#v", got, renamed)
	}
}

func TestSchemaCatalogRejectsPersistedFieldTypeChangeBeforeTouchingData(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	ctx := context.Background()
	created, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: baseTable("people", "people", []schema.FieldDefinition{
			field("stable_name_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	collection, _ := app.FindCollectionByNameOrId("people")
	record := core.NewRecord(collection)
	record.Set("name", "Ada")
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	created.Fields[0].DataType = schema.DataTypeInteger
	created.Fields[0].StorageType = schema.StorageNumber
	_, err = catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: created, ExpectedRevision: 1,
	})
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.field.type_change_unsupported" {
		t.Fatalf("type change error = %#v", err)
	}
	collection, _ = app.FindCollectionByNameOrId("people")
	if collection.Fields.GetByName("name").GetId() == "" {
		t.Fatal("original field disappeared after rejected type change")
	}
	reloaded, err := app.FindRecordById(collection, record.Id)
	if err != nil || reloaded.GetString("name") != "Ada" {
		t.Fatalf("original record changed: record=%#v err=%v", reloaded, err)
	}
}

func TestSchemaCatalogAllowsSameFieldIDInDifferentTables(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	for _, table := range []string{"alpha", "beta"} {
		_, err := catalog.ApplyChange(context.Background(), schemaapi.Change{
			Definition: baseTable(table, table, []schema.FieldDefinition{
				field("shared_field_id", "name", schema.FieldKindScalar, schema.DataTypeShortText),
			}),
			ExpectedRevision: 0,
		})
		if err != nil {
			t.Fatalf("create %s with shared field id: %#v", table, err)
		}
	}
}

func TestSchemaCatalogFormulaMetadataLifecycleIsTransactional(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	ctx := context.Background()
	definition := baseTable("orders", "orders", []schema.FieldDefinition{
		field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeInteger),
		formulaField("total_id", "total", schema.DataTypeInteger, "quantity * 2"),
	})
	created, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: definition, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create formula table: %#v", err)
	}
	if created.Fields[1].Formula.Version != 1 ||
		created.Fields[1].Formula.Status != "backfilling" {
		t.Fatalf("created formula state = %#v", created.Fields[1].Formula)
	}
	metadata, err := app.FindFirstRecordByFilter(
		"vibetable_formulas",
		"table_id={:table} && field_id={:field}",
		map[string]any{"table": "orders", "field": "total_id"},
	)
	if err != nil {
		t.Fatalf("find formula metadata: %v", err)
	}
	if metadata.GetString("source") != "quantity * 2" ||
		metadata.GetString("language") != "cel-v1" ||
		metadata.GetString("result_type") != string(schema.DataTypeInteger) ||
		metadata.GetString("ast_hash") == "" ||
		metadata.GetInt("version") != 1 ||
		metadata.GetString("status") != "backfilling" {
		t.Fatalf("formula metadata = %#v", metadata)
	}
	var dependencies []string
	if err := json.Unmarshal(metadata.GetRaw("dependencies_json").(types.JSONRaw), &dependencies); err != nil {
		t.Fatalf("decode dependencies: %v", err)
	}
	if len(dependencies) != 1 || dependencies[0] != "quantity_id" {
		t.Fatalf("dependencies = %#v", dependencies)
	}

	created.DisplayName = "Orders renamed"
	stable, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: created, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatalf("update unrelated schema: %#v", err)
	}
	if stable.Fields[1].Formula.Version != 1 ||
		stable.Fields[1].Formula.Status != "backfilling" {
		t.Fatalf("unchanged formula state = %#v", stable.Fields[1].Formula)
	}

	stable.Fields[1].Formula.Source = "quantity * 3"
	updated, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: stable, ExpectedRevision: 2,
	})
	if err != nil {
		t.Fatalf("update formula: %#v", err)
	}
	if updated.Fields[1].Formula.Version != 2 ||
		updated.Fields[1].Formula.Status != "backfilling" {
		t.Fatalf("updated formula state = %#v", updated.Fields[1].Formula)
	}
	metadata, err = app.FindFirstRecordByFilter(
		"vibetable_formulas",
		"table_id={:table} && field_id={:field}",
		map[string]any{"table": "orders", "field": "total_id"},
	)
	if err != nil || metadata.GetInt("version") != 2 ||
		metadata.GetString("source") != "quantity * 3" {
		t.Fatalf("updated metadata = %#v err=%v", metadata, err)
	}

	updated.Fields = updated.Fields[:1]
	withoutFormula, err := catalog.ApplyChange(ctx, schemaapi.Change{
		Definition: updated, ExpectedRevision: 3,
	})
	if err != nil {
		t.Fatalf("remove formula: %#v", err)
	}
	if len(withoutFormula.Fields) != 1 {
		t.Fatalf("formula field was not removed: %#v", withoutFormula.Fields)
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_formulas", "table_id={:table}", "", 0, 0,
		map[string]any{"table": "orders"},
	)
	if err != nil || len(records) != 0 {
		t.Fatalf("stale formula metadata = %#v err=%v", records, err)
	}
}

func TestSchemaCatalogInvalidFormulaDoesNotCreateCollectionOrMetadata(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	definition := baseTable("orders", "orders", []schema.FieldDefinition{
		field("quantity_id", "quantity", schema.FieldKindScalar, schema.DataTypeInteger),
		formulaField("bad_id", "bad", schema.DataTypeInteger, "missing + 1"),
	})
	_, err := schemaapi.New(app).ApplyChange(context.Background(), schemaapi.Change{
		Definition: definition, ExpectedRevision: 0,
	})
	var formulaErr *formula.Error
	if !errors.As(err, &formulaErr) || formulaErr.Code != "formula.dependency" {
		t.Fatalf("invalid formula error = %#v", err)
	}
	if _, findErr := app.FindCollectionByNameOrId("orders"); !errors.Is(findErr, sql.ErrNoRows) {
		t.Fatalf("invalid formula left collection behind: %v", findErr)
	}
	records, findErr := app.FindRecordsByFilter(
		"vibetable_formulas", "table_id={:table}", "", 0, 0,
		map[string]any{"table": "orders"},
	)
	if findErr != nil || len(records) != 0 {
		t.Fatalf("invalid formula left metadata %#v err=%v", records, findErr)
	}
}

func TestSchemaCatalogCreatesSelfRelation(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	relation := field("parent_id", "parent", schema.FieldKindRelation, schema.DataTypeRelation)
	relation.Relation = &schema.RelationSpec{
		TargetTableID: "nodes", Cardinality: "one", DeletePolicy: "setNull",
	}
	relation.Constraints = []schema.FieldConstraint{{
		Kind: schema.ConstraintRelation, TargetTableID: "nodes",
		Cardinality: "one", DeletePolicy: "setNull",
	}}
	lookup := field("parent_label_id", "parent_label", schema.FieldKindLookup, schema.DataTypeLookup)
	lookup.StorageType = schema.StorageText
	lookup.ReadOnly = true
	lookup.Lookup = &schema.LookupSpec{
		RelationFieldID: "parent_id", TargetFieldID: "label", Aggregate: "first",
	}
	created, err := schemaapi.New(app).ApplyChange(context.Background(), schemaapi.Change{
		Definition: baseTable("nodes", "nodes", []schema.FieldDefinition{
			field("label", "label", schema.FieldKindScalar, schema.DataTypeShortText),
			relation,
			lookup,
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("create self relation: %#v", err)
	}
	collection, _ := app.FindCollectionByNameOrId("nodes")
	pbRelation, ok := collection.Fields.GetByName("parent").(*core.RelationField)
	if !ok || pbRelation.CollectionId != collection.Id {
		t.Fatalf("self relation target = %#v, collection=%q", pbRelation, collection.Id)
	}
	relationMeta, err := app.FindFirstRecordByFilter(
		"vibetable_relations", "source_table_id='nodes' && source_field_id='parent_id'",
	)
	if err != nil ||
		relationMeta.GetString("relation_id") != "nodes.parent_id" ||
		relationMeta.GetString("target_table_id") != "nodes" {
		t.Fatalf("relation metadata = %#v, err=%v", relationMeta, err)
	}
	lookupMeta, err := app.FindFirstRecordByFilter(
		"vibetable_lookups", "table_id='nodes' && field_id='parent_label_id'",
	)
	if err != nil ||
		lookupMeta.GetString("lookup_id") != "nodes.parent_label_id" ||
		lookupMeta.GetString("relation_field_id") != "parent_id" ||
		lookupMeta.GetString("target_field_id") != "label" {
		t.Fatalf("lookup metadata = %#v, err=%v", lookupMeta, err)
	}

	created.Fields = created.Fields[:1]
	updated, err := schemaapi.New(app).ApplyChange(context.Background(), schemaapi.Change{
		Definition: created, ExpectedRevision: 1,
	})
	if err != nil || updated.SchemaRevision != schema.FormatSchemaRevision(2) {
		t.Fatalf("remove relation metadata: %#v, err=%v", updated, err)
	}
	for _, collectionName := range []string{"vibetable_relations", "vibetable_lookups"} {
		records, findErr := app.FindRecordsByFilter(collectionName, "", "", 0, 0)
		if findErr != nil || len(records) != 0 {
			t.Fatalf("%s metadata after removal = %#v, err=%v", collectionName, records, findErr)
		}
	}
}

func TestSchemaCatalogValidatesLookupReferencesAndOutputType(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*schema.FieldDefinition)
		wantCode string
	}{
		{
			name: "missing relation",
			mutate: func(lookup *schema.FieldDefinition) {
				lookup.Lookup.RelationFieldID = "missing"
			},
			wantCode: "schema.lookup.relation_not_found",
		},
		{
			name: "missing target",
			mutate: func(lookup *schema.FieldDefinition) {
				lookup.Lookup.TargetFieldID = "missing"
			},
			wantCode: "schema.lookup.target_field_not_found",
		},
		{
			name: "output mismatch",
			mutate: func(lookup *schema.FieldDefinition) {
				lookup.StorageType = schema.StorageNumber
			},
			wantCode: "schema.lookup.output_type_mismatch",
		},
		{
			name: "missing later path relation",
			mutate: func(lookup *schema.FieldDefinition) {
				lookup.Lookup.Path = []schema.LookupPathStep{
					{RelationFieldID: "parent_id"},
					{RelationFieldID: "missing"},
				}
			},
			wantCode: "schema.lookup.relation_not_found",
		},
		{
			name: "m2a collection on direct relation",
			mutate: func(lookup *schema.FieldDefinition) {
				lookup.Lookup.Path = []schema.LookupPathStep{{
					RelationFieldID: "parent_id",
					M2ACollection:   "nodes",
				}}
			},
			wantCode: "schema.lookup.m2a_collection_invalid",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := bootstrapApp(t, t.TempDir())
			defer resetApp(t, app)
			relation := field(
				"parent_id", "parent",
				schema.FieldKindRelation, schema.DataTypeRelation,
			)
			relation.Relation = &schema.RelationSpec{
				TargetTableID: "nodes", Cardinality: "one", DeletePolicy: "setNull",
			}
			relation.Constraints = []schema.FieldConstraint{{
				Kind: schema.ConstraintRelation, TargetTableID: "nodes",
				Cardinality: "one", DeletePolicy: "setNull",
			}}
			lookup := field(
				"parent_label_id", "parent_label",
				schema.FieldKindLookup, schema.DataTypeLookup,
			)
			lookup.StorageType = schema.StorageText
			lookup.ReadOnly = true
			lookup.Lookup = &schema.LookupSpec{
				RelationFieldID: "parent_id",
				TargetFieldID:   "label",
				Aggregate:       "first",
			}
			testCase.mutate(&lookup)
			_, err := schemaapi.New(app).ValidateChange(
				context.Background(),
				schemaapi.Change{
					Definition: baseTable(
						"nodes", "nodes",
						[]schema.FieldDefinition{
							field(
								"label", "label",
								schema.FieldKindScalar, schema.DataTypeShortText,
							),
							relation,
							lookup,
						},
					),
					ExpectedRevision: 0,
				},
			)
			var productErr *schema.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != testCase.wantCode {
				t.Fatalf("validation error = %#v", err)
			}
		})
	}
}

func TestSchemaCatalogRejectsHashLookupTarget(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	hashField := field(
		"hash_id", "password_hash",
		schema.FieldKindScalar, schema.DataTypeHash,
	)
	hashField.Editor.Kind = "hash"
	relation := field(
		"parent_id", "parent",
		schema.FieldKindRelation, schema.DataTypeRelation,
	)
	relation.Relation = &schema.RelationSpec{
		TargetTableID: "secure_nodes",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}
	relation.Constraints = []schema.FieldConstraint{{
		Kind:          schema.ConstraintRelation,
		TargetTableID: "secure_nodes",
		Cardinality:   "one",
		DeletePolicy:  "setNull",
	}}
	lookup := field(
		"hash_lookup_id", "hash_lookup",
		schema.FieldKindLookup, schema.DataTypeLookup,
	)
	lookup.StorageType = schema.StorageText
	lookup.ReadOnly = true
	lookup.Lookup = &schema.LookupSpec{
		RelationFieldID: "parent_id",
		TargetFieldID:   "hash_id",
		Aggregate:       "first",
	}
	_, err := schemaapi.New(app).ValidateChange(
		context.Background(),
		schemaapi.Change{
			Definition: baseTable(
				"secure_nodes",
				"secure_nodes",
				[]schema.FieldDefinition{hashField, relation, lookup},
			),
			ExpectedRevision: 0,
		},
	)
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.lookup.target_field_not_found" {
		t.Fatalf("hash lookup validation error = %#v", err)
	}
}

func TestSchemaCatalogRejectsInconsistentStoredRevisionMetadata(t *testing.T) {
	app := bootstrapApp(t, t.TempDir())
	defer resetApp(t, app)
	catalog := schemaapi.New(app)
	created, err := catalog.ApplyChange(context.Background(), schemaapi.Change{
		Definition: baseTable("orders", "orders", []schema.FieldDefinition{
			field("title", "title", schema.FieldKindScalar, schema.DataTypeShortText),
		}),
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	created.SchemaRevision = "schema_0009"
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id='orders'",
	)
	if err != nil {
		t.Fatal(err)
	}
	meta.Set("definition_json", types.JSONRaw(raw))
	if err := app.Save(meta); err != nil {
		t.Fatal(err)
	}

	_, err = catalog.Describe(context.Background(), "orders")
	var productErr *schema.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "schema.metadata.revision_mismatch" {
		t.Fatalf("Describe() error = %#v", err)
	}
}

func bootstrapApp(t *testing.T, dataDir string) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	if err := app.RunAllMigrations(); err != nil {
		resetApp(t, app)
		t.Fatalf("RunAllMigrations(): %v", err)
	}
	return app
}

func resetApp(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	if err := app.ResetBootstrapState(); err != nil {
		t.Errorf("ResetBootstrapState(): %v", err)
	}
}

func baseTable(id, name string, fields []schema.FieldDefinition) schema.TableDefinition {
	return schema.TableDefinition{
		ContractVersion: schema.ContractVersion,
		TableID:         id, PhysicalName: name, DisplayName: name,
		Kind: schema.TableKindBase, SchemaRevision: schema.FormatSchemaRevision(0),
		ArchivePolicy: schema.ArchivePolicy{Mode: schema.ArchiveModeNone},
		Fields:        fields, Indexes: []schema.IndexDefinition{},
	}
}

func field(id, name string, kind schema.FieldKind, dataType schema.DataType) schema.FieldDefinition {
	capability, _ := schema.CapabilityFor(dataType)
	editorKind := "text"
	if dataType == schema.DataTypeLongText {
		editorKind = "textarea"
	}
	return schema.FieldDefinition{
		FieldID: id, PhysicalName: name, DisplayName: name,
		Kind: kind, DataType: dataType, StorageType: capability.Storage, Nullable: true,
		Constraints: []schema.FieldConstraint{},
		Editor:      schema.EditorDefinition{Kind: editorKind, Config: map[string]any{}},
	}
}

func autoDateField(id string, role schema.AutoDateRole) schema.FieldDefinition {
	return schema.FieldDefinition{
		FieldID: id, PhysicalName: id, DisplayName: id,
		Kind: schema.FieldKindSystem, DataType: schema.DataTypeAutoDate,
		StorageType: schema.StorageAutodate, Nullable: false, ReadOnly: true,
		Constraints: []schema.FieldConstraint{},
		Editor:      schema.EditorDefinition{Kind: "readonly", Config: map[string]any{}},
		AutoDate:    &schema.AutoDateSpec{Role: role},
	}
}

func formulaField(id, name string, resultType schema.DataType, source string) schema.FieldDefinition {
	result := field(id, name, schema.FieldKindFormula, schema.DataTypeFormula)
	capability, _ := schema.CapabilityFor(resultType)
	result.StorageType = capability.Storage
	result.ReadOnly = true
	result.Nullable = false
	result.Formula = &schema.FormulaSpec{
		Language: "cel-v1", Source: source, ResultType: resultType,
		Version: 99, Status: "ready",
	}
	return result
}
