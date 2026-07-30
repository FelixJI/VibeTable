package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type rotationTestVault map[string][]byte

func (vault rotationTestVault) Read(
	_ context.Context,
	name string,
) ([]byte, error) {
	value, exists := vault[name]
	if !exists {
		return nil, objectrepo.ErrKeyMissing
	}
	return append([]byte(nil), value...), nil
}

func (vault rotationTestVault) Write(
	_ context.Context,
	name string,
	value []byte,
) error {
	vault[name] = append([]byte(nil), value...)
	return nil
}

func (vault rotationTestVault) Delete(
	_ context.Context,
	name string,
) error {
	delete(vault, name)
	return nil
}

func TestProtectedKeyRotationRecoversKillAfterKopiaChangeBeforeJournal(
	t *testing.T,
) {
	ctx := context.Background()
	root := createProtectedWorkspace(t)
	dataDir := filepath.Join(root, ".vibetable", "data")
	paths, manifest, err := validateBinding(dataDir, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	repositorySpec := workspaceRepositorySpec(
		paths,
		manifest.RepositoryFormat,
		nil,
	)
	oldKey := []byte("old-protected-repository-key-0001")
	vault := rotationTestVault{}
	provider := objectrepo.NewKeyProvider(vault)
	if err := vault.Write(
		ctx,
		"VibeTable/workspace/"+testWorkspaceID+"/repository",
		oldKey,
	); err != nil {
		t.Fatal(err)
	}
	createSpec := repositorySpec
	createSpec.Password = oldKey
	repository, err := objectrepo.CreateWorkspaceRepository(
		ctx,
		createSpec,
	)
	if err != nil {
		t.Fatal(err)
	}
	authority := objectrepo.Authority{
		WorkspaceID: testWorkspaceID,
		FenceEpoch:  3,
		ClaimID:     testClaimID,
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	receipt, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Objects: []objectrepo.ObjectInput{{
			Name: "protected", Content: []byte("still-readable"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(ctx); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("simulated-process-death")
	_, err = rotateProtectedRepository(
		ctx,
		dataDir,
		testWorkspaceID,
		provider,
		func(phase string) error {
			if phase == "afterFormatChangeBeforeJournal" {
				return injected
			}
			return nil
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("fault result = %v", err)
	}
	current, err := provider.Open(
		ctx,
		testWorkspaceID,
		objectrepo.EncryptionProtected,
	)
	if err != nil || string(current.Password) != string(oldKey) {
		t.Fatalf("old vault key changed before verification: %q %v", current.Password, err)
	}
	clearBytes(current.Password)

	rotated, err := rotateProtectedRepository(
		ctx,
		dataDir,
		testWorkspaceID,
		provider,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clearBytes(rotated.RecoveryKey)
	if len(rotated.RecoveryKey) != 32 ||
		string(rotated.RecoveryKey) == string(oldKey) {
		t.Fatalf("rotation recovery key = %x", rotated.RecoveryKey)
	}
	oldSpec := repositorySpec
	oldSpec.Password = oldKey
	if stale, err := objectrepo.OpenWorkspaceRepository(
		ctx, oldSpec,
	); err == nil {
		_ = stale.Close(ctx)
		t.Fatal("old key still opens the repository")
	}
	rotatedSpec := repositorySpec
	rotatedSpec.Password = rotated.RecoveryKey
	reopened, err := objectrepo.OpenWorkspaceRepository(
		ctx,
		rotatedSpec,
	)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := reopened.Open(ctx, receipt.Objects["protected"])
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	firstRotatedKey := append([]byte(nil), rotated.RecoveryKey...)
	defer clearBytes(firstRotatedKey)
	_, err = rotateProtectedRepository(
		ctx,
		dataDir,
		testWorkspaceID,
		provider,
		func(phase string) error {
			if phase == keyRotationVerified {
				return injected
			}
			return nil
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("pre-vault-swap fault result = %v", err)
	}
	stillOld, err := provider.Open(
		ctx,
		testWorkspaceID,
		objectrepo.EncryptionProtected,
	)
	if err != nil ||
		string(stillOld.Password) != string(firstRotatedKey) {
		t.Fatalf(
			"vault changed before swap: %x %v",
			stillOld.Password,
			err,
		)
	}
	clearBytes(stillOld.Password)
	secondRotation, err := rotateProtectedRepository(
		ctx,
		dataDir,
		testWorkspaceID,
		provider,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clearBytes(secondRotation.RecoveryKey)
	if string(secondRotation.RecoveryKey) == string(firstRotatedKey) {
		t.Fatal("resumed second rotation did not publish a new key")
	}
	if _, err := os.Stat(filepath.Join(
		root,
		".vibetable",
		"coordination",
		keyRotationJournalName,
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation journal left behind: %v", err)
	}
}

func TestKeyRotationRPCStagesProtectionAndHostIntent(t *testing.T) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	defer app.ResetBootstrapState()
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	shutdown := false
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {
			shutdown = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	// The handler's repository-mode gate is independent of the repository
	// adapter; using the convenient test repository avoids touching the real
	// Windows credential vault in the test process.
	runtime.manifest.EncryptionMode = "protected"
	preview := dispatch(
		t,
		runtime,
		1,
		"repository.previewKeyRotation",
		`{}`,
	)
	if preview.Error != nil {
		t.Fatalf("preview error = %#v", preview.Error)
	}
	planID := preview.Result.(map[string]any)["planId"].(string)
	params, _ := json.Marshal(map[string]any{
		"planId": planID, "confirmed": true,
	})
	applied := dispatch(
		t,
		runtime,
		2,
		"repository.applyKeyRotation",
		string(params),
	)
	if applied.Error != nil {
		t.Fatalf("apply error = %#v", applied.Error)
	}
	result := applied.Result.(map[string]any)
	if result["state"] != "hostRestartRequired" ||
		result["newRecoveryKeyAvailable"] != false ||
		!shutdown {
		t.Fatalf("apply result = %#v shutdown=%v", result, shutdown)
	}
	paths, _ := resolvePaths(dataDir)
	intent, found, err := readKeyRotationIntent(paths)
	if err != nil || !found ||
		intent.State != keyRotationIntentRequested ||
		intent.ProtectionSnapshotID == "" {
		t.Fatalf("host intent = %#v %v %v", intent, found, err)
	}
	// Simulate process death after the durable host intent was written but
	// before Dispatch committed its generic receipt.
	if _, err := runtime.state.db.Exec(
		`DELETE FROM rpc_operation_receipts
		  WHERE workspace_id = ? AND operation_id = ?`,
		testWorkspaceID,
		intent.OperationID,
	); err != nil {
		t.Fatal(err)
	}
	intent.State = keyRotationIntentCompleted
	if err := writeKeyRotationIntent(paths, intent); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		RequestShutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	replay := dispatch(
		t,
		reopened,
		2,
		"repository.applyKeyRotation",
		string(params),
	)
	if replay.Error != nil ||
		replay.Result.(map[string]any)["state"] !=
			"hostRestartRequired" {
		t.Fatalf("intent receipt replay = %#v", replay)
	}
	if _, found, err := readKeyRotationIntent(paths); err != nil || found {
		t.Fatalf("completed intent was not consumed: found=%v err=%v", found, err)
	}
}

func createProtectedWorkspace(t *testing.T) string {
	t.Helper()
	root := createWorkspace(t, testWorkspaceID)
	path := filepath.Join(root, ".vibetable", "workspace.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["encryptionMode"] = "protected"
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
