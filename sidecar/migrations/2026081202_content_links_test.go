package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestContentProfileAndRecordDocumentLinkCollections(t *testing.T) {
	app := newMigratedTestApp(t)
	for _, name := range []string{
		contentProfilesCollection,
		recordDocumentLinksCollection,
	} {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if _, ok := collection.Fields.GetByName("logical_id").(*core.TextField); !ok {
			t.Fatalf("%s logical_id is not text", name)
		}
		if _, ok := collection.Fields.GetByName("payload_json").(*core.JSONField); !ok {
			t.Fatalf("%s payload_json is not JSON", name)
		}
		if collection.GetIndex("uniq_"+name+"_logical_id") == "" {
			t.Fatalf("%s logical_id unique index missing", name)
		}
	}
}
