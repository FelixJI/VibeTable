package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relationpair"
)

func TestRelationInspectProductParams(t *testing.T) {
	valid := `{"tableId":"orders","fieldId":"fld_relation"}`
	input, err := decodeRelationInspectParams(json.RawMessage(valid))
	if err != nil || input.Limit != 100 {
		t.Fatalf("default page: %+v %v", input, err)
	}
	cursor := `{"pairId":"pair_test","endpoints":[{"tableId":"orders","fieldId":"fld_relation","schemaRevision":"1","dataRevision":0},{"tableId":"authors","fieldId":"fld_back","schemaRevision":"1","dataRevision":1}],"after":["",""],"done":[false,false],"incomplete":false}`
	continuation := `{"tableId":"orders","fieldId":"fld_relation","limit":200,"cursor":` + cursor + `}`
	if _, err := decodeRelationInspectParams(json.RawMessage(continuation)); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`null`, `[]`, `{}`, `{"tableId":null,"fieldId":"f"}`,
		`{"tableId":"t","fieldId":""}`, `{"tableId":"t","fieldId":"f","limit":0}`,
		`{"tableId":"t","fieldId":"f","limit":201}`, `{"tableId":"t","fieldId":"f","limit":null}`,
		`{"tableId":"t","fieldId":"f","limit":true}`, `{"tableId":"t","fieldId":"f","limit":1.5}`,
		`{"tableId":"t","fieldId":"f","password":"secret"}`, `{"tableId":"t","fieldId":"\ud800"}`,
		strings.Replace(continuation, `"done":[false,false]`, `"done":[false]`, 1),
		strings.Replace(continuation, `"done":[false,false]`, `"done":[false,false,false]`, 1),
		strings.Replace(continuation, `"done":[false,false]`, `"done":[false,null]`, 1),
		strings.Replace(continuation, `"incomplete":false`, `"incomplete":null`, 1),
		strings.Replace(continuation, `"schemaRevision":"1",`, ``, 1),
		strings.Replace(continuation, `"dataRevision":0`, `"dataRevision":-1`, 1),
		strings.Replace(continuation, `"dataRevision":0`, `"dataRevision":null`, 1),
		strings.Replace(continuation, `"dataRevision":0`, `"dataRevision":0,"password":"secret"`, 1),
	}
	for _, raw := range cases {
		if _, err := decodeRelationInspectParams(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestRelationInspectProductErrorsAndCancellation(t *testing.T) {
	for _, item := range []struct {
		source error
		code   string
	}{
		{relationpair.ErrRevisionChanged, "relation.inspect.revision_changed"},
		{relationpair.ErrInvalidRequest, "relation.inspect.invalid_request"},
		{errors.New("private database path"), "relation.inspect.storage_failed"},
	} {
		registration := relationInspectRegistration(func(context.Context, relationpair.Request) (relationpair.Report, error) {
			return relationpair.Report{}, item.source
		})
		result, err := registration.Handler(context.Background(), json.RawMessage(`{"tableId":"t","fieldId":"f"}`))
		var public *productrpc.PublicError
		if result != nil || !errors.As(err, &public) || public.Code != item.code || strings.Contains(public.Message, "private") {
			t.Fatalf("unsafe failure: %v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	registration := relationInspectRegistration(func(context.Context, relationpair.Request) (relationpair.Report, error) {
		calls++
		return relationpair.Report{}, nil
	})
	if _, err := registration.Handler(ctx, json.RawMessage(`{"tableId":"t","fieldId":"f"}`)); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-cancel: calls=%d err=%v", calls, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	registration = relationInspectRegistration(func(context.Context, relationpair.Request) (relationpair.Report, error) {
		cancel()
		return relationpair.Report{Complete: true}, nil
	})
	if result, err := registration.Handler(ctx, json.RawMessage(`{"tableId":"t","fieldId":"f"}`)); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late success after cancellation: %v %v", result, err)
	}
}

func unrelatedRelationInspectRegistration(t *testing.T) productrpc.Registration {
	t.Helper()
	return relationInspectRegistration(func(context.Context, relationpair.Request) (relationpair.Report, error) {
		t.Fatal("unrelated fixture must not invoke relation.inspectPair")
		return relationpair.Report{}, nil
	})
}
