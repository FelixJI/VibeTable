package pluginstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pocketbase/dbx"
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
	// The frozen legacy payload remains identical; the only extension is
	// the existing authority record Id, independently checked below.
	stored, err := findRecord(service.app, KindInstallation, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"), "current")
	if err != nil || stored == nil || jsonText(snapshot, "installationId") != stored.Id {
		t.Fatal("missing PB identity projection")
	}
	sameJSON(t, snapshot, replaceJSON(t, frozenInstallation(t), map[string]any{"installationId": stored.Id}))
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
	// Plain core app: the CLI launcher's background modernc dependency check
	// would read Settings().Logs concurrently with the MaxDays write below.
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: root})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Keep the restored fixture free of the same asynchronous log writer.
	app.Settings().Logs.MaxDays = 0
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

func TestInstallationUsesExistingRecordIdentityAcrossReinstall(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	id := jsonText(first, "installationId")
	if id == "" {
		t.Fatal("missing authoritative PB record identity")
	}
	stored, err := findRecord(service.app, KindInstallation, jsonText(first, "projectKey"), jsonText(first, "pluginId"), "current")
	if err != nil || stored == nil || id != stored.Id {
		t.Fatal("identity differs from the existing PB record")
	}
	var persisted map[string]any
	raw, _ := json.Marshal(stored.GetRaw("payload_json"))
	if json.Unmarshal(raw, &persisted) != nil {
		t.Fatal("invalid persisted installation")
	}
	if _, exists := persisted["installationId"]; exists {
		t.Fatal("persisted a second installation identity")
	}
	enabled, err := service.SetEnabled(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"), true)
	if err != nil || jsonText(enabled, "installationId") != id {
		t.Fatal("state update changed installation identity")
	}
	actual := jsonInt(enabled, "revision")
	forged := replaceJSON(t, enabled, map[string]any{"installationId": "forged", "revision": actual + 1})
	if _, err := service.SaveInstallation(ctx, forged, &actual); err == nil {
		t.Fatal("caller rewrote record identity")
	}
	if _, err := service.DeleteInstallation(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"), id, actual, false); err != nil {
		t.Fatal(err)
	}
	second, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if jsonText(second, "installationId") == "" || jsonText(second, "installationId") == id {
		t.Fatal("same-version same-package reinstall reused the old record identity")
	}
}

func TestUninstallCASRejectsUpgradeAndReinstallWithoutCleanup(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	project, plugin := jsonText(first, "projectKey"), jsonText(first, "pluginId")
	id := jsonText(first, "installationId")
	setting := []byte(`{"projectKey":"` + project + `","pluginId":"` + plugin + `","settingKey":"secret","value":"retained","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil); err != nil {
		t.Fatal(err)
	}
	enabled, err := service.SetEnabled(ctx, project, plugin, true)
	if err != nil {
		t.Fatal(err)
	}
	assertRetained := func(wantID string) {
		t.Helper()
		got, err := service.GetInstallation(ctx, project, plugin)
		if err != nil || jsonText(got, "installationId") != wantID {
			t.Fatal("installation was removed", err)
		}
		settings, err := service.GetPrivateSetting(ctx, project, plugin, "secret")
		if err != nil || settings == nil {
			t.Fatal("private settings were removed", err)
		}
		revisions, err := service.ListPackageRevisions(ctx, project, plugin)
		if err != nil || len(revisions) != 1 {
			t.Fatal("package references were removed", err)
		}
	}
	if _, err := service.DeleteInstallation(ctx, project, plugin, id, 1, true); err == nil {
		t.Fatal("stale revision deleted upgraded installation")
	}
	assertRetained(id)
	if _, err := service.DeleteInstallation(ctx, project, plugin, id, jsonInt(enabled, "revision"), false); err != nil {
		t.Fatal(err)
	}
	second, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteInstallation(ctx, project, plugin, id, 1, true); err == nil {
		t.Fatal("ABA deleted reinstallation")
	}
	assertRetained(jsonText(second, "installationId"))
	if _, err := service.DeleteInstallation(ctx, project, plugin, "", 1, true); err == nil {
		t.Fatal("identity-free delete admitted")
	}
	assertRetained(jsonText(second, "installationId"))
	if _, err := service.DeleteInstallation(ctx, project, plugin, jsonText(second, "installationId"), 1, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := service.GetInstallation(ctx, project, plugin); got != nil {
		t.Fatal("matching CAS did not delete")
	}
	if got, _ := service.GetPrivateSetting(ctx, project, plugin, "secret"); got != nil {
		t.Fatal("cleanup did not remove settings")
	}
	if got, _ := service.ListPackageRevisions(ctx, project, plugin); len(got) != 0 {
		t.Fatal("cleanup retained package references")
	}
}
