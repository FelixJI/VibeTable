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

func TestFileRevisionAcceptsProvisionalIdentityWithoutCanonicalNumbers(t *testing.T) {
	raw, err := os.ReadFile(
		filepath.Join(fixturesDir(t), "file-revision.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var revision map[string]any
	if err := json.Unmarshal(raw, &revision); err != nil {
		t.Fatal(err)
	}
	revision["revisionOrdinal"] = float64(0)
	revision["localSequence"] = float64(7)
	revision["formalVersion"] = nil
	revision["kind"] = "autosave"
	revision["restoredFromRevisionId"] = nil
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeStrict[FileRevision](encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RevisionOrdinal != 0 ||
		parsed.LocalSequence == nil ||
		*parsed.LocalSequence != 7 {
		t.Fatalf("provisional revision = %#v", parsed)
	}

	revision["localSequence"] = nil
	encoded, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[FileRevision](encoded); err == nil {
		t.Fatal("provisional revision without localSequence was accepted")
	}

	revision["localSequence"] = float64(0)
	encoded, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[FileRevision](encoded); err == nil {
		t.Fatal("zero localSequence was accepted")
	}

	revision["localSequence"] = float64(7)
	revision["formalVersion"] = float64(3)
	encoded, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[FileRevision](encoded); err == nil {
		t.Fatal("provisional formalVersion was accepted")
	}

	revision["revisionOrdinal"] = float64(4)
	revision["localSequence"] = nil
	revision["formalVersion"] = nil
	revision["kind"] = "formal"
	encoded, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[FileRevision](encoded); err == nil {
		t.Fatal("canonical formal revision without Vn was accepted")
	}
}

func TestRPCCatalogRejectsUnclosedArrayItemSchemas(t *testing.T) {
	for _, mutation := range []string{
		"untyped-items",
		"open-item",
		"missing-required",
	} {
		t.Run(mutation, func(t *testing.T) {
			raw, err := os.ReadFile(
				filepath.Join(fixturesDir(t), "rpc-catalog.json"),
			)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			var target map[string]any
			for _, rawCase := range document["rpcCases"].([]any) {
				candidate := rawCase.(map[string]any)
				if candidate["method"] == "conflict.inspect" {
					target = candidate
					break
				}
			}
			if target == nil {
				t.Fatal("conflict.inspect fixture missing")
			}
			schema := target["resultSchema"].(map[string]any)
			properties := schema["properties"].(map[string]any)
			array := properties["items"].(map[string]any)
			item := array["items"].(map[string]any)
			switch mutation {
			case "untyped-items":
				array["items"] = map[string]any{}
			case "open-item":
				item["additionalProperties"] = true
			case "missing-required":
				required := item["required"].([]any)
				item["required"] = required[1:]
			}
			mutated, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeStrict[RPCContractCatalog](mutated); err == nil {
				t.Fatal("unclosed array item schema was accepted")
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
		"file-revision.json": func(raw []byte) error {
			_, err := DecodeStrict[FileRevision](raw)
			return err
		},
		"rpc-catalog.json": func(raw []byte) error {
			_, err := DecodeStrict[RPCContractCatalog](raw)
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
