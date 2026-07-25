package migrations

import (
	"encoding/json"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

type metadataCollectionUpgrade struct {
	name          string
	logicalIDFrom string
	payloadFields []string
	legacyJSON    []string
}

var metadataCollectionUpgrades = []metadataCollectionUpgrade{
	{
		name:          "vibetable_shared_settings",
		logicalIDFrom: "key",
		payloadFields: []string{"key", "value_json", "revision"},
		legacyJSON:    []string{"value_json"},
	},
	{
		name:          "vibetable_dashboards",
		logicalIDFrom: "dashboard_id",
		payloadFields: []string{
			"dashboard_id", "layout_json", "display_json", "revision",
		},
		legacyJSON: []string{"layout_json"},
	},
	{
		name:          "vibetable_panels",
		logicalIDFrom: "panel_id",
		payloadFields: []string{
			"panel_id", "dashboard_id", "query_json",
			"display_json", "revision",
		},
		legacyJSON: []string{"query_json"},
	},
	{
		name:          "vibetable_presets",
		logicalIDFrom: "preset_id",
		payloadFields: []string{
			"preset_id", "scope", "table_id",
			"projection_json", "revision",
		},
		legacyJSON: []string{"projection_json"},
	},
	{
		name: "vibetable_identifier_mappings",
		payloadFields: []string{
			"entity_kind", "parent_id", "physical_name",
			"display_name", "aliases_json", "origin", "status",
		},
	},
	{
		name: "vibetable_content_versions",
		payloadFields: []string{
			"table_id", "record_id", "name",
			"change_set_id", "created_at",
		},
	},
}

func init() {
	m.Register(
		func(app core.App) error {
			for _, upgrade := range metadataCollectionUpgrades {
				if err := applyMetadataCollectionUpgrade(
					app, upgrade,
				); err != nil {
					return err
				}
			}
			return nil
		},
		func(app core.App) error {
			for index := len(metadataCollectionUpgrades) - 1; index >= 0; index-- {
				if err := revertMetadataCollectionUpgrade(
					app, metadataCollectionUpgrades[index],
				); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

func applyMetadataCollectionUpgrade(
	app core.App,
	upgrade metadataCollectionUpgrade,
) error {
	collection, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		return fmt.Errorf(
			"find internal metadata collection %s: %w",
			upgrade.name, err,
		)
	}
	if collection.Fields.GetByName("logical_id") == nil {
		collection.Fields.Add(&core.TextField{
			Name: "logical_id", Max: 128,
		})
	}
	payloadWasAdded := collection.Fields.GetByName(
		"payload_json",
	) == nil
	if payloadWasAdded {
		collection.Fields.Add(&core.JSONField{
			Name: "payload_json",
		})
	}
	for _, fieldName := range upgrade.legacyJSON {
		field, ok := collection.Fields.GetByName(
			fieldName,
		).(*core.JSONField)
		if !ok {
			return fmt.Errorf(
				"internal metadata collection %s has incompatible legacy JSON field %s",
				upgrade.name, fieldName,
			)
		}
		field.Required = false
	}
	if err := app.Save(collection); err != nil {
		return fmt.Errorf(
			"add internal metadata fields to %s: %w",
			upgrade.name, err,
		)
	}

	records, err := app.FindRecordsByFilter(
		collection, "", "id", 0, 0,
	)
	if err != nil {
		return fmt.Errorf(
			"list internal metadata records in %s: %w",
			upgrade.name, err,
		)
	}
	seen := make(map[string]string, len(records))
	for _, record := range records {
		logicalID := record.GetString("logical_id")
		if logicalID == "" && upgrade.logicalIDFrom != "" {
			logicalID = record.GetString(upgrade.logicalIDFrom)
		}
		if logicalID == "" {
			logicalID = record.Id
		}
		if previous, exists := seen[logicalID]; exists {
			return fmt.Errorf(
				"internal metadata collection %s has duplicate logical id %q in %s and %s",
				upgrade.name, logicalID, previous, record.Id,
			)
		}
		seen[logicalID] = record.Id
		record.Set("logical_id", logicalID)
		storedPayload, storedPayloadErr := json.Marshal(
			record.GetRaw("payload_json"),
		)
		if storedPayloadErr != nil {
			return fmt.Errorf(
				"inspect internal metadata payload in %s: %w",
				upgrade.name, storedPayloadErr,
			)
		}
		if payloadWasAdded || string(storedPayload) == "null" {
			payload := make(
				map[string]any, len(upgrade.payloadFields),
			)
			for _, field := range upgrade.payloadFields {
				payload[field] = record.GetRaw(field)
			}
			raw, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return fmt.Errorf(
					"encode legacy internal metadata in %s: %w",
					upgrade.name, marshalErr,
				)
			}
			record.Set("payload_json", types.JSONRaw(raw))
		}
		if err := app.Save(record); err != nil {
			return fmt.Errorf(
				"backfill internal metadata record %s in %s: %w",
				record.Id, upgrade.name, err,
			)
		}
	}

	logicalField, ok := collection.Fields.GetByName(
		"logical_id",
	).(*core.TextField)
	if !ok {
		return fmt.Errorf(
			"internal metadata collection %s has incompatible logical_id",
			upgrade.name,
		)
	}
	_, ok = collection.Fields.GetByName(
		"payload_json",
	).(*core.JSONField)
	if !ok {
		return fmt.Errorf(
			"internal metadata collection %s has incompatible payload_json",
			upgrade.name,
		)
	}
	logicalField.Required = true
	collection.AddIndex(
		"uniq_"+upgrade.name+"_logical_id",
		true,
		"`logical_id`",
		"",
	)
	if err := app.Save(collection); err != nil {
		return fmt.Errorf(
			"finalize internal metadata collection %s: %w",
			upgrade.name, err,
		)
	}
	return nil
}

func revertMetadataCollectionUpgrade(
	app core.App,
	upgrade metadataCollectionUpgrade,
) error {
	collection, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		return nil
	}
	collection.RemoveIndex(
		"uniq_" + upgrade.name + "_logical_id",
	)
	collection.Fields.RemoveByName("payload_json")
	collection.Fields.RemoveByName("logical_id")
	for _, fieldName := range upgrade.legacyJSON {
		if field, ok := collection.Fields.GetByName(
			fieldName,
		).(*core.JSONField); ok {
			field.Required = true
		}
	}
	if err := app.Save(collection); err != nil {
		return fmt.Errorf(
			"revert internal metadata collection %s: %w",
			upgrade.name, err,
		)
	}
	return nil
}
