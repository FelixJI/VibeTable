package migrations

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestManifestIsValidAndHashIsStableShape(t *testing.T) {
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest(): %v", err)
	}
	if manifest.SchemaVersion != 6 || len(manifest.Migrations) != 6 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if hash := Hash(); len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		t.Fatalf("invalid manifest hash %q", hash)
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

func TestRealtimeOutboxRetentionUpgradesPopulatedV3Database(t *testing.T) {
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

	v3Migrations := core.MigrationsList{}
	allMigrations := core.MigrationsList{}
	for _, migration := range core.AppMigrations.Items() {
		if strings.HasPrefix(migration.File, "2026072401_") ||
			strings.HasPrefix(migration.File, "2026072402_") ||
			strings.HasPrefix(migration.File, "2026072403_") ||
			strings.HasPrefix(migration.File, "2026072404_") ||
			strings.HasPrefix(migration.File, "2026072801_") ||
			strings.HasPrefix(migration.File, "2026072805_") {
			allMigrations.Register(migration.Up, migration.Down, migration.File)
		}
		if strings.HasPrefix(migration.File, "2026072401_") ||
			strings.HasPrefix(migration.File, "2026072402_") ||
			strings.HasPrefix(migration.File, "2026072403_") {
			v3Migrations.Register(migration.Up, migration.Down, migration.File)
		}
	}
	if applied, err := core.NewMigrationsRunner(app, v3Migrations).Up(); err != nil {
		t.Fatalf("apply v3 migrations: %v", err)
	} else if len(applied) != 3 {
		t.Fatalf("applied v3 migrations = %#v", applied)
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
		t.Fatalf("seed v3 outbox: %v", err)
	}
	assertOutboxRetentionState(t, app, 10005, "event0000000001", false)

	runner := core.NewMigrationsRunner(app, allMigrations)
	if applied, err := runner.Up(); err != nil {
		t.Fatalf("upgrade v3 to v6: %v", err)
	} else if len(applied) != 3 ||
		!strings.HasPrefix(applied[0], "2026072404_") ||
		!strings.HasPrefix(applied[1], "2026072801_") ||
		!strings.HasPrefix(applied[2], "2026072805_") {
		t.Fatalf("applied v4-v6 migrations = %#v", applied)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000006", true)

	insertRawOutboxRecord(t, app, 10006)
	assertOutboxRetentionState(t, app, 10000, "event0000000007", true)

	if reverted, err := runner.Down(3); err != nil {
		t.Fatalf("downgrade v6 through v4: %v", err)
	} else if len(reverted) != 3 ||
		!strings.HasPrefix(reverted[0], "2026072805_") ||
		!strings.HasPrefix(reverted[1], "2026072801_") ||
		!strings.HasPrefix(reverted[2], "2026072404_") {
		t.Fatalf("reverted v6-v4 migrations = %#v", reverted)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000007", false)
	insertRawOutboxRecord(t, app, 10007)
	assertOutboxRetentionState(t, app, 10001, "event0000000007", false)

	if applied, err := runner.Up(); err != nil {
		t.Fatalf("reapply v4-v6: %v", err)
	} else if len(applied) != 3 ||
		!strings.HasPrefix(applied[0], "2026072404_") ||
		!strings.HasPrefix(applied[1], "2026072801_") ||
		!strings.HasPrefix(applied[2], "2026072805_") {
		t.Fatalf("reapplied v4-v6 migrations = %#v", applied)
	}
	assertOutboxRetentionState(t, app, 10000, "event0000000008", true)
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

func TestInternalMetadataMigrationBackfillsLegacyRecords(t *testing.T) {
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
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	upgrade := metadataCollectionUpgrades[0]
	if err := revertMetadataCollectionUpgrade(app, upgrade); err != nil {
		t.Fatalf("revert metadata upgrade: %v", err)
	}
	collection, err := app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.NewRecord(collection)
	legacy.Set("key", "theme")
	legacy.Set(
		"value_json",
		types.JSONRaw(`{"mode":"dark"}`),
	)
	legacy.Set("revision", 4)
	if err := app.Save(legacy); err != nil {
		t.Fatalf("save legacy record: %v", err)
	}
	if err := applyMetadataCollectionUpgrade(app, upgrade); err != nil {
		t.Fatalf("apply metadata upgrade: %v", err)
	}
	collection, err = app.FindCollectionByNameOrId(upgrade.name)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := app.FindRecordById(collection, legacy.Id)
	if err != nil || reloaded.GetString("logical_id") != "theme" {
		t.Fatalf("backfilled record = %#v, err=%v", reloaded, err)
	}
	raw, err := json.Marshal(reloaded.GetRaw("payload_json"))
	if err != nil || !strings.Contains(
		string(raw), `"value_json":{"mode":"dark"}`,
	) {
		t.Fatalf("backfilled payload = %s, err=%v", raw, err)
	}
	if collection.GetIndex(
		"uniq_vibetable_shared_settings_logical_id",
	) == "" {
		t.Fatal("backfilled index missing")
	}
}
