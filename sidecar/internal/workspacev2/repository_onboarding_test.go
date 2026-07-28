package workspacev2

import (
	"context"
	"path/filepath"
	"testing"

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
	configPath := filepath.Join(
		root,
		".vibetable",
		"coordination",
		"kopia.repository.config",
	)
	repository, err := objectrepo.OpenKopia(
		context.Background(),
		configPath,
		objectrepo.ConvenientPassword,
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
}
