package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestHistoryReadProductHTTPReturnsFreshAuditedPage(t *testing.T) {
	runtime, ledger, mux, tableID, recordID := historyReadProductFixture(t)
	response := productHistoryReadRequest(
		t,
		mux,
		`{"collection":"`+tableID+`","itemId":"`+recordID+
			`","limit":20,"offset":0,"scope":"row","actions":[]}`,
	)
	if response.Error != nil {
		t.Fatalf("history.read Product HTTP error = %+v", response.Error)
	}
	records, err := ledger.VerifiedRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 3 {
		t.Fatalf("Product read did not drain the business audit outbox: %#v", records)
	}
	var page audit.Page
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Collection != tableID || page.Scope != "row" || page.ItemID == nil ||
		*page.ItemID != recordID || len(page.ChangeSets) != 1 {
		t.Fatalf("history.read Product page = %#v", page)
	}
	if _, err := runtime.ReadBusinessHistory(context.Background(), audit.ReadParams{
		TableID: tableID, ItemID: &recordID, Scope: "row", Limit: 20,
	}); err != nil {
		t.Fatalf("history remains readable after Product drain: %v", err)
	}
}

func TestHistoryReadProductHTTPPreservesLegacyValidationAndPublicErrors(t *testing.T) {
	_, _, mux, tableID, recordID := historyReadProductFixture(t)
	for name, params := range map[string]string{
		"missing required actions": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":20,"offset":0,"scope":"row"}`,
		"forbidden nested credential stays parameter validation": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":20,"offset":0,"scope":"row","actions":[{"sessionSecret":"no"}]}`,
		"null scope reaches handler": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":20,"offset":0,"scope":null,"actions":[]}`,
		"null actions reaches handler": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":20,"offset":0,"scope":"row","actions":null}`,
		"fractional limit reaches handler": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":1.5,"offset":0,"scope":"row","actions":[]}`,
		"non-finite float stays parameter validation": `{"collection":"` + tableID + `","itemId":"` + recordID +
			`","limit":1e309,"offset":0,"scope":"row","actions":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := productHistoryReadRequest(t, mux, params)
			want := productrpc.CodeInternalError
			if name == "missing required actions" ||
				name == "forbidden nested credential stays parameter validation" ||
				name == "non-finite float stays parameter validation" {
				want = productrpc.CodeInvalidParams
			}
			if response.Error == nil || response.Error.Code != want {
				t.Fatalf("legacy validation stage changed: %+v", response.Error)
			}
		})
	}
	emptyScope := productHistoryReadRequest(
		t, mux, `{"collection":"`+tableID+`","itemId":"`+recordID+
			`","limit":20,"offset":0,"scope":"","actions":[]}`,
	)
	if emptyScope.Error != nil || !bytes.Contains(emptyScope.Result, []byte(`"scope":"row"`)) {
		t.Fatalf("empty scope did not keep the legacy row fallback: %+v", emptyScope)
	}
	overflow := productHistoryReadRequest(
		t, mux, `{"collection":"`+tableID+`","itemId":"`+recordID+
			`","limit":`+strings.Repeat("9", 310)+`,"offset":0,"scope":"row","actions":[]}`,
	)
	if overflow.Error == nil || overflow.Error.Code != productrpc.CodeProductData ||
		overflow.Error.Data["code"] != "history.request_invalid" ||
		overflow.Error.Data["path"] != nil {
		t.Fatalf("overflow paging public error = %+v", overflow.Error)
	}
	longFinite := productHistoryReadRequest(
		t, mux, `{"collection":"`+tableID+`","itemId":"`+recordID+
			`","limit":1e`+strings.Repeat("0", maxHistoryReadProductParamsBytes)+
			`,"offset":0,"scope":"row","actions":[]}`,
	)
	if longFinite.Error == nil || longFinite.Error.Code != productrpc.CodeInternalError {
		t.Fatalf("finite exponent budget did not reach the handler: %+v", longFinite.Error)
	}
	domain := productHistoryReadRequest(
		t, mux, `{"collection":"不存在表","itemId":"`+recordID+
			`","limit":20,"offset":0,"scope":"row","actions":[]}`,
	)
	if domain.Error == nil || domain.Error.Code != productrpc.CodeProductData ||
		domain.Error.Data["code"] != "history.table_not_found" ||
		domain.Error.Data["path"] != nil ||
		!reflect.DeepEqual(domain.Error.Data["details"], map[string]any{}) {
		t.Fatalf("domain public error = %+v", domain.Error)
	}
}

func TestHistoryReadProductParamsSizeMatchesPythonNumberNormalization(t *testing.T) {
	for _, testCase := range []struct {
		name, raw, normalized string
	}{
		{"small exponent", `1e-5`, `1e-05`},
		{"fixed million", `1e6`, `1000000.0`},
		{"large exponent", `1e16`, `1e+16`},
		{"negative integer zero", `-0`, `0`},
		{"negative float zero", `-0.0`, `-0.0`},
		{"underflow", `1e-400`, `0.0`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			size, err := historyReadProductParamsSize(
				map[string]any{"limit": json.Number(testCase.raw)})
			if err != nil {
				t.Fatal(err)
			}
			if want := len(`{"limit":` + testCase.normalized + `}`); size != want {
				t.Fatalf("Python normalized size = %d, want %d", size, want)
			}
		})
	}
}

func TestHistoryReadProductHTTPUsesPythonSemanticParamBudget(t *testing.T) {
	_, _, mux, tableID, recordID := historyReadProductFixture(t)
	semanticParams := `{"collection":"` + tableID + `","itemId":"` + recordID +
		`","limit":20,"offset":0,"scope":"row","search":"订单 <b>&","actions":[]}`
	withWhitespace := "\n" + semanticParams + strings.Repeat(" ", 1<<20)
	response := productHistoryReadRequest(t, mux, withWhitespace)
	if response.Error != nil {
		t.Fatalf("under-budget semantic params rejected by HTTP boundary: %+v", response.Error)
	}
	overBudget := `{"collection":"` + tableID + `","itemId":"` + recordID +
		`","limit":20,"offset":0,"scope":"row","search":"` +
		strings.Repeat("x", maxHistoryReadProductParamsBytes) + `","actions":[]}`
	response = productHistoryReadRequest(t, mux, overBudget)
	if response.Error == nil || response.Error.Code != productrpc.CodeInvalidParams {
		t.Fatalf("over-budget params did not fail at the parameter layer: %+v", response.Error)
	}
}

func TestHistoryReadProductHTTPMatchesPythonUnicodeSeparatorBudget(t *testing.T) {
	_, _, mux, tableID, recordID := historyReadProductFixture(t)
	for _, separator := range []string{"\u2028", "\u2029"} {
		t.Run("actual separator", func(t *testing.T) {
			response := productHistoryReadRequest(t, mux,
				`{"collection":"`+tableID+`","itemId":"`+recordID+
					`","limit":20,"offset":0,"scope":"row","search":"`+
					strings.Repeat(separator, 200_000)+`","actions":[]}`,
			)
			if response.Error == nil || response.Error.Code != productrpc.CodeProductData ||
				response.Error.Data["code"] != "history.request_invalid" {
				t.Fatalf("Unicode separator budget did not reach the history handler: %+v", response.Error)
			}
		})
	}
	literal := productHistoryReadRequest(t, mux,
		`{"collection":"`+tableID+`","itemId":"`+recordID+
			`","limit":20,"offset":0,"scope":"row","search":"`+
			strings.Repeat(`\\u2028`, 160_000)+`","actions":[]}`,
	)
	if literal.Error == nil || literal.Error.Code != productrpc.CodeInvalidParams {
		t.Fatalf("literal Unicode escape did not keep its encoded budget: %+v", literal.Error)
	}
}

func TestHistoryProductStoreCleanupTerminatesBeforeReset(t *testing.T) {
	var pb *pocketbase.PocketBase
	terminated := false
	bootstrappedAtTermination := false
	t.Run("fixture", func(t *testing.T) {
		pb = historyProductStore(t, t.TempDir())
		pb.OnTerminate().BindFunc(func(event *core.TerminateEvent) error {
			terminated = true
			bootstrappedAtTermination = event.App.IsBootstrapped()
			return event.Next()
		})
	})
	if !terminated {
		t.Error("history fixture cleanup did not invoke OnTerminate")
	}
	if !bootstrappedAtTermination {
		t.Error("OnTerminate must run before bootstrap state is reset")
	}
	if pb == nil || pb.IsBootstrapped() {
		t.Error("history fixture cleanup did not reset bootstrap state")
	}
}

func historyProductStore(t *testing.T, dataDir string) *pocketbase.PocketBase {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		event := &core.TerminateEvent{App: pb}
		if err := pb.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
			return event.App.ResetBootstrapState()
		}); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return pb
}

func historyReadProductFixture(t *testing.T) (*workspacev2.Runtime, *auditledger.Ledger, http.Handler, string, string) {
	runtime, ledger, mux, table, record, _ := historyRestoreProductFixture(t)
	return runtime, ledger, mux, table, record
}

func historyRestoreProductFixture(
	t *testing.T,
	options ...audit.Option,
) (*workspacev2.Runtime, *auditledger.Ledger, http.Handler, string, string, *pocketbase.PocketBase) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	metadata := filepath.Join(root, ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceID := "11111111-1111-4111-8111-111111111111"
	manifest := `{"contractVersion":"2.0","formatVersion":2,"workspaceId":"` + workspaceID +
		`","displayName":"History Product","createdAt":"2026-07-28T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v3","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(metadata, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(metadata, "data")
	pb := historyProductStore(t, dataDir)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(context.Background(), v2.TableCreateIntent{
		DisplayName: "Product history", OperationID: "history-product-table",
		Actor: v2.Actor{ID: "history-product", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recommended, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldchange.NewCatalog(pb)
	store := fieldchange.NewPocketBasePlanStore(pb)
	planner := fieldchange.NewPlanner(catalog, catalog, store, v2.NewIdentityAllocator(nil))
	executor := fieldchange.NewExecutor(pb, store)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID, ExpectedSchemaRev: table.SchemaRevision,
		Draft: &v2.FieldDraft{
			DisplayName: "Title", LogicalType: v2.LogicalText, Value: recommended.Value,
			Constraints: recommended.Constraints, Storage: recommended.Storage, Display: recommended.Display,
		},
		Actor: v2.Actor{ID: "history-product", Kind: "user"},
	})
	if err != nil || !plan.CanApply {
		t.Fatalf("plan history field: %#v, %v", plan.Errors, err)
	}
	field, err := executor.Apply(context.Background(), v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: "history-product-title",
		Actor: plan.Intent.Actor,
	})
	if err != nil || field.Definition == nil {
		t.Fatalf("create history field: %#v, %v", field, err)
	}
	ledger, err := auditledger.Open(filepath.Join(metadata, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	kernel := mutation.New(pb, mutation.MetadataSchemaSource{})
	history, err := audit.New(pb, kernel, append(options, audit.WithLedgerHistory(ledger))...)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspacev2.Open(context.Background(), workspacev2.Options{
		App: pb, DataDir: dataDir, WorkspaceID: workspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Ledger: ledger,
		Audit: history, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	recordID := "histprod0000001"
	err = runtime.CoordinateBusinessWrite(
		context.Background(), "mutation.apply", "history-product-insert",
		func(ctx context.Context) error {
			_, applyErr := kernel.Apply(ctx, mutation.Request{
				ContractVersion: mutation.ContractVersion, RequestID: "history-product-request",
				IdempotencyKey: "history-product-idempotency", TableID: table.TableID,
				SchemaRevision: field.SchemaRevision,
				Operations: []mutation.Operation{{
					Kind: mutation.OperationInsert, RecordID: &recordID,
					Values: map[string]any{field.Definition.Identity.PhysicalName: "first"},
				}},
				Actor: mutation.Actor{Type: "user", ID: "history-product"},
			})
			return applyErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	events, err := pb.FindRecordsByFilter(
		"vibetable_audit_events", "record_id={:record}", "", 10, 0,
		dbx.Params{"record": recordID},
	)
	if err != nil || len(events) != 1 {
		t.Fatalf("business mutation did not persist one audit event: %#v, %v", events, err)
	}
	capabilities := runtime.Capabilities()
	dispatcher, err := productrpc.New(productrpc.Identity{
		WorkspaceID: capabilities.WorkspaceID, SessionEpoch: capabilities.SessionEpoch,
		FenceEpoch: capabilities.FenceEpoch, ClaimID: capabilities.ClaimID,
	},
		mutationPreviewRegistration(unrelatedMutationProductMustNotRun{t: t}),
		mutationApplyRegistration(unrelatedMutationProductMustNotRun{t: t}),
		productrpc.ReconcileRegistration(schemaapi.New(pb)),
		schemaDescribeRegistration(pb, relation.New(pb, nil, nil)),
		schemaGetTableRegistration(pb),
		schemaListRegistration(schemaapi.New(pb)),
		queryViewRegistration(unrelatedViewMustNotRun{t: t}),
		lookupQueryRegistration(unrelatedLookupQueryMustNotRun{t: t}),
		lookupValuePageRegistration(unrelatedLookupValuePageMustNotRun{t: t}),
		relationPreviewDeltaRegistration(unrelatedRelationPreviewMustNotRun{t: t}),
		fieldSettingsDescribeRegistration(unrelatedFieldSettingsDescribeMustNotRun{t: t}),
		productrpc.AttachmentListRegistration(pb, mustAttachmentManager(t)),
		historyReadRegistration(runtime),
		historyPreviewRestoreRegistration(runtime),
		historyApplyRestoreRegistration(runtime),
		queryReadRowsRegistration(unrelatedQueryReadRowsMustNotRun{t: t}),
		queryValidateSnapshotRegistration(unrelatedQueryValidateSnapshotMustNotRun{t: t}),
		lookupListRegistration(relation.New(pb, nil, nil)),
		relationSearchTargetsRegistration(unrelatedRelationSearchMustNotRun{t: t}),
		unrelatedRelationInspectRegistration(t),
		queryPageRegistration(unrelatedQueryPageMustNotRun{t: t}),
		queryCursorOpenRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		queryCursorFetchRegistration(unrelatedQueryCursorMustNotRun{t: t}),
		querySelectionOpenRegistration(unrelatedSelectionMustNotRun{t: t}),
	)
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerProductRoutes(r, dispatcher)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return runtime, ledger, mux, table.TableID, recordID, pb
}

func productHistoryReadRequest(
	t *testing.T,
	mux http.Handler,
	params string,
) productrpc.ResponseEnvelope {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, productRPCPath, bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":"history-read","method":"history.read","wire":`+
			schemaListWire+`,"params":`+params+`}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	var envelope productrpc.ResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || string(envelope.ID) != `"history-read"` ||
		string(envelope.Wire) != schemaListWire {
		t.Fatalf("history.read Product envelope changed: %d %s", response.Code, response.Body)
	}
	return envelope
}
