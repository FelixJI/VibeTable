package migrations

import (
	"testing"

	"github.com/pocketbase/pocketbase"
)

func TestFieldSettingsV2MigrationCreatesClosedMetadataCollections(t *testing.T) {
	t.Parallel()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: t.TempDir(), HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	}()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}

	fields, err := app.FindCollectionByNameOrId("vibetable_fields")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"schema_model_version", "lifecycle_state", "retired_at", "identity_json",
		"value_semantics_json", "constraints_v2_json", "storage_v2_json",
		"display_v2_json", "recommended_defaults_version", "definition_hash",
		"definition_v2_json",
	} {
		if fields.Fields.GetByName(name) == nil {
			t.Errorf("vibetable_fields is missing %s", name)
		}
	}

	jobs, err := app.FindCollectionByNameOrId("vibetable_jobs")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"plan_id", "field_id", "before_definition_json", "after_definition_json",
		"phase", "shadow_identities_json", "write_lock_owner", "cleanup_state",
	} {
		if jobs.Fields.GetByName(name) == nil {
			t.Errorf("vibetable_jobs is missing %s", name)
		}
	}

	for _, name := range []string{
		"vibetable_schema_change_plans", "vibetable_schema_audit",
	} {
		if _, err := app.FindCollectionByNameOrId(name); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}
