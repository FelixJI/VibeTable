package migrations

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		return app.RunInTransaction(func(tx core.App) error {
			objects := []struct{ name, kind, statement string }{
				{"vibetable_plugin_preservation_revision", "table", `CREATE TABLE vibetable_plugin_preservation_revision (
                    singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
                    revision INTEGER NOT NULL CHECK(typeof(revision) = 'integer' AND revision >= 0)
                )`},
				{"plugin_preservation_insert", "trigger", `CREATE TRIGGER plugin_preservation_insert AFTER INSERT ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`},
				{"plugin_preservation_update", "trigger", `CREATE TRIGGER plugin_preservation_update AFTER UPDATE ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`},
				{"plugin_preservation_delete", "trigger", `CREATE TRIGGER plugin_preservation_delete AFTER DELETE ON vibetable_plugin_records
                    BEGIN UPDATE vibetable_plugin_preservation_revision SET revision = revision + 1 WHERE singleton = 1; END`},
			}
			normalize := func(statement string) string {
				return strings.Join(strings.Fields(statement), " ")
			}
			for _, object := range objects {
				var stored struct {
					Type string `db:"type"`
					SQL  string `db:"sql"`
				}
				err := tx.DB().NewQuery("SELECT type, sql FROM sqlite_schema WHERE name={:name}").
					Bind(dbx.Params{"name": object.name}).One(&stored)
				if errors.Is(err, sql.ErrNoRows) {
					if _, err := tx.DB().NewQuery(object.statement).Execute(); err != nil {
						return err
					}
					if object.kind == "table" {
						if _, err := tx.DB().NewQuery(`INSERT INTO vibetable_plugin_preservation_revision(singleton, revision) VALUES (1, 0)`).Execute(); err != nil {
							return err
						}
					}
				} else if err != nil {
					return err
				} else if stored.Type != object.kind || normalize(stored.SQL) != normalize(object.statement) {
					return fmt.Errorf("incompatible plugin preservation schema: %s", object.name)
				}
			}
			var count int
			if err := tx.DB().NewQuery(`SELECT COUNT(*) FROM vibetable_plugin_preservation_revision WHERE singleton=1 AND typeof(revision)='integer' AND revision>=0`).Row(&count); err != nil {
				return err
			}
			if count != 1 {
				return errors.New("plugin preservation revision is missing or invalid")
			}
			return nil
		})
	}, func(core.App) error {
		// Preserve the monotonic counter and triggers through application rollback.
		return nil
	})
}
