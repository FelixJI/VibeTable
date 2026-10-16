package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

// The subscription authorization gate pins the closed contract: only a
// currently enabled installation (matching id/revision/package) with an
// explicit read+query grant receives the authorized collection identity —
// never revisions or row fields — while revocation, missing grants
// and unknown tables fail closed.
func TestPluginSurfaceAuthorizeSubscriptionReturnsOnlyAuthorizedCollections(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceMutationReplayFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	store := pluginstore.New(f.pb, workspace)
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
	pluginID, hash := "com.example.surface-subscribe", strings.Repeat("f", 64)
	manifest := func(data ...map[string]any) map[string]any {
		return map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{},
			"compatibility": map[string]any{"pluginApi": "3.x"},
			"permissions":   map[string]any{"data": data},
			"actions":       []any{map[string]any{"actionId": "save", "risk": "read", "mode": "local", "invocation": "manual"}},
			"ui":            map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
	}
	save := func(m map[string]any, revision int64, expected *int64) {
		t.Helper()
		if _, err := store.SaveInstallation(ctx, installationSnapshot(t, store.ProjectKey(), pluginID, hash, m, revision, "enabled"), expected); err != nil {
			t.Fatal(err)
		}
	}
	granted := map[string]any{"collection": f.request.TableID, "operations": []string{"read", "query"}, "fields": []string{"*"}}
	writeOnly := map[string]any{"collection": f.request.TableID, "operations": []string{"create", "update", "delete"}, "fields": []string{"*"}}
	save(manifest(granted), 1, nil)
	// PocketBase owns the installation identity; echo the persisted id exactly
	// as the Host does after a real install.
	savedInstallation, identityErr := store.GetInstallation(ctx, store.ProjectKey(), pluginID)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	var identity struct {
		InstallationID string `json:"installationId"`
	}
	if json.Unmarshal(savedInstallation, &identity) != nil || identity.InstallationID == "" {
		t.Fatal("installation identity missing")
	}
	input := pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, InstallationID: identity.InstallationID}
	call := func(collection string) (map[string]any, error) {
		input.Method = "data.authorizeSubscription"
		input.Request, _ = json.Marshal(map[string]any{"collection": collection})
		output, err := executePluginSurfaceAPI(ctx, f.pb, source, kernel, workspace, input, f.runtime.CoordinateBusinessWrite)
		if output == nil {
			return nil, err
		}
		result, ok := output.(map[string]any)
		if !ok {
			t.Fatal("unexpected authorizeSubscription result type")
		}
		return result, err
	}

	if _, err := call(""); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
		t.Fatal("empty collection must be rejected")
	}
	authorized, err := call(f.request.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if authorized["collection"] != f.request.TableID || len(authorized) != 1 {
		t.Fatalf("authorized subscription must carry only collection, got %v", authorized)
	}
	if _, err := call(strings.Repeat("a", 129)); surfaceErrorCode(t, err) != "plugin_capability_invalid" {
		t.Fatal("unbounded table id was admitted")
	}
	if _, err := call("missing-table"); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatal("unknown table must be denied")
	}

	originalID := input.InstallationID
	input.InstallationID = "another-installation"
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_cursor_stale" {
		t.Fatal("replacement installation admitted old subscription")
	}
	input.InstallationID = originalID
	input.PackageHash = strings.Repeat("0", 64)
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatal("stale package admitted subscription")
	}
	input.PackageHash = hash
	disabledRevision := int64(1)
	if _, err := store.SaveInstallation(ctx, installationSnapshot(t, store.ProjectKey(), pluginID, hash, manifest(granted), 2, "disabled"), &disabledRevision); err != nil {
		t.Fatal(err)
	}
	input.InstallationRevision = 2
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatal("disabled installation admitted subscription")
	}
	restoredRevision := int64(2)
	save(manifest(granted), 3, &restoredRevision)
	input.InstallationRevision = 3

	lifecycle, err := schemacore.NewTableLifecycle(f.pb)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "hidden subscription table", OperationID: "hidden-subscription", Actor: v2.Actor{Kind: "user", ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := call(hidden.TableID); surfaceErrorCode(t, err) != "plugin_read_denied" || result != nil {
		t.Fatal("hidden existing table leaked subscription metadata")
	}

	// A write-only grant never authorizes a subscription.
	readOnly := map[string]any{"collection": f.request.TableID, "operations": []string{"read"}, "fields": []string{"*"}}
	expected := int64(3)
	save(manifest(readOnly), 4, &expected)
	input.InstallationRevision = 4
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatal("read without query grant admitted subscription")
	}
	expected = 4
	save(manifest(writeOnly), 5, &expected)
	input.InstallationRevision = 5
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatal("write-only grant must not authorize subscriptions")
	}

	// A stale installation identity fails closed even with a read grant.
	input.InstallationRevision = 6
	if _, err := call(f.request.TableID); surfaceErrorCode(t, err) != "plugin_cursor_stale" {
		t.Fatal("stale installation revision must be rejected")
	}
}
