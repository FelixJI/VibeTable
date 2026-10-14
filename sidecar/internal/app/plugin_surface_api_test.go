package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func TestPluginSurfaceHTTPAfterCommitFaultIsUnknown(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"finish", errors.New("synthetic committed finish failure")},
		{"cancel", context.Canceled},
		{"timeout", context.DeadlineExceeded},
		{"typed_conflict_after_commit", &mutation.ProductError{Code: "mutation.digest_conflict"}},
		{"precommit_conflict", nil},
	} {
		t.Run(failure.name, func(t *testing.T) {
			armed := false
			probing := false
			var cancelCaller context.CancelFunc
			f := newWorkspaceMutationReplayFixture(t, func(point writecoordinator.PersistenceFaultPoint) error {
				if armed && point == writecoordinator.FaultBeforeFinishCommittedMutation {
					return failure.err
				}
				return nil
			})
			workspace := "11111111-1111-4111-8111-111111111111"
			store := pluginstore.New(f.pb, workspace)
			pluginID, hash := "com.example.after-commit", strings.Repeat("d", 64)
			manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"data": []any{map[string]any{"collection": f.request.TableID, "operations": []string{"read", "query", "create", "update"}, "fields": []string{"*"}}}}, "actions": []any{map[string]any{"actionId": "save", "risk": "destructive", "mode": "local", "invocation": "manual"}}, "ui": map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
			snapshot, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash, "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}})
			if _, err := store.SaveInstallation(context.Background(), snapshot, nil); err != nil {
				t.Fatal(err)
			}
			source, err := queryschema.New(f.pb.DataDir())
			if err != nil {
				t.Fatal(err)
			}
			r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
				return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: req}}, nil
			})
			secret, err := auth.Parse(strings.Repeat("2", 64))
			if err != nil {
				t.Fatal(err)
			}
			bindVibetableSessionAuth(r, secret)
			registerPluginSurfaceAPIRoutes(r, f.pb, source, mutation.New(f.pb, mutation.MetadataSchemaSource{}), workspace, 7, func(ctx context.Context, kind, id string, fn func(context.Context) error) error {
				if cancelCaller != nil {
					cancelCaller()
					if !errors.Is(ctx.Err(), context.Canceled) {
						t.Fatal("caller cancellation not inherited")
					}
					return ctx.Err()
				}
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("route did not set deadline")
				}
				remaining := time.Until(deadline)
				if probing {
					if kind != "plugin.surface.mutation" || remaining > 7*time.Second || remaining < 5*time.Second {
						t.Fatalf("caller deadline not inherited: %v", remaining)
					}
					if ctx.Err() != nil {
						t.Fatal("live caller was cancelled")
					}
					return errors.New("bounded deadline probe")
				}
				if kind == "plugin.surface.mutation" && remaining < 110*time.Second {
					t.Fatalf("commit budget prematurely capped: %v", remaining)
				}
				if kind != "plugin.surface.mutation" && remaining > 16*time.Second {
					t.Fatalf("ordinary request budget widened: %v", remaining)
				}
				return f.runtime.CoordinateBusinessWrite(ctx, kind, id, fn)
			})
			mux, err := r.BuildMux()
			if err != nil {
				t.Fatal(err)
			}
			input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, ActiveCollection: f.request.TableID, RequestID: "surface-after-commit"}
			call := func(method string, params any, caller ...context.Context) *httptest.ResponseRecorder {
				input.Method = method
				input.Request, _ = json.Marshal(params)
				raw, _ := json.Marshal(input)
				request := httptest.NewRequest(http.MethodPost, pluginSurfaceAPIPath, bytes.NewReader(raw))
				if len(caller) != 0 {
					request = request.WithContext(caller[0])
				}
				request.Header.Set("X-VibeTable-Session", strings.Repeat("2", 64))
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				return response
			}
			plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": f.request.TableID, "schemaRevision": f.request.SchemaRevision, "operations": []any{map[string]any{"kind": "create", "values": map[string]any{f.field: "committed"}}}}
			var original mutation.Receipt
			if failure.name == "precommit_conflict" {
				original = workspaceMutationReplayReceipt(t, f.call(t, context.Background(), f.request))
				plan["operations"] = []any{map[string]any{"kind": "update", "primaryKey": original.AffectedRows[0].RecordID, "expectedDigest": original.AffectedRows[0].Digest, "values": map[string]any{f.field: "must not write"}}}
			}
			if response := call("audit.start", map[string]any{"taskId": "task-after-commit", "plan": plan}); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			if failure.name == "precommit_conflict" {
				changed := f.request
				changed.RequestID, changed.IdempotencyKey = "concurrent-update", "concurrent-update"
				changed.Operations = []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &original.AffectedRows[0].RecordID, ExpectedDigest: &original.AffectedRows[0].Digest, Values: map[string]any{f.field: "concurrent edit"}}}
				workspaceMutationReplayReceipt(t, f.call(t, context.Background(), changed))
			}
			// Fast probe: inspect the actual context entering the production mutation gate.
			probing = true
			caller, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			probe := call("mutation.commit", plan, caller)
			cancel()
			probing = false
			callerCancelled, cancelRequest := context.WithCancel(context.Background())
			cancelCaller = cancelRequest
			cancelledProbe := call("mutation.commit", plan, callerCancelled)
			cancelCaller = nil
			cancelRequest()
			if cancelledProbe.Code != http.StatusConflict {
				t.Fatal("cancelled probe unexpectedly committed")
			}
			if probe.Code != http.StatusConflict {
				t.Fatal("deadline probe unexpectedly committed")
			}
			before := f.state(t)
			armed = true
			response := call("mutation.commit", plan)
			armed = false
			var wire struct {
				Code    string `json:"code"`
				Outcome string `json:"commitOutcome"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &wire)
			t.Logf("actual HTTP commit response: %d %s", response.Code, response.Body.String())
			if failure.name == "precommit_conflict" {
				if response.Code != http.StatusConflict || wire.Code != "mutation.digest_conflict" || wire.Outcome != "not-committed" {
					t.Fatal("precommit conflict lost definite rejection", response.Body.String())
				}
				if !reflect.DeepEqual(before, f.state(t)) {
					t.Fatal("precommit conflict wrote data or receipt")
				}
				events, err := store.ListAudit(context.Background(), input.ProjectKey, pluginID)
				if err != nil || len(events) != 1 {
					t.Fatal("precommit conflict produced success audit", err)
				}
				for _, fact := range []struct {
					workspace, kind, run string
					expected             string
				}{
					{"22222222-2222-4222-8222-222222222222", "plugin.surface.mutation", input.RequestID, "not-committed"},
					{workspace, "plugin.surface.audit.start", input.RequestID, "not-committed"},
					{workspace, "plugin.surface.mutation", "other-run", "not-committed"},
					{workspace, "plugin.surface.mutation", input.RequestID, "unknown"},
				} {
					if _, err := f.pb.DB().NewQuery(`INSERT INTO workspace_v2_imported_completions (workspace_id,kind,identity) VALUES ({:workspace},{:kind},{:run})`).Bind(map[string]any{"workspace": fact.workspace, "kind": fact.kind, "run": fact.run}).Execute(); err != nil {
						t.Fatal(err)
					}
					response := call("mutation.commit", plan)
					_ = json.Unmarshal(response.Body.Bytes(), &wire)
					if wire.Outcome != fact.expected {
						t.Fatal("imported completion scope/proof ignored", response.Body.String())
					}
				}
				return
			}
			rows, err := f.pb.FindRecordsByFilter(f.definition.PhysicalName, "", "", 0, 0)
			if err != nil || len(rows) != 1 || rows[0].GetString(f.field) != "committed" {
				t.Fatal("after-commit fault lost real row", err)
			}
			if after := f.state(t); after.proofs != before.proofs+1 {
				t.Fatal("committed PB receipt missing")
			}
			events, err := store.ListAudit(context.Background(), input.ProjectKey, pluginID)
			if err != nil || len(events) != 2 || !strings.Contains(string(events[1]), `"outcome":"succeeded"`) {
				t.Fatal("committed successful audit missing", err)
			}
			if response.Code != http.StatusConflict || wire.Code != "plugin_commit_unknown" || wire.Outcome != "unknown" {
				t.Errorf("committed error was classified as definite failure: %s", response.Body.String())
			}
			_ = call("audit.finish", map[string]any{"outcome": "unknown", "errorCode": "plugin_commit_unknown"})
			afterEvents, err := store.ListAudit(context.Background(), input.ProjectKey, pluginID)
			if err != nil || !reflect.DeepEqual(events, afterEvents) {
				t.Fatal("Host observation overwrote authority or replayed business data", err)
			}
		})
	}
}

func TestPluginSurfaceMissingCommitProofNeverEstablishesRollback(t *testing.T) {
	// A PB fixture without the Workspace v2 proof tables cannot prove rollback,
	// even when an error looks like a known digest conflict.
	pb := schemaProductStore(t)
	input := pluginSurfaceAPIRequest{RequestID: "proof-unavailable", SessionEpoch: 7}
	for _, result := range []any{nil, mutation.Receipt{Status: mutation.StatusApplied}} {
		if outcome := surfaceCommitOutcome(context.Background(), pb, "11111111-1111-4111-8111-111111111111", input, result, "mutation.digest_conflict"); outcome != "unknown" {
			t.Fatal("missing or contradictory proof established rollback", outcome)
		}
	}
}

func TestPluginSurfaceWorkspaceWriterOwnsSingleReceipt(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceMutationReplayFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	store := pluginstore.New(f.pb, workspace)
	pluginID, hash := "com.example.writer-surface", strings.Repeat("c", 64)
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"data": []any{map[string]any{"collection": f.request.TableID, "operations": []string{"read", "query", "create", "update"}, "fields": []string{"*"}}}}, "actions": []any{map[string]any{"actionId": "save", "risk": "destructive", "mode": "local", "invocation": "manual"}}, "ui": map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
	manifest["actions"] = append(manifest["actions"].([]any), map[string]any{"actionId": "read-page", "risk": "read", "mode": "local", "invocation": "manual"})
	manifest["ui"].(map[string]any)["customViews"] = append(manifest["ui"].(map[string]any)["customViews"].([]any), map[string]any{"viewId": "read", "actionId": "read-page", "entry": "ui/read.html"})
	snapshot, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash, "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}})
	if _, err := store.SaveInstallation(ctx, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
	input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, ActiveCollection: f.request.TableID}
	call := func(method string, params any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(params)
		// The actual Workspace v2 runtime prepares the intent and verifies its
		// PocketBase receipt; an apply-through gate would hide double inserts.
		return executePluginSurfaceAPI(ctx, f.pb, source, kernel, workspace, input, f.runtime.CoordinateBusinessWrite)
	}
	auditCount := func() int {
		events, err := store.ListAudit(ctx, input.ProjectKey, pluginID)
		if err != nil {
			t.Fatal(err)
		}
		return len(events)
	}
	plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": f.request.TableID, "schemaRevision": f.request.SchemaRevision, "operations": []any{map[string]any{"kind": "create", "values": map[string]any{f.field: "created"}}}}
	// An authenticated Host cannot borrow an action or a page outside this installation.
	for _, identity := range []struct{ action, entry string }{{"undeclared", "ui/index.html"}, {"save", "ui/read.html"}, {"read-page", "ui/read.html"}} {
		before := f.state(t)
		input.ActionID, input.Entry = identity.action, identity.entry
		for _, method := range []string{"mutation.preview", "audit.start", "mutation.commit"} {
			params := any(plan)
			if method == "audit.start" {
				params = map[string]any{"taskId": "denied-task", "plan": plan}
			}
			if _, err := call(method, params); err == nil {
				t.Fatalf("action/page/risk admitted: %+v %s", identity, method)
			}
		}
		if !reflect.DeepEqual(before, f.state(t)) || auditCount() != 0 {
			t.Fatal("denied action wrote data or audit")
		}
	}
	input.ActionID, input.Entry = "save", "ui/index.html"
	input.RequestID = "surface-writer-create"
	beforeStart := f.state(t)
	if _, err := call("audit.start", map[string]any{"taskId": "task-create", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	startedRaw, err := store.GetAudit(ctx, input.ProjectKey, pluginID, input.RequestID+":started")
	var startIdentity map[string]any
	_ = json.Unmarshal(startedRaw, &startIdentity)
	if err != nil || startIdentity["actionId"] != "save" || startIdentity["risk"] != "destructive" {
		t.Fatal("declared action/risk not persisted before confirmation", startIdentity, err)
	}
	started := f.state(t)
	if started.proofs != beforeStart.proofs+1 || started.revision != beforeStart.revision+1 || auditCount() != 1 {
		t.Fatal("independent start did not persist its own receipt")
	}
	result, err := call("mutation.commit", plan)
	if err != nil {
		t.Fatal("real Workspace v2 create failed", err)
	}
	created := result.(mutation.Receipt)
	if created.Status != mutation.StatusApplied || len(created.AffectedRows) != 1 {
		t.Fatalf("invalid create receipt %#v", created)
	}
	committed := f.state(t)
	createdRows, err := f.pb.FindRecordsByFilter(f.definition.PhysicalName, "", "", 0, 0)
	if err != nil || len(createdRows) != 1 || createdRows[0].GetString(f.field) != "created" {
		t.Fatal("created row missing or duplicated", err)
	}
	if committed.proofs != started.proofs+1 || committed.revision != started.revision+1 || auditCount() != 2 {
		t.Fatalf("create or terminal audit did not commit with exactly one receipt: before=%+v after=%+v audits=%d", started, committed, auditCount())
	}
	if replay, err := call("mutation.commit", plan); err != nil || replay.(mutation.Receipt).Status != mutation.StatusReplayed {
		t.Fatal("real gated replay failed", err)
	}
	if !reflect.DeepEqual(committed, f.state(t)) || auditCount() != 2 {
		t.Fatal("replay changed data, audit or business receipts")
	}
	id, digest := created.AffectedRows[0].RecordID, created.AffectedRows[0].Digest
	plan["operations"] = []any{map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": digest, "values": map[string]any{f.field: "updated"}}}
	input.RequestID = "surface-writer-update"
	if _, err := call("audit.start", map[string]any{"taskId": "task-update", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	beforeUpdate := f.state(t)
	result, err = call("mutation.commit", plan)
	if err != nil {
		t.Fatal("real Workspace v2 update failed", err)
	}
	updated := result.(mutation.Receipt)
	row, err := f.pb.FindRecordById(f.definition.PhysicalName, id)
	if err != nil || row.GetString(f.field) != "updated" {
		t.Fatal("update missing", err)
	}
	afterUpdate := f.state(t)
	if afterUpdate.proofs != beforeUpdate.proofs+1 || afterUpdate.revision != beforeUpdate.revision+1 || auditCount() != 4 {
		t.Fatal("update did not persist exactly one receipt and terminal audit")
	}
	plan["operations"] = []any{map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": updated.AffectedRows[0].Digest, "values": map[string]any{f.field: "must roll back"}}}
	input.RequestID = "surface-writer-failed-audit"
	if _, err := call("audit.start", map[string]any{"taskId": "task-failed-audit", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	beforeFailure := f.state(t)
	if _, err := f.pb.DB().NewQuery(`CREATE TRIGGER reject_surface_writer_audit BEFORE INSERT ON vibetable_plugin_records WHEN NEW.item_key = 'surface-writer-failed-audit:terminal' BEGIN SELECT RAISE(ABORT, 'synthetic audit failure'); END`).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := call("mutation.commit", plan); err == nil {
		t.Fatal("audit failure accepted")
	}
	if !reflect.DeepEqual(beforeFailure, f.state(t)) || auditCount() != 5 {
		t.Fatal("audit failure did not atomically roll back data and receipt")
	}
	if _, err := f.pb.DB().NewQuery(`DROP TRIGGER reject_surface_writer_audit`).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := call("audit.finish", map[string]any{"outcome": "failed", "errorCode": "plugin_request_failed"}); err != nil {
		t.Fatal(err)
	}
	if after := f.state(t); after.proofs != beforeFailure.proofs+1 || after.revision != beforeFailure.revision+1 || auditCount() != 6 {
		t.Fatal("independent finish lost its receipt")
	}
	// The existing standalone owner must still persist its receipt, notify
	// exactly once per commit and keep duplicate event ids immutable.
	events, err := store.ListAudit(ctx, input.ProjectKey, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range events {
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		if event["actionId"] != "save" || event["risk"] != "destructive" {
			t.Fatal("declared action/risk lost", event)
		}
	}
	var independent map[string]any
	_ = json.Unmarshal(events[0], &independent)
	independent["eventId"] = "surface-standalone-audit"
	independent["outcome"] = "cancelled"
	notified := 0
	standalone := pluginstore.New(f.pb, workspace, func() { notified++ })
	beforeStandalone := f.state(t)
	for i := 0; i < 2; i++ {
		if i == 1 {
			independent["outcome"] = "succeeded"
		}
		raw, _ := json.Marshal(independent)
		if err := f.runtime.CoordinateBusinessWrite(ctx, "plugin.store.record_audit", fmt.Sprintf("surface-independent-%d", i), func(writeCtx context.Context) error {
			_, err := standalone.RecordAudit(writeCtx, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if after := f.state(t); after.proofs != beforeStandalone.proofs+2 || after.revision != beforeStandalone.revision+2 || auditCount() != 7 || notified != 2 {
		t.Fatal("standalone receipt, notification or idempotency changed")
	}
	events, err = store.ListAudit(ctx, input.ProjectKey, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range events {
		var event struct {
			EventID string `json:"eventId"`
			Outcome string `json:"outcome"`
		}
		_ = json.Unmarshal(raw, &event)
		if event.EventID == "surface-standalone-audit" && event.Outcome != "cancelled" {
			t.Fatal("duplicate standalone audit rewrote immutable history")
		}
	}
}

func TestPluginSurfaceReal550ReadWriteAndLiveAuthority(t *testing.T) {
	ctx := context.Background()
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "surface synthetic", OperationID: "surface-table", Actor: v2.Actor{Kind: "user", ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	amount := createSchemaProductField(t, pb, table.TableID, v2.LogicalNumber, "Amount", "surface-number").Definition
	flag := createSchemaProductField(t, pb, table.TableID, v2.LogicalBool, "Flag", "surface-flag").Definition
	secret := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Hidden", "surface-hidden").Definition
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := source.DescribeQueryTable(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(descriptor.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	err = pb.RunInTransaction(func(tx core.App) error {
		for i := 0; i < 550; i++ {
			row := core.NewRecord(collection)
			row.Set(amount.Identity.PhysicalName, i)
			row.Set(amount.Value.Presence.PhysicalName, i != 1)
			row.Set(flag.Identity.PhysicalName, i%2 == 1)
			row.Set(flag.Value.Presence.PhysicalName, true)
			row.Set(secret.Identity.PhysicalName, "never expose")
			if err := tx.Save(row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	store := pluginstore.New(pb, workspace)
	pluginID := "com.example.surface"
	hash := strings.Repeat("a", 64)
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"data": []any{map[string]any{"collection": table.TableID, "operations": []string{"read", "query", "create", "update"}, "fields": []string{"id", amount.Identity.FieldID, flag.Identity.FieldID}}}, "privateStorage": true}, "actions": []any{map[string]any{"actionId": "save", "risk": "destructive", "mode": "local", "invocation": "manual"}}, "ui": map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
	snapshot := map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash, "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}}
	raw, _ := json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{})
	input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, ActiveCollection: table.TableID, RequestID: "surface-test-read"}
	call := func(method string, params any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(params)
		return executePluginSurfaceAPI(ctx, pb, source, kernel, workspace, input, nil)
	}
	query := map[string]any{"contract": "vibetable.plugin-query.v2", "collection": table.TableID, "fields": []string{"id", amount.Identity.FieldID, flag.Identity.FieldID}, "pageSize": 200}
	// Exercise the exact production Host-only route as well as its transactional owner.
	authSecret, err := auth.Parse(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	router := router.NewRouter(func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: w, Request: r}}, nil
	})
	bindVibetableSessionAuth(router, authSecret)
	registerPluginSurfaceAPIRoutes(router, pb, source, kernel, workspace, 7, nil)
	mux, err := router.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	input.Method = "context.read"
	input.Request = json.RawMessage(`{}`)
	requestBytes, _ := json.Marshal(input)
	for _, authenticated := range []bool{false, true} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+pluginSurfaceAPIPath, bytes.NewReader(requestBytes))
		request.Header.Set("Content-Type", "application/json")
		if authenticated {
			request.Header.Set("X-VibeTable-Session", strings.Repeat("1", 64))
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		expected := http.StatusUnauthorized
		if authenticated {
			expected = http.StatusOK
		}
		if response.StatusCode != expected {
			t.Fatalf("route auth status %d want %d", response.StatusCode, expected)
		}
	}
	var first map[string]any
	allRows := []any{}
	lengths := []int{}
	seen := map[string]bool{}
	for {
		output, err := call("data.query", query)
		if err != nil {
			t.Fatal(err)
		}
		page := output.(map[string]any)
		if first == nil {
			first = page
		}
		rows := page["items"].([]any)
		allRows = append(allRows, rows...)
		lengths = append(lengths, len(rows))
		guards := page["rowGuards"].(map[string]any)
		for _, raw := range rows {
			row := raw.(map[string]any)
			id := row["id"].(string)
			if seen[id] {
				t.Fatal("duplicate page row")
			}
			seen[id] = true
			guard, ok := guards[id].(string)
			if !ok || !strings.HasPrefix(guard, "sha256:") {
				t.Fatalf("missing opaque guard: %#v", guards[id])
			}
			if _, ok := row[secret.Identity.FieldID]; ok {
				t.Fatal("hidden value leaked")
			}
		}
		input.Cursor = ""
		if next := page["nextCursor"].(*string); next != nil {
			input.Cursor = *next
		}
		input.Dependencies = page["dependencies"].(map[string]pluginComputedRevision)
		if input.Cursor == "" {
			break
		}
	}
	if fmt.Sprint(lengths) != "[200 200 150]" || len(seen) != 550 {
		t.Fatalf("partial pages %v", lengths)
	}
	values := map[string]bool{"zero": false, "false": false, "null": false}
	for _, raw := range allRows {
		row := raw.(map[string]any)
		if row[amount.Identity.FieldID] == int64(0) || row[amount.Identity.FieldID] == float64(0) {
			values["zero"] = true
		}
		if row[flag.Identity.FieldID] == false {
			values["false"] = true
		}
		if row[amount.Identity.FieldID] == nil {
			values["null"] = true
		}
	}
	if !values["zero"] || !values["false"] || !values["null"] {
		t.Fatalf("raw values were coerced %#v", values)
	}
	input.Cursor = ""
	input.Dependencies = nil
	t.Run("Host invocation context is authorized rather than discarded", func(t *testing.T) {
		id := allRows[0].(map[string]any)["id"].(string)
		for _, field := range []string{amount.Identity.PhysicalName, secret.Identity.PhysicalName, "id"} {
			input.InvocationContext, _ = json.Marshal(map[string]any{"selectedKeys": []string{id}, "user": map[string]any{"id": "local-user", "displayName": "Synthetic"}, "querySnapshot": map[string]any{"table": table.TableID, "schemaRevision": first["schemaRevision"], "dataRevision": first["dataRevision"], "normalizedQuery": map[string]any{"filters": []any{map[string]any{"field": field, "operator": "eq", "value": 0}}, "limit": 100}}})
			result, err := call("context.read", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			value := result.(map[string]any)
			if len(value["selectedKeys"].([]any)) != 1 || value["user"].(map[string]any)["id"] != "local-user" {
				t.Fatalf("trusted invocation lost %#v", value)
			}
			if (value["querySnapshot"] == nil) != (field == secret.Identity.PhysicalName) {
				t.Fatalf("query context authorization incorrect %#v", value)
			}
		}
		input.InvocationContext = nil
	})
	t.Run("storage trust boundary", func(t *testing.T) {
		for _, key := range []string{"", "_invalid", "你好", "with space", "control\nkey", strings.Repeat("k", 129)} {
			for _, method := range []string{"storage.get", "storage.set", "storage.delete"} {
				params := map[string]any{"key": key}
				if method == "storage.set" {
					params["value"] = true
				}
				if _, err := call(method, params); err == nil {
					t.Errorf("accepted %s key %q", method, key)
				}
			}
		}
		for _, value := range []string{strings.Repeat("x", 65535), strings.Repeat("界", 21845)} {
			if _, err := call("storage.set", map[string]any{"key": "oversize", "value": value}); err == nil {
				t.Error("accepted >65536 encoded UTF-8 bytes")
			}
		}
		for _, value := range []string{strings.Repeat("x", 65534), strings.Repeat("界", 21844), "control\nvalue"} {
			if _, err := call("storage.set", map[string]any{"key": "valid.boundary", "value": value}); err != nil {
				t.Fatal("valid UTF-8 boundary rejected", err)
			}
		}
	})
	t.Run("business-only grant never reveals ID guards or updates by ID", func(t *testing.T) {
		var limited map[string]any
		_ = json.Unmarshal(raw, &limited)
		limited["pluginId"] = "com.example.noid"
		m := limited["manifest"].(map[string]any)
		m["pluginId"] = limited["pluginId"]
		m["permissions"].(map[string]any)["data"] = []any{map[string]any{"collection": table.TableID, "operations": []string{"read", "query", "update"}, "fields": []string{amount.Identity.FieldID}}}
		limitedRaw, _ := json.Marshal(limited)
		if _, err := store.SaveInstallation(ctx, limitedRaw, nil); err != nil {
			t.Fatal(err)
		}
		input.PluginID = "com.example.noid"
		defer func() { input.PluginID = pluginID; input.Cursor = ""; input.Dependencies = nil }()
		output, err := call("data.query", map[string]any{"contract": "vibetable.plugin-query.v2", "collection": table.TableID, "fields": []string{amount.Identity.FieldID}, "pageSize": 200})
		if err != nil {
			t.Fatal(err)
		}
		page := output.(map[string]any)
		if len(page["rowGuards"].(map[string]any)) != 0 {
			t.Error("internal cursor IDs/digests escaped business-only grant")
		}
		input.Cursor = *page["nextCursor"].(*string)
		input.Dependencies = page["dependencies"].(map[string]pluginComputedRevision)
		next, err := call("data.query", map[string]any{"contract": "vibetable.plugin-query.v2", "collection": table.TableID, "fields": []string{amount.Identity.FieldID}, "pageSize": 200})
		if err != nil || len(next.(map[string]any)["items"].([]any)) != 200 || len(next.(map[string]any)["rowGuards"].(map[string]any)) != 0 {
			t.Fatal("internal keyset cursor lost business-only pagination", err)
		}
		input.Cursor = ""
		input.Dependencies = nil
		input.InvocationContext = json.RawMessage(`{"selectedKeys":["ungranted-id"],"user":{"id":"local-user"},"querySnapshot":null}`)
		contextResult, err := call("context.read", map[string]any{})
		if err != nil || len(contextResult.(map[string]any)["selectedKeys"].([]any)) != 0 {
			t.Fatal("selection ID escaped grant", err)
		}
		input.InvocationContext = nil
		id := allRows[0].(map[string]any)["id"].(string)
		guard := first["rowGuards"].(map[string]any)[id]
		if _, err := call("mutation.preview", map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": table.TableID, "schemaRevision": first["schemaRevision"], "operations": []any{map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": guard, "values": map[string]any{amount.Identity.FieldID: 77}}}}); err == nil {
			t.Error("update accepted an ungranted primary key")
		}
	})
	for _, patch := range []map[string]any{{"fields": []string{secret.Identity.FieldID}}, {"sorts": []any{map[string]any{"field": secret.Identity.FieldID}}}, {"filters": []any{map[string]any{"field": secret.Identity.FieldID, "operator": "eq", "value": "never expose"}}}, {"sql": "select *"}, {"pluginId": "fake"}} {
		bad := map[string]any{}
		for k, v := range query {
			bad[k] = v
		}
		for k, v := range patch {
			bad[k] = v
		}
		if _, err := call("data.query", bad); err == nil {
			t.Fatalf("accepted hidden/extra query %v", patch)
		}
	}
	if _, err = call("mutation.apply", map[string]any{}); err == nil {
		t.Fatal("internal method accepted")
	}
	if _, err = call("storage.set", map[string]any{"key": "settings", "value": map[string]any{"zero": 0, "flag": false, "empty": nil}}); err != nil {
		t.Fatal(err)
	}
	stored, err := call("storage.get", map[string]any{"key": "settings"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored)
	if !strings.Contains(string(encoded), `"flag":false`) {
		t.Fatalf("storage coerced %s", encoded)
	}
	var other map[string]any
	if json.Unmarshal(raw, &other) != nil {
		t.Fatal("snapshot fixture invalid")
	}
	other["pluginId"] = "com.example.other"
	other["packageHash"] = "sha256:" + strings.Repeat("b", 64)
	other["manifest"].(map[string]any)["pluginId"] = "com.example.other"
	otherRaw, _ := json.Marshal(other)
	if _, err = store.SaveInstallation(ctx, otherRaw, nil); err != nil {
		t.Fatal(err)
	}
	input.PluginID = "com.example.other"
	input.PackageHash = strings.Repeat("b", 64)
	if borrowed, err := call("storage.get", map[string]any{"key": "settings"}); err != nil || borrowed != nil {
		t.Fatalf("private settings crossed installations %#v %v", borrowed, err)
	}
	input.PackageHash = hash
	if _, err := call("storage.get", map[string]any{"key": "settings"}); err == nil {
		t.Fatal("borrowed package revision admitted")
	}
	input.PluginID = pluginID
	input.PackageHash = hash
	if _, err = call("storage.delete", map[string]any{"key": "settings"}); err != nil {
		t.Fatal(err)
	}
	if stored, err = call("storage.get", map[string]any{"key": "settings"}); err != nil || stored != nil {
		t.Fatalf("delete failed %#v %v", stored, err)
	}
	row := first["items"].([]any)[0].(map[string]any)
	id := row["id"].(string)
	guard := first["rowGuards"].(map[string]any)[id]
	plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": table.TableID, "schemaRevision": first["schemaRevision"], "operations": []any{map[string]any{"kind": "update", "primaryKey": id, "expectedDigest": guard, "values": map[string]any{amount.Identity.FieldID: 999}}}}
	if _, err = call("mutation.preview", plan); err != nil {
		t.Fatal(err)
	}
	// Capture the trusted mutation, then revoke before Apply to exercise the transaction callback itself.
	guarded, err := surfaceMutationRequest(ctx, pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot["revision"] = 2
	snapshot["status"] = "disabled"
	revoked, _ := json.Marshal(snapshot)
	revision := int64(1)
	if _, err = store.SaveInstallation(ctx, revoked, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err = kernel.Preview(ctx, guarded); err == nil {
		t.Fatal("preview admitted a revoked principal")
	}
	if _, err = kernel.Apply(ctx, guarded); err == nil {
		t.Fatal("transaction admitted a revoked principal")
	}
	unchanged, err := pb.FindRecordById(collection, id)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.GetFloat(amount.Identity.PhysicalName) == 999 {
		t.Fatal("revoked transaction wrote a value")
	}
	snapshot["revision"] = 3
	snapshot["status"] = "enabled"
	restored, _ := json.Marshal(snapshot)
	revision = 2
	if _, err = store.SaveInstallation(ctx, restored, &revision); err != nil {
		t.Fatal(err)
	}
	input.InstallationRevision = 3
	for _, outcome := range []string{"rejected", "cancelled", "failed"} {
		input.RequestID = "surface-audit-" + outcome
		if _, err = call("audit.start", map[string]any{"taskId": "task-" + outcome, "plan": plan}); err != nil {
			t.Fatal(err)
		}
		if _, err = call("audit.finish", map[string]any{"outcome": outcome, "errorCode": ""}); err != nil {
			t.Fatal(err)
		}
	}
	input.RequestID = "surface-audit-rollback"
	if _, err = call("audit.start", map[string]any{"taskId": "task-rollback", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	// Failure of the existing audit store must roll back the entire business transaction.
	if _, err = pb.DB().NewQuery(`CREATE TRIGGER reject_surface_terminal BEFORE INSERT ON vibetable_plugin_records WHEN NEW.item_key = 'surface-audit-rollback:terminal' BEGIN SELECT RAISE(ABORT, 'synthetic audit failure'); END`).Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err = call("mutation.commit", plan); err == nil {
		t.Fatal("audit callback failure accepted")
	}
	unchanged, err = pb.FindRecordById(collection, id)
	if err != nil || unchanged.GetFloat(amount.Identity.PhysicalName) == 999 {
		t.Fatal("audit failure failed to roll back data", err)
	}
	if _, err = pb.DB().NewQuery(`DROP TRIGGER reject_surface_terminal`).Execute(); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "surface-single-commit"
	if _, err = call("audit.start", map[string]any{"taskId": "task-success", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	receipt, err := call("mutation.commit", plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.(mutation.Receipt).Status != mutation.StatusApplied {
		t.Fatalf("commit %#v", receipt)
	}
	if _, err = call("audit.finish", map[string]any{"outcome": "unknown", "errorCode": "plugin_commit_unknown"}); err != nil {
		t.Fatal(err)
	}
	audits, err := store.ListAudit(ctx, input.ProjectKey, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	terminals := map[string]string{}
	for _, raw := range audits {
		var event struct {
			EventID  string `json:"eventId"`
			Outcome  string `json:"outcome"`
			RunID    string `json:"runId"`
			ActionID string `json:"actionId"`
		}
		_ = json.Unmarshal(raw, &event)
		if strings.HasSuffix(event.EventID, ":terminal") {
			terminals[event.RunID] = event.Outcome
			if event.ActionID != "save" {
				t.Fatal("audit action identity lost")
			}
		}
	}
	if terminals["surface-single-commit"] != "succeeded" || terminals["surface-audit-rejected"] != "rejected" || terminals["surface-audit-cancelled"] != "cancelled" || terminals["surface-audit-failed"] != "failed" || terminals["surface-audit-rollback"] != "" {
		t.Fatalf("incorrect durable outcomes %#v", terminals)
	}
	replay, err := call("mutation.commit", plan)
	if err != nil || replay.(mutation.Receipt).Status != mutation.StatusReplayed {
		t.Fatalf("replay %#v %v", replay, err)
	}
	input.RequestID = "surface-stale-plan"
	if _, err = call("mutation.commit", plan); err == nil {
		t.Fatal("stale guard accepted")
	}
	// Authorization runs inside Apply before replay too, so revocation cannot retrieve a receipt.
	snapshot["revision"] = 4
	snapshot["status"] = "disabled"
	raw, _ = json.Marshal(snapshot)
	expected := int64(3)
	if _, err = store.SaveInstallation(ctx, raw, &expected); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "surface-single-commit"
	if _, err = call("audit.finish", map[string]any{"outcome": "unknown", "errorCode": "plugin_commit_unknown"}); err != nil {
		t.Fatal("original run could not be audited after disable", err)
	}
	if _, err = call("mutation.commit", plan); err == nil {
		t.Fatal("disabled installation accepted replay")
	}
	input.PluginID = "com.example.other"
	if _, err = call("data.query", query); err == nil {
		t.Fatal("foreign plugin accepted")
	}
	input.PluginID = pluginID
	input.ProjectKey = "local:11111111111141118111111111111111"
	if _, err = call("audit.finish", map[string]any{"outcome": "unknown", "errorCode": "plugin_commit_unknown"}); err == nil {
		t.Fatal("terminal audit crossed workspace")
	}
	if _, err = call("data.query", query); err == nil {
		t.Fatal("foreign workspace accepted")
	}
}

func TestSurfaceInstallationBindsPresentationEntry(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceMutationReplayFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	store := pluginstore.New(f.pb, workspace)
	pluginID, hash := "com.example.presentation-binding", strings.Repeat("e", 64)
	manifest := map[string]any{
		"$schema":       "vibetable.plugin-manifest.v2",
		"pluginId":      pluginID,
		"version":       "1.0.0",
		"displayName":   map[string]any{"zh-CN": "示例"},
		"description":   map[string]any{},
		"compatibility": map[string]any{"pluginApi": "3.x"},
		"permissions":   map[string]any{"privateStorage": true},
		"actions": []any{
			map[string]any{"actionId": "open", "mode": "local", "risk": "read", "invocation": "manual", "presentation": "application", "entry": "ui/index.html"},
			map[string]any{"actionId": "run", "mode": "local", "risk": "write", "invocation": "manual", "presentation": "headless", "entry": "ui/run.js"},
		},
		"ui": map[string]any{"customViews": []any{}},
	}
	snapshot, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash, "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}})
	if _, err := store.SaveInstallation(ctx, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	base := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1}

	for _, tc := range []struct {
		name     string
		actionID string
		entry    string
		wantRisk string
		wantDeny bool
	}{{
		name:     "application-entry",
		actionID: "open",
		entry:    "ui/index.html",
		wantRisk: "read",
	}, {
		name:     "headless-entry",
		actionID: "run",
		entry:    "ui/run.js",
		wantRisk: "write",
	}, {
		name:     "application-wrong-entry",
		actionID: "open",
		entry:    "ui/other.html",
		wantDeny: true,
	}, {
		name:     "headless-empty-entry",
		actionID: "run",
		entry:    "",
		wantDeny: true,
	}} {
		input := base
		input.ActionID = tc.actionID
		input.Entry = tc.entry
		installed, err := surfaceInstallation(ctx, f.pb, workspace, input)
		if tc.wantDeny {
			if err == nil {
				t.Fatal(tc.name, " admitted")
			}
			continue
		}
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if installed.ActionRisk != tc.wantRisk {
			t.Fatal(tc.name, " risk ", installed.ActionRisk)
		}
	}
}
