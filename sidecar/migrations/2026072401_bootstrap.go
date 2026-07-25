package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const metadataTable = "_vibetable_sidecar_meta"

func init() {
	m.Register(func(app core.App) error {
		_, err := app.DB().NewQuery(`
			CREATE TABLE IF NOT EXISTS {{_vibetable_sidecar_meta}} (
				[[key]]     TEXT PRIMARY KEY NOT NULL,
				[[value]]   TEXT NOT NULL,
				[[updated]] TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
			);
			INSERT OR IGNORE INTO {{_vibetable_sidecar_meta}} ([[key]], [[value]])
			VALUES ('schema_version', '1');
		`).Execute()
		return err
	}, func(app core.App) error {
		return app.DeleteTable(metadataTable)
	})
}
