package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type transactionFormulaFunc func(context.Context, core.App, schemaexecution.Table, *core.Record) (map[string]any, error)

func (f transactionFormulaFunc) Calculate(ctx context.Context, app core.App, table schemaexecution.Table, row *core.Record) (map[string]any, error) {
	return f(ctx, app, table, row)
}

type transactionInvalidatorFunc func(context.Context, core.App, mutation.DataChangedEvent) ([]string, error)

func (f transactionInvalidatorFunc) EnqueueInvalidations(ctx context.Context, app core.App, event mutation.DataChangedEvent) ([]string, error) {
	return f(ctx, app, event)
}

type transactionPublisherFunc func(context.Context, mutation.DataChangedEvent) error

func (f transactionPublisherFunc) Publish(ctx context.Context, event mutation.DataChangedEvent) error {
	return f(ctx, event)
}

// Snapshot business rows and their existing transactional history, clocks and
// delivery facts; admission audit events are deliberately outside this set.
func transactionBusinessSnapshot(t *testing.T, f *transactionFixture) string {
	t.Helper()
	state := map[string]any{}
	for _, binding := range f.model.Bindings {
		table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindRecordsByFilter(table.PhysicalName, "", "id", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		images := []map[string]any{}
		for _, row := range rows {
			images = append(images, mutation.ProductRow(f.pb, table, row))
		}
		state[binding.TableID] = images
		state[binding.TableID+":revision"] = table.Snapshot.DataRevision
	}
	for _, name := range []string{"vibetable_audit_events", "vibetable_audit_outbox", "vibetable_outbox"} {
		rows, err := f.pb.FindRecordsByFilter(name, "", "id", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		images := []map[string]any{}
		for _, row := range rows {
			images = append(images, row.FieldsData())
		}
		state[name] = images
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTransactionFailingCommitStagesRollBackEveryBusinessFact(t *testing.T) {
	for _, scenario := range []string{"base row", "final row", "reciprocal", "clock", "outbox", "audit", "invalidator", "formula", "after record", "after outbox", "before commit"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
			if err != nil {
				t.Fatal(err)
			}
			preview := raw.(transactionPreview)
			before := transactionBusinessSnapshot(t, f)
			injected := errors.New("synthetic transaction stage failure")
			observed := false
			options := []mutation.Option{}
			header := f.model.Bindings["headers"]
			table, err := schemaexecution.Describe(context.Background(), f.pb, header.TableID)
			if err != nil {
				t.Fatal(err)
			}
			lines, _ := table.Field(header.Fields["lines"])
			switch scenario {
			case "invalidator":
				options = append(options, mutation.WithComputationInvalidator(transactionInvalidatorFunc(func(context.Context, core.App, mutation.DataChangedEvent) ([]string, error) {
					observed = true
					return nil, injected
				})))
			case "formula":
				options = append(options, mutation.WithFormulaCalculator(transactionFormulaFunc(func(context.Context, core.App, schemaexecution.Table, *core.Record) (map[string]any, error) {
					observed = true
					return nil, injected
				})))
			case "after record", "after outbox", "before commit":
				point := strings.ReplaceAll(scenario, " ", "_")
				options = append(options, mutation.WithFaultInjector(func(actual string) error {
					if actual == point {
						observed = true
						return injected
					}
					return nil
				}))
			default:
				fail := func(event *core.RecordEvent) error {
					name := event.Record.Collection().Name
					reject := false
					switch scenario {
					case "base row", "final row":
						reject = name == table.PhysicalName
					case "reciprocal":
						reject = name == table.PhysicalName && len(event.Record.GetStringSlice(lines.Identity.PhysicalName)) > 0
					case "clock":
						reject = name == "vibetable_tables"
					case "outbox":
						reject = name == "vibetable_outbox"
					case "audit":
						reject = name == "vibetable_audit_events"
					}
					if reject {
						observed = true
						return injected
					}
					return event.Next()
				}
				if scenario == "base row" || scenario == "outbox" || scenario == "audit" {
					f.pb.OnRecordCreate().BindFunc(fail)
				} else {
					f.pb.OnRecordUpdate().BindFunc(fail)
				}
			}
			f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, options...)
			if _, err = f.call("transaction.commit", map[string]any{"requestId": preview.RequestID}); err == nil || !observed {
				t.Fatalf("stage was not reached and rejected: observed=%v err=%v", observed, err)
			}
			if after := transactionBusinessSnapshot(t, f); after != before {
				t.Fatalf("failed %s changed rows, clocks, history or outbox", scenario)
			}
			status, err := f.call("transaction.status", map[string]any{"requestId": preview.RequestID})
			if err != nil || status.(transactionPreview).Status != "prepared" {
				t.Fatalf("failure invented a committed result: %#v %v", status, err)
			}
		})
	}
}

func TestTransactionPublisherFailureKeepsOneDurableSuccess(t *testing.T) {
	f := newTransactionFixture(t)
	called := 0
	f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithPublisher(transactionPublisherFunc(func(context.Context, mutation.DataChangedEvent) error {
		called++
		return errors.New("synthetic notification unavailable")
	})))
	result := f.submit(t, threeTablePlan())
	if called == 0 || result.Status != "applied" {
		t.Fatal("publisher failure erased commit")
	}
	for _, event := range result.Receipt.EmittedEvents {
		pending, err := f.pb.FindFirstRecordByFilter("vibetable_outbox", "event_id={:event}", dbx.Params{"event": event})
		if err != nil || pending.GetString("status") != "pending" {
			t.Fatalf("failed delivery lost its pending outbox: %v", err)
		}
	}
	before := transactionBusinessSnapshot(t, f)
	for _, method := range []string{"transaction.status", "transaction.commit"} {
		replayed, err := f.call(method, map[string]any{"requestId": result.RequestID})
		if err != nil || !reflect.DeepEqual(replayed, result) {
			t.Fatalf("late %s changed success: %#v %v", method, replayed, err)
		}
	}
	if transactionBusinessSnapshot(t, f) != before {
		t.Fatal("success replay duplicated data, history or outbox")
	}
}

func TestTransactionPlanRejectsStaleAmbiguousAndUnauthorizedRows(t *testing.T) {
	for _, scenario := range []string{"global revision", "row digest", "missing row", "duplicate row", "explicit permission", "exhausted clock"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			seed := f.submit(t, threeTablePlan())
			binding := f.model.Bindings["headers"]
			id := seed.NewIDs["headers"]["h"]
			var digest string
			for _, row := range seed.Receipt.Rows {
				if row.TableID == binding.TableID {
					digest = row.Digest
				}
			}
			request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "rejected-plan", IdempotencyKey: "rejected-plan", TableID: binding.TableID, SchemaRevision: f.model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &id, ExpectedDigest: &digest, Values: map[string]any{binding.Fields["code"]: "SYN-2"}}}}
			want := "mutation.digest_conflict"
			denied := errors.New("explicit permission revoked inside transaction")
			authorize := func(context.Context, core.App, string, string) error { return nil }
			switch scenario {
			case "global revision":
				revision := "row_9999"
				request.ExpectedRevision = &revision
				want = "mutation.revision_conflict"
			case "row digest":
				wrong := "sha256:" + strings.Repeat("0", 64)
				request.Operations[0].ExpectedDigest = &wrong
			case "missing row":
				missing := "missingrecord01"
				request.Operations[0].RecordID = &missing
				want = "mutation.record.not_found"
			case "duplicate row":
				request.Operations = append(request.Operations, request.Operations[0])
				want = "transaction.duplicate_row"
			case "explicit permission":
				authorize = func(context.Context, core.App, string, string) error { return denied }
			case "exhausted clock":
				if _, err := f.pb.DB().Update("vibetable_tables", dbx.Params{"data_revision": int64(1<<53 - 1)}, dbx.HashExp{"table_id": binding.TableID}).Execute(); err != nil {
					t.Fatal(err)
				}
				want = "mutation.metadata.data_revision_exhausted"
			}
			before := transactionBusinessSnapshot(t, f)
			_, err := f.kernel.ApplyPlan(context.Background(), []mutation.Request{request}, authorize, func(core.App, mutation.PlanReceipt) error {
				t.Fatal("rejected plan committed its durable result")
				return nil
			})
			if scenario == "explicit permission" {
				if !errors.Is(err, denied) {
					t.Fatal(err)
				}
			} else {
				var rejected *mutation.ProductError
				if !errors.As(err, &rejected) || rejected.Code != want {
					t.Fatalf("%s: %v, want %s", scenario, err, want)
				}
			}
			if transactionBusinessSnapshot(t, f) != before {
				t.Fatalf("%s changed business state", scenario)
			}
		})
	}
}

func TestTransactionDerivedPermissionFailureRollsBackSourceAndTarget(t *testing.T) {
	f := newTransactionFixture(t)
	seed := f.submit(t, threeTablePlan())
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "new", Values: map[string]any{"amount": "0.00", "line": seed.NewIDs["lines"]["l"]}}}}
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	preview := raw.(transactionPreview)
	installed, err := surfaceInstallation(context.Background(), f.pb, f.workspace, f.input)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := transactionRequests(context.Background(), f.pb, f.input, installed, preview)
	if err != nil {
		t.Fatal(err)
	}
	before := transactionBusinessSnapshot(t, f)
	denied := errors.New("reciprocal target update was revoked")
	_, err = f.kernel.ApplyPlan(context.Background(), requests, func(_ context.Context, _ core.App, table, kind string) error {
		if table == f.model.Bindings["lines"].TableID && kind == "update" {
			return denied
		}
		return nil
	}, func(core.App, mutation.PlanReceipt) error { t.Fatal("unauthorized reciprocal committed"); return nil })
	if !errors.Is(err, denied) || transactionBusinessSnapshot(t, f) != before {
		t.Fatalf("derived denial failed atomic rollback: %v", err)
	}
}

func TestTransactionExplicitReferenceCannotTargetSamePlanDeletion(t *testing.T) {
	f := newTransactionFixture(t)
	seed := f.submit(t, threeTablePlan())
	header := f.model.Bindings["headers"]
	var digest string
	for _, row := range seed.Receipt.Rows {
		if row.TableID == header.TableID {
			digest = row.Digest
		}
	}
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "headers", Kind: "delete", RecordID: seed.NewIDs["headers"]["h"], ExpectedDigest: &digest},
		{Entity: "lines", Kind: "create", TempID: "new-line", Values: map[string]any{"amount": "0.00", "header": seed.NewIDs["headers"]["h"]}},
	}}
	before := transactionBusinessSnapshot(t, f)
	_, err := f.call("transaction.preview", map[string]any{"plan": plan})
	var rejected *mutation.ProductError
	if !errors.As(err, &rejected) || rejected.Code != "transaction.relation_conflict" {
		t.Fatalf("explicit reference into deletion=%v", err)
	}
	if transactionBusinessSnapshot(t, f) != before {
		t.Fatal("rejected relation preview changed business rows")
	}
}
