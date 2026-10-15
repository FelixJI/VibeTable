package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func surfaceIOInstallation(t *testing.T, app core.App, workspace, table string, fields []string) (pluginSurfaceAPIRequest, *pluginstore.Service) {
	t.Helper()
	ctx := context.Background()
	store := pluginstore.New(app, workspace)
	pluginID, hash := "com.example.files", strings.Repeat("a", 64)
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"}, "permissions": map[string]any{"files": []string{"pickRead", "pickWrite"}, "data": []any{map[string]any{"collection": table, "operations": []string{"read", "query", "create", "update"}, "fields": fields}}}, "actions": []any{map[string]any{"actionId": "save", "risk": "write", "mode": "local", "invocation": "manual"}}, "ui": map[string]any{"customViews": []any{map[string]any{"viewId": "save", "actionId": "save", "entry": "ui/index.html"}}}}
	if _, err := store.SaveInstallation(ctx, installationSnapshot(t, store.ProjectKey(), pluginID, hash, manifest, 1, "enabled"), nil); err != nil {
		t.Fatal(err)
	}
	return pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, ActionID: "save", Entry: "ui/index.html", PackageHash: hash, SessionEpoch: 7, InstallationRevision: 1, InstallationID: installationRecordID(t, store, pluginID), ActiveCollection: table, SurfaceToken: "surface-a", RequestID: "files-request"}, store
}

func TestPluginSurfaceImportFreezesUpsertAndRechecksAuthoritativeFieldGrants(t *testing.T) {
	ctx := context.Background()
	f := newWorkspaceMutationReplayFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	defaults, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	defaults.Constraints.Unique.Enabled = true
	key := applySchemaProductField(t, f.pb, v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: f.request.TableID, Draft: &v2.FieldDraft{DisplayName: "Unique key", LogicalType: v2.LogicalText, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display}}, "files-key").Definition
	secret := createSchemaProductField(t, f.pb, f.request.TableID, v2.LogicalText, "Hidden secret", "files-secret").Definition
	table, err := schemaexecution.Describe(ctx, f.pb, f.request.TableID)
	if err != nil {
		t.Fatal(err)
	}
	name := f.definition.Snapshot.Fields[0].Identity.FieldID
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
	existingID := "filesexisting01"
	user := mutation.Request{ContractVersion: "2.0", RequestID: "files-seed", IdempotencyKey: "files-seed", TableID: table.Snapshot.TableID, SchemaRevision: table.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "test"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &existingID, Values: map[string]any{key.Identity.FieldID: "existing", name: "before"}}}}
	if _, err = kernel.Apply(ctx, user); err != nil {
		t.Fatal(err)
	}
	input, store := surfaceIOInstallation(t, f.pb, workspace, table.Snapshot.TableID, []string{"id", name, key.Identity.FieldID})
	ports := &pluginSurfaceIO{plans: newImportPlanOwner(workspace)}
	call := func(method string, request any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(request)
		return executePluginSurfaceAPI(ctx, f.pb, source, kernel, workspace, input, nil, ports)
	}
	if _, err = call("data.ioProfile", map[string]any{"collection": table.Snapshot.TableID, "operation": "read", "fields": []string{secret.Identity.FieldID}}); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatalf("hidden field profile: %v", err)
	}
	profile, err := call("data.ioProfile", map[string]any{"collection": table.Snapshot.TableID, "operation": "read", "fields": []string{name}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(profile)
	if strings.Contains(string(raw), secret.Identity.FieldID) {
		t.Fatal("hidden field leaked into profile")
	}
	mint := func(values map[string]any) (any, error) {
		return call("import.mint", map[string]any{"contract": importPlanContract, "collection": table.Snapshot.TableID, "grantId": "host-source", "schemaRevision": table.Snapshot.SchemaRevision, "capabilityHash": table.Snapshot.SchemaRevision, "sourceHash": "existing-source-contract", "mode": "upsert", "upsertKey": key.Identity.FieldID, "rows": []any{map[string]any{"sourceRow": 2, "values": values, "diagnostics": []any{}, "relationResolutions": []any{}}}})
	}
	if _, err = mint(map[string]any{key.Identity.FieldID: "existing", secret.Identity.FieldID: "leak"}); surfaceErrorCode(t, err) != "plugin_write_denied" {
		t.Fatalf("hidden import field: %v", err)
	}

	created, createErr := mint(map[string]any{key.Identity.FieldID: "new-key", name: "created"})
	if createErr != nil {
		t.Fatal(createErr)
	}
	createdToken := created.(importPlanTokenReply)
	createdFrozen, createErr := call("import.freeze", map[string]any{"token": createdToken.Token, "grantId": "host-source", "collection": table.Snapshot.TableID})
	if createErr != nil {
		t.Fatal(createErr)
	}
	input.Request = createdFrozen.(map[string]any)["plan"].(json.RawMessage)
	input.RequestID = "files-legal-import"
	createdRequest, createErr := surfaceMutationRequest(ctx, f.pb, workspace, input, ports)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if _, createErr = kernel.Apply(ctx, createdRequest); createErr != nil {
		t.Fatal(createErr)
	}
	input.RequestID = "files-request"
	exported, exportErr := call("data.exportQuery", map[string]any{"contract": "vibetable.plugin-query.v2", "collection": table.Snapshot.TableID, "fields": []string{name}, "pageSize": 100})
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	exportPage := exported.(map[string]any)
	if _, exists := exportPage["clockPeriod"]; !exists {
		t.Fatal("export dropped volatile snapshot identity")
	}
	for _, rawRow := range exportPage["items"].([]any) {
		row := rawRow.(map[string]any)
		if len(row) != 1 || row["id"] != nil || row[secret.Identity.FieldID] != nil {
			t.Fatal("export projected hidden fields")
		}
	}
	token, err := mint(map[string]any{key.Identity.FieldID: "existing", name: "after"})
	if err != nil {
		t.Fatal(err)
	}
	minted := token.(importPlanTokenReply)
	frozen, err := call("import.freeze", map[string]any{"token": minted.Token, "grantId": "host-source", "collection": table.Snapshot.TableID})
	if err != nil {
		t.Fatal(err)
	}
	plan := frozen.(map[string]any)["plan"].(json.RawMessage)
	var planned struct {
		Operations []struct{ Kind, PrimaryKey, ExpectedDigest string }
	}
	if json.Unmarshal(plan, &planned) != nil || planned.Operations[0].Kind != "update" || planned.Operations[0].PrimaryKey != existingID || planned.Operations[0].ExpectedDigest == "" {
		t.Fatalf("upsert was not frozen: %s", plan)
	}
	changed := user
	changed.RequestID = "files-concurrent"
	changed.IdempotencyKey = changed.RequestID
	changed.Operations = []mutation.Operation{{Kind: mutation.OperationUpdate, RecordID: &existingID, Values: map[string]any{name: "concurrent"}}}
	if _, err = kernel.Apply(ctx, changed); err != nil {
		t.Fatal(err)
	}
	input.Request = plan
	checked, err := surfaceMutationRequest(ctx, f.pb, workspace, input, ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = kernel.Apply(ctx, checked); surfaceErrorCode(t, err) != "mutation.digest_conflict" {
		t.Fatalf("frozen target rematched instead of conflicting: %v", err)
	}
	page, err := query.NewPort(f.pb, source).QueryPage(ctx, table.Snapshot.TableID, query.TableQuery{Limit: 100})
	if err != nil || len(page.Rows) != 2 {
		t.Fatalf("invalid import wrote rows: %+v %v", page, err)
	}
	input.SurfaceToken = "surface-b"
	if _, err = call("import.freeze", map[string]any{"token": minted.Token, "grantId": "host-source", "collection": table.Snapshot.TableID}); surfaceErrorCode(t, err) != "plugin_plan_stale" {
		t.Fatalf("token transferred between surfaces: %v", err)
	}
	input.SurfaceToken = "surface-a"
	input.InstallationID = "replacement-installation"
	if _, err = call("file.authorize", map[string]any{"operation": "pickRead"}); surfaceErrorCode(t, err) != "plugin_cursor_stale" {
		t.Fatal("installation ABA admitted")
	}
	input.InstallationID = installationRecordID(t, store, input.PluginID)
	saved, _ := store.GetInstallation(ctx, store.ProjectKey(), input.PluginID)
	var disabled map[string]any
	_ = json.Unmarshal(saved, &disabled)
	disabled["status"] = "disabled"
	disabled["revision"] = 2
	revision := int64(1)
	bytes, _ := json.Marshal(disabled)
	if _, err = store.SaveInstallation(ctx, bytes, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err = kernel.Apply(ctx, checked); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatalf("revoked installation reached canonical import commit: %v", err)
	}
	if _, err = call("data.ioProfile", map[string]any{"collection": table.Snapshot.TableID, "operation": "read", "fields": []string{name}}); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatalf("revoked export allowed: %v", err)
	}
}

func TestPluginSurfaceAttachmentsBindOwnerFieldAndUseCanonicalKernel(t *testing.T) {
	ctx := context.Background()
	f := newFileListFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	input, _ := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
	ports := &pluginSurfaceIO{attachments: f.manager}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(f.manager))
	call := func(method string, request any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(request)
		return executePluginSurfaceAPI(ctx, f.pb, source, kernel, workspace, input, nil, ports)
	}
	stage := map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "name": "synthetic.png", "base64": base64.StdEncoding.EncodeToString(f.content), "handle": "host-upload", "fileGrantId": "host-file", "runId": "host-run"}
	if _, err = call("attachment.stage", stage); err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(f.pb, source).QueryPage(ctx, f.tableID, query.TableQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	digest := page.Rows[0][productrow.DigestField]
	table, err := schemaexecution.Describe(ctx, f.pb, f.tableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := json.Marshal(map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": f.tableID, "schemaRevision": table.Snapshot.SchemaRevision, "operations": []any{map[string]any{"kind": "setAttachments", "primaryKey": f.recordID, "expectedDigest": digest, "fieldId": f.fieldID, "uploadHandles": []string{"host-upload"}, "removeStoredNames": []string{}}}})
	input.Request = plan
	input.SurfaceToken = "surface-b"
	if _, err = surfaceMutationRequest(ctx, f.pb, workspace, input, ports); surfaceErrorCode(t, err) != "plugin_write_denied" {
		t.Fatalf("stage transferred surfaces: %v", err)
	}
	input.SurfaceToken = "surface-a"
	request, err := surfaceMutationRequest(ctx, f.pb, workspace, input, ports)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = kernel.Apply(ctx, request); err != nil {
		t.Fatal(err)
	}
	listed, err := call("attachment.list", map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), "downloadCapability") || strings.Contains(string(raw), "sha256") {
		t.Fatalf("internal ticket leaked: %s", raw)
	}
	refs, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
	if err != nil || len(refs) != 1 {
		t.Fatal(err)
	}
	block, err := call("attachment.read", map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "storedName": refs[0].StoredName, "offset": 0})
	if err != nil {
		t.Fatal(err)
	}
	if block.(map[string]any)["base64"] != base64.StdEncoding.EncodeToString(f.content) {
		t.Fatal("attachment read differs")
	}
	if _, err = call("attachment.read", map[string]any{"collection": f.tableID, "recordId": "other-record", "fieldId": f.fieldID, "storedName": refs[0].StoredName, "offset": 0}); err == nil {
		t.Fatal("attachment crossed records")
	}
	if _, err = call("attachment.drop", map[string]any{"handles": []string{"host-upload"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = surfaceMutationRequest(ctx, f.pb, workspace, func() pluginSurfaceAPIRequest { copy := input; copy.Request = plan; return copy }(), ports); surfaceErrorCode(t, err) != "plugin_write_denied" {
		t.Fatal("dropped stage reused")
	}
}

func TestPluginSurfaceImportCleanupKeepsUnknownAndReleasesOnlyOwnedUnsubmittedPlans(t *testing.T) {
	owner := newImportPlanOwner("workspace")
	ports := &pluginSurfaceIO{plans: owner}
	input := pluginSurfaceAPIRequest{PluginID: "plugin", InstallationID: "install", InstallationRevision: 1, SessionEpoch: 7, SurfaceToken: "surface", ProjectKey: "workspace"}
	mint := func() importPlanTokenReply {
		t.Helper()
		token, err := owner.mint(importPlanMintRequest{Contract: importPlanContract, Collection: "table", GrantID: "grant", SchemaRevision: "schema", CapabilityHash: "schema", SourceHash: "existing-source-contract", Mode: "create_only", Rows: []json.RawMessage{json.RawMessage(`{}`)}, pluginOwner: surfaceIOOwner(input)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	drop := func(surface string) {
		t.Helper()
		copy := input
		copy.Method = "import.drop"
		copy.SurfaceToken = surface
		copy.Request = json.RawMessage(`{"grantId":"grant"}`)
		if _, err := ports.cleanupImport(copy); err != nil {
			t.Fatal(err)
		}
	}
	token := mint()
	drop("foreign")
	if owner.plans[token.Token] == nil {
		t.Fatal("foreign surface dropped plan")
	}
	drop("surface")
	if owner.plans[token.Token] != nil {
		t.Fatal("unsubmitted plan leaked")
	}
	for i := 0; i < 20; i++ {
		mint()
		drop("surface")
	}
	token = mint()
	claim, err := owner.stage(importPlanStageRequest{Contract: importPlanContract, Token: token.Token, GrantID: "grant", Collection: "table", Mode: "create_only", CapabilityHash: "schema"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.bind(importPlanBindRequest{Contract: importPlanContract, Token: token.Token, Attempt: claim.Attempt, IdempotencyPrefix: "native-run"}); err != nil {
		t.Fatal(err)
	}
	drop("surface")
	if owner.plans[token.Token] == nil {
		t.Fatal("bound plan lost unknown recovery state")
	}
	input.Method = "import.settle"
	input.Request, _ = json.Marshal(importPlanSettleRequest{Contract: importPlanContract, Token: token.Token, Attempt: claim.Attempt, Outcome: "unknown"})
	if _, err = ports.cleanupImport(input); err != nil {
		t.Fatal(err)
	}
	if !owner.plans[token.Token].inFlight {
		t.Fatal("unknown released its claim")
	}
	input.Request, _ = json.Marshal(importPlanSettleRequest{Contract: importPlanContract, Token: token.Token, Attempt: claim.Attempt, Outcome: "committed"})
	if _, err = ports.cleanupImport(input); err != nil {
		t.Fatal(err)
	}
	if owner.plans[token.Token] != nil {
		t.Fatal("settled plugin plan kept its capacity slot")
	}
}
