package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

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
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"data": []any{map[string]any{"collection": table.TableID, "operations": []string{"read", "query", "create", "update"}, "fields": []string{"id", amount.Identity.FieldID, flag.Identity.FieldID}}}, "privateStorage": true}, "actions": []any{}, "ui": map[string]any{}}
	snapshot := map[string]any{"projectKey": store.ProjectKey(), "pluginId": pluginID, "version": "1.0.0", "packageHash": "sha256:" + hash, "sourceType": "package", "sourceLocation": "host-managed", "manifest": manifest, "schemas": map[string]any{}, "status": "enabled", "revision": 1, "sourceChanged": false, "blockingReasons": []any{}}
	raw, _ := json.Marshal(snapshot)
	if _, err = store.SaveInstallation(ctx, raw, nil); err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{})
	input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, ActiveCollection: table.TableID, RequestID: "surface-test-read"}
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
	input.RequestID = "surface-single-commit"
	receipt, err := call("mutation.commit", plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.(mutation.Receipt).Status != mutation.StatusApplied {
		t.Fatalf("commit %#v", receipt)
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
	if _, err = call("mutation.commit", plan); err == nil {
		t.Fatal("disabled installation accepted replay")
	}
	input.PluginID = "com.example.other"
	if _, err = call("data.query", query); err == nil {
		t.Fatal("foreign plugin accepted")
	}
	input.PluginID = pluginID
	input.ProjectKey = "local:11111111111141118111111111111111"
	if _, err = call("data.query", query); err == nil {
		t.Fatal("foreign workspace accepted")
	}
}
