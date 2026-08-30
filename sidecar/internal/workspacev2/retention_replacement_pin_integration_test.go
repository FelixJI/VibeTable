package workspacev2

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
)

func TestRetentionReplacementPinsGateLogicalSnapshotCleanupUntilExpiry(t *testing.T) {
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
	ledger, err := auditledger.Open(filepath.Join(root, ".vibetable", "audit"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	stopFileWatcher(t, runtime)
	now := time.Now().UTC()
	installRetentionTestClock(runtime, &now)

	documentID := "22222222-2222-4222-8222-222222222222"
	save := func(content string, expected *string) filehistory.SaveResult {
		t.Helper()
		token, _ := runtime.coordinator.Current()
		result, saveErr := runtime.history.Save(ctx, filehistory.SaveRequest{
			Token: token, DocumentID: documentID, Path: "replacement-pin.txt",
			ExpectedEffectiveRevision: expected,
			Kind:                      filehistory.RevisionAutosave, Content: []byte(content),
			MimeType: "text/plain", CreatedBy: "retention-test", DeviceID: testClaimID,
		})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		return result
	}
	firstRevision := save("first snapshot content", nil)
	firstSnapshot := dispatch(
		t, runtime, 1, "snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if firstSnapshot.Error != nil {
		t.Fatalf("first snapshot.request = %#v", firstSnapshot.Error)
	}
	save(
		"second snapshot content",
		&firstRevision.Revision.RevisionID,
	)
	secondSnapshot := dispatch(
		t, runtime, 2, "snapshot.request",
		`{"trigger":"manual","urgency":"foreground"}`,
	)
	if secondSnapshot.Error != nil {
		t.Fatalf("second snapshot.request = %#v", secondSnapshot.Error)
	}
	listed := dispatch(t, runtime, 3, "snapshot.list", `{"cursor":null,"limit":20}`)
	if listed.Error != nil {
		t.Fatalf("snapshot.list = %#v", listed.Error)
	}
	snapshots := listed.Result.(map[string]any)["snapshots"].([]map[string]any)
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	for _, item := range snapshots {
		if item["state"] != "ready" ||
			item["integrity"] != "verified" ||
			item["trigger"] != "manual" ||
			item["pinned"] != true {
			t.Fatalf("manual snapshot projection = %#v", item)
		}
	}
	for index, item := range snapshots {
		updated := dispatch(
			t,
			runtime,
			uint64(4+index),
			"snapshot.update",
			fmt.Sprintf(
				`{"snapshotId":%q,"action":"unpin","expectedCatalogRevision":%d}`,
				item["snapshotId"],
				item["catalogRevision"],
			),
		)
		if updated.Error != nil {
			t.Fatalf("snapshot.update = %#v", updated.Error)
		}
	}
	policy := dispatch(
		t,
		runtime,
		6,
		"retention.update",
		`{"expectedRevision":1,"snapshotDays":1,"snapshotCount":1,`+
			`"snapshotBuckets":[],"fileRevisionDays":1,`+
			`"fileRevisionCount":1,"fileRevisionBuckets":[],`+
			`"repositoryLimitBytes":null}`,
	)
	if policy.Error != nil {
		t.Fatalf("retention.update = %#v", policy.Error)
	}

	replacementPins := make([]objectrepo.RootPin, 0, 2)
	var earliestReplacementExpiry, latestReplacementExpiry time.Time
	for _, snapshotID := range []string{
		firstSnapshot.Result.(map[string]any)["snapshotId"].(string),
		secondSnapshot.Result.(map[string]any)["snapshotId"].(string),
	} {
		record, recordErr := runtime.snapshotRecord(ctx, snapshotID)
		if recordErr != nil || record.Pinned {
			t.Fatalf("unpinned snapshot record = %#v, %v", record, recordErr)
		}
		pins, pinsErr := runtime.repository.ListPins(ctx)
		if pinsErr != nil {
			t.Fatal(pinsErr)
		}
		found := false
		for _, pin := range pins {
			if pin.PinID != record.RootPinID {
				continue
			}
			if pin.ExpiresAt == nil {
				t.Fatalf("replacement pin is persistent: %#v", pin)
			}
			pinTTL := pin.ExpiresAt.Sub(pin.CreatedAt)
			if pinTTL <= 23*time.Hour+59*time.Minute || pinTTL > 24*time.Hour {
				t.Fatalf("replacement pin TTL = %s, pin = %#v", pinTTL, pin)
			}
			replacementPins = append(replacementPins, pin)
			if earliestReplacementExpiry.IsZero() ||
				pin.ExpiresAt.Before(earliestReplacementExpiry) {
				earliestReplacementExpiry = pin.ExpiresAt.UTC()
			}
			if pin.ExpiresAt.After(latestReplacementExpiry) {
				latestReplacementExpiry = pin.ExpiresAt.UTC()
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("replacement pin %q not found: %#v", record.RootPinID, pins)
		}
	}
	if len(replacementPins) != 2 || earliestReplacementExpiry.IsZero() ||
		latestReplacementExpiry.IsZero() {
		t.Fatalf("replacement pins = %#v", replacementPins)
	}

	now = earliestReplacementExpiry.Add(-time.Nanosecond)
	immediatePreview := dispatch(t, runtime, 7, "retention.plan", `{}`)
	if immediatePreview.Error != nil {
		t.Fatalf("pre-expiry retention.plan = %#v", immediatePreview.Error)
	}
	if plan := immediatePreview.Result.(map[string]any); plan["reclaimableBytes"].(int64) != 0 {
		t.Fatalf("replacement pins did not protect the pre-expiry plan: %#v", plan)
	}
	preExpiryList := dispatch(
		t, runtime, 8, "snapshot.list", `{"cursor":null,"limit":20}`,
	)
	if preExpiryList.Error != nil ||
		len(preExpiryList.Result.(map[string]any)["snapshots"].([]map[string]any)) != 2 {
		t.Fatalf("pre-expiry snapshot.list = %#v", preExpiryList)
	}
	for sequence, snapshotID := range []string{
		firstSnapshot.Result.(map[string]any)["snapshotId"].(string),
		secondSnapshot.Result.(map[string]any)["snapshotId"].(string),
	} {
		inspected := dispatch(
			t,
			runtime,
			uint64(9+sequence),
			"snapshot.inspect",
			fmt.Sprintf(`{"snapshotId":%q}`, snapshotID),
		)
		if inspected.Error != nil ||
			inspected.Result.(map[string]any)["state"] != "ready" ||
			inspected.Result.(map[string]any)["integrity"] != "verified" {
			t.Fatalf("pre-expiry snapshot.inspect = %#v", inspected)
		}
	}

	olderSnapshotID := firstSnapshot.Result.(map[string]any)["snapshotId"].(string)
	newestSnapshotID := secondSnapshot.Result.(map[string]any)["snapshotId"].(string)
	olderRecord, err := runtime.snapshotRecord(ctx, olderSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	olderRoots, err := snapshot.ReachabilityObjectIDs(
		ctx,
		runtime.repository,
		olderRecord,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	persistentPin, err := runtime.repository.Pin(
		ctx,
		token.Authority(),
		olderRoots,
		"retention-test:persistent-snapshot-reader",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	now = latestReplacementExpiry
	activeReplacementRoots, foreignPin := activeRetentionPinRoots(
		replacementPins,
		testWorkspaceID,
		now,
	)
	if foreignPin || len(activeReplacementRoots) != 0 {
		t.Fatalf("replacement pins active at exact expiry: %#v", activeReplacementRoots)
	}
	activePersistentRoots, foreignPin := activeRetentionPinRoots(
		[]objectrepo.RootPin{persistentPin},
		testWorkspaceID,
		now,
	)
	if foreignPin || len(activePersistentRoots) == 0 {
		t.Fatalf("persistent pin was treated as expired: %#v", activePersistentRoots)
	}
	exactExpiryPreview := dispatch(t, runtime, 11, "retention.plan", `{}`)
	if exactExpiryPreview.Error != nil ||
		exactExpiryPreview.Result.(map[string]any)["reclaimableBytes"].(int64) != 0 {
		t.Fatalf("persistent pin did not protect exact-expiry plan: %#v", exactExpiryPreview)
	}
	if err := runtime.repository.ReleasePin(
		ctx,
		token.Authority(),
		persistentPin.PinID,
	); err != nil {
		t.Fatal(err)
	}

	now = latestReplacementExpiry.Add(time.Nanosecond)
	preview := dispatch(t, runtime, 12, "retention.plan", `{}`)
	if preview.Error != nil {
		t.Fatalf("post-expiry retention.plan = %#v", preview.Error)
	}
	plan := preview.Result.(map[string]any)
	if plan["reclaimableBytes"].(int64) <= 0 {
		t.Fatalf("post-expiry plan did not expose a nonzero candidate: %#v", plan)
	}
	applied := dispatch(
		t,
		runtime,
		13,
		"retention.apply",
		fmt.Sprintf(`{"planId":%q}`, plan["planId"]),
	)
	if applied.Error != nil {
		t.Fatalf("retention.apply = %#v", applied.Error)
	}
	applyResult := applied.Result.(map[string]any)
	if applyResult["deletedObjects"].(int) <= 0 ||
		applyResult["reclaimedBytes"].(int64) != 0 {
		t.Fatalf("retention.apply result = %#v", applyResult)
	}

	postApplyList := dispatch(
		t, runtime, 14, "snapshot.list", `{"cursor":null,"limit":20}`,
	)
	if postApplyList.Error != nil {
		t.Fatalf("post-apply snapshot.list = %#v", postApplyList.Error)
	}
	remaining := postApplyList.Result.(map[string]any)["snapshots"].([]map[string]any)
	if len(remaining) != 1 || remaining[0]["snapshotId"] != newestSnapshotID {
		t.Fatalf("post-apply snapshots = %#v", remaining)
	}
	olderInspect := dispatch(
		t,
		runtime,
		15,
		"snapshot.inspect",
		fmt.Sprintf(`{"snapshotId":%q}`, olderSnapshotID),
	)
	if olderInspect.Error == nil || olderInspect.Error.Code != "snapshot.not_found" {
		t.Fatalf("older snapshot remained inspectable: %#v", olderInspect)
	}
	newestInspect := dispatch(
		t,
		runtime,
		16,
		"snapshot.inspect",
		fmt.Sprintf(`{"snapshotId":%q}`, newestSnapshotID),
	)
	if newestInspect.Error != nil ||
		newestInspect.Result.(map[string]any)["state"] != "ready" ||
		newestInspect.Result.(map[string]any)["integrity"] != "verified" {
		t.Fatalf("newest snapshot.inspect = %#v", newestInspect)
	}

	secondPreview := dispatch(t, runtime, 17, "retention.plan", `{}`)
	if secondPreview.Error != nil ||
		secondPreview.Result.(map[string]any)["reclaimableBytes"].(int64) != 0 {
		t.Fatalf("second retention.plan = %#v", secondPreview)
	}
	secondPlan := secondPreview.Result.(map[string]any)
	secondApply := dispatch(
		t,
		runtime,
		18,
		"retention.apply",
		fmt.Sprintf(`{"planId":%q}`, secondPlan["planId"]),
	)
	if secondApply.Error != nil ||
		secondApply.Result.(map[string]any)["deletedObjects"].(int) != 0 ||
		secondApply.Result.(map[string]any)["reclaimedBytes"].(int64) != 0 {
		t.Fatalf("second retention.apply = %#v", secondApply)
	}
}
