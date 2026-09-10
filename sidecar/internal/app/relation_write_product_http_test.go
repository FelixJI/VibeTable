package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestRelationWriteProductHTTPRealWritesAndReplays(t *testing.T) {
	for _, kind := range []string{"many", "one"} {
		t.Run(kind, func(t *testing.T) {
			f := newRelationWriteFixture(t, kind)
			mux := relationPreviewHTTPMux(t, f.pb, relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
				relationCreateTargetRegistration(f.service), relationApplyDeltaRegistration(f.service), relationUpdateSingleRegistration(f.service))
			create := map[string]any{"relationId": f.relationID, "label": "新目标", "idempotencyKey": "http-create"}
			first := schemaProductRequestForMethod(t, mux, context.Background(), "relation.createTarget", string(previewJSON(t, create)), schemaListWire)
			if first.Error != nil {
				t.Fatal(first.Error)
			}
			before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
			again := schemaProductRequestForMethod(t, mux, context.Background(), "relation.createTarget", string(previewJSON(t, create)), schemaListWire)
			if again.Error != nil || !reflect.DeepEqual(viewWireJSON(t, first.Result), viewWireJSON(t, again.Result)) {
				t.Fatalf("create replay=%+v", again)
			}
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("HTTP create replay wrote authority")
			}
			params := map[string]any{"relationId": f.relationID, "sourceItemId": "writesource0001", "expectedSchemaRevision": f.table.Snapshot.SchemaRevision, "idempotencyKey": "http-write"}
			method := "relation.applyDelta"
			if kind == "many" {
				params["adds"] = []any{map[string]any{"collection": f.table.Snapshot.TableID, "itemId": "writetarget0001"}}
				params["removes"] = []any{}
			} else {
				method = "relation.updateSingle"
				params["target"] = map[string]any{"collection": f.table.Snapshot.TableID, "itemId": "writetarget0001"}
			}
			first = schemaProductRequestForMethod(t, mux, context.Background(), method, string(previewJSON(t, params)), schemaListWire)
			if first.Error != nil || viewWireJSON(t, first.Result)["outcome"] != "committed" {
				t.Fatal(first)
			}
			before = previewAuthorityState(t, f.pb, f.table.PhysicalName)
			again = schemaProductRequestForMethod(t, mux, context.Background(), method, string(previewJSON(t, params)), schemaListWire)
			if again.Error != nil || viewWireJSON(t, again.Result)["outcome"] != "committed" {
				t.Fatal(again)
			}
			if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
				t.Fatal("HTTP relation replay wrote authority")
			}
		})
	}
}

func TestRelationWriteReplaySignalsExactBusinessIntent(t *testing.T) {
	f := newRelationWriteFixture(t, "many")
	request := f.delta("business-replay")
	if err := writecoordinator.EnsurePocketBaseReceiptTable(context.Background(), f.pb); err != nil {
		t.Fatal(err)
	}
	intent := writecoordinator.WriteIntent{Token: writecoordinator.Token{WorkspaceID: "relation-workspace", SessionEpoch: 1, FenceEpoch: 1, ClaimID: "claim-1"}, MutationRevision: 1, AuditSourceEpoch: "relation-test"}
	firstCtx, err := writecoordinator.WithBusinessIntent(context.Background(), intent, "relation.apply-delta", request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyDelta(firstCtx, request); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := f.pb.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
	before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
	intent.MutationRevision = 2
	ctx, err := writecoordinator.WithBusinessIntent(context.Background(), intent, "relation.apply-delta", request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ApplyDelta(ctx, request)
	if err != writecoordinator.ErrBusinessReplay || result.Receipt.Status != mutation.StatusReplayed || result.Current == nil {
		t.Fatalf("replay result=%+v signal=%v", result, err)
	}
	if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
		t.Fatal("business replay wrote authority")
	}
	if err := f.pb.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&receipts); err != nil || receipts != 1 {
		t.Fatalf("replay receipts=%d err=%v", receipts, err)
	}
}

func TestRelationWriteProductHTTPBindsCompleteCallerIntent(t *testing.T) {
	for _, kind := range []string{"one", "many"} {
		for _, changedField := range []string{"secondaryLabel", "extra", "expectedDateUpdated"} {
			t.Run(kind+"/"+changedField, func(t *testing.T) {
				f := newRelationWriteFixture(t, kind)
				mux := relationPreviewHTTPMux(t, f.pb, relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
					relationCreateTargetRegistration(f.service), relationApplyDeltaRegistration(f.service), relationUpdateSingleRegistration(f.service))
				target := map[string]any{"collection": f.table.Snapshot.TableID, "itemId": "writetarget0001", "label": "A", "secondaryLabel": "original"}
				params := map[string]any{"relationId": f.relationID, "sourceItemId": "writesource0001", "expectedSchemaRevision": f.table.Snapshot.SchemaRevision, "expectedDateUpdated": "opaque-original", "idempotencyKey": "complete-intent"}
				method := "relation.updateSingle"
				if kind == "one" {
					params["target"] = target
				} else {
					method = "relation.applyDelta"
					params["adds"] = []any{target}
					params["removes"] = []any{}
				}
				original := schemaProductRequestForMethod(t, mux, context.Background(), method, string(previewJSON(t, params)), schemaListWire)
				if original.Error != nil {
					t.Fatal(original.Error)
				}
				before := previewAuthorityState(t, f.pb, f.table.PhysicalName)
				replayed := schemaProductRequestForMethod(t, mux, context.Background(), method, string(previewJSON(t, params)), schemaListWire)
				if replayed.Error != nil || !reflect.DeepEqual(viewWireJSON(t, original.Result)["current"], viewWireJSON(t, replayed.Result)["current"]) {
					t.Fatalf("same intent did not replay original current: %+v", replayed)
				}
				if changedField != "expectedDateUpdated" {
					target[changedField] = "changed"
				} else {
					params[changedField] = "opaque-changed"
				}
				rejected := schemaProductRequestForMethod(t, mux, context.Background(), method, string(previewJSON(t, params)), schemaListWire)
				if rejected.Error == nil || rejected.Error.Code != -32150 || rejected.Error.Data == nil || viewWireJSON(t, previewJSON(t, rejected.Error.Data))["code"] != "mutation.idempotency_conflict" {
					t.Fatalf("changed %s reused committed intent: %+v", changedField, rejected)
				}
				if !reflect.DeepEqual(before, previewAuthorityState(t, f.pb, f.table.PhysicalName)) {
					t.Fatal("same intent replay or changed intent rejection wrote authority")
				}
			})
		}
	}
}

// The frozen corpus records both the old handler bypass and the real closed
// dispatcher. Only dispatcher steps are a public input/response oracle.
func TestRelationWriteProductHTTPFrozenDispatcher(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "relation-write-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string `json:"producer"`
		Cases    []struct {
			Name       string `json:"name"`
			Dispatcher struct {
				Steps []struct {
					Method   string          `json:"method"`
					Params   json.RawMessage `json:"params"`
					Response struct {
						Result json.RawMessage `json:"result"`
						Error  json.RawMessage `json:"error"`
					} `json:"response"`
				} `json:"steps"`
			} `json:"dispatcher"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "e90889c1cb2c10b3f3a7c69e6f8de46dbccbeef2" || len(corpus.Cases) != 8 {
		t.Fatal("unexpected frozen oracle")
	}
	p := &relationFrozenProbe{}
	mux := relationPreviewHTTPMux(t, schemaProductStore(t), relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}), relationCreateTargetRegistration(p), relationApplyDeltaRegistration(p), relationUpdateSingleRegistration(p))
	checked := 0
	for _, sample := range corpus.Cases {
		for _, step := range sample.Dispatcher.Steps {
			if !strings.HasPrefix(step.Method, "relation.") {
				continue
			}
			t.Run(sample.Name+"/"+step.Method, func(t *testing.T) {
				if sample.Name == "create-transport-error" {
					t.Skip("in-process Go relation port has no retired Python HTTP transport boundary")
				}
				checked++
				p.result = nil
				p.err = nil
				if len(step.Response.Result) != 0 {
					var result map[string]any
					if err = json.Unmarshal(step.Response.Result, &result); err != nil {
						t.Fatal(err)
					}
					p.result = result
				}
				if sample.Name == "apply-public-error" {
					path := "expectedDigest"
					p.err = &mutation.ProductError{Code: "mutation.digest_conflict", Message: "record changed", Path: &path, Details: map[string]any{"expected": "old", "actual": "new"}}
				}
				response := schemaProductRequestForMethod(t, mux, context.Background(), step.Method, string(step.Params), schemaListWire)
				if len(step.Response.Error) != 0 {
					if response.Error == nil || !reflect.DeepEqual(viewWireJSON(t, previewJSON(t, response.Error)), viewWireJSON(t, step.Response.Error)) {
						t.Fatalf("got %+v want error %s", response, step.Response.Error)
					}
				} else if response.Error != nil || !reflect.DeepEqual(viewWireJSON(t, response.Result), viewWireJSON(t, step.Response.Result)) {
					t.Fatalf("got %+v want %s", response, step.Response.Result)
				}
			})
		}
	}
	if checked != 8 {
		t.Fatalf("checked %d frozen relation steps", checked)
	}
}

type relationFrozenProbe struct {
	result map[string]any
	err    error
}

func (p *relationFrozenProbe) CreateTarget(context.Context, relation.CreateTargetRequest) (relation.CreateTargetResult, error) {
	result := relation.CreateTargetResult{Receipt: mutation.Receipt{Status: mutation.StatusApplied}}
	if p.result != nil {
		target := p.result["target"].(map[string]any)
		result.Target = relation.TargetRef{TableID: target["collection"].(string), RecordID: target["itemId"].(string), Label: target["label"].(string)}
	}
	return result, p.err
}
func (p *relationFrozenProbe) ApplyDelta(context.Context, relation.DeltaRequest) (relation.DeltaResult, error) {
	return relation.DeltaResult{}, p.err
}
func (p *relationFrozenProbe) UpdateSingle(context.Context, relation.SingleRequest) (mutation.Receipt, error) {
	return mutation.Receipt{}, p.err
}
