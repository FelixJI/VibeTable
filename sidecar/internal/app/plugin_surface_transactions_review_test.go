package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestTransactionAppliedRecoveryAfterInstallationToggle(t *testing.T) {
	f := newTransactionFixture(t)
	result := f.submit(t, threeTablePlan())
	store := pluginstore.New(f.pb, f.workspace)
	for _, enabled := range []bool{false, true} {
		raw, err := store.SetEnabled(context.Background(), f.input.ProjectKey, f.input.PluginID, enabled)
		if err != nil {
			t.Fatal(err)
		}
		var installed pluginSurfaceInstallation
		if err := json.Unmarshal(raw, &installed); err != nil {
			t.Fatal(err)
		}
		f.input.InstallationRevision = installed.Revision
		if !enabled {
			if _, err := f.call("transaction.status", map[string]any{"requestId": result.RequestID}); err == nil {
				t.Fatal("disabled installation read applied result")
			}
		}
	}
	before := transactionBusinessSnapshot(t, f)
	for _, method := range []string{"transaction.status", "transaction.commit", "transaction.preview"} {
		request := map[string]any{"requestId": result.RequestID}
		if method == "transaction.preview" {
			request["plan"] = threeTablePlan()
		}
		recovered, err := f.call(method, request)
		if err != nil || !reflect.DeepEqual(recovered, result) {
			t.Fatalf("%s lost applied result after re-enable: %#v %v", method, recovered, err)
		}
	}
	if _, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()}); err == nil || !strings.Contains(err.Error(), "transaction.model_required") {
		t.Fatalf("new plan bypassed current model readiness: %v", err)
	}
	if _, err := f.call("transaction.configure", map[string]any{"declaration": f.declaration}); err == nil || !strings.Contains(err.Error(), "transaction.model_required") {
		t.Fatalf("configuration bypassed current model readiness: %v", err)
	}
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("recovery wrote business facts")
	}
	f.input = changeInstalledModel(t, f.pb, f.workspace, f.input, func(model *pluginmodel.Declaration) {
		// Model entities always require read/query. Removing the old entities
		// entirely is the valid manifest operation that revokes their read grants.
		entity := model.Entities[0]
		entity.Key, entity.Label = "unrelated", "无关数据"
		entity.Fields = entity.Fields[:1]
		model.Entities = []pluginmodel.Entity{entity}
	})
	if _, err := f.call("transaction.status", map[string]any{"requestId": result.RequestID}); err == nil {
		t.Fatal("revoked read permissions leaked applied result")
	}
}

func TestTransactionFinalCardinalityConflictAtCommitRollsBack(t *testing.T) {
	f := newTransactionFixture(t, "one-to-one")
	seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
		{Entity: "headers", Kind: "create", TempID: "h1", Values: map[string]any{"code": "H1"}},
		{Entity: "headers", Kind: "create", TempID: "h2", Values: map[string]any{"code": "H2"}},
		{Entity: "lines", Kind: "create", TempID: "l1", Values: map[string]any{"code": "L1"}},
	}}
	created := f.submit(t, seed)
	digests := map[string]string{}
	for _, row := range created.Receipt.Rows {
		digests[row.RecordID] = row.Digest
	}
	h1, h2, l1 := created.NewIDs["headers"]["h1"], created.NewIDs["headers"]["h2"], created.NewIDs["lines"]["l1"]
	firstDigest, secondDigest := digests[h1], digests[h2]
	plan := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{{Entity: "headers", Kind: "update", RecordID: h1, ExpectedDigest: &firstDigest, Values: map[string]any{"peer": l1}}}}
	raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	preview := raw.(transactionPreview)
	plan.Operations[0].RecordID, plan.Operations[0].ExpectedDigest = h2, &secondDigest
	f.submit(t, plan)
	before := transactionBusinessSnapshot(t, f)
	if _, err = f.call("transaction.commit", map[string]any{"requestId": preview.RequestID}); err == nil || !strings.Contains(err.Error(), "reciprocal_cardinality") {
		t.Fatalf("commit did not reject final collision: %v", err)
	}
	if before != transactionBusinessSnapshot(t, f) {
		t.Fatal("failed commit changed rows, history, clocks or outboxes")
	}
}

func TestTransactionOneToOneSwapUsesFinalGraph(t *testing.T) {
	for _, explicitBoth := range []bool{false, true} {
		t.Run(map[bool]string{false: "derived peers", true: "explicit peers"}[explicitBoth], func(t *testing.T) {
			f := newTransactionFixture(t, "one-to-one")
			seed := transactionPlan{Contract: "vibetable.transaction-plan.v1", ModelID: "synthetic", Operations: []transactionOperation{
				{Entity: "headers", Kind: "create", TempID: "h1", Values: map[string]any{"code": "H1", "peer": map[string]any{"entity": "lines", "tempId": "l1"}}},
				{Entity: "headers", Kind: "create", TempID: "h2", Values: map[string]any{"code": "H2", "peer": map[string]any{"entity": "lines", "tempId": "l2"}}},
				{Entity: "lines", Kind: "create", TempID: "l1", Values: map[string]any{"code": "L1"}},
				{Entity: "lines", Kind: "create", TempID: "l2", Values: map[string]any{"code": "L2"}},
			}}
			created := f.submit(t, seed)
			h1, h2 := created.NewIDs["headers"]["h1"], created.NewIDs["headers"]["h2"]
			l1, l2 := created.NewIDs["lines"]["l1"], created.NewIDs["lines"]["l2"]
			plan := transactionPlan{Contract: seed.Contract, ModelID: seed.ModelID, Operations: []transactionOperation{{Entity: "headers", Kind: "update", RecordID: h1, Values: map[string]any{"peer": l2}}, {Entity: "headers", Kind: "update", RecordID: h2, Values: map[string]any{"peer": l1}}}}
			if explicitBoth {
				plan.Operations = append(plan.Operations, transactionOperation{Entity: "lines", Kind: "update", RecordID: l1, Values: map[string]any{"peer": h2}}, transactionOperation{Entity: "lines", Kind: "update", RecordID: l2, Values: map[string]any{"peer": h1}})
			}
			for i := range plan.Operations {
				for _, row := range created.Receipt.Rows {
					if row.RecordID == plan.Operations[i].RecordID {
						digest := row.Digest
						plan.Operations[i].ExpectedDigest = &digest
					}
				}
			}
			before := transactionBusinessSnapshot(t, f)
			raw, err := f.call("transaction.preview", map[string]any{"plan": plan})
			if err != nil {
				t.Fatalf("legal final one-to-one swap rejected: %v", err)
			}
			preview := raw.(transactionPreview)
			if len(preview.Effects) != 4 || before != transactionBusinessSnapshot(t, f) {
				t.Fatal("preview changed data or missed derived effects")
			}
			committed, err := f.call("transaction.commit", map[string]any{"requestId": preview.RequestID})
			if err != nil {
				t.Fatal(err)
			}
			for entity, pairs := range map[string]map[string]string{"headers": {h1: l2, h2: l1}, "lines": {l1: h2, l2: h1}} {
				binding := f.model.Bindings[entity]
				table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
				if err != nil {
					t.Fatal(err)
				}
				field, _ := table.Field(binding.Fields["peer"])
				for id, want := range pairs {
					row, err := f.pb.FindRecordById(table.PhysicalName, id)
					if err != nil || mutation.ProductRow(f.pb, table, row)[field.Identity.PhysicalName] != want {
						t.Fatalf("final pair %s->%s: %v", id, want, err)
					}
				}
			}
			// A genuine final collision is rejected without changing any business fact.
			plan.Operations = []transactionOperation{{Entity: "headers", Kind: "update", RecordID: h1, Values: map[string]any{"peer": l1}}}
			for _, row := range committed.(transactionResult).Receipt.Rows {
				if row.RecordID == h1 {
					digest := row.Digest
					plan.Operations[0].ExpectedDigest = &digest
				}
			}
			before = transactionBusinessSnapshot(t, f)
			if _, err = f.call("transaction.preview", map[string]any{"plan": plan}); err == nil || !strings.Contains(err.Error(), "reciprocal_cardinality") {
				t.Fatalf("final cardinality violation admitted: %v", err)
			}
			if before != transactionBusinessSnapshot(t, f) {
				t.Fatal("rejected final graph changed business facts")
			}
		})
	}
}

// Exercise the production HTTP router and serializer, not a forged Host error body.
func transactionHTTP(t *testing.T, f *transactionFixture, gate businessWriteGate) func(string, any) *httptest.ResponseRecorder {
	t.Helper()
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: req}}, nil
	})
	secret, err := auth.Parse(strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	bindVibetableSessionAuth(r, secret)
	registerPluginSurfaceAPIRoutes(r, f.pb, nil, f.kernel, f.workspace, f.input.SessionEpoch, gate, &pluginSurfaceIO{transactionEpoch: func() uint64 { return f.epoch }})
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return func(method string, params any) *httptest.ResponseRecorder {
		input := f.input
		input.Method = method
		input.Request, _ = json.Marshal(params)
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest(http.MethodPost, pluginSurfaceAPIPath, bytes.NewReader(raw))
		request.Header.Set("X-VibeTable-Session", strings.Repeat("2", 64))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
}

func TestTransactionHTTPCommitOutcomeUsesAuthorityProof(t *testing.T) {
	for _, scenario := range []string{"stale", "rule", "permission", "unknown", "imported proof", "wrong outer proof", "proof unavailable", "configure denied", "configure unknown", "configure imported proof"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTransactionFixture(t)
			if err := writecoordinator.EnsurePocketBaseReceiptTable(context.Background(), f.pb); err != nil {
				t.Fatal(err)
			}
			raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
			if err != nil {
				t.Fatal(err)
			}
			preview := raw.(transactionPreview)
			gate := businessWriteGate(nil)
			wantCode, wantOutcome := "transaction.plan_stale", "not-committed"
			method, params := "transaction.commit", any(map[string]any{"requestId": preview.RequestID, "previewRevision": 2})
			switch scenario {
			case "permission":
				_, err = pluginstore.New(f.pb, f.workspace).SetEnabled(context.Background(), f.input.ProjectKey, f.input.PluginID, false)
				if err != nil {
					t.Fatal(err)
				}
				wantCode = "plugin_read_denied"
			case "rule":
				// A concurrent transaction takes the unique business code after preview.
				f.submit(t, threeTablePlan())
				params = map[string]any{"requestId": preview.RequestID, "previewRevision": 1}
				wantCode = "transaction.unique_violation"
			case "unknown", "configure unknown":
				gate = func(context.Context, string, string, func(context.Context) error) error {
					return errors.New("synthetic authority failure")
				}
				params = map[string]any{"requestId": preview.RequestID, "previewRevision": 1}
				wantCode, wantOutcome = "plugin_commit_unknown", "unknown"
			case "imported proof":
				_, err = f.pb.DB().NewQuery(`INSERT INTO workspace_v2_imported_completions(workspace_id,kind,identity) VALUES ({:workspace},'plugin.transaction.commit',{:id})`).Bind(map[string]any{"workspace": f.workspace, "id": preview.RequestID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
				wantCode, wantOutcome = "plugin_commit_unknown", "unknown"
			case "wrong outer proof", "configure imported proof":
				kind := "plugin.transaction.commit"
				if scenario == "configure imported proof" {
					kind = "plugin.transaction.configure"
				}
				_, err = f.pb.DB().NewQuery(`INSERT INTO workspace_v2_imported_completions(workspace_id,kind,identity) VALUES ({:workspace},{:kind},{:id})`).Bind(map[string]any{"workspace": f.workspace, "kind": kind, "id": f.input.RequestID}).Execute()
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "configure imported proof" {
					wantCode, wantOutcome = "plugin_commit_unknown", "unknown"
				}
			case "proof unavailable":
				if _, err = f.pb.DB().NewQuery(`DROP TABLE workspace_v2_mutation_receipts`).Execute(); err != nil {
					t.Fatal(err)
				}
				wantCode, wantOutcome = "plugin_commit_unknown", "unknown"
			case "configure denied":
				wantCode = "transaction.configuration_denied"
			}
			if strings.HasPrefix(scenario, "configure") {
				method = "transaction.configure"
				params = map[string]any{"declaration": f.declaration}
				if scenario == "configure denied" || scenario == "configure imported proof" {
					params = map[string]any{"declaration": map[string]any{}}
				}
			}
			before := transactionBusinessSnapshot(t, f)
			response := transactionHTTP(t, f, gate)(method, params)
			var wire struct {
				Code    string `json:"code"`
				Outcome string `json:"commitOutcome"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &wire)
			t.Logf("actual HTTP %s: %d %s", method, response.Code, response.Body.String())
			if response.Code != http.StatusConflict || wire.Code != wantCode || wire.Outcome != wantOutcome {
				t.Fatalf("authority outcome mismatch: %s want %s/%s", response.Body.String(), wantCode, wantOutcome)
			}
			if before != transactionBusinessSnapshot(t, f) {
				t.Fatal("rejected request changed business facts")
			}
		})
	}
}

func TestTransactionHTTPCommittedAcknowledgementFailureCannotProveRollback(t *testing.T) {
	for _, method := range []string{"transaction.commit", "transaction.configure"} {
		t.Run(method, func(t *testing.T) {
			f := newTransactionFixture(t)
			if err := writecoordinator.EnsurePocketBaseReceiptTable(context.Background(), f.pb); err != nil {
				t.Fatal(err)
			}
			raw, err := f.call("transaction.preview", map[string]any{"plan": threeTablePlan()})
			if err != nil {
				t.Fatal(err)
			}
			preview := raw.(transactionPreview)
			gate := func(ctx context.Context, kind, id string, apply func(context.Context) error) error {
				intent := writecoordinator.WriteIntent{Token: writecoordinator.Token{WorkspaceID: f.workspace, SessionEpoch: 1, FenceEpoch: 1, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, MutationRevision: 1, AuditSourceEpoch: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
				bound, err := writecoordinator.WithBusinessIntent(ctx, intent, kind, id)
				if err != nil {
					return err
				}
				if err = apply(bound); err != nil {
					return err
				}
				// A current permission change prevents the commit's success recovery;
				// the durable PB receipt still proves it must never be called zero-write.
				if method == "transaction.commit" {
					if _, err = pluginstore.New(f.pb, f.workspace).SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, false); err != nil {
						return err
					}
				}
				return pluginDataError("transaction.plan_stale")
			}
			params := any(map[string]any{"requestId": preview.RequestID, "previewRevision": 1})
			if method == "transaction.configure" {
				params = map[string]any{"declaration": f.declaration}
			}
			response := transactionHTTP(t, f, gate)(method, params)
			var wire struct {
				Code    string `json:"code"`
				Outcome string `json:"commitOutcome"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &wire)
			t.Logf("post-commit HTTP %s: %d %s", method, response.Code, response.Body.String())
			if wire.Code != "plugin_commit_unknown" || wire.Outcome != "unknown" {
				t.Fatalf("committed acknowledgement failure called zero-write: %s", response.Body.String())
			}
			kind, id := "plugin."+method, f.input.RequestID
			if method == "transaction.commit" {
				id = preview.RequestID
			}
			if committed, err := writecoordinator.HasPocketBaseReceiptIdentity(context.Background(), f.pb, f.workspace, kind, id); err != nil || !committed {
				t.Fatalf("fixture did not really commit recovery proof: %v %v", committed, err)
			}
			if method == "transaction.commit" {
				for _, binding := range f.model.Bindings {
					table, err := schemaexecution.Describe(context.Background(), f.pb, binding.TableID)
					if err != nil {
						t.Fatal(err)
					}
					rows, err := f.pb.FindAllRecords(table.PhysicalName)
					if err != nil || len(rows) != 1 {
						t.Fatalf("actual committed rows missing: %d %v", len(rows), err)
					}
				}
			}
		})
	}
}
