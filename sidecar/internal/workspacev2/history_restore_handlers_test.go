package workspacev2

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type historyRestoreStub struct {
	queryParams   audit.ReadParams
	previewParams audit.PreviewParams
	applyParams   audit.ApplyParams
	page          audit.Page
	preview       audit.Preview
	applied       audit.RestoreResult
	previewErr    error
	applyErr      error
	onApply       func(context.Context) error
}

func (stub *historyRestoreStub) ReadChangeSets(
	_ context.Context,
	params audit.ReadParams,
) (audit.Page, error) {
	stub.queryParams = params
	return stub.page, nil
}

func (stub *historyRestoreStub) PreviewRestore(
	_ context.Context,
	params audit.PreviewParams,
) (audit.Preview, error) {
	stub.previewParams = params
	return stub.preview, stub.previewErr
}

func (stub *historyRestoreStub) ApplyRestore(
	ctx context.Context,
	params audit.ApplyParams,
) (audit.RestoreResult, error) {
	stub.applyParams = params
	if stub.onApply != nil {
		if err := stub.onApply(ctx); err != nil {
			return audit.RestoreResult{}, err
		}
	}
	return stub.applied, stub.applyErr
}

func newHistoryRestoreTestRuntime(
	t *testing.T,
	service historyRestoreService,
) *Runtime {
	t.Helper()
	coordinator, err := writecoordinator.New(
		testWorkspaceID,
		3,
		testClaimID,
		7,
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := protocolv2.New()
	dispatcher.BindSession(protocolv2.Session{
		WorkspaceID: testWorkspaceID,
		Epoch:       7,
	})
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  filepath.Join(t.TempDir(), "pb_data"),
		HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	ledger, err := auditledger.Open(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	outbox, err := auditledger.NewPocketBaseOutbox(app)
	if err != nil {
		t.Fatal(err)
	}
	drainer, err := auditledger.NewDrainer(ledger, 256)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		app:                 app,
		coordinator:         coordinator,
		dispatcher:          dispatcher,
		historyRestore:      service,
		ledger:              ledger,
		fileAuditDrainer:    drainer,
		businessAuditOutbox: outbox,
	}
	runtime.registerHistoryRestoreHandlers()
	return runtime
}

func TestCoordinatedBackgroundIdentityDoesNotAdvanceTwice(t *testing.T) {
	runtime := newHistoryRestoreTestRuntime(t, &historyRestoreStub{})
	if err := writecoordinator.EnsurePocketBaseReceiptTable(
		context.Background(),
		runtime.app,
	); err != nil {
		t.Fatal(err)
	}
	applies := 0
	apply := func(ctx context.Context) error {
		applies++
		return writecoordinator.PersistPocketBaseReceipt(
			ctx,
			runtime.app,
			time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC),
		)
	}
	for range 2 {
		if err := runtime.CoordinateIdempotentBusinessWrite(
			context.Background(),
			"formula.backfill.batch",
			"job-1:rows-1-100",
			apply,
		); err != nil {
			t.Fatal(err)
		}
	}
	_, counters := runtime.coordinator.Current()
	if applies != 1 || counters.MutationRevision != 1 {
		t.Fatalf(
			"applies=%d mutationRevision=%d",
			applies,
			counters.MutationRevision,
		)
	}
	found, err := writecoordinator.HasPocketBaseReceiptIdentity(
		context.Background(),
		runtime.app,
		testWorkspaceID,
		"formula.backfill.batch",
		"job-1:rows-1-100",
	)
	if err != nil || !found {
		t.Fatalf("identity receipt found=%v err=%v", found, err)
	}
}

func TestHistoryRestoreV2IsStrictAndEnforcesWorkspaceWire(t *testing.T) {
	field := "status"
	stub := &historyRestoreStub{
		preview: audit.Preview{
			Collection:      "orders",
			ItemID:          "row-1",
			TargetRevision:  "revision-1",
			CurrentHash:     "sha256:current",
			SchemaRevision:  "schema-1",
			ScalarChanges:   []audit.ScalarFieldChange{},
			RelationChanges: []audit.RelationFieldChange{},
			Diagnostics:     []audit.Diagnostic{},
			Token:           "restore-token",
			ExpiresAt:       "2026-07-29T10:00:00Z",
			Scope:           "cell",
			Field:           &field,
			CanApply:        true,
			Restorable:      []string{"status"},
		},
	}
	runtime := newHistoryRestoreTestRuntime(t, stub)

	invalid := dispatch(
		t,
		runtime,
		1,
		"history.previewRestore",
		`{"collection":"orders","itemId":"row-1","targetRevision":"revision-1","scope":"cell","field":"status","unknown":true}`,
	)
	if invalid.Error == nil || invalid.Error.Code != "history.request_invalid" {
		t.Fatalf("unknown params accepted: %#v", invalid)
	}
	missingField := dispatch(
		t,
		runtime,
		1,
		"history.previewRestore",
		`{"collection":"orders","itemId":"row-1","targetRevision":"revision-1","scope":"row"}`,
	)
	if missingField.Error == nil ||
		missingField.Error.Code != "history.request_invalid" {
		t.Fatalf("missing closed param accepted: %#v", missingField)
	}
	valid := dispatch(
		t,
		runtime,
		1,
		"history.previewRestore",
		`{"collection":"orders","itemId":"row-1","targetRevision":"revision-1","scope":"cell","field":"status"}`,
	)
	if valid.Error != nil {
		t.Fatalf("valid preview failed: %#v", valid.Error)
	}
	if stub.previewParams.TableID != "orders" ||
		stub.previewParams.ItemID != "row-1" ||
		stub.previewParams.Field == nil ||
		*stub.previewParams.Field != "status" {
		t.Fatalf("preview params = %#v", stub.previewParams)
	}

	staleSequence := dispatch(
		t,
		runtime,
		1,
		"history.previewRestore",
		`{"collection":"orders","itemId":"row-1","targetRevision":"revision-1","scope":"row","field":null}`,
	)
	if staleSequence.Error == nil ||
		staleSequence.Error.Code != protocolv2.ErrSequenceStale.Error() {
		t.Fatalf("stale sequence accepted: %#v", staleSequence)
	}
	staleEpoch := runtime.Dispatcher().DispatchEnvelope(
		context.Background(),
		requestJSON(
			t,
			2,
			6,
			"history.previewRestore",
			`{"collection":"orders","itemId":"row-1","targetRevision":"revision-1","scope":"row","field":null}`,
		),
	)
	if staleEpoch.Error == nil ||
		staleEpoch.Error.Code != protocolv2.ErrStaleSession.Error() {
		t.Fatalf("stale epoch accepted: %#v", staleEpoch)
	}
}

func TestHistoryQueryV2IsStrictAndReturnsExistingHistoryPageShape(
	t *testing.T,
) {
	itemID := "row-1"
	field := "status"
	stub := &historyRestoreStub{
		page: audit.Page{
			Collection:                 "orders",
			ItemID:                     &itemID,
			ChangeSets:                 []audit.ChangeSet{},
			Total:                      0,
			CapabilityHash:             "sha256:capability",
			SchemaRevision:             "schema:1",
			Scope:                      "cell",
			Field:                      &field,
			HasMore:                    false,
			ArchivedDefaultRevisionIDs: map[string]string{},
		},
	}
	runtime := newHistoryRestoreTestRuntime(t, stub)
	invalid := dispatch(
		t,
		runtime,
		1,
		"history.query",
		`{"collection":"orders","scope":"cell","itemId":"row-1","field":"status","search":"","dateFrom":null,"dateTo":null,"actorId":null,"actions":[],"recordId":null,"limit":50,"offset":0,"unknown":true}`,
	)
	if invalid.Error == nil || invalid.Error.Code != "history.request_invalid" {
		t.Fatalf("unknown history query params accepted: %#v", invalid)
	}
	valid := dispatch(
		t,
		runtime,
		1,
		"history.query",
		`{"collection":"orders","scope":"cell","itemId":"row-1","field":"status","search":"","dateFrom":null,"dateTo":null,"actorId":null,"actions":[],"recordId":null,"limit":50,"offset":0}`,
	)
	if valid.Error != nil {
		t.Fatalf("valid history query failed: %#v", valid.Error)
	}
	page, ok := valid.Result.(audit.Page)
	if !ok || page.Collection != "orders" ||
		page.ChangeSets == nil ||
		page.ArchivedDefaultRevisionIDs == nil {
		t.Fatalf("history query result = %#v", valid.Result)
	}
	if stub.queryParams.TableID != "orders" ||
		stub.queryParams.Scope != "cell" ||
		stub.queryParams.ItemID == nil ||
		*stub.queryParams.ItemID != "row-1" ||
		stub.queryParams.Field == nil ||
		*stub.queryParams.Field != "status" {
		t.Fatalf("history query params = %#v", stub.queryParams)
	}
	runtime.ledger = nil
	missingLedger := dispatch(
		t,
		runtime,
		2,
		"history.query",
		`{"collection":"orders","scope":"cell","itemId":"row-1","field":"status","search":"","dateFrom":null,"dateTo":null,"actorId":null,"actions":[],"recordId":null,"limit":50,"offset":0}`,
	)
	if missingLedger.Error == nil ||
		missingLedger.Error.Code != "history.storage_failed" {
		t.Fatalf("missing ledger did not fail closed: %#v", missingLedger)
	}
}

func TestHistoryApplyRestoreV2CoordinatesBusinessIntentAndRevision(
	t *testing.T,
) {
	dataDir := filepath.Join(t.TempDir(), "pb_data")
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := writecoordinator.EnsurePocketBaseReceiptTable(
		context.Background(),
		app,
	); err != nil {
		t.Fatal(err)
	}
	newRevision := "revision-2"
	stub := &historyRestoreStub{
		applied: audit.RestoreResult{
			Collection:         "orders",
			ItemID:             "row-1",
			RestoredToRevision: "revision-1",
			NewRevisionID:      &newRevision,
			Item:               map[string]any{"status": "new"},
		},
		onApply: func(ctx context.Context) error {
			return writecoordinator.PersistPocketBaseReceipt(
				ctx,
				app,
				time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
			)
		},
	}
	runtime := newHistoryRestoreTestRuntime(t, stub)

	response := dispatch(
		t,
		runtime,
		1,
		"history.applyRestore",
		`{"collection":"orders","itemId":"row-1","token":"restore-token"}`,
	)
	if response.Error != nil {
		t.Fatalf("history apply failed: %#v", response.Error)
	}
	result, ok := response.Result.(applyHistoryRestoreResult)
	if !ok ||
		result.Collection != "orders" ||
		result.MutationRevision != 1 ||
		result.Item["status"] != "new" {
		t.Fatalf("history apply result = %#v", response.Result)
	}
	token, counters := runtime.coordinator.Current()
	if counters.MutationRevision != 1 {
		t.Fatalf("mutation revision = %d", counters.MutationRevision)
	}
	found, err := writecoordinator.HasPocketBaseReceipt(
		context.Background(),
		app,
		token,
		1,
	)
	if err != nil || !found {
		t.Fatalf("business intent receipt found=%v err=%v", found, err)
	}
	if stub.applyParams != (audit.ApplyParams{
		TableID: "orders",
		ItemID:  "row-1",
		Token:   "restore-token",
	}) {
		t.Fatalf("apply params = %#v", stub.applyParams)
	}
}

func TestHistoryApplyRestoreV2FailsClosedWithoutAdvancingRevision(
	t *testing.T,
) {
	stub := &historyRestoreStub{
		applyErr: &audit.Error{
			Code:    "restore_conflict",
			Message: "record changed",
			Details: map[string]any{},
		},
	}
	runtime := newHistoryRestoreTestRuntime(t, stub)
	response := dispatch(
		t,
		runtime,
		1,
		"history.applyRestore",
		`{"collection":"orders","itemId":"row-1","token":"restore-token"}`,
	)
	if response.Error == nil ||
		response.Error.Code != "history.restore_conflict" {
		t.Fatalf("restore conflict = %#v", response)
	}
	_, counters := runtime.coordinator.Current()
	if counters.MutationRevision != 0 {
		t.Fatalf("failed restore advanced mutation revision: %#v", counters)
	}

	stub.applyErr = errors.New("history.storage_failed")
	retry := dispatch(
		t,
		runtime,
		1,
		"history.applyRestore",
		`{"collection":"orders","itemId":"row-1","token":"restore-token"}`,
	)
	if retry.Error == nil || retry.Error.Code != "history.storage_failed" {
		t.Fatalf("failed request consumed sequence: %#v", retry)
	}
	if mapped := publicHistoryRestoreError(&audit.Error{
		Code:    "restore.request_invalid",
		Message: "invalid",
	}); mapped == nil || mapped.Error() != "history.restore_request_invalid" {
		t.Fatalf("non-history audit namespace was not normalized: %v", mapped)
	}
}
