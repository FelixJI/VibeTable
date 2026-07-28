package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/config"
)

const replicaOneShotHelperEnv = "VIBETABLE_TEST_REPLICA_ONESHOT_HELPER"

func TestReplicaOneShotFailureKeepsStdoutEmpty(t *testing.T) {
	base := map[string]string{
		replicaOneShotHelperEnv: "1",
		config.SessionSecretEnv: strings.Repeat("01", 32),
		config.DataDirEnv:       filepath.Join(t.TempDir(), "activity", ".vibetable", "data"),
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

func TestReplicaOneShotCLIHelperProcess(t *testing.T) {
	if os.Getenv(replicaOneShotHelperEnv) != "1" {
		return
	}
	os.Exit(run([]string{"--verify-workspace-replica"}))
}
