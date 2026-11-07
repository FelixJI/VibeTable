package migrations

import (
	"fmt"
	"slices"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/dbutils"
)

const pluginEventIndexName = "idx_vibetable_plugin_records_event_lookup"

// Match the PocketBase JSON field resolver so the indexed value covers the
// same rows, including its fallback for legacy non-JSON values.
const pluginEventIndexColumns = "`kind`, (CASE WHEN json_valid(payload_json) THEN json_extract(payload_json, '$.eventType') ELSE json_extract(json_object('pb', payload_json), '$.pb.eventType') END), `project_key`, `plugin_id`, `seq`"

func init() {
	m.Register(
		func(app core.App) error { return setPluginEventIndex(app, true) },
		func(app core.App) error { return setPluginEventIndex(app, false) },
	)
}

func setPluginEventIndex(app core.App, enabled bool) error {
	return app.RunInTransaction(func(tx core.App) error {
		definition := pluginSharedStateCollections()[0]
		collection, err := tx.FindCollectionByNameOrId(definition.name)
		if err != nil {
			return err
		}
		present, err := validatePluginEventIndex(tx, collection, definition)
		if err != nil || present == enabled {
			return err
		}
		if enabled {
			collection.AddIndex(pluginEventIndexName, false, pluginEventIndexColumns, "")
		} else {
			collection.RemoveIndex(pluginEventIndexName)
		}
		// Save can reload the shared PocketBase cache before the outer transaction ends.
		tx.TxInfo().OnComplete(func(err error) error {
			if err != nil {
				tx.Store().Remove(core.StoreKeyCachedCollections)
			}
			return nil
		})
		if err := tx.Save(collection); err != nil {
			return fmt.Errorf("save plugin event index: %w", err)
		}
		stored, err := tx.FindCollectionByNameOrId(definition.name)
		if err != nil {
			return err
		}
		present, err = validatePluginEventIndex(tx, stored, definition)
		if err != nil {
			return err
		}
		if present != enabled {
			return fmt.Errorf("plugin event index did not reach the requested state")
		}
		return nil
	})
}

func validatePluginEventIndex(app core.App, collection *core.Collection, definition internalCollection) (bool, error) {
	if collection.Name != definition.name || !collection.IsBase() {
		return false, fmt.Errorf("internal collection %s has incompatible identity or type", definition.name)
	}
	index := collection.GetIndex(pluginEventIndexName)
	legacy := *collection
	if index != "" {
		expected := buildInternalCollection(definition)
		expected.AddIndex(pluginEventIndexName, false, pluginEventIndexColumns, "")
		if index != expected.GetIndex(pluginEventIndexName) {
			return false, fmt.Errorf("plugin event index has incompatible collection metadata")
		}
		legacy.Indexes = slices.Clone(collection.Indexes)
		legacy.RemoveIndex(pluginEventIndexName)
	}
	if err := validateInternalCollection(&legacy, definition); err != nil {
		return false, err
	}
	return index != "", validatePluginRecordPhysicalIndexes(app, collection)
}

func validatePluginRecordPhysicalIndexes(app core.App, collection *core.Collection) error {
	expected := make(map[string]string, len(collection.Indexes))
	for _, statement := range collection.Indexes {
		parsed := dbutils.ParseIndex(statement)
		expected[parsed.IndexName] = parsed.Build()
	}
	var physical []struct {
		Name  string `db:"name"`
		Type  string `db:"type"`
		Table string `db:"tbl_name"`
		SQL   string `db:"sql"`
	}
	// The implicit primary key index has no SQL and is outside Collection.Indexes.
	err := app.DB().NewQuery(`
SELECT name, type, tbl_name, COALESCE(sql, '') AS sql FROM sqlite_schema
WHERE (type = 'index' AND tbl_name = {:table} AND sql IS NOT NULL)
   OR name = {:index} COLLATE NOCASE`).Bind(dbx.Params{"table": collection.Name, "index": pluginEventIndexName}).All(&physical)
	if err != nil {
		return err
	}
	for _, row := range physical {
		statement, ok := expected[row.Name]
		if !ok || row.Type != "index" || row.Table != collection.Name || row.SQL != statement {
			return fmt.Errorf("internal collection %s has incompatible physical index %s", collection.Name, row.Name)
		}
		delete(expected, row.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("internal collection %s is missing a physical index", collection.Name)
	}
	return nil
}
