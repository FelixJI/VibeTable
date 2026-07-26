package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/config"
	"github.com/vibetable/vibetable/sidecar/internal/launch"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

const helperProcessEnv = "VIBETABLE_TEST_HELPER_PROCESS"

func TestSidecarProcessReadyHealthAuthAndGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process integration test in short mode")
	}

	secret := strings.Repeat("42", 32)
	dataDir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestSidecarHelperProcess$")
	command.Env = normalizedEnvironment(map[string]string{
		helperProcessEnv:        "1",
		config.SessionSecretEnv: secret,
		config.DataDirEnv:       dataDir,
	})

	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start sidecar helper: %v", err)
	}

	waited := false
	t.Cleanup(func() {
		if !waited && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	type lineResult struct {
		line string
		err  error
	}
	lineReady := make(chan lineResult, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		lineReady <- lineResult{line: line, err: err}
	}()

	var result lineResult
	select {
	case result = <-lineReady:
	case <-time.After(30 * time.Second):
		t.Fatalf("ready handshake timed out; stderr=%s", stderr.String())
	}
	if result.err != nil {
		t.Fatalf("read ready handshake: %v; stderr=%s", result.err, stderr.String())
	}

	var ready launch.Ready
	if err := json.Unmarshal([]byte(result.line), &ready); err != nil {
		t.Fatalf("decode ready handshake %q: %v", result.line, err)
	}
	if ready.Contract != launch.ReadyContract {
		t.Fatalf("ready contract = %q", ready.Contract)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + ready.Address

	response := request(t, client, http.MethodGet, baseURL+"/api/vibetable/v1/health", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated health status = %d, want 401", response.StatusCode)
	}
	drainAndClose(t, response.Body)

	response = request(t, client, http.MethodGet, baseURL+"/api/vibetable/v1/health", secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated health status = %d, want 200", response.StatusCode)
	}
	var health struct {
		Status          string `json:"status"`
		StorageWritable bool   `json:"storageWritable"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if health.Status != "ok" || !health.StorageWritable {
		t.Fatalf("unexpected health response: %#v", health)
	}

	for _, protectedPath := range []string{"/_/", "/api/collections"} {
		response = request(t, client, http.MethodGet, baseURL+protectedPath, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf(
				"unauthenticated %s status = %d, want 401",
				protectedPath,
				response.StatusCode,
			)
		}
		drainAndClose(t, response.Body)
	}

	applyBody := `{
		"definition": {
			"contractVersion": "1.0",
			"tableId": "process-test-table",
			"physicalName": "process_test_table",
			"displayName": "Process test table",
			"kind": "base",
			"schemaRevision": "schema_0000",
			"archivePolicy": {
				"mode": "none",
				"fieldId": null,
				"archivedValue": null
			},
			"fields": [{
				"fieldId": "title",
				"physicalName": "title",
				"displayName": "Title",
				"kind": "scalar",
				"dataType": "shortText",
				"storageType": "",
				"nullable": true,
				"defaultValue": null,
				"constraints": [],
				"editor": {"kind": "text", "config": {}},
				"readOnly": false,
				"formula": null,
				"relation": null,
				"lookup": null,
				"attachmentPolicy": null
			}],
			"indexes": []
		},
		"expectedRevision": 0
	}`
	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v1/schema/apply",
		secret,
		applyBody,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("schema apply status = %d body=%s", response.StatusCode, body)
	}
	var applied struct {
		TableID        string `json:"tableId"`
		SchemaRevision string `json:"schemaRevision"`
	}
	if err := json.NewDecoder(response.Body).Decode(&applied); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if applied.TableID != "process-test-table" || applied.SchemaRevision != "schema_0001" {
		t.Fatalf("unexpected applied schema: %#v", applied)
	}

	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v1/schema/tables/process-test-table",
		secret,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("schema describe status = %d, want 200", response.StatusCode)
	}
	var described schema.TableDefinition
	if err := json.NewDecoder(response.Body).Decode(&described); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	formulaPreviewBody, err := json.Marshal(map[string]any{
		"definition":      described,
		"row":             map[string]any{"title": "draft"},
		"changedFieldIds": []string{"title"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/formulas/preview",
		secret, string(formulaPreviewBody),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("formula preview status = %d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)

	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v1/schema/tables",
		secret,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("schema list status = %d, want 200", response.StatusCode)
	}
	var listed struct {
		Tables []struct {
			TableID string `json:"tableId"`
		} `json:"tables"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed.Tables) != 1 || listed.Tables[0].TableID != "process-test-table" {
		t.Fatalf("unexpected schema list: %#v", listed)
	}

	mutationBody := `{
		"contractVersion":"1.0",
		"requestId":"process-request-1",
		"idempotencyKey":"process-idempotency-1",
		"tableId":"process-test-table",
		"schemaRevision":"schema_0001",
		"operations":[{
			"kind":"insert",
			"recordId":"processrecord01",
			"values":{"title":"persisted"}
		}],
		"actor":{"type":"user","id":"process-user","displayName":null},
		"expectedRevision":null,
		"expectedDigest":null
	}`
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/mutations/apply",
		secret, mutationBody,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("mutation apply status = %d body=%s", response.StatusCode, body)
	}
	var mutationReceipt mutation.Receipt
	if err := json.NewDecoder(response.Body).Decode(&mutationReceipt); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if mutationReceipt.Status != mutation.StatusApplied ||
		len(mutationReceipt.AffectedRows) != 1 ||
		mutationReceipt.AffectedRows[0].RecordID != "processrecord01" {
		t.Fatalf("unexpected mutation receipt: %#v", mutationReceipt)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/mutations/apply",
		secret, mutationBody,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mutation replay status = %d, want 200", response.StatusCode)
	}
	var replayed mutation.Receipt
	if err := json.NewDecoder(response.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if replayed.Status != mutation.StatusReplayed ||
		replayed.ChangeSetID == nil ||
		mutationReceipt.ChangeSetID == nil ||
		*replayed.ChangeSetID != *mutationReceipt.ChangeSetID {
		t.Fatalf("unexpected replay receipt: %#v", replayed)
	}
	conflictingMutation := strings.Replace(
		mutationBody, `"title":"persisted"`, `"title":"different"`, 1,
	)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/mutations/apply",
		secret, conflictingMutation,
	)
	if response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf(
			"mutation idempotency conflict status = %d body=%s",
			response.StatusCode, body,
		)
	}
	drainAndClose(t, response.Body)
	malformedMutation := strings.Replace(
		mutationBody,
		`"expectedDigest":null`,
		`"expectedDigest":null,"provider":"pocketbase"`,
		1,
	)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/mutations/apply",
		secret, malformedMutation,
	)
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf(
			"malformed mutation status = %d body=%s",
			response.StatusCode, body,
		)
	}
	drainAndClose(t, response.Body)

	emptyUpdate := `{
		"contractVersion":"1.0",
		"requestId":"process-empty-update",
		"idempotencyKey":"process-empty-update",
		"tableId":"process-test-table",
		"schemaRevision":"schema_0001",
		"operations":[{
			"kind":"update",
			"recordId":"processrecord01",
			"values":{}
		}],
		"actor":{"type":"user","id":"process-user","displayName":null},
		"expectedRevision":null,
		"expectedDigest":null
	}`
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/mutations/apply",
		secret, emptyUpdate,
	)
	if response.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf(
			"empty update status = %d body=%s",
			response.StatusCode, body,
		)
	}
	var emptyUpdateError mutation.ProductError
	if err := json.NewDecoder(response.Body).Decode(&emptyUpdateError); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if emptyUpdateError.Code != "mutation.operation.empty" {
		t.Fatalf("empty update error = %#v", emptyUpdateError)
	}

	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v1/query",
		secret,
		`{"operation":"page","tableId":"process-test-table","query":{"offset":0,"limit":10}}`,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("query page status = %d body=%s", response.StatusCode, body)
	}
	var page query.Page
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(page.Rows) != 1 ||
		page.TotalRows != 1 ||
		page.Snapshot.SchemaRevision != "schema_0001" ||
		page.Snapshot.DataRevision != 1 ||
		len(page.Snapshot.Digest) != 64 {
		t.Fatalf("unexpected query page: %#v", page)
	}
	validationBody, err := json.Marshal(map[string]any{
		"snapshot": page.Snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v1/query/validate-snapshot",
		secret,
		string(validationBody),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("snapshot validation status = %d body=%s", response.StatusCode, body)
	}
	var validation query.SnapshotValidation
	if err := json.NewDecoder(response.Body).Decode(&validation); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !validation.Valid ||
		validation.CurrentSchemaRevision != "schema_0001" ||
		validation.CurrentDataRevision != 1 {
		t.Fatalf("unexpected snapshot validation: %#v", validation)
	}

	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v1/schema/validate",
		secret,
		`{"definition":{},"expectedRevision":0,"unknown":true}`,
	)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid schema request status = %d, want 400", response.StatusCode)
	}
	drainAndClose(t, response.Body)

	response = request(t, client, http.MethodPost, baseURL+"/api/vibetable/v1/shutdown", secret)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want 202", response.StatusCode)
	}
	drainAndClose(t, response.Body)

	processDone := make(chan error, 1)
	go func() {
		processDone <- command.Wait()
	}()
	select {
	case err := <-processDone:
		waited = true
		if err != nil {
			t.Fatalf("sidecar graceful exit: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("sidecar did not exit after shutdown; stderr=%s", stderr.String())
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatal("sidecar process state does not report exit")
	}

	connection, err := net.DialTimeout("tcp4", ready.Address, 500*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatalf("listener %s remained reachable after process exit", ready.Address)
	}
	if logs := stderr.String(); !strings.Contains(logs, "sidecar graceful shutdown") {
		t.Fatalf("graceful shutdown log missing: %s", logs)
	}
	removeDirectoryEventually(t, dataDir)
}

func TestSidecarHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		return
	}
	os.Args = []string{"vibetable-pb"}
	os.Exit(run(nil))
}

func requestJSON(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	secret string,
	body string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if secret != "" {
		request.Header.Set(auth.HeaderName, secret)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func request(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	secret string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		request.Header.Set(auth.HeaderName, secret)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func drainAndClose(t *testing.T, body io.ReadCloser) {
	t.Helper()
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Error(err)
	}
	if err := body.Close(); err != nil {
		t.Error(err)
	}
}

func removeDirectoryEventually(t *testing.T, path string) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if err := os.RemoveAll(path); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("remove sidecar test data directory: %v", lastErr)
}

func normalizedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string, len(os.Environ())+len(overrides))
	names := make(map[string]string, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key := strings.ToUpper(name)
		if _, exists := values[key]; !exists {
			names[key] = name
			values[key] = value
		}
	}
	for name, value := range overrides {
		key := strings.ToUpper(name)
		names[key] = name
		values[key] = value
	}

	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, fmt.Sprintf("%s=%s", names[key], value))
	}
	return result
}
