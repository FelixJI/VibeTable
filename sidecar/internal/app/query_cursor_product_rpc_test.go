package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
)

type unrelatedQueryCursorMustNotRun struct{ t *testing.T }

func (probe unrelatedQueryCursorMustNotRun) OpenCursor(context.Context, string, query.TableQuery) (query.CursorWindow, error) {
	probe.t.Helper()
	probe.t.Fatal("unrelated Product fixture unexpectedly invoked query.cursorOpen")
	return query.CursorWindow{}, errors.New("unexpected query.cursorOpen invocation")
}

func (probe unrelatedQueryCursorMustNotRun) FetchCursor(context.Context, string) (query.CursorWindow, error) {
	probe.t.Helper()
	probe.t.Fatal("unrelated Product fixture unexpectedly invoked query.cursorFetch")
	return query.CursorWindow{}, errors.New("unexpected query.cursorFetch invocation")
}

type queryCursorProbe struct {
	opens, fetches int
	table, cursor  string
	input          query.TableQuery
	window         query.CursorWindow
	err            error
	cancel         context.CancelFunc
}

func (p *queryCursorProbe) OpenCursor(_ context.Context, table string, input query.TableQuery) (query.CursorWindow, error) {
	p.opens++
	p.table, p.input = table, input
	if p.cancel != nil {
		p.cancel()
	}
	return p.window, p.err
}

func (p *queryCursorProbe) FetchCursor(_ context.Context, cursor string) (query.CursorWindow, error) {
	p.fetches++
	p.cursor = cursor
	if p.cancel != nil {
		p.cancel()
	}
	return p.window, p.err
}

func TestQueryCursorProductPreservesTypedInputAndWindowProjection(t *testing.T) {
	next := ""
	probe := &queryCursorProbe{window: query.CursorWindow{
		Rows:       []map[string]any{{"id": "r1", "text": "中文 Cafe\u0301 👩🏽‍💻", "false": false, "zero": 0, "null": nil}},
		NextCursor: &next, HasMore: true, FilteredRows: 1, TotalRows: 7,
		Snapshot: query.QuerySnapshot{SchemaRevision: "schema_1", DataRevision: 0},
	}}
	open := queryCursorOpenRegistration(probe)
	raw := json.RawMessage(`{"tableId":"中文/表","query":{"keyword":"\ud83d\ude00","limit":5,"filters":[{"field":"x","value":-0.0},{"field":"y","value":false}],"sorts":[{"field":"x"}]}}`)
	if err := open.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	result, err := open.Handler(context.Background(), raw)
	if err != nil || !reflect.DeepEqual(result, probe.window) {
		t.Fatalf("window: %#v %v", result, err)
	}
	if probe.opens != 1 || probe.fetches != 0 || probe.table != "中文/表" || probe.input.Keyword != "😀" || probe.input.Offset != 0 || probe.input.Limit != 5 || probe.input.Filters[0].Value != json.Number("-0.0") || probe.input.Filters[1].Value != false || probe.input.Sorts[0].NullsLast == nil || !*probe.input.Sorts[0].NullsLast {
		t.Fatalf("typed open: %+v", probe)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(wire, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 6 || object["querySnapshot"] == nil || object["snapshot"] != nil || string(object["nextCursor"]) != `""` || string(object["hasMore"]) != "true" {
		t.Fatalf("projection: %s", wire)
	}
	if _, err := open.Handler(context.Background(), json.RawMessage(`{"tableId":"orders","query":{}}`)); err != nil {
		t.Fatal(err)
	}
	if probe.input.Limit != 100 || probe.input.Filters == nil || probe.input.Sorts == nil {
		t.Fatalf("omitted defaults: %+v", probe.input)
	}
	probe.window = query.CursorWindow{Rows: []map[string]any{}}
	result, err = queryCursorFetchRegistration(probe).Handler(context.Background(), json.RawMessage(`{"cursor":"游标 /+"}`))
	if err != nil || !reflect.DeepEqual(result, probe.window) || probe.fetches != 1 || probe.cursor != "游标 /+" {
		t.Fatalf("terminal fetch: %#v %+v %v", result, probe, err)
	}
}

func TestQueryCursorProductKeepsProductAndRESTRejectionBoundaries(t *testing.T) {
	for _, sample := range []struct {
		registration productrpc.Registration
		invalid      []string
	}{
		{queryCursorOpenRegistration(nil), []string{`{}`, `null`, `[]`, `{"tableId":"x"}`, `{"tableId":"","query":{}}`, `{"tableId":false,"query":{}}`, `{"tableId":"x","query":null}`, `{"tableId":"x","query":[]}`, `{"tableId":"x","query":{},"cursor":"x"}`, `{"tableId":"\ud800","query":{}}`, "{\"tableId\":\"\xff\",\"query\":{}}"}},
		{queryCursorFetchRegistration(nil), []string{`{}`, `null`, `[]`, `{"cursor":""}`, `{"cursor":null}`, `{"cursor":false}`, `{"cursor":"x","limit":1}`, `{"cursor":"\udc00"}`, `{"cursor":"x"} {}`}},
	} {
		for _, raw := range sample.invalid {
			if err := sample.registration.ValidateParams(json.RawMessage(raw)); err == nil {
				t.Errorf("%s accepted %q", sample.registration.Method, raw)
			}
		}
	}
	for _, credential := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		raw := json.RawMessage(`{"tableId":"x","query":{"filters":[{"value":{"` + credential + `":"x"}}]}}`)
		if queryCursorOpenRegistration(nil).ValidateParams(raw) == nil {
			t.Fatalf("accepted credential key %s", credential)
		}
	}
	for _, depth := range []int{30, 31} {
		raw := json.RawMessage(`{"tableId":"x","query":{"value":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + `}}`)
		if err := queryCursorOpenRegistration(nil).ValidateParams(raw); (err == nil) != (depth == 30) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	for _, input := range []string{`{"limit":0}`, `{"limit":501}`, `{"offset":1.0}`, `{"offset":false}`, `{"offset":null}`, `{"unknown":0}`, `{"filters":null}`, `{"sorts":[{"field":"x","nullsLast":null}]}`} {
		probe := &queryCursorProbe{}
		registration := queryCursorOpenRegistration(probe)
		raw := json.RawMessage(`{"tableId":"x","query":` + input + `}`)
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("moved REST rejection to Product params: %v", err)
		}
		_, err := registration.Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Message != "query request body is invalid" || probe.opens != 0 {
			t.Fatalf("REST boundary %s: %v", input, err)
		}
	}
	for _, sample := range []struct {
		registration productrpc.Registration
		raw, path    string
	}{
		{queryCursorOpenRegistration(nil), `{"tableId":" \t","query":{}}`, "tableId"},
		{queryCursorFetchRegistration(nil), `{"cursor":" \t"}`, "operation"},
	} {
		if err := sample.registration.ValidateParams(json.RawMessage(sample.raw)); err != nil {
			t.Fatal(err)
		}
		_, err := sample.registration.Handler(context.Background(), json.RawMessage(sample.raw))
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Path == nil || *public.Path != sample.path {
			t.Fatalf("blank text: %v", err)
		}
	}
}

func TestQueryCursorProductPreservesErrorsCancellationAndInvalidWindows(t *testing.T) {
	probe := &queryCursorProbe{}
	for _, sample := range []struct {
		registration productrpc.Registration
		raw          string
	}{
		{queryCursorOpenRegistration(probe), `{"tableId":"orders","query":{}}`},
		{queryCursorFetchRegistration(probe), `{"cursor":"opaque"}`},
	} {
		t.Run(sample.registration.Method, func(t *testing.T) {
			raw := json.RawMessage(sample.raw)
			*probe = queryCursorProbe{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := sample.registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.opens+probe.fetches != 0 {
				t.Fatalf("early cancellation: %v %+v", err, probe)
			}
			source := &query.ProductError{Code: "query.cursor_stale", Path: "cursor", Message: "Cursor is stale", Details: map[string]any{"expected": "data_0"}, Retryable: true}
			probe.err = source
			_, err := sample.registration.Handler(context.Background(), raw)
			var public *productrpc.PublicError
			if !errors.As(err, &public) || public.Code != source.Code || public.Path == nil || *public.Path != source.Path || public.Message != source.Message || !public.Retryable || !reflect.DeepEqual(public.Details, source.Details) {
				t.Fatalf("public error: %v", err)
			}
			probe.err = errors.New("private database failure")
			_, err = sample.registration.Handler(context.Background(), raw)
			if !errors.As(err, &public) || public.Code != "query.internal.failed" || public.Message != "query operation failed" {
				t.Fatalf("unknown error: %v", err)
			}
			probe.err = context.DeadlineExceeded
			if _, err := sample.registration.Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline: %v", err)
			}
			probe.err = nil
			next := "next"
			for _, window := range []query.CursorWindow{{}, {Rows: []map[string]any{nil}}, {Rows: []map[string]any{}, HasMore: true}, {Rows: []map[string]any{}, NextCursor: &next}} {
				probe.window = window
				_, err := sample.registration.Handler(context.Background(), raw)
				if err == nil || errors.As(err, &public) {
					t.Fatalf("invalid window must be internal failure: %+v %v", window, err)
				}
			}
			ctx, cancel = context.WithCancel(context.Background())
			defer cancel()
			probe.window = query.CursorWindow{Rows: []map[string]any{}}
			probe.cancel = cancel
			if _, err := sample.registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) {
				t.Fatalf("late cancellation: %v", err)
			}
		})
	}
}

func TestQueryCursorProductKeepsBothPythonJSONBudgets(t *testing.T) {
	for _, operation := range []string{"cursor.open", "cursor.fetch"} {
		for _, character := range []string{"a", "é", "\u2028", "<", "\n", "\x00"} {
			var encoded strings.Builder
			if err := appendDescribeRevision(&encoded, character); err != nil {
				t.Fatal(err)
			}
			width := encoded.Len() - 2
			skeleton := `{"cursor":""}`
			if operation == "cursor.open" {
				skeleton = `{"tableId":"","query":{}}`
			}
			overhead := len(`,"operation":"` + operation + `"`)
			for _, extra := range []int{0, 1, overhead, overhead + 1} {
				available := maxQueryRequestBytes - len(skeleton) - overhead + extra
				value := "x" + strings.Repeat(character, (available-1)/width) + strings.Repeat("a", (available-1)%width)
				probe := &queryCursorProbe{window: query.CursorWindow{Rows: []map[string]any{}}}
				registration := queryCursorFetchRegistration(probe)
				params := map[string]any{"cursor": value}
				if operation == "cursor.open" {
					registration = queryCursorOpenRegistration(probe)
					params = map[string]any{"tableId": value, "query": map[string]any{}}
				}
				raw, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				err = registration.ValidateParams(raw)
				if extra > overhead {
					if err == nil {
						t.Fatalf("accepted over Product budget: %s %q", operation, character)
					}
					continue
				}
				if err != nil {
					t.Fatalf("Product budget %s %q +%d: %v", operation, character, extra, err)
				}
				_, err = registration.Handler(context.Background(), raw)
				if extra == 0 {
					if err != nil || probe.opens+probe.fetches != 1 {
						t.Fatalf("exact REST budget %s: %v", operation, err)
					}
				} else {
					var public *productrpc.PublicError
					if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Message != "query request exceeds the 1 MiB limit" || probe.opens+probe.fetches != 0 {
						t.Fatalf("REST budget %s %q +%d: %v", operation, character, extra, err)
					}
				}
			}
		}
	}
}
