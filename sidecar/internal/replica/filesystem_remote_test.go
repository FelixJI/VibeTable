package replica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func TestFilesystemRemoteReplicatesAndIndependentlyReopensRoots(t *testing.T) {
	ctx := context.Background()
	workspaceID := "11111111-1111-4111-8111-111111111111"
	selectedRoot := t.TempDir()
	writeMirroredManifest(t, selectedRoot, workspaceID)
	localRepositoryRoot := filepath.Join(t.TempDir(), "activity-repository")
	repository, err := objectrepo.OpenFilesystem(localRepositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	authority := objectrepo.Authority{
		WorkspaceID: workspaceID,
		FenceEpoch:  1,
		ClaimID:     "22222222-2222-4222-8222-222222222222",
	}
	if err := repository.AcceptAuthority(ctx, nil, authority); err != nil {
		t.Fatal(err)
	}
	childContent := []byte("replicated child")
	childID := filesystemObjectID(childContent)
	historicalContent := []byte("historical child")
	historicalID := filesystemObjectID(historicalContent)
	createdAt := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	parentRevisionID := "77777777-7777-4777-8777-777777777777"
	historyPayload, err := json.Marshal(map[string]any{
		"formatVersion": 3,
		"workspaceId":   workspaceID,
		"documents": []map[string]any{{
			"contractVersion":     "2.0",
			"workspaceId":         workspaceID,
			"documentId":          "66666666-6666-4666-8666-666666666666",
			"relativePath":        "table.csv",
			"status":              "active",
			"topologyRevision":    2,
			"effectiveRevisionId": "88888888-8888-4888-8888-888888888888",
			"nextRevisionOrdinal": 3,
			"nextFormalVersion":   3,
			"revisions": []map[string]any{
				{
					"contractVersion":  "2.0",
					"revisionId":       parentRevisionID,
					"documentId":       "66666666-6666-4666-8666-666666666666",
					"parentRevisionId": nil,
					"kind":             "formal",
					"revisionOrdinal":  1,
					"formalVersion":    1,
					"objectId":         historicalID,
					"contentHash":      filesystemDigest(historicalContent),
					"size":             len(historicalContent),
					"mimeType":         "text/csv",
					"createdAt":        createdAt,
					"createdBy":        "fixture",
					"deviceId":         authority.ClaimID,
				},
				{
					"contractVersion":  "2.0",
					"revisionId":       "88888888-8888-4888-8888-888888888888",
					"documentId":       "66666666-6666-4666-8666-666666666666",
					"parentRevisionId": parentRevisionID,
					"kind":             "formal",
					"revisionOrdinal":  2,
					"formalVersion":    2,
					"objectId":         childID,
					"contentHash":      filesystemDigest(childContent),
					"size":             len(childContent),
					"mimeType":         "text/csv",
					"createdAt":        createdAt.Add(time.Second),
					"createdBy":        "fixture",
					"deviceId":         authority.ClaimID,
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	historyCommit, err := repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: authority,
		Objects: []objectrepo.ObjectInput{
			{Name: "historical", Content: historicalContent},
			{Name: "current", Content: childContent},
		},
		Manifests: []objectrepo.ManifestInput{{
			Name: "filehistory-root",
			Labels: map[string]string{
				"type": "filehistory-root", "workspaceId": workspaceID,
			},
			Payload: historyPayload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	databaseContent := strictSnapshotDatabase(t, "sqlite database")
	record := strictSnapshotFixture(
		t,
		repository,
		authority,
		1,
		1,
		databaseContent,
		map[string][]byte{"table.csv": childContent},
		historyCommit.Manifests["filehistory-root"],
	)
	strictBundle, err := snapshot.LoadSnapshotBundle(ctx, repository, record)
	if err != nil {
		t.Fatal(err)
	}
	topologyPayload := append([]byte(nil), strictBundle.TopologyHead.Payload...)
	fileHeadPayload := append([]byte(nil), strictBundle.FileStateHead.Payload...)
	snapshotID := record.SnapshotID
	remote, err := CreateFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := remote.VerifyIdentity(ctx)
	if err != nil || identity.Strength != Advisory {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
	checkpoint, err := makeCheckpoint(ctx, identity, repository, record)
	if err != nil {
		t.Fatal(err)
	}
	replication, err := remote.ReplicateCheckpoint(ctx, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.RecoverCheckpoint(
		ctx,
		snapshotID,
		1,
	); !errors.Is(err, ErrVerificationInvalid) {
		t.Fatalf("unpublished recovery error = %v", err)
	}
	publicationNow := time.Date(2026, 7, 28, 12, 1, 0, 0, time.UTC)
	publication, err := SealPublication(Publication{
		WorkspaceID: workspaceID,
		Claim: Claim{
			WorkspaceID: workspaceID,
			DeviceID:    "55555555-5555-4555-8555-555555555555",
			ClaimID:     authority.ClaimID,
			FenceEpoch:  authority.FenceEpoch,
			Nonce:       replication.CheckpointID,
			Strength:    Advisory,
			Mode:        Writable,
			IssuedAt:    publicationNow,
			HeartbeatAt: publicationNow,
			ExpiresAt:   publicationNow.Add(time.Hour),
		},
		SnapshotID:      snapshotID,
		CatalogRevision: 1,
		CheckpointID:    replication.CheckpointID,
		CreatedAt:       publicationNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.AppendPublication(ctx, publication); err != nil {
		t.Fatal(err)
	}
	verification, err := remote.ReopenAndVerifyRoots(
		ctx,
		checkpoint,
		replication,
	)
	if err != nil ||
		!verification.Reopened ||
		!verification.AllRootsReadable {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
	checkpoint2 := checkpoint
	checkpoint2.CatalogRevision = 2
	checkpoint2.Snapshot.CatalogRevision = 2
	replication2, err := remote.ReplicateCheckpoint(ctx, checkpoint2)
	if err != nil {
		t.Fatalf("second catalog revision conflicted: %v", err)
	}
	publication2Now := publicationNow.Add(time.Minute)
	publication2, err := SealPublication(Publication{
		WorkspaceID: workspaceID,
		Claim: Claim{
			WorkspaceID: workspaceID,
			DeviceID:    publication.Claim.DeviceID,
			ClaimID:     authority.ClaimID,
			FenceEpoch:  authority.FenceEpoch,
			Nonce:       replication2.CheckpointID,
			Strength:    Advisory,
			Mode:        Writable,
			IssuedAt:    publicationNow,
			HeartbeatAt: publication2Now,
			ExpiresAt:   publicationNow.Add(time.Hour),
		},
		PreviousPublicationHash: publication.CanonicalHash,
		SnapshotID:              snapshotID,
		CatalogRevision:         2,
		CheckpointID:            replication2.CheckpointID,
		CreatedAt:               publication2Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.AppendPublication(ctx, publication2); err != nil {
		t.Fatalf("second catalog publication conflicted: %v", err)
	}
	if _, err := remote.ReopenAndVerifyRoots(
		ctx,
		checkpoint,
		replication,
	); err != nil {
		t.Fatalf("first catalog revision lost: %v", err)
	}
	if _, err := remote.ReopenAndVerifyRoots(
		ctx,
		checkpoint2,
		replication2,
	); err != nil {
		t.Fatalf("second catalog revision missing: %v", err)
	}
	if err := os.RemoveAll(localRepositoryRoot); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		objectrepo.NewMemory(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reopenedIdentity, err := reopened.VerifyIdentity(ctx)
	if err != nil || reopenedIdentity != identity {
		t.Fatalf("reopened identity=%#v err=%v", reopenedIdentity, err)
	}
	bundle, err := reopened.RecoverCheckpoint(
		ctx,
		snapshotID,
		1,
	)
	if err != nil {
		t.Fatalf("remote-only recovery failed: %v", err)
	}
	if _, err := reopened.RecoverCheckpoint(
		ctx,
		snapshotID,
		2,
	); err != nil {
		t.Fatalf("second revision verified recovery failed: %v", err)
	}
	if string(bundle.Objects[record.ObjectMap["database"]]) !=
		string(databaseContent) ||
		string(bundle.Objects[record.ObjectMap["file:table.csv"]]) !=
			string(childContent) {
		t.Fatal("remote-only recovery lost database or file content")
	}
	if string(bundle.Objects[historicalID]) != string(historicalContent) ||
		bundle.Manifests[historyCommit.Manifests["filehistory-root"]].Name !=
			"filehistory-root" {
		t.Fatal("remote-only recovery lost file-history closure")
	}
	var recoveredTopologyRef struct {
		ManifestID objectrepo.ManifestID `json:"manifestId"`
	}
	if err := json.Unmarshal(
		bundle.Objects[record.ObjectMap["topology-root"]],
		&recoveredTopologyRef,
	); err != nil ||
		string(bundle.Manifests[recoveredTopologyRef.ManifestID].Payload) !=
			string(topologyPayload) {
		t.Fatal("remote-only recovery lost topology metadata")
	}
	var recoveredFileRef struct {
		SourceRoot objectrepo.ManifestID `json:"sourceRoot"`
	}
	if err := json.Unmarshal(
		bundle.Objects[record.ObjectMap["file-state-root"]],
		&recoveredFileRef,
	); err != nil ||
		string(bundle.Manifests[recoveredFileRef.SourceRoot].Payload) !=
			string(fileHeadPayload) {
		t.Fatal("remote-only recovery lost file-state metadata")
	}
	child := record.ObjectMap["file:table.csv"]
	objectPath, err := remote.objectPath(child)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReopenAndVerifyRoots(
		ctx,
		checkpoint,
		replication,
	); !errors.Is(err, ErrVerificationInvalid) {
		t.Fatalf("tampered root verification error = %v", err)
	}
}

func TestFilesystemRemoteOpenRequiresExistingIdentity(t *testing.T) {
	ctx := context.Background()
	workspaceID := "11111111-1111-4111-8111-111111111111"
	selectedRoot := t.TempDir()
	writeMirroredManifest(t, selectedRoot, workspaceID)
	if _, err := OpenFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		objectrepo.NewMemory(),
	); !errors.Is(err, ErrRemoteIdentityInvalid) {
		t.Fatalf("missing identity open error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		selectedRoot,
		".vibetable",
		"replica-v2",
		workspaceID,
		"identity.json",
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open created identity: %v", err)
	}
	created, err := CreateFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		objectrepo.NewMemory(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		objectrepo.NewMemory(),
	); err != nil {
		t.Fatalf("open existing identity: %v", err)
	}
	if _, err := CreateFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		objectrepo.NewMemory(),
	); !errors.Is(err, ErrRemoteIdentityInvalid) {
		t.Fatalf("duplicate identity create error = %v", err)
	}
	_ = created
}

func TestFilesystemRecoveryReadBudgetRejectsBeforeReadingPastLimit(
	t *testing.T,
) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("789"), 0o600); err != nil {
		t.Fatal(err)
	}
	budget := filesystemRecoveryReadBudget{remaining: 8}
	raw, err := budget.readFile(context.Background(), first)
	if err != nil || string(raw) != "123456" || budget.remaining != 2 {
		t.Fatalf(
			"first read raw=%q remaining=%d err=%v",
			raw,
			budget.remaining,
			err,
		)
	}
	if _, err := budget.readFile(
		context.Background(),
		second,
	); !errors.Is(err, snapshot.ErrBundleResourceLimit) ||
		!errors.Is(err, ErrVerificationInvalid) ||
		budget.remaining != 2 {
		t.Fatalf(
			"over-budget read remaining=%d err=%v",
			budget.remaining,
			err,
		)
	}
}

func TestFilesystemRecoveryEntryLimitsRejectBeforeMapAllocation(
	t *testing.T,
) {
	tests := map[string]func(*Checkpoint){
		"roots": func(checkpoint *Checkpoint) {
			checkpoint.Roots = make(
				[]objectrepo.ObjectID,
				maxFilesystemRecoveryEntries+1,
			)
		},
		"manifests": func(checkpoint *Checkpoint) {
			checkpoint.Manifests = make(
				[]objectrepo.ManifestRecord,
				maxFilesystemRecoveryEntries+1,
			)
		},
		"catalog objects": func(checkpoint *Checkpoint) {
			checkpoint.Snapshot.Objects = make(
				[]objectrepo.ObjectID,
				maxFilesystemRecoveryEntries+1,
			)
		},
		"object map": func(checkpoint *Checkpoint) {
			checkpoint.Snapshot.ObjectMap = make(
				map[string]objectrepo.ObjectID,
				maxFilesystemRecoveryEntries+1,
			)
			for index := 0; index <= maxFilesystemRecoveryEntries; index++ {
				checkpoint.Snapshot.ObjectMap[fmt.Sprintf("root-%d", index)] =
					objectrepo.ObjectID(fmt.Sprintf("obj_%064x", index))
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			checkpoint := Checkpoint{}
			mutate(&checkpoint)
			err := validateFilesystemRecoveryEntryLimits(checkpoint)
			if !errors.Is(err, snapshot.ErrBundleResourceLimit) ||
				!errors.Is(err, ErrVerificationInvalid) {
				t.Fatalf("entry limit error = %v", err)
			}
		})
	}
}

func TestWriteImmutableConcurrentDifferentContentNeverOverwrites(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "immutable", "value.json")
	contents := [][]byte{[]byte("first"), []byte("second")}
	start := make(chan struct{})
	errs := make(chan error, len(contents))
	var group sync.WaitGroup
	for _, content := range contents {
		content := append([]byte(nil), content...)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs <- writeImmutable(path, content)
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("immutable successes = %d, want 1", successes)
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(final) != "first" && string(final) != "second" {
		t.Fatalf("partial/mixed final content = %q", final)
	}
	if err := writeImmutable(path, final); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
}

func TestFilesystemRemotePublicationsAreImmutable(t *testing.T) {
	ctx := context.Background()
	workspaceID := "11111111-1111-4111-8111-111111111111"
	selectedRoot := t.TempDir()
	writeMirroredManifest(t, selectedRoot, workspaceID)
	repository := objectrepo.NewMemory()
	remote, err := CreateFilesystemRemote(
		ctx,
		selectedRoot,
		workspaceID,
		repository,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	checkpointID := "sha256:" + strings.Repeat("a", 64)
	publication, err := SealPublication(Publication{
		WorkspaceID: workspaceID,
		Claim: Claim{
			WorkspaceID: workspaceID,
			DeviceID:    "22222222-2222-4222-8222-222222222222",
			ClaimID:     "33333333-3333-4333-8333-333333333333",
			FenceEpoch:  1,
			Nonce:       checkpointID,
			Strength:    Advisory,
			Mode:        Writable,
			IssuedAt:    now,
			HeartbeatAt: now,
			ExpiresAt:   now.Add(time.Hour),
		},
		SnapshotID:      "44444444-4444-4444-8444-444444444444",
		CatalogRevision: 1,
		CheckpointID:    checkpointID,
		CreatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.AppendPublication(ctx, publication); err != nil {
		t.Fatal(err)
	}
	if err := remote.AppendPublication(ctx, publication); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	changed := publication
	changed.SnapshotID += "-changed"
	if err := remote.AppendPublication(
		ctx,
		changed,
	); err == nil {
		t.Fatal("overwrote immutable publication")
	}
	publications, err := remote.ListPublications(ctx, workspaceID)
	if err != nil || len(publications) != 1 ||
		publications[0].CanonicalHash != publication.CanonicalHash {
		t.Fatalf("publications=%#v err=%v", publications, err)
	}
}

func TestFilesystemConflictBranchesUseTwoDeviceDAGNotArrivalOrder(
	t *testing.T,
) {
	workspaceID := "11111111-1111-4111-8111-111111111111"
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	root := sealFilesystemBranchPublication(
		t,
		workspaceID,
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"",
		now,
	)
	local := sealFilesystemBranchPublication(
		t,
		workspaceID,
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"55555555-5555-4555-8555-555555555555",
		root.CanonicalHash,
		now.Add(time.Minute),
	)
	remoteFirst := sealFilesystemBranchPublication(
		t,
		workspaceID,
		"66666666-6666-4666-8666-666666666666",
		"77777777-7777-4777-8777-777777777777",
		"88888888-8888-4888-8888-888888888888",
		root.CanonicalHash,
		now.Add(2*time.Minute),
	)
	remoteHead := sealFilesystemBranchPublication(
		t,
		workspaceID,
		"66666666-6666-4666-8666-666666666666",
		"77777777-7777-4777-8777-777777777777",
		"99999999-9999-4999-8999-999999999999",
		remoteFirst.CanonicalHash,
		now.Add(3*time.Minute),
	)

	// Filesystem enumeration is not an ordering contract. Deliberately feed
	// child-before-parent and interleave both devices.
	branches, err := filesystemConflictBranches(
		[]Publication{remoteHead, local, remoteFirst, root},
		ConflictScan{
			WorkspaceID:          workspaceID,
			LocalSnapshotID:      local.SnapshotID,
			LocalCatalogRevision: local.CatalogRevision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 ||
		branches[0].Head.CanonicalHash != remoteHead.CanonicalHash ||
		branches[0].Base.CanonicalHash != root.CanonicalHash {
		t.Fatalf("two-device branches = %#v", branches)
	}
}

func sealFilesystemBranchPublication(
	t *testing.T,
	workspaceID string,
	deviceID string,
	claimID string,
	snapshotID string,
	previous string,
	now time.Time,
) Publication {
	t.Helper()
	checkpointSum := sha256.Sum256([]byte(snapshotID))
	checkpointID := "sha256:" +
		hex.EncodeToString(checkpointSum[:])
	publication, err := SealPublication(Publication{
		WorkspaceID: workspaceID,
		Claim: Claim{
			WorkspaceID: workspaceID,
			DeviceID:    deviceID,
			ClaimID:     claimID,
			FenceEpoch:  1,
			Nonce:       checkpointID,
			Strength:    Advisory,
			Mode:        Writable,
			IssuedAt:    now,
			HeartbeatAt: now,
			ExpiresAt:   now.Add(time.Hour),
		},
		PreviousPublicationHash: previous,
		SnapshotID:              snapshotID,
		CatalogRevision:         1,
		CheckpointID:            checkpointID,
		CreatedAt:               now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func writeMirroredManifest(
	t *testing.T,
	root string,
	workspaceID string,
) {
	t.Helper()
	directory := filepath.Join(root, ".vibetable")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(contractsv2.WorkspaceManifest{
		ContractVersion: "2.0",
		FormatVersion:   2,
		WorkspaceID:     workspaceID,
		DisplayName:     "Replica",
		CreatedAt: time.Date(
			2026, 7, 28, 0, 0, 0, 0, time.UTC,
		).Format(time.RFC3339),
		StorageMode:           "mirrored",
		EncryptionMode:        "convenient",
		RepositoryFormat:      "kopia-v3",
		TopologySchemaVersion: 1,
		BusinessSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "workspace.json"),
		raw,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}
