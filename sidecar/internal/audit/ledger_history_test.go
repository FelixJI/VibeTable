package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
)

func appendLedgerPayload(
	t *testing.T,
	ledger *auditledger.Ledger,
	sequence uint64,
	payload map[string]any,
) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := auditledger.NewEnvelope(
		fmt.Sprintf("event-%d", sequence),
		businessHistoryEpoch,
		sequence,
		fmt.Sprintf("mutation-%d", sequence),
		raw,
		time.Date(2026, 7, 29, 10, int(sequence), 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Append(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerHistoryIgnoresKnownReceiptAndRejectsUnknownTypedEvent(
	t *testing.T,
) {
	t.Run("known business receipt", func(t *testing.T) {
		ledger, err := auditledger.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		appendLedgerPayload(t, ledger, 1, map[string]any{
			"type":             businessReceiptType,
			"workspaceId":      "workspace-1",
			"sessionEpoch":     1,
			"fenceEpoch":       1,
			"claimId":          "claim-1",
			"mutationRevision": 1,
			"kind":             "row.update",
			"identity":         "operation-1",
		})
		service := &Service{ledger: ledger}
		events, err := service.projectLedgerHistory(context.Background())
		if err != nil || len(events) != 0 {
			t.Fatalf("known receipt projection = %#v, %v", events, err)
		}
	})
	t.Run("unknown typed event", func(t *testing.T) {
		ledger, err := auditledger.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		appendLedgerPayload(t, ledger, 1, map[string]any{
			"type": "workspace.v2.unknown-business-event",
		})
		service := &Service{ledger: ledger}
		if _, err := service.projectLedgerHistory(
			context.Background(),
		); !errors.Is(err, auditledger.ErrChainCorrupt) {
			t.Fatalf("unknown typed event accepted: %v", err)
		}
	})
	t.Run("malformed rotated business epoch", func(t *testing.T) {
		ledger, err := auditledger.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		raw, _ := json.Marshal(map[string]any{
			"type":             businessReceiptType,
			"workspaceId":      "workspace-1",
			"sessionEpoch":     1,
			"fenceEpoch":       1,
			"claimId":          "claim-1",
			"mutationRevision": 1,
			"kind":             "row.update",
			"identity":         "operation-1",
		})
		envelope, err := auditledger.NewEnvelope(
			"event-1",
			"business-v2:not-a-workspace:00000000000000000002",
			1,
			"mutation-1",
			raw,
			time.Now().UTC(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.Append(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
		service := &Service{ledger: ledger}
		if _, err := service.projectLedgerHistory(
			context.Background(),
		); !errors.Is(err, auditledger.ErrChainCorrupt) {
			t.Fatalf("malformed business epoch accepted: %v", err)
		}
	})
}

func TestBusinessHistoryEpochValidation(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{businessHistoryEpoch, true},
		{
			"business-v2:11111111-1111-4111-8111-111111111111:00000000000000000002",
			true,
		},
		{"business-v2:not-a-workspace:00000000000000000002", false},
		{
			"business-v2:11111111-1111-4111-8111-111111111111:00000000000000000001",
			false,
		},
		{"business-v2:11111111-1111-4111-8111-111111111111:2:extra", false},
	} {
		if actual := isBusinessHistoryEpoch(test.value); actual != test.valid {
			t.Fatalf(
				"isBusinessHistoryEpoch(%q) = %v, want %v",
				test.value,
				actual,
				test.valid,
			)
		}
	}
}
