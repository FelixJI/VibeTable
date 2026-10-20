package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		return app.RunInTransaction(func(tx core.App) error {
			statements := []string{
				`CREATE TABLE vibetable_plugin_preservation_revision (
                    singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
                    revision INTEGER NOT NULL CHECK(typeof(revision) = 'integer' AND revision >= 0)
                )`,
				`INSERT INTO vibetable_plugin_preservation_revision(singleton, revision) VALUES (1, 0)`,
				`CREATE TRIGGER plugin_preservation_insert AFTER INSERT ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`,
				`CREATE TRIGGER plugin_preservation_update AFTER UPDATE ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`,
				`CREATE TRIGGER plugin_preservation_delete AFTER DELETE ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`,
			}
			for _, statement := range statements {
				if _, err := tx.DB().NewQuery(statement).Execute(); err != nil {
					return err
				}
			}
			return nil
		})
	}, func(core.App) error {
		// Preserve the monotonic counter and triggers through application rollback.
		return nil
	})
}
