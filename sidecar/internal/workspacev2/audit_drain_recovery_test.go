package workspacev2

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/diagnostics"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type auditDrainRecoveryOutbox struct {
	auditledger.OutboxStore
	ackEntered chan struct{}
	firstAck   sync.Once
}

func (probe *auditDrainRecoveryOutbox) Acknowledge(ctx context.Context, event, epoch string, sequence uint64) error {
	probe.firstAck.Do(func() { close(probe.ackEntered) })
	return probe.OutboxStore.Acknowledge(ctx, event, epoch, sequence)
}
func auditDrainDatabase(t *testing.T, builder any) *sql.DB {
	t.Helper()
	provider, ok := builder.(interface{ DB() *sql.DB })
	if !ok {
		t.Fatal("fixture did not expose its SQLite pool")
	}
	return provider.DB()
}

func seedAuditDrainEvents(t *testing.T, db *sql.DB) {
	t.Helper()
	transaction, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	statement, err := transaction.Prepare(`INSERT INTO vibetable_audit_outbox
        (event_id,source_epoch,source_sequence,mutation_identity,payload_hash,payload_json,occurred_at,status)
        VALUES (?,?,?,?,?,?,?,'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	for index := 1; index <= 2; index++ {
		envelope, err := auditledger.NewEnvelope(fmt.Sprintf("probe-%d", index), "probe-epoch", uint64(index),
			fmt.Sprintf("mutation-%d", index), json.RawMessage(`{"type":"insert","after":{"title":"synthetic probe","code":"000123","amount":1.5}}`),
			time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := statement.Exec(envelope.EventID, envelope.SourceEpoch, envelope.SourceSequence, envelope.MutationIdentity,
			envelope.PayloadHash, []byte(envelope.Payload), envelope.OccurredAt.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func requestAuditDrainSnapshot(fixture *phaseLogFixture, ctx context.Context, sequence uint64) <-chan protocolv2.ResponseEnvelope {
	done := make(chan protocolv2.ResponseEnvelope, 1)
	go func() {
		raw := fmt.Sprintf(`{"jsonrpc":"2.0","id":"probe-%d","method":"snapshot.request","wire":{"scope":"workspace","workspaceId":"%s","sessionEpoch":7,"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-%012d","sequence":%d},"params":{"trigger":"protection","urgency":"foreground"}}`, sequence, testWorkspaceID, sequence, sequence)
		done <- fixture.runtime.Dispatcher().DispatchEnvelope(ctx, []byte(raw))
	}()
	return done
}

func awaitAuditDrainSnapshot(t *testing.T, done <-chan protocolv2.ResponseEnvelope) protocolv2.ResponseEnvelope {
	t.Helper()
	select {
	case response := <-done:
		return response
	case <-time.After(35 * time.Second):
		t.Fatal("snapshot failed to retire after its unchanged 30-second context budget")
		return protocolv2.ResponseEnvelope{}
	}
}

// A canceled PB acknowledgement must leave the ledger durable and replay without duplication.
func TestProtectionAuditDrainRecoversAfterWriterPoolCancellation(t *testing.T) {
	logWriter := &lockedPhaseLogBuffer{}
	fixture := openRuntimeWithPhaseLogger(t, slog.New(diagnostics.NewJSONHandler(logWriter, slog.LevelInfo)), logWriter)
	writer := auditDrainDatabase(t, fixture.app.NonconcurrentDB())
	seedAuditDrainEvents(t, writer)
	beforeLedger := fixture.ledger.Anchor().LedgerSequence
	outbox := &auditDrainRecoveryOutbox{OutboxStore: fixture.runtime.frozenSource.auditOutbox, ackEntered: make(chan struct{})}
	fixture.runtime.frozenSource.auditOutbox = outbox
	writerBefore := writer.Stats()
	held, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := requestAuditDrainSnapshot(fixture, ctx, 1)
	select {
	case <-outbox.ackEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not reach first PB acknowledgement")
	}
	poll := time.NewTicker(time.Millisecond)
	deadline := time.NewTimer(5 * time.Second)
	defer poll.Stop()
	defer deadline.Stop()
	for writer.Stats().WaitCount == writerBefore.WaitCount {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("PB write pool did not observe blocked acknowledgement")
		}
	}
	cancel()
	response := awaitAuditDrainSnapshot(t, done)
	events := auditDrainEvents(t, logWriter)
	if len(events) != 1 {
		t.Fatalf("canceled source reports: %#v", events)
	}
	detail := events[0]["auditDrain"].(map[string]any)
	t.Logf("canceled report=%v", detail)
	if response.Error == nil || detail["source"] != "business" || detail["phase"] != "ack" ||
		detail["appended"] != float64(1) || detail["acknowledged"] != float64(0) ||
		detail["pbWriteWaitCount"].(float64) < 1 {
		t.Fatalf("PB wait was not attributed to ack: %#v", events[0])
	}
	if fixture.ledger.Anchor().LedgerSequence != beforeLedger+1 {
		t.Fatal("cancellation lost durable append")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer retryCancel()
	response = awaitAuditDrainSnapshot(t, requestAuditDrainSnapshot(fixture, retryCtx, 2))
	if response.Error != nil {
		t.Fatalf("acknowledgement recovery failed: %#v", response.Error)
	}
	events = auditDrainEvents(t, logWriter)
	if len(events) != 3 {
		t.Fatalf("recovery source reports: %#v", events)
	}
	detail = events[1]["auditDrain"].(map[string]any)
	t.Logf("recovery report=%v", detail)
	if detail["phase"] != "completed" || detail["duplicates"] != float64(1) ||
		detail["appended"] != float64(1) || detail["acknowledged"] != float64(2) ||
		fixture.ledger.Anchor().LedgerSequence != beforeLedger+2 {
		t.Fatalf("durable event was not deduplicated: %#v", detail)
	}
}
