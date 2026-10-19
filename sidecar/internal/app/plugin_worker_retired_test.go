package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
)

// TestRetiredWorkerMutationRouteRejectsWithStableCode pins the #486
// retirement boundary of the old arbitrary Worker mutation seam: the route
// stays session-authenticated and boundary-gated, but every direct call now
// receives the stable plugin_worker_retired rejection without reading the
// body, coordinating a business write or executing plugin code. The generic
// plugin surface keeps its own dedicated regression suites.
func TestRetiredWorkerMutationRouteRejectsWithStableCode(t *testing.T) {
	pb := schemaProductStore(t)
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{App: pb, Event: router.Event{Response: w, Request: req}}, nil
	})
	bindWorkspaceV2WriteBoundary(&core.ServeEvent{Router: r})
	secret, err := auth.Parse(strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	bindVibetableSessionAuth(r, secret)
	registerPluginWorkerRetiredRoutes(r)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body, secretHeader string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		request.Header.Set("Content-Type", "application/json")
		if secretHeader != "" {
			request.Header.Set(auth.HeaderName, secretHeader)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	legacyEnvelope, _ := json.Marshal(map[string]any{
		"projectKey": "local:0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7",
		"pluginId":   "com.example.worker", "installationId": "retired0record0001",
		"installationRevision": 1, "packageHash": "sha256:" + strings.Repeat("c", 64),
		"actionId": "save", "sessionEpoch": 7, "activeCollection": "tbl",
	})
	for name, body := range map[string]string{
		"legacy-envelope":      string(legacyEnvelope),
		"garbage":              "not json at all",
		"empty-object":         "{}",
		"privileged-injection": string(append(legacyEnvelope[:len(legacyEnvelope)-1], []byte(`,"privileged":true}`)...)),
	} {
		response := post(pluginWorkerMutationPath, body, strings.Repeat("2", 64))
		if response.Code != http.StatusGone {
			t.Fatalf("%s: status = %d %s", name, response.Code, response.Body.String())
		}
		var rejection struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil ||
			rejection.Code != pluginstore.WorkerRetiredReason ||
			rejection.Details["preservationRpc"] != "pluginPreservation.snapshot" {
			t.Fatalf("%s: rejection = %s", name, response.Body.String())
		}
	}
	if response := post(pluginWorkerMutationPath, string(legacyEnvelope), ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated legacy call = %d %s", response.Code, response.Body.String())
	}
	if response := post(pluginWorkerMutationPath+"/extra", "{}", strings.Repeat("2", 64)); response.Code != http.StatusLocked {
		t.Fatalf("adjacent path admitted = %d %s", response.Code, response.Body.String())
	}
	if response := post(pluginWorkerMutationPath, string(legacyEnvelope), strings.Repeat("3", 64)); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret admitted = %d %s", response.Code, response.Body.String())
	}
}
