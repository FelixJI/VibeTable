package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type transactionFixture struct {
	pb          *pocketbase.PocketBase
	workspace   string
	input       pluginSurfaceAPIRequest
	model       pluginmodel.State
	kernel      *mutation.Kernel
	epoch       uint64
	declaration transactionrules.Declaration
}

type transactionClockObserver struct {
	edges []*core.Record
	calls int
}

func (observer *transactionClockObserver) Calculate(ctx context.Context, app core.App, _ schemaexecution.Table, _ *core.Record) (map[string]any, error) {
	observer.calls++
	for _, edge := range observer.edges {
		current, err := app.FindRecordById("vibetable_computation_dependencies", edge.Id)
		if err != nil {
			return nil, err
		}
		revision, err := relatedcomputation.TableInputRevision(ctx, app, edge.GetString("target_table_id"))
		if err != nil {
			return nil, err
		}
		if int64(current.GetInt(relatedcomputation.InputRevisionField)) != revision {
			return nil, errors.New("formula observed an input clock before the final graph")
		}
	}
	return map[string]any{}, nil
}

func TestTransactionCalculatesOnlyAfterAllInputClocksAdvance(t *testing.T) {
	f := newTransactionFixture(t)
	collection, err := f.pb.FindCollectionByNameOrId("vibetable_computation_dependencies")
	if err != nil {
		t.Fatal(err)
	}
	observer := &transactionClockObserver{}
	for _, binding := range f.model.Bindings {
		edge := core.NewRecord(collection)
		for name, value := range map[string]any{"source_table_id": "tbl_calculated", "computed_field_id": "fld_calculated", "computed_kind": "formula", "target_table_id": binding.TableID, "target_field_id": binding.Fields["amount"], "path_json": map[string]any{"kind": "table"}, "definition_version": 1, relatedcomputation.InputRevisionField: 0} {
			edge.Set(name, value)
		}
		if err := f.pb.Save(edge); err != nil {
			t.Fatal(err)
		}
		observer.edges = append(observer.edges, edge)
	}
	f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithFormulaCalculator(observer))
	f.submit(t, threeTablePlan())
	if observer.calls != 3 {
		t.Fatalf("formula calls=%d want=3 final rows", observer.calls)
	}
}

func newTransactionFixture(t *testing.T, storage ...string) *transactionFixture {
	t.Helper()
	integerUnits := len(storage) > 0 && storage[0] == "integerUnits"
	pb, workspace, input, domain := modelFixture(t)
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		m.Entities = []pluginmodel.Entity{
			{Key: "headers", Label: "合成单头", Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "code", Label: "单号", Type: v2.LogicalText}, {Key: "amount", Label: "金额", Type: v2.LogicalText}, {Key: "lines", Label: "明细", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "lines", Field: "header", Cardinality: "many"}}}},
			{Key: "lines", Label: "合成明细", Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "amount", Label: "金额", Type: v2.LogicalText}, {Key: "header", Label: "单头", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "headers", Field: "lines", Cardinality: "one"}}, {Key: "allocations", Label: "分配", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "allocations", Field: "line", Cardinality: "many"}}}},
			{Key: "allocations", Label: "合成分配", Operations: []string{"read", "query", "create", "update", "delete"}, Fields: []pluginmodel.Field{{Key: "amount", Label: "金额", Type: v2.LogicalText}, {Key: "line", Label: "明细", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "lines", Field: "allocations", Cardinality: "one"}}}},
		}
		if integerUnits {
			for i := range m.Entities {
				for j := range m.Entities[i].Fields {
					field := &m.Entities[i].Fields[j]
					if field.Key == "amount" {
						field.Type, field.Precision = v2.LogicalNumber, "integer"
					}
				}
			}
			m.Entities[0].Fields = append(m.Entities[0].Fields, pluginmodel.Field{Key: "quantity", Label: "普通数量", Type: v2.LogicalNumber, Precision: "float64"})
		}
		if len(storage) > 0 && storage[0] == "state" {
			m.Entities[0].Fields = append(m.Entities[0].Fields, pluginmodel.Field{Key: "status", Label: "合成状态", Type: v2.LogicalText})
		}
		if len(storage) > 0 && storage[0] == "deny-create" {
			m.Entities[2].Operations = []string{"read", "query", "update", "delete"}
		}
		if len(storage) > 0 && storage[0] == "one-to-one" {
			m.Entities = m.Entities[:2]
			m.Entities[0].Fields = []pluginmodel.Field{{Key: "code", Label: "单号", Type: v2.LogicalText}, {Key: "peer", Label: "配对", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "lines", Field: "peer", Cardinality: "one"}}}
			m.Entities[1].Fields = []pluginmodel.Field{{Key: "code", Label: "单号", Type: v2.LogicalText}, {Key: "peer", Label: "配对", Type: v2.LogicalRelation, Relation: &pluginmodel.Relation{Entity: "headers", Field: "peer", Cardinality: "one"}}}
		}
	})
	d := transactionrules.Declaration{Contract: transactionrules.Contract, ModelID: "synthetic", Version: 1, Decimals: []transactionrules.Decimal{}, Rules: []transactionrules.Rule{{ID: "code", Kind: "unique", Entity: "headers", Fields: []string{"code"}}, {ID: "total", Kind: "conservation", Entity: "lines", Target: "headers", Relation: "header", Amount: "amount", Limit: "amount"}, {ID: "allocation", Kind: "balance", Entity: "allocations", Target: "lines", Relation: "line", Amount: "amount", Limit: "amount"}}}
	if len(storage) > 0 && storage[0] == "state" {
		d.Rules = append(d.Rules, transactionrules.Rule{ID: "posted", Kind: "state", Entity: "headers", Field: "status", Fields: []string{"code"}, Transitions: map[string][]string{"draft": {"posted"}, "posted": {}}, FrozenStates: []string{"posted"}})
	}
	for _, entity := range []string{"headers", "lines", "allocations"} {
		format := "decimalText"
		if integerUnits {
			format = "integerUnits"
		}
		d.Decimals = append(d.Decimals, transactionrules.Decimal{Entity: entity, Field: "amount", Format: exactdecimal.Format{Storage: format, Scale: 2, Digits: 38}})
	}
	if len(storage) > 0 && storage[0] == "one-to-one" {
		d.Decimals = []transactionrules.Decimal{}
		d.Rules = []transactionrules.Rule{{ID: "code", Kind: "unique", Entity: "headers", Fields: []string{"code"}}}
	}
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(context.Background(), input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["manifest"].(map[string]any)["permissions"].(map[string]any)["transactions"] = d
	previous := input.InstallationRevision
	input.InstallationRevision++
	obj["revision"] = input.InstallationRevision
	raw, _ = json.Marshal(obj)
	if _, err := store.SaveInstallation(context.Background(), raw, &previous); err != nil {
		t.Fatal(err)
	}
	created, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": created.(modelPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	f := &transactionFixture{pb: pb, workspace: workspace, input: input, model: completed.(pluginmodel.State), kernel: mutation.New(pb, mutation.MetadataSchemaSource{}), epoch: 1, declaration: d}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": d}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *transactionFixture) call(method string, request any) (any, error) {
	return f.callWith(context.Background(), method, request, nil)
}

func (f *transactionFixture) callWith(ctx context.Context, method string, request any, gate businessWriteGate) (any, error) {
	if method == "transaction.commit" {
		if object, ok := request.(map[string]any); ok {
			if _, set := object["previewRevision"]; !set {
				object["previewRevision"] = 1
			}
		}
	}
	input := f.input
	input.Method = method
	input.Request, _ = json.Marshal(request)
	return executeSurfaceTransaction(ctx, f.pb, f.kernel, f.workspace, input, gate, &pluginSurfaceIO{transactionEpoch: func() uint64 { return f.epoch }})
}
func threeTablePlan() transactionPlan {
	return transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "headers", Kind: "create", TempID: "h", Values: map[string]any{"code": "SYN-1", "amount": "10.00"}},
		{Entity: "lines", Kind: "create", TempID: "l", Values: map[string]any{"amount": "10.00", "header": map[string]any{"entity": "headers", "tempId": "h"}}},
		{Entity: "allocations", Kind: "create", TempID: "a", Values: map[string]any{"amount": "8.00", "line": map[string]any{"entity": "lines", "tempId": "l"}}},
	}}
}

func TestTransactionThreeTablesAndDurableResult(t *testing.T) {
	f := newTransactionFixture(t)
	plan := threeTablePlan()
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(transactionPreview)
	raw, err = f.call("transaction.commit", map[string]any{"requestId": p.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(transactionResult)
	if !validTransactionResult(result, f.workspace, f.input.InstallationID, p.RequestID) {
		t.Fatal("successful result is incomplete")
	}
	for _, invalid := range []transactionResult{{}, {Contract: result.Contract, WorkspaceID: f.workspace, InstallationID: f.input.InstallationID, RequestID: p.RequestID, Status: "applied"}} {
		if validTransactionResult(invalid, f.workspace, f.input.InstallationID, p.RequestID) {
			t.Fatal("partial result accepted as complete success")
		}
	}
	if len(result.Receipt.Rows) != 3 || len(result.Receipt.Revisions) != 3 || !reflect.DeepEqual(result.NewIDs, p.NewIDs) {
		t.Fatalf("%+v", result)
	}
	f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{})
	f.epoch++
	for _, method := range []string{"transaction.status", "transaction.commit"} {
		raw, err = f.call(method, map[string]any{"requestId": p.RequestID})
		if err != nil || !reflect.DeepEqual(raw, result) {
			t.Fatalf("%s: %#v %v", method, raw, err)
		}
	}
	plan.Operations[0].Values["code"] = "changed"
	if _, err = f.call("transaction.preview", map[string]any{"requestId": p.RequestID, "plan": plan}); err == nil {
		t.Fatal("different input replayed")
	}
	for _, binding := range f.model.Bindings {
		definition, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindAllRecords(definition.PhysicalName)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows %d %v", len(rows), err)
		}
	}
}

func TestTransactionAtomicRejectionsAndUnknown(t *testing.T) {
	for _, scenario := range []string{"conservation", "reference", "fault", "epoch", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			plan := threeTablePlan()
			if scenario == "conservation" {
				plan.Operations[1].Values["amount"] = "9.00"
			}
			if scenario == "reference" {
				plan.Operations[2].Values["line"] = "missingrecord01"
			}
			raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
			if err != nil {
				if scenario == "reference" {
					for _, binding := range f.model.Bindings {
						table, describeErr := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
						if describeErr != nil {
							t.Fatal(describeErr)
						}
						rows, readErr := f.pb.FindAllRecords(table.PhysicalName)
						if readErr != nil || len(rows) != 0 {
							t.Fatalf("invalid reference preview wrote rows: %d %v", len(rows), readErr)
						}
					}
					return
				}
				t.Fatal(err)
			}
			p := raw.(transactionPreview)
			if scenario == "fault" {
				f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithFaultInjector(func(point string) error {
					if point == "after_audit" {
						return errors.New("injected")
					}
					return nil
				}))
			}
			if scenario == "epoch" {
				f.epoch++
			}
			id := p.RequestID
			if scenario == "missing" {
				id = "unknown-original-id"
			}
			raw, err = f.call("transaction.commit", map[string]any{"requestId": id})
			if scenario == "epoch" || scenario == "missing" {
				if err != nil || raw.(map[string]any)["status"] != "unknown" {
					t.Fatalf("%#v %v", raw, err)
				}
			} else if err == nil {
				t.Fatal("invalid commit succeeded")
			}
			for _, binding := range f.model.Bindings {
				definition, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := f.pb.FindAllRecords(definition.PhysicalName)
				if err != nil || len(rows) != 0 {
					t.Fatalf("orphan rows %d %v", len(rows), err)
				}
			}
			var result transactionResult
			found, err := loadTransactionEvent(context.Background(), f.pb, f.workspace, f.input, p.RequestID, "result", &result)
			if err != nil || found {
				t.Fatalf("false result %v %v", found, err)
			}
		})
	}
}

func TestTransactionRuleGuardsNativeWriteAndPhantom(t *testing.T) {
	f := newTransactionFixture(t)
	raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(transactionPreview)
	if _, err := f.call("transaction.commit", map[string]any{"requestId": p.RequestID}); err != nil {
		t.Fatal(err)
	}
	b := f.model.Bindings["allocations"]
	lineID := p.NewIDs["lines"]["l"]
	request := mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "ordinary", IdempotencyKey: "ordinary", TableID: b.TableID, SchemaRevision: f.model.Revisions[b.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, Values: map[string]any{b.Fields["amount"]: "8.00", b.Fields["line"]: lineID}}}}
	if _, err := f.kernel.Apply(context.Background(), request); err == nil {
		t.Fatal("native phantom bypassed balance")
	}
	definition, err := schemaexecution.Describe(context.Background(), f.pb, b.TableID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.pb.FindAllRecords(definition.PhysicalName)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d %v", len(rows), err)
	}
	request.Actor.Type = "import"
	request.IdempotencyKey = "import"
	request.RequestID = "import"
	importAuthority := &sourceImportAuthority{kernel: f.kernel}
	if _, err := importAuthority.Mutate(context.Background(), request, func(core.App, mutation.Receipt) error { t.Fatal("rejected import committed its receipt"); return nil }); err == nil {
		t.Fatal("import bypassed balance")
	}
	request.Actor.Type = "restore"
	request.IdempotencyKey = "restore"
	request.RequestID = "restore"
	if _, err := f.kernel.Apply(context.Background(), request); err == nil {
		t.Fatal("restore bypassed balance")
	}
}

func (f *transactionFixture) submit(t *testing.T, plan transactionPlan) transactionResult {
	t.Helper()
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = f.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	return raw.(transactionResult)
}

func TestTransactionConcurrentPhantomAndSameRequest(t *testing.T) {
	f := newTransactionFixture(t)
	initial := threeTablePlan()
	initial.Operations[2].Values["amount"] = "0.00"
	seed := f.submit(t, initial)
	previews := make([]transactionPreview, 2)
	for i := range previews {
		plan := transactionPlan{Contract: initial.Contract, ModelID: initial.ModelID, Operations: []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "eight", Values: map[string]any{"amount": "8.00", "line": seed.NewIDs["lines"]["l"]}}}}
		raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
		if err != nil {
			t.Fatal(err)
		}
		previews[i] = raw.(transactionPreview)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range previews {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.call("transaction.commit", map[string]any{"requestId": previews[i].RequestID})
		}(i)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("expected exactly one success: %v", errs)
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var rejected *mutation.ProductError
		if !errors.As(err, &rejected) || rejected.Code != "transaction.balance_violation" {
			t.Fatalf("loser did not detect the committed phantom: %v", err)
		}
	}
	winning := 0
	if errs[0] != nil {
		winning = 1
	}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.call("transaction.commit", map[string]any{"requestId": previews[winning].RequestID})
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("same request replay failed: %v", errs)
	}
}

func TestTransactionReciprocalConflictBudgetRevocationAndNotes(t *testing.T) {
	t.Run("contradiction", func(t *testing.T) {
		f := newTransactionFixture(t)
		plan := threeTablePlan()
		plan.Operations[0].Values["lines"] = []any{}
		raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
		if err != nil {
			var rejected *mutation.ProductError
			if !errors.As(err, &rejected) || rejected.Code != "transaction.relation_conflict" {
				t.Fatal(err)
			}
			return
		}
		if _, err := f.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID}); err == nil {
			t.Fatal("explicit empty relation contradicted reciprocal")
		}
	})
	t.Run("budget", func(t *testing.T) {
		f := newTransactionFixture(t)
		plan := threeTablePlan()
		plan.Operations[0].Values["code"] = strings.Repeat("x", 1<<20)
		if _, err := f.call("transaction.preview", map[string]any{"plan": plan}); err == nil {
			t.Fatal("oversize admitted")
		}
		plan = threeTablePlan()
		plan.Operations = make([]transactionOperation, 1001)
		if _, err := f.call("transaction.preview", map[string]any{"plan": plan}); err == nil {
			t.Fatal("1001 operations admitted")
		}
	})
	t.Run("revoked success stays protected", func(t *testing.T) {
		f := newTransactionFixture(t)
		result := f.submit(t, threeTablePlan())
		if _, err := pluginstore.New(f.pb, f.workspace).SetEnabled(context.Background(), f.input.ProjectKey, f.input.PluginID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := f.call("transaction.status", map[string]any{"requestId": result.RequestID}); err == nil {
			t.Fatal("revoked installation read result")
		}
	})
	t.Run("notes", func(t *testing.T) {
		f := newTransactionFixture(t)
		result := f.submit(t, threeTablePlan())
		binding := f.model.Bindings["headers"]
		field := createSchemaProductField(t, f.pb, binding.TableID, v2.LogicalText, "扩展备注", "transaction-note")
		table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		id := result.NewIDs["headers"]["h"]
		record, err := f.pb.FindRecordById(table.PhysicalName, id)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := productrow.Digest(mutation.ProductRow(f.pb, table, record))
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.kernel.Apply(context.Background(), mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "note", IdempotencyKey: "note", TableID: binding.TableID, SchemaRevision: table.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &id, ExpectedDigest: &digest, Values: map[string]any{field.FieldID: "editable"}}}})
		if err != nil {
			t.Fatal(err)
		}
		plan := threeTablePlan()
		plan.Operations[0].Values["code"] = "SYN-2"
		f.submit(t, plan)
	})
}

func TestTransactionHistoryRestoreCannotBreakConservation(t *testing.T) {
	f := newTransactionFixture(t)
	first := f.submit(t, threeTablePlan())
	line := f.model.Bindings["lines"]
	lineID := first.NewIDs["lines"]["l"]
	history, err := audit.New(f.pb, f.kernel)
	if err != nil {
		t.Fatal(err)
	}
	page, err := history.ReadChangeSets(context.Background(), audit.ReadParams{TableID: line.TableID, ItemID: &lineID, Scope: "row", Limit: 10})
	if err != nil || len(page.ChangeSets) != 1 {
		t.Fatalf("history: %+v %v", page, err)
	}
	target := page.ChangeSets[0].RootRevisionID
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic"}
	for _, entity := range []string{"headers", "lines"} {
		binding := f.model.Bindings[entity]
		var row mutation.PlanRow
		for _, candidate := range first.Receipt.Rows {
			if candidate.TableID == binding.TableID {
				row = candidate
			}
		}
		digest := row.Digest
		plan.Operations = append(plan.Operations, transactionOperation{Entity: entity, Kind: "update", RecordID: row.RecordID, ExpectedDigest: &digest, Values: map[string]any{"amount": "9.00"}})
	}
	f.submit(t, plan)
	preview, err := history.PreviewRestore(context.Background(), audit.PreviewParams{TableID: line.TableID, ItemID: lineID, TargetRevision: target, Scope: "row"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.ApplyRestore(context.Background(), audit.ApplyParams{TableID: line.TableID, ItemID: lineID, Token: preview.Token}); err == nil {
		t.Fatal("history restore bypassed conservation")
	}
}

func TestTransactionRechecksAuthorizationAndRecoversLostAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"revoked", "cancel_before_commit", "lost_ack"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
			if err != nil {
				t.Fatal(err)
			}
			request := map[string]any{"requestId": raw.(transactionPreview).RequestID}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel_before_commit" {
				f.kernel = mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithFaultInjector(func(point string) error {
					if point == "after_outbox" {
						cancel()
					}
					return nil
				}))
			}
			gate := func(ctx context.Context, _, _ string, apply func(context.Context) error) error {
				if scenario == "revoked" {
					if _, err := pluginstore.New(f.pb, f.workspace).SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, false); err != nil {
						return err
					}
				}
				if err := apply(ctx); err != nil {
					return err
				}
				if scenario == "lost_ack" {
					cancel()
					return errors.New("synthetic acknowledgement lost after PB commit")
				}
				return nil
			}
			raw, err = f.callWith(ctx, "transaction.commit", request, gate)
			if scenario == "lost_ack" {
				if err != nil {
					t.Fatal(err)
				}
				status, statusErr := f.call("transaction.status", request)
				if statusErr != nil || !reflect.DeepEqual(raw, status) {
					t.Fatalf("successful result was not sticky: %#v %v", status, statusErr)
				}
			} else if err == nil {
				t.Fatal("revoked or cancelled commit succeeded")
			}
			for _, binding := range f.model.Bindings {
				definition, describeErr := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
				if describeErr != nil {
					t.Fatal(describeErr)
				}
				rows, readErr := f.pb.FindAllRecords(definition.PhysicalName)
				want := 0
				if scenario == "lost_ack" {
					want = 1
				}
				if readErr != nil || len(rows) != want {
					t.Fatalf("committed rows=%d want=%d: %v", len(rows), want, readErr)
				}
			}
		})
	}
}

func TestTransactionConcurrentFirstCommitUsesOneResult(t *testing.T) {
	f := newTransactionFixture(t)
	raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(transactionPreview)
	var ready, done sync.WaitGroup
	ready.Add(2)
	gate := func(ctx context.Context, _, _ string, apply func(context.Context) error) error {
		ready.Done()
		ready.Wait()
		return apply(ctx)
	}
	results := make([]any, 2)
	errs := make([]error, 2)
	for i := range results {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			results[i], errs[i] = f.callWith(context.Background(), "transaction.commit", map[string]any{"requestId": p.RequestID}, gate)
		}(i)
	}
	done.Wait()
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("concurrent first commit: %v %#v", errs, results)
	}
}

func TestTransactionFinalGraphSupportsExplicitCyclesAndWholeDocumentDelete(t *testing.T) {
	f := newTransactionFixture(t)
	plan := threeTablePlan()
	plan.Operations[0].Values["lines"] = []any{map[string]any{"entity": "lines", "tempId": "l"}}
	plan.Operations[1].Values["allocations"] = []any{map[string]any{"entity": "allocations", "tempId": "a"}}
	seed := f.submit(t, plan)
	plan.Operations = nil
	for entity, binding := range f.model.Bindings {
		for _, row := range seed.Receipt.Rows {
			if row.TableID == binding.TableID {
				digest := row.Digest
				plan.Operations = append(plan.Operations, transactionOperation{Entity: entity, Kind: "delete", RecordID: row.RecordID, ExpectedDigest: &digest})
			}
		}
	}
	deleted := f.submit(t, plan)
	if len(deleted.Receipt.Rows) != 3 {
		t.Fatalf("delete receipt=%+v", deleted.Receipt)
	}
	for _, binding := range f.model.Bindings {
		table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindAllRecords(table.PhysicalName)
		if err != nil || len(rows) != 0 {
			t.Fatalf("orphan rows after document delete: %d %v", len(rows), err)
		}
	}
}

func TestTransactionDerivedWriteBudgetRejectsWholePlan(t *testing.T) {
	f := newTransactionFixture(t)
	seed := f.submit(t, threeTablePlan())
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic"}
	for i := 0; i < 1000; i++ {
		plan.Operations = append(plan.Operations, transactionOperation{Entity: "allocations", Kind: "create", TempID: fmt.Sprint(i), Values: map[string]any{"amount": "0.00", "line": seed.NewIDs["lines"]["l"]}})
	}
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err == nil {
		_, err = f.call("transaction.commit", map[string]any{"requestId": raw.(transactionPreview).RequestID})
	}
	var rejected *mutation.ProductError
	if !errors.As(err, &rejected) || rejected.Code != "transaction.budget_exceeded" {
		t.Fatalf("derived row was not counted: %v", err)
	}
	binding := f.model.Bindings["allocations"]
	table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.pb.FindAllRecords(table.PhysicalName)
	if err != nil || len(rows) != 1 {
		t.Fatalf("partial derived-budget write: %d %v", len(rows), err)
	}
}

func TestTransactionInspectionHasNoPocketBaseWritesOrDeleteHooks(t *testing.T) {
	f := newTransactionFixture(t)
	seed := f.submit(t, threeTablePlan())
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic"}
	for entity, binding := range f.model.Bindings {
		for _, row := range seed.Receipt.Rows {
			if row.TableID == binding.TableID {
				digest := row.Digest
				plan.Operations = append(plan.Operations, transactionOperation{Entity: entity, Kind: "delete", RecordID: row.RecordID, ExpectedDigest: &digest})
			}
		}
	}
	preview := transactionPreview{Contract: transactionrules.Contract, WorkspaceID: f.workspace, InstallationID: f.input.InstallationID, RequestID: "inspection-only", Epoch: f.epoch, Plan: plan, Model: f.model, Declaration: f.declaration, NewIDs: map[string]map[string]string{}}
	installed, err := surfaceInstallation(context.Background(), f.pb, f.workspace, f.input)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := transactionRequests(context.Background(), f.pb, f.input, installed, preview)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	observe := func(event *core.RecordEvent) error { writes++; return event.Next() }
	f.pb.OnRecordCreate().BindFunc(observe)
	f.pb.OnRecordUpdate().BindFunc(observe)
	f.pb.OnRecordDelete().BindFunc(observe)
	inspection, err := f.kernel.InspectPlan(context.Background(), requests, transactionAuthorize(f.input, f.workspace))
	if err != nil || len(inspection.Effects) != 3 || writes != 0 {
		t.Fatalf("inspection effects=%d writes/hooks=%d error=%v", len(inspection.Effects), writes, err)
	}
	for _, binding := range f.model.Bindings {
		table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.pb.FindAllRecords(table.PhysicalName)
		if err != nil || len(rows) != 1 {
			t.Fatalf("inspection changed business rows: %d %v", len(rows), err)
		}
	}
}

func TestTransactionConfirmedEffectsBecomeStaleAndRefreshKeepsRequest(t *testing.T) {
	f := newTransactionFixture(t)
	seed := f.submit(t, threeTablePlan())
	plan := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "later", Values: map[string]any{"amount": "0.00", "line": seed.NewIDs["lines"]["l"]}}}}
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(transactionPreview)
	if len(p.Effects) != 2 || len(p.DataRevisions) != 2 {
		t.Fatalf("reciprocal impact absent: %+v", p)
	}
	other := plan
	other.Operations = []transactionOperation{{Entity: "allocations", Kind: "create", TempID: "other", Values: map[string]any{"amount": "0.00", "line": seed.NewIDs["lines"]["l"]}}}
	f.submit(t, other)
	if _, err = f.call("transaction.commit", map[string]any{"requestId": p.RequestID, "previewRevision": p.PreviewRevision}); err == nil {
		t.Fatal("confirmation expanded to a changed reciprocal row")
	}
	raw, err = f.call("transaction.preview", map[string]any{"requestId": p.RequestID, "plan": plan, "refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := raw.(transactionPreview)
	if refreshed.RequestID != p.RequestID || refreshed.PreviewRevision != 2 || !reflect.DeepEqual(refreshed.NewIDs, p.NewIDs) || reflect.DeepEqual(refreshed.DataRevisions, p.DataRevisions) {
		t.Fatalf("refresh replaced identity or missed versions: %+v", refreshed)
	}
	if _, err = f.call("transaction.commit", map[string]any{"requestId": p.RequestID, "previewRevision": 1}); err == nil {
		t.Fatal("old native confirmation committed refreshed plan")
	}
	if _, err = f.call("transaction.commit", map[string]any{"requestId": p.RequestID, "previewRevision": 2}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionIntegerUnitsRejectRoundedWireFractionsAtEveryEntry(t *testing.T) {
	f := newTransactionFixture(t, "integerUnits")
	for _, number := range []string{"9007199254740990.9", "9007199254740991.1", "9007199254740992"} {
		input := f.input
		input.Method = "transaction.preview"
		input.Request = []byte(`{"plan":{"contract":"vibetable.transaction-plan.v1","modelId":"synthetic","operations":[{"entity":"headers","kind":"create","tempId":"raw","values":{"code":"SYN-raw","amount":` + number + `}}]}}`)
		if _, err := executePluginSurfaceAPI(context.Background(), f.pb, nil, f.kernel, f.workspace, input, nil, &pluginSurfaceIO{transactionEpoch: func() uint64 { return f.epoch }}); err == nil {
			t.Fatalf("transaction accepted rounded fraction %s", number)
		}
		input.Method = "mutation.preview"
		binding := f.model.Bindings["headers"]
		input.Request = []byte(`{"contract":"vibetable.surface-mutation-plan.v1","collection":"` + binding.TableID + `","schemaRevision":"` + f.model.Revisions[binding.TableID] + `","operations":[{"kind":"create","values":{"` + binding.Fields["code"] + `":"SYN-raw","` + binding.Fields["amount"] + `":` + number + `}}]}`)
		if _, err := executePluginSurfaceAPI(context.Background(), f.pb, nil, f.kernel, f.workspace, input, nil); err == nil {
			t.Fatalf("single-table surface accepted rounded fraction %s", number)
		}
		for _, rawInput := range []bool{false, true} {
			var request mutation.Request
			operation := mutation.Operation{Kind: mutation.OperationInsert, Values: map[string]any{binding.Fields["amount"]: json.Number(number)}}
			if rawInput {
				operation.Values = map[string]any{}
				operation.RawValues = map[string]any{binding.Fields["amount"]: number}
			}
			raw, err := json.Marshal(mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: "wire", IdempotencyKey: "wire", TableID: binding.TableID, SchemaRevision: f.model.Revisions[binding.TableID], Actor: mutation.Actor{Type: "user", ID: "local-user"}, Operations: []mutation.Operation{operation}})
			if err != nil {
				t.Fatal(err)
			}
			if err := mutation.DecodeStrict(raw, &request); err != nil {
				t.Fatal(err)
			}
			if _, err := f.kernel.Preview(context.Background(), request); err == nil {
				t.Fatalf("native raw=%v accepted rounded fraction %s", rawInput, number)
			}
		}
	}
	input := f.input
	input.Method = "transaction.preview"
	input.Request = []byte(`{"plan":{"contract":"vibetable.transaction-plan.v1","modelId":"synthetic","operations":[{"entity":"headers","kind":"create","tempId":"raw","values":{"code":"SYN-raw","amount":9007199254740991}}]}}`)
	if _, err := executePluginSurfaceAPI(context.Background(), f.pb, nil, f.kernel, f.workspace, input, nil, &pluginSurfaceIO{transactionEpoch: func() uint64 { return f.epoch }}); err != nil {
		t.Fatalf("exact safe integer rejected: %v", err)
	}
}

func TestTransactionNumericPlansRetainSameKeyAcrossPersistenceAndRefresh(t *testing.T) {
	f := newTransactionFixture(t, "integerUnits")
	plan := threeTablePlan()
	for i := range plan.Operations {
		plan.Operations[i].Values["amount"] = json.Number("1000")
	}
	plan.Operations[0].Values["quantity"] = json.Number("1.25")
	plan.Operations[2].Values["amount"] = json.Number("800")
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	p := raw.(transactionPreview)
	again, err := f.call("transaction.preview", map[string]any{"requestId": p.RequestID, "plan": plan})
	if err != nil || !reflect.DeepEqual(again, p) {
		t.Fatalf("numeric plan changed after persistence: %#v %v", again, err)
	}
	raw, err = f.call("transaction.preview", map[string]any{"requestId": p.RequestID, "plan": plan, "refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := raw.(transactionPreview)
	if !reflect.DeepEqual(refreshed.Plan, p.Plan) || !reflect.DeepEqual(refreshed.NewIDs, p.NewIDs) {
		t.Fatal("refresh changed numeric plan or allocated IDs")
	}
	result, err := f.call("transaction.commit", map[string]any{"requestId": p.RequestID, "previewRevision": refreshed.PreviewRevision})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.call("transaction.preview", map[string]any{"requestId": p.RequestID, "plan": plan})
	if err != nil || !reflect.DeepEqual(result, replayed) {
		t.Fatalf("numeric same-key success did not replay: %#v %v", replayed, err)
	}
}

func TestTransactionReplicaRecoveryInvalidatesPendingButRetainsSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Kopia-backed recovery")
	}
	f := newTransactionFixture(t)
	f.epoch = 7
	success := f.submit(t, threeTablePlan())
	plan := threeTablePlan()
	plan.Operations[0].Values["code"] = "SYN-2"
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	pending := raw.(transactionPreview)
	sourceData := f.pb.DataDir()
	if err := f.pb.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}
	activity := filepath.Join(t.TempDir(), "activity")
	metadata := filepath.Join(activity, ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	database, err := os.ReadFile(filepath.Join(sourceData, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata, "data", "data.db"), database, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"contractVersion": "2.0", "formatVersion": 3, "workspaceId": f.workspace, "displayName": "合成恢复", "createdAt": "2026-07-28T08:00:00Z", "storageMode": "mirrored", "encryptionMode": "convenient", "repositoryFormat": "kopia-v4", "topologySchemaVersion": 1, "businessSchemaVersion": 1, "importedFromWorkspaceId": nil, "sourceSnapshotId": nil})
	if err := os.WriteFile(filepath.Join(metadata, "workspace.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "replica")
	if err := os.MkdirAll(filepath.Join(selected, ".vibetable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, ".vibetable", "workspace.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	options := workspacev2.ReplicaOneShotOptions{DataDir: filepath.Join(metadata, "data"), ActivityRoot: activity, ReplicaRoot: selected, WorkspaceID: f.workspace, SessionEpoch: 1, FenceEpoch: 1, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	if _, err := workspacev2.InitializeWorkspaceReplica(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.ActivityRoot = filepath.Join(t.TempDir(), "recovered")
	options.DataDir = filepath.Join(options.ActivityRoot, ".vibetable", "data")
	if _, err := workspacev2.RecoverWorkspaceReplica(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	epoch, err := writecoordinator.ReadPersistentBusinessEpoch(context.Background(), filepath.Join(options.ActivityRoot, ".vibetable", "coordination", "write-coordinator.db"), f.workspace)
	if err != nil || epoch <= pending.Epoch {
		t.Fatalf("restored epoch %d %v", epoch, err)
	}
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: options.DataDir, HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pb.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	})
	f.pb, f.kernel, f.epoch = pb, mutation.New(pb, mutation.MetadataSchemaSource{}), epoch
	for _, id := range []string{pending.RequestID, "missing-before-snapshot"} {
		for _, method := range []string{"transaction.preview", "transaction.commit", "transaction.status"} {
			request := map[string]any{"requestId": id}
			if method == "transaction.preview" {
				request["plan"] = plan
			}
			raw, err := f.call(method, request)
			if err != nil || raw.(map[string]any)["status"] != "unknown" {
				t.Fatalf("%s: %#v %v", method, raw, err)
			}
		}
	}
	raw, err = f.call("transaction.commit", map[string]any{"requestId": success.RequestID})
	if err != nil || !reflect.DeepEqual(raw, success) {
		t.Fatalf("success lost: %#v %v", raw, err)
	}
	f.submit(t, plan)
}
