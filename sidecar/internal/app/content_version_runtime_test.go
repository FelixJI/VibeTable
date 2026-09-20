package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contentmetadata "github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

func TestContentVersionHTTPRealRuntimeLifecycleAndDurableResults(t *testing.T) {
	runtime, ledger, mux, table, item, pb := historyRestoreProductFixture(t)
	call := func(method string, p map[string]any) productrpc.ResponseEnvelope {
		t.Helper()
		p["collection"] = table
		p["itemId"] = item
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return restoreProductRequest(t, mux, method, string(raw), schemaListWire, context.Background())
	}
	success := func(method string, p map[string]any) (json.RawMessage, map[string]any) {
		t.Helper()
		r := call(method, p)
		if r.Error != nil {
			t.Fatalf("%s: %+v", method, r.Error)
		}
		var value map[string]any
		if err := json.Unmarshal(r.Result, &value); err != nil {
			t.Fatal(err)
		}
		return r.Result, value
	}
	create := map[string]any{"operationId": "version-create-original", "key": "named", "name": "Named first"}
	original, entry := success("version.create", create)
	_, listed := success("version.list", map[string]any{})
	if len(listed["versions"].([]any)) != 1 {
		t.Fatal(listed)
	}
	replay, _ := success("version.create", create)
	if string(original) != string(replay) {
		t.Fatalf("create replay changed: %s / %s", original, replay)
	}
	changed := map[string]any{"operationId": "version-create-original", "key": "other", "name": "Named first"}
	if r := call("version.create", changed); r.Error == nil || r.Error.Data["code"] != "version_idempotency_conflict" {
		t.Fatalf("changed payload: %+v", r)
	}
	id := entry["id"].(string)
	// This direct fixture edit simulates an intervening record edit. The restore itself
	// crosses the real Runtime, audit token, mutation kernel and PocketBase transaction.
	meta, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:id}", dbx.Params{"id": table})
	if err != nil {
		t.Fatal(err)
	}
	record, err := pb.FindRecordById(meta.GetString("collection_id"), item)
	if err != nil {
		t.Fatal(err)
	}
	field := ""
	for key, value := range record.PublicExport() {
		if value == "first" {
			field = key
		}
	}
	if field == "" {
		t.Fatal("fixture field missing")
	}
	record.Set(field, "second")
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	_, compare := success("version.compare", map[string]any{"versionId": id})
	promote := map[string]any{"versionId": id, "operationId": "version-promote-original", "expectedRevision": compare["versionRevision"], "mainHash": compare["mainHash"]}
	promoted, result := success("version.promote", promote)
	if result["promoted"] != id {
		t.Fatal(result)
	}
	restored, err := pb.FindRecordById(meta.GetString("collection_id"), item)
	if err != nil || restored.GetString(field) != "first" {
		t.Fatalf("restore: %v %v", restored, err)
	}
	receiptCount := func() int {
		var n int
		if err := pb.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	count := receiptCount()
	again, _ := success("version.promote", promote)
	if string(again) != string(promoted) || receiptCount() != count {
		t.Fatalf("promote replay advanced receipt or changed result: %s / %s", promoted, again)
	}
	_, saved := success("version.save", map[string]any{"versionId": id, "operationId": "version-save", "expectedRevision": entry["revision"], "values": map[string]any{}})
	stale := call("version.delete", map[string]any{"versionId": id, "operationId": "version-stale-delete", "expectedRevision": entry["revision"]})
	if stale.Error == nil || stale.Error.Data["code"] != "version_edit_conflict" {
		t.Fatalf("stale CAS: %+v", stale)
	}
	deletion := map[string]any{"versionId": id, "operationId": "version-delete", "expectedRevision": saved["metadataRevision"]}
	deleted, _ := success("version.delete", deletion)
	count = receiptCount()
	for _, sample := range []struct {
		method string
		params map[string]any
		want   json.RawMessage
	}{{"version.create", create, original}, {"version.promote", promote, promoted}, {"version.delete", deletion, deleted}} {
		got, _ := success(sample.method, sample.params)
		if string(got) != string(sample.want) {
			t.Fatalf("%s after delete replay changed", sample.method)
		}
	}
	if receiptCount() != count {
		t.Fatal("replay after delete advanced workspace receipt")
	}
	_, listed = success("version.list", map[string]any{})
	if len(listed["versions"].([]any)) != 0 {
		t.Fatal(listed)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := call("version.create", create); r.Error == nil {
		t.Fatal("closed workspace replay bypassed admission")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	dataDir := pb.DataDir()
	event := &core.TerminateEvent{App: pb}
	if err := pb.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error { return event.App.ResetBootstrapState() }); err != nil {
		t.Fatal(err)
	}
	reopenedPB := historyProductStore(t, dataDir)
	reopenedLedger, err := auditledger.Open(filepath.Join(filepath.Dir(dataDir), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedLedger.Close() })
	kernel := mutation.New(reopenedPB, mutation.MetadataSchemaSource{})
	restoredHistory, err := audit.New(reopenedPB, kernel, audit.WithLedgerHistory(reopenedLedger))
	if err != nil {
		t.Fatal(err)
	}
	reopenedRuntime, err := workspacev2.Open(context.Background(), workspacev2.Options{
		App: reopenedPB, DataDir: dataDir, WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7, FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Ledger: reopenedLedger, Audit: restoredHistory, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedRuntime.Close(context.Background()) })
	reopenedOwner := contentmetadata.NewContentVersions(reopenedPB, reopenedRuntime, restoredHistory)
	var beforeReopenReplay int
	if err := reopenedPB.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&beforeReopenReplay); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		method string
		params map[string]any
		want   json.RawMessage
	}{{"version.create", create, original}, {"version.promote", promote, promoted}, {"version.delete", deletion, deleted}} {
		params, _ := json.Marshal(sample.params)
		result, err := contentVersionRegistration(sample.method, reopenedOwner, reopenedRuntime.CoordinateBusinessWrite).Handler(context.Background(), params)
		encoded, _ := json.Marshal(result)
		if err != nil || string(encoded) != string(sample.want) {
			t.Fatalf("%s real runtime/PB reopen changed original result: %s / %s %v", sample.method, encoded, sample.want, err)
		}
	}
	var afterReopenReplay int
	if err := reopenedPB.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&afterReopenReplay); err != nil || afterReopenReplay != beforeReopenReplay {
		t.Fatalf("reopen replay advanced receipt: %d/%d %v", beforeReopenReplay, afterReopenReplay, err)
	}
}

func TestContentVersionHTTPRejectsScopeCASAndInvalidValuesWithoutWrites(t *testing.T) {
	_, _, mux, table, item, pb := historyRestoreProductFixture(t)
	call := func(method string, p map[string]any, wire string, ctx context.Context) productrpc.ResponseEnvelope {
		t.Helper()
		raw, _ := json.Marshal(p)
		return restoreProductRequest(t, mux, method, string(raw), wire, ctx)
	}
	base := map[string]any{"collection": table, "itemId": item, "key": "kept", "name": "Preserved", "operationId": "scope-create"}
	response := call("version.create", base, schemaListWire, context.Background())
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	var entry map[string]any
	_ = json.Unmarshal(response.Result, &entry)
	count := func() int {
		var n int
		if err := pb.DB().NewQuery("SELECT COUNT(*) FROM workspace_v2_mutation_receipts").Row(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	meta, err := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:id}", dbx.Params{"id": table})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(meta.GetString("collection_id"))
	if err != nil {
		t.Fatal(err)
	}
	other := core.NewRecord(collection)
	other.Id = "otherrecord0001"
	if err := pb.Save(other); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"version.compare", "version.save", "version.delete", "version.promote"} {
		p := map[string]any{"collection": table, "itemId": other.Id, "versionId": entry["id"]}
		if method != "version.compare" {
			p["expectedRevision"] = entry["revision"]
			p["operationId"] = "other-" + method
		}
		if method == "version.promote" {
			p["mainHash"] = "wrong"
		}
		r := call(method, p, schemaListWire, context.Background())
		if r.Error == nil || r.Error.Data["code"] != "version_not_found" {
			t.Fatalf("%s cross-record scope: %+v", method, r.Error)
		}
	}
	for _, method := range []string{"version.save", "version.delete", "version.promote"} {
		p := map[string]any{"collection": table, "itemId": item, "versionId": entry["id"], "expectedRevision": "stale", "operationId": "scope-" + method}
		if method == "version.promote" {
			p["mainHash"] = "stale"
		}
		r := call(method, p, schemaListWire, context.Background())
		if r.Error == nil || r.Error.Data["code"] != "version_edit_conflict" {
			t.Fatalf("%s CAS: %+v", method, r.Error)
		}
	}
	staleMain := call("version.promote", map[string]any{"collection": table, "itemId": item, "versionId": entry["id"], "operationId": "stale-main", "expectedRevision": entry["revision"], "mainHash": "outdated-record-hash"}, schemaListWire, context.Background())
	if staleMain.Error == nil || staleMain.Error.Data["code"] != "version_main_conflict" {
		t.Fatalf("main CAS: %+v", staleMain.Error)
	}
	bad := map[string]any{"collection": table, "itemId": item, "versionId": entry["id"], "expectedRevision": entry["revision"], "operationId": "values", "values": map[string]any{"title": "arbitrary"}}
	r := call("version.save", bad, schemaListWire, context.Background())
	if r.Error == nil || r.Error.Data["code"] != "version_values_not_allowed" {
		t.Fatalf("values: %+v", r.Error)
	}
	bad["values"] = map[string]any{}
	bad["itemId"] = "missingrecord01"
	r = call("version.save", bad, schemaListWire, context.Background())
	if r.Error == nil || r.Error.Data["code"] != "version_record_unavailable" {
		t.Fatalf("record: %+v", r.Error)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	r = call("version.create", base, schemaListWire, canceled)
	if r.Error == nil {
		t.Fatal("cancelled replay bypassed gate")
	}
	staleWire := strings.Replace(schemaListWire, `"sessionEpoch":7`, `"sessionEpoch":6`, 1)
	r = call("version.create", base, staleWire, context.Background())
	if r.Error == nil || r.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale epoch: %+v", r.Error)
	}
	if count() != before {
		t.Fatal("rejected command advanced workspace receipt")
	}
}

func TestContentVersionPromoteReceiptFailureRollsBackAuditAndRecord(t *testing.T) {
	_, _, mux, table, item, pb := historyRestoreProductFixture(t)
	call := func(method string, p map[string]any) productrpc.ResponseEnvelope {
		p["collection"] = table
		p["itemId"] = item
		raw, _ := json.Marshal(p)
		return restoreProductRequest(t, mux, method, string(raw), schemaListWire, context.Background())
	}
	created := call("version.create", map[string]any{"operationId": "rollback-create"})
	if created.Error != nil {
		t.Fatal(created.Error)
	}
	var entry map[string]any
	_ = json.Unmarshal(created.Result, &entry)
	meta, _ := pb.FindFirstRecordByFilter("vibetable_tables", "table_id={:id}", dbx.Params{"id": table})
	record, _ := pb.FindRecordById(meta.GetString("collection_id"), item)
	field := ""
	for k, v := range record.PublicExport() {
		if v == "first" {
			field = k
		}
	}
	record.Set(field, "intervening")
	if err := pb.Save(record); err != nil {
		t.Fatal(err)
	}
	compared := call("version.compare", map[string]any{"versionId": entry["id"]})
	if compared.Error != nil {
		t.Fatal(compared.Error)
	}
	var comparison map[string]any
	_ = json.Unmarshal(compared.Result, &comparison)
	snapshot := func() []int {
		var counts []int
		for _, name := range []string{"vibetable_audit_events", "vibetable_outbox", "vibetable_idempotency_keys", "workspace_v2_mutation_receipts"} {
			var n int
			if err := pb.DB().NewQuery("SELECT COUNT(*) FROM " + name).Row(&n); err != nil {
				t.Fatal(err)
			}
			counts = append(counts, n)
		}
		return counts
	}
	before := snapshot()
	hook := pb.OnRecordCreate().BindFunc(func(event *core.RecordEvent) error {
		if event.Record.Collection().Name == "vibetable_idempotency_keys" && event.Record.GetString("key") == "metadata:version.promote:rollback-promote" {
			return errors.New("injected named revision receipt failure")
		}
		return event.Next()
	})
	params := map[string]any{"versionId": entry["id"], "expectedRevision": comparison["versionRevision"], "mainHash": comparison["mainHash"], "operationId": "rollback-promote"}
	failed := call("version.promote", params)
	pb.OnRecordCreate().Unbind(hook)
	if failed.Error == nil || !reflect.DeepEqual(before, snapshot()) {
		t.Fatalf("receipt failure committed partial state: %+v %v %v", failed.Error, before, snapshot())
	}
	record, _ = pb.FindRecordById(meta.GetString("collection_id"), item)
	if record.GetString(field) != "intervening" {
		t.Fatal("row escaped failed transaction")
	}
	retry := call("version.promote", params)
	if retry.Error != nil {
		t.Fatalf("retry after rollback: %+v", retry.Error)
	}
}
