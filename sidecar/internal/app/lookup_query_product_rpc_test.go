package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

type lookupQueryProbe struct {
	catalog                     relation.CatalogResult
	view                        relation.LookupQueryResult
	describeErr, queryErr       error
	calls                       []string
	input                       relation.LookupQueryRequest
	cancelDescribe, cancelQuery context.CancelFunc
}

func (p *lookupQueryProbe) Describe(_ context.Context, table string) (relation.CatalogResult, error) {
	p.calls = append(p.calls, "describe:"+table)
	if p.cancelDescribe != nil {
		p.cancelDescribe()
	}
	return p.catalog, p.describeErr
}
func (p *lookupQueryProbe) QueryLookups(_ context.Context, input relation.LookupQueryRequest) (relation.LookupQueryResult, error) {
	p.calls = append(p.calls, "query")
	p.input = input
	if p.cancelQuery != nil {
		p.cancelQuery()
	}
	return p.view, p.queryErr
}

type lookupQueryOracleCase struct {
	Name    string `json:"name"`
	Request struct {
		Params json.RawMessage `json:"params"`
	} `json:"request"`
	Fixture struct {
		Catalog, Page json.RawMessage
		Failure       *string
	} `json:"authorityFixture"`
	AuthorityRequests []json.RawMessage `json:"authorityRequests"`
	Response          json.RawMessage   `json:"response"`
}

func lookupQueryOracle(t *testing.T) []lookupQueryOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "lookup-query-python-oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string                  `json:"producerCommit"`
		Cases    []lookupQueryOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Producer != "6e25fd033697c57a4ca113caf98c90293b892548" || len(corpus.Cases) != 39 {
		t.Fatalf("unexpected original corpus %s/%d", corpus.Producer, len(corpus.Cases))
	}
	return corpus.Cases
}
func lookupQueryTypedFixture(t *testing.T, grouped bool) (*lookupQueryProbe, map[string]any) {
	t.Helper()
	name := "typed-shape-dynamic-rows"
	if grouped {
		name = "typed-shape-grouped-rows"
	}
	for _, sample := range lookupQueryOracle(t) {
		if sample.Name == name {
			p := &lookupQueryProbe{}
			if err := json.Unmarshal(sample.Fixture.Catalog, &p.catalog); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sample.Fixture.Page, &p.view); err != nil {
				t.Fatal(err)
			}
			params, err := decodeLookupQueryParams(sample.Request.Params)
			if err != nil {
				t.Fatal(err)
			}
			return p, params
		}
	}
	t.Fatal("typed fixture missing")
	return nil, nil
}
func lookupQueryBytes(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func lookupQuerySetRevision(t *testing.T, p *lookupQueryProbe, params map[string]any) {
	t.Helper()
	revision, err := describeRevision(map[string]any{"schemaRevision": p.catalog.SchemaRevision, "lookups": p.catalog.Lookups})
	if err != nil {
		t.Fatal(err)
	}
	params["lookupRevision"] = revision
}

func TestLookupQueryProductOrderingProjectionAndCancellation(t *testing.T) {
	for _, stage := range []string{"before", "describe", "query"} {
		t.Run("cancel-"+stage, func(t *testing.T) {
			p, params := lookupQueryTypedFixture(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "before":
				cancel()
			case "describe":
				p.cancelDescribe = cancel
			case "query":
				p.cancelQuery = cancel
			}
			_, err := lookupQueryRegistration(p).Handler(ctx, lookupQueryBytes(t, params))
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			want := map[string]int{"before": 0, "describe": 1, "query": 2}[stage]
			if len(p.calls) != want {
				t.Fatal(p.calls)
			}
		})
	}
	for _, sample := range []struct {
		name    string
		change  func(*lookupQueryProbe, map[string]any)
		calls   int
		message string
	}{
		{"groups before catalog", func(p *lookupQueryProbe, v map[string]any) {
			v["query"] = map[string]any{"groups": nil}
			p.describeErr = errors.New("not reached")
		}, 0, "query.groups"},
		{"revision before direction", func(p *lookupQueryProbe, v map[string]any) {
			v["schemaRevision"] = "stale"
			v["query"] = map[string]any{"groups": []any{map[string]any{"direction": "bad"}}}
		}, 1, "revisions are stale"},
		{"direction before query", func(p *lookupQueryProbe, v map[string]any) {
			v["query"] = map[string]any{"groups": []any{map[string]any{"direction": "bad"}}}
			p.queryErr = errors.New("not reached")
		}, 1, "direction"},
		{"unknown field after query", func(p *lookupQueryProbe, v map[string]any) { v["fieldRefs"] = []any{"unknown"} }, 2, "unknown Lookup"},
		{"window before definitions", func(p *lookupQueryProbe, v map[string]any) {
			p.catalog.Lookups[0].TargetFieldID = ""
			lookupQuerySetRevision(t, p, v)
			p.view.HasMoreGroups = true
		}, 2, "bounded window"},
		{"unselected invalid definition", func(p *lookupQueryProbe, v map[string]any) {
			p.catalog.Lookups = append(p.catalog.Lookups, relation.LookupDescriptor{PhysicalName: "unselected"})
			lookupQuerySetRevision(t, p, v)
		}, 2, "cardinality"},
		{"nil catalog", func(p *lookupQueryProbe, v map[string]any) { p.catalog.Lookups = nil }, 1, "catalog"},
		{"nil rows", func(p *lookupQueryProbe, v map[string]any) { p.view.Rows = nil }, 2, "query page"},
		{"nil groups", func(p *lookupQueryProbe, v map[string]any) { p.view.GroupRows = nil }, 2, "lookup view"},
		{"empty group key", func(p *lookupQueryProbe, v map[string]any) {
			p.view.GroupRows = []query.GroupRow{{Key: []any{}, Summaries: []any{}, Count: 1}}
		}, 2, "group rows"},
		{"REST missing parent summaries", func(p *lookupQueryProbe, v map[string]any) {
			n := int64(2)
			p.view.GroupRows = []query.GroupRow{{Key: []any{"EU", nil}, Count: 1, Summaries: []any{}, ParentCount: &n}}
		}, 2, "lookup view"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			p, v := lookupQueryTypedFixture(t, false)
			sample.change(p, v)
			_, err := lookupQueryRegistration(p).Handler(context.Background(), lookupQueryBytes(t, v))
			if err == nil || !strings.Contains(err.Error(), sample.message) || len(p.calls) != sample.calls {
				t.Fatalf("err=%v calls=%v", err, p.calls)
			}
		})
	}
	p, v := lookupQueryTypedFixture(t, false)
	second := p.catalog.Lookups[0]
	second.DisplayName = "后者"
	second.OutputStorage = "number"
	p.catalog.Lookups = append(p.catalog.Lookups, second)
	lookupQuerySetRevision(t, p, v)
	v["fieldRefs"] = []any{"customer_name", "customer_name"}
	v["requestGeneration"] = json.Number("-123456789012345678901234567890")
	got, err := lookupQueryRegistration(p).Handler(context.Background(), lookupQueryBytes(t, v))
	if err != nil {
		t.Fatal(err)
	}
	result := got.(map[string]any)
	columns := result["columns"].([]any)
	if len(columns) != 2 || !reflect.DeepEqual(columns[0], columns[1]) || columns[0].(map[string]any)["title"] != "后者" || columns[0].(map[string]any)["outputType"] != "decimal" {
		t.Fatal(columns)
	}
	if result["requestGeneration"] != v["requestGeneration"] || !reflect.DeepEqual(result["rows"], p.view.Rows) {
		t.Fatal(result)
	}
}

func TestLookupQueryProductClosedParamsAndRESTBoundary(t *testing.T) {
	_, base := lookupQueryTypedFixture(t, false)
	for name := range base {
		copy := map[string]any{}
		for key, value := range base {
			if key != name {
				copy[key] = value
			}
		}
		if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, copy)) == nil {
			t.Fatal(name)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{}`, strings.Replace(string(lookupQueryBytes(t, base)), `"orders"`, `"\ud800"`, 1)} {
		if lookupQueryRegistration(nil).ValidateParams([]byte(raw)) == nil {
			t.Fatal(raw)
		}
	}
	for _, field := range []string{"requestGeneration", "query", "fieldRefs", "collection"} {
		_, v := lookupQueryTypedFixture(t, false)
		v[field] = true
		if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, v)) == nil {
			t.Fatal(field)
		}
	}
	for _, key := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		_, v := lookupQueryTypedFixture(t, false)
		v["query"] = map[string]any{"nested": map[string]any{key: "test-only"}}
		if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, v)) == nil {
			t.Fatal(key)
		}
	}
	_, v := lookupQueryTypedFixture(t, false)
	var deep any = nil
	for range 33 {
		deep = []any{deep}
	}
	v["query"] = map[string]any{"probe": deep}
	if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, v)) == nil {
		t.Fatal("depth accepted")
	}
	_, v = lookupQueryTypedFixture(t, false)
	v["contract"] = ""
	overhead := len(lookupQueryBytes(t, v))
	v["contract"] = strings.Repeat("x", maxRelationRequestBytes-overhead)
	if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, v)) != nil {
		t.Fatal("exact Product budget rejected")
	}
	v["contract"] = v["contract"].(string) + "x"
	if lookupQueryRegistration(nil).ValidateParams(lookupQueryBytes(t, v)) == nil {
		t.Fatal("over budget accepted")
	}
	for _, body := range []map[string]any{{"offset": nil}, {"limit": 0}, {"limit": 501}, {"unknown": true}} {
		p, v := lookupQueryTypedFixture(t, false)
		v["query"] = body
		_, err := lookupQueryRegistration(p).Handler(context.Background(), lookupQueryBytes(t, v))
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "relation.request.invalid" || len(p.calls) != 1 {
			t.Fatalf("query=%v error=%v calls=%v", body, err, p.calls)
		}
	}
	for _, second := range []bool{false, true} {
		p, v := lookupQueryTypedFixture(t, false)
		path := "schemaRevision"
		failure := &mutation.ProductError{Code: "lookup.schema_revision_conflict", Path: &path, Message: "conflict", Details: map[string]any{"phase": "page"}}
		if second {
			p.queryErr = failure
		} else {
			p.describeErr = failure
		}
		_, err := lookupQueryRegistration(p).Handler(context.Background(), lookupQueryBytes(t, v))
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != failure.Code || !reflect.DeepEqual(public.Details, failure.Details) {
			t.Fatal(err)
		}
	}
	p, v := lookupQueryTypedFixture(t, false)
	p.queryErr = &query.ProductError{Code: "query.table.not_found", Path: "tableId", Message: "missing"}
	_, err := lookupQueryRegistration(p).Handler(context.Background(), lookupQueryBytes(t, v))
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "query.table.not_found" {
		t.Fatal(err)
	}
}

func TestLookupQueryProductDynamicParentIdentity(t *testing.T) {
	count := int64(1)
	rows := []query.GroupRow{}
	for _, key := range []any{int64(1), float64(2), false, nil, map[string]any{"b": 2, "a": 1}, map[string]any{"a": 1, "b": 2}} {
		rows = append(rows, query.GroupRow{Key: []any{key, "child"}, Count: 1, ParentCount: &count, Summaries: []any{1}, ParentSummaries: []any{1}})
	}
	nodes, err := lookupQueryGroupNodes(rows)
	if err != nil || len(nodes) != 11 {
		t.Fatalf("dynamic parents collapsed: nodes=%v err=%v", nodes, err)
	}
}
