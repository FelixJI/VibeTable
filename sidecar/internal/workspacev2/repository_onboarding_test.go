package workspacev2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

func TestInitializeRepositoryIsOneShotAndVerified(t *testing.T) {
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	result, err := InitializeRepository(
		context.Background(),
		dataDir,
		testWorkspaceID,
		3,
		testClaimID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != testWorkspaceID ||
		result.EncryptionMode != objectrepo.EncryptionConvenient ||
		len(result.RecoveryKey) != 0 {
		t.Fatalf("initialization result = %#v", result)
	}
	paths, manifest, err := validateBinding(
		dataDir,
		testWorkspaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := objectrepo.OpenWorkspaceRepository(
		context.Background(),
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			[]byte(objectrepo.ConvenientPassword),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	report, verifyErr := repository.Verify(context.Background(), nil)
	closeErr := repository.Close(context.Background())
	if verifyErr != nil || closeErr != nil || !report.Valid {
		t.Fatalf(
			"initialized repository did not reopen and verify: %#v %v %v",
			report,
			verifyErr,
			closeErr,
		)
	}
	if _, err := InitializeRepository(
		context.Background(),
		dataDir,
		testWorkspaceID,
		3,
		testClaimID,
	); err == nil || err.Error() != "repository.already_initialized" {
		t.Fatalf("second initialization was not rejected: %v", err)
	}
	stillPresent, err := objectrepo.OpenWorkspaceRepository(
		context.Background(),
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			[]byte(objectrepo.ConvenientPassword),
		),
	)
	if err != nil {
		t.Fatalf("second initialization removed existing repository: %v", err)
	}
	if err := stillPresent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeRepositoryWaitsForExclusiveRepositorySession(
	t *testing.T,
) {
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	paths, manifest, err := validateBinding(dataDir, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	releaseRepository, err := objectrepo.AcquireWorkspaceRepositorySession(
		context.Background(),
		paths.coordination,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := objectrepo.CreateWorkspaceRepository(
		context.Background(),
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			[]byte(objectrepo.ConvenientPassword),
		),
	)
	if err != nil {
		_ = releaseRepository()
		t.Fatal(err)
	}
	if err := repository.Close(context.Background()); err != nil {
		_ = releaseRepository()
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(
		context.Background(),
		100*time.Millisecond,
	)
	defer cancel()
	_, initializeErr := InitializeRepository(
		waitCtx,
		dataDir,
		testWorkspaceID,
		3,
		testClaimID,
	)
	if !errors.Is(initializeErr, context.DeadlineExceeded) {
		_ = releaseRepository()
		t.Fatalf("session contention error = %v", initializeErr)
	}
	if err := releaseRepository(); err != nil {
		t.Fatal(err)
	}

	stillPresent, err := objectrepo.OpenWorkspaceRepository(
		context.Background(),
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			[]byte(objectrepo.ConvenientPassword),
		),
	)
	if err != nil {
		t.Fatalf("contended initialization changed repository: %v", err)
	}
	if err := stillPresent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeRepositoryFailureCleansOnlyItsNewState(
	t *testing.T,
) {
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	paths, manifest, err := validateBinding(dataDir, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = InitializeRepository(
		context.Background(),
		dataDir,
		testWorkspaceID,
		0,
		testClaimID,
	)
	if err == nil {
		t.Fatal("invalid authority initialization unexpectedly succeeded")
	}
	_, openErr := objectrepo.OpenWorkspaceRepository(
		context.Background(),
		workspaceRepositorySpec(
			paths,
			manifest.RepositoryFormat,
			[]byte(objectrepo.ConvenientPassword),
		),
	)
	if !errors.Is(openErr, objectrepo.ErrRepositoryNotInitialized) {
		t.Fatalf("failed initialization left repository state: %v", openErr)
	}
	result, err := InitializeRepository(
		context.Background(),
		dataDir,
		testWorkspaceID,
		3,
		testClaimID,
	)
	if err != nil {
		t.Fatalf("clean retry failed: %v", err)
	}
	if result.WorkspaceID != testWorkspaceID {
		t.Fatalf("retry result = %#v", result)
	}
}

func TestInitializeRepositoryPreservesPreexistingEngineState(
	t *testing.T,
) {
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	paths, _, err := validateBinding(dataDir, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	preexisting := filepath.Join(paths.objects, "kopia", "preexisting")
	if err := os.MkdirAll(filepath.Dir(preexisting), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preexisting, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InitializeRepository(
		context.Background(),
		dataDir,
		testWorkspaceID,
		3,
		testClaimID,
	)
	if err == nil || err.Error() != "repository.already_initialized" {
		t.Fatalf("preexisting engine state error = %v", err)
	}
	raw, readErr := os.ReadFile(preexisting)
	if readErr != nil || string(raw) != "keep" {
		t.Fatalf("preexisting engine state changed: %q %v", raw, readErr)
	}
}

func TestRestoreProtectedRepositoryWaitsForExclusiveRepositorySession(
	t *testing.T,
) {
	root := createProtectedWorkspace(t)
	dataDir := filepath.Join(root, ".vibetable", "data")
	paths, _, err := validateBinding(dataDir, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	releaseRepository, err := objectrepo.AcquireWorkspaceRepositorySession(
		context.Background(),
		paths.coordination,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(
		context.Background(),
		100*time.Millisecond,
	)
	defer cancel()
	restoreErr := RestoreProtectedRepository(
		waitCtx,
		dataDir,
		testWorkspaceID,
		make([]byte, 32),
	)
	if !errors.Is(restoreErr, context.DeadlineExceeded) {
		_ = releaseRepository()
		t.Fatalf("session contention error = %v", restoreErr)
	}
	if err := releaseRepository(); err != nil {
		t.Fatal(err)
	}
}
