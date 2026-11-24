package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestPluginSurfaceOutputArchiveIsAtomicImmutableAndReopensOriginalBytes(t *testing.T) {
	f := newFileListFixtureWithLimit(t, surfaceOutputMaxBytes)
	workspace := "11111111-1111-4111-8111-111111111111"
	input, store := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
	// Synthetic padding exercises the independent >1 MiB archive path.
	f.content = append(f.content, bytes.Repeat([]byte("synthetic"), 150000)...)
	checkOutputArchive(t, f, workspace, input, store, "issued.png", "image/png")
}

func TestModelOutputOnlyGrantsArchiveAndReopen(t *testing.T) {
	pb, workspace, input, domain := modelFixture(t)
	input = changeInstalledModel(t, pb, workspace, input, func(m *pluginmodel.Declaration) {
		f := modelArchiveField()
		f.File.AllowedMIMETypes = []string{xlsxArchiveMIME}
		f.File.MaxBytesPerFile = 1 << 20
		m.Entities[0].Fields = append(m.Entities[0].Fields, f)
		m.Entities[2].Fields = append(m.Entities[2].Fields, pluginmodel.Field{Key: f.Key, Label: f.Label, Type: "text"})
	})
	input.SurfaceToken = "surface-a"
	preview, err := modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.(modelPreview).RequestID})
	if err != nil {
		t.Fatal(err)
	}
	bindings := done.(pluginmodel.State).Bindings
	owner := bindings["people"]
	store := pluginstore.New(pb, workspace)
	input = changeDataGrants(t, store, input, []pluginReadGrant{
		{Collection: "$active", Operations: []string{"read", "query"}, Fields: []string{"*"}},
		{Collection: owner.TableID, Operations: []string{"read", "query", "update"}, Fields: []string{owner.Fields["archive_file"]}},
	})
	input.ActiveCollection = owner.TableID
	preview, err = modelCall(t, pb, workspace, input, domain, "model.preview", modelPreviewRequest{Mode: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = modelCall(t, pb, workspace, input, domain, "model.commit", map[string]any{"requestId": preview.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
	extension := createSchemaProductField(t, pb, owner.TableID, "text", "用户备注", "output-read-extension").FieldID
	schema, err := schemaexecution.Describe(t.Context(), pb, owner.TableID)
	if err != nil {
		t.Fatal(err)
	}
	id := "issuedrecord001"
	manager := mustAttachmentManager(t)
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(manager))
	_, err = kernel.Apply(t.Context(), mutation.Request{ContractVersion: "2.0", RequestID: "issued-seed", IdempotencyKey: "issued-seed", TableID: owner.TableID, SchemaRevision: schema.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{owner.Fields["name"]: "synthetic", extension: "extra"}}}})
	if err != nil {
		t.Fatal(err)
	}
	field := owner.Fields["archive_file"]
	installed, err := surfaceInstallation(t.Context(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "create", "update"} {
		grant := installed.grantFor(owner.TableID, input.ActiveCollection, op)
		if grant == nil || pluginAllowed(*grant, schema.Snapshot)[field] != "" {
			t.Fatalf("ordinary %s grant: %+v", op, grant)
		}
	}
	ports := &pluginSurfaceIO{attachments: manager}
	for _, method := range []string{"attachment.list", "attachment.read", "attachment.stage"} {
		input.Method = method
		input.Request, _ = json.Marshal(map[string]any{"collection": owner.TableID, "recordId": id, "fieldId": field})
		if _, err := executeSurfaceAttachment(t.Context(), pb, workspace, input, ports); surfaceErrorCode(t, err) != "plugin_read_denied" {
			t.Fatalf("%s: %v", method, err)
		}
	}
	// Even a caller who knows a stored name or an unrelated staged handle cannot replace an issued file.
	guard := "guard"
	for _, op := range []map[string]any{
		{"kind": "setAttachments", "removeStoredNames": []string{"already-issued"}},
		{"kind": "setAttachments", "uploadHandles": []string{"unrelated"}},
		{"kind": "update", "values": map[string]any{field: "raw"}},
	} {
		op["primaryKey"], op["expectedDigest"] = id, guard
		if op["kind"] == "setAttachments" {
			op["fieldId"] = field
		}
		input.Request, _ = json.Marshal(map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": owner.TableID, "schemaRevision": schema.Snapshot.SchemaRevision, "operations": []any{op}})
		if _, err := surfaceMutationRequest(t.Context(), pb, workspace, input, ports); surfaceErrorCode(t, err) != "plugin_write_denied" {
			t.Fatalf("accepted unauthorized mutation: %v", err)
		}
	}
	input.Method = "output.stage.begin"
	for _, tc := range []struct {
		size              int
		mime, field, code string
	}{
		{1<<20 + 1, xlsxArchiveMIME, field, "mutation.attachment.too_large"},
		{100, "image/png", field, "mutation.attachment.mime_not_allowed"},
		{100, xlsxArchiveMIME, owner.Fields["name"], "plugin_write_denied"},
	} {
		input.Request, _ = json.Marshal(map[string]any{"handle": "probe", "artifactId": "probe", "collection": owner.TableID, "recordId": id, "fieldId": tc.field, "name": "issued.xlsx", "mediaType": tc.mime, "sizeBytes": tc.size, "source": surfaceOutputSource{"t", "d", "test"}})
		if _, err := executeSurfaceOutput(t.Context(), pb, workspace, input, ports); surfaceErrorCode(t, err) != tc.code {
			t.Fatalf("archive field policy: want %s got %v", tc.code, err)
		}
	}
	checkOutputArchive(t, fileListFixture{pb: pb, manager: manager, tableID: owner.TableID, recordID: id, fieldID: field, content: syntheticLedgerXLSX(t)}, workspace, input, store, "issued.xlsx", xlsxArchiveMIME)
	checkOutputDataProjections(t, pb, workspace, input, bindings, extension)
}

func checkOutputDataProjections(t *testing.T, pb core.App, workspace string, input pluginSurfaceAPIRequest, bindings map[string]pluginmodel.Binding, extension string) {
	t.Helper()
	owner := bindings["people"]
	field := owner.Fields["archive_file"]
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	read := func(method string, request any) (map[string]any, error) {
		raw, _ := json.Marshal(request)
		return executePluginData(t.Context(), pb, source, workspace, pluginDataRequest{InstallationID: input.InstallationID, PackageHash: input.PackageHash, InstallationRevision: input.InstallationRevision, ProjectKey: input.ProjectKey, PluginID: input.PluginID, ActiveCollection: input.ActiveCollection, Operation: method, Request: raw})
	}
	describe, err := read("describe", map[string]any{"collection": owner.TableID, "accepts": []string{pluginDataContract}})
	if err != nil {
		t.Fatal(err)
	}
	visible := map[string]bool{}
	for _, f := range describe["fields"].([]any) {
		visible[f.(map[string]any)["fieldId"].(string)] = true
	}
	if visible[field] || !visible[owner.Fields["name"]] || !visible[extension] {
		t.Fatalf("mixed grant metadata: %v", visible)
	}
	q := pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: owner.TableID}
	for _, op := range []string{"query", "exportQuery"} {
		q.Fields = []string{field}
		if _, err := read(op, q); surfaceErrorCode(t, err) != "plugin_read_denied" {
			t.Fatalf("%s exposed archive file: %v", op, err)
		}
		q.Fields = []string{owner.Fields["name"], extension}
		result, err := read(op, q)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		rows := result["items"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)[owner.Fields["name"]] != "synthetic" || rows[0].(map[string]any)[extension] != "extra" {
			t.Fatalf("%s lost ordinary data: %v %v", op, result, err)
		}
	}
	// The canonical explicit grant still applies when the wildcard refers to another table.
	other := bindings["assets"]
	input.ActiveCollection = other.TableID
	dataset := func(fieldID string) (map[string]any, error) {
		q.Fields = []string{fieldID}
		return read("dataset.open", pluginDatasetRequest{Contract: pluginDatasetContract, Sources: []pluginDatasetSource{{Query: q}}})
	}
	if _, err := dataset(field); surfaceErrorCode(t, err) != "plugin_read_denied" {
		t.Fatalf("dataset exposed explicitly granted archive field: %v", err)
	}
	if _, err := dataset(owner.Fields["name"]); err != nil {
		t.Fatalf("dataset lost ordinary field: %v", err)
	}
	q.Collection, q.Fields = other.TableID, []string{other.Fields["archive_file"]}
	if _, err := read("query", q); err != nil {
		t.Fatalf("same logical field name in another table: %v", err)
	}
}

const xlsxArchiveMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func syntheticLedgerXLSX(t *testing.T) []byte {
	t.Helper()
	files := os.DirFS("../../../tests/fixtures/ooxml/xlsx-ledger")
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, part := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/sharedStrings.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		source, err := fs.ReadFile(files, part)
		if err != nil {
			t.Fatal(err)
		}
		entry, err := w.Create(part)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(source); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func checkOutputArchive(t *testing.T, f fileListFixture, workspace string, input pluginSurfaceAPIRequest, store *pluginstore.Service, name, mediaType string) {
	t.Helper()
	ctx := t.Context()
	ports := &pluginSurfaceIO{attachments: f.manager}
	call := func(method string, request any) (any, error) {
		input.Method = method
		input.Request, _ = json.Marshal(request)
		return executeSurfaceOutput(ctx, f.pb, workspace, input, ports)
	}
	content := f.content
	sourceMetadata := surfaceOutputSource{"template-v1", "business-revision-7", "issued-copy"}
	begin := map[string]any{"handle": "output-upload-one", "artifactId": "output-issued-one", "collection": f.tableID, "recordId": f.recordID,
		"fieldId": f.fieldID, "name": name, "mediaType": mediaType, "sizeBytes": len(content), "source": sourceMetadata}
	if _, err := call("output.stage.begin", begin); err != nil {
		t.Fatal(err)
	}
	input.Request, _ = json.Marshal(map[string]any{"handles": []string{"output-upload-one"}})
	if _, err := ports.dropAttachments(input); err != nil {
		t.Fatal(err)
	}
	if refs, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID); err != nil || len(refs) != 0 {
		t.Fatalf("cancelled archive changed row: %v %v", refs, err)
	}
	if _, err := call("output.stage.begin", begin); err != nil {
		t.Fatalf("cancelled artifact cannot be staged again: %v", err)
	}
	input.SurfaceToken = "foreign"
	if _, err := call("output.stage.write", map[string]any{"handle": "output-upload-one", "offset": 0, "base64": "eA=="}); err == nil {
		t.Fatal("foreign surface wrote an owned output")
	}
	input.SurfaceToken = "surface-a"
	for offset := 0; offset < len(content); offset += surfaceOutputBlockBytes {
		end := min(offset+surfaceOutputBlockBytes, len(content))
		if _, err := call("output.stage.write", map[string]any{"handle": "output-upload-one", "offset": offset, "base64": base64.StdEncoding.EncodeToString(content[offset:end])}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call("output.stage.finish", map[string]any{"handle": "output-upload-one"}); err != nil {
		t.Fatal(err)
	}
	schema, err := schemaexecution.Describe(ctx, f.pb, f.tableID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	page, err := query.NewPort(f.pb, source).QueryPage(ctx, f.tableID, query.TableQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = "output-archive-run"
	input.Request, _ = json.Marshal(map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": f.tableID, "schemaRevision": schema.Snapshot.SchemaRevision,
		"operations": []any{map[string]any{"kind": "setAttachments", "primaryKey": f.recordID, "expectedDigest": page.Rows[0][productrow.DigestField], "fieldId": f.fieldID,
			"uploadHandles": []string{"output-upload-one"}, "removeStoredNames": []string{}}}})
	request, err := surfaceMutationRequest(ctx, f.pb, workspace, input, ports)
	if err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(f.manager))
	rollback := errors.New("synthetic provenance transaction rollback")
	_, err = kernel.ApplyWithCommit(ctx, request, func(tx core.App, _ mutation.Receipt) error {
		if err := surfaceOutputArchiveFinish(ctx, tx, workspace, input, ports); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback did not reach provenance boundary: %v", err)
	}
	refs, err := f.manager.RefsByID(ctx, f.pb, f.tableID, f.recordID, f.fieldID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("rolled-back output changed attachments: %v %v", refs, err)
	}
	if raw, err := store.GetAudit(ctx, input.ProjectKey, input.PluginID, "output-issued-one"); err != nil || raw != nil {
		t.Fatalf("provenance escaped rollback: %s %v", raw, err)
	}
	commits := 0
	commit := func(tx core.App, _ mutation.Receipt) error {
		commits++
		return surfaceOutputArchiveFinish(ctx, tx, workspace, input, ports)
	}
	if _, err = kernel.ApplyWithCommit(ctx, request, commit); err != nil {
		t.Fatal(err)
	}
	if _, err = kernel.ApplyWithCommit(ctx, request, commit); err != nil || commits != 1 {
		t.Fatalf("retry replayed archive or provenance: commits=%d err=%v", commits, err)
	}
	// New surfaces can reopen using a durable artifact ID, but never another installation.
	input.SurfaceToken = "reopened-surface"
	opened, err := call("output.open", map[string]any{"artifactId": "output-issued-one"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := opened.(map[string]any)
	if metadata["mediaType"] != mediaType || metadata["displayName"] != name || metadata["source"].(surfaceOutputSource) != sourceMetadata || metadata["sizeBytes"] != int64(len(content)) {
		t.Fatalf("source was regenerated or changed: %#v", metadata)
	}
	wire, _ := json.Marshal(metadata)
	if bytes.Contains(wire, []byte("storedName")) || bytes.Contains(wire, []byte(f.pb.DataDir())) {
		t.Fatalf("internal file authority escaped: %s", wire)
	}
	var read []byte
	for offset := 0; offset < len(content); offset += surfaceOutputBlockBytes {
		block, err := call("output.read", map[string]any{"artifactId": "output-issued-one", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := base64.StdEncoding.DecodeString(block.(map[string]any)["base64"].(string))
		read = append(read, data...)
	}
	if !bytes.Equal(read, content) {
		t.Fatal("reprint changed original bytes")
	}
	begin["source"] = surfaceOutputSource{"template-v2", "business-revision-8", "changed"}
	if _, err = call("output.stage.begin", begin); err == nil {
		t.Fatal("same artifact ID replaced issued source")
	}
	if mediaType == xlsxArchiveMIME {
		begin["artifactId"], begin["handle"] = "other-artifact", "other-handle"
		if _, err := call("output.stage.begin", begin); surfaceErrorCode(t, err) != "mutation.attachment.too_many" {
			t.Fatalf("full version accepted a second archive: %v", err)
		}
	}
	input.InstallationID = "foreign-installation"
	if _, err = call("output.open", map[string]any{"artifactId": "output-issued-one"}); err == nil {
		t.Fatal("foreign installation read an issued artifact")
	}
}

func TestPluginSurfaceOutputBudgetOrderAndExpiredStage(t *testing.T) {
	f := newFileListFixture(t)
	workspace := "11111111-1111-4111-8111-111111111111"
	input, _ := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
	ports := &pluginSurfaceIO{attachments: f.manager}
	call := func(method string, request any) error {
		input.Method = method
		input.Request, _ = json.Marshal(request)
		_, err := executeSurfaceOutput(context.Background(), f.pb, workspace, input, ports)
		return err
	}
	begin := map[string]any{"handle": "output-budget-one", "artifactId": "artifact-budget-one", "collection": f.tableID, "recordId": f.recordID,
		"fieldId": f.fieldID, "name": "synthetic.png", "mediaType": "image/png", "sizeBytes": surfaceOutputMaxBytes + 1, "source": surfaceOutputSource{"t1", "d1", "test"}}
	if err := call("output.stage.begin", begin); surfaceErrorCode(t, err) != "plugin_file_limit" {
		t.Fatalf("oversized output: %v", err)
	}
	begin["sizeBytes"] = 10
	if err := call("output.stage.begin", begin); err != nil {
		t.Fatal(err)
	}
	if err := call("output.stage.write", map[string]any{"handle": "output-budget-one", "offset": 1, "base64": "eA=="}); err == nil {
		t.Fatal("out-of-order write accepted")
	}
	if err := call("output.stage.finish", map[string]any{"handle": "output-budget-one"}); err == nil {
		t.Fatal("incomplete output sealed")
	}
	ports.outputs.items["output-budget-one"].expires = time.Now().Add(-time.Second)
	if err := call("output.stage.write", map[string]any{"handle": "output-budget-one", "offset": 0, "base64": "eA=="}); err == nil {
		t.Fatal("expired output accepted")
	}
	if len(ports.outputs.items) != 0 {
		t.Fatal("expired buffer retained its memory budget")
	}
}
