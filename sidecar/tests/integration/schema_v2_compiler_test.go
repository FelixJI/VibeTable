package integration_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestSchemaV2CompilerCreatesPresenceAndPartialUniqueInPocketBase(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: queryTempDir(t), HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer resetApp(t, app)
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("../../../contracts/schema-v2/fixtures/field-definition.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		t.Fatal(err)
	}
	definition.Value.Required = true
	definition.Constraints.Unique.Enabled = true
	compiled, err := v2.CompileField(definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	index, ok, err := v2.CompileUniqueIndex("orders_v2", definition)
	if err != nil || !ok {
		t.Fatalf("CompileUniqueIndex() = %q, %v, %v", index, ok, err)
	}

	collection := core.NewBaseCollection("orders_v2")
	collection.Fields.Add(compiled.Value, compiled.Presence)
	collection.Indexes = append(collection.Indexes, index)
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	stored, err := app.FindCollectionByNameOrId("orders_v2")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields.GetById(definition.Identity.ProviderFieldID) == nil ||
		stored.Fields.GetById(definition.Value.Presence.ProviderFieldID) == nil {
		t.Fatal("provider identities were not preserved")
	}

	saveNumberV2Record(t, app, stored, "record000000001", 0, true, true)
	saveNumberV2Record(t, app, stored, "record000000002", 0, false, true)
	saveNumberV2Record(t, app, stored, "record000000003", 0, false, true)
	saveNumberV2Record(t, app, stored, "record000000004", 0, true, false)
}

func TestSchemaV2CollectionCreationRollsBackAtomically(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: queryTempDir(t), HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer resetApp(t, app)
	sentinel := errors.New("fault after schema save")
	err := app.RunInTransaction(func(transaction core.App) error {
		if err := transaction.Save(core.NewBaseCollection("rolled_back_v2")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("transaction error = %v", err)
	}
	if _, err := app.FindCollectionByNameOrId("rolled_back_v2"); err == nil {
		t.Fatal("failed transaction leaked a collection")
	}
}

func saveNumberV2Record(
	t *testing.T,
	app core.App,
	collection *core.Collection,
	id string,
	value float64,
	present bool,
	wantSuccess bool,
) {
	t.Helper()
	record := core.NewRecord(collection)
	record.Id = id
	record.Set("f_01jabcde", value)
	record.Set("__vt_has_f_01jabcde", present)
	err := app.Save(record)
	if wantSuccess && err != nil {
		t.Fatalf("save record %s: %v", id, err)
	}
	if !wantSuccess && err == nil {
		raw, _ := json.Marshal(record)
		t.Fatalf("duplicate explicit zero unexpectedly saved: %s", raw)
	}
}
