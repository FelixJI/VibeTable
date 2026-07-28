package protocolv2

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

func TestDispatcherFailsClosedForOldEpochSequenceAndUnknownFields(t *testing.T) {
	dispatcher := New()
	dispatcher.BindSession(Session{WorkspaceID: "11111111-1111-4111-8111-111111111111", Epoch: 8, Sequence: 11})
	calls := 0
	dispatcher.Register("snapshot.list", WorkspaceScope, func(
		context.Context,
		json.RawMessage,
		json.RawMessage,
	) (any, error) {
		calls++
		return map[string]any{"snapshots": []any{}}, nil
	})
	request := func(epoch, sequence int, extra string) []byte {
		return []byte(`{
			"jsonrpc":"2.0",
			"id":"request-1",
			"method":"snapshot.list",
			"wire":{
				"scope":"workspace",
				"workspaceId":"11111111-1111-4111-8111-111111111111",
				"sessionEpoch":` + strconv.Itoa(epoch) + `,
				"operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				"sequence":` + strconv.Itoa(sequence) + `
			},
			"params":{}` + extra + `
		}`)
	}
	if _, err := dispatcher.Dispatch(context.Background(), request(7, 12, "")); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), request(8, 11, "")); !errors.Is(err, ErrSequenceStale) {
		t.Fatalf("old sequence accepted: %v", err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), request(8, 12, `,"unknown":true`)); err == nil {
		t.Fatal("unknown top-level field accepted")
	}
	if _, err := dispatcher.Dispatch(context.Background(), request(8, 12, "")); err != nil || calls != 1 {
		t.Fatalf("current scoped request failed: calls=%d err=%v", calls, err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), request(8, 12, "")); !errors.Is(
		err,
		ErrSequenceStale,
	) {
		t.Fatalf("duplicate sequence accepted: %v", err)
	}
}

func TestGlobalMethodRejectsWorkspaceScope(t *testing.T) {
	dispatcher := New()
	dispatcher.Register("workspace.list", GlobalScope, func(
		context.Context,
		json.RawMessage,
		json.RawMessage,
	) (any, error) {
		return nil, nil
	})
	raw := []byte(`{
		"jsonrpc":"2.0","id":"x","method":"workspace.list",
		"wire":{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":1,"operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":1},
		"params":{}
	}`)
	if _, err := dispatcher.Dispatch(context.Background(), raw); !errors.Is(err, ErrScopeRequired) {
		t.Fatalf("workspace scope accepted for global method: %v", err)
	}
}

func TestDispatcherPublishesSequenceBeforeAcknowledgingSuccess(t *testing.T) {
	dispatcher := New()
	dispatcher.BindSession(Session{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		Epoch:       2,
	})
	persistErr := errors.New("sequence store unavailable")
	dispatcher.SetSequenceCommit(func(context.Context, Session) error {
		return persistErr
	})
	dispatcher.Register("snapshot.list", WorkspaceScope, func(
		context.Context,
		json.RawMessage,
		json.RawMessage,
	) (any, error) {
		return map[string]any{"snapshots": []any{}}, nil
	})
	raw := []byte(`{
		"jsonrpc":"2.0","id":"request-2","method":"snapshot.list",
		"wire":{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":2,"operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":1},
		"params":{}
	}`)
	if _, err := dispatcher.Dispatch(
		context.Background(),
		raw,
	); !errors.Is(err, persistErr) {
		t.Fatalf("sequence persistence error = %v", err)
	}
	dispatcher.SetSequenceCommit(nil)
	if _, err := dispatcher.Dispatch(context.Background(), raw); err != nil {
		t.Fatalf("unacknowledged sequence was consumed: %v", err)
	}
}

func TestDispatcherEnvelopeEchoesWireAndEnumeratesOnlyRegisteredMethods(t *testing.T) {
	dispatcher := New()
	dispatcher.BindSession(Session{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		Epoch:       2,
	})
	dispatcher.Register("snapshot.list", WorkspaceScope, func(
		context.Context,
		json.RawMessage,
		json.RawMessage,
	) (any, error) {
		return map[string]any{"snapshots": []any{}}, nil
	})
	raw := []byte(`{
		"jsonrpc":"2.0","id":"request-3","method":"snapshot.list",
		"wire":{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":2,"operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":1},
		"params":{},"unknown":true
	}`)
	response := dispatcher.DispatchEnvelope(context.Background(), raw)
	if response.ID != "request-3" ||
		response.Error == nil ||
		response.Error.Code != "workspace.request_invalid" ||
		string(response.Wire) == "null" {
		t.Fatalf("error envelope = %#v", response)
	}
	methods := dispatcher.Methods()
	if len(methods) != 1 ||
		methods[0].Name != "snapshot.list" ||
		methods[0].Scope != WorkspaceScope {
		t.Fatalf("registered methods = %#v", methods)
	}
}

func TestDispatcherRecoversAuthorityReceiptAcrossCentralCommitKillWindow(t *testing.T) {
	const (
		workspaceID = "11111111-1111-4111-8111-111111111111"
		operationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	var (
		authorityReceipt OperationReceipt
		authorityFound   bool
		handlerCalls     int
		centralCommits   int
	)
	loadAuthority := func(
		_ context.Context,
		gotWorkspaceID string,
		gotOperationID string,
	) (OperationReceipt, bool, error) {
		if gotWorkspaceID != workspaceID || gotOperationID != operationID {
			return OperationReceipt{}, false, nil
		}
		return authorityReceipt, authorityFound, nil
	}
	register := func(dispatcher *Dispatcher) {
		dispatcher.Register("fileHistory.import", WorkspaceScope, func(
			ctx context.Context,
			_ json.RawMessage,
			_ json.RawMessage,
		) (any, error) {
			handlerCalls++
			result := map[string]any{
				"documentId": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				"revisionId": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			}
			receipt, err := BuildContextOperationReceipt(ctx, result)
			if err != nil {
				return nil, err
			}
			// This assignment models the authority transaction committing its
			// visible side effect and exact receipt atomically.
			authorityReceipt = receipt
			authorityFound = true
			return result, nil
		})
	}
	request := func(params string) []byte {
		return []byte(`{
			"jsonrpc":"2.0","id":"request-4","method":"fileHistory.import",
			"wire":{"scope":"workspace","workspaceId":"` + workspaceID + `",
				"sessionEpoch":7,"operationId":"` + operationID + `","sequence":1},
			"params":` + params + `
		}`)
	}

	beforeCrash := New()
	beforeCrash.BindSession(Session{WorkspaceID: workspaceID, Epoch: 7})
	beforeCrash.SetAuthorityOperationReceiptStore(loadAuthority)
	killWindow := errors.New("simulated process death before central receipt")
	beforeCrash.SetOperationReceiptStore(
		nil,
		func(context.Context, Session, OperationReceipt) error {
			return killWindow
		},
	)
	register(beforeCrash)
	if _, err := beforeCrash.Dispatch(
		context.Background(),
		request(`{"source":"grant"}`),
	); !errors.Is(err, killWindow) {
		t.Fatalf("kill-window error = %v", err)
	}
	if handlerCalls != 1 || !authorityFound {
		t.Fatalf(
			"authority publication calls=%d receipt=%v",
			handlerCalls,
			authorityFound,
		)
	}

	afterRestart := New()
	afterRestart.BindSession(Session{WorkspaceID: workspaceID, Epoch: 7})
	afterRestart.SetAuthorityOperationReceiptStore(loadAuthority)
	afterRestart.SetOperationReceiptStore(
		func(context.Context, string, string) (OperationReceipt, bool, error) {
			return OperationReceipt{}, false, nil
		},
		func(
			_ context.Context,
			session Session,
			receipt OperationReceipt,
		) error {
			centralCommits++
			if session.Sequence != 1 || receipt.RequestHash == "" {
				t.Fatalf(
					"central backfill session=%#v receipt=%#v",
					session,
					receipt,
				)
			}
			return nil
		},
	)
	register(afterRestart)
	replayed, err := afterRestart.Dispatch(
		context.Background(),
		request(`{"source":"grant"}`),
	)
	if err != nil {
		t.Fatalf("authority replay failed: %v", err)
	}
	raw, err := json.Marshal(replayed)
	if err != nil || string(raw) != string(authorityReceipt.Result) {
		t.Fatalf(
			"replayed result=%s authority=%s err=%v",
			raw,
			authorityReceipt.Result,
			err,
		)
	}
	if handlerCalls != 1 || centralCommits != 1 {
		t.Fatalf(
			"replay duplicated side effect: calls=%d backfills=%d",
			handlerCalls,
			centralCommits,
		)
	}
	if _, err := afterRestart.Dispatch(
		context.Background(),
		request(`{"source":"different"}`),
	); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed request fingerprint accepted: %v", err)
	}
}

func TestGlobalAuthorityReceiptFingerprintDoesNotBindWorkspaceSessionEpoch(
	t *testing.T,
) {
	const workspaceID = "11111111-1111-4111-8111-111111111111"
	var authority OperationReceipt
	first := New()
	first.BindSession(Session{WorkspaceID: workspaceID, Epoch: 17})
	first.SetOperationReceiptStore(
		nil,
		func(context.Context, Session, OperationReceipt) error {
			return errors.New("simulated central receipt loss")
		},
	)
	first.Register("snapshot.inspectPackage", GlobalScope, func(
		ctx context.Context,
		_ json.RawMessage,
		_ json.RawMessage,
	) (any, error) {
		result := map[string]any{"planId": "plan"}
		var err error
		authority, err = BuildContextOperationReceipt(ctx, result)
		return result, err
	})
	raw := []byte(`{
		"jsonrpc":"2.0","id":"global-1","method":"snapshot.inspectPackage",
		"wire":{"scope":"global","operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"params":{"pathGrant":"grant"}
	}`)
	if _, err := first.Dispatch(context.Background(), raw); err == nil {
		t.Fatal("central commit fault was not observed")
	}
	second := New()
	// A global operation may be replayed while a different workspace session
	// is active; its request fingerprint intentionally binds epoch zero.
	second.BindSession(Session{WorkspaceID: workspaceID, Epoch: 99})
	second.SetAuthorityOperationReceiptStore(func(
		context.Context,
		string,
		string,
	) (OperationReceipt, bool, error) {
		return authority, true, nil
	})
	second.Register("snapshot.inspectPackage", GlobalScope, func(
		context.Context,
		json.RawMessage,
		json.RawMessage,
	) (any, error) {
		t.Fatal("global authority replay called handler")
		return nil, nil
	})
	if _, err := second.Dispatch(context.Background(), raw); err != nil {
		t.Fatalf("global authority replay = %v", err)
	}
}
