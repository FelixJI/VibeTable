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
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
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
	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v2/product/capabilities",
		secret,
	)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"legacy launch Product capabilities status = %d, want 404",
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
	created, titleFieldID := createProcessTableWithTitle(t, client, baseURL, secret)

	mutationBody := strings.ReplaceAll(`{
		"contractVersion":"2.0",
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
	}`, "process-test-table", created.TableID)
	mutationBody = strings.ReplaceAll(
		mutationBody, "schema_0001", created.SchemaRevision,
	)
	mutationBody = strings.ReplaceAll(
		mutationBody, strconv.Quote("title"), strconv.Quote(titleFieldID),
	)
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
		mutationBody, `"persisted"`, `"different"`, 1,
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

	emptyUpdate := strings.ReplaceAll(`{
		"contractVersion":"2.0",
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
	}`, "process-test-table", created.TableID)
	emptyUpdate = strings.ReplaceAll(
		emptyUpdate, "schema_0001", created.SchemaRevision,
	)
	emptyUpdate = strings.ReplaceAll(
		emptyUpdate, strconv.Quote("title"), strconv.Quote(titleFieldID),
	)
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
		`{"operation":"page","tableId":`+strconv.Quote(created.TableID)+`,"query":{"offset":0,"limit":10}}`,
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
		page.Snapshot.SchemaRevision != created.SchemaRevision ||
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
		validation.CurrentSchemaRevision != created.SchemaRevision ||
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
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("removed schema write status = %d, want 404", response.StatusCode)
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
	if logs := stderr.String(); !strings.Contains(logs, "sidecar.graceful_shutdown") {
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

	response = request(
		t, client, http.MethodGet,
		baseURL+"/api/vibetable/v2/product/capabilities", "",
	)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Product capabilities status=%d", response.StatusCode)
	}
	drainAndClose(t, response.Body)
	response = request(
		t, client, http.MethodGet,
		baseURL+"/api/vibetable/v2/product/capabilities", secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("Product capabilities status=%d body=%s", response.StatusCode, body)
	}
	var productCapabilities struct {
		ContractVersion string   `json:"contractVersion"`
		WorkspaceID     string   `json:"workspaceId"`
		SessionEpoch    uint64   `json:"sessionEpoch"`
		FenceEpoch      uint64   `json:"fenceEpoch"`
		ClaimID         string   `json:"claimId"`
		RPCMethods      []string `json:"rpcMethods"`
		Registrations   []struct {
			Method string `json:"method"`
			Scope  string `json:"scope"`
		} `json:"registrations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&productCapabilities); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if productCapabilities.ContractVersion != "2.0" ||
		productCapabilities.WorkspaceID != env[config.WorkspaceIDEnv] ||
		productCapabilities.SessionEpoch != 7 || productCapabilities.FenceEpoch != 3 ||
		productCapabilities.ClaimID != env[config.ClaimIDEnv] ||
		len(productCapabilities.RPCMethods) != 4 ||
		productCapabilities.RPCMethods[0] != "events.reconcile" ||
		productCapabilities.RPCMethods[1] != "file.list" ||
		productCapabilities.RPCMethods[2] != "schema.getTable" ||
		productCapabilities.RPCMethods[3] != "schema.list" ||
		len(productCapabilities.Registrations) != 4 ||
		productCapabilities.Registrations[0].Method != "events.reconcile" ||
		productCapabilities.Registrations[0].Scope != "workspace" ||
		productCapabilities.Registrations[1].Method != "file.list" ||
		productCapabilities.Registrations[1].Scope != "workspace" ||
		productCapabilities.Registrations[2].Method != "schema.getTable" ||
		productCapabilities.Registrations[2].Scope != "workspace" ||
		productCapabilities.Registrations[3].Method != "schema.list" ||
		productCapabilities.Registrations[3].Scope != "workspace" {
		t.Fatalf("Product capabilities = %#v", productCapabilities)
	}

	response = requestJSON(
		t, client, http.MethodPost, baseURL+"/api/vibetable/v2/product/rpc", secret,
		workspaceV2Request(0, 7, "schema.list", `{}`),
	)
	var catalogResponse struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalogResponse); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(catalogResponse.Result) != `{"tables":[]}` || len(catalogResponse.Error) != 0 {
		t.Fatalf("schema.list production response = %d %+v", response.StatusCode, catalogResponse)
	}

	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/product/rpc", secret,
		workspaceV2Request(0, 7, "not.migrated", `{}`),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("Product method-not-found status=%d body=%s", response.StatusCode, body)
	}
	var productMethodMissing struct {
		ID    string          `json:"id"`
		Wire  json.RawMessage `json:"wire"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&productMethodMissing); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if productMethodMissing.ID != "request-0" || len(productMethodMissing.Wire) == 0 ||
		productMethodMissing.Error.Code != -32601 ||
		productMethodMissing.Error.Message != "Method not found" {
		t.Fatalf("Product method-not-found envelope = %#v", productMethodMissing)
	}

	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/product/rpc", secret,
		workspaceV2Request(0, 7, "not.migrated", `[]`),
	)
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("Product non-object params status=%d body=%s", response.StatusCode, body)
	}
	var productInvalidRequest struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&productInvalidRequest); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if productInvalidRequest.Error.Code != -32600 ||
		productInvalidRequest.Error.Message != "Invalid Request" {
		t.Fatalf("Product invalid request envelope = %#v", productInvalidRequest)
	}

	tableCreateBody := `{
		"displayName":"Read purity",
		"operationId":"process-read-purity-create",
		"actor":{"id":"process-test","kind":"test"}
	}`
	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v2/schema/tables",
		secret,
		tableCreateBody,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("table create status=%d body=%s", response.StatusCode, body)
	}
	var created v2.TableCreateReceipt
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if created.TableID == "" || created.SchemaRevision != "schema_0001" {
		t.Fatalf("table create receipt = %#v", created)
	}
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
	assertBusinessReceiptKindCount(
		t,
		dataDir,
		map[string]int{
			"schema.table.create": 1,
		},
	)
	response = requestJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/vibetable/v2/schema/tables",
		secret,
		tableCreateBody,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("table create replay status=%d body=%s", response.StatusCode, body)
	}
	var replayed v2.TableCreateReceipt
	if err := json.NewDecoder(response.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if replayed.TableID != created.TableID ||
		replayed.SchemaRevision != "schema_0001" {
		t.Fatalf("table create replay = %#v", replayed)
	}
	revisionAfterReplay, err := writecoordinator.ReadPersistentMutationRevision(
		context.Background(),
		coordinatorPath,
		env[config.WorkspaceIDEnv],
	)
	if err != nil {
		t.Fatal(err)
	}
	if revisionAfterReplay != revisionBefore {
		t.Fatalf(
			"idempotent schema replay changed mutation revision from %d to %d",
			revisionBefore,
			revisionAfterReplay,
		)
	}
	response = request(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/vibetable/v2/schema/tables/"+created.TableID,
		secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("schema describe status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	revisionAfter, err := writecoordinator.ReadPersistentMutationRevision(
		context.Background(),
		coordinatorPath,
		env[config.WorkspaceIDEnv],
	)
	if err != nil {
		t.Fatal(err)
	}
	if revisionAfter != revisionBefore {
		t.Fatalf(
			"schema GET mutation revision changed from %d to %d",
			revisionBefore,
			revisionAfter,
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
		"formatVersion":2,
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

func createProcessTableWithTitle(
	t *testing.T,
	client *http.Client,
	baseURL string,
	secret string,
) (v2.TableCreateReceipt, string) {
	t.Helper()
	createRaw, err := json.Marshal(v2.TableCreateIntent{
		DisplayName: "Process test table",
		OperationID: "process-test-table-create",
		Actor:       v2.Actor{ID: "process-test", Kind: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/schema/tables",
		secret, string(createRaw),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("table create status=%d body=%s", response.StatusCode, body)
	}
	var created v2.TableCreateReceipt
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	recommended, err := v2.RecommendedDefaults(v2.LogicalText)
	if err != nil {
		t.Fatal(err)
	}
	planRaw, err := json.Marshal(v2.FieldChangeIntent{
		Action:            v2.ActionCreate,
		TableID:           created.TableID,
		ExpectedSchemaRev: created.SchemaRevision,
		Draft: &v2.FieldDraft{
			DisplayName: "Title",
			LogicalType: v2.LogicalText,
			Value:       recommended.Value,
			Constraints: recommended.Constraints,
			Storage:     recommended.Storage,
			Display:     recommended.Display,
		},
		Actor: v2.Actor{ID: "process-test", Kind: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/field-change/plan",
		secret, string(planRaw),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("field plan status=%d body=%s", response.StatusCode, body)
	}
	var plan v2.FieldChangePlan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	applyRaw, err := json.Marshal(v2.ApplyRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		OperationID:   "process-test-title-create",
		Actor:         v2.Actor{ID: "process-test", Kind: "test"},
		Confirmations: append([]string(nil), plan.Confirmations...),
	})
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v2/field-change/apply",
		secret, string(applyRaw),
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("field apply status=%d body=%s", response.StatusCode, body)
	}
	var applied v2.ApplyReceipt
	if err := json.NewDecoder(response.Body).Decode(&applied); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	created.SchemaRevision = applied.SchemaRevision
	return created, applied.FieldID
}

func assertBusinessReceiptKindCount(
	t *testing.T,
	dataDir string,
	expected map[string]int,
) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for kind, want := range expected {
		var count int
		if err := database.QueryRow(
			`SELECT COUNT(*) FROM workspace_v2_mutation_receipts
			  WHERE kind = ?`,
			kind,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("receipt kind %q count=%d want=%d", kind, count, want)
		}
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
