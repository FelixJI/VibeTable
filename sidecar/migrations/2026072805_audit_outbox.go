package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

var auditOutboxCollection = internalCollection{
	name: "vibetable_audit_outbox",
	fields: []internalField{
		{name: "event_id", kind: "text", required: true},
		{name: "source_epoch", kind: "text", required: true},
		{name: "source_sequence", kind: "number", required: true},
		{name: "mutation_identity", kind: "text", required: true},
		{name: "payload_hash", kind: "text", required: true},
		{name: "payload_json", kind: "json", required: true},
		{name: "occurred_at", kind: "date", required: true},
		{name: "status", kind: "text", required: true},
		{name: "attempts", kind: "number"},
	},
	unique:       []string{"event_id"},
	uniqueGroups: [][]string{{"source_epoch", "source_sequence"}},
}

func init() {
	m.Register(
		func(app core.App) error {
			if existing, err := app.FindCollectionByNameOrId(auditOutboxCollection.name); err == nil {
				return validateInternalCollection(existing, auditOutboxCollection)
			}
			return app.Save(buildInternalCollection(auditOutboxCollection))
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId(auditOutboxCollection.name)
			if err != nil {
				return nil
			}
			return app.Delete(collection)
		},
	)
}
