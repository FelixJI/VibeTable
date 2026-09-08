package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
)

type relationSearchProbe struct {
	calls   int
	request relation.SearchRequest
	result  relation.SearchResult
	err     error
	cancel  context.CancelFunc
}

func (probe *relationSearchProbe) SearchTargets(_ context.Context, input relation.SearchRequest) (relation.SearchResult, error) {
	probe.calls++
	probe.request = input
	if probe.cancel != nil {
		probe.cancel()
	}
	return probe.result, probe.err
}

func TestRelationSearchProductDefaultsAndProjection(t *testing.T) {
	probe := &relationSearchProbe{result: relation.SearchResult{Items: []relation.TargetRef{{TableID: "t", RecordID: "r", Label: "中文 Cafe\u0301", SecondaryLabel: "hidden"}}, Total: -1}}
	result, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), json.RawMessage(`{"relationId":"rel"}`))
	if err != nil {
		t.Fatal(err)
	}
	if probe.request != (relation.SearchRequest{RelationID: "rel", Query: "", Offset: 0, Limit: 50}) {
		t.Fatal(probe.request)
	}
	if !reflect.DeepEqual(result, map[string]any{"items": []map[string]any{{"collection": "t", "itemId": "r", "label": "中文 Cafe\u0301"}}, "total": int64(-1)}) {
		t.Fatal(result)
	}
	for _, text := range []string{`{"relationId":" ","query":" \t ","offset":-1,"limit":101}`, `{"relationId":"rel","limit":0}`} {
		if _, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), json.RawMessage(text)); err != nil {
			t.Fatal(err)
		}
	}
	for _, items := range [][]relation.TargetRef{nil, {{TableID: "t", RecordID: "r"}}, {{TableID: "t", Label: "x"}}, {{RecordID: "r", Label: "x"}}} {
		probe.result.Items = items
		if _, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), json.RawMessage(`{"relationId":"rel"}`)); err == nil {
			t.Fatal("accepted invalid item projection")
		}
	}
}

func TestRelationSearchProductParameterAndBodyBoundaries(t *testing.T) {
	registration := relationSearchTargetsRegistration(nil)
	for _, raw := range []string{`{}`, `null`, `[]`, `{"relationId":""}`, `{"relationId":"r","query":""}`, `{"relationId":"r","offset":false}`, `{"relationId":"r","limit":1.0}`, `{"relationId":"r","limit":1e0}`, `{"relationId":"r","query":null}`, `{"relationId":"r","extra":1}`, `{"relationId":"\ud800"}`, "{\"relationId\":\"\xff\"}"} {
		if registration.ValidateParams(json.RawMessage(raw)) == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, key := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		if validateRelationSearchValue(map[string]any{"nested": map[string]any{key: "x"}}, 0) == nil {
			t.Fatal(key)
		}
	}
	var nested any = 0
	for range 32 {
		nested = []any{nested}
	}
	if validateRelationSearchValue(nested, 0) != nil || validateRelationSearchValue([]any{nested}, 0) == nil {
		t.Fatal("depth boundary changed")
	}
	prefix, suffix := `{"relationId":"r","query":"`, `"}`
	for _, extra := range []int{-64, 0, 1} {
		raw := json.RawMessage(prefix + strings.Repeat("x", maxRelationRequestBytes-len(prefix)-len(suffix)+extra) + suffix)
		if err := registration.ValidateParams(raw); (err == nil) != (extra <= 0) {
			t.Fatalf("Product budget %d: %v", extra, err)
		}
		if extra > 0 {
			continue
		}
		probe := &relationSearchProbe{result: relation.SearchResult{Items: []relation.TargetRef{}}}
		_, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if extra == 0 && (!errors.As(err, &public) || public.Code != "relation.request.invalid" || probe.calls != 0) {
			t.Fatalf("REST budget %v calls=%d", err, probe.calls)
		}
		if extra < 0 && (err != nil || probe.calls != 1) {
			t.Fatalf("below budgets %v calls=%d", err, probe.calls)
		}
	}
	overflow := json.RawMessage(`{"relationId":"r","limit":999999999999999999999999}`)
	if registration.ValidateParams(overflow) != nil {
		t.Fatal("Python integer should reach REST boundary")
	}
	probe := &relationSearchProbe{}
	_, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), overflow)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "relation.request.invalid" || probe.calls != 0 {
		t.Fatal(err)
	}
}

func TestRelationSearchProductCancellationAndLegacyErrors(t *testing.T) {
	raw := json.RawMessage(`{"relationId":"rel"}`)
	probe := &relationSearchProbe{result: relation.SearchResult{Items: []relation.TargetRef{}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := relationSearchTargetsRegistration(probe).Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 0 {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	probe.cancel = cancel
	if _, err := relationSearchTargetsRegistration(probe).Handler(ctx, raw); !errors.Is(err, context.Canceled) || probe.calls != 1 {
		t.Fatal(err)
	}
	probe.cancel = nil
	path := "relationId"
	source := &mutation.ProductError{Code: "relation.not_found", Path: &path, Message: "Relation was not found", Details: map[string]any{"relationId": "rel"}}
	probe.err = source
	_, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != source.Code || !reflect.DeepEqual(public.Details, source.Details) || public.Path == nil || *public.Path != path {
		t.Fatal(err)
	}
	// The old REST writeMutationError maps foreign query errors to this same closed error.
	for _, source := range []error{errors.New("private sqlite failure"), &query.ProductError{Code: "query.storage.failed", Message: "private"}} {
		probe.err = source
		_, err = relationSearchTargetsRegistration(probe).Handler(context.Background(), raw)
		if !errors.As(err, &public) || public.Code != "mutation.internal.failed" || public.Message != "mutation operation failed" || !public.Retryable {
			t.Fatal(err)
		}
	}
	probe.err = context.DeadlineExceeded
	if _, err := relationSearchTargetsRegistration(probe).Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
