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
	owner := done.(pluginmodel.State).Bindings["people"]
	schema, err := schemaexecution.Describe(t.Context(), pb, owner.TableID)
	if err != nil {
		t.Fatal(err)
	}
	id := "issuedrecord001"
	manager := mustAttachmentManager(t)
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(manager))
	_, err = kernel.Apply(t.Context(), mutation.Request{ContractVersion: "2.0", RequestID: "issued-seed", IdempotencyKey: "issued-seed", TableID: owner.TableID, SchemaRevision: schema.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "user", ID: "user"}, Operations: []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &id, Values: map[string]any{owner.Fields["name"]: "synthetic"}}}})
	if err != nil {
		t.Fatal(err)
	}
	field := owner.Fields["archive_file"]
	installed, err := surfaceInstallation(t.Context(), pb, workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "create", "update"} {
		grant := installed.grantFor(owner.TableID, "", op)
		if grant == nil || pluginHas(grant.Fields, field) {
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
	checkOutputArchive(t, fileListFixture{pb: pb, manager: manager, tableID: owner.TableID, recordID: id, fieldID: field, content: syntheticLedgerXLSX(t)}, workspace, input, pluginstore.New(pb, workspace), "issued.xlsx", xlsxArchiveMIME)
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
