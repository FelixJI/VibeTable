package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

func schemaAuditPayload() map[string]any {
	return map[string]any{
		"contract":       "vibetable.schema.v2",
		"operationId":    "operation-schema-1",
		"planId":         "plan-schema-1",
		"action":         "create",
		"tableId":        "orders",
		"fieldId":        "fld_status",
		"schemaRevision": "schema_0002",
		"beforeHash":     "",
		"afterHash":      "sha256:after",
		"actor": map[string]any{
			"kind": "user",
			"id":   "actor-1",
		},
		"outcome":        "applied",
		"migrationJobId": "",
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
			"type": "workspace.v2.unknown-secret-business-event",
		})
		var logs bytes.Buffer
		service := &Service{
			ledger: ledger,
			logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		}
		if _, err := service.projectLedgerHistory(
			context.Background(),
		); !errors.Is(err, auditledger.ErrChainCorrupt) {
			t.Fatalf("unknown typed event accepted: %v", err)
		}
		logged := logs.String()
		if !strings.Contains(logged, `"msg":"history.ledger_projection_failed"`) ||
			!strings.Contains(logged, `"errorCode":"audit.chain_corrupt"`) {
			t.Fatalf("missing safe history diagnostic: %s", logged)
		}
		if strings.Contains(logged, "unknown-secret-business-event") {
			t.Fatalf("payload escaped into history diagnostic: %s", logged)
		}
	})
	t.Run("malformed schema audit", func(t *testing.T) {
		ledger, err := auditledger.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		payload := schemaAuditPayload()
		delete(payload, "outcome")
		appendLedgerPayload(t, ledger, 1, payload)
		service := &Service{ledger: ledger}
		if _, err := service.projectLedgerHistory(
			context.Background(),
		); !errors.Is(err, auditledger.ErrChainCorrupt) {
			t.Fatalf("malformed schema audit accepted: %v", err)
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

func TestLedgerHistoryIgnoresSchemaAuditAndProjectsRowMutation(t *testing.T) {
	ledger, err := auditledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	appendLedgerPayload(t, ledger, 1, schemaAuditPayload())
	appendLedgerPayload(t, ledger, 2, map[string]any{
		"revisionId":     "revision-1",
		"changeSetId":    "change-set-1",
		"sequence":       1,
		"tableId":        "orders",
		"recordId":       "record-1",
		"operation":      "update",
		"before":         map[string]any{"status": "todo"},
		"after":          map[string]any{"status": "done"},
		"schemaRevision": 2,
		"dataRevision":   1,
		"requestId":      "request-1",
		"actor": map[string]any{
			"type":        "user",
			"id":          "actor-1",
			"displayName": nil,
		},
	})

	service := &Service{ledger: ledger}
	events, err := service.projectLedgerHistory(context.Background())
	if err != nil {
		t.Fatalf("mixed schema and row history projection failed: %v", err)
	}
	if len(events) != 1 || events[0].Id != "revision-1" {
		t.Fatalf("projected events = %#v", events)
	}
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
		{
			"business-v2:11111111111141118111111111111111:00000000000000000002",
			false,
		},
		{"business-v2:11111111-1111-4111-8111-111111111111:2", false},
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
