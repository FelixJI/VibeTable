package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const viewV2MetadataField = "view_v2_json"

func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("vibetable_tables")
			if err != nil {
				return fmt.Errorf("find table metadata for view v2: %w", err)
			}
			if collection.Fields.GetByName(viewV2MetadataField) != nil {
				return fmt.Errorf("table metadata already contains %s", viewV2MetadataField)
			}
			collection.Fields.Add(&core.JSONField{Name: viewV2MetadataField})
			if err := app.Save(collection); err != nil {
				return fmt.Errorf("add view v2 metadata: %w", err)
			}
			return nil
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("vibetable_tables")
			if err != nil {
				return nil
			}
			field := collection.Fields.GetByName(viewV2MetadataField)
			if field == nil {
				return nil
			}
			collection.Fields.RemoveById(field.GetId())
			if err := app.Save(collection); err != nil {
				return fmt.Errorf("remove view v2 metadata: %w", err)
			}
			return nil
		},
	)
}
