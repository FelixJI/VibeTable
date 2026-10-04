package migrations

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// importHistoryCollection is the durable projection of file import outcomes.
// Entries start as interrupted and may only be promoted to succeeded inside
// the business mutation transaction that actually committed the rows; the
// terminal states failed/cancelled/aborted record only execution outcomes
// reported by the Host worker. The collection stores no tokens, grants,
// original file paths or credentials — source_name is a sanitized short file
// name supplied by the Host. Business semantics live in internal/importhistory.
func importHistoryCollection() internalCollection {
	return internalCollection{
		name: "vibetable_import_history",
		fields: []internalField{
			{name: "task_id", kind: "text", required: true, max: 128},
			{name: "collection", kind: "text", required: true, max: 128},
			{name: "source_type", kind: "text", required: true, max: 16},
			{name: "source_name", kind: "text", required: true, max: 256},
			{name: "idempotency_key", kind: "text", required: true, max: 256},
			{name: "state", kind: "text", required: true, max: 16},
			{name: "commit_state", kind: "text", required: true, max: 16},
			// Optional numbers: unknown counts stay NULL instead of a fake zero
			// write count (PocketBase treats numeric zero as blank for required
			// number fields anyway).
			{name: "created_count", kind: "number", required: false},
			{name: "updated_count", kind: "number", required: false},
			// session_epoch is a small monotonic counter; zero is a valid epoch,
			// so the field stays optional and the application enforces the
			// non-negative integer invariant.
			{name: "session_epoch", kind: "number", required: false},
			{name: "error_code", kind: "text", required: false, max: 64},
			{name: "started_at", kind: "date", required: true},
			{name: "finished_at", kind: "date", required: false},
		},
		unique: []string{"task_id"},
		// One idempotency key can carry at most one import attribution per
		// collection; a second task claiming the same pair is rejected instead
		// of double-attributing committed rows.
		uniqueGroups: [][]string{{"idempotency_key", "collection"}},
		indexes:      []string{"started_at", "collection"},
	}
}

func init() {
	m.Register(func(app core.App) error {
		definition := importHistoryCollection()
		existing, err := app.FindCollectionByNameOrId(definition.name)
		if err == nil {
			if err := validateInternalCollection(existing, definition); err != nil {
				return err
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := app.Save(buildInternalCollection(definition)); err != nil {
			return fmt.Errorf("create import history collection: %w", err)
		}
		return nil
	}, func(core.App) error {
		// The projection is business-visible history and survives a schema
		// rollback; the additive collection is recreated by re-running this
		// migration.
		return nil
	})
}
