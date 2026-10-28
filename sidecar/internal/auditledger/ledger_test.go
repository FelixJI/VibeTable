package auditledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type testOutbox struct {
	pending      []Envelope
	acked        map[string]bool
	acknowledged []string
	failOnce     bool
	pendingError error
	afterPending func()
	beforeAck    func()
}

func (outbox *testOutbox) Pending(
	_ context.Context,
	limit int,
) ([]Envelope, error) {
	if outbox.pendingError != nil {
		return nil, outbox.pendingError
	}
	result := make([]Envelope, 0, limit)
	for _, envelope := range outbox.pending {
		if !outbox.acked[envelope.EventID] {
			result = append(result, envelope)
		}
		if len(result) == limit {
			break
		}
	}
	if outbox.afterPending != nil {
		outbox.afterPending()
	}
	return result, nil
}

func (outbox *testOutbox) Acknowledge(
	ctx context.Context,
	eventID string,
	sourceEpoch string,
	sourceSequence uint64,
) error {
	if outbox.beforeAck != nil {
		outbox.beforeAck()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
		report.Phase != "completed" || report.PendingCalls != 3 || report.AppendCalls != 2 || report.AcknowledgeCalls != 3 ||
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
	if err == nil || first.Phase != "ack" || first.Appended != 1 || first.Acknowledged != 0 || first.Anchor.LedgerSequence != 1 || first.AppendCalls != 1 || first.AcknowledgeCalls != 1 {
		t.Fatalf("first drain = %#v, %v", first, err)
	}
	second, err := drainer.Drain(context.Background(), outbox)
	if err != nil ||
		second.Phase != "completed" || second.PendingCalls != 2 || second.AppendCalls != 1 || second.AcknowledgeCalls != 1 ||
		second.Appended != 0 ||
		second.Duplicates != 1 ||
		second.Acknowledged != 1 ||
		second.Anchor.LedgerSequence != 1 {
		t.Fatalf("second drain = %#v, %v", second, err)
	}
}

func TestDrainCommitsBatchBeforeAcknowledgementAndReplaysAfterReopen(t *testing.T) {
	root := t.TempDir()
	ledger, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	drainer, err := NewDrainer(ledger, 2)
	if err != nil {
		t.Fatal(err)
	}
	outbox := &testOutbox{
		pending: []Envelope{envelopeAt(t, "event-1", 1), envelopeAt(t, "event-2", 2)},
		acked:   map[string]bool{}, failOnce: true,
		beforeAck: func() {
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.Anchor().LedgerSequence != 2 {
				t.Fatalf("ack began before the complete batch was durable: %#v", reopened.Anchor())
			}
		},
	}
	first, err := drainer.Drain(context.Background(), outbox)
	if err == nil || first.Appended != 2 || first.Acknowledged != 0 {
		t.Fatalf("first drain: %#v, %v", first, err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	drainer, err = NewDrainer(ledger, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := drainer.Drain(context.Background(), outbox)
	if err != nil || second.Appended != 0 || second.Duplicates != 2 || second.Acknowledged != 2 {
		t.Fatalf("replayed drain: %#v, %v", second, err)
	}
}

func TestDrainBatchFailureDoesNotPublishPrefix(t *testing.T) {
	for _, failure := range []string{"source_gap", "insert_failure", "commit_failure"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			ledger, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			second := envelopeAt(t, "event-2", 2)
			if failure == "source_gap" {
				second = envelopeAt(t, "event-3", 3)
			} else {
				statement := `CREATE TRIGGER fail_second BEFORE INSERT ON audit_ledger
					WHEN NEW.source_sequence = 2 BEGIN SELECT RAISE(ABORT, 'synthetic insert failure'); END`
				if failure == "commit_failure" {
					statement = `CREATE TABLE missing_parent (id INTEGER PRIMARY KEY);
						CREATE TABLE deferred_child (id INTEGER REFERENCES missing_parent(id) DEFERRABLE INITIALLY DEFERRED);
						CREATE TRIGGER fail_commit AFTER INSERT ON audit_ledger
						BEGIN INSERT INTO deferred_child VALUES (NEW.ledger_sequence); END`
				}
				if _, err := ledger.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			drainer, err := NewDrainer(ledger, 2)
			if err != nil {
				t.Fatal(err)
			}
			outbox := &testOutbox{pending: []Envelope{envelopeAt(t, "event-1", 1), second}, acked: map[string]bool{}}
			report, err := drainer.Drain(context.Background(), outbox)
			if err == nil || report.Appended != 0 || report.Acknowledged != 0 || ledger.Anchor() != (Anchor{}) {
				t.Fatalf("failed batch published a prefix: %#v, %v, anchor=%#v", report, err, ledger.Anchor())
			}
			if failure == "commit_failure" && !strings.HasPrefix(err.Error(), "flush audit record:") {
				t.Fatalf("failure did not reach COMMIT: %v", err)
			}
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.Anchor() != (Anchor{}) {
				t.Fatalf("failed batch was persisted: %#v", reopened.Anchor())
			}
		})
	}
}

func TestDrainCancellationAfterCommitLeavesRetryableBatch(t *testing.T) {
	ledger, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	drainer, err := NewDrainer(ledger, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbox := &testOutbox{
		pending: []Envelope{envelopeAt(t, "event-1", 1), envelopeAt(t, "event-2", 2)},
		acked:   map[string]bool{}, beforeAck: cancel,
	}
	first, err := drainer.Drain(ctx, outbox)
	if !errors.Is(err, context.Canceled) || first.Appended != 2 || first.Acknowledged != 0 || len(outbox.acked) != 0 {
		t.Fatalf("cancelled drain: %#v, %v", first, err)
	}
	outbox.beforeAck = nil
	second, err := drainer.Drain(context.Background(), outbox)
	if err != nil || second.Duplicates != 2 || second.Acknowledged != 2 || second.Anchor.LedgerSequence != 2 {
		t.Fatalf("replayed drain: %#v, %v", second, err)
	}
}

func TestAppendBatchKeepsEnvelopeIdentityAndSourceOrder(t *testing.T) {
	one, two := envelopeAt(t, "event-1", 1), envelopeAt(t, "event-2", 2)
	conflicting := one
	conflicting.MutationIdentity = "different-mutation"
	for _, test := range []struct {
		name       string
		envelopes  []Envelope
		err        error
		records    int
		duplicates int
	}{
		{name: "empty"},
		{name: "duplicate", envelopes: []Envelope{one, one, two}, records: 2, duplicates: 1},
		{name: "event conflict", envelopes: []Envelope{one, conflicting}, err: ErrEventConflict},
		{name: "source conflict", envelopes: []Envelope{one, envelopeAt(t, "other-event", 1)}, err: ErrSourceConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			results, err := ledger.appendBatch(context.Background(), test.envelopes)
			if !errors.Is(err, test.err) || len(ledger.Records(0, 10)) != test.records {
				t.Fatalf("batch results=%#v err=%v anchor=%#v", results, err, ledger.Anchor())
			}
			duplicates := 0
			for _, result := range results {
				if result.Duplicate {
					duplicates++
				}
			}
			if duplicates != test.duplicates {
				t.Fatalf("duplicates=%d, want %d", duplicates, test.duplicates)
			}
			if err := ledger.Verify(); err != nil {
				t.Fatal(err)
			}
		})
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

func TestDrainReportsFailedStage(t *testing.T) {
	for _, phase := range []string{"pending", "append", "ack", "between_records"} {
		t.Run(phase, func(t *testing.T) {
			ledger, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			drainer, err := NewDrainer(ledger, 2)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			outbox := &testOutbox{pending: []Envelope{envelopeAt(t, "event-1", 1)}, acked: map[string]bool{}}
			expected := errors.New("synthetic pending failure")
			appendCalls, ackCalls := 0, 0
			switch phase {
			case "pending":
				outbox.pendingError = expected
			case "append":
				outbox.pending = []Envelope{envelopeAt(t, "gap", 2)}
				expected = ErrSourceGap
				appendCalls = 1
			case "ack":
				outbox.failOnce = true
				appendCalls, ackCalls = 1, 1
			case "between_records":
				outbox.afterPending = cancel
				expected = context.Canceled
			}
			report, err := drainer.Drain(ctx, outbox)
			if err == nil || report.Phase != phase || report.PendingCalls != 1 ||
				report.AppendCalls != appendCalls || report.AcknowledgeCalls != ackCalls {
				t.Fatalf("phase=%s report=%#v error=%v", phase, report, err)
			}
			if phase != "ack" && !errors.Is(err, expected) {
				t.Fatalf("error changed: %v", err)
			}
			if report.PendingDuration < 0 || report.AppendDuration < 0 || report.AcknowledgeDuration < 0 || report.AppendMaxDuration > report.AppendDuration {
				t.Fatalf("negative duration: %#v", report)
			}
			if appendCalls == 0 && report.AppendDuration != 0 || ackCalls == 0 && report.AcknowledgeDuration != 0 {
				t.Fatalf("duration attributed to an operation that never ran: %#v", report)
			}
		})
	}
}
