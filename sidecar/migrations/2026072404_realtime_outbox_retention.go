package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

const realtimeOutboxRetention = 10_000

func init() {
	m.Register(
		func(app core.App) error {
			if _, err := app.DB().NewQuery(`
				DELETE FROM vibetable_outbox
				WHERE rowid NOT IN (
					SELECT rowid FROM vibetable_outbox
					ORDER BY rowid DESC LIMIT {:limit}
				)
			`).Bind(map[string]any{"limit": realtimeOutboxRetention}).Execute(); err != nil {
				return err
			}
			_, err := app.DB().NewQuery(`
				CREATE TRIGGER IF NOT EXISTS vibetable_outbox_retain_latest
				AFTER INSERT ON vibetable_outbox
				BEGIN
					DELETE FROM vibetable_outbox
					WHERE rowid <= (
						SELECT rowid FROM vibetable_outbox
						ORDER BY rowid DESC LIMIT 1 OFFSET 10000
					);
				END
			`).Execute()
			return err
		},
		func(app core.App) error {
			_, err := app.DB().NewQuery(
				"DROP TRIGGER IF EXISTS vibetable_outbox_retain_latest",
			).Execute()
			return err
		},
	)
}
