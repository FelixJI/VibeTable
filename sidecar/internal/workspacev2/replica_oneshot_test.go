package workspacev2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	_ "modernc.org/sqlite"
)

func TestReplicaOneShotReceiptHasStrictDesktopShape(t *testing.T) {
	verifiedAt := time.Date(
		2026, 7, 28, 12, 34, 56, 123456700, time.UTC,
	)
	selection := replicaOneShotSelection{
		bundle: replica.FilesystemRecoveryBundle{
			Snapshot: snapshot.Record{
				MutationRevision: 9,
			},
		},
		identity: replica.RemoteIdentity{
			WorkspaceID: testWorkspaceID,
			ReplicaID:   "22222222-2222-4222-8222-222222222222",
			Strength:    replica.Advisory,
		},
		publication: replica.Publication{
			SnapshotID:      "33333333-3333-4333-8333-333333333333",
			CatalogRevision: 7,
			CheckpointID:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		verifiedAt: verifiedAt,
	}
	for _, operation := range []string{"initialize", "verify", "recover"} {
		activity := ""
		if operation == "recover" {
			activity = filepath.Join(t.TempDir(), "activity")
		}
		receipt, err := buildReplicaOneShotReceipt(
			operation,
			activity,
			selection,
			8,
		)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		expected := []string{
			"activityRoot",
			"catalogRevision",
			"checkpointId",
			"contractVersion",
			"mutationRevision",
			"operation",
			"receiptHash",
			"replicaId",
			"requiredMutationRevision",
			"snapshotId",
			"verifiedAt",
			"workspaceId",
		}
		if operation == "recover" {
			expected = append(expected, "restored")
		} else {
			expected = append(expected, "healthy")
		}
		actual := make([]string, 0, len(document))
		for name := range document {
			actual = append(actual, name)
		}
		sort.Strings(actual)
		sort.Strings(expected)
		if !equalReplicaOneShotStrings(actual, expected) {
			t.Fatalf("%s keys=%v want=%v", operation, actual, expected)
		}
		if document["receiptHash"] == "" ||
			document["verifiedAt"] != "2026-07-28T12:34:56.1234567Z" {
			t.Fatalf("%s receipt=%s", operation, raw)
		}
	}
}

func TestReplicaRecoveryTargetMustBeNewAndDataDirBound(t *testing.T) {
	parent := t.TempDir()
	activity := filepath.Join(parent, "staging")
	options := ReplicaOneShotOptions{
		DataDir:      filepath.Join(activity, ".vibetable", "data"),
		ActivityRoot: activity,
		ReplicaRoot:  filepath.Join(parent, "replica"),
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	resolved, dataDir, err := validateRecoveryTarget(options)
	if err != nil ||
		resolved != activity ||
		dataDir != options.DataDir {
		t.Fatalf("target=(%q,%q) err=%v", resolved, dataDir, err)
	}
	options.DataDir = filepath.Join(parent, "elsewhere")
	if _, _, err := validateRecoveryTarget(options); err == nil {
		t.Fatal("mismatched trusted data directory accepted")
	}
	options.DataDir = filepath.Join(activity, ".vibetable", "data")
	if err := os.MkdirAll(activity, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(activity, "unexpected"),
		[]byte("occupied"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateRecoveryTarget(options); err == nil {
		t.Fatal("nonempty recovery target accepted")
	}
}

func TestReplicaOneShotStrictJSONAndPathBoundary(t *testing.T) {
	var value struct {
		Value int `json:"value"`
	}
	if err := decodeStrictReplicaOneShot(
		[]byte(`{"value":1}`),
		&value,
	); err != nil || value.Value != 1 {
		t.Fatalf("strict decode=%#v err=%v", value, err)
	}
	for _, raw := range []string{
		`{"value":1,"unknown":true}`,
		`{"value":1}{}`,
		`{"value":1} trailing`,
	} {
		if err := decodeStrictReplicaOneShot(
			[]byte(raw),
			&value,
		); err == nil {
			t.Fatalf("invalid strict JSON accepted: %s", raw)
		}
	}
	root := filepath.Join(t.TempDir(), "files")
	for _, relative := range []string{
		"../escape",
		"/absolute",
		`windows\escape`,
		"a/../../escape",
	} {
		if _, err := replicaRecoveryTarget(root, relative); err == nil {
			t.Fatalf("unsafe relative path accepted: %q", relative)
		}
	}
	if target, err := replicaRecoveryTarget(
		root,
		"nested/document.txt",
	); err != nil ||
		target != filepath.Join(root, "nested", "document.txt") {
		t.Fatalf("safe path=%q err=%v", target, err)
	}
}

func TestInstallReplicaCoordinatorRestoresMonotonicHighWatermarks(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "write-coordinator.db")
	options := ReplicaOneShotOptions{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	record := snapshot.Record{
		MutationRevision: 19,
		SnapshotSequence: 23,
	}
	anchor := auditledger.Anchor{
		SourceEpoch:    "business-v2",
		SourceSequence: 17,
		LedgerSequence: 18,
		Hash:           "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := installReplicaCoordinator(
		context.Background(),
		databasePath,
		options,
		record,
		anchor,
	); err != nil {
		t.Fatal(err)
	}
	coordinator, err := writecoordinator.OpenPersistent(
		databasePath,
		options.WorkspaceID,
		options.FenceEpoch,
		options.ClaimID,
		options.SessionEpoch,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	recovery := coordinator.RecoveryState()
	highWatermark := coordinator.HighWatermark()
	if recovery.Counters.MutationRevision != record.MutationRevision ||
		recovery.Counters.SnapshotSequence != record.SnapshotSequence ||
		recovery.Counters.SessionEpoch != options.SessionEpoch ||
		highWatermark.SourceEpoch != anchor.SourceEpoch ||
		highWatermark.SourceSequence != anchor.SourceSequence ||
		highWatermark.ChainHash != anchor.Hash {
		t.Fatalf("recovered coordination=%#v", recovery)
	}
}

func TestInstallReplicaFileHeadUsesRecoveredHistoryRoot(t *testing.T) {
	headPath := filepath.Join(t.TempDir(), "filehistory-head.db")
	historyID := objectrepo.ManifestID(
		"manifest_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	fileHeadID := objectrepo.ManifestID(
		"manifest_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	payload, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"workspaceId":   testWorkspaceID,
		"historyRoot":   historyID,
		"fileRevision":  uint64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[objectrepo.ManifestID]objectrepo.ManifestRecord{
		fileHeadID: {
			ID:   fileHeadID,
			Name: "file-state-head",
			Labels: map[string]string{
				"type": "file-state-head",
			},
			Payload: payload,
		},
		historyID: {
			ID:   historyID,
			Name: "filehistory-root",
		},
	}
	options := ReplicaOneShotOptions{
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	record := snapshot.Record{
		FileRevision:     5,
		MutationRevision: 9,
	}
	if err := installReplicaFileHead(
		context.Background(),
		record,
		manifests,
		headPath,
		options,
	); err != nil {
		t.Fatal(err)
	}
	store, err := filehistory.OpenPersistentHeadStore(headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	head, found, err := store.Load(context.Background(), testWorkspaceID)
	if err != nil || !found ||
		head.Root != historyID ||
		head.Revision != record.FileRevision ||
		head.MutationRevision != record.MutationRevision {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
}

func TestInstallReplicaDatabaseAndFilesMaterializesOnlyBoundPaths(t *testing.T) {
	root := t.TempDir()
	sourceDatabase := filepath.Join(root, "source.db")
	db, err := sql.Open("sqlite", sourceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE proof(value TEXT);
		INSERT INTO proof(value) VALUES ('replica');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(sourceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"theme":"dark"}`)
	file := []byte("recovered file")
	fileID := contentIDReplicaOneShot(file)
	fileRoot, err := json.Marshal(map[string]any{
		"formatVersion": 1,
		"sourceRoot":    "manifest_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"files": map[string]objectrepo.ObjectID{
			"nested/document.txt": fileID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.Record{ObjectMap: map[string]objectrepo.ObjectID{
		"database":           contentIDReplicaOneShot(database),
		"workspace-settings": contentIDReplicaOneShot(settings),
		"file-state-root":    contentIDReplicaOneShot(fileRoot),
	}}
	objects := map[objectrepo.ObjectID][]byte{
		record.ObjectMap["database"]:           database,
		record.ObjectMap["workspace-settings"]: settings,
		record.ObjectMap["file-state-root"]:    fileRoot,
		fileID:                                 file,
	}
	metadata := filepath.Join(root, "activity", ".vibetable")
	if err := installReplicaDatabaseAndFiles(
		record,
		objects,
		metadata,
	); err != nil {
		t.Fatal(err)
	}
	recovered, err := os.ReadFile(
		filepath.Join(metadata, "files", "nested", "document.txt"),
	)
	if err != nil || string(recovered) != string(file) {
		t.Fatalf("recovered file=%q err=%v", recovered, err)
	}
}

func TestReplicaOneShotInitializeVerifyRecoverRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Kopia-backed one-shot round trip")
	}
	ctx := context.Background()
	activity := createWorkspace(t, testWorkspaceID)
	setReplicaOneShotManifestMirrored(t, activity)
	dataDir := filepath.Join(activity, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}

	selected := filepath.Join(t.TempDir(), "selected")
	if err := os.MkdirAll(
		filepath.Join(selected, ".vibetable"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(
		filepath.Join(activity, ".vibetable", "workspace.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(selected, ".vibetable", "workspace.json"),
		manifest,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	options := ReplicaOneShotOptions{
		DataDir:      dataDir,
		ReplicaRoot:  selected,
		WorkspaceID:  testWorkspaceID,
		SessionEpoch: 7,
		FenceEpoch:   3,
		ClaimID:      testClaimID,
	}
	initialized, err := InitializeWorkspaceReplica(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if initialized.Operation != "initialize" ||
		initialized.Healthy == nil ||
		!*initialized.Healthy ||
		initialized.ActivityRoot != nil ||
		initialized.SnapshotID == "" ||
		initialized.CatalogRevision == 0 {
		t.Fatalf("initialized=%#v", initialized)
	}
	runtime, closeRuntime, err := openReplicaOneShotRuntime(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	second, _, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerProtection,
			Pinned:      true,
		},
	)
	if err != nil {
		_ = closeRuntime()
		t.Fatal(err)
	}
	runtime.replicaConflict.managerMu.RLock()
	manager := runtime.replicaConflict.manager
	runtime.replicaConflict.managerMu.RUnlock()
	if manager == nil {
		_ = closeRuntime()
		t.Fatal("replica manager missing")
	}
	if err := manager.Synchronize(ctx); err != nil {
		_ = closeRuntime()
		t.Fatal(err)
	}
	if err := closeRuntime(); err != nil {
		t.Fatal(err)
	}
	beforeVerify := replicaOneShotTreeState(t, selected)
	verified, err := VerifyWorkspaceReplica(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	afterVerify := replicaOneShotTreeState(t, selected)
	if !equalReplicaOneShotStrings(beforeVerify, afterVerify) {
		t.Fatalf(
			"read-only verify mutated selected root:\nbefore=%v\nafter=%v",
			beforeVerify,
			afterVerify,
		)
	}
	if verified.Operation != "verify" ||
		verified.SnapshotID != second.SnapshotID ||
		verified.SnapshotID == initialized.SnapshotID ||
		verified.ReplicaID != initialized.ReplicaID {
		t.Fatalf("verified=%#v initialized=%#v", verified, initialized)
	}

	synchronizeLocalHighWatermark := func() ReplicaOneShotReceipt {
		t.Helper()
		localRuntime, closeLocalRuntime, err :=
			openReplicaOneShotRuntime(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		token, _ := localRuntime.coordinator.Current()
		if _, _, err := localRuntime.snapshots.Capture(
			ctx,
			snapshot.CaptureRequest{
				WorkspaceID: testWorkspaceID,
				Authority:   token.Authority(),
				Trigger:     snapshot.TriggerProtection,
				Pinned:      true,
			},
		); err != nil {
			_ = closeLocalRuntime()
			t.Fatal(err)
		}
		localRuntime.replicaConflict.managerMu.RLock()
		manager := localRuntime.replicaConflict.manager
		localRuntime.replicaConflict.managerMu.RUnlock()
		if manager == nil {
			_ = closeLocalRuntime()
			t.Fatal("replica manager missing")
		}
		if err := manager.Synchronize(ctx); err != nil {
			_ = closeLocalRuntime()
			t.Fatal(err)
		}
		if err := closeLocalRuntime(); err != nil {
			t.Fatal(err)
		}
		receipt, err := VerifyWorkspaceReplica(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.MutationRevision <
			receipt.RequiredMutationRevision {
			t.Fatalf("uncovered synchronized receipt=%#v", receipt)
		}
		return receipt
	}

	businessRuntime, closeBusinessRuntime, err :=
		openReplicaOneShotRuntime(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := businessRuntime.CoordinateBusinessWrite(
		ctx,
		"test.business",
		"business-after-reopen",
		func(context.Context) error { return nil },
	); err != nil {
		_ = closeBusinessRuntime()
		t.Fatal(err)
	}
	if err := closeBusinessRuntime(); err != nil {
		t.Fatal(err)
	}
	if receipt, err := VerifyWorkspaceReplica(
		ctx,
		options,
	); err == nil {
		t.Fatalf("unsynchronized business commit verified: %#v", receipt)
	}
	verified = synchronizeLocalHighWatermark()

	fileRuntime, closeFileRuntime, err :=
		openReplicaOneShotRuntime(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	fileToken, _ := fileRuntime.coordinator.Current()
	if _, err := fileRuntime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      fileToken,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "crash-reopen/file-history.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("file-history-authoritative-commit"),
			MimeType:   "text/plain",
			CreatedBy:  "replica-one-shot-test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		_ = closeFileRuntime()
		t.Fatal(err)
	}
	if err := closeFileRuntime(); err != nil {
		t.Fatal(err)
	}
	if receipt, err := VerifyWorkspaceReplica(
		ctx,
		options,
	); err == nil {
		t.Fatalf("unsynchronized file-history commit verified: %#v", receipt)
	}
	verified = synchronizeLocalHighWatermark()

	readOnlyRemote, err := replica.OpenFilesystemRemoteReadOnly(
		ctx,
		selected,
		testWorkspaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	publications, err := readOnlyRemote.ListPublications(
		ctx,
		testWorkspaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedSnapshots := map[string]struct{}{}
	for _, publication := range publications {
		expectedSnapshots[publication.SnapshotID] = struct{}{}
	}
	recoveredRoot := filepath.Join(t.TempDir(), "recovered")
	recoveryOptions := options
	recoveryOptions.ActivityRoot = recoveredRoot
	recoveryOptions.DataDir = filepath.Join(
		recoveredRoot,
		".vibetable",
		"data",
	)
	recovered, err := RecoverWorkspaceReplica(ctx, recoveryOptions)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Operation != "recover" ||
		recovered.Restored == nil ||
		!*recovered.Restored ||
		recovered.ActivityRoot == nil ||
		*recovered.ActivityRoot != recoveredRoot ||
		recovered.SnapshotID != verified.SnapshotID {
		t.Fatalf("recovered=%#v", recovered)
	}
	for _, relative := range []string{
		"data",
		"topology",
		"objects",
		"audit",
		"snapshots",
		"coordination",
		"files",
	} {
		info, err := os.Stat(
			filepath.Join(recoveredRoot, ".vibetable", relative),
		)
		if err != nil || !info.IsDir() {
			t.Fatalf("recovered %s info=%#v err=%v", relative, info, err)
		}
	}
	catalog, err := snapshot.OpenDurableCatalog(
		filepath.Join(
			recoveredRoot,
			".vibetable",
			"snapshots",
			"catalog.db",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	records, err := catalog.List(ctx, testWorkspaceID)
	closeErr := catalog.Close()
	if err != nil || closeErr != nil ||
		len(records) != len(expectedSnapshots) {
		t.Fatalf(
			"recovered catalog=%#v err=%v close=%v",
			records,
			err,
			closeErr,
		)
	}
	for _, record := range records {
		if _, expected := expectedSnapshots[record.SnapshotID]; !expected {
			t.Fatalf("unexpected recovered snapshot: %#v", record)
		}
		delete(expectedSnapshots, record.SnapshotID)
	}
	if len(expectedSnapshots) != 0 {
		t.Fatalf("missing recovered snapshots: %v", expectedSnapshots)
	}
	ledger, err := auditledger.Open(
		filepath.Join(recoveredRoot, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	verifyErr := ledger.Verify()
	closeErr = ledger.Close()
	if verifyErr != nil || closeErr != nil {
		t.Fatalf("recovered ledger verify=%v close=%v", verifyErr, closeErr)
	}
}

func setReplicaOneShotManifestMirrored(t *testing.T, activity string) {
	t.Helper()
	manifestPath := filepath.Join(
		activity,
		".vibetable",
		"workspace.json",
	)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := contractsv2.DecodeStrict[contractsv2.WorkspaceManifest](raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest.StorageMode = "mirrored"
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func replicaOneShotTreeState(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	err := filepath.WalkDir(
		root,
		func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			digest := ""
			if !entry.IsDir() {
				raw, err := os.ReadFile(current)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(raw)
				digest = hex.EncodeToString(sum[:])
			}
			result = append(result, relative+"|"+
				info.Mode().String()+"|"+
				info.ModTime().UTC().Format(time.RFC3339Nano)+"|"+
				digest)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func equalReplicaOneShotStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
