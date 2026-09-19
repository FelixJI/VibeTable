package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

type relationWriteOraclePort struct {
	t      *testing.T
	bodies []json.RawMessage
	status int
	calls  []map[string]any
}

func (p *relationWriteOraclePort) call(method, path string, body any, out any) error {
	p.t.Helper()
	p.calls = append(p.calls, map[string]any{"method": method, "path": path, "body": body, "query": map[string]any{}})
	if p.status != 200 {
		return relationRequestError("wrong target")
	}
	if len(p.bodies) == 0 {
		p.t.Fatal("unexpected authority call")
	}
	raw := p.bodies[0]
	p.bodies = p.bodies[1:]
	return json.Unmarshal(raw, out)
}
func (p *relationWriteOraclePort) Describe(ctx context.Context, table string) (relation.CatalogResult, error) {
	var out relation.CatalogResult
	err := p.call("GET", "/api/vibetable/v1/relations/describe", nil, &out)
	p.calls[len(p.calls)-1]["query"] = map[string]any{"tableId": table}
	return out, err
}
func (p *relationWriteOraclePort) ReadRows(ctx context.Context, table string, ids []string) ([]map[string]any, error) {
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	err := p.call("POST", "/api/vibetable/v1/query", map[string]any{"operation": "readRows", "tableId": table, "rowIds": ids}, &out)
	return out.Rows, err
}
func (p *relationWriteOraclePort) CreateTarget(ctx context.Context, input relation.CreateTargetRequest) (relation.CreateTargetResult, error) {
	var out relation.CreateTargetResult
	err := p.call("POST", "/api/vibetable/v1/relations/create-target", map[string]any{"relationId": input.RelationID, "label": input.Label, "values": input.Values, "requestId": input.RequestID, "idempotencyKey": input.IdempotencyKey, "actor": input.Actor}, &out)
	return out, err
}
func (p *relationWriteOraclePort) ApplyDelta(ctx context.Context, input relation.DeltaRequest) (relation.DeltaResult, error) {
	var out relation.DeltaResult
	err := p.call("POST", "/api/vibetable/v1/relations/apply-delta", input, &out)
	return out, err
}

func TestRelationWriteOriginalPythonOracle(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/relation-write-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Producer string `json:"producerCommit"`
		Cases    []struct {
			Name    string `json:"name"`
			Request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			} `json:"request"`
			Fixture  []json.RawMessage `json:"authorityFixture"`
			Status   int               `json:"authorityStatus"`
			Requests []json.RawMessage `json:"authorityRequests"`
			Response struct {
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			} `json:"response"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.Producer != "55e0bfa41bbbf6d2f4129227d29a085e6c599a87" || len(oracle.Cases) != 63 {
		t.Fatal("unexpected frozen producer")
	}
	for _, sample := range oracle.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			port := &relationWriteOraclePort{t: t, bodies: sample.Fixture, status: sample.Status}
			var registration productrpc.Registration
			for _, reg := range relationWriteRegistrations(port, port) {
				if reg.Method == sample.Request.Method {
					registration = reg
				}
			}
			if sample.Response.Error != nil && sample.Response.Error.Code == -32600 {
				if json.Valid(sample.Request.Params) && len(sample.Request.Params) > 0 && sample.Request.Params[0] == '[' {
					return
				}
				t.Fatal("unexpected envelope fixture")
			}
			if err := registration.ValidateParams(sample.Request.Params); err != nil {
				if sample.Response.Error == nil || sample.Response.Error.Code != -32602 {
					t.Fatalf("unexpected params rejection: %v", err)
				}
				if len(port.calls) != 0 {
					t.Fatal("invalid params reached authority")
				}
				return
			}
			result, err := registration.Handler(context.Background(), sample.Request.Params)
			if sample.Response.Error != nil {
				if err == nil {
					t.Fatal("expected error")
				}
				var public *productrpc.PublicError
				publicError := errors.As(err, &public)
				if (sample.Response.Error.Code == -32150) != publicError {
					t.Fatalf("wrong error boundary: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				got, want := viewWireJSON(t, encoded), viewWireJSON(t, sample.Response.Result)
				// The scripted Python receipt contains only two fields. Typed Go receipt
				// always emits its declared fields; real producer tests cover that shape.
				if receipt, ok := want["receipt"].(map[string]any); ok {
					actual, ok := got["receipt"].(map[string]any)
					if !ok {
						t.Fatal("missing receipt")
					}
					for key, value := range receipt {
						if !reflect.DeepEqual(actual[key], value) {
							t.Fatalf("receipt %s differs", key)
						}
					}
					delete(got, "receipt")
					delete(want, "receipt")
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("result got=%#v want=%#v", got, want)
				}
			}
			if len(port.calls) != len(sample.Requests) {
				t.Fatalf("calls %d want %d", len(port.calls), len(sample.Requests))
			}
			for i, call := range port.calls {
				gotRaw, _ := json.Marshal(call)
				got := viewWireJSON(t, gotRaw)
				want := viewWireJSON(t, sample.Requests[i])
				// Typed domain requests omit nullable optional fields and actor displayName.

				for _, object := range []map[string]any{got, want} {
					if body, ok := object["body"].(map[string]any); ok {
						delete(body, "expectedDigest")
						if actor, ok := body["actor"].(map[string]any); ok {
							delete(actor, "displayName")
						}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("call got=%#v want=%#v", got, want)
				}
			}
		})
	}
}
