package migrations

import (
	"database/sql"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Durable job facts and batch mappings never contain source credentials or
// restartable plan tokens. A batch is saved in its business transaction; unlike
// the short-lived mutation cache it has no expiry that permits re-execution.
func sourceImportCollections() []internalCollection {
	return []internalCollection{
		{name: "vibetable_source_import_jobs", fields: []internalField{
			{name: "job_id", kind: "text", required: true, max: 128},
			{name: "started_at", kind: "date", required: true},
			{name: "result_json", kind: "json", required: true},
		}, unique: []string{"job_id"}, indexes: []string{"started_at"}},
		{name: "vibetable_source_import_batches", fields: []internalField{
			{name: "job_id", kind: "text", required: true, max: 128},
			{name: "batch_id", kind: "text", required: true, max: 128},
			{name: "receipt_json", kind: "json", required: true},
		}, uniqueGroups: [][]string{{"job_id", "batch_id"}}, indexes: []string{"job_id"}},
	}
}

func init() {
	m.Register(func(app core.App) error {
		for _, definition := range sourceImportCollections() {
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
				return err
			}
		}
		return nil
	}, func(core.App) error { return nil })
}
