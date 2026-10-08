package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("vibetable_fields")
		if err != nil {
			return err
		}
		collection.Fields.Add(&core.JSONField{Name: "auto_number_state_json"})
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("vibetable_fields")
		if err != nil {
			return err
		}
		collection.Fields.RemoveByName("auto_number_state_json")
		return app.Save(collection)
	})
}
