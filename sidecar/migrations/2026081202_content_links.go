package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const (
	contentProfilesCollection     = "vibetable_content_profiles"
	recordDocumentLinksCollection = "vibetable_record_document_links"
)

func init() {
	m.Register(
		func(app core.App) error {
			for _, name := range []string{
				contentProfilesCollection,
				recordDocumentLinksCollection,
			} {
				if _, err := app.FindCollectionByNameOrId(name); err == nil {
					return fmt.Errorf("internal collection %s already exists", name)
				}
				collection := core.NewBaseCollection(name)
				collection.Fields.Add(&core.TextField{
					Name: "logical_id", Required: true, Max: 128,
				})
				collection.Fields.Add(&core.JSONField{
					Name: "payload_json", Required: true,
				})
				collection.AddIndex(
					"uniq_"+name+"_logical_id",
					true,
					"`logical_id`",
					"",
				)
				if err := app.Save(collection); err != nil {
					return fmt.Errorf("create internal collection %s: %w", name, err)
				}
			}
			return nil
		},
		func(app core.App) error {
			for _, name := range []string{
				recordDocumentLinksCollection,
				contentProfilesCollection,
			} {
				collection, err := app.FindCollectionByNameOrId(name)
				if err != nil {
					continue
				}
				if err := app.Delete(collection); err != nil {
					return fmt.Errorf("delete internal collection %s: %w", name, err)
				}
			}
			return nil
		},
	)
}
