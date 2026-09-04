package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
)

const fileListWire = `{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":7,"operationId":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","sequence":1}`

func TestFileListProductHTTPMatchesAttachmentRESTAndConsumesCapabilities(t *testing.T) {
	fixture := newFileListFixture(t)
	params := fixture.params()
	assertFileListEmpty(t, fixture.mux, params)
	fixture.upload(t)

	rest := fileListREST(t, fixture.mux, params)
	product := fileListProductRequest(t, fixture.mux, context.Background(), params, fileListWire)
	var productResult struct {
		Attachments []attachments.Ref `json:"attachments"`
	}
	if product.Error != nil || json.Unmarshal(product.Result, &productResult) != nil {
		t.Fatalf("Product file.list = %+v", product)
	}
	if len(rest.Attachments) != 1 || len(productResult.Attachments) != 1 {
		t.Fatalf("attachment count REST=%d Product=%d", len(rest.Attachments), len(productResult.Attachments))
	}
	assertStableAttachmentRef(t, rest.Attachments[0], productResult.Attachments[0])
	for name, ref := range map[string]attachments.Ref{
		"REST": rest.Attachments[0], "Product": productResult.Attachments[0],
	} {
		if ref.DownloadCapability == "" || len(ref.Thumbnails) != 1 ||
			ref.Thumbnails[0].DownloadCapability == "" {
			t.Fatalf("%s omitted attachment capabilities: %#v", name, ref)
		}
		assertAttachmentDownload(t, fixture.mux, ref.DownloadCapability, fixture.content, true)
		assertAttachmentDownload(t, fixture.mux, ref.Thumbnails[0].DownloadCapability, nil, false)
	}
}

type fileListFixture struct {
	pb       core.App
	manager  *attachments.Manager
	mux      http.Handler
	tableID  string
	recordID string
	fieldID  string
	content  []byte
}

func newFileListFixture(t *testing.T) fileListFixture {
	t.Helper()
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{
		DisplayName: "附件 📎", OperationID: "file-list-table",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recommended, err := v2.RecommendedDefaults(v2.LogicalFile)
	if err != nil {
		t.Fatal(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "图片", LogicalType: v2.LogicalFile, Value: recommended.Value,
		Constraints: recommended.Constraints, Storage: recommended.Storage,
		Display: recommended.Display, JSON: recommended.JSON,
		File: &v2.FileSpec{
			MaxFiles: 1, MaxBytesPerFile: 4096, AllowedMIMETypes: []string{"image/png"},
			Thumbs: []string{"8x8"}, Protected: true,
		},
	}
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	revisions, err := catalog.Revisions(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(ctx, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: revisions.Schema,
		Draft: &draft, Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil || !plan.CanApply {
		t.Fatalf("file field plan = %#v, %v", plan, err)
	}
	field, err := fieldchange.NewExecutor(pb, store).Apply(ctx, v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "file-list-field",
		Actor: v2.Actor{ID: "local-user", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := schemaapi.New(pb).Describe(ctx, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	manager := mustAttachmentManager(t)
	recordID := "filelistrecord1"
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(manager))
	_, err = kernel.Apply(ctx, mutation.Request{
		ContractVersion: mutation.ContractVersion, RequestID: "file-list-insert",
		IdempotencyKey: "file-list-insert", TableID: table.TableID,
		SchemaRevision: definition.Snapshot.SchemaRevision,
		Operations:     []mutation.Operation{{Kind: mutation.OperationInsert, RecordID: &recordID, Values: map[string]any{}}},
		Actor:          mutation.Actor{Type: "user", ID: "local-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fileListFixture{
		pb: pb, manager: manager, mux: fileListProductMux(t, pb, manager), tableID: table.TableID,
		recordID: recordID, fieldID: field.FieldID, content: fileListPNG(t),
	}
}

func (fixture fileListFixture) params() string {
	raw, _ := json.Marshal(map[string]string{
		"tableId": fixture.tableID, "recordId": fixture.recordID, "fieldId": fixture.fieldID,
	})
	return string(raw)
}

func (fixture fileListFixture) upload(t *testing.T) {
	t.Helper()
	if err := fixture.manager.Stage("file-list-upload", "猫.png", fixture.content); err != nil {
		t.Fatal(err)
	}
	kernel := mutation.New(fixture.pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(fixture.manager))
	definition, err := schemaapi.New(fixture.pb).Describe(context.Background(), fixture.tableID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kernel.Apply(context.Background(), mutation.Request{
		ContractVersion: mutation.ContractVersion, RequestID: "file-list-upload",
		IdempotencyKey: "file-list-upload", TableID: fixture.tableID,
		SchemaRevision: definition.Snapshot.SchemaRevision,
		Operations: []mutation.Operation{{
			Kind: mutation.OperationSetAttachments, RecordID: &fixture.recordID, FieldID: fixture.fieldID,
			UploadHandles: []string{"file-list-upload"}, RemoveStoredNames: []string{},
		}},
		Actor: mutation.Actor{Type: "user", ID: "local-user"},
	})
	fixture.manager.Drop("file-list-upload")
	if err != nil {
		t.Fatal(err)
	}
}

func assertFileListEmpty(t *testing.T, mux http.Handler, params string) {
	t.Helper()
	rest := fileListREST(t, mux, params)
	product := fileListProductRequest(t, mux, context.Background(), params, fileListWire)
	if string(product.Result) != `{"attachments":[]}` || product.Error != nil ||
		len(rest.Attachments) != 0 || string(rest.raw) != `{"attachments":[]}` {
		t.Fatalf("empty attachments REST=%s Product=%s error=%+v", rest.raw, product.Result, product.Error)
	}
}

type attachmentListResponse struct {
	Attachments []attachments.Ref `json:"attachments"`
	raw         []byte
}

func fileListREST(t *testing.T, mux http.Handler, params string) attachmentListResponse {
	t.Helper()
	var values map[string]string
	if err := json.Unmarshal([]byte(params), &values); err != nil {
		t.Fatal(err)
	}
	query := url.Values{
		"tableId": {values["tableId"]}, "recordId": {values["recordId"]}, "fieldId": {values["fieldId"]},
	}.Encode()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/vibetable/v1/attachments/refs?"+query, nil,
	))
	result := attachmentListResponse{raw: response.Body.Bytes()}
	if response.Code != http.StatusOK || json.Unmarshal(result.raw, &result) != nil {
		t.Fatalf("attachment REST = %d %s", response.Code, result.raw)
	}
	return result
}

func assertStableAttachmentRef(t *testing.T, rest, product attachments.Ref) {
	t.Helper()
	if rest.ContractVersion != product.ContractVersion || rest.TableID != product.TableID ||
		rest.RecordID != product.RecordID || rest.FieldID != product.FieldID ||
		rest.StoredName != product.StoredName || rest.OriginalName != product.OriginalName ||
		rest.MIMEType != product.MIMEType || rest.Size != product.Size || rest.SHA256 != product.SHA256 ||
		rest.OriginalName != "猫.png" || rest.MIMEType != "image/png" || len(rest.Thumbnails) != len(product.Thumbnails) {
		t.Fatalf("stable attachment parity REST=%#v Product=%#v", rest, product)
	}
	for index := range rest.Thumbnails {
		if rest.Thumbnails[index].Variant != product.Thumbnails[index].Variant {
			t.Fatalf("thumbnail parity REST=%#v Product=%#v", rest, product)
		}
	}
}

func assertAttachmentDownload(t *testing.T, mux http.Handler, capability string, want []byte, original bool) {
	t.Helper()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/vibetable/v1/attachments/download?"+url.Values{"capability": {capability}}.Encode(), nil,
	))
	body, err := io.ReadAll(response.Result().Body)
	if err != nil || response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || len(body) == 0 {
		t.Fatalf("attachment download status=%d contentType=%q bytes=%d err=%v", response.Code, response.Header().Get("Content-Type"), len(body), err)
	}
	if original && !bytes.Equal(body, want) {
		t.Fatal("attachment download changed original content")
	}
}

func fileListPNG(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			imageData.Set(x, y, color.NRGBA{R: uint8(40 + x*20), G: uint8(70 + y*20), B: 180, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestFileListProductHTTPRejectsClosedParamsAndStaleScopeBeforeResolver(t *testing.T) {
	pb := schemaProductStore(t)
	mux := fileListProductMux(t, pb, mustAttachmentManager(t))
	for _, params := range []string{
		`null`, `[]`, `true`, `7`, `"text"`, `{}`, `{"tableId":"t","recordId":"r"}`,
		`{"tableId":"t","recordId":"r","fieldId":""}`,
		`{"tableId":"t","recordId":"r","fieldId":"f","extra":true}`,
	} {
		t.Run(params, func(t *testing.T) {
			response := fileListProductRequest(t, mux, context.Background(), params, fileListWire)
			code := productrpc.CodeInvalidRequest
			if strings.HasPrefix(params, "{") {
				code = productrpc.CodeInvalidParams
			}
			if response.Error == nil || response.Error.Code != code {
				t.Fatalf("invalid params reached resolver: %+v", response)
			}
		})
	}
	response := fileListProductRequest(t, mux, context.Background(), `{"tableId":"t","recordId":"r","fieldId":"f"}`,
		strings.Replace(fileListWire, `"sessionEpoch":7`, `"sessionEpoch":8`, 1))
	if response.Error == nil || response.Error.Code != productrpc.CodeInvalidRequest {
		t.Fatalf("stale scope reached resolver: %+v", response)
	}
}

func TestFileListProductHTTPPreservesAttachmentErrorAndCancellation(t *testing.T) {
	pb := schemaProductStore(t)
	manager := mustAttachmentManager(t)
	mux := fileListProductMux(t, pb, manager)
	params := `{"tableId":"不存在","recordId":"record-1","fieldId":"file-1"}`
	product := fileListProductRequest(t, mux, context.Background(), params, fileListWire)
	if product.Error == nil || product.Error.Code != productrpc.CodeProductData {
		t.Fatalf("attachment error = %+v", product)
	}
	rest := httptest.NewRecorder()
	mux.ServeHTTP(rest, httptest.NewRequest(http.MethodGet,
		"/api/vibetable/v1/attachments/refs?tableId=%E4%B8%8D%E5%AD%98%E5%9C%A8&recordId=record-1&fieldId=file-1", nil))
	var restError map[string]any
	if err := json.Unmarshal(rest.Body.Bytes(), &restError); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"code", "path", "message", "details", "retryable"} {
		if !reflect.DeepEqual(product.Error.Data[key], restError[key]) {
			t.Fatalf("error parity %s: Product=%v REST=%v", key, product.Error.Data, restError)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	product = fileListProductRequest(t, mux, ctx, params, fileListWire)
	if product.Error == nil || product.Error.Code != productrpc.CodeInternalError || product.Error.Data != nil {
		t.Fatalf("canceled attachment read = %+v", product)
	}
}

func mustAttachmentManager(t *testing.T) *attachments.Manager {
	t.Helper()
	manager, err := attachments.New()
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func fileListProductMux(t *testing.T, app core.App, manager *attachments.Manager) http.Handler {
	t.Helper()
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: "11111111-1111-4111-8111-111111111111", SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	},
		productrpc.ReconcileRegistration(schemaapi.New(app)),
		schemaGetTableRegistration(app),
		schemaListRegistration(schemaapi.New(app)),
		productrpc.AttachmentListRegistration(app, manager),
	)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}, App: app}, nil
	})
	registerAttachmentRoutes(r, manager)
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func fileListProductRequest(t *testing.T, mux http.Handler, ctx context.Context, params, wire string) productrpc.ResponseEnvelope {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":"file-list","method":"file.list","wire":`+wire+`,"params":`+params+`}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	var envelope productrpc.ResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope.ID) != `"file-list"` || string(envelope.Wire) != wire {
		t.Fatalf("Product envelope changed: %d %s", response.Code, response.Body)
	}
	return envelope
}
