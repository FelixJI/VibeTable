package workspacev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/replica"
	"github.com/vibetable/vibetable/sidecar/internal/retention"
)

const (
	testWorkspaceID = "11111111-1111-4111-8111-111111111111"
	testClaimID     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testOperationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func TestRuntimeCompositionPersistsSequencePolicyAndSnapshots(t *testing.T) {
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
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	}()
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	}
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	methods := runtime.Capabilities().RPCMethods
	if len(methods) < 9 {
		t.Fatalf("capabilities = %#v", methods)
	}
	advertised := make(map[string]bool, len(methods))
	for _, method := range methods {
		advertised[method] = true
	}
	for _, method := range []string{"retention.plan", "retention.apply"} {
		if !advertised[method] {
			t.Fatalf("production retention method missing: %s (%#v)",
				method,
				methods,
			)
		}
	}
	for _, method := range []string{
		"replica.status",
		"replica.synchronize",
		"replica.forceTakeover",
		"conflict.list",
		"conflict.inspect",
		"conflict.preview",
		"conflict.apply",
	} {
		if advertised[method] {
			t.Fatalf(
				"unconfigured remote capability advertised: %s",
				method,
			)
		}
	}

	response := dispatch(t, runtime, 1, "retention.get", `{}`)
	if response.Error != nil {
		t.Fatalf("retention.get error = %#v", response.Error)
	}
	policy := response.Result.(RetentionPolicy)
	if policy.PolicyRevision != 1 ||
		policy.SnapshotCount != 50 ||
		policy.FileRevisionCount != 100 {
		t.Fatalf("default policy = %#v", policy)
	}
	response = dispatch(
		t,
		runtime,
		2,
		"retention.update",
		`{"expectedRevision":1,"snapshotDays":40,"snapshotCount":60,"snapshotBuckets":["daily","weekly"],"fileRevisionDays":35,"fileRevisionCount":120,"fileRevisionBuckets":["daily","monthly"],"repositoryLimitBytes":null}`,
	)
	if response.Error != nil {
		t.Fatalf("retention.update error = %#v", response.Error)
	}
	_, counters := runtime.coordinator.Current()
	if counters.MutationRevision != 1 {
		t.Fatalf("retention update bypassed write coordinator: %#v", counters)
	}
	insertAuditOutbox(t, app)
	response = dispatch(
		t,
		runtime,
		3,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if response.Error != nil {
		t.Fatalf("snapshot.request error = %#v", response.Error)
	}
	_, counters = runtime.coordinator.Current()
	if counters.SnapshotSequence != 1 {
		t.Fatalf("snapshot bypassed capture coordinator: %#v", counters)
	}
	if anchor := ledger.Anchor(); anchor.LedgerSequence != 2 ||
		anchor.SourceEpoch != "business-v2" ||
		anchor.SourceSequence != 1 {
		t.Fatalf("snapshot did not drain audit before capture: %#v", anchor)
	}
	response = dispatch(
		t,
		runtime,
		4,
		"snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	if response.Error != nil {
		t.Fatalf("snapshot.list error = %#v", response.Error)
	}
	list := response.Result.(map[string]any)
	if snapshots := list["snapshots"].([]map[string]any); len(snapshots) != 1 {
		t.Fatalf("snapshot list = %#v", list)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	duplicate := dispatch(
		t,
		reopened,
		4,
		"snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	if duplicate.Error != nil {
		t.Fatalf("durable operation replay failed: %#v", duplicate)
	}
	response = dispatch(t, reopened, 5, "retention.get", `{}`)
	if response.Error != nil {
		t.Fatalf("restarted retention.get = %#v", response.Error)
	}
	policy = response.Result.(RetentionPolicy)
	if policy.PolicyRevision != 2 ||
		policy.SnapshotDays != 40 ||
		policy.SnapshotCount != 60 ||
		len(policy.SnapshotBuckets) != 2 ||
		policy.SnapshotBuckets[0] != "daily" ||
		len(policy.FileRevisionBuckets) != 2 ||
		policy.FileRevisionBuckets[1] != "monthly" {
		t.Fatalf("restarted policy = %#v", policy)
	}
}

func TestSnapshotRequestAllowsInitialMutationRevisionZero(t *testing.T) {
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
	defer func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Error(err)
		}
	}()
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())

	response := dispatch(
		t,
		runtime,
		1,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if response.Error != nil {
		t.Fatalf("snapshot.request error = %#v", response.Error)
	}
	result := response.Result.(map[string]any)
	if result["mutationRevision"] != uint64(0) {
		t.Fatalf("initial mutationRevision = %#v", result)
	}
}

func TestRepositoryLimitPausesOnlyAutomaticSnapshotsAndProjectsRealReasons(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
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
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	policy, _, err := runtime.state.retention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	limit := uint64(1)
	policy.PolicyRevision++
	policy.RepositoryLimitBytes = &limit
	if err := runtime.state.updateRetention(
		ctx,
		1,
		policy,
		1,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ensureAutomaticSnapshotWithinLimit(
		ctx,
	); err == nil ||
		err.Error() != "snapshot.repository_limit_reached" {
		t.Fatalf("automatic quota error = %v", err)
	}
	protection, err := runtime.RetentionProtectionStatus(ctx)
	if err != nil ||
		!protection.Quota.AutomaticSnapshotsPaused ||
		protection.Quota.LimitBytes == nil ||
		*protection.Quota.LimitBytes != limit ||
		protection.Quota.UsageBytes < limit ||
		protection.Quota.Warning != "snapshot.repository_limit_reached" {
		t.Fatalf("durable quota status = %#v, %v", protection, err)
	}
	if runtime.retention.cancel != nil {
		runtime.retention.cancel()
		runtime.retention.wg.Wait()
		runtime.retention.cancel = nil
	}
	maintenanceFailureAt := time.Date(
		2026, 7, 29, 11, 0, 0, 0, time.UTC,
	)
	if err := runtime.retention.store.RecordMaintenanceFailure(
		ctx,
		retention.MaintenanceSweep,
		"injected background maintenance failure",
		maintenanceFailureAt,
	); err != nil {
		t.Fatal(err)
	}
	statusResponse := dispatch(
		t,
		runtime,
		1,
		"retention.status",
		`{}`,
	)
	if statusResponse.Error != nil {
		t.Fatalf("retention.status = %#v", statusResponse.Error)
	}
	status, ok := statusResponse.Result.(contractsv2.RetentionStatus)
	if !ok ||
		status.RepositoryUsageBytes < limit ||
		status.RepositoryLimitBytes == nil ||
		*status.RepositoryLimitBytes != limit ||
		!status.AutomaticSnapshotsPaused ||
		status.WarningCode == nil ||
		*status.WarningCode != "snapshot.repository_limit_reached" ||
		status.IntegrityStatus == "" ||
		status.MaintenanceFailure == nil ||
		*status.MaintenanceFailure !=
			"injected background maintenance failure" ||
		status.MaintenanceFailureStage == nil ||
		*status.MaintenanceFailureStage != "sweep" ||
		status.LastMaintenanceFailureAt == nil ||
		*status.LastMaintenanceFailureAt !=
			maintenanceFailureAt.Format(time.RFC3339Nano) {
		t.Fatalf("retention.status result = %#v", statusResponse.Result)
	}
	token, _ := runtime.coordinator.Current()
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "quota-still-writable.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("normal writes remain available"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatalf("repository quota blocked normal write: %v", err)
	}
	automatic := dispatch(
		t,
		runtime,
		2,
		"snapshot.request",
		`{"trigger":"automatic","urgency":"background"}`,
	)
	if automatic.Error == nil ||
		automatic.Error.Code != "snapshot.repository_limit_reached" {
		t.Fatalf("automatic snapshot response = %#v", automatic)
	}
	insertAuditOutbox(t, app)
	manual := dispatch(
		t,
		runtime,
		3,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if manual.Error != nil {
		t.Fatalf("manual snapshot blocked by quota: %#v", manual.Error)
	}
	listed := dispatch(
		t,
		runtime,
		4,
		"snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	if listed.Error != nil {
		t.Fatal(listed.Error)
	}
	items := listed.Result.(map[string]any)["snapshots"].([]map[string]any)
	if len(items) != 1 ||
		items[0]["syncState"] != "localOnly" ||
		!containsString(
			items[0]["retentionReasons"].([]string),
			"pinned",
		) ||
		!containsString(
			items[0]["retentionReasons"].([]string),
			"recent",
		) {
		t.Fatalf("snapshot projection = %#v", items)
	}
}

func TestAuthorityReceiptsCloseFileHistoryAndSnapshotKillWindows(
	t *testing.T,
) {
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
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	}
	runtime, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	documentID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	token, _ := runtime.coordinator.Current()
	seed, err := runtime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: documentID, Path: "receipt.txt",
		Kind: filehistory.RevisionFormal, Content: []byte("authority"),
		MimeType: "text/plain", CreatedBy: "test", DeviceID: testClaimID,
	})
	if err != nil {
		t.Fatal(err)
	}
	unlinkParams := `{"documentId":"` + documentID +
		`","expectedEffectiveRevisionId":"` +
		seed.Revision.RevisionID + `"}`
	unlink := dispatch(t, runtime, 1, "fileHistory.unlink", unlinkParams)
	if unlink.Error != nil {
		t.Fatalf("unlink failed: %#v", unlink.Error)
	}
	headAfterUnlink, found, err := runtime.headStore.Load(
		ctx,
		testWorkspaceID,
	)
	if err != nil || !found {
		t.Fatalf("head after unlink = %#v found=%v err=%v",
			headAfterUnlink, found, err)
	}
	snapshotResponse := dispatch(
		t,
		runtime,
		2,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if snapshotResponse.Error != nil {
		t.Fatalf("snapshot request failed: %#v", snapshotResponse.Error)
	}
	snapshotsBefore, err := runtime.catalog.List(ctx, testWorkspaceID)
	if err != nil || len(snapshotsBefore) != 1 {
		t.Fatalf("snapshots before replay=%d err=%v",
			len(snapshotsBefore), err)
	}
	for _, operationID := range []string{
		"bbbbbbbb-bbbb-4bbb-8bbb-000000000001",
		"bbbbbbbb-bbbb-4bbb-8bbb-000000000002",
	} {
		if _, err := runtime.state.db.ExecContext(
			ctx,
			`DELETE FROM rpc_operation_receipts
			  WHERE workspace_id = ? AND operation_id = ?`,
			testWorkspaceID,
			operationID,
		); err != nil {
			t.Fatal(err)
		}
	}
	unlinkJSON, err := json.Marshal(unlink.Result)
	if err != nil {
		t.Fatal(err)
	}
	snapshotJSON, err := json.Marshal(snapshotResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	replayedUnlink := dispatch(
		t,
		reopened,
		1,
		"fileHistory.unlink",
		unlinkParams,
	)
	if replayedUnlink.Error != nil {
		t.Fatalf("authority unlink replay failed: %#v",
			replayedUnlink.Error)
	}
	replayedUnlinkJSON, _ := json.Marshal(replayedUnlink.Result)
	var (
		firstUnlinkValue    any
		replayedUnlinkValue any
	)
	if json.Unmarshal(unlinkJSON, &firstUnlinkValue) != nil ||
		json.Unmarshal(replayedUnlinkJSON, &replayedUnlinkValue) != nil ||
		!reflect.DeepEqual(firstUnlinkValue, replayedUnlinkValue) {
		t.Fatalf("unlink exact result changed: first=%s replay=%s",
			unlinkJSON, replayedUnlinkJSON)
	}
	replayedSnapshot := dispatch(
		t,
		reopened,
		2,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if replayedSnapshot.Error != nil {
		t.Fatalf("authority snapshot replay failed: %#v",
			replayedSnapshot.Error)
	}
	replayedSnapshotJSON, _ := json.Marshal(replayedSnapshot.Result)
	if string(replayedSnapshotJSON) != string(snapshotJSON) {
		t.Fatalf("snapshot exact result changed: first=%s replay=%s",
			snapshotJSON, replayedSnapshotJSON)
	}
	headAfterReplay, found, err := reopened.headStore.Load(
		ctx,
		testWorkspaceID,
	)
	if err != nil || !found || headAfterReplay != headAfterUnlink {
		t.Fatalf(
			"filehistory side effect repeated: before=%#v after=%#v err=%v",
			headAfterUnlink,
			headAfterReplay,
			err,
		)
	}
	snapshotsAfter, err := reopened.catalog.List(ctx, testWorkspaceID)
	if err != nil || len(snapshotsAfter) != len(snapshotsBefore) {
		t.Fatalf(
			"snapshot side effect repeated: before=%d after=%d err=%v",
			len(snapshotsBefore),
			len(snapshotsAfter),
			err,
		)
	}
}

func TestRuntimeFailsClosedForIdentityParamsAndEpoch(t *testing.T) {
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
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	}
	bad := options
	bad.WorkspaceID = "99999999-9999-4999-8999-999999999999"
	if _, err := Open(context.Background(), bad); err == nil ||
		err.Error() != "workspace.identity_mismatch" {
		t.Fatalf("manifest mismatch error = %v", err)
	}
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	unknown := dispatch(
		t,
		runtime,
		1,
		"snapshot.list",
		`{"cursor":null,"limit":50,"unknown":true}`,
	)
	if unknown.Error == nil ||
		unknown.Error.Code != "workspace.request_invalid" {
		t.Fatalf("unknown params accepted: %#v", unknown)
	}
	valid := dispatch(
		t,
		runtime,
		1,
		"snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	if valid.Error != nil {
		t.Fatalf("failed params consumed sequence: %#v", valid.Error)
	}
	staleRaw := requestJSON(
		t,
		2,
		6,
		"snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	stale := runtime.Dispatcher().DispatchEnvelope(
		context.Background(),
		staleRaw,
	)
	if stale.Error == nil ||
		stale.Error.Code != protocolv2.ErrStaleSession.Error() {
		t.Fatalf("stale epoch accepted: %#v", stale)
	}
}

type verifiedAdvisoryRemote struct {
	identity     replica.RemoteIdentity
	identityErr  error
	publications []replica.Publication
}

func (remote *verifiedAdvisoryRemote) VerifyIdentity(
	context.Context,
) (replica.RemoteIdentity, error) {
	return remote.identity, remote.identityErr
}

func (*verifiedAdvisoryRemote) LeaseStore() replica.LeaseCASStore {
	return nil
}

func (remote *verifiedAdvisoryRemote) ReplicateCheckpoint(
	_ context.Context,
	checkpoint replica.Checkpoint,
) (replica.ReplicationReceipt, error) {
	return replica.ReplicationReceipt{
		WorkspaceID:     checkpoint.WorkspaceID,
		ReplicaID:       checkpoint.ReplicaID,
		SnapshotID:      checkpoint.SnapshotID,
		CatalogRevision: checkpoint.CatalogRevision,
		CheckpointID:    "sha256:" + strings.Repeat("c", 64),
		RootDigest:      checkpoint.RootDigest,
		CommittedAt:     time.Now().UTC(),
	}, nil
}

func (remote *verifiedAdvisoryRemote) ReopenAndVerifyRoots(
	_ context.Context,
	checkpoint replica.Checkpoint,
	replication replica.ReplicationReceipt,
) (replica.VerificationReceipt, error) {
	return replica.VerificationReceipt{
		WorkspaceID:      checkpoint.WorkspaceID,
		ReplicaID:        checkpoint.ReplicaID,
		SnapshotID:       checkpoint.SnapshotID,
		CatalogRevision:  checkpoint.CatalogRevision,
		CheckpointID:     replication.CheckpointID,
		RootDigest:       checkpoint.RootDigest,
		Reopened:         true,
		AllRootsReadable: true,
		VerifiedAt:       time.Now().UTC(),
	}, nil
}

func (remote *verifiedAdvisoryRemote) AppendPublication(
	_ context.Context,
	publication replica.Publication,
) error {
	remote.publications = append(remote.publications, publication)
	return nil
}

func (remote *verifiedAdvisoryRemote) ListPublications(
	context.Context,
	string,
) ([]replica.Publication, error) {
	return append([]replica.Publication(nil), remote.publications...), nil
}

func (*verifiedAdvisoryRemote) DiscoverConflicts(
	context.Context,
	replica.ConflictScan,
) ([]replica.IncomingConflict, error) {
	return []replica.IncomingConflict{}, nil
}

func TestRuntimeRegistersReplicaAndConflictOnlyForVerifiedRemote(t *testing.T) {
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
	remote := &verifiedAdvisoryRemote{
		identity: replica.RemoteIdentity{
			WorkspaceID: testWorkspaceID,
			ReplicaID:   "replica-test",
			Strength:    replica.Advisory,
		},
	}
	runtime, err := Open(context.Background(), Options{
		App:                   app,
		DataDir:               dataDir,
		WorkspaceID:           testWorkspaceID,
		SessionEpoch:          7,
		FenceEpoch:            3,
		ClaimID:               testClaimID,
		Ledger:                ledger,
		ReplicaRemote:         remote,
		ReplicaDeviceID:       "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		ReplicaPublicationKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	methods := map[string]bool{}
	for _, method := range runtime.Capabilities().RPCMethods {
		methods[method] = true
	}
	for _, method := range []string{
		"replica.status",
		"replica.synchronize",
		"replica.forceTakeover",
		"conflict.list",
		"conflict.inspect",
		"conflict.preview",
		"conflict.apply",
	} {
		if !methods[method] {
			t.Fatalf("verified remote method missing: %s", method)
		}
	}
	response := dispatch(t, runtime, 1, "replica.status", `{}`)
	if response.Error != nil {
		t.Fatalf("replica.status error = %#v", response.Error)
	}
	status := response.Result.(map[string]any)
	if status["coordinationStrength"] != "advisory" {
		t.Fatalf("advisory mislabeled: %#v", status)
	}
	// Stop the background workers so this test can prove the exact high-water
	// transitions deterministically instead of racing a fast in-memory remote.
	runtime.schedulerCancel()
	runtime.schedulerWG.Wait()
	runtime.schedulerCancel = nil
	runtime.replicaConflict.cancel()
	runtime.replicaConflict.wg.Wait()
	runtime.replicaConflict.cancel = nil
	if err := runtime.CoordinateBusinessWrite(
		context.Background(),
		"test.local_mutation",
		"record-1",
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	pendingResponse := dispatch(t, runtime, 2, "replica.status", `{}`)
	if pendingResponse.Error != nil {
		t.Fatalf(
			"pending replica.status error = %#v",
			pendingResponse.Error,
		)
	}
	pending := pendingResponse.Result.(map[string]any)
	if pending["syncState"] != "pending" ||
		pending["pendingSync"] != true {
		t.Fatalf("local mutation was not immediately pending: %#v", pending)
	}
	if err := runtime.replicaConflict.synchronizeOnce(
		context.Background(),
	); err != nil {
		t.Fatal(err)
	}
	stillPendingResponse := dispatch(t, runtime, 3, "replica.status", `{}`)
	stillPending := stillPendingResponse.Result.(map[string]any)
	if stillPending["syncState"] != "pending" ||
		stillPending["pendingSync"] != true {
		t.Fatalf(
			"unsnapshotted mutation was incorrectly replicated: %#v",
			stillPending,
		)
	}
	insertAuditOutbox(t, app)
	snapshotResponse := dispatch(
		t,
		runtime,
		4,
		"snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if snapshotResponse.Error != nil {
		t.Fatalf("snapshot.request error = %#v", snapshotResponse.Error)
	}
	if err := runtime.replicaConflict.synchronizeOnce(
		context.Background(),
	); err != nil {
		t.Fatal(err)
	}
	replicatedResponse := dispatch(t, runtime, 5, "replica.status", `{}`)
	replicated := replicatedResponse.Result.(map[string]any)
	if replicated["syncState"] != "replicated" ||
		replicated["pendingSync"] != false {
		t.Fatalf("protected mutation was not replicated: %#v", replicated)
	}
}

func TestRuntimeKeepsLocalWorkspaceAvailableWhenReplicaIsOffline(t *testing.T) {
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
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		ReplicaRemote: &verifiedAdvisoryRemote{
			identityErr: replica.ErrRemoteUnavailable,
		},
		ReplicaDeviceID:       "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		ReplicaPublicationKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("offline replica blocked local runtime: %v", err)
	}
	defer runtime.Close(context.Background())
	methods := map[string]bool{}
	for _, method := range runtime.Capabilities().RPCMethods {
		methods[method] = true
	}
	for _, method := range []string{
		"replica.status",
		"replica.synchronize",
		"replica.forceTakeover",
	} {
		if !methods[method] {
			t.Fatalf("offline replica method missing: %s", method)
		}
	}
	for method := range methods {
		if strings.HasPrefix(method, "conflict.") {
			t.Fatalf("unverified conflict method advertised: %s", method)
		}
	}
	offlineStatus := dispatch(t, runtime, 1, "replica.status", `{}`)
	if offlineStatus.Error != nil {
		t.Fatalf("offline replica.status = %#v", offlineStatus.Error)
	}
	status := offlineStatus.Result.(map[string]any)
	if status["coordinationStrength"] != "advisory" ||
		status["syncState"] != "failed" ||
		status["pendingSync"] != true {
		t.Fatalf("offline status = %#v", status)
	}
	queued := dispatch(t, runtime, 2, "replica.synchronize", `{}`)
	if queued.Error != nil ||
		queued.Result.(map[string]any)["state"] != "queued" {
		if queued.Error != nil {
			t.Fatalf("offline queue error = %#v", *queued.Error)
		}
		t.Fatalf("offline queue result = %#v", queued)
	}
	if _, err := os.Stat(filepath.Join(
		root,
		".vibetable",
		"coordination",
		"replica-pending.json",
	)); err != nil {
		t.Fatalf("durable pending marker missing: %v", err)
	}
	response := dispatch(
		t, runtime, 3, "snapshot.list",
		`{"cursor":null,"limit":50}`,
	)
	if response.Error != nil {
		t.Fatalf("local method unavailable: %#v", response.Error)
	}
}

func TestMirroredRuntimeRequiresAndComposesVerifiedFilesystemReplica(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	manifestPath := filepath.Join(root, ".vibetable", "workspace.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest contractsv2.WorkspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
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
	selectedRoot := t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(selectedRoot, ".vibetable"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(selectedRoot, ".vibetable", "workspace.json"),
		raw,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
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
	options := Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	}
	if _, err := Open(
		ctx,
		options,
	); err == nil ||
		err.Error() != "replica.selected_root_required" {
		t.Fatalf("mirrored runtime without root error = %v", err)
	}
	options.ReplicaRoot = selectedRoot
	if _, err := replica.CreateFilesystemRemote(
		ctx,
		selectedRoot,
		testWorkspaceID,
		objectrepo.NewMemory(),
	); err != nil {
		t.Fatalf("initialize filesystem replica: %v", err)
	}
	runtime, err := Open(ctx, options)
	if err != nil {
		t.Fatalf("verified filesystem replica composition: %v", err)
	}
	defer runtime.Close(ctx)
	methods := map[string]bool{}
	for _, method := range runtime.Capabilities().RPCMethods {
		methods[method] = true
	}
	for _, method := range []string{
		"replica.status",
		"replica.synchronize",
		"replica.forceTakeover",
		"conflict.list",
		"conflict.inspect",
		"conflict.preview",
		"conflict.apply",
	} {
		if !methods[method] {
			t.Fatalf("filesystem replica method missing: %s", method)
		}
	}
}

func TestRuntimeListsCanonicalDocumentsAndRestoresRevision(t *testing.T) {
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir, HideStartBanner: true,
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
	runtime, err := Open(context.Background(), Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	token, _ := runtime.coordinator.Current()
	documentID := "22222222-2222-4222-8222-222222222222"
	first, err := runtime.history.Save(
		context.Background(),
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			Path: "plans/q3.txt", Kind: filehistory.RevisionFormal,
			Content: []byte("first"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.history.Save(
		context.Background(),
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			ExpectedEffectiveRevision: &first.Revision.RevisionID,
			Kind:                      filehistory.RevisionFormal,
			Content:                   []byte("second"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	response := dispatch(
		t,
		runtime,
		1,
		"fileHistory.listDocuments",
		`{"includeDeleted":false}`,
	)
	if response.Error != nil {
		t.Fatalf("list documents error = %#v", response.Error)
	}
	documents := response.Result.(map[string]any)["documents"].([]contractsv2.FileDocument)
	if len(documents) != 1 ||
		documents[0].DocumentID != documentID ||
		documents[0].RelativePath != "plans/q3.txt" ||
		documents[0].EffectiveRevisionID == nil ||
		*documents[0].EffectiveRevisionID != second.Revision.RevisionID {
		t.Fatalf("documents = %#v", documents)
	}
	response = dispatch(
		t,
		runtime,
		2,
		"fileHistory.restore",
		`{"documentId":"`+documentID+
			`","expectedEffectiveRevisionId":"`+
			second.Revision.RevisionID+
			`","historicalRevisionId":"`+
			first.Revision.RevisionID+`"}`,
	)
	if response.Error != nil {
		t.Fatalf("restore revision error = %#v", response.Error)
	}
	result := response.Result.(map[string]any)
	if result["formalVersion"] != uint64(3) {
		t.Fatalf("restore result = %#v", result)
	}
	document, err := runtime.history.Inspect(documentID)
	if err != nil {
		t.Fatal(err)
	}
	effective := document.Revisions[len(document.Revisions)-1]
	if effective.Kind != filehistory.RevisionRestore ||
		effective.RestoredFromRevisionID == nil ||
		*effective.RestoredFromRevisionID != first.Revision.RevisionID {
		t.Fatalf("restored document = %#v", document)
	}
	if err := runtime.history.ConfigureClaimMode(token.ClaimID, true); err != nil {
		t.Fatal(err)
	}
	provisional, err := runtime.history.Save(
		context.Background(),
		filehistory.SaveRequest{
			Token: token, DocumentID: documentID,
			ExpectedEffectiveRevision: &effective.RevisionID,
			Kind:                      filehistory.RevisionFormal,
			Content:                   []byte("offline"), MimeType: "text/plain",
			CreatedBy: "test", DeviceID: testClaimID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tree := dispatch(
		t,
		runtime,
		3,
		"fileHistory.readTree",
		`{"documentId":"`+documentID+`"}`,
	)
	if tree.Error != nil {
		t.Fatalf("read tree error = %#v", tree.Error)
	}
	revisions := tree.Result.(map[string]any)["revisions"].([]map[string]any)
	projected := revisions[len(revisions)-1]
	localSequence, localOK := projected["localSequence"].(*uint64)
	formalVersion, formalOK := projected["formalVersion"].(*uint64)
	if projected["revisionId"] != provisional.Revision.RevisionID ||
		projected["revisionOrdinal"] != uint64(0) ||
		!localOK || localSequence == nil || *localSequence != 1 ||
		!formalOK || formalVersion != nil {
		t.Fatalf("provisional tree projection = %#v", projected)
	}
	response = dispatch(
		t,
		runtime,
		4,
		"fileHistory.restore",
		`{"documentId":"`+documentID+
			`","expectedEffectiveRevisionId":"`+
			provisional.Revision.RevisionID+
			`","historicalRevisionId":"`+
			first.Revision.RevisionID+`"}`,
	)
	if response.Error != nil {
		t.Fatalf("provisional restore error = %#v", response.Error)
	}
	provisionalRestore := response.Result.(map[string]any)
	if provisionalRestore["revisionOrdinal"] != uint64(0) ||
		provisionalRestore["localSequence"] != uint64(2) ||
		provisionalRestore["formalVersion"] != nil {
		t.Fatalf("provisional restore result = %#v", provisionalRestore)
	}
}

func TestValidateStartupBindingRejectsMismatchBeforeCreatingLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	metadata := filepath.Join(root, ".vibetable")
	dataDir := filepath.Join(metadata, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{
		"contractVersion":"2.0","formatVersion":1,
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"displayName":"Identity","createdAt":"2026-07-28T08:00:00Z",
		"storageMode":"direct","encryptionMode":"convenient",
		"repositoryFormat":"kopia-v3","topologySchemaVersion":1,
		"businessSchemaVersion":1,"importedFromWorkspaceId":null,
		"sourceSnapshotId":null
	}`
	if err := os.WriteFile(
		filepath.Join(metadata, "workspace.json"),
		[]byte(raw),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	err := ValidateStartupBinding(
		dataDir,
		"99999999-9999-4999-8999-999999999999",
		7,
		3,
		testClaimID,
	)
	if err == nil || err.Error() != "workspace.identity_mismatch" {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(metadata, "coordination")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("identity validation wrote coordination layout: %v", err)
	}
}

func createWorkspace(t *testing.T, workspaceID string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	metadata := filepath.Join(root, ".vibetable")
	for _, name := range []string{
		"data", "topology", "objects", "audit", "snapshots",
		"coordination", "quarantine", "temp",
	} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"contractVersion":         "2.0",
		"formatVersion":           1,
		"workspaceId":             workspaceID,
		"displayName":             "测试工作区",
		"createdAt":               time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"storageMode":             "direct",
		"encryptionMode":          "convenient",
		"repositoryFormat":        "kopia-v3",
		"topologySchemaVersion":   1,
		"businessSchemaVersion":   1,
		"importedFromWorkspaceId": nil,
		"sourceSnapshotId":        nil,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metadata, "workspace.json"),
		raw,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return root
}

func createAuditOutbox(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	if _, err := app.DB().NewQuery(`
		CREATE TABLE vibetable_audit_outbox (
			event_id TEXT PRIMARY KEY,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			UNIQUE(source_epoch, source_sequence)
		)
	`).Execute(); err != nil {
		t.Fatal(err)
	}
}

func insertAuditOutbox(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	occurredAt := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	envelope, err := auditledger.NewEnvelope(
		"business-event-1",
		"business-v2",
		1,
		"mutation-1",
		json.RawMessage(`{"type":"test"}`),
		occurredAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().NewQuery(`
		INSERT INTO vibetable_audit_outbox (
			event_id, source_epoch, source_sequence, mutation_identity,
			payload_hash, payload_json, occurred_at, status
		) VALUES (
			{:event}, {:epoch}, {:sequence}, {:mutation},
			{:hash}, {:payload}, {:occurred}, 'pending'
		)
	`).Bind(dbx.Params{
		"event":    envelope.EventID,
		"epoch":    envelope.SourceEpoch,
		"sequence": envelope.SourceSequence,
		"mutation": envelope.MutationIdentity,
		"hash":     envelope.PayloadHash,
		"payload":  []byte(envelope.Payload),
		"occurred": envelope.OccurredAt.Format(time.RFC3339Nano),
	}).Execute(); err != nil {
		t.Fatal(err)
	}
}

func dispatch(
	t *testing.T,
	runtime *Runtime,
	sequence uint64,
	method string,
	params string,
) protocolv2.ResponseEnvelope {
	t.Helper()
	return runtime.Dispatcher().DispatchEnvelope(
		context.Background(),
		requestJSON(t, sequence, 7, method, params),
	)
}

func requestJSON(
	t *testing.T,
	sequence uint64,
	epoch uint64,
	method string,
	params string,
) []byte {
	t.Helper()
	value := `{"jsonrpc":"2.0","id":"request-` +
		strconv.FormatUint(sequence, 10) +
		`","method":` + strconv.Quote(method) +
		`,"wire":{"scope":"workspace","workspaceId":"` +
		testWorkspaceID +
		`","sessionEpoch":` + strconv.FormatUint(epoch, 10) +
		`,"operationId":"` + fmt.Sprintf(
		"bbbbbbbb-bbbb-4bbb-8bbb-%012d",
		sequence,
	) +
		`","sequence":` + strconv.FormatUint(sequence, 10) +
		`},"params":` + params + `}`
	return []byte(value)
}
