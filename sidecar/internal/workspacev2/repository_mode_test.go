package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestRepositoryModeSwitchResumesAndPreservesObjects(t *testing.T) {
	for _, source := range []objectrepo.EncryptionMode{objectrepo.EncryptionConvenient, objectrepo.EncryptionProtected} {
		for _, storageMode := range []string{"direct", "mirrored"} {
			for _, phase := range []string{"afterFormatChangeBeforeJournal", "manifestChanged", "replicaManifestChanged", "credentialDeleted"} {
				if phase == "replicaManifestChanged" && storageMode != "mirrored" || phase == "credentialDeleted" && source != objectrepo.EncryptionProtected {
					continue
				}
				t.Run(string(source)+"/"+storageMode+"/"+phase, func(t *testing.T) {
					ctx := context.Background()
					var root string
					if source == objectrepo.EncryptionProtected {
						root = createProtectedWorkspace(t)
					} else {
						root = createWorkspace(t, testWorkspaceID)
					}
					dataDir := filepath.Join(root, ".vibetable", "data")
					paths, manifest, err := validateBinding(dataDir, testWorkspaceID)
					if err != nil {
						t.Fatal(err)
					}
					var replicaRoot string
					if storageMode == "mirrored" {
						manifest.StorageMode = "mirrored"
						raw, err := json.Marshal(manifest)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(paths.manifest, raw, 0o600); err != nil {
							t.Fatal(err)
						}
						replicaRoot = t.TempDir()
						if err := os.Mkdir(filepath.Join(replicaRoot, ".vibetable"), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(replicaRoot, ".vibetable", "workspace.json"), raw, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					vault := rotationTestVault{}
					provider := objectrepo.NewKeyProvider(vault)
					if source == objectrepo.EncryptionProtected {
						if _, err := provider.CreateProtected(ctx, testWorkspaceID); err != nil {
							t.Fatal(err)
						}
					}
					current, err := provider.Open(ctx, testWorkspaceID, source)
					if err != nil {
						t.Fatal(err)
					}
					defer clearBytes(current.Password)
					spec := workspaceRepositorySpec(paths, manifest.RepositoryFormat, current.Password)
					repository, err := objectrepo.CreateWorkspaceRepository(ctx, spec)
					if err != nil {
						t.Fatal(err)
					}
					authority := objectrepo.Authority{WorkspaceID: testWorkspaceID, FenceEpoch: 3, ClaimID: testClaimID}
					if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
						t.Fatal(err)
					}
					receipt, err := repository.Commit(ctx, objectrepo.CommitRequest{
						Authority: authority,
						Objects:   []objectrepo.ObjectInput{{Name: "data", Content: []byte("retained")}},
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := repository.Close(ctx); err != nil {
						t.Fatal(err)
					}
					target := objectrepo.EncryptionProtected
					if source == objectrepo.EncryptionProtected {
						target = objectrepo.EncryptionConvenient
					}
					interrupted := errors.New("interrupted")
					_, err = rotateRepositoryMode(ctx, dataDir, testWorkspaceID, provider, target, replicaRoot, func(at string) error {
						if at == phase {
							return interrupted
						}
						return nil
					})
					if !errors.Is(err, interrupted) {
						t.Fatalf("fault = %v", err)
					}
					// A conflicting retry must not replace the durable target.
					if _, err := rotateRepositoryMode(ctx, dataDir, testWorkspaceID, provider, source, replicaRoot, nil); err == nil {
						t.Fatal("conflicting target accepted")
					}
					if phase == "replicaManifestChanged" {
						path := filepath.Join(replicaRoot, ".vibetable", "workspace.json")
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if _, err := rotateRepositoryMode(ctx, dataDir, testWorkspaceID, provider, target, replicaRoot, nil); err == nil {
							t.Fatal("offline replica was ignored during resume")
						}
						if _, found, err := readKeyRotationJournal(paths); err != nil || !found {
							t.Fatalf("offline failure discarded recovery journal: %v", err)
						}
						if err := os.WriteFile(path, raw, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					rotated, err := rotateRepositoryMode(ctx, dataDir, testWorkspaceID, provider, target, replicaRoot, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer clearBytes(rotated.RecoveryKey)
					_, changedManifest, err := validateBinding(dataDir, testWorkspaceID)
					if err != nil || changedManifest.EncryptionMode != string(target) {
						t.Fatalf("manifest mode = %s: %v", changedManifest.EncryptionMode, err)
					}
					if replicaRoot != "" {
						selected, err := readReplicaManifest(replicaRoot, testWorkspaceID)
						if err != nil || selected.EncryptionMode != string(target) {
							t.Fatalf("replica manifest mode = %s: %v", selected.EncryptionMode, err)
						}
					}
					rotation, err := objectrepo.NewWorkspaceRepositoryRotation(spec)
					if err != nil {
						t.Fatal(err)
					}
					if err := rotation.VerifyPassword(ctx, current.Password); err == nil {
						t.Fatal("old password still accepted")
					}
					if target == objectrepo.EncryptionProtected {
						if len(rotated.RecoveryKey) != 32 {
							t.Fatal("missing new recovery key")
						}
						if err := provider.DeleteProtected(ctx, testWorkspaceID); err != nil {
							t.Fatal(err)
						}
						if _, err := provider.RestoreProtected(ctx, testWorkspaceID, rotated.RecoveryKey, rotation.VerifyPassword); err != nil {
							t.Fatalf("new key cannot recover lost vault: %v", err)
						}
					} else {
						if len(rotated.RecoveryKey) != 0 {
							t.Fatal("convenient mode returned a recovery key")
						}
						if _, err := provider.Open(ctx, testWorkspaceID, objectrepo.EncryptionProtected); !errors.Is(err, objectrepo.ErrKeyMissing) {
							t.Fatalf("old vault credential retained: %v", err)
						}
					}
					next, err := provider.Open(ctx, testWorkspaceID, target)
					if err != nil {
						t.Fatal(err)
					}
					defer clearBytes(next.Password)
					spec.Password = next.Password
					reopened, err := objectrepo.OpenWorkspaceRepository(ctx, spec)
					if err != nil {
						t.Fatal(err)
					}
					defer reopened.Close(ctx)
					stream, err := reopened.Open(ctx, receipt.Objects["data"])
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close()
					var data bytes.Buffer
					if _, err := data.ReadFrom(stream); err != nil || data.String() != "retained" {
						t.Fatalf("object lost: %v", err)
					}
				})
			}
		}
	}
}
