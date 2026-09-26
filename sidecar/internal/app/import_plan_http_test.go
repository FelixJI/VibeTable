package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
)

// importPlanHTTPRouter builds the production-side router stack for the import
// plan lifecycle ports: the real session admission hook, the workspace v2
// write boundary and the real route registrations.
func importPlanHTTPRouter(t *testing.T) (*router.Router[*core.RequestEvent], string) {
	t.Helper()
	secret, encoded, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	r := router.NewRouter(func(writer http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: writer, Request: request}}, nil
	})
	bindVibetableSessionAuth(r, secret)
	bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
	registerImportRoutes(r, nil, newImportPlanOwner("11111111-1111-4111-8111-111111111111"))
	return r, encoded
}

func importPlanHTTPCall(
	t *testing.T,
	mux *router.Router[*core.RequestEvent],
	path string,
	body any,
	session string,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	built, buildErr := mux.BuildMux()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	if session != "" {
		request.Header.Set(auth.HeaderName, session)
	}
	built.ServeHTTP(response, request)
	var payload map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response, payload
}

func TestImportPlanHTTPRequiresExactSessionSecret(t *testing.T) {
	r, encoded := importPlanHTTPRouter(t)
	mintBody := map[string]any{
		"contract":       importPlanContract,
		"collection":     "vibetable_demo",
		"grantId":        "grant-1",
		"schemaRevision": "schema-1",
		"capabilityHash": "cap-1",
		"sourceHash":     "sha-1",
		"mode":           "create_only",
		"upsertKey":      nil,
		"rows":           []map[string]any{},
	}
	for name, session := range map[string]string{
		"missing header":  "",
		"wrong secret":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"malformed value": encoded + "x",
	} {
		response, payload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans", mintBody, session)
		if response.Code != http.StatusUnauthorized || payload["code"] != "session.unauthorized" {
			t.Fatalf("%s: got %d %v", name, response.Code, payload)
		}
	}
	response, _ := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans", mintBody, encoded)
	if response.Code != http.StatusOK {
		t.Fatalf("valid session rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestImportPlanHTTPAdjacentPathsStayFailClosed(t *testing.T) {
	r, encoded := importPlanHTTPRouter(t)
	for _, path := range []string{
		"/api/vibetable/v2/import-plans/replay",
		"/api/vibetable/v2/import-plans/stage/extra",
		"/api/vibetable/v2/import-plans/",
	} {
		response, payload := importPlanHTTPCall(t, r, path, map[string]any{}, encoded)
		if response.Code != http.StatusLocked || payload["code"] != "workspace.v1_write_disabled" {
			t.Fatalf("adjacent path %s opened: %d %v", path, response.Code, payload)
		}
	}
}

func TestImportPlanHTTPLifecycleCarriesAttemptLease(t *testing.T) {
	r, encoded := importPlanHTTPRouter(t)
	minted, payload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans", map[string]any{
		"contract":       importPlanContract,
		"collection":     "vibetable_demo",
		"grantId":        "grant-1",
		"schemaRevision": "schema-1",
		"capabilityHash": "cap-1",
		"sourceHash":     "sha-1",
		"mode":           "create_only",
		"upsertKey":      nil,
		"rows":           []map[string]any{{"sourceRow": 2, "values": map[string]any{"number": "A-1"}}},
	}, encoded)
	if minted.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", minted.Code, minted.Body.String())
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatalf("mint reply missing token: %v", payload)
	}
	staged, stagePayload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans/stage", map[string]any{
		"contract":       importPlanContract,
		"token":          token,
		"grantId":        "grant-1",
		"collection":     "vibetable_demo",
		"mode":           "create_only",
		"capabilityHash": "cap-1",
	}, encoded)
	if staged.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", staged.Code, staged.Body.String())
	}
	attempt, ok := stagePayload["attempt"].(float64)
	if !ok || attempt != 1 {
		t.Fatalf("stage reply attempt = %v, want 1", stagePayload["attempt"])
	}
	bind, bindPayload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans/bind", map[string]any{
		"contract":          importPlanContract,
		"token":             token,
		"idempotencyPrefix": "imp-http",
		"attempt":           attempt,
	}, encoded)
	if bind.Code != http.StatusOK || bindPayload["idempotencyKey"] != "imp-http-0" {
		t.Fatalf("bind: %d %s", bind.Code, bind.Body.String())
	}
	stale, stalePayload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans/settle", map[string]any{
		"contract": importPlanContract,
		"token":    token,
		"outcome":  "rejected",
		"attempt":  attempt + 7,
	}, encoded)
	if stale.Code != http.StatusUnprocessableEntity || stalePayload["code"] != "import_plan_stale" {
		t.Fatalf("stale settle: %d %v", stale.Code, stalePayload)
	}
	settled, settlePayload := importPlanHTTPCall(t, r, "/api/vibetable/v2/import-plans/settle", map[string]any{
		"contract": importPlanContract,
		"token":    token,
		"outcome":  "committed",
		"attempt":  attempt,
	}, encoded)
	if settled.Code != http.StatusOK || settlePayload["consumed"] != true {
		t.Fatalf("settle: %d %v", settled.Code, settlePayload)
	}
}
