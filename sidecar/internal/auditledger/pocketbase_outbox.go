package auditledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type PocketBaseOutbox struct {
	app core.App
}

func NewPocketBaseOutbox(app core.App) (*PocketBaseOutbox, error) {
	if app == nil {
		return nil, errors.New("audit.outbox_app_required")
	}
	return &PocketBaseOutbox{app: app}, nil
}

func (outbox *PocketBaseOutbox) Pending(
	ctx context.Context,
	limit int,
) ([]Envelope, error) {
	if limit <= 0 {
		return nil, errors.New("audit.outbox_limit_invalid")
	}
	type row struct {
		EventID          string `db:"event_id"`
		SourceEpoch      string `db:"source_epoch"`
		SourceSequence   uint64 `db:"source_sequence"`
		MutationIdentity string `db:"mutation_identity"`
		PayloadHash      string `db:"payload_hash"`
		Payload          []byte `db:"payload_json"`
		OccurredAt       string `db:"occurred_at"`
	}
	var rows []row
	if err := outbox.app.DB().NewQuery(`
		SELECT event_id, source_epoch, source_sequence, mutation_identity,
		       payload_hash, payload_json, occurred_at
		FROM vibetable_audit_outbox
		WHERE status = 'pending'
		ORDER BY source_epoch, source_sequence
		LIMIT {:limit}
	`).WithContext(ctx).Bind(dbx.Params{"limit": limit}).All(&rows); err != nil {
		return nil, fmt.Errorf("read audit outbox: %w", err)
	}
	result := make([]Envelope, 0, len(rows))
	for _, item := range rows {
		occurredAt, err := parsePocketBaseTime(item.OccurredAt)
		if err != nil {
			return nil, fmt.Errorf("read audit outbox timestamp: %w", err)
		}
		envelope, err := NewEnvelope(
			item.EventID,
			item.SourceEpoch,
			item.SourceSequence,
			item.MutationIdentity,
			item.Payload,
			occurredAt,
		)
		if err != nil || envelope.PayloadHash != item.PayloadHash {
			return nil, errors.Join(ErrPayloadMismatch, err)
		}
		result = append(result, envelope)
	}
	return result, nil
}

func (outbox *PocketBaseOutbox) Acknowledge(
	ctx context.Context,
	eventID string,
	sourceEpoch string,
	sourceSequence uint64,
) error {
	result, err := outbox.app.DB().NewQuery(`
		UPDATE vibetable_audit_outbox
		SET status = 'drained'
		WHERE event_id = {:event}
		  AND source_epoch = {:epoch}
		  AND source_sequence = {:sequence}
		  AND status = 'pending'
	`).WithContext(ctx).Bind(dbx.Params{
		"event": eventID, "epoch": sourceEpoch, "sequence": sourceSequence,
	}).Execute()
	if err != nil {
		return fmt.Errorf("acknowledge audit outbox: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return errors.Join(ErrSourceConflict, err)
	}
	return nil
}

func parsePocketBaseTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.000Z",
		"2006-01-02 15:04:05Z",
	} {
		if result, err := time.Parse(layout, value); err == nil {
			return result.UTC(), nil
		}
	}
	return time.Time{}, errors.New("audit.outbox_timestamp_invalid")
}
