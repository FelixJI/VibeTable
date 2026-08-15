package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const interfacesCollection = "vibetable_interfaces"

func init() {
	m.Register(
		func(app core.App) error {
			if _, err := app.FindCollectionByNameOrId(interfacesCollection); err == nil {
				return fmt.Errorf("internal collection %s already exists", interfacesCollection)
			}

			collection := core.NewBaseCollection(interfacesCollection)
			collection.Fields.Add(&core.TextField{
				Name: "logical_id", Required: true, Max: 128,
			})
			collection.Fields.Add(&core.JSONField{
				Name: "payload_json", Required: true,
			})
			collection.AddIndex(
				"uniq_"+interfacesCollection+"_logical_id",
				true,
				"`logical_id`",
				"",
			)
			if err := app.Save(collection); err != nil {
				return fmt.Errorf("create internal collection %s: %w", interfacesCollection, err)
			}
			return nil
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId(interfacesCollection)
			if err != nil {
				return nil
			}
			if err := app.Delete(collection); err != nil {
				return fmt.Errorf("delete internal collection %s: %w", interfacesCollection, err)
			}
			return nil
		},
	)
}
