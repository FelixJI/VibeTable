package workspacev2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/snapshotpkg"
)

func TestInspectPackagePlanImportsWithoutTreatingPlanIDAsPathGrant(
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
	if _, err := app.DB().NewQuery(`
		CREATE TABLE IF NOT EXISTS _vibetable_sidecar_meta (
			key TEXT PRIMARY KEY NOT NULL,
			value TEXT NOT NULL,
			updated TEXT DEFAULT (
				strftime('%Y-%m-%d %H:%M:%fZ')
			) NOT NULL
		);
		INSERT OR IGNORE INTO _vibetable_sidecar_meta (key, value)
		VALUES ('schema_version', '1');
	`).Execute(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, app)
	ledger, err := auditledger.Open(
		filepath.Join(root, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	if _, err := runtime.history.Save(
		ctx,
		filehistory.SaveRequest{
			Token:      token,
			DocumentID: "22222222-2222-4222-8222-222222222222",
			Path:       "package.txt",
			Kind:       filehistory.RevisionFormal,
			Content:    []byte("package"),
			MimeType:   "text/plain",
			CreatedBy:  "test",
			DeviceID:   testClaimID,
		},
	); err != nil {
		t.Fatal(err)
	}
	record, _, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries, packageManifest, err := runtime.snapshotPackageEntries(
		ctx, record,
	)
	if err != nil {
		t.Fatal(err)
	}
	databaseEntry := "objects/" + base64.RawURLEncoding.EncodeToString(
		[]byte("database"),
	)
	metadata := snapshotpkg.Metadata{
		FormatVersion:     2,
		WorkspaceID:       record.WorkspaceID,
		SnapshotID:        record.SnapshotID,
		WriterVersion:     "2.0.0",
		MinimumAppVersion: packageManifest.MinimumAppVersion,
	}
	tamperedEntries := make(map[string][]byte, len(entries))
	for name, raw := range entries {
		tamperedEntries[name] = append([]byte(nil), raw...)
	}
	tamperedEntries[databaseEntry][len(tamperedEntries[databaseEntry])-1] ^= 0xff
	var tamperedPackage bytes.Buffer
	if err := snapshotpkg.Export(
		&tamperedPackage, metadata, tamperedEntries, nil,
	); err != nil {
		t.Fatal(err)
	}
	outerInspection, err := snapshotpkg.Inspect(
		bytes.NewReader(tamperedPackage.Bytes()),
		int64(tamperedPackage.Len()),
		snapshotpkg.DefaultLimits(),
		nil,
	)
	if err != nil {
		t.Fatalf("outer package hashes should be self-consistent: %v", err)
	}
	if _, err := decodeImportedSnapshot(
		ctx, tamperedEntries, outerInspection,
	); !errors.Is(err, snapshotpkg.ErrInvalidPackage) {
		t.Fatalf("inner snapshot tamper error = %v", err)
	}
	packagePath := filepath.Join(t.TempDir(), "workspace.vtsnapshot")
	exportGrantID := "host-path-grant://cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	exportGrant, _ := json.Marshal(pathGrantEnvelope{
		GrantID:     exportGrantID,
		Method:      "snapshot.export",
		OperationID: testOperationID,
		Purpose:     "snapshot-export",
		Path:        packagePath,
	})
	workspaceWire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	exportParams, _ := json.Marshal(exportSnapshotParams{
		SnapshotID: record.SnapshotID,
		PathGrant:  exportGrantID,
		Encryption: "none",
		Recipients: []string{},
		Credential: json.RawMessage(`null`),
	})
	if _, err := runtime.exportSnapshot(
		WithPathGrantHeader(
			ctx,
			base64.RawURLEncoding.EncodeToString(exportGrant),
		),
		workspaceWire,
		exportParams,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetBootstrapState(); err != nil {
		t.Fatal(err)
	}

	targetWorkspaceID := "99999999-9999-4999-8999-999999999999"
	targetRoot := createWorkspace(t, targetWorkspaceID)
	targetDataDir := filepath.Join(targetRoot, ".vibetable", "data")
	targetApp := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: targetDataDir, HideStartBanner: true,
	})
	if err := targetApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	createAuditOutbox(t, targetApp)
	defer targetApp.ResetBootstrapState()
	targetLedger, err := auditledger.Open(
		filepath.Join(targetRoot, ".vibetable", "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer targetLedger.Close()
	targetRuntime, err := Open(ctx, Options{
		App: targetApp, DataDir: targetDataDir,
		WorkspaceID: targetWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: targetLedger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer targetRuntime.Close(ctx)
	inspectOperationID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	inspectGrantID := "host-path-grant://eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	inspectGrant, _ := json.Marshal(pathGrantEnvelope{
		GrantID:     inspectGrantID,
		Method:      "snapshot.inspectPackage",
		OperationID: inspectOperationID,
		Purpose:     "snapshot-import",
		Path:        packagePath,
	})
	globalWire := json.RawMessage(`{
		"scope":"global",
		"sequence":2,
		"operationId":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	}`)
	inspectParams, _ := json.Marshal(inspectSnapshotPackageParams{
		PathGrant:  inspectGrantID,
		Credential: json.RawMessage(`null`),
	})
	inspection, err := targetRuntime.inspectSnapshotPackage(
		WithPathGrantHeader(
			ctx,
			base64.RawURLEncoding.EncodeToString(inspectGrant),
		),
		globalWire,
		inspectParams,
	)
	if err != nil {
		t.Fatal(err)
	}
	planID := inspection.(map[string]any)["planId"].(string)
	inspectionResult := inspection.(map[string]any)
	if inspectionResult["workspaceId"] != testWorkspaceID ||
		inspectionResult["sourceSnapshotId"] != record.SnapshotID {
		t.Fatalf("inspect provenance = %#v", inspectionResult)
	}
	if !validUUID(planID) {
		t.Fatalf("inspect planId = %q", planID)
	}
	durablePlan, err := targetRuntime.state.snapshotImportPlan(ctx, planID)
	if err != nil ||
		durablePlan.SourceSize <= 0 ||
		durablePlan.SourceHash == "" ||
		durablePlan.SourceIdentity == "" {
		t.Fatalf("durable import binding=%#v err=%v", durablePlan, err)
	}
	importWire := json.RawMessage(`{
		"scope":"global",
		"sequence":3,
		"operationId":"ffffffff-ffff-4fff-8fff-ffffffffffff"
	}`)
	importParams, _ := json.Marshal(importSnapshotPackageParams{
		PlanID:            planID,
		Credential:        json.RawMessage(`null`),
		TargetMode:        "newWorkspace",
		TargetWorkspaceID: &targetWorkspaceID,
	})
	packageBytes, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := packagePath + ".inspected-original"
	if err := os.Rename(packagePath, originalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packagePath, packageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := targetRuntime.importSnapshotPackage(
		ctx,
		importWire,
		importParams,
	); err == nil || err.Error() != "snapshot.import_plan_stale" {
		t.Fatalf("replacement import error=%v", err)
	}
	if err := os.Remove(packagePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalPath, packagePath); err != nil {
		t.Fatal(err)
	}
	imported, err := targetRuntime.importSnapshotPackage(
		ctx,
		importWire,
		importParams,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := imported.(map[string]any)
	if result["state"] != "restoreRequired" ||
		!validUUID(result["snapshotId"].(string)) ||
		result["sourceWorkspaceId"] != testWorkspaceID ||
		result["sourceSnapshotId"] != record.SnapshotID {
		t.Fatalf("import result = %#v", result)
	}
	targetRecords, err := targetRuntime.catalog.List(
		ctx,
		targetWorkspaceID,
	)
	if err != nil || len(targetRecords) != 1 ||
		targetRecords[0].SnapshotID != result["snapshotId"] ||
		targetRecords[0].SourceWorkspaceID != testWorkspaceID ||
		targetRecords[0].SourceSnapshotID != record.SnapshotID {
		t.Fatalf("target snapshot provenance = %#v err=%v",
			targetRecords, err)
	}
	if _, err := targetRuntime.state.snapshotImportPlan(ctx, planID); err == nil {
		t.Fatal("consumed import plan remained available")
	}
}
