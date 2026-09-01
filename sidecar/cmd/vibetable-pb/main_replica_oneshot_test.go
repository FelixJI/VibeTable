package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
	"github.com/vibetable/vibetable/sidecar/internal/config"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/launch"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

const (
	replicaOneShotHelperEnv = "VIBETABLE_TEST_REPLICA_ONESHOT_HELPER"
	replicaRuntimeHelperEnv = "VIBETABLE_TEST_REPLICA_RUNTIME_HELPER"
	replicaProcessTimeout   = 45 * time.Second
)

func TestReplicaOneShotFailureKeepsStdoutEmpty(t *testing.T) {
	base := map[string]string{
		replicaOneShotHelperEnv: "verify",
		config.SessionSecretEnv: strings.Repeat("01", 32),
		config.DataDirEnv:       filepath.Join(t.TempDir(), "activity", ".vibetable", "data"),
		config.ActivityRootEnv:  "",
		config.WorkspaceIDEnv:   "11111111-1111-4111-8111-111111111111",
		config.SessionEpochEnv:  "7",
		config.FenceEpochEnv:    "3",
		config.ClaimIDEnv:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	for _, test := range []struct {
		name     string
		root     string
		exitCode int
	}{
		{name: "invalid config", exitCode: 2},
		{
			name:     "unavailable replica",
			root:     filepath.Join(t.TempDir(), "missing-replica"),
			exitCode: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := make(map[string]string, len(base)+1)
			for name, value := range base {
				env[name] = value
			}
			if test.root != "" {
				env[config.ReplicaRootEnv] = test.root
			}
			ctx, cancel := context.WithTimeout(context.Background(), replicaProcessTimeout)
			defer cancel()
			command := exec.CommandContext(
				ctx,
				os.Args[0],
				"-test.run=^TestReplicaOneShotCLIHelperProcess$",
			)
			command.Env = normalizedEnvironment(env)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("verify failure case %q timed out; stderr=%s", test.name, &stderr)
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != test.exitCode {
				t.Fatalf(
					"exit=%v want=%d stderr=%s",
					err,
					test.exitCode,
					stderr.String(),
				)
			}
			if stdout.Len() != 0 {
				t.Fatalf(
					"failed one-shot wrote stdout: %q",
					stdout.String(),
				)
			}
		})
	}
}

func TestReplicaOneShotRecoveredDataStartsProductionProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replica production process round trip in short mode")
	}
	const workspaceID = "11111111-1111-4111-8111-111111111111"
	const claimID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secret := strings.Repeat("31", 32)
	container := t.TempDir()
	source := filepath.Join(container, "source")
	sourceData := createV2Workspace(t, source)
	identity := map[string]string{
		config.SessionSecretEnv: secret,
		config.WorkspaceIDEnv:   workspaceID,
		config.SessionEpochEnv:  "1",
		config.FenceEpochEnv:    "3",
		config.ClaimIDEnv:       claimID,
	}

	bootstrap := startReplicaRuntimeProcess(
		t,
		replicaStageEnvironment(identity, sourceData, "", ""),
	)
	bootstrap.stop(t, secret)
	selected := prepareMirroredReplica(t, source, container)

	initialized := runReplicaOneShotProcess(t, "initialize", replicaStageEnvironment(
		identity, sourceData, "", selected,
	))
	assertReplicaOneShotReceipt(t, initialized, "initialize", workspaceID, "")
	verified := runReplicaOneShotProcess(t, "verify", replicaStageEnvironment(
		identity, sourceData, "", selected,
	))
	assertReplicaOneShotReceipt(t, verified, "verify", workspaceID, "")

	recovered := filepath.Join(container, "recovered")
	recoveredData := filepath.Join(recovered, ".vibetable", "data")
	recovery := runReplicaOneShotProcess(t, "recover", replicaStageEnvironment(
		identity, recoveredData, recovered, selected,
	))
	assertReplicaOneShotReceipt(t, recovery, "recover", workspaceID, recovered)

	runtime := startReplicaRuntimeProcess(
		t,
		replicaStageEnvironment(identity, recoveredData, "", selected),
	)
	assertReplicaRuntimeCapabilities(t, runtime.baseURL, secret, workspaceID, claimID)
	runtime.stop(t, secret)
	removeDirectoryEventually(t, container)
}

type replicaRuntimeProcess struct {
	command *exec.Cmd
	stderr  *bytes.Buffer
	ready   chan replicaProcessReadyResult
	done    chan struct{}
	waitErr error
	tail    replicaProcessTail
	baseURL string
}

type replicaProcessReadyResult struct {
	ready launch.Ready
	err   error
}

type replicaProcessTail struct {
	raw []byte
	err error
}

type replicaProcessReady launch.Ready

func (value replicaProcessReady) Validate() error {
	ready := launch.Ready(value)
	host, portText, err := net.SplitHostPort(ready.Address)
	port, portErr := strconv.Atoi(portText)
	if ready.Contract != launch.ReadyContract || ready.Event != "sidecar.ready" ||
		ready.PID <= 0 || err != nil || portErr != nil || host != "127.0.0.1" ||
		port < 1 || port > 65_535 {
		return fmt.Errorf("invalid production ready record")
	}
	if ready.Build != buildinfo.Current(migrations.Hash()) {
		return fmt.Errorf("invalid production build identity")
	}
	return nil
}

func startReplicaRuntimeProcess(
	t *testing.T,
	env map[string]string,
) *replicaRuntimeProcess {
	t.Helper()
	values := cloneReplicaEnvironment(env)
	values[replicaRuntimeHelperEnv] = "1"
	command := exec.Command(os.Args[0], "-test.run=^TestReplicaRuntimeHelperProcess$")
	command.Env = normalizedEnvironment(values)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &replicaRuntimeProcess{
		command: command,
		stderr:  &bytes.Buffer{},
		ready:   make(chan replicaProcessReadyResult, 1),
		done:    make(chan struct{}),
	}
	command.Stderr = process.stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start replica runtime: %v", err)
	}
	go process.observeStdout(stdout)
	t.Cleanup(func() { process.ensureStopped(t) })

	var readyResult replicaProcessReadyResult
	select {
	case readyResult = <-process.ready:
	case <-time.After(replicaProcessTimeout):
		process.terminate()
		t.Fatalf("replica runtime ready timeout; stderr=%s", process.stderr)
	}
	if readyResult.err != nil {
		process.terminate()
		t.Fatalf("read replica runtime ready: %v; stderr=%s", readyResult.err, process.stderr)
	}
	ready := readyResult.ready
	if ready.PID != command.Process.Pid {
		process.terminate()
		t.Fatalf("unexpected replica runtime ready: %#v", ready)
	}
	process.baseURL = "http://" + ready.Address
	return process
}

func (process *replicaRuntimeProcess) observeStdout(stdout io.ReadCloser) {
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadBytes('\n')
	result := replicaProcessReadyResult{err: err}
	if err == nil {
		var decoded replicaProcessReady
		decoded, result.err = contractsv2.DecodeStrict[replicaProcessReady](line)
		result.ready = launch.Ready(decoded)
	}
	process.ready <- result
	process.tail.raw, process.tail.err = io.ReadAll(reader)
	process.waitErr = process.command.Wait()
	close(process.done)
}

func (process *replicaRuntimeProcess) stop(t *testing.T, secret string) {
	t.Helper()
	response := request(
		t, &http.Client{Timeout: 15 * time.Second}, http.MethodPost,
		process.baseURL+"/api/vibetable/v1/shutdown", secret,
	)
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("replica runtime shutdown status=%d body=%s", response.StatusCode, body)
	}
	drainAndClose(t, response.Body)
	select {
	case <-process.done:
	case <-time.After(replicaProcessTimeout):
		process.terminate()
		t.Fatalf("replica runtime shutdown timeout; stderr=%s", process.stderr)
	}
	if process.waitErr != nil {
		t.Fatalf("replica runtime exit: %v; stderr=%s", process.waitErr, process.stderr)
	}
	if process.tail.err != nil || len(process.tail.raw) != 0 {
		t.Fatalf("replica runtime stdout tail=%q err=%v", process.tail.raw, process.tail.err)
	}
}

func (process *replicaRuntimeProcess) terminate() {
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.command.Process.Kill()
	select {
	case <-process.done:
	case <-time.After(replicaProcessTimeout):
	}
}

func (process *replicaRuntimeProcess) ensureStopped(t *testing.T) {
	t.Helper()
	select {
	case <-process.done:
		return
	default:
	}
	process.terminate()
	select {
	case <-process.done:
	default:
		t.Errorf("replica runtime process remained alive")
	}
}

func prepareMirroredReplica(t *testing.T, source, container string) string {
	t.Helper()
	manifestPath := filepath.Join(source, ".vibetable", "workspace.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contractsv2.DecodeStrict[contractsv2.WorkspaceManifest](raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest.StorageMode = "mirrored"
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(container, "selected")
	if err := os.MkdirAll(filepath.Join(selected, ".vibetable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(selected, ".vibetable", "workspace.json"), raw, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	return selected
}

func replicaStageEnvironment(
	identity map[string]string,
	dataDir, activityRoot, replicaRoot string,
) map[string]string {
	result := cloneReplicaEnvironment(identity)
	result[config.DataDirEnv] = dataDir
	result[config.ActivityRootEnv] = activityRoot
	result[config.ReplicaRootEnv] = replicaRoot
	return result
}

func runReplicaOneShotProcess(
	t *testing.T,
	operation string,
	env map[string]string,
) workspacev2.ReplicaOneShotReceipt {
	t.Helper()
	values := cloneReplicaEnvironment(env)
	values[replicaOneShotHelperEnv] = operation
	ctx, cancel := context.WithTimeout(context.Background(), replicaProcessTimeout)
	defer cancel()
	command := exec.CommandContext(
		ctx, os.Args[0], "-test.run=^TestReplicaOneShotCLIHelperProcess$",
	)
	command.Env = normalizedEnvironment(values)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("%s replica one-shot timed out; stderr=%s", operation, &stderr)
		}
		t.Fatalf("%s replica one-shot: %v; stderr=%s", operation, err, &stderr)
	}
	expectedFields := []string{
		"activityRoot", "catalogRevision", "checkpointId", "contractVersion",
		"mutationRevision", "operation", "receiptHash", "replicaId",
		"requiredMutationRevision", "snapshotId", "verifiedAt", "workspaceId",
	}
	if operation == "recover" {
		expectedFields = append(expectedFields, "restored")
	} else {
		expectedFields = append(expectedFields, "healthy")
	}
	requireExactJSONFields(t, stdout.Bytes(), expectedFields)
	var receipt workspacev2.ReplicaOneShotReceipt
	decodeExactJSON(t, stdout.Bytes(), &receipt)
	return receipt
}

func assertReplicaOneShotReceipt(
	t *testing.T,
	receipt workspacev2.ReplicaOneShotReceipt,
	operation, workspaceID, activityRoot string,
) {
	t.Helper()
	if receipt.ContractVersion != contractsv2.ContractVersion ||
		receipt.Operation != operation || receipt.WorkspaceID != workspaceID ||
		receipt.CatalogRevision == 0 || receipt.CheckpointID == "" ||
		receipt.ReplicaID == "" || receipt.SnapshotID == "" ||
		receipt.VerifiedAt == "" || !strings.HasPrefix(receipt.ReceiptHash, "sha256:") {
		t.Fatalf("%s replica receipt=%#v", operation, receipt)
	}
	if operation == "recover" {
		if receipt.ActivityRoot == nil || *receipt.ActivityRoot != activityRoot ||
			receipt.Restored == nil || !*receipt.Restored || receipt.Healthy != nil {
			t.Fatalf("recover replica receipt=%#v", receipt)
		}
		return
	}
	if receipt.ActivityRoot != nil || receipt.Healthy == nil || !*receipt.Healthy ||
		receipt.Restored != nil {
		t.Fatalf("%s replica receipt=%#v", operation, receipt)
	}
}

func assertReplicaRuntimeCapabilities(
	t *testing.T,
	baseURL, secret, workspaceID, claimID string,
) {
	t.Helper()
	client := &http.Client{Timeout: 15 * time.Second}
	response := request(
		t, client, http.MethodGet, baseURL+"/api/vibetable/v2/capabilities", "",
	)
	unauthBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read unauthenticated capabilities body: read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf(
			"unauthenticated capabilities status=%d, want 401 body=%s",
			response.StatusCode,
			unauthBody,
		)
	}
	response = request(
		t, client, http.MethodGet, baseURL+"/api/vibetable/v2/capabilities", secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("capabilities status=%d body=%s", response.StatusCode, body)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	requireExactJSONFields(t, raw, []string{
		"contractVersion", "workspaceId", "sessionEpoch", "fenceEpoch",
		"claimId", "rpcMethods", "registrations",
	})
	var capabilities workspacev2.CapabilityDocument
	decodeExactJSON(t, raw, &capabilities)
	if capabilities.ContractVersion != "2.0" || capabilities.WorkspaceID != workspaceID ||
		capabilities.SessionEpoch != 1 || capabilities.FenceEpoch != 3 ||
		capabilities.ClaimID != claimID {
		t.Fatalf("capability identity=%#v", capabilities)
	}
	expected := strings.Fields(replicaRuntimeRegistrations)
	actual := make([]string, len(capabilities.Registrations))
	methods := make([]string, len(capabilities.Registrations))
	for index, registration := range capabilities.Registrations {
		actual[index] = registration.Name + ":" + string(registration.Scope)
		methods[index] = registration.Name
	}
	if !slices.Equal(actual, expected) || !slices.Equal(capabilities.RPCMethods, methods) {
		t.Fatalf("capability registrations=%q methods=%q", actual, capabilities.RPCMethods)
	}
}

const replicaRuntimeRegistrations = `
conflict.apply:workspace conflict.inspect:workspace conflict.list:workspace conflict.preview:workspace
fileHistory.activateLeaf:workspace fileHistory.applyPendingChange:workspace fileHistory.assertEffectiveRevision:workspace
fileHistory.import:workspace fileHistory.listPendingChanges:workspace fileHistory.materializeDiffPair:workspace
fileHistory.queryDocuments:workspace fileHistory.readTree:workspace fileHistory.relink:workspace
fileHistory.restore:workspace fileHistory.unlink:workspace fileHistory.upgrade:workspace
history.applyRestore:workspace history.previewRestore:workspace history.query:workspace
replica.forceTakeover:workspace replica.status:workspace replica.synchronize:workspace
repository.applyKeyRotation:workspace repository.previewKeyRotation:workspace repository.verify:workspace
retention.apply:workspace retention.get:workspace retention.plan:workspace retention.status:workspace
retention.update:workspace snapshot.applyExtract:workspace snapshot.applyRestore:workspace
snapshot.export:workspace snapshot.import:global snapshot.inspect:workspace snapshot.inspectPackage:global
snapshot.list:workspace snapshot.previewExtract:workspace snapshot.previewRestore:workspace
snapshot.request:workspace snapshot.update:workspace workspaceDiagnostics.get:workspace
workspaceSearch.cancel:workspace workspaceSearch.query:workspace workspaceSearch.rebuild:workspace
workspaceSearch.resolveHit:workspace workspaceSearch.status:workspace`

func requireExactJSONFields(t *testing.T, raw []byte, expected []string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode JSON fields: %v", err)
	}
	actual := make([]string, 0, len(fields))
	for field := range fields {
		actual = append(actual, field)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("JSON fields=%q want=%q", actual, expected)
	}
}

func decodeExactJSON(t *testing.T, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode strict JSON: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("strict JSON has trailing content: %v", err)
	}
}

func cloneReplicaEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+3)
	for name, value := range source {
		result[name] = value
	}
	return result
}

func TestReplicaOneShotCLIHelperProcess(t *testing.T) {
	var flag string
	switch os.Getenv(replicaOneShotHelperEnv) {
	case "":
		return
	case "initialize":
		flag = "--initialize-workspace-replica"
	case "recover":
		flag = "--recover-workspace-replica"
	case "verify":
		flag = "--verify-workspace-replica"
	default:
		os.Exit(2)
	}
	os.Exit(run([]string{flag}))
}

func TestReplicaRuntimeHelperProcess(t *testing.T) {
	if os.Getenv(replicaRuntimeHelperEnv) != "1" {
		return
	}
	os.Exit(run(nil))
}
