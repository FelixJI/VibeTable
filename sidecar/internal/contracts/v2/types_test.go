package v2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGoldenFixturesDecodeStrictly(t *testing.T) {
	tests := map[string]func([]byte) error{
		"workspace-manifest.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceManifest](raw)
			return err
		},
		"workspace-registry-entry.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceRegistryEntry](raw)
			return err
		},
		"workspace-session.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceSession](raw)
			return err
		},
		"file-document.json": func(raw []byte) error {
			_, err := DecodeStrict[FileDocument](raw)
			return err
		},
		"file-revision.json": func(raw []byte) error {
			_, err := DecodeStrict[FileRevision](raw)
			return err
		},
		"snapshot-manifest.json": func(raw []byte) error {
			_, err := DecodeStrict[SnapshotManifest](raw)
			return err
		},
		"snapshot-seal.json": func(raw []byte) error {
			_, err := DecodeStrict[SnapshotSeal](raw)
			return err
		},
		"snapshot-catalog-entry.json": func(raw []byte) error {
			_, err := DecodeStrict[SnapshotCatalogEntry](raw)
			return err
		},
		"lease-claim.json": func(raw []byte) error {
			_, err := DecodeStrict[LeaseClaim](raw)
			return err
		},
		"retention-policy.json": func(raw []byte) error {
			_, err := DecodeStrict[RetentionPolicy](raw)
			return err
		},
		"workspace-event.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceEvent](raw)
			return err
		},
		"rpc-catalog.json": func(raw []byte) error {
			_, err := DecodeStrict[RPCContractCatalog](raw)
			return err
		},
	}
	for name, decode := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixturesDir(t), name))
			if err != nil {
				t.Fatal(err)
			}
			if err := decode(raw); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
		})
	}
}

func TestSharedNegativeFixtureCorpusFailsClosed(t *testing.T) {
	type negativeCase struct {
		Name      string   `json:"name"`
		Fixture   string   `json:"fixture"`
		Operation string   `json:"operation"`
		Path      []string `json:"path"`
		Value     any      `json:"value"`
	}
	var corpus struct {
		SchemaVersion int            `json:"schemaVersion"`
		Cases         []negativeCase `json:"cases"`
	}
	raw, err := os.ReadFile(filepath.Join(fixturesDir(t), "..", "negative-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &corpus); err != nil || corpus.SchemaVersion != 1 {
		t.Fatalf("invalid negative corpus: %v", err)
	}
	decoders := map[string]func([]byte) error{
		"workspace-manifest.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceManifest](raw)
			return err
		},
		"workspace-event.json": func(raw []byte) error {
			_, err := DecodeStrict[WorkspaceEvent](raw)
			return err
		},
	}
	for _, testCase := range corpus.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(fixturesDir(t), testCase.Fixture))
			if err != nil {
				t.Fatal(err)
			}
			mutated := source
			if testCase.Operation == "appendRaw" {
				mutated = append(append([]byte(nil), source...), []byte(testCase.Value.(string))...)
			} else {
				var document map[string]any
				if err := json.Unmarshal(source, &document); err != nil {
					t.Fatal(err)
				}
				target := document
				for _, segment := range testCase.Path[:len(testCase.Path)-1] {
					target = target[segment].(map[string]any)
				}
				key := testCase.Path[len(testCase.Path)-1]
				if testCase.Operation == "remove" {
					delete(target, key)
				} else {
					target[key] = testCase.Value
				}
				mutated, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
			}
			decode := decoders[testCase.Fixture]
			if decode == nil {
				t.Fatalf("negative fixture has no decoder: %s", testCase.Fixture)
			}
			if err := decode(mutated); err == nil {
				t.Fatal("negative fixture was accepted")
			}
		})
	}
}

func TestDecodeStrictRejectsUnknownMissingInvalidAndTrailing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixturesDir(t), "workspace-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(
		string(raw),
		`"contractVersion": "2.0",`,
		`"contractVersion": "2.0", "unknown": true,`,
		1,
	)
	if _, err := DecodeStrict[WorkspaceManifest]([]byte(unknown)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	missing := strings.Replace(string(raw), `"formatVersion": 1,`, "", 1)
	if _, err := DecodeStrict[WorkspaceManifest]([]byte(missing)); err == nil {
		t.Fatal("missing formatVersion was accepted")
	}
	invalid := strings.Replace(string(raw), `"storageMode": "direct"`, `"storageMode": "remote"`, 1)
	if _, err := DecodeStrict[WorkspaceManifest]([]byte(invalid)); err == nil {
		t.Fatal("invalid enum was accepted")
	}
	if _, err := DecodeStrict[WorkspaceManifest](append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestGlobalWireScopeIsStrict(t *testing.T) {
	raw := []byte(`{"scope":"global","operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":0}`)
	if _, err := DecodeStrict[GlobalWireScope](raw); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[GlobalWireScope]([]byte(`{"scope":"global","operationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","sequence":0,"workspaceId":"11111111-1111-4111-8111-111111111111"}`)); err == nil {
		t.Fatal("global scope accepted workspace-only field")
	}
}

func TestWorkspaceScopeRejectsOldEpochAndSequence(t *testing.T) {
	scope := WorkspaceWireScope{
		Scope:        "workspace",
		WorkspaceID:  "11111111-1111-4111-8111-111111111111",
		SessionEpoch: 7,
		OperationID:  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Sequence:     12,
	}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := scope.EnsureCurrent(scope.WorkspaceID, 7, 12); err != nil {
		t.Fatal(err)
	}
	if err := scope.EnsureCurrent(scope.WorkspaceID, 8, 0); err == nil ||
		err.Error() != "workspace.session_epoch_stale" {
		t.Fatalf("old epoch error = %v", err)
	}
	if err := scope.EnsureCurrent(scope.WorkspaceID, 7, 13); err == nil ||
		err.Error() != "workspace.sequence_stale" {
		t.Fatalf("old sequence error = %v", err)
	}
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(
		filepath.Dir(file), "..", "..", "..", "..",
		"contracts", "v2", "fixtures",
	))
}
