package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func unrelatedContentVersionRegistration(t *testing.T, method string) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(json.RawMessage) error { return nil }, Handler: func(context.Context, json.RawMessage) (any, error) {
		t.Fatalf("unrelated named revision handler ran: %s", method)
		return nil, nil
	}}
}

func TestContentVersionGenericWritesAreClosedWhileReadsStayAvailable(t *testing.T) {
	pb := historyProductStore(t, t.TempDir())
	r := router.NewRouter(func(w http.ResponseWriter, request *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: request}}, nil
	})
	registerMetadataRoutes(r, metadata.New(pb), func(context.Context, string, string, func(context.Context) error) error {
		t.Fatal("generic write reached coordinator")
		return nil
	})
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"upsert", "delete"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/vibetable/v1/metadata/content_versions/"+suffix, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s generic write: %d %s", suffix, response.Code, response.Body)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/vibetable/v1/metadata/content_versions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("generic read: %d %s", response.Code, response.Body)
	}
}
