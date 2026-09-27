package migrations

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// pluginSharedStateCollections keeps the Go-owned plugin shared catalog
// (installation identity, package revision, private settings and audit)
// plus the durable import completion marker for the legacy Python
// plugins.db migration. Payloads are canonical JSON documents; business
// semantics live in internal/pluginstore.
func pluginSharedStateCollections() []internalCollection {
	return []internalCollection{
		{
			name: "vibetable_plugin_records",
			fields: []internalField{
				{name: "kind", kind: "text", required: true, max: 32},
				{name: "project_key", kind: "text", required: true, max: 512},
				{name: "plugin_id", kind: "text", required: true, max: 512},
				{name: "item_key", kind: "text", required: true, max: 512},
				{name: "payload_json", kind: "json", required: true},
				// Optional: PocketBase treats 0 as blank for required numbers,
				// while non-audit kinds intentionally carry seq 0.
				{name: "seq", kind: "number", required: false},
			},
			uniqueGroups: [][]string{{
				"kind", "project_key", "plugin_id", "item_key",
			}},
			indexes: []string{"project_key", "plugin_id"},
		},
		{
			name: "vibetable_plugin_import_markers",
			fields: []internalField{
				{name: "source_key", kind: "text", required: true, max: 128},
				{name: "source_path", kind: "text", required: true, max: 2048},
				{name: "record_count", kind: "number", required: false},
				{name: "completed_at", kind: "date", required: true},
			},
			unique: []string{"source_key"},
		},
	}
}

func init() {
	m.Register(func(app core.App) error {
		for _, definition := range pluginSharedStateCollections() {
			existing, err := app.FindCollectionByNameOrId(definition.name)
			if err == nil {
				if err := validateInternalCollection(existing, definition); err != nil {
					return err
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := app.Save(buildInternalCollection(definition)); err != nil {
				return fmt.Errorf("create plugin shared state collection %s: %w", definition.name, err)
			}
		}
		return nil
	}, func(core.App) error {
		// Business plugin records survive a schema rollback; the additive
		// collections are recreated by re-running this migration.
		return nil
	})
}
