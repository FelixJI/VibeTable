package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/config"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
)

func TestLegacyMigrationCLIUsesOfflineCopyAndClosesBeforeReceipt(t *testing.T) {
	for _, scenario := range []string{"success", "newer-format", "missing-catalog"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source, staging := filepath.Join(root, "original"), filepath.Join(root, "staging")
			original := extractLegacyCLIFixture(t, source, staging)
			manifestPath := filepath.Join(staging, ".vibetable", "workspace.json")
			if scenario == "newer-format" {
				var fields map[string]any
				if err := json.Unmarshal(original[".vibetable/workspace.json"], &fields); err != nil {
					t.Fatal(err)
				}
				fields["formatVersion"] = 3
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing-catalog" {
				for _, suffix := range []string{"", "-wal", "-shm"} {
					if err := os.Remove(filepath.Join(staging, ".vibetable", "snapshots", "catalog.db"+suffix)); err != nil {
						t.Fatal(err)
					}
				}
			}
			beforeManifest, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				ctx, cancel := context.WithTimeout(context.Background(), replicaProcessTimeout)
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplicaOneShotCLIHelperProcess$")
				command.Env = normalizedEnvironment(map[string]string{
					replicaOneShotHelperEnv: "migration",
					config.SessionSecretEnv: strings.Repeat("01", 32),
					config.DataDirEnv:       filepath.Join(staging, ".vibetable", "data"),
					config.WorkspaceIDEnv:   "acb519c1-192b-49c8-8a53-bdf60de1eeaf",
					config.SessionEpochEnv:  "1", config.FenceEpochEnv: "1",
					config.ClaimIDEnv:     "f4597380-0d9e-4e39-892c-b41c540c0bf5",
					config.ReplicaRootEnv: "", config.ActivityRootEnv: "",
				})
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				deadlineErr := ctx.Err()
				cancel()
				if deadlineErr != nil {
					t.Fatalf("migration timed out: %v; stderr=%s", deadlineErr, &stderr)
				}
				if scenario == "success" {
					if err != nil {
						t.Fatalf("migration process: %v; stderr=%s", err, &stderr)
					}
					var manifest contractsv2.WorkspaceManifest
					decodeExactJSON(t, stdout.Bytes(), &manifest)
					if err := manifest.Validate(); err != nil {
						t.Fatal(err)
					}
					if manifest.WorkspaceID != "acb519c1-192b-49c8-8a53-bdf60de1eeaf" {
						t.Fatalf("receipt changed workspace: %#v", manifest)
					}
				} else {
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 1 || stdout.Len() != 0 {
						t.Fatalf("failed migration: exit=%v stdout=%q stderr=%s", err, &stdout, &stderr)
					}
				}
				actual, err := os.ReadFile(manifestPath)
				if err != nil || !bytes.Equal(actual, beforeManifest) {
					t.Fatalf("CLI published manifest: %v", err)
				}
				// Windows rejects this while a process retains non-share-delete file handles.
				closed := staging + "-closed"
				if err := os.Rename(staging, closed); err != nil {
					t.Fatalf("migration retained staging handles: %v", err)
				}
				if err := os.Rename(closed, staging); err != nil {
					t.Fatal(err)
				}
			}
			count := 0
			if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				name, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				expected, found := original[filepath.ToSlash(name)]
				if !found || !bytes.Equal(raw, expected) {
					t.Errorf("CLI changed original %s", name)
				}
				count++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if count != len(original) {
				t.Fatalf("CLI changed original file set: %d -> %d", len(original), count)
			}
		})
	}
}

func extractLegacyCLIFixture(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	archive, err := zip.OpenReader("../../../contracts/v2/fixtures/formal-v0.5.0/workspace.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	original := make(map[string][]byte, len(archive.File))
	for _, entry := range archive.File {
		if !filepath.IsLocal(entry.Name) || !entry.Mode().IsRegular() {
			t.Fatalf("invalid fixture entry %s", entry.Name)
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("fixture read: %v / %v", readErr, closeErr)
		}
		original[entry.Name] = raw
		for _, root := range roots {
			path := filepath.Join(root, filepath.FromSlash(entry.Name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return original
}
