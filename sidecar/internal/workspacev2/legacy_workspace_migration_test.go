package workspacev2

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const formalLegacyWorkspaceID = "acb519c1-192b-49c8-8a53-bdf60de1eeaf"

func TestLegacyMigrationCopyValidatesWithoutPublishingFormat(t *testing.T) {
	root, original := unpackFormalLegacyWorkspace(t)
	options := formalLegacyOptions(root)
	for range 2 {
		manifest, err := VerifyLegacyMigrationCopy(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.FormatVersion != 2 || manifest.WorkspaceID != formalLegacyWorkspaceID {
			t.Fatalf("unexpected proposed manifest: %#v", manifest)
		}
		for _, name := range []string{".vibetable/workspace.json", ".vibetable/coordination/desktop-runtime-authority.json"} {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(raw, original[name]) {
				t.Fatalf("migration verifier published %s: %v", name, err)
			}
		}
		if err := ValidateStartupBinding(options.DataDir, options.WorkspaceID, options.SessionEpoch, options.FenceEpoch, options.ClaimID); err == nil || !strings.Contains(err.Error(), "workspace.format_unsupported") {
			t.Fatalf("ordinary startup admitted an unpublished copy: %v", err)
		}
	}
}

func TestLegacyMigrationCopyRejectsInvalidPreflightWithoutWrites(t *testing.T) {
	for _, scenario := range []string{"newer-format", "current-format", "repository-format", "missing-repository-config", "identity", "authority", "cancelled", "missing-catalog", "missing-coordinator", "missing-audit", "missing-data", "missing-runtime-state", "missing-retention", "missing-filehistory"} {
		t.Run(scenario, func(t *testing.T) {
			root, before := unpackFormalLegacyWorkspace(t)
			options := formalLegacyOptions(root)
			ctx := context.Background()
			switch scenario {
			case "newer-format", "current-format", "repository-format":
				var manifest map[string]any
				if err := json.Unmarshal(before[".vibetable/workspace.json"], &manifest); err != nil {
					t.Fatal(err)
				}
				if scenario == "repository-format" {
					manifest["repositoryFormat"] = "unsupported-engine"
				} else {
					manifest["formatVersion"] = 3
				}
				if scenario == "current-format" {
					manifest["formatVersion"] = 2
				}
				raw, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				before[".vibetable/workspace.json"] = raw
				if err := os.WriteFile(filepath.Join(root, ".vibetable", "workspace.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-repository-config":
				name := ".vibetable/coordination/kopia.repository.config"
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
					t.Fatal(err)
				}
				delete(before, name)
			case "identity":
				options.WorkspaceID = testWorkspaceID
			case "authority":
				options.ClaimID = testClaimID
			case "missing-catalog", "missing-coordinator", "missing-audit", "missing-data", "missing-runtime-state", "missing-retention", "missing-filehistory":
				name := map[string]string{
					"missing-catalog":       ".vibetable/snapshots/catalog.db",
					"missing-coordinator":   ".vibetable/coordination/write-coordinator.db",
					"missing-audit":         ".vibetable/audit/ledger.db",
					"missing-data":          ".vibetable/data/data.db",
					"missing-runtime-state": ".vibetable/coordination/workspace-v2.db",
					"missing-retention":     ".vibetable/coordination/retention.db",
					"missing-filehistory":   ".vibetable/topology/filehistory-head.db",
				}[scenario]
				for _, suffix := range []string{"", "-wal", "-shm"} {
					if err := os.Remove(filepath.Join(root, filepath.FromSlash(name+suffix))); err != nil {
						t.Fatal(err)
					}
					delete(before, name+suffix)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := VerifyLegacyMigrationCopy(ctx, options); err == nil {
				t.Fatal("invalid migration preflight succeeded")
			}
			count := 0
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				name, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				name = filepath.ToSlash(name)
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if expected, found := before[name]; !found || !bytes.Equal(raw, expected) {
					t.Errorf("rejected preflight changed %s", name)
				}
				count++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if count != len(before) {
				t.Fatalf("rejected preflight changed file set: %d -> %d", len(before), count)
			}
		})
	}
}

func formalLegacyOptions(root string) ReplicaOneShotOptions {
	return ReplicaOneShotOptions{
		DataDir:     filepath.Join(root, ".vibetable", "data"),
		WorkspaceID: formalLegacyWorkspaceID, SessionEpoch: 1, FenceEpoch: 1,
		ClaimID: "f4597380-0d9e-4e39-892c-b41c540c0bf5",
	}
}

func unpackFormalLegacyWorkspace(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	archive, err := zip.OpenReader("../../../contracts/v2/fixtures/formal-v0.5.0/workspace.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	before := make(map[string][]byte, len(archive.File))
	for _, entry := range archive.File {
		if !filepath.IsLocal(entry.Name) || !entry.Mode().IsRegular() {
			t.Fatalf("unexpected corpus entry: %s", entry.Name)
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read corpus: %v / %v", readErr, closeErr)
		}
		target := filepath.Join(root, filepath.FromSlash(entry.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		before[entry.Name] = raw
	}
	return root, before
}

func TestReplicaOneShotRevalidatesBindingAfterMigrationPreflight(t *testing.T) {
	for _, scenario := range []string{"workspaceId", "formatVersion"} {
		t.Run(scenario, func(t *testing.T) {
			root := createWorkspace(t, testWorkspaceID)
			options := ReplicaOneShotOptions{
				DataDir:     filepath.Join(root, ".vibetable", "data"),
				WorkspaceID: testWorkspaceID, ClaimID: testClaimID,
				SessionEpoch: 7, FenceEpoch: 3,
			}
			_, closeRuntime, err := openReplicaOneShotRuntimeWithMigrationLoader(context.Background(), options, func() error {
				path := filepath.Join(root, ".vibetable", "workspace.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var manifest map[string]any
				if err := json.Unmarshal(raw, &manifest); err != nil {
					return err
				}
				if scenario == "workspaceId" {
					manifest[scenario] = formalLegacyWorkspaceID
				} else {
					manifest[scenario] = 1
				}
				raw, err = json.Marshal(manifest)
				if err != nil {
					return err
				}
				return os.WriteFile(path, raw, 0o600)
			})
			if closeRuntime != nil {
				defer func() { _ = closeRuntime() }()
			}
			want := "workspace.identity_mismatch"
			if scenario == "formatVersion" {
				want = "workspace.format_unsupported"
			}
			if err == nil || !strings.Contains(err.Error(), want) || closeRuntime != nil {
				t.Fatalf("changed binding admitted: close=%t err=%v", closeRuntime != nil, err)
			}
		})
	}
}
