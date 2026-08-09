package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

var relationPairFieldUpgrades = map[string][]internalField{
	"vibetable_relations": {
		{name: "pair_id", kind: "text"},
		{name: "reciprocal_field_id", kind: "text"},
	},
	"vibetable_tables": {
		{name: "primary_display_field_id", kind: "text"},
	},
}

var relationPairCollectionOrder = []string{
	"vibetable_relations",
	"vibetable_tables",
}

func init() {
	m.Register(
		func(app core.App) error {
			for _, collectionName := range relationPairCollectionOrder {
				fields := relationPairFieldUpgrades[collectionName]
				collection, err := app.FindCollectionByNameOrId(collectionName)
				if err != nil {
					return fmt.Errorf("find %s for relation pairs: %w", collectionName, err)
				}
				for _, field := range fields {
					if collection.Fields.GetByName(field.name) == nil {
						addInternalField(collection, field)
					}
				}
				if err := app.Save(collection); err != nil {
					return fmt.Errorf("extend %s for relation pairs: %w", collectionName, err)
				}
				if collectionName == "vibetable_relations" {
					if err := triggerStartupMigrationFault(
						"2026080501_relation_pairs",
						"after-relations",
					); err != nil {
						return err
					}
				}
			}
			return nil
		},
		func(app core.App) error {
			for _, collectionName := range relationPairCollectionOrder {
				fields := relationPairFieldUpgrades[collectionName]
				collection, err := app.FindCollectionByNameOrId(collectionName)
				if err != nil {
					continue
				}
				for _, field := range fields {
					collection.Fields.RemoveByName(field.name)
				}
				if err := app.Save(collection); err != nil {
					return fmt.Errorf("revert %s relation pair fields: %w", collectionName, err)
				}
			}
			return nil
		},
	)
}
