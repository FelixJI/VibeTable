package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const pasteTestWire = "{\"scope\":\"workspace\",\"workspaceId\":\"11111111-1111-4111-8111-111111111111\",\"sessionEpoch\":7,\"operationId\":\"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb\",\"sequence\":0}"

func pasteTestCall(t *testing.T, dispatcher *productrpc.Dispatcher, method string, params any) productrpc.ResponseEnvelope {
	t.Helper()
	return pasteTestCallWithWire(t, dispatcher, method, params, pasteTestWire)
}

func pasteTestCallWithWire(t *testing.T, dispatcher *productrpc.Dispatcher, method string, params any, wire string) productrpc.ResponseEnvelope {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":\"paste\",\"method\":%q,\"wire\":%s,\"params\":%s}", method, wire, body)
	return dispatcher.Dispatch(context.Background(), []byte(request))
}

func TestPasteProductOwnsPlanAcrossCallsAndConsumesAfterCommit(t *testing.T) {
	fixture := newMutationHTTPFixture(t)
	source, err := queryschema.New(fixture.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	port := query.NewPort(fixture.pb, source)
	owner := newPasteOwner(schemaapi.New(fixture.pb).Describe, port, importvalue.New(fieldchange.NewCatalog(fixture.pb)), fixture.authority, fixture.runtime.CoordinateBusinessWrite, "11111111-1111-4111-8111-111111111111")
	dispatcher := relationWriteDispatcher(t, map[string]productrpc.Registration{"table.previewPaste": pastePreviewRegistration(owner), "table.applyPaste": pasteApplyRegistration(owner)})
	preview := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{
		"collection": fixture.request.TableID, "schemaRevision": fixture.request.SchemaRevision,
		"selection": map[string]any{"rowKeys": []any{}}, "startCell": map[string]any{"rowKey": nil, "column": fixture.field},
		"cells": []any{[]any{map[string]any{"rowIndex": 0, "columnIndex": 0, "column": nil, "rawValue": "pasted"}}},
	})
	if preview.Error != nil {
		t.Fatalf("preview error: %+v", preview.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(preview.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary["insertRows"] != 1 || plan.Token.Token == "" || len(plan.Rows) != 1 || plan.Rows[0].Changes[fixture.field]["after"] != "pasted" {
		t.Fatalf("plan = %+v", plan)
	}
	applyParams := map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "paste-test-commit"}
	applied := pasteTestCall(t, dispatcher, "table.applyPaste", applyParams)
	if applied.Error != nil {
		t.Fatalf("apply error: %+v", applied.Error)
	}
	var result pasteApplyResult
	if err := json.Unmarshal(applied.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "committed" || len(result.CreatedRowKeys) != 1 {
		t.Fatalf("apply = %+v", result)
	}
	records, err := port.ReadRows(context.Background(), fixture.request.TableID, []string{fmt.Sprint(result.CreatedRowKeys[0])})
	if err != nil || len(records) != 1 || records[0][fixture.field] != "pasted" {
		t.Fatalf("persisted rows = %+v, %v", records, err)
	}
	again := pasteTestCall(t, dispatcher, "table.applyPaste", applyParams)
	if again.Error == nil || again.Error.Code != productrpc.CodePaste || again.Error.Data["code"] != "paste_token_consumed" {
		t.Fatalf("second apply = %+v", again)
	}
}

type pasteUnknownOnce struct {
	actual   mutationKernel
	mu       sync.Mutex
	attempts int
}

func (kernel *pasteUnknownOnce) Preview(ctx context.Context, request mutation.Request) (mutation.PreviewResult, error) {
	return kernel.actual.Preview(ctx, request)
}
func (kernel *pasteUnknownOnce) Apply(ctx context.Context, request mutation.Request) (mutation.Receipt, error) {
	kernel.mu.Lock()
	kernel.attempts++
	attempt := kernel.attempts
	kernel.mu.Unlock()
	if attempt == 1 {
		return mutation.Receipt{}, errors.New("unknown transport outcome")
	}
	return kernel.actual.Apply(ctx, request)
}

func newPasteTestRuntime(t *testing.T) (mutationHTTPFixture, *pasteOwner, *productrpc.Dispatcher) {
	t.Helper()
	fixture := newMutationHTTPFixture(t)
	source, err := queryschema.New(fixture.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := newPasteOwner(schemaapi.New(fixture.pb).Describe, query.NewPort(fixture.pb, source), importvalue.New(fieldchange.NewCatalog(fixture.pb)), fixture.authority, fixture.runtime.CoordinateBusinessWrite, "11111111-1111-4111-8111-111111111111")
	dispatcher := relationWriteDispatcher(t, map[string]productrpc.Registration{"table.previewPaste": pastePreviewRegistration(owner), "table.applyPaste": pasteApplyRegistration(owner)})
	return fixture, owner, dispatcher
}
func pasteTestPreview(t *testing.T, fixture mutationHTTPFixture, dispatcher *productrpc.Dispatcher) pastePlan {
	t.Helper()
	response := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{"collection": fixture.request.TableID, "schemaRevision": fixture.request.SchemaRevision,
		"selection": map[string]any{"rowKeys": []any{}}, "startCell": map[string]any{"rowKey": nil, "column": fixture.field},
		"cells": []any{[]any{map[string]any{"rowIndex": 0, "columnIndex": 0, "column": nil, "rawValue": "second"}}}})
	if response.Error != nil {
		t.Fatalf("preview: %+v", response.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(response.Result, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}
func pasteAssertCode(t *testing.T, response productrpc.ResponseEnvelope, code string) {
	t.Helper()
	if response.Error == nil || response.Error.Code != productrpc.CodePaste || response.Error.Data["code"] != code {
		t.Fatalf("response = %+v; want %s", response, code)
	}
}
func TestPasteProductPendingBindsKeyAndRetriesOnlyUntilCommitted(t *testing.T) {
	fixture, owner, dispatcher := newPasteTestRuntime(t)
	probe := &pasteUnknownOnce{actual: fixture.authority}
	owner.kernel = probe
	plan := pasteTestPreview(t, fixture, dispatcher)
	params := map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "same-key"}
	pending := pasteTestCall(t, dispatcher, "table.applyPaste", params)
	if pending.Error != nil {
		t.Fatalf("pending error: %+v", pending.Error)
	}
	var pendingResult pasteApplyResult
	if err := json.Unmarshal(pending.Result, &pendingResult); err != nil {
		t.Fatal(err)
	}
	if pendingResult.Outcome != "pending" {
		t.Fatalf("pending = %+v", pendingResult)
	}
	mismatch := map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "different-key"}
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", mismatch), "paste_idempotency_mismatch")
	committed := pasteTestCall(t, dispatcher, "table.applyPaste", params)
	if committed.Error != nil {
		t.Fatalf("commit error: %+v", committed.Error)
	}
	var result pasteApplyResult
	if err := json.Unmarshal(committed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "committed" || len(result.CreatedRowKeys) != 1 {
		t.Fatalf("commit = %+v", result)
	}
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", params), "paste_token_consumed")
	if probe.attempts != 2 {
		t.Fatalf("apply attempts = %d", probe.attempts)
	}
}
func TestPasteProductRejectsExpiredSchemaAndForeignCollection(t *testing.T) {
	fixture, owner, dispatcher := newPasteTestRuntime(t)
	params := func(token string) map[string]any {
		return map[string]any{"collection": fixture.request.TableID, "token": token, "idempotencyKey": "key"}
	}
	plan := pasteTestPreview(t, fixture, dispatcher)
	owner.now = func() time.Time { return time.Unix(int64(plan.Token.ExpiresAt)+1, 0) }
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", params(plan.Token.Token)), "paste_token_expired")
	owner.now = time.Now
	plan = pasteTestPreview(t, fixture, dispatcher)
	owner.mu.Lock()
	owner.plans[plan.Token.Token].schemaRevision = "schema_older"
	owner.mu.Unlock()
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", params(plan.Token.Token)), "schema_mismatch")
	plan = pasteTestPreview(t, fixture, dispatcher)
	originalSchema := owner.schema
	owner.schema = func(ctx context.Context, collection string) (schemaexecution.Table, error) {
		if collection == "other-table" {
			table, err := originalSchema(ctx, fixture.request.TableID)
			table.Snapshot.TableID = collection
			return table, err
		}
		return originalSchema(ctx, collection)
	}
	other := map[string]any{"collection": "other-table", "token": plan.Token.Token, "idempotencyKey": "key"}
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", other), "paste_token_invalid")
}
func TestPasteProductConcurrentApplyCommitsOnce(t *testing.T) {
	fixture, _, dispatcher := newPasteTestRuntime(t)
	plan := pasteTestPreview(t, fixture, dispatcher)
	params := map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "race-key"}
	responses := make([]productrpc.ResponseEnvelope, 2)
	var group sync.WaitGroup
	for index := range responses {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			responses[index] = pasteTestCall(t, dispatcher, "table.applyPaste", params)
		}(index)
	}
	group.Wait()
	committed, consumed := 0, 0
	for _, response := range responses {
		if response.Error != nil {
			if response.Error.Code == productrpc.CodePaste && response.Error.Data["code"] == "paste_token_consumed" {
				consumed++
			} else {
				t.Fatalf("unexpected error: %+v", response.Error)
			}
			continue
		}
		var result pasteApplyResult
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Outcome == "committed" {
			committed++
		}
	}
	if committed != 1 || consumed != 1 {
		t.Fatalf("outcomes: committed=%d consumed=%d responses=%+v", committed, consumed, responses)
	}
}

func TestPasteProductUpdateUsesLiveDigestAndReturnsConflict(t *testing.T) {
	fixture, _, dispatcher := newPasteTestRuntime(t)
	inserted, err := fixture.authority.kernel.Apply(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if inserted.Status != mutation.StatusApplied {
		t.Fatalf("insert: %+v", inserted)
	}
	rowID := *fixture.request.Operations[0].RecordID
	preview := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{
		"collection": fixture.request.TableID, "schemaRevision": fixture.request.SchemaRevision,
		"selection": map[string]any{"rowKeys": []any{rowID}},
		"startCell": map[string]any{"rowKey": rowID, "column": fixture.field},
		"cells":     []any{[]any{map[string]any{"rowIndex": 0, "columnIndex": 0, "rawValue": "updated"}}}})
	if preview.Error != nil {
		t.Fatalf("preview: %+v", preview.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(preview.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary["updateRows"] != 1 || plan.Rows[0].ExpectedDateUpdated == nil || *plan.Rows[0].ExpectedDateUpdated == "" {
		t.Fatalf("guarded update = %+v", plan)
	}
	changed := fixture.request
	changed.RequestID = "another-request"
	changed.IdempotencyKey = "another-key"
	changed.Operations = []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &rowID, Values: map[string]any{fixture.field: "concurrent"}}}
	if _, err := fixture.authority.kernel.Apply(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	response := pasteTestCall(t, dispatcher, "table.applyPaste", map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "paste-conflict"})
	if response.Error != nil {
		t.Fatalf("conflict error: %+v", response.Error)
	}
	var result pasteApplyResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "conflict" || len(result.Conflicts) != 1 || result.Conflicts[0].RowKey != rowID {
		t.Fatalf("conflict = %+v", result)
	}
}

func TestPasteProductParamsMatchFrozenPythonDTO(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "contract", "fixtures", "paste-owner-params-parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string          `json:"name"`
		Method     string          `json:"method"`
		Params     json.RawMessage `json:"params"`
		Normalized json.RawMessage `json:"normalized"`
		Accepted   bool            `json:"accepted"`
	}
	if err := json.Unmarshal(content, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 19 {
		t.Fatalf("frozen cases = %d", len(cases))
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			var actual any
			var decodeErr error
			switch test.Method {
			case "table.previewPaste":
				actual, decodeErr = decodePastePreview(test.Params)
			case "table.applyPaste":
				actual, decodeErr = decodePasteApply(test.Params)
			default:
				t.Fatalf("unknown method %s", test.Method)
			}
			if (decodeErr == nil) != test.Accepted {
				t.Fatalf("accepted=%v, error=%v, want=%v", decodeErr == nil, decodeErr, test.Accepted)
			}
			if !test.Accepted {
				return
			}
			raw, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(test.Normalized, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("normalized = %s; want %s", raw, test.Normalized)
			}
		})
	}
}

func TestPasteProductInsertRetainsNormalizedNullCellChange(t *testing.T) {
	fixture, owner, dispatcher := newPasteTestRuntime(t)
	number := createSchemaProductField(t, fixture.pb, fixture.request.TableID, v2.LogicalNumber, "Amount", "paste-empty-number")
	field := number.Definition.Identity.PhysicalName
	preview := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{
		"collection": fixture.request.TableID, "schemaRevision": number.SchemaRevision,
		"selection": map[string]any{"rowKeys": []any{}},
		"startCell": map[string]any{"rowKey": nil, "column": fixture.field},
		"cells":     []any{[]any{map[string]any{"rowIndex": 0, "columnIndex": 0, "column": field, "rawValue": ""}}}})
	if preview.Error != nil {
		t.Fatalf("preview: %+v", preview.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(preview.Result, &plan); err != nil {
		t.Fatal(err)
	}
	change, exists := plan.Rows[0].Changes[field]
	if !exists || change["before"] != nil || change["after"] != nil {
		t.Fatalf("normalized insert must retain before/after null: %+v", plan.Rows[0].Changes)
	}
	owner.mu.Lock()
	raw := owner.plans[plan.Token.Token].rawRows[0][field]
	owner.mu.Unlock()
	if raw != "" {
		t.Fatalf("raw authoritative cell = %v", raw)
	}
}

func TestPasteProductNoOpUpdateComparesNormalizedJSONValues(t *testing.T) {
	fixture, _, dispatcher := newPasteTestRuntime(t)
	number := createSchemaProductField(t, fixture.pb, fixture.request.TableID, v2.LogicalNumber, "Amount", "paste-noop-number")
	nested := createSchemaProductField(t, fixture.pb, fixture.request.TableID, v2.LogicalJSON, "Payload", "paste-noop-json")
	amount := number.Definition.Identity.PhysicalName
	payload := nested.Definition.Identity.PhysicalName
	recordID := *fixture.request.Operations[0].RecordID
	inserted := fixture.request
	inserted.SchemaRevision = nested.SchemaRevision
	inserted.Operations = []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{
		fixture.field: "same", amount: 0, payload: map[string]any{"enabled": false, "items": []any{0, nil}},
	}}}
	if _, err := fixture.authority.kernel.Apply(context.Background(), inserted); err != nil {
		t.Fatal(err)
	}
	response := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{
		"collection": fixture.request.TableID, "schemaRevision": nested.SchemaRevision,
		"selection": map[string]any{"rowKeys": []any{recordID}},
		"startCell": map[string]any{"rowKey": recordID, "column": fixture.field},
		"cells": []any{[]any{
			map[string]any{"rowIndex": 0, "columnIndex": 0, "column": fixture.field, "rawValue": "same"},
			map[string]any{"rowIndex": 0, "columnIndex": 0, "column": amount, "rawValue": "0"},
			map[string]any{"rowIndex": 0, "columnIndex": 0, "column": payload, "rawValue": "{\"enabled\":false,\"items\":[0,null]}"},
		}},
	})
	if response.Error != nil {
		t.Fatalf("preview: %+v", response.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(response.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Rows[0].Changes) != 0 || plan.Summary["skipRows"] != 1 || plan.Summary["updateRows"] != 0 {
		t.Fatalf("same persisted JSON update should be skipped: %+v", plan)
	}
}

func TestPasteJSONEquivalentKeepsTypesAndNumericValue(t *testing.T) {
	cases := []struct {
		left, right any
		equal       bool
	}{
		{left: 0, right: float64(0), equal: true},
		{left: map[string]any{"enabled": false, "items": []any{int64(0), nil}}, right: map[string]any{"items": []any{float64(0), nil}, "enabled": false}, equal: true},
		{left: false, right: 0, equal: false},
		{left: map[string]any{"enabled": false}, right: map[string]any{"enabled": 0}, equal: false},
	}
	for _, test := range cases {
		equal, err := pasteJSONEquivalent(test.left, test.right)
		if err != nil || equal != test.equal {
			t.Fatalf("compare %v and %v = %v, %v; want %v", test.left, test.right, equal, err, test.equal)
		}
	}
	if _, err := pasteJSONEquivalent(make(chan int), 0); err == nil {
		t.Fatal("unsupported JSON value must return an error")
	}
}

type pasteCommitThenLoseReceipt struct {
	actual mutationKernel
	calls  int
	first  mutation.Receipt
	replay mutation.Receipt
}

func (kernel *pasteCommitThenLoseReceipt) Preview(ctx context.Context, request mutation.Request) (mutation.PreviewResult, error) {
	return kernel.actual.Preview(ctx, request)
}

func (kernel *pasteCommitThenLoseReceipt) Apply(ctx context.Context, request mutation.Request) (mutation.Receipt, error) {
	receipt, err := kernel.actual.Apply(ctx, request)
	kernel.calls++
	if kernel.calls == 1 {
		if err != nil {
			return receipt, err
		}
		kernel.first = receipt
		return mutation.Receipt{}, errors.New("response lost after kernel commit")
	}
	kernel.replay = receipt
	return receipt, err
}

func TestPasteProductCommittedButReplyLostReplaysSameRequest(t *testing.T) {
	fixture, owner, dispatcher := newPasteTestRuntime(t)
	probe := &pasteCommitThenLoseReceipt{actual: fixture.authority}
	owner.kernel = probe
	plan := pasteTestPreview(t, fixture, dispatcher)
	params := map[string]any{"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "committed-before-lost-response"}
	pending := pasteTestCall(t, dispatcher, "table.applyPaste", params)
	if pending.Error != nil {
		t.Fatalf("pending error: %+v", pending.Error)
	}
	var pendingResult pasteApplyResult
	if err := json.Unmarshal(pending.Result, &pendingResult); err != nil {
		t.Fatal(err)
	}
	if pendingResult.Outcome != "pending" || probe.first.Status != mutation.StatusApplied || len(probe.first.AffectedRows) != 1 {
		t.Fatalf("first kernel commit and lost response: pending=%+v receipt=%+v", pendingResult, probe.first)
	}
	retryWire := "{\"scope\":\"workspace\",\"workspaceId\":\"11111111-1111-4111-8111-111111111111\",\"sessionEpoch\":7,\"operationId\":\"cccccccc-cccc-4ccc-8ccc-cccccccccccc\",\"sequence\":0}"
	retried := pasteTestCallWithWire(t, dispatcher, "table.applyPaste", params, retryWire)
	if retried.Error != nil {
		t.Fatalf("retry error: %+v", retried.Error)
	}
	var result pasteApplyResult
	if err := json.Unmarshal(retried.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "committed" || probe.replay.Status != mutation.StatusReplayed || len(result.CreatedRowKeys) != 1 {
		t.Fatalf("retry result=%+v receipt=%+v calls=%d applies=%d", result, probe.replay, probe.calls, fixture.authority.applies)
	}
	if len(probe.replay.AffectedRows) != 1 || probe.replay.AffectedRows[0].RecordID != probe.first.AffectedRows[0].RecordID || probe.calls != 2 {
		t.Fatalf("kernel receipts first=%+v replay=%+v calls=%d", probe.first, probe.replay, probe.calls)
	}
	records, err := fixture.pb.FindRecordsByFilter(fixture.definition.PhysicalName, "", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("business records=%d, err=%v", len(records), err)
	}
	pasteAssertCode(t, pasteTestCall(t, dispatcher, "table.applyPaste", params), "paste_token_consumed")
}

func TestPasteProductArraySelectionUsesEmptySelection(t *testing.T) {
	fixture, _, dispatcher := newPasteTestRuntime(t)
	response := pasteTestCall(t, dispatcher, "table.previewPaste", map[string]any{
		"collection": fixture.request.TableID, "schemaRevision": fixture.request.SchemaRevision,
		"selection": []any{"ignored"}, "startCell": map[string]any{"rowKey": nil, "column": fixture.field},
		"cells": []any{[]any{map[string]any{"rowIndex": 0, "columnIndex": 0, "rawValue": "array-selection"}}},
	})
	if response.Error != nil {
		t.Fatalf("preview error: %+v", response.Error)
	}
	var plan pastePlan
	if err := json.Unmarshal(response.Result, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary["insertRows"] != 1 || len(plan.Rows) != 1 || plan.Rows[0].Kind != "insert" {
		t.Fatalf("array selection plan = %+v", plan)
	}
	applied := pasteTestCall(t, dispatcher, "table.applyPaste", map[string]any{
		"collection": fixture.request.TableID, "token": plan.Token.Token, "idempotencyKey": "array-selection",
	})
	if applied.Error != nil {
		t.Fatalf("apply error: %+v", applied.Error)
	}
	var result pasteApplyResult
	if err := json.Unmarshal(applied.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "committed" || len(result.CreatedRowKeys) != 1 {
		t.Fatalf("apply = %+v", result)
	}
}
