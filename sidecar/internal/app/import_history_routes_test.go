package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/importhistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type importHistoryFixture struct {
	pb         *pocketbase.PocketBase
	mux        http.Handler
	store      *importhistory.Store
	session    string
	tableID    string
	fieldID    string
	revision   string
	physical   string
	gateKinds  []string
	failPoint  string
	tableCount func() (int, error)
}

func newImportHistoryFixture(t *testing.T, dataDir string, reuseTableID string) *importHistoryFixture {
	t.Helper()
	pb := historyProductStore(t, dataDir)
	ctx := context.Background()
	tableID := reuseTableID
	if tableID == "" {
		lifecycle, err := schemacore.NewTableLifecycle(pb)
		if err != nil {
			t.Fatal(err)
		}
		table, err := lifecycle.Create(ctx, v2.TableCreateIntent{
			DisplayName: "导入历史", OperationID: "import-history-table",
			Actor: v2.Actor{ID: "local-user", Kind: "user"},
		})
		if err != nil {
			t.Fatal(err)
		}
		field := createSchemaProductField(
			t, pb, table.TableID, v2.LogicalText, "标题", "import-history-field",
		)
		tableID = field.TableID
	}
	definition, err := schemaexecution.Describe(ctx, pb, tableID)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Snapshot.Fields) == 0 {
		t.Fatal("import history fixture table has no fields")
	}
	fixture := &importHistoryFixture{
		pb: pb, tableID: tableID,
		fieldID:  definition.Snapshot.Fields[0].Identity.FieldID,
		revision: definition.Snapshot.SchemaRevision,
		physical: definition.PhysicalName,
	}
	fixture.tableCount = func() (int, error) {
		var count int
		queryErr := pb.DB().NewQuery(
			"SELECT COUNT(*) FROM `" + fixture.physical + "`",
		).Row(&count)
		return count, queryErr
	}
	secret, encoded, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	fixture.session = encoded
	fixture.store = importhistory.NewStore()
	kernel := mutation.New(
		pb,
		mutation.MetadataSchemaSource{},
		mutation.WithFaultInjector(func(point string) error {
			if point == fixture.failPoint {
				return errors.New("injected failure")
			}
			return nil
		}),
	)
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
	})
	bindVibetableSessionAuth(r, secret)
	gate := businessWriteGate(func(
		ctx context.Context,
		kind string,
		identity string,
		apply func(context.Context) error,
	) error {
		fixture.gateKinds = append(fixture.gateKinds, kind+"/"+identity)
		return apply(ctx)
	})
	registerMutationRoutes(r, importhistory.Project(kernel, fixture.store), nil, gate)
	registerImportHistoryRoutes(r, pb, fixture.store, gate)
	fixture.mux, err = r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *importHistoryFixture) call(
	t *testing.T,
	method string,
	path string,
	body any,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set(auth.HeaderName, fixture.session)
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	var payload map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response, payload
}

func (fixture *importHistoryFixture) startBody(taskID string) map[string]any {
	return map[string]any{
		"taskId":         taskID,
		"collection":     fixture.tableID,
		"sourceType":     "xlsx",
		"sourceName":     "订单导入.xlsx",
		"idempotencyKey": "import-" + taskID,
		"sessionEpoch":   7,
	}
}

func (fixture *importHistoryFixture) applyBody(taskID string) map[string]any {
	return map[string]any{
		"contractVersion":  mutation.ContractVersion,
		"requestId":        "import-request-" + taskID,
		"idempotencyKey":   "import-" + taskID,
		"tableId":          fixture.tableID,
		"schemaRevision":   fixture.revision,
		"expectedRevision": nil,
		"expectedDigest":   nil,
		"operations": []map[string]any{{
			"kind":     "insert",
			"recordId": importHistoryRecordID(taskID),
			"values":   map[string]any{fixture.fieldID: "导入行"},
		}},
		"actor": map[string]any{"type": "import", "id": "local-user", "displayName": nil},
	}
}

func TestImportHistoryHTTPCommitsWhenHostSendsPhysicalCollection(t *testing.T) {
	fixture := newImportHistoryFixture(t, t.TempDir(), "")

	// The Host starts the projection with the physical collection name while
	// the authoritative mutation request carries the logical tableId; the
	// catalog aliasing must reconcile attribution for the real UI import flow.
	body := fixture.startBody("task-physical")
	body["collection"] = fixture.physical
	startResponse, entry := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start", body,
	)
	if startResponse.Code != http.StatusOK || entry["state"] != "interrupted" {
		t.Fatalf("physical start = %d %v", startResponse.Code, entry)
	}
	applyResponse, receipt := fixture.call(
		t, http.MethodPost, "/api/vibetable/v1/mutations/apply",
		fixture.applyBody("task-physical"),
	)
	if applyResponse.Code != http.StatusOK || receipt["status"] != "applied" {
		t.Fatalf("apply = %d %v", applyResponse.Code, receipt)
	}
	_, payload := fixture.call(t, http.MethodGet, "/api/vibetable/v2/import-history", nil)
	items := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	projection := items[0].(map[string]any)
	if projection["state"] != "succeeded" ||
		projection["commitState"] != "committed" ||
		projection["createdCount"] != float64(1) ||
		projection["collection"] != fixture.physical {
		t.Fatalf("physical projection = %v", projection)
	}
}

func TestImportHistoryHTTPCoordinatedReplayUnderWorkspaceV2(t *testing.T) {
	ctx := context.Background()
	metadata := filepath.Join(t.TempDir(), "workspace", ".vibetable")
	for _, name := range []string{"data", "topology", "objects", "audit", "snapshots", "coordination", "quarantine", "temp"} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceID := "11111111-1111-4111-8111-111111111111"
	manifest := `{"contractVersion":"2.0","formatVersion":3,"workspaceId":"` + workspaceID + `","displayName":"Import History","createdAt":"2026-10-01T08:00:00Z","storageMode":"direct","encryptionMode":"convenient","repositoryFormat":"kopia-v4","topologySchemaVersion":1,"businessSchemaVersion":1,"importedFromWorkspaceId":null,"sourceSnapshotId":null}`
	if err := os.WriteFile(filepath.Join(metadata, "workspace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(metadata, "data")
	pb := historyProductStore(t, dataDir)
	ledger, err := auditledger.Open(filepath.Join(metadata, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	history, err := audit.New(pb, mutation.New(pb, mutation.MetadataSchemaSource{}), audit.WithLedgerHistory(ledger))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := workspacev2.Open(ctx, workspacev2.Options{
		App: pb, DataDir: dataDir, WorkspaceID: workspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Ledger: ledger, Audit: history, DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	secret, encoded, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	store := importhistory.NewStore()
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
	})
	// Full production admission chain for the import history ports: session
	// auth, the formal Workspace v2 write boundary allowlist and the
	// coordinated business write gate.
	bindVibetableSessionAuth(r, secret)
	bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
	registerImportHistoryRoutes(r, pb, store, runtime.CoordinateIdempotentBusinessWrite)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set(auth.HeaderName, encoded)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		return response, payload
	}

	startBody := map[string]any{
		"taskId":         "task-coordinated",
		"collection":     "tbl_coordinated",
		"sourceType":     "csv",
		"sourceName":     "旧入口.csv",
		"idempotencyKey": "import-task-coordinated",
		"sessionEpoch":   7,
	}
	first, entry := call(http.MethodPost, "/api/vibetable/v2/import-history/start", startBody)
	if first.Code != http.StatusOK || entry["state"] != "interrupted" {
		t.Fatalf("coordinated start = %d %v", first.Code, entry)
	}
	// The transactional receipt proves the coordinated write committed, so a
	// replay of the same identity is skipped and answered from the persisted
	// projection instead of allocating a second mutationRevision.
	var beforeReplay int
	if err := pb.DB().NewQuery(
		"SELECT COUNT(*) FROM workspace_v2_mutation_receipts",
	).Row(&beforeReplay); err != nil {
		t.Fatal(err)
	}
	replayed, replayEntry := call(http.MethodPost, "/api/vibetable/v2/import-history/start", startBody)
	if replayed.Code != http.StatusOK || replayEntry["state"] != "interrupted" ||
		replayEntry["taskId"] != "task-coordinated" {
		t.Fatalf("coordinated start replay = %d %v", replayed.Code, replayEntry)
	}
	var afterReplay int
	if err := pb.DB().NewQuery(
		"SELECT COUNT(*) FROM workspace_v2_mutation_receipts",
	).Row(&afterReplay); err != nil {
		t.Fatal(err)
	}
	if afterReplay != beforeReplay {
		t.Fatalf("replayed start allocated another receipt: %d -> %d", beforeReplay, afterReplay)
	}

	finished, final := call(http.MethodPost, "/api/vibetable/v2/import-history/finish", map[string]any{
		"taskId": "task-coordinated", "state": "failed",
	})
	if finished.Code != http.StatusOK || final["state"] != "failed" ||
		final["errorCode"] != "import.failed" {
		t.Fatalf("coordinated finish = %d %v", finished.Code, final)
	}
	repeated, repeatedFinal := call(http.MethodPost, "/api/vibetable/v2/import-history/finish", map[string]any{
		"taskId": "task-coordinated", "state": "failed",
	})
	if repeated.Code != http.StatusOK || repeatedFinal["state"] != "failed" {
		t.Fatalf("coordinated finish replay = %d %v", repeated.Code, repeatedFinal)
	}

	// Adjacent write paths stay fail-closed behind the formal boundary even
	// with a valid session secret; the projection writes only exist on the
	// exact coordinated allowlist entries.
	adjacent, adjacentPayload := call(http.MethodPost, "/api/vibetable/v2/import-history/settle", map[string]any{})
	if adjacent.Code != http.StatusLocked ||
		adjacentPayload["code"] != "workspace.v1_write_disabled" {
		t.Fatalf("adjacent path escaped the write boundary: %d %v", adjacent.Code, adjacentPayload)
	}
}

// importHistoryRecordID derives a stable PocketBase-shaped record id from a
// task id so tests can address committed rows deterministically.
func importHistoryRecordID(taskID string) string {
	digits := ""
	for _, symbol := range taskID {
		if symbol >= '0' && symbol <= '9' {
			digits += string(symbol)
		}
	}
	if len(digits) > 8 {
		digits = digits[len(digits)-8:]
	}
	for len(digits) < 8 {
		digits = "0" + digits
	}
	return "imphist" + digits
}

func TestImportHistoryHTTPMutationsProjectAtomically(t *testing.T) {
	fixture := newImportHistoryFixture(t, t.TempDir(), "")

	response, entry := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start",
		fixture.startBody("task-1101"),
	)
	if response.Code != http.StatusOK || entry["state"] != "interrupted" ||
		entry["commitState"] != "unknown" ||
		entry["createdCount"] != nil || entry["updatedCount"] != nil ||
		entry["errorCode"] != "import.interrupted" {
		t.Fatalf("start = %d %v", response.Code, entry)
	}
	if startedAt, _ := entry["startedAt"].(string); startedAt == "" || !strings.HasSuffix(startedAt, "Z") {
		t.Fatalf("startedAt = %v", entry["startedAt"])
	}

	// The business apply commits and the projection promotes inside the same
	// transaction.
	applyResponse, receipt := fixture.call(
		t, http.MethodPost, "/api/vibetable/v1/mutations/apply",
		fixture.applyBody("task-1101"),
	)
	if applyResponse.Code != http.StatusOK || receipt["status"] != "applied" {
		t.Fatalf("apply = %d %v", applyResponse.Code, receipt)
	}
	if count, err := fixture.tableCount(); err != nil || count != 1 {
		t.Fatalf("business rows = %d, %v", count, err)
	}

	listResponse, payload := fixture.call(
		t, http.MethodGet, "/api/vibetable/v2/import-history", nil,
	)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list = %d %s", listResponse.Code, listResponse.Body.String())
	}
	items := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	projected := items[0].(map[string]any)
	if projected["taskId"] != "task-1101" ||
		projected["state"] != "succeeded" ||
		projected["commitState"] != "committed" ||
		projected["createdCount"] != float64(1) ||
		projected["updatedCount"] != float64(0) ||
		projected["errorCode"] != nil ||
		projected["finishedAt"] == nil {
		t.Fatalf("projected entry = %v", projected)
	}

	// A committed success can never be overwritten by a late Host finish.
	lateResponse, lateEntry := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/finish",
		map[string]any{"taskId": "task-1101", "state": "aborted"},
	)
	if lateResponse.Code != http.StatusOK || lateEntry["state"] != "succeeded" {
		t.Fatalf("late finish = %d %v", lateResponse.Code, lateEntry)
	}

	// Injected pre-commit failure rolls back the business rows AND the
	// projection promotion in the same transaction.
	startSecond, _ := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start",
		fixture.startBody("task-1102"),
	)
	if startSecond.Code != http.StatusOK {
		t.Fatalf("second start = %d %s", startSecond.Code, startSecond.Body.String())
	}
	fixture.failPoint = "before_commit"
	applyFailed, failure := fixture.call(
		t, http.MethodPost, "/api/vibetable/v1/mutations/apply",
		fixture.applyBody("task-1102"),
	)
	if applyFailed.Code != http.StatusInternalServerError ||
		failure["code"] != "mutation.internal.failed" {
		t.Fatalf("failed apply = %d %v", applyFailed.Code, failure)
	}
	if count, err := fixture.tableCount(); err != nil || count != 1 {
		t.Fatalf("business rows after rollback = %d, %v", count, err)
	}
	_, payload = fixture.call(t, http.MethodGet, "/api/vibetable/v2/import-history", nil)
	rolled := payload["items"].([]any)[0].(map[string]any)
	if rolled["taskId"] != "task-1102" ||
		rolled["state"] != "interrupted" ||
		rolled["commitState"] != "unknown" ||
		rolled["createdCount"] != nil || rolled["updatedCount"] != nil {
		t.Fatalf("rolled back projection = %v", rolled)
	}

	// Host finish records the terminal execution outcome with unknown counts.
	finishResponse, finished := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/finish",
		map[string]any{"taskId": "task-1102", "state": "failed"},
	)
	if finishResponse.Code != http.StatusOK ||
		finished["state"] != "failed" ||
		finished["errorCode"] != "import.failed" ||
		finished["createdCount"] != nil {
		t.Fatalf("finish = %d %v", finishResponse.Code, finished)
	}
	// Repeated finish stays idempotent.
	repeatResponse, repeated := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/finish",
		map[string]any{"taskId": "task-1102", "state": "cancelled"},
	)
	if repeatResponse.Code != http.StatusOK || repeated["state"] != "failed" {
		t.Fatalf("repeat finish = %d %v", repeatResponse.Code, repeated)
	}

	// Every projection mutation stayed behind the business write gate.
	wantGates := []string{
		"import_history.start/task-1101",
		"mutation.apply/import-task-1101",
		"import_history.finish/task-1101",
		"import_history.start/task-1102",
		"mutation.apply/import-task-1102",
		"import_history.finish/task-1102",
		"import_history.finish/task-1102",
	}
	if len(fixture.gateKinds) != len(wantGates) {
		t.Fatalf("gate kinds = %v", fixture.gateKinds)
	}
	for index, want := range wantGates {
		if fixture.gateKinds[index] != want {
			t.Fatalf("gate kinds = %v, want %v", fixture.gateKinds, wantGates)
		}
	}
}

func TestImportHistoryHTTPProjectionSurvivesReopen(t *testing.T) {
	dataDir := t.TempDir()
	fixture := newImportHistoryFixture(t, dataDir, "")
	if _, entry := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start",
		fixture.startBody("task-reopen"),
	); entry["state"] != "interrupted" {
		t.Fatalf("start = %v", entry)
	}
	applied, receipt := fixture.call(
		t, http.MethodPost, "/api/vibetable/v1/mutations/apply",
		fixture.applyBody("task-reopen"),
	)
	if applied.Code != http.StatusOK || receipt["status"] != "applied" {
		t.Fatalf("apply = %d %v", applied.Code, receipt)
	}
	pb := fixture.pb
	event := &core.TerminateEvent{App: pb}
	if err := pb.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
		return event.App.ResetBootstrapState()
	}); err != nil {
		t.Fatal(err)
	}

	reopened := newImportHistoryFixture(t, dataDir, fixture.tableID)
	listResponse, payload := reopened.call(
		t, http.MethodGet, "/api/vibetable/v2/import-history", nil,
	)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("reopened list = %d %s", listResponse.Code, listResponse.Body.String())
	}
	items := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("reopened items = %v", items)
	}
	projection := items[0].(map[string]any)
	if projection["taskId"] != "task-reopen" ||
		projection["state"] != "succeeded" ||
		projection["commitState"] != "committed" ||
		projection["createdCount"] != float64(1) {
		t.Fatalf("reopened projection = %v", projection)
	}
}

func TestImportHistoryHTTPContractBoundaries(t *testing.T) {
	fixture := newImportHistoryFixture(t, t.TempDir(), "")

	// Unknown credential-shaped fields are rejected by strict decoding.
	for field, value := range map[string]any{
		"token":       "imp1.secret",
		"grant":       "grant-1",
		"sourcePath":  `C:\tmp\a.csv`,
		"accessToken": "abc",
	} {
		body := fixture.startBody("task-decode")
		body[field] = value
		response, payload := fixture.call(
			t, http.MethodPost, "/api/vibetable/v2/import-history/start", body,
		)
		if response.Code != http.StatusBadRequest ||
			payload["code"] != "import_history.request.invalid" {
			t.Fatalf("credential field %s accepted: %d %v", field, response.Code, payload)
		}
	}

	// Session auth is enforced exactly like every other sidecar route.
	request := httptest.NewRequest(
		http.MethodPost, "/api/vibetable/v2/import-history/start",
		bytes.NewReader([]byte(`{}`)),
	)
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing session = %d", response.Code)
	}

	// Invalid terminal states never touch the projection.
	badState, payload := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/finish",
		map[string]any{"taskId": "task-none", "state": "succeeded"},
	)
	if badState.Code != http.StatusBadRequest ||
		payload["code"] != "import_history.request.invalid" {
		t.Fatalf("succeeded finish = %d %v", badState.Code, payload)
	}
	missing, payload := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/finish",
		map[string]any{"taskId": "task-none", "state": "failed"},
	)
	if missing.Code != http.StatusNotFound ||
		payload["code"] != "import_history.task_not_found" {
		t.Fatalf("unknown task finish = %d %v", missing.Code, payload)
	}
	// Same key attributing a second task to the same collection is rejected.
	fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start",
		fixture.startBody("task-dup"),
	)
	duplicate := fixture.startBody("task-dup-2")
	duplicate["idempotencyKey"] = "import-task-dup"
	conflict, payload := fixture.call(
		t, http.MethodPost, "/api/vibetable/v2/import-history/start", duplicate,
	)
	if conflict.Code != http.StatusConflict ||
		payload["code"] != "import_history.idempotency_conflict" {
		t.Fatalf("duplicate attribution = %d %v", conflict.Code, payload)
	}
}
