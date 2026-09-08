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

type queryPageProbe struct {
	calls  int
	table  string
	input  query.TableQuery
	page   query.Page
	err    error
	cancel context.CancelFunc
}

func (p *queryPageProbe) QueryPage(ctx context.Context, table string, input query.TableQuery) (query.Page, error) {
	if err := ctx.Err(); err != nil {
		return query.Page{}, err
	}
	p.calls++
	p.table, p.input = table, input
	if p.cancel != nil {
		p.cancel()
	}
	return p.page, p.err
}

func TestQueryPageProductSeparatesProductAndRESTRejections(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"tableId":"orders"}`, `{"tableId":"","query":{}}`,
		`{"tableId":"orders","query":null}`, `{"tableId":false,"query":{}}`,
		`{"tableId":"orders","query":[],"extra":0}`, `{"tableId":"orders","query":{},"extra":0}`,
		`{"tableId":"orders","query":{"filters":[{"value":{"password":"x"}}]}}`,
		`{"tableId":"\ud800","query":{}}`, `{"tableId":"orders","query":{"keyword":"\udc00"}}`,
		"{\"tableId\":\"\xff\",\"query\":{}}",
	} {
		if queryPageRegistration(nil).ValidateParams(json.RawMessage(raw)) == nil {
			t.Errorf("accepted invalid Product params %q", raw)
		}
	}
	for _, input := range []string{
		`{"unknown":0}`, `{"offset":false}`, `{"offset":1.0}`, `{"offset":null}`,
		`{"limit":0}`, `{"limit":501}`, `{"filters":null}`, `{"sorts":[{"field":"x","nullsLast":null}]}`,
		`{"filters":[{"field":"x","extra":true}]}`,
	} {
		raw := json.RawMessage(`{"tableId":"orders","query":` + input + `}`)
		probe := &queryPageProbe{}
		registration := queryPageRegistration(probe)
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("REST rejection moved to Product layer: %s %v", input, err)
		}
		_, err := registration.Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Message != "query request body is invalid" || probe.calls != 0 {
			t.Fatalf("REST input %s: %v calls %d", input, err, probe.calls)
		}
	}
	for _, depth := range []int{30, 31} {
		raw := json.RawMessage(`{"tableId":"orders","query":{"value":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + `}}`)
		if err := queryPageRegistration(nil).ValidateParams(raw); (err == nil) != (depth == 30) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}

func TestQueryPageProductPreservesTypedInputAndClosedProjection(t *testing.T) {
	rows := []map[string]any{{"id": "r1", "text": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي", "zero": 0, "false": false, "null": nil, "array": []any{}, "object": map[string]any{}}}
	page := query.Page{Rows: rows, Offset: 3, Limit: 5, FilteredRows: 1, TotalRows: 7, Snapshot: query.QuerySnapshot{SchemaRevision: "schema_1", DataRevision: 0}}
	probe := &queryPageProbe{page: page}
	registration := queryPageRegistration(probe)
	raw := json.RawMessage(`{"tableId":"中文/表","query":{"keyword":"\ud83d\ude00","offset":-1,"limit":5,"filters":[{"field":"x","value":false},{"field":"x","value":-0.0}],"sorts":[{"field":"x"}]}}`)
	if err := registration.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	result, err := registration.Handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if probe.table != "中文/表" || probe.input.Keyword != "😀" || probe.input.Offset != -1 || probe.input.Limit != 5 || probe.input.Filters[0].Value != false || probe.input.Filters[1].Value != json.Number("-0.0") || probe.input.Sorts[0].NullsLast == nil || !*probe.input.Sorts[0].NullsLast {
		t.Fatalf("typed REST input changed: %#v", probe.input)
	}
	want := map[string]any{"rows": rows, "offset": 3, "limit": 5, "filteredRows": int64(1), "totalRows": int64(7), "snapshot": page.Snapshot}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("projection got %#v want %#v", result, want)
	}
	probe.page.Rows = []map[string]any{}
	if _, err := registration.Handler(context.Background(), json.RawMessage(`{"tableId":"orders","query":{}}`)); err != nil {
		t.Fatal(err)
	}
	if probe.input.Offset != 0 || probe.input.Limit != 100 || probe.input.Filters == nil || probe.input.Sorts == nil {
		t.Fatalf("omitted defaults: %#v", probe.input)
	}
	for _, rows := range [][]map[string]any{nil, {nil}} {
		probe.page.Rows = rows
		if _, err := registration.Handler(context.Background(), raw); err == nil {
			t.Fatal("accepted invalid rows")
		}
	}
}

func TestQueryPageProductPreservesErrorsAndCancellation(t *testing.T) {
	raw := json.RawMessage(`{"tableId":"orders","query":{}}`)
	source := &query.ProductError{Code: "query.snapshot_stale", Path: "snapshot", Message: "Snapshot is stale", Details: map[string]any{"expected": "data_0"}, Retryable: true}
	probe := &queryPageProbe{err: source}
	registration := queryPageRegistration(probe)
	_, err := registration.Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != source.Code || public.Path == nil || *public.Path != source.Path || public.Message != source.Message || !reflect.DeepEqual(public.Details, source.Details) || !public.Retryable {
		t.Fatalf("public error: %#v", err)
	}
	probe.err = errors.New("private storage detail")
	_, err = registration.Handler(context.Background(), raw)
	if !errors.As(err, &public) || public.Code != "query.internal.failed" || public.Message != "query operation failed" {
		t.Fatalf("unknown error: %#v", err)
	}
	probe.calls = 0
	_, err = registration.Handler(context.Background(), json.RawMessage(`{"tableId":" \t","query":{}}`))
	if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Path == nil || *public.Path != "tableId" || probe.calls != 0 {
		t.Fatalf("blank table: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 0 {
		t.Fatalf("early cancellation: %v", err)
	}
	probe.err = context.DeadlineExceeded
	if _, err := registration.Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("port deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	probe.err = nil
	probe.page.Rows = []map[string]any{}
	probe.cancel = cancel
	if _, err := registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation: %v", err)
	}
}

func TestQueryPageProductKeepsBothPythonJSONBudgets(t *testing.T) {
	for _, sample := range []struct {
		character string
		size      int
	}{{"a", 1}, {"é", 2}, {"\u2028", 3}, {"<", 1}, {"\n", 2}, {"\x00", 6}} {
		for _, extra := range []int{0, 1, len(`,"operation":"page"`), len(`,"operation":"page"`) + 1} {
			available := maxQueryRequestBytes - len(`{"tableId":"","query":{}}`) - len(`,"operation":"page"`) + extra
			table := "x" + strings.Repeat(sample.character, (available-1)/sample.size) + strings.Repeat("a", (available-1)%sample.size)
			raw, err := json.Marshal(map[string]any{"tableId": table, "query": map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			p := &queryPageProbe{page: query.Page{Rows: []map[string]any{}}}
			registration := queryPageRegistration(p)
			err = registration.ValidateParams(raw)
			if extra > len(`,"operation":"page"`) {
				if err == nil {
					t.Fatalf("over parameter budget accepted %q", sample.character)
				}
				continue
			}
			if err != nil {
				t.Fatalf("semantic parameter budget %q +%d: %v", sample.character, extra, err)
			}
			_, err = registration.Handler(context.Background(), raw)
			if extra == 0 {
				if err != nil || p.calls != 1 {
					t.Fatalf("exact REST budget: %v calls %d", err, p.calls)
				}
			} else {
				var public *productrpc.PublicError
				if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Message != "query request exceeds the 1 MiB limit" || p.calls != 0 {
					t.Fatalf("REST budget boundary: %v calls %d", err, p.calls)
				}
			}
		}
	}
}
