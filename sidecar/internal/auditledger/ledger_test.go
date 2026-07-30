package auditledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type testOutbox struct {
	pending      []Envelope
	acked        map[string]bool
	acknowledged []string
	failOnce     bool
}

func (outbox *testOutbox) Pending(
	_ context.Context,
	limit int,
) ([]Envelope, error) {
	result := make([]Envelope, 0, limit)
	for _, envelope := range outbox.pending {
		if !outbox.acked[envelope.EventID] {
			result = append(result, envelope)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (outbox *testOutbox) Acknowledge(
	_ context.Context,
	eventID string,
	sourceEpoch string,
	sourceSequence uint64,
) error {
	if outbox.failOnce {
		outbox.failOnce = false
		return errors.New("source acknowledgement failed")
	}
	outbox.acked[eventID] = true
	outbox.acknowledged = append(
		outbox.acknowledged,
		fmt.Sprintf("%s:%s:%d", eventID, sourceEpoch, sourceSequence),
	)
	return nil
}

func envelopeAt(t *testing.T, id string, sequence uint64) Envelope {
	t.Helper()
	envelope, err := NewEnvelope(
		id,
		"epoch-1",
		sequence,
		"mutation-"+id,
		json.RawMessage(`{"z":2,"a":1}`),
		time.Date(2026, 7, 28, 10, int(sequence), 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestLedgerPersistsAndVerifiesHashChainAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ledger, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	first := envelopeAt(t, "event-1", 1)
	second := envelopeAt(t, "event-2", 2)
	for _, envelope := range []Envelope{first, second} {
		result, appendErr := ledger.Append(context.Background(), envelope)
		if appendErr != nil || result.Duplicate {
			t.Fatalf("append = %#v, %v", result, appendErr)
		}
	}
	anchor := ledger.Anchor()
	if anchor.SourceEpoch != "epoch-1" ||
		anchor.SourceSequence != 2 ||
		anchor.LedgerSequence != 2 ||
		anchor.Hash == "" {
		t.Fatalf("anchor = %#v", anchor)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Verify(); err != nil {
		t.Fatal(err)
	}
	records := reopened.Records(0, 10)
	if len(records) != 2 ||
		records[0].Envelope.PayloadHash != records[1].Envelope.PayloadHash ||
		records[1].PreviousHash != records[0].Hash {
		t.Fatalf("records = %#v", records)
	}
	duplicate, err := reopened.Append(context.Background(), first)
	if err != nil || !duplicate.Duplicate || reopened.Anchor() != anchor {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
}

func TestDrainContinuesUntilAllBatchesAreAcknowledged(t *testing.T) {
	ledger, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	drainer, err := NewDrainer(ledger, 2)
	if err != nil {
		t.Fatal(err)
	}
	outbox := &testOutbox{
		pending: []Envelope{
			envelopeAt(t, "event-2", 2),
			envelopeAt(t, "event-1", 1),
			envelopeAt(t, "event-3", 3),
		},
		acked: map[string]bool{},
	}
	report, err := drainer.Drain(context.Background(), outbox)
	if err != nil ||
		report.Appended != 3 ||
		report.Acknowledged != 3 ||
		report.Anchor.SourceEpoch != "epoch-1" ||
		report.Anchor.SourceSequence != 3 ||
		report.Anchor.LedgerSequence != 3 {
		t.Fatalf("drain = %#v, %v", report, err)
	}
	if len(outbox.acknowledged) != 3 ||
		outbox.acknowledged[0] != "event-1:epoch-1:1" ||
		outbox.acknowledged[1] != "event-2:epoch-1:2" ||
		outbox.acknowledged[2] != "event-3:epoch-1:3" {
		t.Fatalf("acknowledgements = %#v", outbox.acknowledged)
	}
}

func TestDrainRetriesAcknowledgementWithoutDuplicatingLedgerEvent(t *testing.T) {
	ledger, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	drainer, err := NewDrainer(ledger, 10)
	if err != nil {
		t.Fatal(err)
	}
	outbox := &testOutbox{
		pending:  []Envelope{envelopeAt(t, "event-1", 1)},
		acked:    map[string]bool{},
		failOnce: true,
	}
	first, err := drainer.Drain(context.Background(), outbox)
	if err == nil || first.Appended != 1 || first.Acknowledged != 0 {
		t.Fatalf("first drain = %#v, %v", first, err)
	}
	second, err := drainer.Drain(context.Background(), outbox)
	if err != nil ||
		second.Appended != 0 ||
		second.Duplicates != 1 ||
		second.Acknowledged != 1 ||
		second.Anchor.LedgerSequence != 1 {
		t.Fatalf("second drain = %#v, %v", second, err)
	}
}

func TestLedgerRejectsPayloadTamperingAndSourceSequenceReuse(t *testing.T) {
	root := t.TempDir()
	ledger, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	first := envelopeAt(t, "event-1", 1)
	if _, err := ledger.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	conflict := envelopeAt(t, "event-other", 1)
	if _, err := ledger.Append(
		context.Background(), conflict,
	); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("source conflict = %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, ledgerFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE audit_ledger SET payload = ? WHERE ledger_sequence = 1`,
		[]byte(`{"a":999}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(root); !errors.Is(err, ErrChainCorrupt) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("tampered reopen error = %v", err)
	}
}

func TestVerifiedRecordsFailsClosedOnLiveTamperAndTruncation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		tamper string
	}{
		{
			name: "payload tamper",
			tamper: `UPDATE audit_ledger
				SET payload = '{"tampered":true}'
				WHERE ledger_sequence = 2`,
		},
		{
			name:   "tail truncation",
			tamper: `DELETE FROM audit_ledger WHERE ledger_sequence = 2`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			ledger, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			for sequence := uint64(1); sequence <= 2; sequence++ {
				if _, err := ledger.Append(
					context.Background(),
					envelopeAt(
						t,
						fmt.Sprintf("event-%d", sequence),
						sequence,
					),
				); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open(
				"sqlite",
				filepath.Join(root, ledgerFileName),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(testCase.tamper); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.VerifiedRecords(
				context.Background(),
			); !errors.Is(err, ErrChainCorrupt) {
				t.Fatalf("live tamper accepted: %v", err)
			}
		})
	}
}

func TestLedgerRejectsSourceGapAndExportsVerifiablePrefix(t *testing.T) {
	ledger, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if _, err := ledger.Append(context.Background(), envelopeAt(t, "event-2", 2)); !errors.Is(
		err,
		ErrSourceGap,
	) {
		t.Fatalf("source gap accepted: %v", err)
	}
	for sequence := uint64(1); sequence <= 3; sequence++ {
		if _, err := ledger.Append(
			context.Background(),
			envelopeAt(t, fmt.Sprintf("event-%d", sequence), sequence),
		); err != nil {
			t.Fatal(err)
		}
	}
	raw, objectID, err := ledger.ExportPrefix(ledger.Anchor())
	if err != nil || objectID == "" {
		t.Fatalf("export prefix failed: %s %v", objectID, err)
	}
	if anchor, err := VerifyPrefix(raw); err != nil || anchor != ledger.Anchor() {
		t.Fatalf("verify prefix failed: %#v %v", anchor, err)
	}
	var prefix Prefix
	if err := json.Unmarshal(raw, &prefix); err != nil {
		t.Fatal(err)
	}
	prefix.Records[1].Envelope.Payload = json.RawMessage(`{"tampered":true}`)
	tampered, _ := json.Marshal(prefix)
	if _, err := VerifyPrefix(tampered); !errors.Is(err, ErrChainCorrupt) {
		t.Fatalf("tampered prefix accepted: %v", err)
	}
}
