package workspacev2

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestBusinessAuditEpochRotatesAcrossSnapshotRestoreAndHistoryContinues(
	t *testing.T,
) {
	ctx := context.Background()
	root := createWorkspace(t, testWorkspaceID)
	dataDir := filepath.Join(root, ".vibetable", "data")
	openApp := func() *pocketbase.PocketBase {
		app := pocketbase.NewWithConfig(pocketbase.Config{
			DefaultDataDir: dataDir, HideStartBanner: true,
		})
		migrations.Register(app)
		if err := app.Bootstrap(); err != nil {
			t.Fatal(err)
		}
		if err := app.RunAllMigrations(); err != nil {
			_ = app.ResetBootstrapState()
			t.Fatal(err)
		}
		return app
	}
	app := openApp()
	definition, err := schemaapi.New(app).ApplyChange(
		ctx,
		schemaapi.Change{
			Definition: schema.TableDefinition{
				ContractVersion: schema.ContractVersion,
				TableID:         "audit_epoch_notes",
				PhysicalName:    "audit_epoch_notes",
				DisplayName:     "Audit epoch notes",
				Kind:            schema.TableKindBase,
				SchemaRevision:  schema.FormatSchemaRevision(0),
				ArchivePolicy: schema.ArchivePolicy{
					Mode: schema.ArchiveModeNone,
				},
				Fields: []schema.FieldDefinition{{
					FieldID:      "title_id",
					PhysicalName: "title",
					DisplayName:  "Title",
					Kind:         schema.FieldKindScalar,
					DataType:     schema.DataTypeShortText,
					StorageType:  schema.StorageText,
					Nullable:     true,
					Constraints:  []schema.FieldConstraint{},
					Editor: schema.EditorDefinition{
						Kind: "text", Config: map[string]any{},
					},
				}},
				Indexes: []schema.IndexDefinition{},
			},
			ExpectedRevision: 0,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".vibetable", "audit")
	ledger, err := auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	newBusinessServices := func(
		app *pocketbase.PocketBase,
		ledger *auditledger.Ledger,
	) (*mutation.Kernel, *audit.Service) {
		kernel := mutation.New(app, mutation.MetadataSchemaSource{})
		service, serviceErr := audit.New(
			app,
			kernel,
			mutation.MetadataSchemaSource{},
			[]byte(strings.Repeat("a", 32)),
			audit.WithLedgerHistory(ledger),
		)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		return kernel, service
	}
	kernel, auditService := newBusinessServices(app, ledger)
	shutdownRequested := false
	runtime, err := Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		Audit: auditService,
		RequestShutdown: func() {
			shutdownRequested = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stopBackgroundWorkers(runtime)
	recordID := "auditepochrow01"
	apply := func(
		runtime *Runtime,
		kernel *mutation.Kernel,
		key string,
		kind mutation.OperationKind,
		title string,
	) {
		t.Helper()
		err := runtime.CoordinateBusinessWrite(
			ctx,
			"mutation.apply",
			key,
			func(writeCtx context.Context) error {
				_, applyErr := kernel.Apply(writeCtx, mutation.Request{
					ContractVersion: mutation.ContractVersion,
					RequestID:       "request-" + key,
					IdempotencyKey:  "idempotency-" + key,
					TableID:         definition.TableID,
					SchemaRevision:  definition.SchemaRevision,
					Operations: []mutation.Operation{{
						Kind:     kind,
						RecordID: &recordID,
						Values: map[string]any{
							"title": title,
						},
					}},
					Actor: mutation.Actor{
						Type: "user",
						ID:   "audit-epoch-test",
					},
				})
				return applyErr
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(runtime, kernel, "before-snapshot", mutation.OperationInsert, "one")
	if _, err := runtime.Drain(ctx, noDeadline()); err != nil {
		t.Fatal(err)
	}
	token, _ := runtime.coordinator.Current()
	target, created, err := runtime.snapshots.Capture(
		ctx,
		snapshot.CaptureRequest{
			WorkspaceID: testWorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerManual,
			Pinned:      true,
		},
	)
	if err != nil || !created {
		t.Fatalf("snapshot=%#v created=%v err=%v", target, created, err)
	}
	apply(runtime, kernel, "after-snapshot", mutation.OperationUpdate, "two")
	if _, err := runtime.Drain(ctx, noDeadline()); err != nil {
		t.Fatal(err)
	}
	beforeRestore := historyForRecord(
		t, auditService, definition.TableID, recordID,
	)
	if len(beforeRestore.ChangeSets) != 2 {
		t.Fatalf("pre-restore history = %#v", beforeRestore)
	}
	preRestoreChangeSets := map[string]bool{}
	for _, changeSet := range beforeRestore.ChangeSets {
		preRestoreChangeSets[changeSet.ChangeSetID] = true
	}
	previewRaw, _ := json.Marshal(previewSnapshotRestoreParams{
		SnapshotID: target.SnapshotID,
		TargetMode: "currentWorkspace",
	})
	preview, err := runtime.previewSnapshotRestore(ctx, nil, previewRaw)
	if err != nil {
		t.Fatal(err)
	}
	applyRaw, _ := json.Marshal(applySnapshotRestoreParams{
		PlanID:    preview.(map[string]any)["planId"].(string),
		Confirmed: true,
	})
	wire := json.RawMessage(`{
		"scope":"workspace",
		"workspaceId":"11111111-1111-4111-8111-111111111111",
		"sessionEpoch":7,
		"sequence":1,
		"operationId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`)
	if _, err := runtime.applySnapshotRestore(ctx, wire, applyRaw); err != nil {
		t.Fatal(err)
	}
	if !shutdownRequested {
		t.Fatal("restore did not request shutdown")
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
	installed, err := ApplyPendingSnapshotRestore(
		ctx, dataDir, testWorkspaceID,
	)
	if err != nil || !installed {
		t.Fatalf("offline restore installed=%v err=%v", installed, err)
	}

	app = openApp()
	defer app.ResetBootstrapState()
	ledger, err = auditledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	kernel, auditService = newBusinessServices(app, ledger)
	runtime, err = Open(ctx, Options{
		App: app, DataDir: dataDir,
		WorkspaceID: testWorkspaceID, SessionEpoch: 7,
		FenceEpoch: 3, ClaimID: testClaimID, Ledger: ledger,
		Audit: auditService,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(ctx)
	stopBackgroundWorkers(runtime)
	if err := runtime.CompletePendingSnapshotRestore(ctx); err != nil {
		t.Fatal(err)
	}
	apply(runtime, kernel, "after-restore", mutation.OperationUpdate, "three")
	if _, err := runtime.Drain(ctx, noDeadline()); err != nil {
		t.Fatal(err)
	}
	queryRaw, _ := json.Marshal(queryHistoryParams{
		Collection: definition.TableID,
		Scope:      "row",
		ItemID:     &recordID,
		Search:     "",
		Actions:    []string{},
		Limit:      20,
		Offset:     0,
	})
	queryResult, err := runtime.queryHistory(ctx, nil, queryRaw)
	if err != nil {
		t.Fatalf("history.query: %v", err)
	}
	afterRestore, ok := queryResult.(audit.Page)
	if !ok {
		t.Fatalf("history.query result = %#v", queryResult)
	}
	if len(afterRestore.ChangeSets) != 3 ||
		afterRestore.ChangeSets[0].Action != "update" {
		t.Fatalf("post-restore history = %#v", afterRestore)
	}
	for changeSetID := range preRestoreChangeSets {
		found := false
		for _, changeSet := range afterRestore.ChangeSets {
			if changeSet.ChangeSetID == changeSetID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf(
				"history.query lost pre-restore change set %q: %#v",
				changeSetID,
				afterRestore.ChangeSets,
			)
		}
	}
	seenEpochs := map[string]uint64{}
	receiptEpochs := map[string]uint64{}
	for _, record := range ledger.Records(0, 100) {
		var payload map[string]any
		if json.Unmarshal(record.Envelope.Payload, &payload) != nil {
			t.Fatal("invalid ledger payload")
		}
		if payload["type"] == "workspace.v2.business-mutation" {
			receiptEpochs[record.Envelope.SourceEpoch]++
		}
		if !isBusinessHistoryPayload(record.Envelope.Payload) {
			continue
		}
		seenEpochs[record.Envelope.SourceEpoch]++
	}
	rotated :=
		"business-v2:" + testWorkspaceID + ":00000000000000000002"
	if seenEpochs["business-v2"] != 2 || seenEpochs[rotated] != 1 {
		t.Fatalf("business audit epochs = %#v", seenEpochs)
	}
	if receiptEpochs["business-v2"] != 2 || receiptEpochs[rotated] != 1 {
		t.Fatalf("business receipt epochs = %#v", receiptEpochs)
	}
	if err := ledger.Verify(); err != nil {
		t.Fatalf("ledger verification: %v", err)
	}
}

func noDeadline() time.Time {
	return time.Time{}
}

func stopBackgroundWorkers(runtime *Runtime) {
	if runtime.schedulerCancel != nil {
		runtime.schedulerCancel()
		runtime.schedulerWG.Wait()
		runtime.schedulerCancel = nil
	}
}

func historyForRecord(
	t *testing.T,
	service *audit.Service,
	tableID string,
	recordID string,
) audit.Page {
	t.Helper()
	page, err := service.ReadChangeSets(
		context.Background(),
		audit.ReadParams{
			TableID: tableID,
			ItemID:  &recordID,
			Scope:   "row",
			Limit:   20,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func isBusinessHistoryPayload(raw json.RawMessage) bool {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	_, hasChangeSet := payload["changeSetId"]
	return hasChangeSet
}
