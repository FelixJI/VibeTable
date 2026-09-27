package pluginstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func frozenInstallRequest(t *testing.T) (json.RawMessage, json.RawMessage) {
	t.Helper()
	var snapshot map[string]any
	if err := json.Unmarshal(frozenInstallation(t), &snapshot); err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(map[string]any{"planId": "plan-1", "projectRevision": "epoch-1", "projectKey": snapshot["projectKey"], "sourceType": snapshot["sourceType"], "sourceLocation": snapshot["sourceLocation"], "packageHash": snapshot["packageHash"], "manifest": snapshot["manifest"], "schemas": snapshot["schemas"]})
	if err != nil {
		t.Fatal(err)
	}
	return plan, loadFrozenOracleCase(t, "package-revision-persisted")
}

func sameJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON mismatch\ngot %s\nwant %s", got, want)
	}
}

func TestCommitInstallMatchesFrozenOracleAndIsAtomic(t *testing.T) {
	service := newTestService(t)
	service.workspaceID = "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	plan, revision := frozenInstallRequest(t)
	snapshot, err := service.CommitInstall(context.Background(), plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, snapshot, frozenInstallation(t))
	if service.ProjectKey() != "local:"+oracleWorkspaceID {
		t.Fatal(service.ProjectKey())
	}
	if _, err := service.CommitInstall(context.Background(), plan, revision); err == nil {
		t.Fatal("duplicate committed")
	} else if public, ok := PublicError(err); !ok || public.Code != PublicCodeAlreadyInstalled {
		t.Fatalf("duplicate: %v", err)
	}
	records, err := service.app.FindRecordsByFilter("vibetable_outbox", "topic='plugin.catalog.changed'", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("events=%d %v", len(records), err)
	}
	raw, _ := json.Marshal(records[0].GetRaw("payload_json"))
	var event CatalogChangedEvent
	if json.Unmarshal(raw, &event) != nil || !ValidateCatalogEvent(event) {
		t.Fatalf("bad event: %s", raw)
	}
	for _, kind := range []string{KindInstallation, KindRevision, KindAudit} {
		rows, err := service.list(context.Background(), kind, service.ProjectKey(), "")
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s %d %v", kind, len(rows), err)
		}
	}

	failed := newTestService(t)
	failed.app.OnRecordCreate("vibetable_outbox").BindFunc(func(*core.RecordEvent) error { return errors.New("outbox unavailable") })
	if _, err := failed.CommitInstall(context.Background(), plan, revision); err == nil {
		t.Fatal("commit ignored outbox failure")
	}
	count, err := failed.app.CountRecords(recordsCollection)
	if err != nil || count != 0 {
		t.Fatalf("partial install=%d %v", count, err)
	}
}

func TestSnapshotValidationRejectsMalformedAndForeignState(t *testing.T) {
	service := newTestService(t)
	for _, changes := range []map[string]any{{"revision": 0}, {"revision": 1.5}, {"status": "unknown"}, {"manifest": map[string]any{}}, {"sourceChanged": "false"}, {"blockingReasons": "ignored"}, {"projectKey": "local:00000000000000000000000000000000"}} {
		if _, err := service.SaveInstallation(context.Background(), replaceJSON(t, frozenInstallation(t), changes), nil); err == nil {
			t.Fatalf("accepted %#v", changes)
		}
	}
}

func TestNewWorkspaceMarkerPreventsLaterLegacyImportAndSurvivesReopen(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "plugins.db")
	if _, err := service.ImportLegacySQLite(ctx, missing); err != nil {
		t.Fatal(err)
	}
	reopened := New(service.app, "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7")
	result, err := reopened.ImportLegacySQLite(ctx, frozenLegacyFixture(t, nil))
	if err != nil || result.Status != ImportStatusAlreadyComplete || result.RecordCount != 0 {
		t.Fatalf("late import=%+v %v", result, err)
	}
	items, err := reopened.ListInstallations(ctx, reopened.ProjectKey())
	if err != nil || len(items) != 0 {
		t.Fatalf("legacy overwrote new workspace: %s %v", items, err)
	}
}

func TestWholeDatabaseSnapshotRestoresFourKindsAndMigrationMarkerAtNewPath(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if _, err := service.ImportLegacySQLite(ctx, frozenLegacyFixture(t, nil)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := service.app.DB().NewQuery("VACUUM INTO {:destination}").Bind(dbx.Params{"destination": filepath.Join(root, "data.db")}).Execute(); err != nil {
		t.Fatal(err)
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: root, HideStartBanner: true})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	restored := New(app, "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7")
	if result, err := restored.ImportLegacySQLite(ctx, filepath.Join(root, "removed-legacy-source.db")); err != nil || result.Status != ImportStatusAlreadyComplete {
		t.Fatalf("marker lost: %+v %v", result, err)
	}
	for _, kind := range []string{KindInstallation, KindRevision, KindSetting, KindAudit} {
		original, err := service.list(ctx, kind, service.ProjectKey(), "")
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := restored.list(ctx, kind, restored.ProjectKey(), "")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(original, reopened) {
			t.Fatalf("restored %s drift: %+v / %+v", kind, original, reopened)
		}
	}
}
