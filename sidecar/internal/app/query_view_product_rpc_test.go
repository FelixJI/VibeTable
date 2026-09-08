package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

type queryViewProbe struct {
	calls  int
	table  string
	input  query.ViewQuery
	result query.ViewResult
	err    error
	cancel context.CancelFunc
}

func (p *queryViewProbe) ExecuteViewQuery(_ context.Context, table string, input query.ViewQuery) (query.ViewResult, error) {
	p.calls++
	p.table, p.input = table, input
	if p.cancel != nil {
		p.cancel()
	}
	return p.result, p.err
}

func TestQueryViewProductParameterBoundaries(t *testing.T) {
	registration := queryViewRegistration(nil)
	for _, raw := range []string{`{}`, `null`, `[]`, `{"tableId":"x"}`, `{"tableId":"","view":{}}`,
		`{"tableId":"x","view":null}`, `{"tableId":"x","view":{},"extra":false}`,
		`{"tableId":"\ud800","view":{}}`, "{\"tableId\":\"\xff\",\"view\":{}}"} {
		if registration.ValidateParams(json.RawMessage(raw)) == nil {
			t.Errorf("accepted invalid params %q", raw)
		}
	}
	for _, key := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		if registration.ValidateParams(json.RawMessage(`{"tableId":"x","view":{"nested":{"`+key+`":"x"}}}`)) == nil {
			t.Errorf("accepted credential %s", key)
		}
	}
	for _, depth := range []int{30, 31} {
		raw := `{"tableId":"x","view":{"nested":` + strings.Repeat("[", depth) + `0` + strings.Repeat("]", depth) + `}}`
		if err := registration.ValidateParams(json.RawMessage(raw)); (err == nil) != (depth == 30) {
			t.Errorf("depth %d: %v", depth, err)
		}
	}
	for _, raw := range []string{`{"tableId":" ","view":{}}`, `{"tableId":"x","view":{"extra":true}}`, `{"tableId":"x","view":{"query":{"limit":0}}}`} {
		probe := &queryViewProbe{}
		_, err := queryViewRegistration(probe).Handler(context.Background(), json.RawMessage(raw))
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "query.request.invalid" || probe.calls != 0 {
			t.Errorf("REST rejection %s: %v, calls %d", raw, err, probe.calls)
		}
	}
}

func TestQueryViewProductKeepsBothJSONBudgets(t *testing.T) {
	prefix, suffix := `{"tableId":"x","view":{"query":{"keyword":"`, `"}}}`
	for _, extra := range []int{-32, 0, 1} {
		raw := json.RawMessage(prefix + strings.Repeat("x", maxQueryRequestBytes-len(prefix)-len(suffix)+extra) + suffix)
		probe := &queryViewProbe{}
		registration := queryViewRegistration(probe)
		err := registration.ValidateParams(raw)
		if (err == nil) != (extra <= 0) {
			t.Fatalf("params budget %d: %v", extra, err)
		}
		if extra > 0 {
			continue
		}
		_, err = registration.Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if extra == 0 && (!errors.As(err, &public) || public.Code != "query.request.invalid" || probe.calls != 0) {
			t.Fatalf("REST body budget: %v calls %d", err, probe.calls)
		}
		if extra < 0 && probe.calls != 1 {
			t.Fatalf("below both budgets did not call Port: %v", err)
		}
	}
	raw := json.RawMessage(`{"tableId":"x","view":{"query":{"keyword":"` + strings.Repeat(`\u2028`, 180000) + `"}}}`)
	if err := queryViewRegistration(nil).ValidateParams(raw); err != nil {
		t.Fatalf("compact UTF-8 budget counted escaped wire instead: %v", err)
	}
}

func TestQueryViewProductCancellationAndPublicErrors(t *testing.T) {
	raw := json.RawMessage(`{"tableId":"orders","view":{}}`)
	probe := &queryViewProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queryViewRegistration(probe).Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 0 {
		t.Fatalf("early cancellation %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	probe.cancel = cancel
	if _, err := queryViewRegistration(probe).Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 1 {
		t.Fatalf("late cancellation %v", err)
	}
	probe.cancel = nil
	for _, source := range []error{context.DeadlineExceeded, errors.New("private sqlite failure"), &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Retryable: true}} {
		probe.err = source
		_, err := queryViewRegistration(probe).Handler(context.Background(), raw)
		if errors.Is(source, context.DeadlineExceeded) {
			if !errors.Is(err, source) {
				t.Fatal(err)
			}
			continue
		}
		var public *productrpc.PublicError
		if !errors.As(err, &public) {
			t.Fatalf("not public: %v", err)
		}
		if _, ok := source.(*query.ProductError); ok {
			if public.Code != "query.snapshot_stale" || !public.Retryable {
				t.Fatal(public)
			}
		} else if public.Code != "query.internal.failed" || public.Message != "query operation failed" {
			t.Fatal(public)
		}
	}
}
