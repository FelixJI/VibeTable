package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/config"
	"github.com/vibetable/vibetable/sidecar/internal/launch"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
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
	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v2/capabilities",
		secret,
	)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"legacy launch v2 capabilities status = %d, want 404",
			response.StatusCode,
		)
	}
	drainAndClose(t, response.Body)

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

func TestSidecarWorkspaceV2HTTPFailsClosedAndPersistsAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process integration test in short mode")
	}
	secret := strings.Repeat("24", 32)
	root := filepath.Join(t.TempDir(), "workspace")
	dataDir := createV2Workspace(t, root)
	env := map[string]string{
		helperProcessEnv:        "1",
		config.SessionSecretEnv: secret,
		config.DataDirEnv:       dataDir,
		config.WorkspaceIDEnv:   "11111111-1111-4111-8111-111111111111",
		config.SessionEpochEnv:  "7",
		config.FenceEpochEnv:    "3",
		config.ClaimIDEnv:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	command, stderr, baseURL := startSidecarHelper(t, env)
	client := &http.Client{Timeout: 15 * time.Second}

	response := request(
		t, client, http.MethodGet,
		baseURL+"/api/vibetable/v2/capabilities", secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("capabilities status=%d body=%s", response.StatusCode, body)
	}
	var capabilities struct {
		WorkspaceID   string   `json:"workspaceId"`
		RPCMethods    []string `json:"rpcMethods"`
		Registrations []struct {
			Method string `json:"method"`
			Scope  string `json:"scope"`
		} `json:"registrations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if capabilities.WorkspaceID != env[config.WorkspaceIDEnv] ||
		len(capabilities.RPCMethods) < 9 ||
		len(capabilities.Registrations) != len(capabilities.RPCMethods) {
		t.Fatalf("capabilities = %#v", capabilities)
	}

	formulaSchemaBody := workspaceV2FormulaSchemaBody(t)
	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v1/schema/apply",
		secret,
		formulaSchemaBody,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("formula schema apply status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	waitForFormulaJobsSettled(t, dataDir)
	coordinatorPath := filepath.Join(
		root,
		".vibetable",
		"coordination",
		"write-coordinator.db",
	)
	revisionBefore, err := writecoordinator.ReadPersistentMutationRevision(
		context.Background(),
		coordinatorPath,
		env[config.WorkspaceIDEnv],
	)
	if err != nil {
		t.Fatal(err)
	}
	projectionBefore := readFormulaReadProjection(t, dataDir)
	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v1/schema/tables/formula-read-purity",
		secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("formula schema describe status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	waitForFormulaJobsSettled(t, dataDir)
	revisionAfter, err := writecoordinator.ReadPersistentMutationRevision(
		context.Background(),
		coordinatorPath,
		env[config.WorkspaceIDEnv],
	)
	if err != nil {
		t.Fatal(err)
	}
	projectionAfter := readFormulaReadProjection(t, dataDir)
	if revisionAfter != revisionBefore {
		t.Fatalf(
			"schema GET mutation revision changed from %d to %d",
			revisionBefore,
			revisionAfter,
		)
	}
	if projectionAfter != projectionBefore {
		t.Fatalf(
			"schema GET changed jobs/business projection: before=%#v after=%#v",
			projectionBefore,
			projectionAfter,
		)
	}

	for _, legacyWrite := range []struct {
		method string
		path   string
		body   string
	}{
		{
			method: http.MethodPost,
			path:   "/api/collections/vibetable_tables/records",
			body:   `{}`,
		},
	} {
		response = requestJSON(
			t,
			client,
			legacyWrite.method,
			baseURL+legacyWrite.path,
			secret,
			legacyWrite.body,
		)
		if response.StatusCode != http.StatusLocked {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf(
				"legacy write %s status=%d body=%s",
				legacyWrite.path,
				response.StatusCode,
				body,
			)
		}
		var denied struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(response.Body).Decode(&denied); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if denied.Code != "workspace.v1_write_disabled" {
			t.Fatalf("legacy write error = %#v", denied)
		}
	}

	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(1, 7, "retention.get", `{}`)+` {}`,
	)
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("trailing JSON status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	invalid := workspaceV2Request(
		1, 7, "retention.get", `{"unknown":true}`,
	)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret, invalid,
	)
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("unknown params status=%d body=%s", response.StatusCode, body)
	}
	var invalidEnvelope struct {
		ID    string `json:"id"`
		Wire  any    `json:"wire"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&invalidEnvelope); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if invalidEnvelope.ID != "request-1" ||
		invalidEnvelope.Wire == nil ||
		invalidEnvelope.Error.Code != "workspace.request_invalid" {
		t.Fatalf("invalid envelope = %#v", invalidEnvelope)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(1, 7, "retention.get", `{}`),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("valid reused sequence status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(
			2,
			7,
			"retention.update",
			`{"expectedRevision":1,"snapshotDays":40,"snapshotCount":60,"snapshotBuckets":["daily","weekly"],"fileRevisionDays":35,"fileRevisionCount":120,"fileRevisionBuckets":["daily","monthly"],"repositoryLimitBytes":null}`,
		),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("retention update status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(3, 6, "snapshot.list", `{"cursor":null,"limit":50}`),
	)
	var staleEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&staleEnvelope); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if staleEnvelope.Error.Code != "workspace.session_epoch_stale" {
		t.Fatalf("stale epoch response = %#v", staleEnvelope)
	}
	response = request(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/shutdown", secret,
	)
	drainAndClose(t, response.Body)
	waitSidecarHelper(t, command, stderr)

	command, stderr, baseURL = startSidecarHelper(t, env)
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(
			2,
			7,
			"retention.update",
			`{"expectedRevision":1,"snapshotDays":40,"snapshotCount":60,"snapshotBuckets":["daily","weekly"],"fileRevisionDays":35,"fileRevisionCount":120,"fileRevisionBuckets":["daily","monthly"],"repositoryLimitBytes":null}`,
		),
	)
	var duplicateEnvelope struct {
		Result struct {
			PolicyRevision uint64 `json:"policyRevision"`
			SnapshotDays   uint64 `json:"snapshotDays"`
		} `json:"result"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&duplicateEnvelope); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if duplicateEnvelope.Error != nil ||
		duplicateEnvelope.Result.PolicyRevision != 2 ||
		duplicateEnvelope.Result.SnapshotDays != 40 {
		t.Fatalf(
			"restart did not replay durable operation: %#v",
			duplicateEnvelope,
		)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/rpc", secret,
		workspaceV2Request(3, 7, "retention.get", `{}`),
	)
	var policyEnvelope struct {
		Result struct {
			PolicyRevision uint64 `json:"policyRevision"`
			SnapshotDays   uint64 `json:"snapshotDays"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&policyEnvelope); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if policyEnvelope.Result.PolicyRevision != 2 ||
		policyEnvelope.Result.SnapshotDays != 40 {
		t.Fatalf("restarted policy = %#v", policyEnvelope)
	}
	response = request(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/shutdown", secret,
	)
	drainAndClose(t, response.Body)
	waitSidecarHelper(t, command, stderr)
}

func TestSidecarHelperProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		return
	}
	os.Args = []string{"vibetable-pb"}
	os.Exit(run(nil))
}

func startSidecarHelper(
	t *testing.T,
	env map[string]string,
) (*exec.Cmd, *bytes.Buffer, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSidecarHelperProcess$")
	command.Env = normalizedEnvironment(env)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	type result struct {
		line string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ready <- result{line: line, err: err}
	}()
	select {
	case value := <-ready:
		if value.err != nil {
			t.Fatalf("read helper ready: %v stderr=%s", value.err, stderr)
		}
		var record launch.Ready
		if err := json.Unmarshal([]byte(value.line), &record); err != nil {
			t.Fatal(err)
		}
		return command, stderr, "http://" + record.Address
	case <-time.After(45 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("helper ready timeout stderr=%s", stderr)
	}
	return nil, nil, ""
}

func waitSidecarHelper(
	t *testing.T,
	command *exec.Cmd,
	stderr *bytes.Buffer,
) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sidecar helper exit: %v stderr=%s", err, stderr)
		}
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("sidecar helper stop timeout stderr=%s", stderr)
	}
}

func createV2Workspace(t *testing.T, root string) string {
	t.Helper()
	metadata := filepath.Join(root, ".vibetable")
	for _, directory := range []string{
		"data", "topology", "objects", "audit", "snapshots",
		"coordination", "quarantine", "temp",
	} {
		if err := os.MkdirAll(
			filepath.Join(metadata, directory),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
	}
	raw := `{
		"contractVersion":"2.0",
		"formatVersion":1,
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"displayName":"HTTP test workspace",
		"createdAt":"2026-07-28T08:00:00Z",
		"storageMode":"direct",
		"encryptionMode":"convenient",
		"repositoryFormat":"kopia-v3",
		"topologySchemaVersion":1,
		"businessSchemaVersion":1,
		"importedFromWorkspaceId":null,
		"sourceSnapshotId":null
	}`
	if err := os.WriteFile(
		filepath.Join(metadata, "workspace.json"),
		[]byte(raw),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(metadata, "data")
}

func workspaceV2Request(
	sequence uint64,
	epoch uint64,
	method string,
	params string,
) string {
	return `{"jsonrpc":"2.0","id":"request-` +
		strconv.FormatUint(sequence, 10) +
		`","method":` + strconv.Quote(method) +
		`,"wire":{"scope":"workspace","workspaceId":"11111111-1111-4111-8111-111111111111","sessionEpoch":` +
		strconv.FormatUint(epoch, 10) +
		`,"operationId":"` + fmt.Sprintf(
		"bbbbbbbb-bbbb-4bbb-8bbb-%012d",
		sequence,
	) + `","sequence":` +
		strconv.FormatUint(sequence, 10) +
		`},"params":` + params + `}`
}

func workspaceV2FormulaSchemaBody(t *testing.T) string {
	t.Helper()
	change := schemaapi.Change{
		Definition: schema.TableDefinition{
			ContractVersion: "1.0",
			TableID:         "formula-read-purity",
			PhysicalName:    "formula_read_purity",
			DisplayName:     "Formula read purity",
			Kind:            schema.TableKindBase,
			SchemaRevision:  "schema_0000",
			ArchivePolicy: schema.ArchivePolicy{
				Mode: schema.ArchiveModeNone,
			},
			Fields: []schema.FieldDefinition{
				{
					FieldID:      "quantity",
					PhysicalName: "quantity",
					DisplayName:  "Quantity",
					Kind:         schema.FieldKindScalar,
					DataType:     schema.DataTypeInteger,
					StorageType:  schema.StorageNumber,
					Nullable:     true,
					Constraints:  []schema.FieldConstraint{},
					Editor: schema.EditorDefinition{
						Kind: "number", Config: map[string]any{},
					},
				},
				{
					FieldID:      "total",
					PhysicalName: "total",
					DisplayName:  "Total",
					Kind:         schema.FieldKindFormula,
					DataType:     schema.DataTypeFormula,
					StorageType:  schema.StorageNumber,
					Nullable:     false,
					Constraints:  []schema.FieldConstraint{},
					Editor: schema.EditorDefinition{
						Kind: "readonly", Config: map[string]any{},
					},
					ReadOnly: true,
					Formula: &schema.FormulaSpec{
						Language:   "cel-v1",
						Source:     "quantity * 2",
						ResultType: schema.DataTypeInteger,
						Version:    1,
						Status:     "ready",
					},
				},
			},
			Indexes: []schema.IndexDefinition{},
		},
		ExpectedRevision: 0,
		OperationID:      "formula-read-purity-create",
	}
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type formulaReadProjection struct {
	Jobs         int
	JobStates    string
	FormulaState string
	TableState   string
	BusinessRows int
}

func readFormulaReadProjection(t *testing.T, dataDir string) formulaReadProjection {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	result := formulaReadProjection{}
	queries := []struct {
		query  string
		target any
	}{
		{
			"SELECT COUNT(*) FROM vibetable_jobs",
			&result.Jobs,
		},
		{
			`SELECT COALESCE(GROUP_CONCAT(
				id || ':' || state || ':' || CAST(progress_json AS TEXT), '|'
			), '') FROM vibetable_jobs`,
			&result.JobStates,
		},
		{
			`SELECT COALESCE(GROUP_CONCAT(
				field_id || ':' || status || ':' || version, '|'
			), '') FROM vibetable_formulas`,
			&result.FormulaState,
		},
		{
			`SELECT COALESCE(GROUP_CONCAT(
				table_id || ':' || schema_revision || ':' ||
				CAST(definition_json AS TEXT), '|'
			), '') FROM vibetable_tables`,
			&result.TableState,
		},
		{
			"SELECT COUNT(*) FROM formula_read_purity",
			&result.BusinessRows,
		},
	}
	for _, item := range queries {
		if err := database.QueryRow(item.query).Scan(item.target); err != nil {
			t.Fatalf("read formula GET projection: %v", err)
		}
	}
	return result
}

func waitForFormulaJobsSettled(t *testing.T, dataDir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		database, err := sql.Open("sqlite", filepath.Join(dataDir, "data.db"))
		if err == nil {
			var pending int
			queryErr := database.QueryRow(
				"SELECT COUNT(*) FROM vibetable_jobs " +
					"WHERE state='queued' OR state='running'",
			).Scan(&pending)
			_ = database.Close()
			if queryErr == nil && pending == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("formula jobs did not settle")
		}
		time.Sleep(25 * time.Millisecond)
	}
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
