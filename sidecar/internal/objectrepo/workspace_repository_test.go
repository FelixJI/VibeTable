package objectrepo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kopiarepo "github.com/kopia/kopia/repo"
)

func TestWorkspaceRepositoryClosesWriteSessionsSynchronously(t *testing.T) {
	ctx := context.Background()
	repository, authority := workspaceRepository(t)
	adapter := repository.(*KopiaRepository)
	tracked := &faultingWriteSessionRepository{Repository: adapter.repository}
	adapter.repository = tracked
	nextAuthority := authority
	nextAuthority.FenceEpoch++
	nextAuthority.ClaimID = "next-claim"
	if err := repository.AcceptAuthority(ctx, &authority, nextAuthority); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Commit(ctx, CommitRequest{Authority: nextAuthority,
		Objects: []ObjectInput{{Name: "proof", Content: []byte("session-object")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Commit(ctx, CommitRequest{Authority: nextAuthority,
		Objects: []ObjectInput{{Name: "", Content: []byte("invalid")}}}); err == nil || err.Error() != "repository.object_name_invalid" {
		t.Fatalf("invalid commit error = %v", err)
	}
	if tracked.opened != 3 || tracked.closed != 3 {
		t.Fatalf("write sessions opened=%d closed=%d", tracked.opened, tracked.closed)
	}
}

func workspaceRepository(t *testing.T) (WorkspaceRepository, Authority) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	spec := WorkspaceRepositorySpec{
		Format: workspaceRepositoryFormat, CoordinationRoot: filepath.Join(root, "coordination"),
		ObjectsRoot: filepath.Join(root, "objects"), Password: []byte("warm-cache-password"),
	}
	authority := Authority{WorkspaceID: "warm-cache-workspace", FenceEpoch: 1, ClaimID: "warm-cache-claim"}
	repository, err := CreateWorkspaceRepository(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return repository, authority
}

func TestWorkspaceRepositoryWriteSessionErrors(t *testing.T) {
	closeFailure := errors.New("writer close failed")
	cleanupCancelled := errors.New("writer cleanup context cancelled")
	for _, operation := range []string{"authority", "commit"} {
		for _, failure := range []string{"cancelled", "close", "cancelled-and-close"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				repository, authority := workspaceRepository(t)
				nextAuthority := authority
				nextAuthority.FenceEpoch++
				nextAuthority.ClaimID = "warm-cache-next-claim"
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				faults := &faultingWriteSessionRepository{cleanupCancelled: cleanupCancelled}
				if failure != "close" {
					faults.afterNewWriter = cancel
				}
				if failure != "cancelled" {
					faults.closeErr = closeFailure
				}
				// Decorate only the third-party interface; all writers and storage are real.
				adapter := repository.(*KopiaRepository)
				faults.Repository = adapter.repository
				adapter.repository = faults
				var receipt DurableCommitReceipt
				var err error
				if operation == "authority" {
					err = repository.AcceptAuthority(ctx, &authority, nextAuthority)
				} else {
					receipt, err = repository.Commit(ctx, CommitRequest{Authority: authority,
						Objects: []ObjectInput{{Name: "proof", Content: []byte("write-session-data")}}})
				}
				if failure != "close" && !errors.Is(err, context.Canceled) {
					t.Errorf("cancellation lost: %v", err)
				}
				if failure != "cancelled" && !errors.Is(err, closeFailure) {
					t.Errorf("close error lost: %v", err)
				}
				if errors.Is(err, cleanupCancelled) {
					t.Errorf("cleanup inherited cancellation: %v", err)
				}
				if operation == "authority" && failure == "close" {
					memory := *adapter.state.Authority
					if !authorityEqual(memory, nextAuthority) {
						t.Fatalf("memory authority = %+v", memory)
					}
					if err := adapter.loadState(context.Background()); err != nil {
						t.Fatal(err)
					}
					if !authorityEqual(*adapter.state.Authority, memory) {
						t.Fatalf("persisted authority = %+v", adapter.state.Authority)
					}
				}
				if operation == "commit" && failure == "close" {
					if !receipt.Durable || receipt.Revision != 1 {
						t.Fatalf("flushed receipt changed: %+v", receipt)
					}
					reader, readErr := repository.Open(context.Background(), receipt.Objects["proof"])
					if readErr != nil {
						t.Fatal(readErr)
					}
					raw, readErr := io.ReadAll(reader)
					if err := errors.Join(readErr, reader.Close()); err != nil || string(raw) != "write-session-data" {
						t.Fatalf("flushed object changed: %q, %v", raw, err)
					}
				}
				if err := repository.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestKopiaRetentionClosesDirectWriters(t *testing.T) {
	closeFailure := errors.New("direct writer close failed")
	cleanupCancelled := errors.New("direct writer cleanup context cancelled")
	for _, failure := range []string{"none", "cancelled", "close"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newKopiaRetentionFixture(t)
			direct := fixture.repository.repository.(kopiarepo.DirectRepository)
			tracked := &trackingDirectRepository{
				DirectRepository: direct, cleanupCancelled: cleanupCancelled,
			}
			fixture.repository.repository = tracked
			t.Cleanup(func() {
				if err := fixture.repository.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancelled" {
				tracked.afterNewWriter = cancel
			}
			if failure == "close" {
				tracked.closeErr = closeFailure
			}
			_, err := fixture.repository.RetireAndMaintain(
				ctx,
				RetentionMaintenanceRequest{
					Authority: fixture.authority, ExpectedRevision: fixture.revision,
					ObjectIDs: []ObjectID{fixture.garbage},
				},
			)
			if failure == "none" && err != nil {
				t.Fatal(err)
			}
			if failure == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if failure == "close" && !errors.Is(err, closeFailure) {
				t.Fatalf("close error lost: %v", err)
			}
			if errors.Is(err, cleanupCancelled) {
				t.Fatalf("cleanup inherited cancellation: %v", err)
			}
			want := 1
			if failure == "none" {
				want = 6
			}
			if tracked.opened != want || tracked.closed != want {
				t.Fatalf("direct writers opened=%d closed=%d", tracked.opened, tracked.closed)
			}
		})
	}
}

type faultingWriteSessionRepository struct {
	kopiarepo.Repository
	afterNewWriter   func()
	closeErr         error
	cleanupCancelled error
	opened           int
	closed           int
}

func (repository *faultingWriteSessionRepository) NewWriter(ctx context.Context, options kopiarepo.WriteSessionOptions) (context.Context, kopiarepo.RepositoryWriter, error) {
	sessionCtx, writer, err := repository.Repository.NewWriter(ctx, options)
	if err != nil {
		return sessionCtx, writer, err
	}
	repository.opened++
	if repository.afterNewWriter != nil {
		repository.afterNewWriter()
	}
	return sessionCtx, &faultingWriteSession{writer, repository}, nil
}

type faultingWriteSession struct {
	kopiarepo.RepositoryWriter
	repository *faultingWriteSessionRepository
}

func (writer *faultingWriteSession) Close(ctx context.Context) error {
	writer.repository.closed++
	closeErr := writer.RepositoryWriter.Close(ctx)
	if ctx.Err() != nil {
		closeErr = errors.Join(closeErr, writer.repository.cleanupCancelled)
	}
	return errors.Join(closeErr, writer.repository.closeErr)
}

type trackingDirectRepository struct {
	kopiarepo.DirectRepository
	afterNewWriter   func()
	closeErr         error
	cleanupCancelled error
	opened           int
	closed           int
}

func (repository *trackingDirectRepository) NewDirectWriter(
	ctx context.Context,
	options kopiarepo.WriteSessionOptions,
) (context.Context, kopiarepo.DirectRepositoryWriter, error) {
	sessionCtx, writer, err := repository.DirectRepository.NewDirectWriter(ctx, options)
	if err != nil {
		return sessionCtx, writer, err
	}
	repository.opened++
	if repository.afterNewWriter != nil {
		repository.afterNewWriter()
	}
	return sessionCtx, &trackingDirectWriter{
		DirectRepositoryWriter: writer,
		repository:             repository,
	}, nil
}

type trackingDirectWriter struct {
	kopiarepo.DirectRepositoryWriter
	repository *trackingDirectRepository
}

func (writer *trackingDirectWriter) Close(ctx context.Context) error {
	writer.repository.closed++
	closeErr := writer.DirectRepositoryWriter.Close(ctx)
	if ctx.Err() != nil {
		closeErr = errors.Join(closeErr, writer.repository.cleanupCancelled)
	}
	return errors.Join(closeErr, writer.repository.closeErr)
}

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
	ctx := context.Background()
	root := t.TempDir()
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
