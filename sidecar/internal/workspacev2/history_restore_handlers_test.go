package workspacev2

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type historyRestoreStub struct {
	previewParams audit.PreviewParams
	applyParams   audit.ApplyParams
	preview       audit.Preview
	applied       audit.RestoreResult
	previewErr    error
	applyErr      error
	onApply       func(context.Context) error
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
	runtime := &Runtime{
		coordinator:    coordinator,
		dispatcher:     dispatcher,
		historyRestore: service,
	}
	runtime.registerHistoryRestoreHandlers()
	return runtime
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
