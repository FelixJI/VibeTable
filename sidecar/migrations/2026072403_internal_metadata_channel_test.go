package migrations

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const expectedMigrationManifestHash = "9bf1f4b24141a3d4bd9ee64c1fa2d0935e132152b1d91df9c1531ed465bd4938"

type legacyMetadataFixture struct {
	upgrade metadataCollectionUpgrade
	values  map[string]any
}

func TestInternalMetadataChannelUpgradesAllCollectionsReversiblyAndIdempotently(
	t *testing.T,
) {
	app := newMigratedTestApp(t)
	fixtures := metadataUpgradeFixtures()
	if len(fixtures) != 6 {
		t.Fatalf("metadata upgrade fixture count = %d, want 6", len(fixtures))
	}
	gotNames := make([]string, 0, len(fixtures))
	for _, fixture := range fixtures {
		gotNames = append(gotNames, fixture.upgrade.name)
	}
	wantNames := []string{
		"vibetable_shared_settings",
		"vibetable_dashboards",
		"vibetable_panels",
		"vibetable_presets",
		"vibetable_identifier_mappings",
		"vibetable_content_versions",
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("metadata upgrade collections = %v, want %v", gotNames, wantNames)
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.upgrade.name, func(t *testing.T) {
			if err := runMetadataUpgradeTransaction(
				app,
				func(txApp core.App) error {
					return revertMetadataCollectionUpgrade(txApp, fixture.upgrade)
				},
			); err != nil {
				t.Fatalf("revert metadata upgrade: %v", err)
			}

			collection, err := app.FindCollectionByNameOrId(fixture.upgrade.name)
			if err != nil {
				t.Fatalf("find legacy collection: %v", err)
			}
			legacy := core.NewRecord(collection)
			for name, value := range fixture.values {
				legacy.Set(name, value)
			}
			if err := app.Save(legacy); err != nil {
				t.Fatalf("save legacy record: %v", err)
			}

			apply := func() {
				t.Helper()
				if err := runMetadataUpgradeTransaction(
					app,
					func(txApp core.App) error {
						return applyMetadataCollectionUpgrade(txApp, fixture.upgrade)
					},
				); err != nil {
					t.Fatalf("apply metadata upgrade: %v", err)
				}
			}

			apply()
			first := metadataUpgradeSnapshot(t, app, fixture, legacy.Id)

			// Applying an already completed v3 upgrade must not rewrite either
			// its logical identity or its canonical payload.
			apply()
			second := metadataUpgradeSnapshot(t, app, fixture, legacy.Id)
			if second != first {
				t.Fatalf(
					"second apply changed upgraded state:\nfirst:  %s\nsecond: %s",
					first,
					second,
				)
			}

			if err := runMetadataUpgradeTransaction(
				app,
				func(txApp core.App) error {
					return revertMetadataCollectionUpgrade(txApp, fixture.upgrade)
				},
			); err != nil {
				t.Fatalf("second revert metadata upgrade: %v", err)
			}
			assertLegacyMetadataSchema(t, app, fixture.upgrade)

			// A v3 -> v2 -> v3 round trip reconstructs the same v3 state
			// from the retained legacy columns.
			apply()
			roundTripped := metadataUpgradeSnapshot(t, app, fixture, legacy.Id)
			if roundTripped != first {
				t.Fatalf(
					"down/up round trip changed upgraded state:\nfirst: %s\nround trip: %s",
					first,
					roundTripped,
				)
			}
		})
	}
}

func TestInternalMetadataChannelDuplicateLogicalIDRollsBackAtomically(
	t *testing.T,
) {
	app := newMigratedTestApp(t)
	upgrade := metadataCollectionUpgrades[0]
	if err := runMetadataUpgradeTransaction(
		app,
		func(txApp core.App) error {
			return revertMetadataCollectionUpgrade(txApp, upgrade)
		},
	); err != nil {
		t.Fatalf("revert metadata upgrade: %v", err)
	}

	collection, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		t.Fatalf("find legacy collection: %v", err)
	}
	collection.Fields.Add(&core.TextField{Name: "logical_id", Max: 128})
	if err := app.Save(collection); err != nil {
		t.Fatalf("add partial logical_id field: %v", err)
	}

	recordIDs := make([]string, 0, 2)
	for index, key := range []string{"first", "second"} {
		record := core.NewRecord(collection)
		record.Set("key", key)
		record.Set("value_json", types.JSONRaw(fmt.Sprintf(`{"order":%d}`, index)))
		record.Set("revision", index+1)
		record.Set("logical_id", "duplicate")
		if err := app.Save(record); err != nil {
			t.Fatalf("save duplicate logical id record %d: %v", index, err)
		}
		recordIDs = append(recordIDs, record.Id)
	}

	err = runMetadataUpgradeTransaction(
		app,
		func(txApp core.App) error {
			return applyMetadataCollectionUpgrade(txApp, upgrade)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate logical id") {
		t.Fatalf("apply duplicate logical ids error = %v", err)
	}

	rolledBack, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		t.Fatalf("find rolled back collection: %v", err)
	}
	if rolledBack.Fields.GetByName("payload_json") != nil {
		t.Fatal("failed upgrade left payload_json behind")
	}
	logicalField, ok := rolledBack.Fields.GetByName("logical_id").(*core.TextField)
	if !ok {
		t.Fatal("failed upgrade removed preexisting logical_id")
	}
	if logicalField.Required {
		t.Fatal("failed upgrade made preexisting logical_id required")
	}
	if rolledBack.GetIndex("uniq_"+upgrade.name+"_logical_id") != "" {
		t.Fatal("failed upgrade left logical_id unique index behind")
	}
	valueField, ok := rolledBack.Fields.GetByName("value_json").(*core.JSONField)
	if !ok || !valueField.Required {
		t.Fatal("failed upgrade did not restore the required legacy JSON field")
	}
	for _, recordID := range recordIDs {
		record, findErr := app.FindRecordById(rolledBack, recordID)
		if findErr != nil {
			t.Fatalf("find rolled back record %s: %v", recordID, findErr)
		}
		if record.GetString("logical_id") != "duplicate" {
			t.Fatalf(
				"rolled back logical_id for %s = %q",
				recordID,
				record.GetString("logical_id"),
			)
		}
		if record.GetRaw("payload_json") != nil {
			t.Fatalf("failed upgrade persisted payload for %s", recordID)
		}
	}
}

func TestMigrationManifestHashIsPinnedAndRepeatable(t *testing.T) {
	first := Hash()
	second := Hash()
	if first != second {
		t.Fatalf("manifest hash changed between reads: %q != %q", first, second)
	}
	if first != expectedMigrationManifestHash {
		t.Fatalf(
			"manifest hash = %q, want pinned %q",
			first,
			expectedMigrationManifestHash,
		)
	}

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	last := manifest.Migrations[len(manifest.Migrations)-1]
	if last.ID != 2026072805 ||
		last.Source != "2026072805_audit_outbox.go" ||
		last.SHA256 != "1d53794ee64f9b746caf0aee638ee872ac92b5918257ca77d631c2b14f291de8" {
		t.Fatalf("unexpected pinned v6 migration entry: %#v", last)
	}
}

func newMigratedTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		for attempt := 0; attempt < 5; attempt++ {
			if err := os.RemoveAll(dataDir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("RunAllMigrations(): %v", err)
	}
	return app
}

func runMetadataUpgradeTransaction(
	app core.App,
	operation func(core.App) error,
) error {
	return app.RunInTransaction(operation)
}

func metadataUpgradeFixtures() []legacyMetadataFixture {
	fixtures := []legacyMetadataFixture{
		{
			values: map[string]any{
				"key":        "theme",
				"value_json": types.JSONRaw(`{"mode":"dark"}`),
				"revision":   4,
			},
		},
		{
			values: map[string]any{
				"dashboard_id": "dashboard-1",
				"layout_json":  types.JSONRaw(`{"columns":12}`),
				"display_json": types.JSONRaw(`{"title":"Sales"}`),
				"revision":     5,
			},
		},
		{
			values: map[string]any{
				"panel_id":     "panel-1",
				"dashboard_id": "dashboard-1",
				"query_json":   types.JSONRaw(`{"table":"orders"}`),
				"display_json": types.JSONRaw(`{"kind":"bar"}`),
				"revision":     6,
			},
		},
		{
			values: map[string]any{
				"preset_id":       "preset-1",
				"scope":           "table",
				"table_id":        "orders",
				"projection_json": types.JSONRaw(`{"fields":["total"]}`),
				"revision":        7,
			},
		},
		{
			values: map[string]any{
				"entity_kind":   "field",
				"parent_id":     "orders",
				"physical_name": "order_total",
				"display_name":  "Order total",
				"aliases_json":  types.JSONRaw(`["total"]`),
				"origin":        "user",
				"status":        "active",
			},
		},
		{
			values: map[string]any{
				"table_id":      "orders",
				"record_id":     "record-1",
				"name":          "Before import",
				"change_set_id": "change-set-1",
				"created_at":    "2026-07-24 12:34:56.000Z",
			},
		},
	}
	for index := range fixtures {
		fixtures[index].upgrade = metadataCollectionUpgrades[index]
	}
	return fixtures
}

func metadataUpgradeSnapshot(
	t *testing.T,
	app core.App,
	fixture legacyMetadataFixture,
	recordID string,
) string {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(fixture.upgrade.name)
	if err != nil {
		t.Fatalf("find upgraded collection: %v", err)
	}
	logicalField, ok := collection.Fields.GetByName("logical_id").(*core.TextField)
	if !ok || !logicalField.Required {
		t.Fatal("logical_id is not a required text field")
	}
	if _, ok := collection.Fields.GetByName("payload_json").(*core.JSONField); !ok {
		t.Fatal("payload_json is not a JSON field")
	}
	if collection.GetIndex("uniq_"+fixture.upgrade.name+"_logical_id") == "" {
		t.Fatal("logical_id unique index missing")
	}

	record, err := app.FindRecordById(collection, recordID)
	if err != nil {
		t.Fatalf("find upgraded record: %v", err)
	}
	wantLogicalID := recordID
	if fixture.upgrade.logicalIDFrom != "" {
		wantLogicalID = fmt.Sprint(fixture.values[fixture.upgrade.logicalIDFrom])
	}
	if got := record.GetString("logical_id"); got != wantLogicalID {
		t.Fatalf("logical_id = %q, want %q", got, wantLogicalID)
	}

	raw, err := json.Marshal(record.GetRaw("payload_json"))
	if err != nil {
		t.Fatalf("marshal payload_json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload_json %s: %v", raw, err)
	}
	if len(payload) != len(fixture.upgrade.payloadFields) {
		t.Fatalf(
			"payload keys = %v, want exactly %v",
			payload,
			fixture.upgrade.payloadFields,
		)
	}
	for _, field := range fixture.upgrade.payloadFields {
		payloadValue, exists := payload[field]
		if !exists {
			t.Fatalf("payload %s lacks field %q", raw, field)
		}
		var legacyValue any
		legacyRaw, marshalErr := json.Marshal(record.GetRaw(field))
		if marshalErr != nil {
			t.Fatalf("marshal retained legacy field %q: %v", field, marshalErr)
		}
		if err := json.Unmarshal(legacyRaw, &legacyValue); err != nil {
			t.Fatalf("decode retained legacy field %q: %v", field, err)
		}
		if !reflect.DeepEqual(payloadValue, legacyValue) {
			t.Fatalf(
				"payload field %q = %#v, want retained legacy value %#v",
				field,
				payloadValue,
				legacyValue,
			)
		}
	}
	return fmt.Sprintf(
		"%s|required=%t|index=%s|payload=%s",
		wantLogicalID,
		logicalField.Required,
		collection.GetIndex("uniq_"+fixture.upgrade.name+"_logical_id"),
		raw,
	)
}

func assertLegacyMetadataSchema(
	t *testing.T,
	app core.App,
	upgrade metadataCollectionUpgrade,
) {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		t.Fatalf("find reverted collection: %v", err)
	}
	if collection.Fields.GetByName("logical_id") != nil ||
		collection.Fields.GetByName("payload_json") != nil {
		t.Fatal("revert left v3 metadata fields behind")
	}
	if collection.GetIndex("uniq_"+upgrade.name+"_logical_id") != "" {
		t.Fatal("revert left v3 logical_id index behind")
	}
	for _, fieldName := range upgrade.legacyJSON {
		field, ok := collection.Fields.GetByName(fieldName).(*core.JSONField)
		if !ok || !field.Required {
			t.Fatalf("legacy JSON field %q was not restored", fieldName)
		}
	}
}
