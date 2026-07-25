package migrations

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

type internalField struct {
	name     string
	kind     string
	required bool
}

type internalCollection struct {
	name         string
	fields       []internalField
	unique       []string
	uniqueGroups [][]string
}

var internalCollections = []internalCollection{
	{name: "vibetable_tables", fields: []internalField{
		{name: "table_id", kind: "text", required: true},
		{name: "collection_id", kind: "text", required: true},
		{name: "physical_name", kind: "text", required: true},
		{name: "display_name", kind: "text", required: true},
		{name: "kind", kind: "text", required: true},
		{name: "schema_revision", kind: "number", required: true},
		// PocketBase treats numeric zero as blank for Required number fields.
		// Keep the storage field optional and enforce presence in application
		// code so the initial data revision can remain exactly zero.
		{name: "data_revision", kind: "number"},
		{name: "archive_policy", kind: "text", required: true},
		{name: "definition_json", kind: "json", required: true},
	}, unique: []string{"table_id", "physical_name"}},
	{name: "vibetable_fields", fields: []internalField{
		{name: "table_id", kind: "text", required: true},
		{name: "field_id", kind: "text", required: true},
		{name: "physical_name", kind: "text", required: true},
		{name: "display_name", kind: "text", required: true},
		{name: "kind", kind: "text", required: true},
		{name: "data_type", kind: "text", required: true},
		{name: "storage_type", kind: "text", required: true},
		// PocketBase considers an empty JSON array blank for Required fields,
		// while the v1 contract explicitly permits constraints: [].
		{name: "constraints_json", kind: "json"},
		{name: "editor_json", kind: "json"},
	}, uniqueGroups: [][]string{{"table_id", "field_id"}}},
	{name: "vibetable_formulas", fields: []internalField{
		{name: "table_id", kind: "text", required: true}, {name: "field_id", kind: "text", required: true},
		{name: "source", kind: "text", required: true},
		{name: "language", kind: "text", required: true}, {name: "result_type", kind: "text", required: true},
		{name: "ast_hash", kind: "text"}, {name: "dependencies_json", kind: "json"},
		{name: "version", kind: "number", required: true}, {name: "status", kind: "text", required: true},
	}, uniqueGroups: [][]string{{"table_id", "field_id"}}},
	{name: "vibetable_formula_dependencies", fields: []internalField{
		{name: "source_table_id", kind: "text", required: true},
		{name: "formula_field_id", kind: "text", required: true},
		{name: "relation_field_id", kind: "text", required: true},
		{name: "target_table_id", kind: "text", required: true},
		{name: "target_field_id", kind: "text", required: true},
		{name: "dependency_kind", kind: "text", required: true},
	}, uniqueGroups: [][]string{{
		"source_table_id", "formula_field_id", "relation_field_id",
		"target_table_id", "target_field_id",
	}}},
	{name: "vibetable_relations", fields: []internalField{
		{name: "relation_id", kind: "text", required: true}, {name: "source_table_id", kind: "text", required: true},
		{name: "source_field_id", kind: "text", required: true},
		{name: "target_table_id", kind: "text", required: true}, {name: "cardinality", kind: "text", required: true},
		{name: "junction_table_id", kind: "text"}, {name: "delete_policy", kind: "text", required: true},
	}, unique: []string{"relation_id"}, uniqueGroups: [][]string{{"source_table_id", "source_field_id"}}},
	{name: "vibetable_lookups", fields: []internalField{
		{name: "lookup_id", kind: "text", required: true}, {name: "path_json", kind: "json", required: true},
		{name: "table_id", kind: "text", required: true}, {name: "field_id", kind: "text", required: true},
		{name: "relation_field_id", kind: "text", required: true},
		{name: "target_field_id", kind: "text", required: true},
		{name: "aggregate", kind: "text"}, {name: "output_type", kind: "text", required: true},
		{name: "revision", kind: "number", required: true},
	}, unique: []string{"lookup_id"}, uniqueGroups: [][]string{{"table_id", "field_id"}}},
	{name: "vibetable_audit_events", fields: []internalField{
		{name: "change_set_id", kind: "text", required: true}, {name: "sequence", kind: "number", required: true},
		{name: "data_revision", kind: "number", required: true},
		{name: "table_id", kind: "text", required: true}, {name: "record_id", kind: "text", required: true},
		{name: "operation", kind: "text", required: true}, {name: "before_json", kind: "json"},
		{name: "after_json", kind: "json"}, {name: "schema_revision", kind: "number", required: true},
		{name: "request_id", kind: "text", required: true},
		{name: "actor_type", kind: "text", required: true},
		{name: "actor_id", kind: "text", required: true},
		{name: "actor_display_name", kind: "text"},
		// Base collections do not automatically expose a created timestamp.
		// Persist the authoritative event time so repeated archive/restore
		// cycles have a deterministic latest-before-image ordering.
		{name: "occurred_at", kind: "date", required: true},
	}, uniqueGroups: [][]string{{"change_set_id", "sequence"}}},
	{name: "vibetable_idempotency_keys", fields: []internalField{
		{name: "key", kind: "text", required: true}, {name: "request_hash", kind: "text", required: true},
		{name: "status", kind: "text", required: true}, {name: "receipt_json", kind: "json"},
		{name: "expires_at", kind: "date", required: true},
	}, unique: []string{"key"}},
	{name: "vibetable_outbox", fields: []internalField{
		{name: "event_id", kind: "text", required: true}, {name: "topic", kind: "text", required: true},
		{name: "payload_json", kind: "json", required: true}, {name: "status", kind: "text", required: true},
		// PocketBase treats numeric zero as blank for Required NumberField.
		// Outbox attempts must start at zero, so the application enforces the
		// non-negative integer invariant while the physical field stays optional.
		{name: "attempts", kind: "number"},
	}, unique: []string{"event_id"}},
	{name: "vibetable_jobs", fields: []internalField{
		{name: "job_type", kind: "text", required: true}, {name: "state", kind: "text", required: true},
		{name: "cursor_json", kind: "json"}, {name: "progress_json", kind: "json"},
		{name: "error_json", kind: "json"}, {name: "schema_revision", kind: "number", required: true},
		{name: "source_event_id", kind: "text"}, {name: "source_table_id", kind: "text"},
		{name: "relation_field_id", kind: "text"},
	}, uniqueGroups: [][]string{{
		"job_type", "source_event_id", "source_table_id", "relation_field_id",
	}}},
	{name: "vibetable_attachment_meta", fields: []internalField{
		{name: "table_id", kind: "text", required: true}, {name: "record_id", kind: "text", required: true},
		{name: "field_id", kind: "text", required: true}, {name: "stored_name", kind: "text", required: true},
		{name: "original_name", kind: "text", required: true}, {name: "mime", kind: "text", required: true},
		{name: "size", kind: "number", required: true}, {name: "hash", kind: "text", required: true},
	}, uniqueGroups: [][]string{{"table_id", "record_id", "field_id", "stored_name"}}},
	{name: "vibetable_attachment_versions", fields: []internalField{
		{name: "table_id", kind: "text", required: true}, {name: "record_id", kind: "text", required: true},
		{name: "field_id", kind: "text", required: true}, {name: "field_name", kind: "text", required: true},
		{name: "stored_name", kind: "text", required: true},
		{name: "original_name", kind: "text", required: true}, {name: "mime", kind: "text", required: true},
		{name: "size", kind: "number", required: true}, {name: "hash", kind: "text", required: true},
		{name: "blob", kind: "file", required: true},
	}, uniqueGroups: [][]string{{"table_id", "record_id", "field_id", "stored_name"}}},
	{name: "vibetable_shared_settings", fields: []internalField{
		{name: "key", kind: "text", required: true}, {name: "value_json", kind: "json", required: true},
		{name: "revision", kind: "number", required: true},
	}, unique: []string{"key"}},
	{name: "vibetable_dashboards", fields: []internalField{
		{name: "dashboard_id", kind: "text", required: true}, {name: "layout_json", kind: "json", required: true},
		{name: "display_json", kind: "json"}, {name: "revision", kind: "number", required: true},
	}, unique: []string{"dashboard_id"}},
	{name: "vibetable_panels", fields: []internalField{
		{name: "panel_id", kind: "text", required: true}, {name: "dashboard_id", kind: "text", required: true},
		{name: "query_json", kind: "json", required: true}, {name: "display_json", kind: "json"},
		{name: "revision", kind: "number", required: true},
	}, unique: []string{"panel_id"}},
	{name: "vibetable_presets", fields: []internalField{
		{name: "preset_id", kind: "text", required: true}, {name: "scope", kind: "text", required: true},
		{name: "table_id", kind: "text"}, {name: "projection_json", kind: "json", required: true},
		{name: "revision", kind: "number", required: true},
	}, unique: []string{"preset_id"}},
	{name: "vibetable_identifier_mappings", fields: []internalField{
		{name: "entity_kind", kind: "text", required: true}, {name: "parent_id", kind: "text"},
		{name: "physical_name", kind: "text", required: true}, {name: "display_name", kind: "text", required: true},
		{name: "aliases_json", kind: "json"}, {name: "origin", kind: "text", required: true},
		{name: "status", kind: "text", required: true},
	}},
	{name: "vibetable_content_versions", fields: []internalField{
		{name: "table_id", kind: "text", required: true}, {name: "record_id", kind: "text", required: true},
		{name: "name", kind: "text", required: true}, {name: "change_set_id", kind: "text", required: true},
		{name: "created_at", kind: "date", required: true},
	}},
	{name: "vibetable_workspace_index", fields: []internalField{
		{name: "document_id", kind: "text", required: true}, {name: "record_link_json", kind: "json"},
		{name: "published_revision", kind: "number", required: true}, {name: "outbox_state", kind: "text", required: true},
	}, unique: []string{"document_id"}},
}

func init() {
	m.Register(func(app core.App) error {
		for _, definition := range internalCollections {
			if existing, err := app.FindCollectionByNameOrId(definition.name); err == nil {
				if err := validateInternalCollection(existing, definition); err != nil {
					return err
				}
				continue
			}
			collection := buildInternalCollection(definition)
			if err := app.Save(collection); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for index := len(internalCollections) - 1; index >= 0; index-- {
			collection, err := app.FindCollectionByNameOrId(internalCollections[index].name)
			if err != nil {
				continue
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		return nil
	})
}

func buildInternalCollection(definition internalCollection) *core.Collection {
	collection := core.NewBaseCollection(definition.name)
	for _, field := range definition.fields {
		switch field.kind {
		case "text":
			collection.Fields.Add(&core.TextField{
				Name: field.name, Required: field.required, Max: 10000,
			})
		case "number":
			collection.Fields.Add(&core.NumberField{
				Name: field.name, Required: field.required,
			})
		case "json":
			collection.Fields.Add(&core.JSONField{
				Name: field.name, Required: field.required,
			})
		case "date":
			collection.Fields.Add(&core.DateField{
				Name: field.name, Required: field.required,
			})
		case "file":
			collection.Fields.Add(&core.FileField{
				Name: field.name, Required: field.required,
				MaxSelect: 1, MaxSize: 100 << 20, Protected: true,
			})
		}
	}
	for _, field := range definition.unique {
		collection.AddIndex(
			"uniq_"+definition.name+"_"+field,
			true,
			"`"+field+"`",
			"",
		)
	}
	for _, fields := range definition.uniqueGroups {
		columns := ""
		indexName := "uniq_" + definition.name
		for fieldIndex, field := range fields {
			if fieldIndex > 0 {
				columns += ", "
			}
			columns += "`" + field + "`"
			indexName += "_" + field
		}
		collection.AddIndex(indexName, true, columns, "")
	}
	return collection
}

func validateInternalCollection(
	existing *core.Collection,
	definition internalCollection,
) error {
	expected := buildInternalCollection(definition)
	for _, fieldDefinition := range definition.fields {
		actual := existing.Fields.GetByName(fieldDefinition.name)
		want := expected.Fields.GetByName(fieldDefinition.name)
		if actual == nil ||
			reflect.TypeOf(actual) != reflect.TypeOf(want) ||
			!internalFieldShapeMatches(actual, want) {
			return fmt.Errorf(
				"internal collection %s has incompatible field %s",
				definition.name,
				fieldDefinition.name,
			)
		}
	}
	actualCustomFields := 0
	for _, field := range existing.Fields {
		if !field.GetSystem() {
			actualCustomFields++
		}
	}
	if actualCustomFields != len(definition.fields) {
		return fmt.Errorf(
			"internal collection %s has an incompatible field set",
			definition.name,
		)
	}
	actualIndexes := append([]string(nil), existing.Indexes...)
	expectedIndexes := append([]string(nil), expected.Indexes...)
	sort.Strings(actualIndexes)
	sort.Strings(expectedIndexes)
	if !reflect.DeepEqual(actualIndexes, expectedIndexes) {
		return fmt.Errorf(
			"internal collection %s has an incompatible index set",
			definition.name,
		)
	}
	return nil
}

func internalFieldShapeMatches(actual, expected core.Field) bool {
	switch typed := actual.(type) {
	case *core.TextField:
		want := expected.(*core.TextField)
		return typed.Required == want.Required &&
			typed.Min == want.Min &&
			typed.Max == want.Max &&
			typed.Pattern == want.Pattern
	case *core.NumberField:
		want := expected.(*core.NumberField)
		return typed.Required == want.Required &&
			typed.OnlyInt == want.OnlyInt &&
			reflect.DeepEqual(typed.Min, want.Min) &&
			reflect.DeepEqual(typed.Max, want.Max)
	case *core.JSONField:
		want := expected.(*core.JSONField)
		return typed.Required == want.Required &&
			typed.MaxSize == want.MaxSize
	case *core.DateField:
		want := expected.(*core.DateField)
		return typed.Required == want.Required &&
			typed.Min == want.Min &&
			typed.Max == want.Max
	case *core.FileField:
		want := expected.(*core.FileField)
		return typed.Required == want.Required &&
			typed.MaxSelect == want.MaxSelect &&
			typed.MaxSize == want.MaxSize &&
			typed.Protected == want.Protected &&
			reflect.DeepEqual(typed.MimeTypes, want.MimeTypes) &&
			reflect.DeepEqual(typed.Thumbs, want.Thumbs)
	default:
		return false
	}
}
