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

type querySelectionProbe struct {
	calls  int
	table  string
	input  query.TableQuery
	result query.SelectionProjection
	err    error
	cancel context.CancelFunc
}

func (p *querySelectionProbe) OpenSelectionProjection(_ context.Context, table string, input query.TableQuery) (query.SelectionProjection, error) {
	p.calls++
	p.table, p.input = table, input
	if p.cancel != nil {
		p.cancel()
	}
	return p.result, p.err
}

func TestQuerySelectionProductKeepsClosedParamsAndRESTBoundary(t *testing.T) {
	registration := querySelectionOpenRegistration(nil)
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"tableId":"x"}`, `{"tableId":"","query":{}}`,
		`{"tableId":false,"query":{}}`, `{"tableId":"x","query":null}`,
		`{"tableId":"x","query":[],"extra":1}`, `{"tableId":"\ud800","query":{}}`,
		"{\"tableId\":\"\\xff\",\"query\":{}}",
	} {
		if err := registration.ValidateParams(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid params %q", raw)
		}
	}
	for _, credential := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		raw := json.RawMessage(`{"tableId":"x","query":{"value":{"` + credential + `":"x"}}}`)
		if err := registration.ValidateParams(raw); err == nil {
			t.Errorf("accepted credential %q", credential)
		}
	}
	for _, depth := range []int{30, 31} {
		raw := json.RawMessage(`{"tableId":"x","query":{"value":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + `}}`)
		if err := registration.ValidateParams(raw); (err == nil) != (depth == 30) {
			t.Errorf("depth %d: %v", depth, err)
		}
	}
	for _, raw := range []string{
		`{"tableId":"x","query":{"limit":0}}`,
		`{"tableId":"x","query":{"unknown":true}}`, `{"tableId":"x","query":{"filters":null}}`,
	} {
		probe := &querySelectionProbe{}
		result, err := querySelectionOpenRegistration(probe).Handler(context.Background(), json.RawMessage(raw))
		var public *productrpc.PublicError
		if result != nil || !errors.As(err, &public) || public.Code != "query.request.invalid" || probe.calls != 0 {
			t.Errorf("REST rejection %s: result=%#v err=%v calls=%d", raw, result, err, probe.calls)
		}
	}
}

func TestQuerySelectionProductPreservesCancellationAndInternalFailures(t *testing.T) {
	probe := &querySelectionProbe{}
	registration := querySelectionOpenRegistration(probe)
	raw := json.RawMessage(`{"tableId":"orders","query":{}}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 0 {
		t.Fatalf("early cancellation: %v, calls=%d", err, probe.calls)
	}
	probe.err = &query.ProductError{Code: "query.cursor_stale", Path: "cursor", Message: "Cursor is stale", Details: map[string]any{"expected": "data_0"}, Retryable: true}
	_, err := registration.Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "query.cursor_stale" || public.Path == nil || *public.Path != "cursor" || !public.Retryable {
		t.Fatalf("public product error: %v", err)
	}
	probe.err = errors.New("private database failure")
	_, err = registration.Handler(context.Background(), raw)
	if !errors.As(err, &public) || public.Code != "query.internal.failed" || public.Message != "query operation failed" {
		t.Fatalf("internal error: %v", err)
	}
	probe.err = context.DeadlineExceeded
	if _, err := registration.Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}

func TestQuerySelectionProductRejectsMalformedAuthorityProjection(t *testing.T) {
	next := "next"
	for _, projection := range []query.SelectionProjection{
		{},
		{CursorWindow: query.CursorWindow{Rows: []map[string]any{nil}}},
		{CursorWindow: query.CursorWindow{Rows: []map[string]any{}, HasMore: true}},
		{CursorWindow: query.CursorWindow{Rows: []map[string]any{}, NextCursor: &next}},
		{SchemaSnapshot: query.SelectionProjection{}.SchemaSnapshot, CursorWindow: query.CursorWindow{Rows: []map[string]any{}, Snapshot: query.QuerySnapshot{Table: "other"}}},
	} {
		probe := &querySelectionProbe{result: projection}
		_, err := querySelectionOpenRegistration(probe).Handler(context.Background(), json.RawMessage(`{"tableId":"orders","query":{}}`))
		var public *productrpc.PublicError
		if err == nil || errors.As(err, &public) {
			t.Fatalf("malformed authority projection accepted or public: %#v, %v", projection, err)
		}
	}
}
