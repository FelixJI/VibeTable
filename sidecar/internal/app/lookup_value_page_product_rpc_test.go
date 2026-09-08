package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	lookupcalc "github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

type valuePageProbe struct {
	catalog                  relation.CatalogResult
	result                   lookupcalc.CellValue
	catalogErr, pageErr      error
	calls                    []string
	collection               string
	request                  relation.LookupValuePageRequest
	afterDescribe, afterPage func()
}

func (p *valuePageProbe) Describe(_ context.Context, table string) (relation.CatalogResult, error) {
	p.calls = append(p.calls, "describe")
	p.collection = table
	if p.afterDescribe != nil {
		p.afterDescribe()
	}
	return p.catalog, p.catalogErr
}
func (p *valuePageProbe) LookupValuePage(_ context.Context, input relation.LookupValuePageRequest) (lookupcalc.CellValue, error) {
	p.calls = append(p.calls, "page")
	p.request = input
	if p.afterPage != nil {
		p.afterPage()
	}
	return p.result, p.pageErr
}
func valuePageJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func valuePageParams(t *testing.T, catalog relation.CatalogResult) map[string]any {
	t.Helper()
	revision, err := describeRevision(map[string]any{"schemaRevision": catalog.SchemaRevision, "lookups": catalog.Lookups})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"collection": "orders", "fieldRef": "physical_lookup", "sourceRecordId": "source-1",
		"schemaRevision": catalog.SchemaRevision, "permissionRevision": catalog.SchemaRevision, "lookupRevision": revision, "offset": 0, "limit": 1}
}
func newValuePageProbe() *valuePageProbe {
	return &valuePageProbe{catalog: relation.CatalogResult{SchemaRevision: "schema-1", Lookups: []relation.LookupDescriptor{
		{FieldID: "stable-id", PhysicalName: "physical_lookup"},
	}}, result: lookupcalc.CellValue{State: "ok", Value: []any{false, 0, "", nil, []any{}, map[string]any{}, "中文 Cafe\u0301 👩🏽‍💻"}, Provenance: []lookupcalc.ValueProvenance{}, ProvenanceTotalKnown: true, ProvenanceLimit: 1}}
}

func TestLookupValuePageProductPreservesTranslationAndTypedResult(t *testing.T) {
	p := newValuePageProbe()
	p.catalog.Lookups = append(p.catalog.Lookups, relation.LookupDescriptor{FieldID: "must-not-select", PhysicalName: "physical_lookup"})
	params := valuePageParams(t, p.catalog)
	params["collection"] = " 中文 "
	params["sourceRecordId"] = " Cafe\u0301 "
	p.result.State = "domain-state"
	p.result.Provenance = nil
	p.result.ProvenanceTotal = -1
	p.result.Diagnostic = &lookupcalc.Diagnostic{Code: "domain.code", Message: "细节"}
	p.result.Value = map[string]any{"null": nil, "false": false, "zero": 0, "array": []any{}, "object": map[string]any{}}
	result, err := lookupValuePageRegistration(p).Handler(context.Background(), valuePageJSON(t, params))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.calls, []string{"describe", "page"}) || p.collection != " 中文 " || p.request != (relation.LookupValuePageRequest{
		TableID: " 中文 ", SchemaRevision: "schema-1", SourceRecordID: " Cafe\u0301 ", FieldID: "stable-id", Offset: 0, Limit: 1,
	}) {
		t.Fatalf("calls=%v request=%+v", p.calls, p.request)
	}
	if !reflect.DeepEqual(viewWireJSON(t, valuePageJSON(t, result)), viewWireJSON(t, valuePageJSON(t, p.result))) {
		t.Fatal("typed JSON projection changed")
	}
}

func TestLookupValuePageProductKeepsValidationAndHandlerOrder(t *testing.T) {
	p := newValuePageProbe()
	params := valuePageParams(t, p.catalog)
	registration := lookupValuePageRegistration(p)
	for key := range params {
		copy := valuePageParams(t, p.catalog)
		delete(copy, key)
		if registration.ValidateParams(valuePageJSON(t, copy)) == nil {
			t.Fatal("missing accepted", key)
		}
	}
	for _, change := range []struct {
		key   string
		value any
	}{
		{"extra", true}, {"limit", true}, {"offset", 1.0}, {"collection", ""}, {"sourceRecordId", nil}, {"fieldRef", []any{}}, {"sourceRecordId", map[string]any{"sessionSecret": "rejected"}},
	} {
		copy := valuePageParams(t, p.catalog)
		copy[change.key] = change.value
		raw := valuePageJSON(t, copy)
		// json.Marshal(1.0) emits integer wire text; retain the actual float boundary.
		if change.key == "offset" {
			raw = json.RawMessage(strings.Replace(string(raw), `"offset":1`, `"offset":1.0`, 1))
		}
		if registration.ValidateParams(raw) == nil {
			t.Fatal("invalid accepted", change)
		}
	}
	raw := string(valuePageJSON(t, params))
	raw = strings.Replace(raw, `"sourceRecordId":"source-1"`, `"sourceRecordId":"\ud800"`, 1)
	if registration.ValidateParams(json.RawMessage(raw)) == nil {
		t.Fatal("surrogate accepted")
	}
	raw = strings.Replace(string(valuePageJSON(t, params)), "source-1", string([]byte{0xff}), 1)
	if registration.ValidateParams(json.RawMessage(raw)) == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	for _, key := range []string{"schemaRevision", "permissionRevision", "lookupRevision"} {
		p.calls = nil
		copy := valuePageParams(t, p.catalog)
		copy[key] = "stale"
		_, err := registration.Handler(context.Background(), valuePageJSON(t, copy))
		if err == nil || !strings.Contains(err.Error(), "revisions are stale") || !reflect.DeepEqual(p.calls, []string{"describe"}) {
			t.Fatalf("%s %v %v", key, p.calls, err)
		}
	}
	p.catalog.Lookups[0].FieldID = ""
	copy := valuePageParams(t, p.catalog)
	copy["offset"] = -1
	if _, err := registration.Handler(context.Background(), valuePageJSON(t, copy)); err == nil || !strings.Contains(err.Error(), "paging is invalid") {
		t.Fatal("fieldId preempted paging", err)
	}
	copy["offset"] = 0
	if _, err := registration.Handler(context.Background(), valuePageJSON(t, copy)); err == nil || !strings.Contains(err.Error(), "fieldId") {
		t.Fatal(err)
	}
	for _, limit := range []int{0, 501} {
		p = newValuePageProbe()
		copy = valuePageParams(t, p.catalog)
		copy["limit"] = limit
		if _, err := lookupValuePageRegistration(p).Handler(context.Background(), valuePageJSON(t, copy)); err == nil || len(p.calls) != 1 {
			t.Fatal(limit, err, p.calls)
		}
	}
}

func TestLookupValuePageProductKeepsBothBudgetsAndIntegerBoundary(t *testing.T) {
	p := newValuePageProbe()
	params := valuePageParams(t, p.catalog)
	params["sourceRecordId"] = ""
	params["sourceRecordId"] = strings.Repeat("x", maxRelationRequestBytes-len(valuePageJSON(t, params)))
	raw := valuePageJSON(t, params)
	registration := lookupValuePageRegistration(p)
	if len(raw) != maxRelationRequestBytes || registration.ValidateParams(raw) != nil {
		t.Fatal("exact Product budget rejected")
	}
	if _, err := registration.Handler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	params["sourceRecordId"] = params["sourceRecordId"].(string) + "x"
	if registration.ValidateParams(valuePageJSON(t, params)) == nil {
		t.Fatal("over Product budget accepted")
	}
	p = newValuePageProbe()
	p.catalog.Lookups[0].FieldID = strings.Repeat("f", maxRelationRequestBytes)
	params = valuePageParams(t, p.catalog)
	if _, err := lookupValuePageRegistration(p).Handler(context.Background(), valuePageJSON(t, params)); err == nil {
		t.Fatal("REST body budget ignored")
	} else {
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "relation.request.invalid" || len(p.calls) != 1 {
			t.Fatal(err, p.calls)
		}
	}
	p = newValuePageProbe()
	params = valuePageParams(t, p.catalog)
	params["offset"] = json.Number("9223372036854775808")
	registration = lookupValuePageRegistration(p)
	if err := registration.ValidateParams(valuePageJSON(t, params)); err != nil {
		t.Fatal("Product integer was narrowed", err)
	}
	_, err := registration.Handler(context.Background(), valuePageJSON(t, params))
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "relation.request.invalid" || len(p.calls) != 1 {
		t.Fatal(err, p.calls)
	}
}

func TestLookupValuePageProductKeepsErrorFamiliesAndCancellation(t *testing.T) {
	for _, phase := range []string{"describe", "page"} {
		for _, source := range []error{errors.New("internal"), &query.ProductError{Code: "query.storage.failed", Path: "tableId", Message: "query failed"}, &mutation.ProductError{Code: "lookup.schema_revision_conflict", Message: "stale", Details: map[string]any{}}} {
			p := newValuePageProbe()
			if phase == "describe" {
				p.catalogErr = source
			} else {
				p.pageErr = source
			}
			_, err := lookupValuePageRegistration(p).Handler(context.Background(), valuePageJSON(t, valuePageParams(t, p.catalog)))
			var public *productrpc.PublicError
			if !errors.As(err, &public) {
				t.Fatal(err)
			}
			want := "mutation.internal.failed"
			if phase == "page" {
				want = "query.internal.failed"
			}
			var q *query.ProductError
			if phase == "page" && errors.As(source, &q) {
				want = q.Code
			}
			var m *mutation.ProductError
			if errors.As(source, &m) {
				want = m.Code
			}
			if public.Code != want {
				t.Fatalf("%s: got %s want %s", phase, public.Code, want)
			}
		}
	}
	for _, when := range []string{"before", "describe", "page"} {
		p := newValuePageProbe()
		ctx, cancel := context.WithCancel(context.Background())
		wantCalls := 0
		switch when {
		case "before":
			cancel()
		case "describe":
			p.afterDescribe = cancel
			wantCalls = 1
		case "page":
			p.afterPage = cancel
			wantCalls = 2
		}
		_, err := lookupValuePageRegistration(p).Handler(ctx, valuePageJSON(t, valuePageParams(t, p.catalog)))
		cancel()
		if !errors.Is(err, context.Canceled) || len(p.calls) != wantCalls {
			t.Fatal(when, err, p.calls)
		}
	}
	p := newValuePageProbe()
	p.result.Value = math.NaN()
	if _, err := lookupValuePageRegistration(p).Handler(context.Background(), valuePageJSON(t, valuePageParams(t, p.catalog))); err == nil {
		t.Fatal("nonfinite JSON accepted")
	}
}
