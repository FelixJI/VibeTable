package pluginstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
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

// frozenNativeInstallRequest adapts the frozen v1 oracle install case to the
// post-#484 native contract: #486 rejects new commits of legacy Worker
// packages, so the live install/upgrade/rollback flows are exercised through
// the native form a current Host admission sends.
func frozenNativeInstallRequest(t *testing.T) (json.RawMessage, json.RawMessage) {
	t.Helper()
	plan, revision := frozenInstallRequest(t)
	decode := func(raw json.RawMessage) map[string]any {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	planObject, revisionObject := decode(plan), decode(revision)
	nativeizeFrozenManifest(t, planObject["manifest"])
	nativeizeFrozenManifest(t, revisionObject["manifest"])
	encodedPlan, err := json.Marshal(planObject)
	if err != nil {
		t.Fatal(err)
	}
	encodedRevision, err := json.Marshal(revisionObject)
	if err != nil {
		t.Fatal(err)
	}
	return encodedPlan, encodedRevision
}

// nativeizeFrozenManifest converts the frozen v1 oracle manifest into the
// native install contract (schema v2, pluginApi 3.x, workerEntry removed,
// one custom view per action) without touching any other field.
func nativeizeFrozenManifest(t *testing.T, value any) {
	t.Helper()
	manifest, ok := value.(map[string]any)
	if !ok {
		t.Fatal("frozen payload has no manifest")
	}
	manifest["$schema"] = "vibetable.plugin-manifest.v2"
	compatibility, ok := manifest["compatibility"].(map[string]any)
	if !ok {
		t.Fatal("frozen manifest has no compatibility")
	}
	compatibility["pluginApi"] = "3.x"
	views := []any{}
	actions, ok := manifest["actions"].([]any)
	if !ok {
		t.Fatal("frozen manifest has no actions")
	}
	for _, item := range actions {
		action, ok := item.(map[string]any)
		if !ok {
			t.Fatal("frozen manifest action is malformed")
		}
		delete(action, "workerEntry")
		views = append(views, map[string]any{
			"viewId": action["actionId"], "actionId": action["actionId"], "entry": "ui/index.html",
		})
	}
	manifest["ui"] = map[string]any{"customViews": views}
}

func TestCommitPackageRejectsLegacyWorkerContract(t *testing.T) {
	// The frozen v1 oracle plan is exactly the legacy Worker shape #486
	// retires: every commit flavor must reject it fail-closed without
	// persisting anything, while the same plan in native form still commits.
	plan, revision := frozenInstallRequest(t)
	service := newTestService(t)
	ctx := context.Background()
	bound := replaceJSON(t, plan, map[string]any{"targetInstallationId": "retired0record0001", "targetRevision": 1})
	for name, commit := range map[string]func(context.Context, json.RawMessage, json.RawMessage) (json.RawMessage, error){
		"install":  service.CommitInstall,
		"upgrade":  service.CommitUpgrade,
		"rollback": service.CommitRollback,
	} {
		if name == "install" {
			if _, err := commit(ctx, plan, revision); err == nil {
				t.Fatalf("%s committed a legacy Worker package", name)
			} else if storeErr, ok := err.(*Error); !ok || storeErr.Code != WorkerRetiredReason {
				t.Fatalf("%s rejection = %v, want %s", name, err, WorkerRetiredReason)
			}
		} else {
			if _, err := commit(ctx, bound, revision); err == nil {
				t.Fatalf("%s committed a legacy Worker package", name)
			} else if storeErr, ok := err.(*Error); !ok || storeErr.Code != WorkerRetiredReason {
				t.Fatalf("%s rejection = %v, want %s", name, err, WorkerRetiredReason)
			}
		}
		count, err := service.app.CountRecords(recordsCollection)
		if err != nil || count != 0 {
			t.Fatalf("%s persisted %d records", name, count)
		}
	}
	public, ok := PublicError(workerRetiredRejection())
	if !ok || public.Code != PublicCodeBlocked {
		t.Fatalf("retired rejection public mapping = %+v ok=%v", public, ok)
	}
	if reasons, _ := public.Details["blockingReasons"].([]string); len(reasons) != 1 || reasons[0] != WorkerRetiredReason {
		t.Fatalf("retired rejection details = %#v", public.Details)
	}
	nativePlan, nativeRevision := frozenNativeInstallRequest(t)
	if _, err := service.CommitInstall(ctx, nativePlan, nativeRevision); err != nil {
		t.Fatalf("native install rejected: %v", err)
	}
}

func TestUpgradeAndLatePrivateWriteAreBoundToOriginalInstallation(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	project, plugin := jsonText(first, "projectKey"), jsonText(first, "pluginId")
	id := jsonText(first, "installationId")
	bound := replaceJSON(t, plan, map[string]any{"targetInstallationId": id, "targetRevision": 1})
	setting := []byte(`{"projectKey":"` + project + `","pluginId":"` + plugin + `","settingKey":"late","value":"old","revision":1}`)
	// A request already read an absent key; neither deletion nor ABA can admit its late set.
	if err := service.ValidateProject(project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteInstallation(ctx, project, plugin, id, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, 1); err == nil {
		t.Fatal("late write resurrected deleted settings")
	}
	if _, err := service.CommitUpgrade(ctx, bound, revision); err == nil {
		t.Fatal("upgrade turned into install")
	}
	second, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitUpgrade(ctx, bound, revision); err == nil {
		t.Fatal("ABA upgrade changed new installation")
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, 1); err == nil {
		t.Fatal("ABA late setting changed new installation")
	}
	if value, _ := service.GetPrivateSetting(ctx, project, plugin, "late"); value != nil {
		t.Fatalf("unexpected setting %s", value)
	}
	authority, _ := service.GetInstallation(ctx, project, plugin)
	sameJSON(t, authority, second)
	newID := jsonText(second, "installationId")
	nextPlan := replaceJSON(t, plan, map[string]any{"targetInstallationId": newID, "targetRevision": 1})
	upgraded, err := service.CommitUpgrade(ctx, nextPlan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if jsonInt(upgraded, "revision") != 2 || jsonText(upgraded, "installationId") != newID {
		t.Fatalf("upgrade identity %s", upgraded)
	}
	if _, err := service.CommitUpgrade(ctx, nextPlan, revision); err == nil {
		t.Fatal("stale plan applied twice")
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil, newID, 1); err == nil {
		t.Fatal("prior revision authorized a write")
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil, newID, 2); err != nil {
		t.Fatal(err)
	}
}

func sameJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON mismatch\ngot %s\nwant %s", got, want)
	}
}

func TestUpgradeTransactionFailurePreservesInstallationAndPackageLedger(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.ListPackageRevisions(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"))
	if err != nil {
		t.Fatal(err)
	}
	bound := replaceJSON(t, plan, map[string]any{"targetInstallationId": jsonText(first, "installationId"), "targetRevision": 1})
	service.app.OnRecordCreate("vibetable_outbox").BindFunc(func(*core.RecordEvent) error { return errors.New("outbox unavailable") })
	for _, commit := range []func(context.Context, json.RawMessage, json.RawMessage) (json.RawMessage, error){service.CommitUpgrade, service.CommitRollback} {
		if _, err := commit(ctx, bound, revision); err == nil {
			t.Fatal("ignored outbox failure")
		}
		after, err := service.GetInstallation(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"))
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, after, first)
		revisions, err := service.ListPackageRevisions(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"))
		if err != nil || !reflect.DeepEqual(before, revisions) {
			t.Fatalf("partial package ledger %s %v", revisions, err)
		}
	}
}

func TestAtomicUpgradeSwitchesCompletePackageAndRollbackPreservesIdentity(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := decodePayload(plan)
	manifest := decoded["manifest"].(map[string]any)
	manifest["version"] = "2.0.0"
	nextHash := "sha256:" + strings.Repeat("b", 64)
	nextPlan := replaceJSON(t, plan, map[string]any{"targetInstallationId": jsonText(first, "installationId"), "targetRevision": 1,
		"packageHash": nextHash, "manifest": manifest})
	nextRevision := replaceJSON(t, revision, map[string]any{"packageHash": nextHash, "manifest": manifest, "version": "2.0.0", "localPath": "C:/cache/next.vtplugin"})
	second, err := service.CommitUpgrade(ctx, nextPlan, nextRevision)
	if err != nil {
		t.Fatal(err)
	}
	if jsonText(second, "version") != "2.0.0" || jsonText(second, "packageHash") != nextHash || jsonInt(second, "revision") != 2 {
		t.Fatalf("partial upgraded snapshot %s", second)
	}
	rollbackPlan := replaceJSON(t, plan, map[string]any{"targetInstallationId": jsonText(first, "installationId"), "targetRevision": 2})
	third, err := service.CommitRollback(ctx, rollbackPlan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if jsonText(third, "packageHash") != jsonText(first, "packageHash") || jsonText(third, "version") != jsonText(first, "version") || jsonInt(third, "revision") != 3 || jsonText(third, "installationId") != jsonText(first, "installationId") {
		t.Fatalf("rollback lost original identity or package %s", third)
	}
	revisions, err := service.ListPackageRevisions(ctx, jsonText(first, "projectKey"), jsonText(first, "pluginId"))
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions %s %v", revisions, err)
	}
	for _, item := range revisions {
		if jsonText(item, "packageHash") == nextHash && jsonText(item, "state") != "rollback" {
			t.Fatalf("rollback state %s", item)
		}
		if jsonText(item, "packageHash") == jsonText(first, "packageHash") && jsonText(item, "state") != "current" {
			t.Fatalf("current state %s", item)
		}
	}
}

func TestDeleteInstallationAuditFailureRollsBackWholeUninstall(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	original, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	project, plugin, id := jsonText(original, "projectKey"), jsonText(original, "pluginId"), jsonText(original, "installationId")
	setting := []byte(`{"projectKey":"` + project + `","pluginId":"` + plugin + `","settingKey":"private","value":"keep","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, 1); err != nil {
		t.Fatal(err)
	}
	service.app.OnRecordCreate(recordsCollection).BindFunc(func(event *core.RecordEvent) error {
		if event.Record.GetString("kind") == KindAudit {
			return errors.New("synthetic uninstall audit failure")
		}
		return event.Next()
	})
	if _, err := service.DeleteInstallation(ctx, project, plugin, id, 1, true); err == nil {
		t.Fatal("uninstall did not include its audit transaction")
	}
	after, err := service.GetInstallation(ctx, project, plugin)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, after, original)
	settings, err := service.GetPrivateSetting(ctx, project, plugin, "private")
	if err != nil || settings == nil {
		t.Fatal("failed uninstall removed private settings", err)
	}
	revisions, err := service.ListPackageRevisions(ctx, project, plugin)
	if err != nil || len(revisions) != 1 {
		t.Fatal("failed uninstall removed package ledger", err)
	}
}

func TestCommitInstallMatchesFrozenOracleAndIsAtomic(t *testing.T) {
	service := newTestService(t)
	service.workspaceID = "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7"
	plan, revision := frozenNativeInstallRequest(t)
	snapshot, err := service.CommitInstall(context.Background(), plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	// The frozen legacy payload stays authoritative except for the two
	// deliberate post-#484/#486 changes: the authority record Id and the
	// native manifest contract, both independently checked below.
	stored, err := findRecord(service.app, KindInstallation, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"), "current")
	if err != nil || stored == nil || jsonText(snapshot, "installationId") != stored.Id {
		t.Fatal("missing PB identity projection")
	}
	var frozenObject map[string]any
	if err := json.Unmarshal(frozenInstallation(t), &frozenObject); err != nil {
		t.Fatal(err)
	}
	nativeizeFrozenManifest(t, frozenObject["manifest"])
	frozenNative, err := json.Marshal(frozenObject)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, snapshot, replaceJSON(t, frozenNative, map[string]any{"installationId": stored.Id}))
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
	plan, revision := frozenNativeInstallRequest(t)
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
	plan, revision := frozenNativeInstallRequest(t)
	first, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	project, plugin := jsonText(first, "projectKey"), jsonText(first, "pluginId")
	id := jsonText(first, "installationId")
	setting := []byte(`{"projectKey":"` + project + `","pluginId":"` + plugin + `","settingKey":"secret","value":"retained","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, jsonInt(first, "revision")); err != nil {
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

func TestBoundSetEnabledRejectsRevisionAndInstallationABA(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	original, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	project, plugin, id := jsonText(original, "projectKey"), jsonText(original, "pluginId"), jsonText(original, "installationId")
	if _, err = service.SetEnabledForInstallation(ctx, project, plugin, false, id, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetEnabledForInstallation(ctx, project, plugin, true, id, 1); err == nil {
		t.Fatal("stale revision enabled installation")
	}
	if _, err = service.DeleteInstallation(ctx, project, plugin, id, 2, false); err != nil {
		t.Fatal(err)
	}
	replacement, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetEnabledForInstallation(ctx, project, plugin, true, id, 1); err == nil {
		t.Fatal("ABA enabled new installation")
	}
	current, err := service.GetInstallation(ctx, project, plugin)
	if err != nil || !reflect.DeepEqual(current, replacement) {
		t.Fatal("failed CAS changed replacement", err)
	}
}
