package auditledger

import (
	"context"
	"errors"
	"time"
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
	Appended            int
	Duplicates          int
	Acknowledged        int
	Anchor              Anchor
	Phase               string // Last attempted operation, or completed after the empty pending read.
	PendingCalls        int
	AppendCalls         int
	AcknowledgeCalls    int
	PendingDuration     time.Duration
	AppendDuration      time.Duration
	AppendMaxDuration   time.Duration
	AcknowledgeDuration time.Duration
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
		report.Phase = "pending"
		report.PendingCalls++
		started := time.Now()
		pending, err := outbox.Pending(ctx, drainer.batchSize)
		report.PendingDuration += time.Since(started)
		if err != nil {
			report.Anchor = drainer.ledger.Anchor()
			return report, err
		}
		if len(pending) == 0 {
			report.Phase = "completed"
			report.Anchor = drainer.ledger.Anchor()
			return report, nil
		}
		SortEnvelopes(pending)
		for _, envelope := range pending {
			report.Phase = "between_records"
			if err := ctx.Err(); err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			report.Phase = "append"
			report.AppendCalls++
			started = time.Now()
			result, err := drainer.ledger.Append(ctx, envelope)
			appendElapsed := time.Since(started)
			report.AppendDuration += appendElapsed
			report.AppendMaxDuration = max(report.AppendMaxDuration, appendElapsed)
			if err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			if result.Duplicate {
				report.Duplicates++
			} else {
				report.Appended++
			}
			// Sync happens inside Append, so failed source acknowledgement remains safely retryable.
			report.Phase = "ack"
			report.AcknowledgeCalls++
			started = time.Now()
			err = outbox.Acknowledge(
				ctx,
				envelope.EventID,
				envelope.SourceEpoch,
				envelope.SourceSequence,
			)
			report.AcknowledgeDuration += time.Since(started)
			if err != nil {
				report.Anchor = drainer.ledger.Anchor()
				return report, err
			}
			report.Acknowledged++
		}
	}
}
