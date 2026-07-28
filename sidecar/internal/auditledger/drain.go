package auditledger

import (
	"context"
	"errors"
)

// OutboxStore is implemented by the source business database. Pending returns
// the oldest unacknowledged source batch; Enqueue belongs to the source
// transaction, while the ledger owns only pending reads and post-ledger
// acknowledgement.
type OutboxStore interface {
	Pending(context.Context, int) ([]Envelope, error)
	Acknowledge(context.Context, string, string, uint64) error
}

type DrainReport struct {
	Appended     int
	Duplicates   int
	Acknowledged int
	Anchor       Anchor
}

type Drainer struct {
	ledger    *Ledger
	batchSize int
}

func NewDrainer(ledger *Ledger, batchSize int) (*Drainer, error) {
	if ledger == nil {
		return nil, errors.New("audit.ledger_required")
	}
	if batchSize <= 0 {
		return nil, errors.New("audit.batch_size_invalid")
	}
	return &Drainer{ledger: ledger, batchSize: batchSize}, nil
}

func (drainer *Drainer) Drain(
	ctx context.Context,
	outbox OutboxStore,
) (DrainReport, error) {
	if outbox == nil {
		return DrainReport{}, errors.New("audit.outbox_required")
	}
	report := DrainReport{}
	for {
		pending, err := outbox.Pending(ctx, drainer.batchSize)
		if err != nil {
			report.Anchor = drainer.ledger.Anchor()
			return report, err
		}
		if len(pending) == 0 {
			report.Anchor = drainer.ledger.Anchor()
			return report, nil
		}
		SortEnvelopes(pending)
		for _, envelope := range pending {
			if err := ctx.Err(); err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			result, err := drainer.ledger.Append(ctx, envelope)
			if err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			if result.Duplicate {
				report.Duplicates++
			} else {
				report.Appended++
			}
			// Sync happens inside Append. A source acknowledgement can therefore
			// be retried safely after a crash or source-side failure.
			if err := outbox.Acknowledge(
				ctx,
				envelope.EventID,
				envelope.SourceEpoch,
				envelope.SourceSequence,
			); err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			report.Acknowledged++
		}
	}
}
