package pluginstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
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
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
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
	snapshot := frozenInstallation(t)
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
	setting := []byte(`{"projectKey":"local:0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7","pluginId":"com.example.reader","settingKey":"token","value":"a","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil); err != nil {
		t.Fatalf("create setting: %v", err)
	}
	if _, err := service.SavePrivateSetting(ctx, setting, nil); err == nil {
		t.Fatal("setting create must fail when the key exists")
	}
	updated := replaceJSON(t, setting, map[string]any{"value": "b", "revision": 2})
	if _, err := service.SavePrivateSetting(ctx, updated, int64Ptr(1)); err != nil {
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
