package migrations

import (
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func setLookupRelationRequired(app core.App, required bool) error {
	for _, name := range []string{"vibetable_lookups", "vibetable_computation_dependencies"} {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			return err
		}
		if required {
			count, err := app.CountRecords(collection, dbx.HashExp{"relation_field_id": ""})
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("cannot downgrade while conditional lookup dependencies remain in %s", name)
			}
		}
		field, ok := collection.Fields.GetByName("relation_field_id").(*core.TextField)
		if !ok {
			return fmt.Errorf("%s relation field metadata is unavailable", name)
		}
		field.Required = required
		if err := app.Save(collection); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	m.Register(func(app core.App) error {
		return setLookupRelationRequired(app, false)
	}, func(app core.App) error {
		return setLookupRelationRequired(app, true)
	})
}
