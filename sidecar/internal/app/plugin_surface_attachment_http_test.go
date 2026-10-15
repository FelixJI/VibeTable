package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
)

func TestPluginSurfaceAttachmentStageHTTPWireBudgets(t *testing.T) {
	f := newFileListFixtureWithLimit(t, 1<<20)
	workspace := "11111111-1111-4111-8111-111111111111"
	source, err := queryschema.New(f.pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	input, _ := surfaceIOInstallation(t, f.pb, workspace, f.tableID, []string{"id", f.fieldID})
	ports := &pluginSurfaceIO{attachments: f.manager}
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{}, mutation.WithAttachmentManager(f.manager))
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: req}}, nil
	})
	registerPluginSurfaceAPIRoutes(r, f.pb, source, kernel, workspace, 7, nil, ports)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	send := func(method string, request any, padding int) *httptest.ResponseRecorder {
		t.Helper()
		input.Method = method
		input.Request, _ = json.Marshal(request)
		wire, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if padding > 0 {
			if padding < len(wire) {
				t.Fatal("wire target below envelope size")
			}
			wire = append(wire, bytes.Repeat([]byte(" "), padding-len(wire))...)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, pluginSurfaceAPIPath, bytes.NewReader(wire)))
		return response
	}
	for _, size := range []int{800 * 1024, 1 << 20, (1 << 20) + 1} {
		content := make([]byte, size)
		copy(content, f.content)
		handle := strings.Repeat("h", 8) + string(rune('a'+size%3))
		response := send("attachment.stage", map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "name": "synthetic.png", "base64": base64.StdEncoding.EncodeToString(content), "handle": handle, "fileGrantId": "host-file", "runId": "host-run"}, 0)
		if size <= 1<<20 {
			if response.Code != http.StatusOK {
				t.Errorf("legal decoded bytes=%d status=%d body=%s", size, response.Code, response.Body)
			}
		} else if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "plugin_file_limit") {
			t.Errorf("oversize decoded bytes admitted: status=%d body=%s", response.Code, response.Body)
		}
	}
	originalInstallation := input.InstallationID
	input.InstallationID = "foreign-installation"
	denied := send("attachment.stage", map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "name": "denied.png", "base64": base64.StdEncoding.EncodeToString(make([]byte, 1<<20)), "handle": "denied-stage", "fileGrantId": "host-file", "runId": "host-run"}, 0)
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), "plugin_cursor_stale") {
		t.Fatalf("large stage bypassed identity: %d %s", denied.Code, denied.Body)
	}
	input.InstallationID = originalInstallation
	for _, method := range []string{"file.authorize", "attachment.stage", "not-a-method"} {
		padding := (1 << 20) + 1
		if method == "attachment.stage" {
			padding = (2 << 20) + 1
		}
		response := send(method, map[string]any{"operation": "pickRead"}, padding)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "plugin_capability_invalid") {
			t.Errorf("wire limit bypassed by %s: status=%d body=%s", method, response.Code, response.Body)
		}
	}
	response := send("attachment.stage", map[string]any{"collection": f.tableID, "recordId": f.recordID, "fieldId": f.fieldID, "name": "boundary.png", "base64": base64.StdEncoding.EncodeToString(f.content), "handle": "wire-boundary", "fileGrantId": "host-file", "runId": "host-run"}, 2<<20)
	if response.Code != http.StatusOK {
		t.Fatalf("exact stage wire boundary: %d %s", response.Code, response.Body)
	}
	response = send("file.authorize", map[string]any{"operation": "pickRead"}, 1<<20)
	if response.Code != http.StatusOK {
		t.Fatalf("ordinary legal request failed: %d %s", response.Code, response.Body)
	}
}

func TestPluginSurfaceAttachmentWireBudgetMatchesContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/plugin-surface-api.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Limits map[string]int `json:"limits"`
	}
	if err = json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Limits["attachmentStageRequestBytes"] != pluginSurfaceAttachmentStageRequestBytes || contract.Limits["messageBytes"] != maxMutationRequestBytes || contract.Limits["fileBytes"] != 1<<20 {
		t.Fatalf("attachment encoded/decoded or ordinary budget drift: %+v", contract.Limits)
	}
}
