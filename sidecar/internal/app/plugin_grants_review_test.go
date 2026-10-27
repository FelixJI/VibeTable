package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestPluginGrantsUnionFieldsWithoutCrossOperationElevation(t *testing.T) {
	var installed pluginSurfaceInstallation
	installed.Manifest.Permissions.Data = []pluginReadGrant{
		{Collection: "$active", Operations: []string{"read", "query", "update"}, Fields: []string{"id"}},
		{Collection: "items", Operations: []string{"read", "query"}, Fields: []string{"private_read"}},
	}
	installed.ModelGrants = []pluginReadGrant{
		{Collection: "items", Operations: []string{"read", "query", "update"}, Fields: []string{"id", "name"}},
		{Collection: "items", Operations: []string{"read", "query"}, Fields: []string{"other_model"}},
		{Collection: "foreign", Operations: []string{"read", "query", "update"}, Fields: []string{"foreign"}},
	}
	read := installed.grant("items", "items")
	if read == nil || !pluginHas(read.Fields, "name") || !pluginHas(read.Fields, "private_read") || !pluginHas(read.Fields, "other_model") || pluginHas(read.Fields, "foreign") {
		t.Fatalf("read union: %+v", read)
	}
	input := pluginSurfaceAPIRequest{ActiveCollection: "items"}
	write := surfaceIOGrant(installed, input, "items", "update")
	if write == nil || !pluginHas(write.Fields, "name") || pluginHas(write.Fields, "private_read") || pluginHas(write.Fields, "other_model") || pluginHas(write.Fields, "foreign") {
		t.Fatalf("write union/elevation: %+v", write)
	}
	if surfaceIOGrant(installed, input, "items", "create") != nil || installed.grant("ungranted", "items") != nil {
		t.Fatal("ungranted operation/table admitted")
	}
	installed.ModelGrants = nil
	if pluginHas(installed.grant("items", "items").Fields, "name") {
		t.Fatal("revoked model fields retained")
	}
}

func TestPluginMappedReadGrantAndExplicitWritesShareTableWithoutElevation(t *testing.T) {
	pb, workspace, input, domain, preview, mapped := existingModelMapping(t)
	ctx := context.Background()
	editable := createSchemaProductField(t, pb, mapped.TableID, v2.LogicalText, "显式可写字段", "grant-editable")
	store := pluginstore.New(pb, workspace)
	raw, err := store.GetInstallation(ctx, input.ProjectKey, input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var installation map[string]any
	if err := json.Unmarshal(raw, &installation); err != nil {
		t.Fatal(err)
	}
	installation["manifest"].(map[string]any)["permissions"].(map[string]any)["data"] = []pluginReadGrant{
		{Collection: "$active", Operations: []string{"read", "query", "create", "update"}, Fields: []string{"id"}},
		{Collection: mapped.TableID, Operations: []string{"read", "query", "create", "update"}, Fields: []string{editable.FieldID}},
	}
	previous := input.InstallationRevision
	input.InstallationRevision++
	installation["revision"] = input.InstallationRevision
	raw, _ = json.Marshal(installation)
	if _, err := store.SaveInstallation(ctx, raw, &previous); err != nil {
		t.Fatal(err)
	}
	input.ActiveCollection = mapped.TableID
	result, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "map", Mappings: preview.Bindings})
	if err != nil || len(result.(modelPreview).Diagnostics) != 0 {
		t.Fatalf("mapping: %+v %v", result, err)
	}
	if _, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": result.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	describe, _ := json.Marshal(map[string]any{"accepts": []string{pluginDataContract}, "collection": mapped.TableID})
	metadata, err := executePluginData(ctx, pb, source, workspace, pluginDataRequest{InstallationID: input.InstallationID, PackageHash: input.PackageHash, InstallationRevision: input.InstallationRevision, ProjectKey: input.ProjectKey, PluginID: input.PluginID, ActiveCollection: input.ActiveCollection, Operation: "describe", Request: describe})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, field := range metadata["fields"].([]any) {
		fields[field.(map[string]any)["fieldId"].(string)] = true
	}
	if !fields["id"] || !fields[mapped.FieldID] || !fields[editable.FieldID] {
		t.Fatalf("mapped fields shadowed: %+v", fields)
	}
	input.Request, _ = json.Marshal(map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": mapped.TableID, "schemaRevision": editable.SchemaRevision, "operations": []any{map[string]any{"kind": "create", "values": map[string]any{editable.FieldID: "allowed"}}}})
	authorized, err := surfaceMutationRequest(ctx, pb, workspace, input)
	if err != nil {
		t.Fatalf("explicit same-operation union denied: %v", err)
	}
	input.Request, _ = json.Marshal(map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": mapped.TableID, "schemaRevision": editable.SchemaRevision, "operations": []any{map[string]any{"kind": "create", "values": map[string]any{mapped.FieldID: "read-only"}}}})
	if _, err := surfaceMutationRequest(ctx, pb, workspace, input); surfaceErrorCode(t, err) != "plugin_write_denied" {
		t.Fatalf("read-only model field elevated: %v", err)
	}
	if _, err := store.SetEnabled(ctx, input.ProjectKey, input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	if err := authorized.Authorize(ctx, pb); err == nil {
		t.Fatal("revoked installation retained transaction authorization")
	}
}
