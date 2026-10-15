package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
)

const workerTestWorkspace = "11111111-1111-4111-8111-111111111111"

func workerMutationFixture(t *testing.T) (workspaceMutationReplayFixture, *pluginstore.Service, pluginWorkerMutationRequest, []byte) {
	t.Helper()
	f := newWorkspaceMutationReplayFixture(t)
	store := pluginstore.New(f.pb, "11111111-1111-4111-8111-111111111111")
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v1", "pluginId": "com.example.worker", "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "2.x"}, "permissions": map[string]any{"data": []any{map[string]any{"collection": f.request.TableID, "operations": []string{"create", "update", "read", "query"}, "fields": []string{"*"}}}}, "ui": map[string]any{}, "actions": []any{map[string]any{"actionId": "save", "risk": "write", "mode": "local", "invocation": "manual", "workerEntry": "worker.js"}}}
	raw, _ := json.Marshal(map[string]any{"projectKey": store.ProjectKey(), "pluginId": "com.example.worker", "version": "1.0.0", "packageHash": "sha256:" + strings.Repeat("c", 64), "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []string{}})
	snapshot, err := store.SaveInstallation(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := pluginWorkerMutationRequest{ProjectKey: store.ProjectKey(), PluginID: "com.example.worker", InstallationID: installationRecordID(t, store, "com.example.worker"), InstallationRevision: 1,
		PackageHash: "sha256:" + strings.Repeat("c", 64), ActionID: "save", SessionEpoch: 7, ActiveCollection: f.request.TableID, Request: f.request}
	return f, store, input, snapshot
}

func TestWorkerMutationRejectsLifecycleChangeWhileWaitingForBusinessGate(t *testing.T) {
	for _, transition := range []string{"disabled", "revision", "uninstall", "reinstall"} {
		t.Run(transition, func(t *testing.T) {
			f, store, input, snapshot := workerMutationFixture(t)
			ctx := context.Background()
			before := f.state(t)
			gate := func(ctx context.Context, kind, id string, fn func(context.Context) error) error {
				var err error
				switch transition {
				case "disabled":
					_, err = store.SetEnabled(ctx, input.ProjectKey, input.PluginID, false)
				case "revision":
					var next map[string]any
					_ = json.Unmarshal(snapshot, &next)
					next["revision"] = 2
					raw, _ := json.Marshal(next)
					expected := int64(1)
					_, err = store.SaveInstallation(ctx, raw, &expected)
				default:
					_, err = store.DeleteInstallation(ctx, input.ProjectKey, input.PluginID, input.InstallationID, 1, false)
					if err == nil && transition == "reinstall" {
						var next map[string]any
						_ = json.Unmarshal(snapshot, &next)
						delete(next, "installationId")
						raw, _ := json.Marshal(next)
						_, err = store.SaveInstallation(ctx, raw, nil)
					}
				}
				if err != nil {
					return err
				}
				before = f.state(t)
				return f.runtime.CoordinateBusinessWrite(ctx, kind, id, fn)
			}
			receipt, err := executePluginWorkerMutation(ctx, f.pb, mutation.New(f.pb, mutation.MetadataSchemaSource{}), workerTestWorkspace, input, gate)
			if err == nil || receipt.Status != "" {
				t.Fatalf("retired Worker committed %s: %+v %v", transition, receipt, err)
			}
			if !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("denied Worker wrote rows or receipts")
			}
		})
	}
}

func TestWorkerMutationUsesCurrentGrantsAndKeepsCommittedReplayProof(t *testing.T) {
	for _, invalid := range []string{"action", "package", "field", "grant", "none"} {
		t.Run(invalid, func(t *testing.T) {
			f, store, input, snapshot := workerMutationFixture(t)
			if invalid == "action" {
				input.ActionID = "other"
			}
			if invalid == "package" {
				input.PackageHash = strings.Repeat("f", 64)
			}
			if invalid == "field" {
				input.Request.Operations[0].Values = map[string]any{"undeclared": "bad"}
			}
			if invalid == "grant" {
				var payload map[string]any
				_ = json.Unmarshal(snapshot, &payload)
				payload["manifest"].(map[string]any)["permissions"].(map[string]any)["data"] = []any{map[string]any{"collection": input.Request.TableID, "operations": []string{"create", "update"}, "fields": []string{"id"}}}
				raw, _ := json.Marshal(payload)
				expected := int64(1)
				if _, err := store.SaveInstallation(context.Background(), raw, &expected); err != nil {
					t.Fatal(err)
				}
			}
			kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
			first, err := executePluginWorkerMutation(context.Background(), f.pb, kernel, workerTestWorkspace, input, f.runtime.CoordinateBusinessWrite)
			if invalid != "none" {
				if err == nil {
					t.Fatal("invalid Worker authority accepted")
				}
				return
			}
			if err != nil || first.Status != mutation.StatusApplied {
				t.Fatal(first, err)
			}
			before := f.state(t)
			replay, err := executePluginWorkerMutation(context.Background(), f.pb, kernel, workerTestWorkspace, input, f.runtime.CoordinateBusinessWrite)
			if err != nil || replay.Status != mutation.StatusReplayed || !reflect.DeepEqual(before, f.state(t)) {
				t.Fatal("Worker mutation replay changed data", err)
			}
			if _, err := store.SetEnabled(context.Background(), input.ProjectKey, input.PluginID, false); err != nil {
				t.Fatal(err)
			}
			late, err := executePluginWorkerMutation(context.Background(), f.pb, kernel, workerTestWorkspace, input, f.runtime.CoordinateBusinessWrite)
			if err == nil || pluginCommitOutcome(context.Background(), f.pb, workerTestWorkspace, "plugin.worker.mutation", input.Request.IdempotencyKey, late, "plugin_cursor_stale") != "unknown" {
				t.Fatal("committed receipt was misreported as rollback")
			}
		})
	}
}

func TestWorkerMutationHTTPIsSessionClosedAndEpochBound(t *testing.T) {
	f, _, input, _ := workerMutationFixture(t)
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: f.pb, Event: router.Event{Response: w, Request: req}}, nil
	})
	secret, err := auth.Parse(strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	bindVibetableSessionAuth(r, secret)
	calls := 0
	registerPluginWorkerMutationRoutes(r, f.pb, mutation.New(f.pb, mutation.MetadataSchemaSource{}), workerTestWorkspace, 7, func(ctx context.Context, kind, id string, fn func(context.Context) error) error {
		calls++
		return f.runtime.CoordinateBusinessWrite(ctx, kind, id, fn)
	})
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"no-secret", "wrong-epoch", "unknown-field", "valid"} {
		envelope := input
		if mode == "wrong-epoch" {
			envelope.SessionEpoch = 6
		}
		raw, _ := json.Marshal(envelope)
		if mode == "unknown-field" {
			raw = append(raw[:len(raw)-1], []byte(`,"privileged":true}`)...)
		}
		req := httptest.NewRequest(http.MethodPost, pluginWorkerMutationPath, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if mode != "no-secret" {
			req.Header.Set(auth.HeaderName, strings.Repeat("2", 64))
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		expected := http.StatusBadRequest
		if mode == "no-secret" {
			expected = http.StatusUnauthorized
		}
		if mode == "valid" {
			expected = http.StatusOK
		}
		if response.Code != expected {
			t.Fatalf("%s: %d %s", mode, response.Code, response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("invalid admission reached authority %d times", calls)
	}
}
