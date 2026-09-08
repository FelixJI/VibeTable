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

type snapshotValidationProbe struct {
	calls    int
	snapshot query.QuerySnapshot
	current  *query.TableQuery
	result   query.SnapshotValidation
	err      error
	cancel   context.CancelFunc
}

func (p *snapshotValidationProbe) ValidateSnapshot(_ context.Context, snapshot query.QuerySnapshot, current *query.TableQuery) (query.SnapshotValidation, error) {
	p.calls++
	p.snapshot, p.current = snapshot, current
	if p.cancel != nil {
		p.cancel()
	}
	return p.result, p.err
}

func TestQueryValidateSnapshotProductParams(t *testing.T) {
	registration := queryValidateSnapshotRegistration(nil)
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"snapshot":null}`, `{"snapshot":[]}`, `{"snapshot":true}`,
		`{"snapshot":{},"currentQuery":null}`, `{"snapshot":{},"currentQuery":[]}`,
		`{"snapshot":{},"unknown":false}`, `{"snapshot":{"nested":{"password":"x"}}}`,
		`{"snapshot":{"table":"\ud800"}}`, "{\"snapshot\":{\"table\":\"\xff\"}}",
		`{"snapshot":{}} {}`, `{"snapshot":{},"currentQuery":{"filters":[{"value":{"accessToken":"x"}}]}}`,
	} {
		if err := registration.ValidateParams(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{`{"snapshot":{}}`, `{"snapshot":{},"currentQuery":{}}`, `{"snapshot":{"extension":false}}`} {
		if err := registration.ValidateParams(json.RawMessage(raw)); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
	// Both budgets count compact UTF-8 bytes, not source whitespace or escaped spelling.
	prefix, suffix := `{"snapshot":{"table":"`, `"}}`
	size := maxQueryRequestBytes - len(prefix) - len(suffix)
	for _, extra := range []int{0, 1} {
		raw := json.RawMessage(prefix + strings.Repeat("a", size+extra) + suffix)
		if err := registration.ValidateParams(raw); (err != nil) != (extra == 1) {
			t.Fatalf("budget +%d: %v", extra, err)
		}
	}
	raw := json.RawMessage("  " + prefix + strings.Repeat(`\u00e9`, size/2) + strings.Repeat("a", size%2) + suffix + "  ")
	if err := registration.ValidateParams(raw); err != nil {
		t.Fatalf("compact Unicode budget: %v", err)
	}
	for _, depth := range []int{31, 32} {
		raw := json.RawMessage(`{"snapshot":` + strings.Repeat(`{"x":`, depth) + `0` + strings.Repeat(`}`, depth) + `}`)
		if err := registration.ValidateParams(raw); (err != nil) != (depth == 32) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}

func TestQueryValidateSnapshotProductTypedBoundary(t *testing.T) {
	for _, raw := range []string{
		`{"snapshot":{"extension":false}}`, `{"snapshot":{"dataRevision":"seven"}}`,
		`{"snapshot":{},"currentQuery":{"custom":"中文"}}`, `{"snapshot":{},"currentQuery":{"limit":"50"}}`,
	} {
		probe := &snapshotValidationProbe{}
		registration := queryValidateSnapshotRegistration(probe)
		if err := registration.ValidateParams(json.RawMessage(raw)); err != nil {
			t.Fatalf("outer DTO: %v", err)
		}
		result, err := registration.Handler(context.Background(), json.RawMessage(raw))
		var public *productrpc.PublicError
		if !errors.As(err, &public) || public.Code != "query.request.invalid" || public.Message != "query request body is invalid" || result != nil || probe.calls != 0 {
			t.Fatalf("typed rejection: %v %v calls=%d", result, err, probe.calls)
		}
	}
}

func TestQueryValidateSnapshotProductResultAndInputs(t *testing.T) {
	for _, reason := range []string{"", "query_changed", "schema_changed", "application_write"} {
		t.Run(reason, func(t *testing.T) {
			want := query.SnapshotValidation{Valid: reason == "", Reason: reason, CurrentDataRevision: 7, CurrentSchemaRevision: "schema-中文"}
			probe := &snapshotValidationProbe{result: want}
			registration := queryValidateSnapshotRegistration(probe)
			raw := json.RawMessage(`{"snapshot":{"snapshotId":"scripted","digest":"opaque","databaseId":"db","table":"中文👩🏽‍💻","schemaRevision":"s","dataRevision":7,"normalizedQuery":{"offset":0,"limit":50}},"currentQuery":{"keyword":"Café 中文","filters":[{"field":"title","operator":"eq","value":false}],"offset":0,"limit":50}}`)
			result, err := registration.Handler(context.Background(), raw)
			if err != nil || !reflect.DeepEqual(result, want) || probe.calls != 1 {
				t.Fatalf("result: %#v %v calls=%d", result, err, probe.calls)
			}
			if probe.snapshot.Table != "中文👩🏽‍💻" || probe.snapshot.DataRevision != 7 || probe.current == nil || probe.current.Keyword != "Café 中文" {
				t.Fatalf("inputs: %#v %#v", probe.snapshot, probe.current)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), `"reason"`) != (reason != "") {
				t.Fatalf("reason omission: %s", encoded)
			}
		})
	}
	for _, current := range []string{"", `,"currentQuery":{}`} {
		probe := &snapshotValidationProbe{}
		_, err := queryValidateSnapshotRegistration(probe).Handler(context.Background(), json.RawMessage(`{"snapshot":{}`+current+`}`))
		if err != nil || probe.calls != 1 || (probe.current == nil) != (current == "") {
			t.Fatalf("optional current: %v %#v", err, probe.current)
		}
		if probe.current != nil && probe.current.Limit != 100 {
			t.Fatalf("REST default: %#v", probe.current)
		}
	}
}

func TestQueryValidateSnapshotProductErrorsAndCancellation(t *testing.T) {
	raw := json.RawMessage(`{"snapshot":{}}`)
	domain := &query.ProductError{Code: "query.snapshot.invalid", Path: "snapshotId", Message: "query snapshot id is invalid", Details: map[string]any{}, Retryable: false}
	for _, source := range []error{domain, errors.New("private database detail"), context.DeadlineExceeded} {
		probe := &snapshotValidationProbe{err: source}
		result, err := queryValidateSnapshotRegistration(probe).Handler(context.Background(), raw)
		if result != nil || probe.calls != 1 {
			t.Fatalf("result leaked: %v", result)
		}
		if errors.Is(source, context.DeadlineExceeded) {
			if !errors.Is(err, source) {
				t.Fatal(err)
			}
			continue
		}
		var public *productrpc.PublicError
		if !errors.As(err, &public) {
			t.Fatalf("public error: %v", err)
		}
		if source == domain {
			if public.Code != domain.Code || public.Path == nil || *public.Path != domain.Path || public.Message != domain.Message || !reflect.DeepEqual(public.Details, domain.Details) || public.Retryable != domain.Retryable {
				t.Fatalf("domain fields: %#v", public)
			}
		} else if public.Code != "query.internal.failed" || public.Message != "query operation failed" {
			t.Fatalf("private error: %#v", public)
		}
	}
	for _, before := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		probe := &snapshotValidationProbe{result: query.SnapshotValidation{Valid: true}, cancel: cancel}
		if before {
			cancel()
		}
		result, err := queryValidateSnapshotRegistration(probe).Handler(ctx, raw)
		cancel()
		wantCalls := 1
		if before {
			wantCalls = 0
		}
		if !errors.Is(err, context.Canceled) || result != nil || probe.calls != wantCalls {
			t.Fatalf("cancel before=%v: %v %v calls=%d", before, result, err, probe.calls)
		}
	}
}
