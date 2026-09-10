package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

type mutationProductProbe struct {
	calls   int
	request mutation.Request
	ctx     context.Context
	preview mutation.PreviewResult
	receipt mutation.Receipt
	err     error
	after   func()
}

func (p *mutationProductProbe) Preview(ctx context.Context, request mutation.Request) (mutation.PreviewResult, error) {
	p.calls++
	p.request, p.ctx = request, ctx
	if p.after != nil {
		p.after()
	}
	return p.preview, p.err
}
func (p *mutationProductProbe) Apply(ctx context.Context, request mutation.Request) (mutation.Receipt, error) {
	p.calls++
	p.request, p.ctx = request, ctx
	if p.after != nil {
		p.after()
	}
	return p.receipt, p.err
}
func mutationProductRegistrations(probe *mutationProductProbe) []productrpc.Registration {
	return []productrpc.Registration{mutationPreviewRegistration(probe), mutationApplyRegistration(probe)}
}
func mutationProductInput() json.RawMessage {
	return json.RawMessage(`{"contractVersion":"2.0","requestId":"request-typed","idempotencyKey":"operation-typed","tableId":"orders","schemaRevision":"schema_1","expectedRevision":null,"expectedDigest":null,"actor":{"type":"user","id":"local-user","displayName":null},"operations":[{"kind":"update","recordId":"abcdefghijklmno","values":{"title":"中文 Café 👩🏽‍💻","number":1.25,"missing":null,"empty":"","enabled":false}}]}`)
}
func mutationProductEdit(t *testing.T, edit func(map[string]any)) json.RawMessage {
	t.Helper()
	var params map[string]any
	if err := json.Unmarshal(mutationProductInput(), &params); err != nil {
		t.Fatal(err)
	}
	edit(params)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMutationProductRootParams(t *testing.T) {
	for _, reg := range mutationProductRegistrations(nil) {
		t.Run(reg.Method, func(t *testing.T) {
			for _, raw := range []json.RawMessage{
				json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{}`),
				mutationProductEdit(t, func(p map[string]any) { delete(p, "actor") }),
				mutationProductEdit(t, func(p map[string]any) { p["operations"] = nil }),
				mutationProductEdit(t, func(p map[string]any) { p["expectedRevision"] = 7 }),
				mutationProductEdit(t, func(p map[string]any) { p["expectedDigest"] = "" }),
				mutationProductEdit(t, func(p map[string]any) { p["internalBypassMigrationFence"] = true }),
				mutationProductEdit(t, func(p map[string]any) { p["actor"] = map[string]any{"password": "private"} }),
				json.RawMessage(strings.Replace(string(mutationProductInput()), "orders", `\ud800`, 1)),
			} {
				if err := reg.ValidateParams(raw); err == nil {
					t.Fatalf("accepted root params %s", raw)
				}
			}
			for _, raw := range []json.RawMessage{mutationProductInput(), mutationProductEdit(t, func(p map[string]any) { p["expectedRevision"], p["expectedDigest"] = nil, nil }), mutationProductEdit(t, func(p map[string]any) { p["actor"], p["operations"] = map[string]any{}, []any{} })} {
				if err := reg.ValidateParams(raw); err != nil {
					t.Fatalf("rejected Python root DTO: %v", err)
				}
			}
		})
	}
}

func TestMutationProductDomainRejectionsDoNotInvokeKernel(t *testing.T) {
	for _, raw := range []json.RawMessage{
		mutationProductEdit(t, func(p map[string]any) { delete(p, "expectedRevision") }),
		mutationProductEdit(t, func(p map[string]any) { p["actor"] = map[string]any{"id": "local-user", "kind": "user"} }),
		mutationProductEdit(t, func(p map[string]any) { p["operations"] = []any{map[string]any{"kind": "unknown"}} }),
		mutationProductEdit(t, func(p map[string]any) {
			p["operations"] = []any{map[string]any{"kind": "update", "recordId": "abcdefghijklmno", "values": nil}}
		}),
	} {
		probe := &mutationProductProbe{}
		for _, reg := range mutationProductRegistrations(probe) {
			if err := reg.ValidateParams(raw); err != nil {
				t.Fatal(err)
			}
			_, err := reg.Handler(context.Background(), raw)
			var public *productrpc.PublicError
			if !errors.As(err, &public) || public.Code != "mutation.request.invalid" {
				t.Fatalf("domain error = %v", err)
			}
		}
		if probe.calls != 0 {
			t.Fatal("domain rejection invoked kernel")
		}
	}
}

func TestMutationProductCompleteHistoricalTypedWire(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/v2/mutation-product-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Cases []struct {
			Name     string
			Request  struct{ Params json.RawMessage }
			Response struct{ Result json.RawMessage }
		}
	}
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range oracle.Cases {
		if !strings.HasSuffix(entry.Name, ":typed-complete") {
			continue
		}
		found++
		t.Run(entry.Name, func(t *testing.T) {
			probe := &mutationProductProbe{}
			reg := mutationPreviewRegistration(probe)
			if strings.HasPrefix(entry.Name, "mutation.preview:") {
				if err := json.Unmarshal(entry.Response.Result, &probe.preview); err != nil {
					t.Fatal(err)
				}
			} else {
				reg = mutationApplyRegistration(probe)
				if err := json.Unmarshal(entry.Response.Result, &probe.receipt); err != nil {
					t.Fatal(err)
				}
			}
			if err := reg.ValidateParams(entry.Request.Params); err != nil {
				t.Fatal(err)
			}
			result, err := reg.Handler(context.Background(), entry.Request.Params)
			if err != nil {
				t.Fatal(err)
			}
			var expected mutation.Request
			if err := mutation.DecodeStrict(entry.Request.Params, &expected); err != nil {
				t.Fatal(err)
			}
			if probe.calls != 1 || !reflect.DeepEqual(probe.request, expected) || probe.request.InternalBypassMigrationFence {
				t.Fatalf("request changed: %+v", probe.request)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var actualWire, expectedWire any
			if err := json.Unmarshal(encoded, &actualWire); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(entry.Response.Result, &expectedWire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actualWire, expectedWire) {
				t.Fatalf("wire changed: %s", encoded)
			}
		})
	}
	if found != 2 {
		t.Fatalf("typed fixtures = %d", found)
	}
}

func TestMutationProductApplyUsesGateAndItsContext(t *testing.T) {
	type gateContextKey struct{}
	probe := &mutationProductProbe{}
	gateCalls := 0
	gate := func(ctx context.Context, kind, identity string, apply func(context.Context) error) error {
		gateCalls++
		if kind != "mutation.apply" || identity != "operation-typed" {
			t.Fatalf("gate identity %s %s", kind, identity)
		}
		return apply(context.WithValue(ctx, gateContextKey{}, "gate"))
	}
	if _, err := mutationApplyRegistration(probe, gate).Handler(context.Background(), mutationProductInput()); err != nil {
		t.Fatal(err)
	}
	if gateCalls != 1 || probe.calls != 1 || probe.ctx.Value(gateContextKey{}) != "gate" {
		t.Fatal("gate bypassed")
	}
	blocked := func(context.Context, string, string, func(context.Context) error) error {
		return &mutation.ProductError{Code: "mutation.blocked", Message: "blocked"}
	}
	_, err := mutationApplyRegistration(probe, blocked).Handler(context.Background(), mutationProductInput())
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "mutation.blocked" || probe.calls != 1 {
		t.Fatal("blocked gate invoked kernel")
	}
	if _, err := mutationPreviewRegistration(probe).Handler(context.Background(), mutationProductInput()); err != nil {
		t.Fatal(err)
	}
	if gateCalls != 1 || probe.calls != 2 {
		t.Fatal("preview changed write gate")
	}
}

func TestMutationProductErrorMappingAndCancellation(t *testing.T) {
	path := "operations[0]"
	for _, test := range []struct {
		err   error
		code  string
		retry bool
	}{
		{fmt.Errorf("wrapped: %w", &mutation.ProductError{Code: "mutation.revision_conflict", Path: &path, Message: "stale", Details: map[string]any{"revision": "r1"}, Retryable: true}), "mutation.revision_conflict", true},
		{&formula.Error{Code: "formula.runtime", Path: &path, Message: "formula failed"}, "formula.runtime", false},
		{errors.New("private database path"), "mutation.internal.failed", true},
	} {
		for _, reg := range mutationProductRegistrations(&mutationProductProbe{err: test.err}) {
			_, err := reg.Handler(context.Background(), mutationProductInput())
			var public *productrpc.PublicError
			if !errors.As(err, &public) || public.Code != test.code || public.Retryable != test.retry || strings.Contains(public.Message, "private") {
				t.Fatalf("error %v", err)
			}
			if test.code != "mutation.internal.failed" && (public.Path == nil || *public.Path != path) {
				t.Fatal("public path lost")
			}
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		wrapped := fmt.Errorf("wrapped: %w", cause)
		for _, reg := range mutationProductRegistrations(&mutationProductProbe{err: wrapped}) {
			_, err := reg.Handler(context.Background(), mutationProductInput())
			if !errors.Is(err, cause) {
				t.Fatalf("context error swallowed: %v", err)
			}
			var public *productrpc.PublicError
			if errors.As(err, &public) {
				t.Fatal("context made public")
			}
		}
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		} else {
			cancel()
		}
		probe := &mutationProductProbe{}
		for _, reg := range mutationProductRegistrations(probe) {
			if _, err := reg.Handler(ctx, mutationProductInput()); !errors.Is(err, ctx.Err()) {
				t.Fatal(err)
			}
		}
		cancel()
		if probe.calls != 0 {
			t.Fatal("retired context invoked kernel")
		}
	}
	for index := 0; index < 2; index++ {
		ctx, cancel := context.WithCancel(context.Background())
		probe := &mutationProductProbe{after: cancel}
		_, err := mutationProductRegistrations(probe)[index].Handler(ctx, mutationProductInput())
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("late success survived cancellation")
		}
	}
}

func TestMutationProductCompactBudgetAndDepth(t *testing.T) {
	for _, reg := range mutationProductRegistrations(&mutationProductProbe{}) {
		base := mutationProductEdit(t, func(p map[string]any) { p["requestId"] = "" })
		size := maxMutationRequestBytes - len(base)
		for _, extra := range []int{0, 1} {
			raw := json.RawMessage(strings.Replace(string(base), `"requestId":""`, `"requestId":"`+strings.Repeat("a", size+extra)+`"`, 1))
			if err := reg.ValidateParams(raw); (err != nil) != (extra == 1) {
				t.Fatalf("compact budget +%d: %v", extra, err)
			}
			if extra == 0 {
				padded := append(append(json.RawMessage("  \n"), raw...), ' ')
				if _, err := reg.Handler(context.Background(), padded); err != nil {
					t.Fatalf("wire whitespace consumed domain budget: %v", err)
				}
			}
		}
		// Python sends decoded UTF-8, not the six-byte source escape spelling.
		escaped := json.RawMessage(strings.Replace(string(base), `"requestId":""`, `"requestId":"`+strings.Repeat(`\u00e9`, size/2)+strings.Repeat("a", size%2)+`"`, 1))
		if _, err := reg.Handler(context.Background(), escaped); err != nil {
			t.Fatalf("escaped Unicode budget: %v", err)
		}
		for _, depth := range []int{30, 33} {
			nested := strings.Repeat(`{"x":`, depth) + `0` + strings.Repeat(`}`, depth)
			raw := json.RawMessage(strings.Replace(string(mutationProductInput()), `"displayName":null`, `"displayName":`+nested, 1))
			if err := reg.ValidateParams(raw); (err != nil) != (depth == 33) {
				t.Fatalf("depth %d: %v", depth, err)
			}
		}
	}
	oversized := strings.Repeat(" ", maxMutationRequestBytes+1)
	_, err := decodeMutationRequest(strings.NewReader(oversized))
	var public *productrpc.PublicError
	if !errors.As(publicMutationProductError(err), &public) || public.Code != "mutation.request.invalid" {
		t.Fatal("REST budget error changed")
	}
}
