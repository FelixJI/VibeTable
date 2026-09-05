package objectrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBackupProofUsesSaltedArgon2idAndDetectsChanges(t *testing.T) {
	raw := []byte("password-format-backup")
	proof, err := workspaceBackupProof(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(proof, "argon2id$") {
		t.Fatalf("proof format = %q", proof)
	}
	if !verifyWorkspaceBackupProof(raw, proof) {
		t.Fatal("proof did not verify its source")
	}
	if verifyWorkspaceBackupProof([]byte("changed"), proof) {
		t.Fatal("proof accepted changed backup content")
	}
}

func TestWorkspaceRepositoryFactoryCreatesOnceAndReopens(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(fmt.Sprintf("publish=%v", publish), func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			root := filepath.Join(parent, "staging")
			spec := WorkspaceRepositorySpec{
				Format:           workspaceRepositoryFormat,
				CoordinationRoot: filepath.Join(root, "coordination"),
				ObjectsRoot:      filepath.Join(root, "objects"),
				Password:         []byte("workspace-factory-password"),
			}
			if _, err := OpenWorkspaceRepository(ctx, spec); !errors.Is(
				err,
				ErrRepositoryNotInitialized,
			) {
				t.Fatalf("missing open error = %v", err)
			}
			repository, created, err := OpenOrCreateWorkspaceRepository(ctx, spec)
			if err != nil || !created {
				t.Fatalf("create = %v created=%v", err, created)
			}
			authority := Authority{
				WorkspaceID: "workspace-factory",
				FenceEpoch:  1,
				ClaimID:     "claim-factory",
			}
			if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
				t.Fatal(err)
			}
			receipt, err := repository.Commit(ctx, CommitRequest{
				Authority: authority,
				Objects: []ObjectInput{{
					Name: "proof", Content: []byte("factory-data"),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if publish {
				finalRoot := filepath.Join(parent, "published")
				if err := os.Rename(root, finalRoot); err != nil {
					t.Fatal(err)
				}
				spec.CoordinationRoot = filepath.Join(finalRoot, "coordination")
				spec.ObjectsRoot = filepath.Join(finalRoot, "objects")

				configFile, _, err := workspaceRepositoryPaths(spec)
				if err != nil {
					t.Fatal(err)
				}
				original, err := os.ReadFile(configFile)
				if err != nil {
					t.Fatal(err)
				}
				for _, invalid := range []WorkspaceRepositorySpec{
					{Format: spec.Format, CoordinationRoot: spec.CoordinationRoot, ObjectsRoot: spec.ObjectsRoot, Password: []byte("wrong-password")},
					{Format: spec.Format, CoordinationRoot: spec.CoordinationRoot, ObjectsRoot: filepath.Join(parent, "missing"), Password: spec.Password},
				} {
					unexpected, err := OpenWorkspaceRepository(ctx, invalid)
					if err == nil {
						_ = unexpected.Close(ctx)
						t.Fatal("invalid relocated repository opened")
					}
					after, readErr := os.ReadFile(configFile)
					if readErr != nil || !bytes.Equal(after, original) {
						t.Fatalf("failed open changed config: %v", readErr)
					}
				}
				if _, err := os.Stat(filepath.Join(parent, "missing")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing object store was created: %v", err)
				}
			}
			reopened, created, err := OpenOrCreateWorkspaceRepository(ctx, spec)
			if err != nil || created {
				t.Fatalf("reopen = %v created=%v", err, created)
			}
			reader, err := reopened.Open(ctx, receipt.Objects["proof"])
			if err != nil {
				t.Fatal(err)
			}
			raw, readErr := io.ReadAll(reader)
			closeReaderErr := reader.Close()
			closeRepositoryErr := reopened.Close(ctx)
			if err := errors.Join(
				readErr,
				closeReaderErr,
				closeRepositoryErr,
			); err != nil {
				t.Fatal(err)
			}
			if string(raw) != "factory-data" {
				t.Fatalf("reopened data = %q", raw)
			}
			existing, err := OpenWorkspaceRepository(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			if err := existing.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceRepositoryCreateFailureCleansOnlyNewArtifacts(
	t *testing.T,
) {
	ctx := context.Background()
	root := t.TempDir()
	spec := WorkspaceRepositorySpec{
		Format:           workspaceRepositoryFormat,
		CoordinationRoot: filepath.Join(root, "coordination"),
		ObjectsRoot:      filepath.Join(root, "objects"),
		Password:         []byte("partial-create-password"),
	}
	unrelated := filepath.Join(spec.ObjectsRoot, "keep.txt")
	if err := os.MkdirAll(spec.ObjectsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-empty engine storage target is pre-existing state and must fail
	// before invoking the creator or deleting anything.
	storageRoot := filepath.Join(spec.ObjectsRoot, "kopia")
	if err := os.MkdirAll(storageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	preexisting := filepath.Join(storageRoot, "existing")
	if err := os.WriteFile(preexisting, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := createNewWorkspaceRepository(
		ctx,
		spec,
		"repository.already_initialized",
		func(
			context.Context,
			string,
			string,
			string,
		) (WorkspaceRepository, error) {
			called = true
			return nil, errors.New("must not run")
		},
	)
	if err == nil || called {
		t.Fatalf("pre-existing create = %v called=%v", err, called)
	}
	if raw, readErr := os.ReadFile(preexisting); readErr != nil ||
		string(raw) != "existing" {
		t.Fatalf("pre-existing artifact changed: %q %v", raw, readErr)
	}
	if err := os.RemoveAll(storageRoot); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("partial create failed")
	_, err = createNewWorkspaceRepository(
		ctx,
		spec,
		"repository.already_initialized",
		func(
			_ context.Context,
			storageRoot string,
			configFile string,
			_ string,
		) (WorkspaceRepository, error) {
			if err := os.MkdirAll(
				filepath.Dir(configFile),
				0o700,
			); err != nil {
				return nil, err
			}
			if err := os.WriteFile(
				configFile,
				[]byte("partial"),
				0o600,
			); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(storageRoot, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(
				filepath.Join(storageRoot, "partial"),
				[]byte("partial"),
				0o600,
			); err != nil {
				return nil, err
			}
			return nil, injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("partial create error = %v", err)
	}
	configFile, storageRoot, pathErr := workspaceRepositoryPaths(spec)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(configFile); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("partial config remains: %v", statErr)
	}
	if _, statErr := os.Stat(storageRoot); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("partial storage remains: %v", statErr)
	}
	if raw, readErr := os.ReadFile(unrelated); readErr != nil ||
		string(raw) != "keep" {
		t.Fatalf("unrelated artifact changed: %q %v", raw, readErr)
	}
}

func TestWorkspaceRepositoryRotationRestoresOpaqueBackup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	oldPassword := []byte("rotation-old-password")
	newPassword := []byte("rotation-new-password")
	spec := WorkspaceRepositorySpec{
		Format:           workspaceRepositoryFormat,
		CoordinationRoot: filepath.Join(root, "coordination"),
		ObjectsRoot:      filepath.Join(root, "objects"),
		Password:         oldPassword,
	}
	repository, err := CreateWorkspaceRepository(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	authority := Authority{
		WorkspaceID: "workspace-rotation",
		FenceEpoch:  1,
		ClaimID:     "claim-rotation",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Commit(ctx, CommitRequest{
		Authority: authority,
		Objects: []ObjectInput{{
			Name: "proof", Content: []byte("rotation-data"),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(ctx); err != nil {
		t.Fatal(err)
	}
	spec.Password = nil
	rotation, err := NewWorkspaceRepositoryRotation(spec)
	if err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Join(root, "backup")
	proof, err := rotation.Backup(ctx, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotation.ChangePassword(
		ctx,
		oldPassword,
		newPassword,
	); err != nil {
		t.Fatal(err)
	}
	if err := rotation.VerifyPassword(ctx, newPassword); err != nil {
		t.Fatalf("new password does not verify: %v", err)
	}
	if err := rotation.Restore(ctx, backupRoot, proof); err != nil {
		t.Fatal(err)
	}
	if err := rotation.VerifyPassword(ctx, oldPassword); err != nil {
		t.Fatalf("restored password does not verify: %v", err)
	}
}
