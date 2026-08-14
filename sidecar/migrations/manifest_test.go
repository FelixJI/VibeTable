package migrations

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

const expectedMigrationManifestHash = "125efc10962d65fbe0103efda320d632d3aea8ab9d49f48ff2fcf21067390db9"

func TestManifestIsValidAndHashIsStableShape(t *testing.T) {
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.SchemaVersion != 10 || len(manifest.Migrations) != 9 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if hash := Hash(); hash != expectedMigrationManifestHash {
		t.Fatalf("manifest hash = %q, want %q", hash, expectedMigrationManifestHash)
	}
	last := manifest.Migrations[len(manifest.Migrations)-1]
	if last.ID != 2026081203 ||
		last.Source != "2026081203_view_v2_metadata.go" ||
		last.SHA256 != "ca3bc00025aa676417b4c0202ba8a4a942b85350f12c638277fe7983879795fc" {
		t.Fatalf("unexpected pinned latest migration entry: %#v", last)
	}
}

func TestMigrationRejectsPreexistingPartialInternalCollection(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()

	partial := core.NewBaseCollection("vibetable_tables")
	partial.Fields.Add(&core.TextField{
		Name: "table_id", Required: true, Max: 10000,
	})
	if err := app.Save(partial); err != nil {
		t.Fatalf("save partial collection: %v", err)
	}
	err := app.RunAllMigrations()
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("RunAllMigrations() error = %v, want incompatible schema", err)
	}
}

func TestPocketBaseMigrationRunnerIsIdempotent(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()

	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("first RunAllMigrations(): %v", err)
	}
	if !app.HasTable(metadataTable) {
		t.Fatalf("migration did not create %s", metadataTable)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("second RunAllMigrations(): %v", err)
	}
	if !app.HasTable(metadataTable) {
		t.Fatalf("second migration run removed %s", metadataTable)
	}
}

func TestFreshMetadataCollectionsUseCanonicalSchema(t *testing.T) {
	app := newMigratedTestApp(t)

	for _, fixture := range []struct {
		name   string
		fields []string
	}{
		{"vibetable_shared_settings", []string{"logical_id", "payload_json"}},
		{"vibetable_dashboards", []string{"logical_id", "payload_json"}},
		{"vibetable_panels", []string{"logical_id", "payload_json", "parent_id"}},
		{"vibetable_presets", []string{"logical_id", "payload_json"}},
		{"vibetable_content_versions", []string{"logical_id", "payload_json"}},
	} {
		collection, err := app.FindCollectionByNameOrId(fixture.name)
		if err != nil {
			t.Fatalf("find %s: %v", fixture.name, err)
		}
		customFields := 0
		for _, field := range collection.Fields {
			if !field.GetSystem() {
				customFields++
			}
		}
		if customFields != len(fixture.fields) {
			t.Fatalf("%s custom field count = %d, want %d", fixture.name, customFields, len(fixture.fields))
		}
		for _, name := range fixture.fields {
			if collection.Fields.GetByName(name) == nil {
				t.Fatalf("%s missing canonical field %s", fixture.name, name)
			}
		}
		logical, ok := collection.Fields.GetByName("logical_id").(*core.TextField)
		if !ok || !logical.Required || logical.Max != 128 {
			t.Fatalf("%s logical_id = %#v", fixture.name, logical)
		}
		payload, ok := collection.Fields.GetByName("payload_json").(*core.JSONField)
		// PocketBase treats empty JSON containers as blank. The metadata module
		// owns logical presence validation so `{}`, `[]`, and `null` remain valid.
		if !ok || payload.Required {
			t.Fatalf("%s payload_json = %#v", fixture.name, payload)
		}
		if collection.GetIndex("uniq_"+fixture.name+"_logical_id") == "" {
			t.Fatalf("%s logical_id unique index missing", fixture.name)
		}
	}
	panels, err := app.FindCollectionByNameOrId("vibetable_panels")
	if err != nil {
		t.Fatal(err)
	}
	if panels.GetIndex("idx_vibetable_panels_parent_id") == "" {
		t.Fatal("vibetable_panels parent_id index missing")
	}
}

func TestRealtimeOutboxRetentionPreservesPopulatedInitialDatabase(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()

	initialMigrations := core.MigrationsList{}
	retentionMigrations := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026072401_") ||
			strings.HasPrefix(migration.File, "2026072402_") ||
			strings.HasPrefix(migration.File, "2026072404_") ||
			strings.HasPrefix(migration.File, "2026072801_") ||
			strings.HasPrefix(migration.File, "2026072805_") {
			retentionMigrations.Register(migration.Up, migration.Down, migration.File)
		}
		if strings.HasPrefix(migration.File, "2026072401_") ||
			strings.HasPrefix(migration.File, "2026072402_") {
			initialMigrations.Register(migration.Up, migration.Down, migration.File)
		}
	}
	if applied, err := core.NewMigrationsRunner(app, initialMigrations).Up(); err != nil {
		t.Fatalf("apply initial migrations: %v", err)
	} else if len(applied) != 2 {
		t.Fatalf("applied initial migrations = %#v", applied)
	}

	_, err := app.DB().NewQuery(`
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < 10005
		)
		INSERT INTO vibetable_outbox (
			id, event_id, topic, payload_json, status, attempts
		)
		SELECT
			printf('record%09d', value),
			printf('event%010d', value),
			'data.changed',
			'{}',
			'pending',
			0
		FROM sequence
	`).Execute()
	if err != nil {
		t.Fatalf("seed initial outbox: %v", err)
	}
	assertOutboxRetentionState(t, app, 10005, "event0000000001", false)

	runner := core.NewMigrationsRunner(app, retentionMigrations)
	if applied, err := runner.Up(); err != nil {
		t.Fatalf("apply retention migrations: %v", err)
	} else if len(applied) != 3 ||
		!strings.HasPrefix(applied[0], "2026072404_") ||
		!strings.HasPrefix(applied[1], "2026072801_") ||
		!strings.HasPrefix(applied[2], "2026072805_") {
		t.Fatalf("applied retention migrations = %#v", applied)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000006", true)

	insertRawOutboxRecord(t, app, 10006)
	assertOutboxRetentionState(t, app, 10000, "event0000000007", true)

	if reverted, err := runner.Down(3); err != nil {
		t.Fatalf("revert retention migrations: %v", err)
	} else if len(reverted) != 3 ||
		!strings.HasPrefix(reverted[0], "2026072805_") ||
		!strings.HasPrefix(reverted[1], "2026072801_") ||
		!strings.HasPrefix(reverted[2], "2026072404_") {
		t.Fatalf("reverted retention migrations = %#v", reverted)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000007", false)
	insertRawOutboxRecord(t, app, 10007)
	assertOutboxRetentionState(t, app, 10001, "event0000000007", false)

	if applied, err := runner.Up(); err != nil {
		t.Fatalf("reapply retention migrations: %v", err)
	} else if len(applied) != 3 ||
		!strings.HasPrefix(applied[0], "2026072404_") ||
		!strings.HasPrefix(applied[1], "2026072801_") ||
		!strings.HasPrefix(applied[2], "2026072805_") {
		t.Fatalf("reapplied retention migrations = %#v", applied)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000008", true)
}

func TestCanonicalRelationAndLookupCollectionsOmitRemovedFields(t *testing.T) {
	app := newMigratedTestApp(t)
	for collectionName, removedFields := range map[string][]string{
		"vibetable_relations": {"junction_table_id"},
		"vibetable_lookups":   {"aggregate"},
	} {
		collection, err := app.FindCollectionByNameOrId(collectionName)
		if err != nil {
			t.Fatalf("find %s: %v", collectionName, err)
		}
		for _, field := range removedFields {
			if collection.Fields.GetByName(field) != nil {
				t.Fatalf("%s still exposes removed field %s", collectionName, field)
			}
		}
	}
}

func insertRawOutboxRecord(t *testing.T, app core.App, sequence int) {
	t.Helper()
	_, err := app.DB().NewQuery(`
		INSERT INTO vibetable_outbox (
			id, event_id, topic, payload_json, status, attempts
		) VALUES (
			{:id}, {:event}, 'data.changed', '{}', 'pending', 0
		)
	`).Bind(map[string]any{
		"id":    fmt.Sprintf("record%09d", sequence),
		"event": fmt.Sprintf("event%010d", sequence),
	}).Execute()
	if err != nil {
		t.Fatalf("insert outbox event %d: %v", sequence, err)
	}
}

func assertOutboxRetentionState(
	t *testing.T,
	app core.App,
	wantCount int,
	wantOldestEvent string,
	wantTrigger bool,
) {
	t.Helper()
	var state struct {
		Count       int    `db:"count"`
		OldestEvent string `db:"oldest_event"`
	}
	if err := app.DB().NewQuery(`
		SELECT
			COUNT(*) AS count,
			COALESCE((
				SELECT event_id FROM vibetable_outbox
				ORDER BY rowid ASC LIMIT 1
			), '') AS oldest_event
		FROM vibetable_outbox
	`).One(&state); err != nil {
		t.Fatalf("read outbox retention state: %v", err)
	}
	if state.Count != wantCount || state.OldestEvent != wantOldestEvent {
		t.Fatalf(
			"outbox retention state = count %d oldest %q, want %d %q",
			state.Count,
			state.OldestEvent,
			wantCount,
			wantOldestEvent,
		)
	}

	var triggerCount int
	if err := app.DB().NewQuery(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND name = 'vibetable_outbox_retain_latest'
	`).Row(&triggerCount); err != nil {
		t.Fatalf("read outbox retention trigger: %v", err)
	}
	if got := triggerCount == 1; got != wantTrigger {
		t.Fatalf("outbox retention trigger present = %v, want %v", got, wantTrigger)
	}
}
