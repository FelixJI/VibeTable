package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

func unrelatedRelationWriteRegistration(t *testing.T, method string) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
		t.Helper()
		t.Fatalf("unrelated fixture invoked %s", method)
		return nil, nil
	}}
}

type relationWriteProbe struct {
	calls   int
	create  relation.CreateTargetRequest
	delta   relation.DeltaRequest
	single  relation.SingleRequest
	receipt mutation.Receipt
	err     error
}

func (p *relationWriteProbe) CreateTarget(_ context.Context, r relation.CreateTargetRequest) (relation.CreateTargetResult, error) {
	p.calls++
	p.create = r
	return relation.CreateTargetResult{Target: relation.TargetRef{TableID: "targets", RecordID: "target-1", Label: "中文"}, Receipt: p.receipt}, p.err
}
func (p *relationWriteProbe) ApplyDelta(_ context.Context, r relation.DeltaRequest) (relation.DeltaResult, error) {
	p.calls++
	p.delta = r
	return relation.DeltaResult{Current: []relation.TargetRef{{TableID: "targets", RecordID: "target-1", Label: "中文"}}, Receipt: p.receipt}, p.err
}
func (p *relationWriteProbe) UpdateSingle(_ context.Context, r relation.SingleRequest) (mutation.Receipt, error) {
	p.calls++
	p.single = r
	return p.receipt, p.err
}

func TestRelationWriteProductTranslationAndClosedRoots(t *testing.T) {
	p := &relationWriteProbe{receipt: mutation.Receipt{ContractVersion: mutation.ContractVersion, Status: mutation.StatusApplied}}
	create := relationCreateTargetRegistration(p)
	raw := json.RawMessage(`{"relationId":"orders.related","values":{"name":"中文"},"idempotencyKey":"create-1"}`)
	if err := create.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	result, err := create.Handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.create.RequestID != "create-1" || p.create.IdempotencyKey != "create-1" || p.create.Actor.ID != "local-user" || p.create.Values["name"] != "中文" {
		t.Fatal(p.create)
	}
	want := map[string]any{"outcome": "committed", "requestId": "create-1", "target": map[string]any{"collection": "targets", "itemId": "target-1", "label": "中文", "secondaryLabel": nil}}
	if !reflect.DeepEqual(result, want) {
		t.Fatal(result)
	}
	single := relationUpdateSingleRegistration(p)
	raw = json.RawMessage(`{"relationId":"orders.related","sourceItemId":"source-1","target":{"target":{"collection":"targets","itemId":"target-1"},"extra":"echo"},"expectedSchemaRevision":"schema-1","expectedDateUpdated":"ignored-by-existing-contract","idempotencyKey":"single-1"}`)
	if err = single.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	result, err = single.Handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.single.Target == nil || p.single.Target.TableID != "targets" || p.single.Target.Label != "target-1" || p.single.ExpectedDigest != nil {
		t.Fatal(p.single)
	}
	if current := result.(map[string]any)["current"].(map[string]any); current["extra"] != "echo" {
		t.Fatal(result)
	}
	clear := json.RawMessage(`{"relationId":"orders.related","sourceItemId":"source-1","target":null,"expectedSchemaRevision":"schema-1","idempotencyKey":"clear-1"}`)
	result, err = single.Handler(context.Background(), clear)
	if err != nil || result.(map[string]any)["current"] != nil || p.single.Target != nil {
		t.Fatalf("%v %v", result, err)
	}
	delta := relationApplyDeltaRegistration(p)
	result, err = delta.Handler(context.Background(), json.RawMessage(relationPreviewParams))
	if err != nil {
		t.Fatal(err)
	}
	if p.delta.ExpectedDigest != nil || p.delta.RequestID != "preview-1" || result.(map[string]any)["outcome"] != "committed" {
		t.Fatal(result, p.delta)
	}
	for _, sample := range []struct {
		reg productrpc.Registration
		raw string
	}{
		{create, `{"relationId":"x","idempotencyKey":"k","label":null}`},
		{create, `{"relationId":"x","idempotencyKey":"k","values":[]}`},
		{single, `{"relationId":"x","sourceItemId":"r","expectedSchemaRevision":"s","idempotencyKey":"k"}`},
		{single, `{"relationId":"x","sourceItemId":"r","target":null,"expectedSchemaRevision":"s","expectedDateUpdated":1,"idempotencyKey":"k"}`},
		{delta, `{"relationId":"x","sourceItemId":"r","expectedSchemaRevision":"s","adds":[],"removes":[],"idempotencyKey":"k","updates":[]}`},
	} {
		if err = sample.reg.ValidateParams(json.RawMessage(sample.raw)); err == nil {
			t.Fatalf("accepted %s", sample.raw)
		}
	}
}

func TestRelationWriteProductFenceCancellationAndPending(t *testing.T) {
	for _, method := range []string{"create", "single", "delta"} {
		t.Run(method, func(t *testing.T) {
			p := &relationWriteProbe{receipt: mutation.Receipt{Status: mutation.StatusApplied}}
			denied := errors.New("fence denied")
			gate := businessWriteGate(func(context.Context, string, string, func(context.Context) error) error { return denied })
			var reg productrpc.Registration
			var raw json.RawMessage
			switch method {
			case "create":
				reg = relationCreateTargetRegistration(p, gate)
				raw = json.RawMessage(`{"relationId":"x","label":"new","idempotencyKey":"k"}`)
			case "single":
				reg = relationUpdateSingleRegistration(p, gate)
				raw = json.RawMessage(`{"relationId":"x","sourceItemId":"r","target":null,"expectedSchemaRevision":"s","idempotencyKey":"k"}`)
			case "delta":
				reg = relationApplyDeltaRegistration(p, gate)
				raw = json.RawMessage(relationPreviewParams)
			}
			if _, err := reg.Handler(context.Background(), raw); err == nil || p.calls != 0 {
				t.Fatalf("fence calls=%d err=%v", p.calls, err)
			}
			switch method {
			case "create":
				reg = relationCreateTargetRegistration(p)
			case "single":
				reg = relationUpdateSingleRegistration(p)
			case "delta":
				reg = relationApplyDeltaRegistration(p)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := reg.Handler(ctx, raw); !errors.Is(err, context.Canceled) || p.calls != 0 {
				t.Fatalf("cancel calls=%d err=%v", p.calls, err)
			}
			p.receipt.Status = mutation.StatusPending
			if _, err := reg.Handler(context.Background(), raw); err == nil {
				t.Fatal("pending reported committed")
			}
		})
	}
}
