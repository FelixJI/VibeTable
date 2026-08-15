package schemaaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type Event struct {
	OperationID    string
	PlanID         string
	Action         string
	TableID        string
	FieldID        string
	SchemaRevision string
	BeforeHash     string
	AfterHash      string
	Actor          v2.Actor
	Outcome        string
	MigrationJobID string
}

// SaveOutbox persists a redacted schema event for the external append-only
// ledger. The payload intentionally has no field definitions, defaults, row
// images, or other business values.
func SaveOutbox(
	ctx context.Context,
	app core.App,
	event Event,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_audit_outbox")
	if err != nil {
		return fmt.Errorf("load schema audit outbox: %w", err)
	}
	sourceEpoch := "schema-v2"
	if coordinated, ok := writecoordinator.BusinessAuditSourceEpoch(ctx); ok {
		sourceEpoch = coordinated
	}
	var sourceSequence int64
	if err := app.DB().NewQuery(`
		SELECT COALESCE(MAX(source_sequence), 0) + 1
		FROM vibetable_audit_outbox
		WHERE source_epoch = {:epoch}
	`).Bind(dbx.Params{"epoch": sourceEpoch}).Row(&sourceSequence); err != nil || sourceSequence <= 0 {
		return errors.Join(errors.New("allocate schema audit sequence"), err)
	}
	payload, err := json.Marshal(map[string]any{
		"contract":       v2.Contract,
		"operationId":    event.OperationID,
		"planId":         event.PlanID,
		"action":         event.Action,
		"tableId":        event.TableID,
		"fieldId":        event.FieldID,
		"schemaRevision": event.SchemaRevision,
		"beforeHash":     event.BeforeHash,
		"afterHash":      event.AfterHash,
		"actor": map[string]string{
			"id": event.Actor.ID, "kind": event.Actor.Kind,
		},
		"outcome":        event.Outcome,
		"migrationJobId": event.MigrationJobID,
	})
	if err != nil {
		return fmt.Errorf("encode schema audit outbox: %w", err)
	}
	envelope, err := auditledger.NewEnvelope(
		"schema:"+event.OperationID,
		sourceEpoch,
		uint64(sourceSequence),
		event.OperationID,
		payload,
		now.UTC(),
	)
	if err != nil {
		return fmt.Errorf("build schema audit outbox: %w", err)
	}
	outbox := core.NewRecord(collection)
	outbox.Set("event_id", envelope.EventID)
	outbox.Set("source_epoch", envelope.SourceEpoch)
	outbox.Set("source_sequence", envelope.SourceSequence)
	outbox.Set("mutation_identity", envelope.MutationIdentity)
	outbox.Set("payload_hash", envelope.PayloadHash)
	outbox.Set("payload_json", types.JSONRaw(envelope.Payload))
	outbox.Set("occurred_at", envelope.OccurredAt)
	outbox.Set("status", "pending")
	outbox.Set("attempts", 0)
	if err := app.Save(outbox); err != nil {
		return fmt.Errorf("save schema audit outbox: %w", err)
	}
	return nil
}
