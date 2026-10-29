package mutation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestPlanAdmissionRequiresCompleteBoundedCommands(t *testing.T) {
	id := "admittedrecord1"
	base := Request{ContractVersion: ContractVersion, RequestID: "request", IdempotencyKey: "request", TableID: "table", SchemaRevision: "schema_0001", Actor: Actor{Type: "user", ID: "local-user"}, Operations: []Operation{{Kind: OperationInsert, RecordID: &id, Values: map[string]any{"title": "bounded"}}}}
	authorize := func(context.Context, core.App, string, string) error {
		t.Fatal("invalid command reached authorization")
		return nil
	}
	commit := func(core.App, PlanReceipt) error { t.Fatal("invalid command reached commit"); return nil }
	for _, scenario := range []string{"empty", "no authorization", "no durable result", "invalid version", "missing allocated id", "total operations", "total bytes"} {
		t.Run(scenario, func(t *testing.T) {
			requests := []Request{base}
			auth, save := authorize, commit
			want := "transaction.plan_invalid"
			switch scenario {
			case "empty":
				requests = nil
			case "no authorization":
				auth = nil
			case "no durable result":
				save = nil
			case "invalid version":
				requests[0].ContractVersion = "unsupported"
				want = "mutation.contract.unsupported_version"
			case "missing allocated id":
				requests[0].Operations = []Operation{{Kind: OperationInsert, Values: map[string]any{"title": "bounded"}}}
				want = "transaction.id_required"
			case "total operations":
				request := base
				request.Operations = make([]Operation, 501)
				for i := range request.Operations {
					request.Operations[i] = base.Operations[0]
				}
				requests = []Request{request, request}
				want = "transaction.budget_exceeded"
			case "total bytes":
				request := base
				request.Operations = []Operation{{Kind: OperationInsert, RecordID: &id, Values: map[string]any{"title": strings.Repeat("x", 600*1024)}}}
				requests = []Request{request, request}
				want = "transaction.budget_exceeded"
			}
			_, err := New(nil, nil).ApplyPlan(context.Background(), requests, auth, save)
			var rejected *ProductError
			if !errors.As(err, &rejected) || rejected.Code != want {
				t.Fatalf("admission=%v; want %s", err, want)
			}
		})
	}
}

func TestPlanCancelledWhileWaitingForWriterDoesNotExecute(t *testing.T) {
	kernel := New(nil, nil)
	if err := kernel.coordinator.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer kernel.coordinator.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := "cancelrecord001"
	request := Request{ContractVersion: ContractVersion, RequestID: "cancelled", IdempotencyKey: "cancelled", TableID: "table", SchemaRevision: "schema_0001", Actor: Actor{Type: "user", ID: "local-user"}, Operations: []Operation{{Kind: OperationInsert, RecordID: &id, Values: map[string]any{}}}}
	_, err := kernel.ApplyPlan(ctx, []Request{request}, func(context.Context, core.App, string, string) error { t.Fatal("cancelled plan executed"); return nil }, func(core.App, PlanReceipt) error { t.Fatal("cancelled plan committed"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := kernel.InspectPlan(ctx, []Request{request}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
