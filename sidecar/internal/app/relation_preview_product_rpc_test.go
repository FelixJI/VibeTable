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

const relationPreviewParams = `{"relationId":"orders.related","sourceItemId":"source-1","expectedSchemaRevision":"schema-1","adds":[],"removes":[],"idempotencyKey":"preview-1"}`

type relationPreviewProbe struct {
	calls   int
	request relation.DeltaRequest
	result  relation.DeltaPreview
	err     error
	after   func()
}

func (p *relationPreviewProbe) PreviewDelta(_ context.Context, r relation.DeltaRequest) (relation.DeltaPreview, error) {
	p.calls++
	p.request = r
	if p.after != nil {
		p.after()
	}
	return p.result, p.err
}
func previewJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func previewObject(t *testing.T) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(relationPreviewParams), &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestRelationPreviewProductTranslationAndEcho(t *testing.T) {
	original := previewObject(t)
	original["expectedDateUpdated"] = map[string]any{"ignored": false}
	original["adds"] = []any{map[string]any{"target": map[string]any{"collection": "authors", "itemId": "a2", "label": ""}, "extra": []any{false, "中文"}}}
	original["removes"] = []any{map[string]any{"tableId": "authors", "collection": "wrong", "recordId": "a1", "itemId": "wrong", "label": " "}}
	p := &relationPreviewProbe{result: relation.DeltaPreview{
		Current: []relation.TargetRef{{TableID: "authors", RecordID: "a1", Label: "中文 Cafe\u0301"}, {TableID: "authors", RecordID: "a3", Label: "三", SecondaryLabel: "副"}},
		Result:  []relation.TargetRef{{RecordID: "a2"}}, Adds: 99, Removes: 99, CanApply: false,
	}}
	result, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), previewJSON(t, original))
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)
	if !reflect.DeepEqual(got["delta"], original) || len(got) != 4 || got["canApply"] != false || !reflect.DeepEqual(got["diagnostics"], []any{}) {
		t.Fatal(got)
	}
	want := []any{
		map[string]any{"collection": "authors", "itemId": "a1", "label": "中文 Cafe\u0301", "secondaryLabel": nil},
		map[string]any{"collection": "authors", "itemId": "a3", "label": "三", "secondaryLabel": "副"},
	}
	if !reflect.DeepEqual(got["current"], want) {
		t.Fatal(got)
	}
	if p.calls != 1 || p.request.SourceRecordID != "source-1" || p.request.SchemaRevision != "schema-1" || p.request.RequestID != "preview-1" || p.request.IdempotencyKey != "preview-1" || p.request.ExpectedDigest != nil || p.request.Actor.Type != "user" || p.request.Actor.ID != "local-user" {
		t.Fatal(p.request)
	}
	if !reflect.DeepEqual(p.request.Adds, []relation.TargetRef{{TableID: "authors", RecordID: "a2", Label: "a2"}}) || !reflect.DeepEqual(p.request.Removes, []relation.TargetRef{{TableID: "authors", RecordID: "a1", Label: " "}}) {
		t.Fatal(p.request)
	}
}

func TestRelationPreviewProductKeepsValidationLayers(t *testing.T) {
	reg := relationPreviewDeltaRegistration(nil)
	for _, raw := range []string{"null", "[]", "{}",
		strings.Replace(relationPreviewParams, `"relationId"`, `"unknown"`, 1),
		strings.Replace(relationPreviewParams, "orders.related", `\ud800`, 1),
		strings.Replace(relationPreviewParams, "orders.related", string([]byte{0xff}), 1),
	} {
		if err := reg.ValidateParams(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, change := range []struct {
		key   string
		value any
	}{
		{"relationId", ""}, {"relationId", false}, {"sourceItemId", nil}, {"expectedSchemaRevision", 1},
		{"adds", nil}, {"adds", []any{false}}, {"removes", map[string]any{}}, {"idempotencyKey", ""},
		{"adds", []any{map[string]any{"target": map[string]any{}, "collection": "authors", "itemId": "a1"}}},
		{"adds", []any{map[string]any{"collection": "authors", "itemId": "a1", "label": nil}}},
	} {
		object := previewObject(t)
		object[change.key] = change.value
		raw := previewJSON(t, object)
		if err := reg.ValidateParams(raw); err != nil {
			t.Fatalf("wrong Product layer %s: %v", raw, err)
		}
		p := &relationPreviewProbe{}
		_, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), raw)
		var public *productrpc.PublicError
		if err == nil || errors.As(err, &public) || p.calls != 0 {
			t.Fatalf("handler rejection %v calls=%d", err, p.calls)
		}
	}
	for _, key := range []string{"accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret"} {
		object := previewObject(t)
		object["expectedDateUpdated"] = map[string]any{key: "oracle-only"}
		if err := reg.ValidateParams(previewJSON(t, object)); err == nil {
			t.Fatal(key)
		}
	}
	var nested any = 0
	for range 31 {
		nested = []any{nested}
	}
	object := previewObject(t)
	object["expectedDateUpdated"] = nested
	if err := reg.ValidateParams(previewJSON(t, object)); err != nil {
		t.Fatal(err)
	}
	object["expectedDateUpdated"] = []any{nested}
	if err := reg.ValidateParams(previewJSON(t, object)); err == nil {
		t.Fatal("depth 33 accepted")
	}
}

func TestRelationPreviewProductKeepsBothBudgets(t *testing.T) {
	target := map[string]any{"collection": "authors", "itemId": "a2", "label": ""}
	object := previewObject(t)
	object["adds"] = []any{target}
	target["label"] = strings.Repeat("x", (1<<20)-len(previewJSON(t, object)))
	raw := previewJSON(t, object)
	if err := relationPreviewDeltaRegistration(nil).ValidateParams(raw); err != nil {
		t.Fatal(err)
	}
	p := &relationPreviewProbe{result: relation.DeltaPreview{Current: []relation.TargetRef{}, CanApply: true}}
	_, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "relation.request.invalid" || p.calls != 0 {
		t.Fatalf("REST budget %v %+v", err, p)
	}
	target["label"] = target["label"].(string) + "x"
	if err := relationPreviewDeltaRegistration(nil).ValidateParams(previewJSON(t, object)); err == nil {
		t.Fatal("Product budget exceeded")
	}
	object = previewObject(t)
	object["expectedDateUpdated"] = ""
	object["expectedDateUpdated"] = strings.Repeat("x", (1<<20)-len(previewJSON(t, object)))
	if _, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), previewJSON(t, object)); err != nil || p.calls != 1 {
		t.Fatalf("echo-only budget %v", err)
	}
}

func TestRelationPreviewProductErrorsAndCancellation(t *testing.T) {
	raw := json.RawMessage(relationPreviewParams)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &relationPreviewProbe{result: relation.DeltaPreview{Current: []relation.TargetRef{}}}
	if _, err := relationPreviewDeltaRegistration(p).Handler(ctx, raw); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	p.after = cancel
	if _, err := relationPreviewDeltaRegistration(p).Handler(ctx, raw); !errors.Is(err, context.Canceled) || p.calls != 1 {
		t.Fatal(err)
	}
	p.after = nil
	path := "removes[0]"
	source := &mutation.ProductError{Code: "relation.target_not_linked", Path: &path, Message: "remove target is not linked", Details: map[string]any{"recordId": "a1"}}
	p.err = source
	_, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), raw)
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != source.Code || public.Path == nil || *public.Path != path || public.Message != source.Message || !reflect.DeepEqual(public.Details, source.Details) || public.Retryable {
		t.Fatal(err)
	}
	for _, failure := range []error{errors.New("private"), &query.ProductError{Code: "query.rows.limit", Message: "private"}} {
		p.err = failure
		_, err = relationPreviewDeltaRegistration(p).Handler(context.Background(), raw)
		if !errors.As(err, &public) || public.Code != "mutation.internal.failed" || public.Message != "mutation operation failed" || !public.Retryable {
			t.Fatal(err)
		}
	}
	p.err = context.DeadlineExceeded
	if _, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	p.err = nil
	for _, current := range [][]relation.TargetRef{nil, {{RecordID: "a1", Label: "label"}}, {{TableID: "authors", Label: "label"}}, {{TableID: "authors", RecordID: "a1"}}} {
		p.result.Current = current
		if _, err := relationPreviewDeltaRegistration(p).Handler(context.Background(), raw); err == nil {
			t.Fatal("invalid current accepted")
		}
	}
}
