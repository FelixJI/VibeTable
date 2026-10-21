package pluginstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	// Registers the PocketBase system migrations, mirroring the CLI package's
	// own blank import because core.NewBaseApp does not import them itself.
	_ "github.com/pocketbase/pocketbase/migrations"
	// Registers the sidecar migrations (including the plugin shared-state
	// collections) so the test app's RunAllMigrations creates them.
	_ "github.com/vibetable/vibetable/sidecar/migrations"
	_ "modernc.org/sqlite"
)

// frozenOraclePath points at the migration-frozen Python catalog oracle.
// Go owns the same records now; these tests consume the frozen expected
// payloads so behavior drift fails here first.
const frozenOraclePath = "../../../contracts/v2/plugin-catalog-python-oracle.json"

type oracleCase struct {
	Name   string          `json:"name"`
	Result json.RawMessage `json:"result"`
	Saved  json.RawMessage `json:"saved"`
}

type oracleDocument struct {
	Cases []oracleCase `json:"cases"`
}

func loadFrozenOracleCase(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(frozenOraclePath)
	if err != nil {
		t.Fatalf("read frozen oracle: %v", err)
	}
	var document oracleDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode frozen oracle: %v", err)
	}
	for _, testCase := range document.Cases {
		if testCase.Name == name {
			if len(testCase.Result) == 0 {
				return testCase.Saved
			}
			return testCase.Result
		}
	}
	t.Fatalf("frozen oracle case %q is missing", name)
	return nil
}

const oracleWorkspaceID = "0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7"

func newTestService(t *testing.T) *Service {
	t.Helper()
	// PocketBase keeps short-lived file handles on Windows; use the same
	// retried cleanup as the migrations test helper instead of t.TempDir's
	// single unlink.
	dataDir, err := os.MkdirTemp("", "pluginstore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for attempt := 0; attempt < 5; attempt++ {
			if err := os.RemoveAll(dataDir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	// Plain core app (like PocketBase tests/app.go): the CLI launcher's
	// fire-and-forget modernc dependency check logs from a background
	// goroutine and races the Settings().Logs.MaxDays write below.
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: dataDir})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	// Match PocketBase tests/app.go: retained logs write asynchronously without
	// a join point and can race ResetBootstrapState. This fixture tests plugin
	// records and returned errors, not the auxiliary log database.
	app.Settings().Logs.MaxDays = 0
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("RunAllMigrations(): %v", err)
	}
	return New(app, oracleWorkspaceID)
}

func frozenInstallation(t *testing.T) json.RawMessage {
	t.Helper()
	return loadFrozenOracleCase(t, "install-snapshot")
}

func TestSaveInstallationCreateAndCASConflict(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	snapshot := frozenInstallation(t)

	saved, err := service.SaveInstallation(ctx, snapshot, nil)
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	if jsonInt(saved, "revision") != 1 {
		t.Fatalf("frozen installation revision = %d, want 1", jsonInt(saved, "revision"))
	}

	// Creating again must keep the frozen already-installed semantics.
	if _, err := service.SaveInstallation(ctx, snapshot, nil); err == nil {
		t.Fatal("duplicate create must fail")
	} else {
		public, ok := PublicError(err)
		if !ok || public.Code != "" ||
			public.Message != "plugin revision mismatch: expected None, found 1" {
			t.Fatalf("duplicate create public error = %+v ok=%v", public, ok)
		}
	}

	// The frozen CAS conflict message must be preserved.
	_, err = service.SaveInstallation(ctx, bumpRevision(t, snapshot, 3), int64Ptr(3))
	if err == nil || !strings.Contains(
		err.Error(), "plugin revision mismatch: expected 3, found 1",
	) {
		t.Fatalf("CAS conflict error = %v, want frozen mismatch message", err)
	}

	updated, err := service.SaveInstallation(ctx, bumpRevision(t, snapshot, 2), int64Ptr(1))
	if err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	if jsonInt(updated, "revision") != 2 {
		t.Fatalf("updated revision = %d, want 2", jsonInt(updated, "revision"))
	}
}

func TestListInstallationsOrdersByPluginID(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	first := frozenInstallation(t)
	second := replaceJSON(t, first, map[string]any{"pluginId": "aaa.first"})
	var object map[string]any
	if err := json.Unmarshal(second, &object); err != nil {
		t.Fatal(err)
	}
	object["manifest"].(map[string]any)["pluginId"] = "aaa.first"
	second, _ = json.Marshal(object)

	for _, snapshot := range []json.RawMessage{first, second} {
		if _, err := service.SaveInstallation(ctx, snapshot, nil); err != nil {
			t.Fatalf("create installation: %v", err)
		}
	}
	listed, err := service.ListInstallations(ctx, jsonText(first, "projectKey"))
	if err != nil {
		t.Fatalf("list installations: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d installations, want 2", len(listed))
	}
	if snapshotID(listed[0]) != "aaa.first" {
		t.Fatalf("first listed plugin = %q, want aaa.first", snapshotID(listed[0]))
	}
}

func TestSetEnabledLifecycleMatchesFrozenOracle(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	// The frozen oracle snapshot itself is a legacy v1 Worker payload that
	// #486 keeps permanently blocked; the enable/disable lifecycle runs on
	// the native form a current Host installs.
	snapshot := nativeFrozenInstallation(t)
	if _, err := service.SaveInstallation(ctx, snapshot, nil); err != nil {
		t.Fatalf("create installation: %v", err)
	}
	if _, err := service.RecordAudit(ctx, installAuditFromOracle(t)); err != nil {
		t.Fatalf("record install audit: %v", err)
	}

	enabled, err := service.SetEnabled(ctx, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"), true)
	if err != nil {
		t.Fatalf("setEnabled(true): %v", err)
	}
	if stringField(enabled, "status") != "enabled" ||
		rawHasNonNull(enabled, "disabledReason") ||
		jsonInt(enabled, "revision") != 2 {
		t.Fatalf("enabled snapshot = %s", enabled)
	}
	disabled, err := service.SetEnabled(ctx, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"), false)
	if err != nil {
		t.Fatalf("setEnabled(false): %v", err)
	}
	if stringField(disabled, "status") != "disabled" ||
		stringField(disabled, "disabledReason") != "disabled_by_user" ||
		jsonInt(disabled, "revision") != 3 {
		t.Fatalf("disabled snapshot = %s", disabled)
	}

	audit, err := service.ListAudit(ctx, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"))
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(audit) != 3 {
		t.Fatalf("audit length = %d, want install/enable/disable", len(audit))
	}
	wantTypes := []string{"install", "enable", "disable"}
	for index, want := range wantTypes {
		if stringField(audit[index], "eventType") != want {
			t.Fatalf("audit[%d].eventType = %q, want %q", index, stringField(audit[index], "eventType"), want)
		}
		if stringField(audit[index], "outcome") != "succeeded" ||
			stringField(audit[index], "actor") != "local-user" {
			t.Fatalf("audit[%d] outcome/actor drift: %s", index, audit[index])
		}
	}
}

func TestSetEnabledBlockedFailClosed(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	snapshot := replaceJSON(t, frozenInstallation(t), map[string]any{
		"blockingReasons": []string{"plugin_signature_invalid"},
	})
	if _, err := service.SaveInstallation(ctx, snapshot, nil); err != nil {
		t.Fatalf("create installation: %v", err)
	}
	_, err := service.SetEnabled(ctx, jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId"), true)
	if err == nil || !strings.Contains(err.Error(), "plugin_blocked") {
		t.Fatalf("blocked enable error = %v, want plugin_blocked", err)
	}
	public, ok := PublicError(err)
	if !ok || public.Code != PublicCodeBlocked ||
		public.Message != "plugin has blocking reasons" {
		t.Fatalf("blocked public error = %+v ok=%v", public, ok)
	}
	// The derived Worker-retirement reason joins the stored reasons so the
	// public diagnosis stays explicit about both blockers.
	reasons, _ := public.Details["blockingReasons"].([]string)
	if len(reasons) != 2 || reasons[0] != "plugin_signature_invalid" || reasons[1] != WorkerRetiredReason {
		t.Fatalf("blocked public details = %#v", public.Details)
	}
}

// nativeFrozenInstallation returns the frozen oracle install snapshot with
// its manifest converted to the native post-#484 contract.
func nativeFrozenInstallation(t *testing.T) json.RawMessage {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(frozenInstallation(t), &object); err != nil {
		t.Fatal(err)
	}
	nativeizeFrozenManifest(t, object["manifest"])
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestLegacyWorkerInstallationProjectedBlockedAndNeverEnabled covers the
// #486 read projection and enable boundary for stored schema v1 / API
// 1.x-2.x installations: list/read views surface the retirement reason,
// enables are rejected fail-closed with a stable diagnosis, disable keeps
// working, and the stored identity/settings/audit payload is never
// rewritten.
func TestLegacyWorkerInstallationProjectedBlockedAndNeverEnabled(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	snapshot := frozenInstallation(t)
	if _, err := service.SaveInstallation(ctx, snapshot, nil); err != nil {
		t.Fatalf("legacy raw snapshot must stay storable: %v", err)
	}
	project, plugin := jsonText(snapshot, "projectKey"), jsonText(snapshot, "pluginId")
	containsReason := func(raw json.RawMessage) bool {
		for _, reason := range jsonStrings(raw, "blockingReasons") {
			if reason == WorkerRetiredReason {
				return true
			}
		}
		return false
	}
	read, err := service.GetInstallation(ctx, project, plugin)
	if err != nil || !containsReason(read) {
		t.Fatalf("get_installation projection = %s err=%v", read, err)
	}
	listed, err := service.ListInstallations(ctx, project)
	if err != nil || len(listed) != 1 || !containsReason(listed[0]) {
		t.Fatalf("list_installations projection = %s err=%v", listed, err)
	}
	stored, err := findRecord(service.app, KindInstallation, project, plugin, "current")
	if err != nil || stored == nil {
		t.Fatalf("stored installation missing: %v", err)
	}
	storedJSON, err := json.Marshal(stored.GetRaw("payload_json"))
	if err != nil || bytes.Contains(storedJSON, []byte(WorkerRetiredReason)) {
		t.Fatalf("projection rewrote the stored payload: %s err=%v", storedJSON, err)
	}
	// Enables are rejected fail-closed with the stable diagnosis; the
	// stored reasons alone stay untouched.
	if _, err := service.SetEnabled(ctx, project, plugin, true); err == nil {
		t.Fatal("legacy Worker installation was enabled")
	} else {
		public, ok := PublicError(err)
		if !ok || public.Code != PublicCodeBlocked {
			t.Fatalf("legacy enable error = %v", err)
		}
		reasons, _ := public.Details["blockingReasons"].([]string)
		if len(reasons) != 1 || reasons[0] != WorkerRetiredReason {
			t.Fatalf("legacy enable details = %#v", public.Details)
		}
	}
	// Disable remains available and audited; re-enable stays blocked.
	disabled, err := service.SetEnabled(ctx, project, plugin, false)
	if err != nil || stringField(disabled, "status") != "disabled" {
		t.Fatalf("legacy disable = %s err=%v", disabled, err)
	}
	if _, err := service.SetEnabled(ctx, project, plugin, true); err == nil {
		t.Fatal("legacy Worker installation was re-enabled after disable")
	}
	storedAfter, err := findRecord(service.app, KindInstallation, project, plugin, "current")
	if err != nil || storedAfter == nil {
		t.Fatalf("stored installation missing after lifecycle: %v", err)
	}
	storedAfterJSON, err := json.Marshal(storedAfter.GetRaw("payload_json"))
	if err != nil || bytes.Contains(storedAfterJSON, []byte(WorkerRetiredReason)) {
		t.Fatalf("lifecycle persisted the derived reason: %s err=%v", storedAfterJSON, err)
	}
	audit, err := service.ListAudit(ctx, project, plugin)
	if err != nil || len(audit) != 1 || stringField(audit[0], "eventType") != "disable" {
		t.Fatalf("legacy disable audit = %s err=%v", audit, err)
	}
}

// TestPublicErrorWireMatchesFrozenOracle consumes the frozen error cases
// item by item: internal Go codes must never leak onto the public wire and
// the public code/message pairs must equal the frozen oracle values.
func TestPublicErrorWireMatchesFrozenOracle(t *testing.T) {
	type frozenError struct {
		Type         string `json:"type"`
		Message      string `json:"message"`
		RPCErrorData *struct {
			Code string `json:"code"`
		} `json:"rpcErrorData"`
	}
	document := struct {
		Cases []struct {
			Name  string      `json:"name"`
			Error frozenError `json:"error"`
		} `json:"cases"`
	}{}
	raw, err := os.ReadFile(frozenOraclePath)
	if err != nil {
		t.Fatalf("read frozen oracle: %v", err)
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode frozen oracle: %v", err)
	}
	internal := map[string]error{
		"install-duplicate-error": &Error{
			Code: "plugin.already_installed", Message: "internal spelling",
		},
		"set-enabled-missing-error": &Error{
			Code: "plugin.not_found", Message: "internal spelling",
		},
		"set-enabled-blocked-error": &Error{
			Code: "plugin_blocked", Message: "internal spelling",
		},
		"store-cas-conflict-error": &Error{
			Code:    "plugin.revision_conflict",
			Message: "plugin revision mismatch: expected 0, found 1",
		},
	}
	for _, testCase := range document.Cases {
		sample, mapped := internal[testCase.Name]
		if !mapped {
			continue
		}
		public, ok := PublicError(sample)
		if !ok {
			t.Fatalf("%s has no public mapping", testCase.Name)
		}
		wantCode := ""
		if testCase.Error.RPCErrorData != nil {
			wantCode = testCase.Error.RPCErrorData.Code
		}
		if public.Code != wantCode || public.Message != testCase.Error.Message {
			t.Fatalf(
				"%s public error = {code:%q message:%q}, want {code:%q message:%q}",
				testCase.Name, public.Code, public.Message, wantCode, testCase.Error.Message,
			)
		}
	}
	// Unmapped internal errors must fail closed, never invent a public code.
	if _, ok := PublicError(storageError()); ok {
		t.Fatal("storage error must not map to a public code")
	}
	if _, ok := PublicError(errors.New("boom")); ok {
		t.Fatal("foreign error must not map to a public code")
	}
}

func TestPrivateSettingCAS(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	installation, err := service.SaveInstallation(ctx, frozenInstallation(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := jsonText(installation, "installationId")
	setting := []byte(`{"projectKey":"local:0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7","pluginId":"com.example.reader","settingKey":"token","value":"a","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, 1); err != nil {
		t.Fatalf("create setting: %v", err)
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil, id, 1); err == nil {
		t.Fatal("setting create must fail when the key exists")
	}
	updated := replaceJSON(t, setting, map[string]any{"value": "b", "revision": 2})
	if _, err := service.SavePrivateSetting(ctx, updated, int64Ptr(1), id, 1); err != nil {
		t.Fatalf("setting CAS update: %v", err)
	}
	loaded, err := service.GetPrivateSetting(ctx, service.ProjectKey(), "com.example.reader", "token")
	if err != nil || stringField(loaded, "value") != "b" {
		t.Fatalf("loaded setting = %s err=%v", loaded, err)
	}
}

func TestPackageRevisionPathReference(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	revision := replaceJSON(t, loadFrozenOracleCase(t, "package-revision-persisted"), map[string]any{"localPath": "C:/cache/x.vtplugin"})
	if _, err := service.SavePackageRevision(ctx, revision); err != nil {
		t.Fatalf("save package revision: %v", err)
	}
	referenced, err := service.IsPackagePathReferenced(ctx, "C:/cache/x.vtplugin")
	if err != nil || !referenced {
		t.Fatalf("referenced = %v err = %v", referenced, err)
	}
	referenced, err = service.IsPackagePathReferenced(ctx, "C:/cache/other.vtplugin")
	if err != nil || referenced {
		t.Fatalf("other path referenced = %v err = %v", referenced, err)
	}
	removed, err := service.DeletePackageRevisions(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d err = %v", removed, err)
	}
}

type frozenRow struct {
	Kind       string          `json:"kind"`
	ProjectKey string          `json:"projectKey"`
	PluginID   string          `json:"pluginId"`
	ItemKey    string          `json:"itemKey"`
	Payload    json.RawMessage `json:"payload"`
}

func frozenFourKinds(t *testing.T) []frozenRow {
	t.Helper()
	raw, err := os.ReadFile(frozenOraclePath)
	if err != nil {
		t.Fatalf("read frozen oracle: %v", err)
	}
	var document struct {
		Cases []struct {
			Name string          `json:"name"`
			Rows []frozenRow     `json:"rows"`
			Raw  json.RawMessage `json:"-"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode frozen oracle: %v", err)
	}
	for _, testCase := range document.Cases {
		if testCase.Name == "persisted-four-kinds" {
			if len(testCase.Rows) == 0 {
				t.Fatal("frozen four-kinds rows are missing")
			}
			return testCase.Rows
		}
	}
	t.Fatal("frozen persisted-four-kinds case is missing")
	return nil
}

func frozenLegacyFixture(t *testing.T, mutate func(rows []legacyRow) []legacyRow) string {
	t.Helper()
	rows := make([]legacyRow, 0, 4)
	for _, frozen := range frozenFourKinds(t) {
		rows = append(rows, legacyRow{
			Kind: frozen.Kind, Project: frozen.ProjectKey, Plugin: frozen.PluginID,
			Item: frozen.ItemKey, Payload: string(frozen.Payload),
		})
	}
	if mutate != nil {
		rows = mutate(rows)
	}
	return writeLegacyFixture(t, rows...)
}

func frozenInstallationRecord(t *testing.T) json.RawMessage {
	t.Helper()
	for _, frozen := range frozenFourKinds(t) {
		if frozen.Kind == KindInstallation && frozen.PluginID == "com.example.reader" {
			return frozen.Payload
		}
	}
	t.Fatal("frozen reader installation row is missing")
	return nil
}

func TestImportLegacySQLiteSingleTransactionMarkerAndIdempotency(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	source := frozenLegacyFixture(t, nil)
	frozen := frozenFourKinds(t)
	wantCount := int64(len(frozen))

	result, err := service.ImportLegacySQLite(ctx, source)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Status != ImportStatusImported || result.RecordCount != wantCount {
		t.Fatalf("result = %+v, want %d imported records", result, wantCount)
	}

	// The marker is anchored to the workspace UUID + legacy schema version,
	// not the file path: a relocated (or changed) legacy source can never
	// overwrite the completed import.
	relocated := filepath.Join(t.TempDir(), "relocated", "plugins.db")
	if err := os.MkdirAll(filepath.Dir(relocated), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relocated, mustRead(t, source), 0o600); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ImportLegacySQLite(ctx, relocated)
	if err != nil {
		t.Fatalf("relocated replay import: %v", err)
	}
	if replayed.Status != ImportStatusAlreadyComplete || replayed.RecordCount != wantCount {
		t.Fatalf("replay result = %+v", replayed)
	}
	wantRevision := jsonInt(frozenInstallationRecord(t), "revision")
	loaded, err := service.GetInstallation(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || jsonInt(loaded, "revision") != wantRevision {
		t.Fatalf("post-replay installation revision = %d, want frozen %d (err=%v)",
			jsonInt(loaded, "revision"), wantRevision, err)
	}

	// Audit order follows the frozen insertion sequence.
	audit, err := service.ListAudit(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	wantFirst := "install"
	if stringField(audit[0], "eventType") != wantFirst {
		t.Fatalf("audit[0].eventType = %q, want %q", stringField(audit[0], "eventType"), wantFirst)
	}

	// The source file is untouched (read-only) and still present.
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("legacy source removed: %v", err)
	}
}

func TestImportLegacySQLiteChangedSourceCannotOverwrite(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	source := frozenLegacyFixture(t, nil)
	if _, err := service.ImportLegacySQLite(ctx, source); err != nil {
		t.Fatalf("import: %v", err)
	}
	changed := frozenLegacyFixture(t, func(rows []legacyRow) []legacyRow {
		for index := range rows {
			if rows[index].Kind == KindInstallation {
				rows[index].Payload = string(replaceJSON(t, json.RawMessage(rows[index].Payload), map[string]any{"revision": 99}))
			}
		}
		return rows
	})
	replayed, err := service.ImportLegacySQLite(ctx, changed)
	if err != nil {
		t.Fatalf("changed replay import: %v", err)
	}
	if replayed.Status != ImportStatusAlreadyComplete {
		t.Fatalf("changed replay result = %+v", replayed)
	}
	wantRevision := jsonInt(frozenInstallationRecord(t), "revision")
	loaded, err := service.GetInstallation(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || jsonInt(loaded, "revision") != wantRevision {
		t.Fatalf("overwritten installation revision = %d, want frozen %d (err=%v)",
			jsonInt(loaded, "revision"), wantRevision, err)
	}
}

func TestImportLegacySQLiteConflictWithoutMarkerFailsClosed(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if _, err := service.SaveInstallation(ctx, frozenInstallation(t), nil); err != nil {
		t.Fatalf("seed target installation: %v", err)
	}
	source := frozenLegacyFixture(t, nil)
	if _, err := service.ImportLegacySQLite(ctx, source); err == nil ||
		!strings.Contains(err.Error(), "plugin.import_conflict") {
		t.Fatalf("conflict error = %v, want plugin.import_conflict", err)
	}
	// Failure must not leave a marker or partial imports behind.
	marker, err := service.findMarker(service.ImportMarkerKey())
	if err != nil || marker != nil {
		t.Fatalf("partial marker = %+v err = %v", marker, err)
	}
	revisions, err := service.ListPackageRevisions(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || len(revisions) != 0 {
		t.Fatalf("partial revisions leaked: %d err = %v", len(revisions), err)
	}
}

func TestImportLegacySQLiteRejectsForeignWorkspaceAndIdentityMismatch(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()

	foreign := frozenLegacyFixture(t, func(rows []legacyRow) []legacyRow {
		for index := range rows {
			rows[index].Project = "local:00000000-0000-4000-8000-000000000999"
		}
		return rows
	})
	if _, err := service.ImportLegacySQLite(ctx, foreign); err == nil ||
		!strings.Contains(err.Error(), "plugin.import_source_invalid") {
		t.Fatalf("foreign workspace error = %v", err)
	}

	mismatched := frozenLegacyFixture(t, func(rows []legacyRow) []legacyRow {
		for index := range rows {
			if rows[index].Kind == KindAudit {
				rows[index].Item = fmt.Sprintf("not-the-event-id-%d", index)
			}
		}
		return rows
	})
	if _, err := service.ImportLegacySQLite(ctx, mismatched); err == nil ||
		!strings.Contains(err.Error(), "plugin.import_source_invalid") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	loaded, err := service.GetInstallation(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || loaded != nil {
		t.Fatalf("rejected import leaked rows: %s err = %v", loaded, err)
	}
}

func TestImportLegacySQLiteInvalidSourceFails(t *testing.T) {
	service := newTestService(t)
	path := filepath.Join(t.TempDir(), "missing.db")
	if result, err := service.ImportLegacySQLite(context.Background(), path); err != nil || result.RecordCount != 0 {
		t.Fatalf("missing source must complete a new workspace import: %+v %v", result, err)
	}
	service = newTestService(t)
	other := filepath.Join(t.TempDir(), "not-plugins.db")
	if err := os.WriteFile(other, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportLegacySQLite(context.Background(), other); err == nil {
		t.Fatal("invalid source must fail")
	}
}

// ---- fixtures ----

type legacyRow struct {
	Kind, Project, Plugin, Item, Payload string
}

func writeLegacyFixture(t *testing.T, rows ...legacyRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(
		"CREATE TABLE plugin_records (kind TEXT NOT NULL, project_key TEXT NOT NULL, " +
			"plugin_id TEXT NOT NULL, item_key TEXT NOT NULL, payload TEXT NOT NULL, " +
			"PRIMARY KEY (kind, project_key, plugin_id, item_key))",
	); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := db.Exec(
			"INSERT INTO plugin_records(kind, project_key, plugin_id, item_key, payload) "+
				"VALUES (?, ?, ?, ?, ?)",
			row.Kind, row.Project, row.Plugin, row.Item, row.Payload,
		); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func installAuditFromOracle(t *testing.T) json.RawMessage {
	t.Helper()
	document := struct {
		Cases []struct {
			Name   string            `json:"name"`
			Reader []json.RawMessage `json:"reader"`
		} `json:"cases"`
	}{}
	raw, err := os.ReadFile(frozenOraclePath)
	if err != nil {
		t.Fatalf("read frozen oracle: %v", err)
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode frozen oracle: %v", err)
	}
	for _, testCase := range document.Cases {
		if testCase.Name == "list-audit" {
			if len(testCase.Reader) == 0 || stringField(testCase.Reader[0], "eventType") != "install" {
				t.Fatal("frozen install audit event is missing")
			}
			return testCase.Reader[0]
		}
	}
	t.Fatal("frozen list-audit case is missing")
	return nil
}

func bumpRevision(t *testing.T, raw json.RawMessage, revision int64) json.RawMessage {
	t.Helper()
	return replaceJSON(t, raw, map[string]any{"revision": revision})
}

func replaceJSON(t *testing.T, raw json.RawMessage, changes map[string]any) json.RawMessage {
	t.Helper()
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for key, value := range changes {
		object[key] = value
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return encoded
}

func stringField(raw json.RawMessage, key string) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

func rawHasNonNull(raw json.RawMessage, key string) bool {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return true
	}
	value, exists := object[key]
	return exists && value != nil
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func int64Ptr(value int64) *int64 { return &value }

func TestCallerOwnedAuditRequiresTransactionAndPreservesRollback(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	event := installAuditFromOracle(t)
	project, plugin, _, err := validateAuditPayload(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAuditInTransaction(ctx, event); err == nil {
		t.Fatal("caller-owned audit accepted a nontransactional app")
	}
	notified := 0
	rollback := errors.New("synthetic outer rollback")
	err = service.app.RunInTransaction(func(tx core.App) error {
		owned := New(tx, service.workspaceID, func() { notified++ })
		if _, err := owned.RecordAuditInTransaction(ctx, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	audits, err := service.ListAudit(ctx, project, plugin)
	if err != nil || len(audits) != 0 || notified != 0 {
		t.Fatal("caller-owned audit escaped rollback or emitted early", err)
	}
	err = service.app.RunInTransaction(func(tx core.App) error {
		owned := New(tx, service.workspaceID)
		if _, err := owned.RecordAuditInTransaction(ctx, event); err != nil {
			return err
		}
		_, err := owned.RecordAuditInTransaction(ctx, event)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	audits, err = service.ListAudit(ctx, project, plugin)
	if err != nil || len(audits) != 1 {
		t.Fatal("caller-owned audit lost immutable idempotency", err)
	}
}

func TestNativeManifestRequiresPageForEveryAction(t *testing.T) {
	var snapshot map[string]any
	if err := json.Unmarshal(loadFrozenOracleCase(t, "install-snapshot"), &snapshot); err != nil {
		t.Fatal(err)
	}
	manifest := snapshot["manifest"].(map[string]any)
	manifest["$schema"] = "vibetable.plugin-manifest.v2"
	manifest["compatibility"].(map[string]any)["pluginApi"] = "3.x"
	actions := manifest["actions"].([]any)
	for _, value := range actions {
		delete(value.(map[string]any), "workerEntry")
	}
	manifest["ui"] = map[string]any{"customViews": []any{}}
	if validateManifest(manifest, manifest["pluginId"].(string), manifest["version"].(string)) == nil {
		t.Fatal("native action without page accepted")
	}
	views := []any{}
	for _, value := range actions {
		action := value.(map[string]any)
		views = append(views, map[string]any{"viewId": action["actionId"], "actionId": action["actionId"], "entry": "ui/index.html"})
	}
	manifest["ui"] = map[string]any{"customViews": views}
	if err := validateManifest(manifest, manifest["pluginId"].(string), manifest["version"].(string)); err != nil {
		t.Fatal(err)
	}
	views[0].(map[string]any)["actionId"] = "other-action"
	if validateManifest(manifest, manifest["pluginId"].(string), manifest["version"].(string)) == nil {
		t.Fatal("unmatched native page accepted")
	}
}

// Reject materialization of unrelated history while observing exact record lookup.
type auditLookupApp struct {
	core.App
	lists   int
	lookups int
}

func (app *auditLookupApp) FindRecordsByFilter(collection any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
	app.lists++
	return nil, errors.New("unbounded audit enumeration forbidden")
}
func (app *auditLookupApp) FindFirstRecordByFilter(collection any, filter string, params ...dbx.Params) (*core.Record, error) {
	app.lookups++
	return app.App.FindFirstRecordByFilter(collection, filter, params...)
}
func TestGetAuditUsesExactBoundedLookup(t *testing.T) {
	service := newTestService(t)
	raw := installAuditFromOracle(t)
	if _, err := service.RecordAudit(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	_ = json.Unmarshal(raw, &event)
	project, plugin, id := event["projectKey"].(string), event["pluginId"].(string), event["eventId"].(string)
	unrelated := map[string]any{}
	_ = json.Unmarshal(raw, &unrelated)
	unrelated["eventId"] = "unrelated-history"
	unrelated["details"] = map[string]any{"plan": strings.Repeat("large prior plan", 10000)}
	unrelatedRaw, _ := json.Marshal(unrelated)
	if _, err := service.RecordAudit(context.Background(), unrelatedRaw); err != nil {
		t.Fatal(err)
	}
	unrelatedRecord, _ := findRecord(service.app, KindAudit, project, plugin, "unrelated-history")
	unrelatedRecord.Set("payload_json", map[string]any{"corrupt": true})
	if err := service.app.Save(unrelatedRecord); err != nil {
		t.Fatal(err)
	}
	spy := &auditLookupApp{App: service.app}
	lookup := New(spy, oracleWorkspaceID)
	actual, err := lookup.GetAudit(context.Background(), project, plugin, id)
	var actualValue, expectedValue any
	_ = json.Unmarshal(actual, &actualValue)
	_ = json.Unmarshal(raw, &expectedValue)
	if err != nil || !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatal("exact audit lookup failed", err)
	}
	if spy.lists != 0 || spy.lookups != 1 {
		t.Fatal("lookup enumerated history", spy.lists, spy.lookups)
	}
	if raw, err := lookup.GetAudit(context.Background(), project, plugin, "missing"); err != nil || raw != nil {
		t.Fatal("missing event semantics", err)
	}
	if raw, err := lookup.GetAudit(context.Background(), project, "other-plugin", id); err != nil || raw != nil {
		t.Fatal("cross-plugin audit leak", err)
	}
	if _, err := lookup.GetAudit(context.Background(), "local:foreign", plugin, id); err == nil {
		t.Fatal("foreign project accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	before := spy.lookups
	if _, err := lookup.GetAudit(cancelled, project, plugin, id); !errors.Is(err, context.Canceled) || spy.lookups != before {
		t.Fatal("cancelled lookup queried store", err)
	}
	stored, err := findRecord(service.app, KindAudit, project, plugin, id)
	if err != nil || stored == nil {
		t.Fatal("missing fixture", err)
	}
	stored.Set("payload_json", map[string]any{"corrupt": true})
	if err := service.app.Save(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.GetAudit(context.Background(), project, plugin, id); err == nil {
		t.Fatal("corrupt audit accepted")
	}

}

func presentationTestManifest() map[string]any {
	return map[string]any{
		"$schema":       "vibetable.plugin-manifest.v2",
		"pluginId":      "com.example.presentation",
		"version":       "1.0.0",
		"displayName":   map[string]any{"zh-CN": "示例"},
		"description":   map[string]any{},
		"compatibility": map[string]any{"pluginApi": "3.x"},
		"permissions":   map[string]any{},
		"ui":            map[string]any{"customViews": []any{}},
		"actions": []any{map[string]any{
			"actionId": "open", "mode": "local", "risk": "read", "invocation": "manual",
		}},
	}
}

func TestPresentationActionsValidateAndBindClosedSet(t *testing.T) {
	build := func(presentation, entry string, extra map[string]any, views []any) map[string]any {
		action := map[string]any{
			"actionId": "open", "mode": "local", "risk": "read", "invocation": "manual",
		}
		for key, value := range extra {
			action[key] = value
		}
		if presentation != "" {
			action["presentation"] = presentation
			action["entry"] = entry
		}
		return map[string]any{
			"$schema":       "vibetable.plugin-manifest.v2",
			"pluginId":      "com.example.presentation",
			"version":       "1.0.0",
			"displayName":   map[string]any{"zh-CN": "示例"},
			"description":   map[string]any{},
			"compatibility": map[string]any{"pluginApi": "3.x"},
			"permissions":   map[string]any{},
			"ui":            map[string]any{"customViews": views},
			"actions":       []any{action},
		}
	}
	check := func(name string, manifest map[string]any, wantValid bool) {
		t.Helper()
		err := validateManifest(manifest, manifest["pluginId"].(string), manifest["version"].(string))
		if wantValid && err != nil {
			t.Fatal(name, " rejected:", err)
		}
		if !wantValid && err == nil {
			t.Fatal(name, " accepted")
		}
	}

	// Old stacks required a custom view for every v2 action; the new shapes
	// install with the entry declared on the action itself.
	check("application", build("application", "ui/index.html", nil, nil), true)
	check("headless", build("headless", "ui/app.js", nil, nil), true)
	check("application-upper-html", build("application", "ui/ENTRY.HTML", nil, nil), true)
	check("headless-module", build("headless", "ui/ENTRY.mjs", nil, nil), true)
	check("headless-upper-module", build("headless", "ui/ENTRY.MJS", nil, nil), true)
	check("application-js", build("application", "ui/app.js", nil, nil), false)
	check("application-htm", build("application", "ui/index.htm", nil, nil), false)
	check("headless-html", build("headless", "ui/index.html", nil, nil), false)
	check("headless-no-suffix", build("headless", "ui/ENTRY", nil, nil), false)
	check("reserved-application", build("application", "__vibetable_host__/headless.html", nil, nil), false)
	check("reserved-application-nested", build("application", "__vibetable_host__/nested/entry.HTML", nil, nil), false)
	check("reserved-headless", build("headless", "__vibetable_host__/headless.js", nil, nil), false)
	check("reserved-headless-nested", build("headless", "__vibetable_host__/nested/entry.MJS", nil, nil), false)

	view := []any{map[string]any{"viewId": "v", "actionId": "open", "entry": "ui/index.html"}}
	check("surface-with-entry", build("surface", "ui/index.html", nil, view), false)
	check("missing-entry", build("application", "", nil, nil), false)
	check("unknown-presentation", build("widget", "ui/index.html", nil, nil), false)
	check("legacy-schema-channel", build("headless", "ui/app.js", map[string]any{"inputSchema": "schemas/input.json"}, nil), false)
	check("legacy-view-channel", build("application", "ui/index.html", nil, view), false)
	// A surface action still needs its legacy view binding.
	check("surface-without-view", build("surface", "", nil, nil), false)
	check("surface-with-view", build("", "", nil, view), true)
	for _, key := range []string{"entry", "html", "script", "style"} {
		reservedView := map[string]any{"viewId": "v", "actionId": "open", "entry": "ui/index.html"}
		reservedView[key] = "__vibetable_host__/nested/resource.html"
		check("reserved-custom-"+key, build("", "", nil, []any{reservedView}), false)
	}

	// v1 manifests keep rejecting the new shapes entirely.
	v1 := build("application", "ui/index.html", map[string]any{"workerEntry": "dist/worker.js"}, nil)
	v1["$schema"] = "vibetable.plugin-manifest.v1"
	v1["compatibility"] = map[string]any{"pluginApi": "1.x"}
	check("v1-presentation", v1, false)
}
