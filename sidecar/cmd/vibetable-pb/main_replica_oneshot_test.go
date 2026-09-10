package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/config"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

const replicaOneShotHelperEnv = "VIBETABLE_TEST_REPLICA_ONESHOT_HELPER"

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
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestReplicaOneShotCLIHelperProcess$",
			)
			command.Env = normalizedEnvironment(env)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
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

func TestReplicaOneShotRecoveredDataStartsOrdinarySidecar(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process integration test in short mode")
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
	bootstrapEnv := cloneReplicaEnvironment(identity)
	bootstrapEnv[helperProcessEnv] = "1"
	bootstrapEnv[config.DataDirEnv] = sourceData
	bootstrap, bootstrapStderr, bootstrapURL := startSidecarHelper(t, bootstrapEnv)
	response := request(
		t, &http.Client{Timeout: 15 * time.Second}, http.MethodPost,
		bootstrapURL+"/api/vibetable/v1/shutdown", secret,
	)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("bootstrap shutdown status=%d", response.StatusCode)
	}
	drainAndClose(t, response.Body)
	waitSidecarHelper(t, bootstrap, bootstrapStderr)

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
		filepath.Join(selected, ".vibetable", "workspace.json"),
		raw,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	oneShotEnv := cloneReplicaEnvironment(identity)
	oneShotEnv[config.DataDirEnv] = sourceData
	oneShotEnv[config.ReplicaRootEnv] = selected
	oneShotEnv[config.ActivityRootEnv] = ""
	initialized := runReplicaOneShotProcess(t, "initialize", oneShotEnv)
	verified := runReplicaOneShotProcess(t, "verify", oneShotEnv)
	if initialized.Operation != "initialize" || verified.Operation != "verify" ||
		initialized.WorkspaceID != workspaceID || verified.WorkspaceID != workspaceID {
		t.Fatalf("initialize=%#v verify=%#v", initialized, verified)
	}

	recovered := filepath.Join(container, "recovered")
	recoveryEnv := cloneReplicaEnvironment(oneShotEnv)
	recoveryEnv[config.ActivityRootEnv] = recovered
	recoveryEnv[config.DataDirEnv] = filepath.Join(recovered, ".vibetable", "data")
	receipt := runReplicaOneShotProcess(t, "recover", recoveryEnv)
	if receipt.Operation != "recover" || receipt.ActivityRoot == nil ||
		*receipt.ActivityRoot != recovered || receipt.WorkspaceID != workspaceID {
		t.Fatalf("recover=%#v", receipt)
	}

	runtimeEnv := cloneReplicaEnvironment(identity)
	runtimeEnv[helperProcessEnv] = "1"
	runtimeEnv[config.DataDirEnv] = recoveryEnv[config.DataDirEnv]
	runtimeEnv[config.ReplicaRootEnv] = selected
	command, stderr, baseURL := startSidecarHelper(t, runtimeEnv)
	client := &http.Client{Timeout: 15 * time.Second}
	response = request(
		t, client, http.MethodGet,
		baseURL+"/api/vibetable/v2/capabilities", secret,
	)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("capabilities status=%d body=%s", response.StatusCode, body)
	}
	var capabilities struct {
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
	if err := json.NewDecoder(response.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if capabilities.ContractVersion != "2.0" ||
		capabilities.WorkspaceID != workspaceID || capabilities.SessionEpoch != 1 ||
		capabilities.FenceEpoch != 3 || capabilities.ClaimID != claimID ||
		len(capabilities.RPCMethods) == 0 ||
		len(capabilities.RPCMethods) != len(capabilities.Registrations) {
		t.Fatalf("capabilities=%#v", capabilities)
	}
	for index, registration := range capabilities.Registrations {
		if registration.Method != capabilities.RPCMethods[index] ||
			(registration.Scope != "global" && registration.Scope != "workspace") {
			t.Fatalf("registration[%d]=%#v", index, registration)
		}
	}

	response = request(
		t, client, http.MethodPost,
		baseURL+"/api/vibetable/v1/shutdown", secret,
	)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status=%d", response.StatusCode)
	}
	drainAndClose(t, response.Body)
	waitSidecarHelper(t, command, stderr)
}

func runReplicaOneShotProcess(
	t *testing.T,
	operation string,
	env map[string]string,
) workspacev2.ReplicaOneShotReceipt {
	t.Helper()
	values := cloneReplicaEnvironment(env)
	values[replicaOneShotHelperEnv] = operation
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestReplicaOneShotCLIHelperProcess$",
	)
	command.Env = normalizedEnvironment(values)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s one-shot: %v stderr=%s", operation, err, &stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var receipt workspacev2.ReplicaOneShotReceipt
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode %s receipt: %v", operation, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("%s wrote more than one receipt: %v", operation, err)
	}
	return receipt
}

func cloneReplicaEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
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
