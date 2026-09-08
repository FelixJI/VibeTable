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

type queryReadRowsProbe struct {
	calls  int
	table  string
	ids    []string
	rows   []map[string]any
	err    error
	cancel context.CancelFunc
}

func (p *queryReadRowsProbe) ReadRows(_ context.Context, table string, ids []string) ([]map[string]any, error) {
	p.calls++
	p.table = table
	p.ids = ids
	if p.cancel != nil {
		p.cancel()
	}
	return p.rows, p.err
}

func TestQueryReadRowsProductPreservesPythonRejectionLayers(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"tableId":"orders"}`, `{"tableId":"","rowIds":[]}`,
		`{"tableId":"orders","rowIds":null}`, `{"tableId":false,"rowIds":[]}`,
		`{"tableId":"orders","rowIds":"r1"}`, `{"tableId":"orders","rowIds":[],"extra":0}`,
		`{"tableId":"orders","rowIds":[{"password":"x"}]}`,
		`{"tableId":"\ud800","rowIds":[]}`, `{"tableId":"orders","rowIds":["\udc00"]}`,
		"{\"tableId\":\"\xff\",\"rowIds\":[]}",
	} {
		if queryReadRowsRegistration(nil).ValidateParams(json.RawMessage(raw)) == nil {
			t.Errorf("accepted invalid params %q", raw)
		}
	}
	for _, id := range []string{`""`, `0`, `false`, `null`, `{}`, `[]`} {
		raw := json.RawMessage(`{"tableId":"orders","rowIds":[` + id + `]}`)
		p := &queryReadRowsProbe{}
		registration := queryReadRowsRegistration(p)
		if err := registration.ValidateParams(raw); err != nil {
			t.Fatalf("element validation moved out of handler: %v", err)
		}
		_, err := registration.Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if err == nil || errors.As(err, &public) || p.calls != 0 {
			t.Fatalf("invalid element %s: %v, calls %d", id, err, p.calls)
		}
	}
	for _, depth := range []int{31, 32} {
		raw := json.RawMessage(`{"tableId":"orders","rowIds":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + `}`)
		err := queryReadRowsRegistration(nil).ValidateParams(raw)
		if (err == nil) != (depth == 31) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}

func TestQueryReadRowsProductPreservesValuesAndOrdering(t *testing.T) {
	rows := []map[string]any{{"id": "r1", "text": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي", "zero": 0, "false": false, "null": nil, "array": []any{}, "object": map[string]any{}}}
	p := &queryReadRowsProbe{rows: rows}
	registration := queryReadRowsRegistration(p)
	raw := json.RawMessage(`{"tableId":"中文/表","rowIds":["r1","r1"," ","\ud83d\ude00"]}`)
	if err := registration.ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	got, err := registration.Handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.table != "中文/表" || !reflect.DeepEqual(p.ids, []string{"r1", "r1", " ", "😀"}) || !reflect.DeepEqual(got, map[string]any{"rows": rows}) {
		t.Fatalf("projection or forwarding changed: %#v, %#v", p, got)
	}
	p.rows = []map[string]any{}
	got, err = registration.Handler(context.Background(), json.RawMessage(`{"tableId":"orders","rowIds":[]}`))
	if err != nil || !reflect.DeepEqual(got, map[string]any{"rows": []map[string]any{}}) || len(p.ids) != 0 {
		t.Fatalf("empty rows: %#v %v", got, err)
	}
	for _, malformed := range [][]map[string]any{nil, {nil}} {
		p.rows = malformed
		if _, err := registration.Handler(context.Background(), raw); err == nil {
			t.Fatal("accepted malformed rows")
		}
	}
}

func TestQueryReadRowsProductKeepsBothPythonJSONBudgets(t *testing.T) {
	for _, sample := range []struct {
		character string
		size      int
	}{{"a", 1}, {"é", 2}, {"\u2028", 3}, {"<", 1}, {"\n", 2}, {"\x00", 6}} {
		for _, extra := range []int{0, 1, len(`,"operation":"readRows"`), len(`,"operation":"readRows"`) + 1} {
			available := maxQueryRequestBytes - len(`{"tableId":"","rowIds":[]}`) - len(`,"operation":"readRows"`) + extra
			table := "x" + strings.Repeat(sample.character, (available-1)/sample.size) + strings.Repeat("a", (available-1)%sample.size)
			raw, err := json.Marshal(map[string]any{"tableId": table, "rowIds": []any{}})
			if err != nil {
				t.Fatal(err)
			}
			p := &queryReadRowsProbe{rows: []map[string]any{}}
			registration := queryReadRowsRegistration(p)
			err = registration.ValidateParams(raw)
			if extra > len(`,"operation":"readRows"`) {
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

func TestQueryReadRowsProductPreservesDomainErrorsAndCancellation(t *testing.T) {
	raw := json.RawMessage(`{"tableId":"orders","rowIds":["r1"]}`)
	source := &query.ProductError{Code: "query.rows.limit", Path: "rowIds", Message: "at most 200 row ids are allowed", Details: map[string]any{"limit": 200}, Retryable: true}
	p := &queryReadRowsProbe{err: source}
	registration := queryReadRowsRegistration(p)
	_, err := registration.Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != source.Code || public.Path == nil || *public.Path != source.Path || !reflect.DeepEqual(public.Details, source.Details) || !public.Retryable {
		t.Fatalf("domain mapping: %#v", err)
	}
	p.err = errors.New("private storage details")
	_, err = registration.Handler(context.Background(), raw)
	if !errors.As(err, &public) || public.Code != "query.internal.failed" || public.Message != "query operation failed" {
		t.Fatalf("unknown error: %#v", err)
	}
	p.calls = 0
	_, err = registration.Handler(context.Background(), json.RawMessage(`{"tableId":" \t","rowIds":[]}`))
	if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Path == nil || *public.Path != "tableId" || p.calls != 0 {
		t.Fatalf("blank table: %v calls %d", err, p.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatalf("early cancellation: %v", err)
	}
	p.err = context.DeadlineExceeded
	if _, err := registration.Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("port deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	p.err = nil
	p.rows = []map[string]any{}
	p.cancel = cancel
	if _, err := registration.Handler(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation: %v", err)
	}
}
