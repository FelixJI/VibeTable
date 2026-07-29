package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

var fieldSettingsV2FieldUpgrades = map[string][]internalField{
	"vibetable_fields": {
		{name: "schema_model_version", kind: "number"},
		{name: "lifecycle_state", kind: "text"},
		{name: "retired_at", kind: "date"},
		{name: "identity_json", kind: "json"},
		{name: "value_semantics_json", kind: "json"},
		{name: "constraints_v2_json", kind: "json"},
		{name: "storage_v2_json", kind: "json"},
		{name: "display_v2_json", kind: "json"},
		{name: "recommended_defaults_version", kind: "number"},
		{name: "definition_hash", kind: "text"},
		{name: "definition_v2_json", kind: "json"},
	},
	"vibetable_jobs": {
		{name: "plan_id", kind: "text"},
		{name: "field_id", kind: "text"},
		{name: "before_definition_json", kind: "json"},
		{name: "after_definition_json", kind: "json"},
		{name: "phase", kind: "text"},
		{name: "shadow_identities_json", kind: "json"},
		{name: "write_lock_owner", kind: "text"},
		{name: "cleanup_state", kind: "text"},
	},
}

var fieldSettingsV2Collections = []internalCollection{
	{
		name: "vibetable_schema_change_plans",
		fields: []internalField{
			{name: "plan_id", kind: "text", required: true},
			{name: "intent_hash", kind: "text", required: true},
			{name: "plan_hash", kind: "text", required: true},
			{name: "table_id", kind: "text", required: true},
			{name: "field_id", kind: "text"},
			{name: "action", kind: "text", required: true},
			{name: "expected_schema_revision", kind: "text", required: true},
			{name: "expected_data_revision", kind: "number"},
			{name: "plan_json", kind: "json", required: true},
			{name: "actor_json", kind: "json", required: true},
			{name: "status", kind: "text", required: true},
			{name: "expires_at", kind: "date", required: true},
			{name: "applied_operation_id", kind: "text"},
		},
		unique: []string{"plan_id"},
	},
	{
		name: "vibetable_schema_audit",
		fields: []internalField{
			{name: "operation_id", kind: "text", required: true},
			{name: "plan_id", kind: "text", required: true},
			{name: "action", kind: "text", required: true},
			{name: "table_id", kind: "text", required: true},
			{name: "field_id", kind: "text"},
			{name: "before_hash", kind: "text"},
			{name: "after_hash", kind: "text"},
			{name: "before_definition_json", kind: "json"},
			{name: "after_definition_json", kind: "json"},
			{name: "outcome", kind: "text", required: true},
			{name: "error_code", kind: "text"},
			{name: "actor_json", kind: "json", required: true},
			{name: "occurred_at", kind: "date", required: true},
			{name: "migration_job_id", kind: "text"},
			{name: "backup_receipt", kind: "text"},
		},
		unique: []string{"operation_id"},
	},
}

func init() {
	m.Register(
		func(app core.App) error {
			for collectionName, fields := range fieldSettingsV2FieldUpgrades {
				collection, err := app.FindCollectionByNameOrId(collectionName)
				if err != nil {
					return fmt.Errorf("find %s for field settings v2: %w", collectionName, err)
				}
				for _, field := range fields {
					if collection.Fields.GetByName(field.name) == nil {
						addInternalField(collection, field)
					}
				}
				if err := app.Save(collection); err != nil {
					return fmt.Errorf("extend %s for field settings v2: %w", collectionName, err)
				}
			}
			for _, definition := range fieldSettingsV2Collections {
				if existing, err := app.FindCollectionByNameOrId(definition.name); err == nil {
					if err := validateInternalCollection(existing, definition); err != nil {
						return err
					}
					continue
				}
				if err := app.Save(buildInternalCollection(definition)); err != nil {
					return fmt.Errorf("create %s: %w", definition.name, err)
				}
			}
			return nil
		},
		func(app core.App) error {
			for index := len(fieldSettingsV2Collections) - 1; index >= 0; index-- {
				collection, err := app.FindCollectionByNameOrId(
					fieldSettingsV2Collections[index].name,
				)
				if err == nil {
					if err := app.Delete(collection); err != nil {
						return fmt.Errorf("delete %s: %w", collection.Name, err)
					}
				}
			}
			for collectionName, fields := range fieldSettingsV2FieldUpgrades {
				collection, err := app.FindCollectionByNameOrId(collectionName)
				if err != nil {
					continue
				}
				for _, field := range fields {
					collection.Fields.RemoveByName(field.name)
				}
				if err := app.Save(collection); err != nil {
					return fmt.Errorf("revert %s field settings v2 fields: %w", collectionName, err)
				}
			}
			return nil
		},
	)
}

func addInternalField(collection *core.Collection, field internalField) {
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
	}
}
