package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestInterfacesMigrationCreatesCanonicalRevisionedMetadataCollection(t *testing.T) {
	app := newMigratedTestApp(t)
	collection, err := app.FindCollectionByNameOrId(interfacesCollection)
	if err != nil {
		t.Fatalf("find interfaces collection: %v", err)
	}
	logical, ok := collection.Fields.GetByName("logical_id").(*core.TextField)
	if !ok || !logical.Required || logical.Max != 128 {
		t.Fatalf("logical_id field = %#v", collection.Fields.GetByName("logical_id"))
	}
	payload, ok := collection.Fields.GetByName("payload_json").(*core.JSONField)
	if !ok || !payload.Required {
		t.Fatalf("payload_json field = %#v", collection.Fields.GetByName("payload_json"))
	}
	if collection.GetIndex("uniq_"+interfacesCollection+"_logical_id") == "" {
		t.Fatal("interfaces logical_id unique index missing")
	}

	record := core.NewRecord(collection)
	record.Set("logical_id", "interface-1")
	record.Set("payload_json", types.JSONRaw(`{"id":"interface-1","pages":[]}`))
	if err := app.Save(record); err != nil {
		t.Fatalf("save interface metadata: %v", err)
	}

	duplicate := core.NewRecord(collection)
	duplicate.Set("logical_id", "interface-1")
	duplicate.Set("payload_json", types.JSONRaw(`{"id":"interface-1","pages":[]}`))
	if err := app.Save(duplicate); err == nil {
		t.Fatal("duplicate interface logical_id was accepted")
	}
}
