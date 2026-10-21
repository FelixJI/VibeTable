package pluginstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type preservationStoredRow struct {
	ID         string `db:"id"`
	Kind       string `db:"kind"`
	ItemKey    string `db:"item_key"`
	Payload    string `db:"payload_json"`
	Seq        int64  `db:"seq"`
	ProjectKey string `db:"project_key"`
	PluginID   string `db:"plugin_id"`
}

func readPreservationRows(t *testing.T, service *Service) []preservationStoredRow {
	t.Helper()
	rows := []preservationStoredRow{}
	err := service.app.DB().NewQuery(
		"SELECT id, kind, item_key, payload_json, seq, project_key, plugin_id FROM {{" + recordsCollection + "}} ORDER BY id",
	).All(&rows)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// expectedPreservationRevision derives the frozen per-record revision
// contract independently from storage: payload CAS revision for
// installation/setting snapshots, stored seq otherwise, empty when absent.
func expectedPreservationRevision(row preservationStoredRow) string {
	switch row.Kind {
	case KindInstallation, KindSetting:
		if revision := jsonInt(json.RawMessage(row.Payload), "revision"); revision > 0 {
			return strconv.FormatInt(revision, 10)
		}
	}
	if row.Seq > 0 {
		return strconv.FormatInt(row.Seq, 10)
	}
	return ""
}

type preservationDocument struct {
	RecordID   string `json:"recordId"`
	Kind       string `json:"kind"`
	ItemKey    string `json:"itemKey"`
	RawPayload string `json:"rawPayload"`
	Seq        int64  `json:"seq"`
}

// TestPreservationSnapshotStreamsAllKindsVerbatimWithBoundedPages seeds a
// real PocketBase store with the frozen legacy four-kind fixture plus
// unknown-key private settings larger than one shard, pages the complete
// collection through the read-only snapshot and reconciles every reassembled
// record against the stored payload_json text byte-for-byte. The whole
// transfer exceeds the 4 MiB product budget while every page stays bounded,
// and the storage rows are identical before and after the export.
func TestPreservationSnapshotStreamsAllKindsVerbatimWithBoundedPages(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if _, err := service.ImportLegacySQLite(ctx, frozenLegacyFixture(t, nil)); err != nil {
		t.Fatalf("import frozen legacy fixture: %v", err)
	}
	installation, err := service.GetInstallation(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil || len(installation) == 0 {
		t.Fatalf("installation after import: %s %v", installation, err)
	}
	installationID := jsonText(installation, "installationId")
	installationRevision := jsonInt(installation, "revision")
	if installationID == "" || installationRevision < 1 {
		t.Fatalf("installation identity = %q revision = %d", installationID, installationRevision)
	}
	// Unknown private setting keys with values far larger than one shard:
	// preservation must keep them verbatim, not through a closed DTO.
	filler := strings.Repeat("保", 220_000)
	for index := 0; index < 8; index++ {
		setting, marshalErr := json.Marshal(map[string]any{
			"projectKey": service.ProjectKey(), "pluginId": "com.example.reader",
			"settingKey": fmt.Sprintf("legacy.unknown.key.%d", index),
			"value":      map[string]any{"legacyPrivate": filler, "未知字段": index},
			"revision":   int64(index + 1),
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := service.SavePrivateSetting(ctx, setting, nil, installationID, installationRevision); err != nil {
			t.Fatalf("seed unknown private setting: %v", err)
		}
	}
	before := readPreservationRows(t, service)
	// The frozen fixture carries a second plugin's installation; the export
	// is scoped to one project/plugin pair, so only those rows may stream.
	inScope := make([]preservationStoredRow, 0, len(before))
	for _, row := range before {
		if row.ProjectKey == service.ProjectKey() && row.PluginID == "com.example.reader" {
			inScope = append(inScope, row)
		}
	}
	expected := make(map[string]preservationStoredRow, len(inScope))
	for _, row := range inScope {
		expected[row.ID] = row
	}
	request := PreservationSnapshotRequest{
		ProjectKey: service.ProjectKey(), PluginID: "com.example.reader",
		InstallationID: installationID, InstallationRevision: installationRevision,
	}
	streams := map[string]*bytes.Buffer{}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	total, pages := 0, 0
	for {
		page, pageErr := service.PreservationSnapshot(ctx, request)
		if pageErr != nil {
			t.Fatalf("page %d: %v", pages, pageErr)
		}
		pages++
		if page.ContractVersion != PreservationContractVersion {
			t.Fatalf("contract version = %q", page.ContractVersion)
		}
		if len(page.Chunk) > PreservationChunkBytes {
			t.Fatalf("page %d exceeded the shard budget: %d", pages, len(page.Chunk))
		}
		total += len(page.Chunk)
		if page.CollectionDone {
			if !page.RecordDone && page.RecordID != "" || len(page.Chunk) != 0 || page.RecordID != "" {
				t.Fatalf("terminal page = %+v", page)
			}
			break
		}
		if page.RecordID == "" || page.Kind == "" || page.ItemKey == "" {
			t.Fatalf("data page without identity = %+v", page)
		}
		row, known := expected[page.RecordID]
		if !known {
			t.Fatalf("page %d streamed unknown record %q", pages, page.RecordID)
		}
		if page.Kind != row.Kind || page.ItemKey != row.ItemKey {
			t.Fatalf("record %q identity drift: %+v vs %+v", page.RecordID, page, row)
		}
		kinds[row.Kind] = true
		stream := streams[page.RecordID]
		if stream == nil {
			stream = &bytes.Buffer{}
			streams[page.RecordID] = stream
		}
		stream.Write(page.Chunk)
		if page.RecordDone {
			var document preservationDocument
			if err := json.Unmarshal(stream.Bytes(), &document); err != nil {
				t.Fatalf("record %q stream = %s: %v", page.RecordID, stream.String(), err)
			}
			if document.RecordID != row.ID || document.Kind != row.Kind ||
				document.ItemKey != row.ItemKey || document.Seq != row.Seq {
				t.Fatalf("record %q document = %+v vs %+v", row.ID, document, row)
			}
			// The frozen contract: rawPayload is the stored payload_json
			// text verbatim (original whitespace and unknown keys intact).
			if document.RawPayload != row.Payload {
				t.Fatalf("record %q rawPayload drifted:\n got %s\nwant %s", row.ID, document.RawPayload, row.Payload)
			}
			if page.Revision != expectedPreservationRevision(row) {
				t.Fatalf("record %q revision = %q, want %q", row.ID, page.Revision, expectedPreservationRevision(row))
			}
			if row.Kind == KindInstallation &&
				page.Revision != strconv.FormatInt(installationRevision, 10) {
				t.Fatalf("installation record revision = %q, want %d", page.Revision, installationRevision)
			}
			seen[row.ID] = true
			delete(streams, page.RecordID)
		}
		request.SnapshotRevision = page.SnapshotRevision
		request.AfterRecordID = page.NextAfterRecordID
		request.RecordOffset = page.NextRecordOffset
		if pages > 10_000 {
			t.Fatal("preservation cursor did not terminate")
		}
	}
	if len(streams) != 0 {
		t.Fatalf("unterminated record streams: %v", streams)
	}
	if len(seen) != len(inScope) {
		missing := make([]string, 0, len(inScope))
		for _, row := range inScope {
			if !seen[row.ID] {
				missing = append(missing, row.ID+"/"+row.Kind+"/"+row.ItemKey)
			}
		}
		t.Fatalf("exported %d of %d records; missing %v", len(seen), len(inScope), missing)
	}
	for _, kind := range []string{KindInstallation, KindRevision, KindSetting, KindAudit} {
		if !kinds[kind] {
			t.Fatalf("kind %q missing from the export", kind)
		}
	}
	if total <= 4<<20 {
		t.Fatalf("multi-page proof transferred only %d bytes", total)
	}
	after := readPreservationRows(t, service)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preservation wrote the original records")
	}
}

// TestPreservationSnapshotFailsClosedOnIdentityCursorAndCancellation keeps
// every degraded input on a stable rejection: wrong workspace, wrong
// installation identity/revision, unknown plugin, malformed or exhausted
// cursors and cancellation never leak records.
func TestPreservationSnapshotFailsClosedOnIdentityCursorAndCancellation(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if _, err := service.ImportLegacySQLite(ctx, frozenLegacyFixture(t, nil)); err != nil {
		t.Fatal(err)
	}
	installation, err := service.GetInstallation(ctx, service.ProjectKey(), "com.example.reader")
	if err != nil {
		t.Fatal(err)
	}
	base := PreservationSnapshotRequest{
		ProjectKey: service.ProjectKey(), PluginID: "com.example.reader",
		InstallationID:       jsonText(installation, "installationId"),
		InstallationRevision: jsonInt(installation, "revision"),
	}
	firstPage, err := service.PreservationSnapshot(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	base.SnapshotRevision = firstPage.SnapshotRevision
	storeErrCode := func(request PreservationSnapshotRequest) string {
		_, err := service.PreservationSnapshot(ctx, request)
		var storeErr *Error
		if !errors.As(err, &storeErr) {
			return fmt.Sprintf("non-store error %v", err)
		}
		return storeErr.Code
	}
	if code := storeErrCode(PreservationSnapshotRequest{
		ProjectKey: "local:00000000000000000000000000000099", PluginID: base.PluginID,
		InstallationID: base.InstallationID, InstallationRevision: base.InstallationRevision,
	}); code != "plugin.request_invalid" {
		t.Fatalf("cross-project code = %s", code)
	}
	if code := storeErrCode(PreservationSnapshotRequest{
		ProjectKey: base.ProjectKey, PluginID: "com.example.missing",
		InstallationID: base.InstallationID, InstallationRevision: 1,
	}); code != "plugin.not_found" {
		t.Fatalf("missing plugin code = %s", code)
	}
	wrongIdentity := base
	wrongIdentity.InstallationID = "retired0record0001"
	if code := storeErrCode(wrongIdentity); code != "plugin.revision_conflict" {
		t.Fatalf("wrong installation code = %s", code)
	}
	wrongRevision := base
	wrongRevision.InstallationRevision++
	if code := storeErrCode(wrongRevision); code != "plugin.revision_conflict" {
		t.Fatalf("wrong revision code = %s", code)
	}
	malformedCursor := base
	malformedCursor.AfterRecordID = "../bad"
	if code := storeErrCode(malformedCursor); code != "plugin.request_invalid" {
		t.Fatalf("malformed cursor code = %s", code)
	}
	badOffset := base
	badOffset.RecordOffset = PreservationChunkBytes + 1
	if code := storeErrCode(badOffset); code != "plugin.request_invalid" {
		t.Fatalf("unaligned offset code = %s", code)
	}
	negativeOffset := base
	negativeOffset.RecordOffset = -PreservationChunkBytes
	if code := storeErrCode(negativeOffset); code != "plugin.request_invalid" {
		t.Fatalf("negative offset code = %s", code)
	}
	// All frozen rows are smaller than one shard, so a first-page fragment
	// offset cannot address any record byte.
	beyondRecord := base
	beyondRecord.RecordOffset = PreservationChunkBytes
	if code := storeErrCode(beyondRecord); code != "plugin.request_invalid" {
		t.Fatalf("beyond-record cursor code = %s", code)
	}
	rows := readPreservationRows(t, service)
	highest := rows[0].ID
	for _, row := range rows {
		if row.ID > highest {
			highest = row.ID
		}
	}
	beyondCollection := base
	beyondCollection.AfterRecordID = highest
	beyondCollection.RecordOffset = PreservationChunkBytes
	if code := storeErrCode(beyondCollection); code != "plugin.request_invalid" {
		t.Fatalf("beyond-collection cursor code = %s", code)
	}
	terminal, err := service.PreservationSnapshot(ctx, PreservationSnapshotRequest{
		ProjectKey: base.ProjectKey, PluginID: base.PluginID,
		InstallationID: base.InstallationID, InstallationRevision: base.InstallationRevision,
		AfterRecordID: highest, SnapshotRevision: base.SnapshotRevision,
	})
	if err != nil || !terminal.CollectionDone || len(terminal.Chunk) != 0 {
		t.Fatalf("terminal page = %+v err = %v", terminal, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.PreservationSnapshot(cancelled, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled page error = %v", err)
	}
	if after := readPreservationRows(t, service); !reflect.DeepEqual(rows, after) {
		t.Fatal("rejected preservation pages wrote records")
	}
}

// preservationPageHookApp pauses only the raw page query, after the real
// installation lookup has completed, including inside a transaction.
type preservationPageHookApp struct {
	core.App
	beforePage func()
}

func (app *preservationPageHookApp) DB() dbx.Builder {
	return preservationPageHookBuilder{Builder: app.App.DB(), beforePage: app.beforePage}
}

func (app *preservationPageHookApp) RunInTransaction(apply func(core.App) error) error {
	return app.App.RunInTransaction(func(tx core.App) error {
		return apply(&preservationPageHookApp{App: tx, beforePage: app.beforePage})
	})
}

type preservationPageHookBuilder struct {
	dbx.Builder
	beforePage func()
}

func (builder preservationPageHookBuilder) NewQuery(statement string) *dbx.Query {
	query := builder.Builder.NewQuery(statement)
	if strings.HasPrefix(statement, "SELECT id, kind, item_key, payload_json, seq FROM ") {
		query.WithExecHook(func(_ *dbx.Query, execute func() error) error {
			builder.beforePage()
			return execute()
		})
	}
	return query
}

func TestPreservationSnapshotDoesNotReportEOFWhenUninstallCleansNextRecord(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	installation, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	request := PreservationSnapshotRequest{
		ProjectKey: service.ProjectKey(), PluginID: jsonText(installation, "pluginId"),
		InstallationID:       jsonText(installation, "installationId"),
		InstallationRevision: jsonInt(installation, "revision"),
	}
	setting := []byte(`{"projectKey":"` + request.ProjectKey + `","pluginId":"` + request.PluginID + `","settingKey":"private","value":{"unknown":"preserve me"},"revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, request.InstallationID, request.InstallationRevision); err != nil {
		t.Fatal(err)
	}
	rows := readPreservationRows(t, service)
	request.AfterRecordID = rows[len(rows)-1].ID
	const settingID = "zzzzzzzzzzzzzzz"
	if request.AfterRecordID >= settingID {
		t.Fatal("fixture needs room for a final private setting")
	}
	if _, err := service.app.DB().NewQuery(
		"UPDATE {{" + recordsCollection + "}} SET id={:id} WHERE kind={:kind}",
	).Bind(dbx.Params{"id": settingID, "kind": KindSetting}).Execute(); err != nil {
		t.Fatal(err)
	}

	initialPage, err := service.PreservationSnapshot(ctx, PreservationSnapshotRequest{
		ProjectKey: request.ProjectKey, PluginID: request.PluginID, InstallationID: request.InstallationID, InstallationRevision: request.InstallationRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.SnapshotRevision = initialPage.SnapshotRevision

	// A separate PocketBase connection commits the actual uninstall while
	// preservation has already validated identity but has not queried its page.
	writerApp := core.NewBaseApp(core.BaseAppConfig{DataDir: service.app.DataDir()})
	if err := writerApp.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writerApp.Settings().Logs.MaxDays = 0
	t.Cleanup(func() { _ = writerApp.ResetBootstrapState() })
	writerApp.OnRecordCreate(recordsCollection).BindFunc(func(event *core.RecordEvent) error {
		if event.Record.GetString("kind") == KindAudit {
			event.Record.Id = "000000000000000"
		}
		return event.Next()
	})
	writer := New(writerApp, service.workspaceID)
	uninstalled := false
	service.app = &preservationPageHookApp{App: service.app, beforePage: func() {
		deleted, deleteErr := writer.DeleteInstallation(ctx, request.ProjectKey, request.PluginID,
			request.InstallationID, request.InstallationRevision, true)
		if deleteErr != nil || !deleted {
			t.Fatalf("interleaved uninstall = %v, error = %v", deleted, deleteErr)
		}
		uninstalled = true
	}}
	page, err := service.PreservationSnapshot(ctx, request)
	if !uninstalled {
		t.Fatal("uninstall did not commit between identity validation and page query")
	}
	if err != nil || page.CollectionDone || page.RecordID != settingID || !page.RecordDone {
		t.Fatalf("page after interleaved cleanup = %+v, error = %v", page, err)
	}
	var document preservationDocument
	if err := json.Unmarshal(page.Chunk, &document); err != nil {
		t.Fatal(err)
	}
	if document.RawPayload != string(setting) {
		t.Fatalf("preserved private setting = %q, want %q", document.RawPayload, setting)
	}
	if got, err := writer.GetPrivateSetting(ctx, request.ProjectKey, request.PluginID, "private"); err != nil || got != nil {
		t.Fatalf("uninstall did not actually clean settings: %s, error = %v", got, err)
	}
	// Revalidation on the next page must reject the retired installation.
	request.SnapshotRevision = page.SnapshotRevision
	request.AfterRecordID = page.NextAfterRecordID
	_, err = service.PreservationSnapshot(ctx, request)
	var storeErr *Error
	if !errors.As(err, &storeErr) || storeErr.Code != "plugin.not_found" {
		t.Fatalf("page after committed uninstall = %v", err)
	}
}

func TestPreservationSnapshotRejectsSettingAndAuditWritesAcrossPages(t *testing.T) {
	for _, operation := range []string{"save-setting", "delete-setting", "audit", "caller-owned-audit", "package-revision"} {
		t.Run(operation, func(t *testing.T) {
			service := newTestService(t)
			ctx := context.Background()
			plan, revision := frozenNativeInstallRequest(t)
			installation, err := service.CommitInstall(ctx, plan, revision)
			if err != nil {
				t.Fatal(err)
			}
			request := PreservationSnapshotRequest{
				ProjectKey: service.ProjectKey(), PluginID: jsonText(installation, "pluginId"),
				InstallationID: jsonText(installation, "installationId"), InstallationRevision: jsonInt(installation, "revision"),
			}
			setting := []byte(`{"projectKey":"` + request.ProjectKey + `","pluginId":"` + request.PluginID + `","settingKey":"private","value":{"before":true},"revision":1}`)
			if _, err := service.SavePrivateSetting(ctx, setting, nil, request.InstallationID, request.InstallationRevision); err != nil {
				t.Fatal(err)
			}
			first, err := service.PreservationSnapshot(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			request.SnapshotRevision = first.SnapshotRevision
			request.AfterRecordID = first.NextAfterRecordID
			request.RecordOffset = first.NextRecordOffset
			event, err := lifecycleAudit(service.now(), installation, "preservation-test")
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "save-setting":
				setting = bytes.Replace(setting, []byte(`"revision":1`), []byte(`"revision":2`), 1)
				_, err = service.SavePrivateSetting(ctx, setting, int64Ptr(1), request.InstallationID, request.InstallationRevision)
			case "delete-setting":
				_, err = service.DeletePrivateSetting(ctx, request.ProjectKey, request.PluginID, "private")
			case "audit":
				_, err = service.RecordAudit(ctx, event)
			case "caller-owned-audit":
				err = service.app.RunInTransaction(func(tx core.App) error {
					_, err := New(tx, service.workspaceID).RecordAuditInTransaction(ctx, event)
					return err
				})
			case "package-revision":
				_, err = service.SavePackageRevision(ctx, revision)
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := service.GetInstallation(ctx, request.ProjectKey, request.PluginID)
			if err != nil || jsonInt(current, "revision") != request.InstallationRevision {
				t.Fatalf("fixture changed the installation revision: %s, %v", current, err)
			}
			_, err = service.PreservationSnapshot(ctx, request)
			var storeErr *Error
			if !errors.As(err, &storeErr) || storeErr.Code != "plugin.preservation_changed" {
				t.Fatalf("mixed snapshot was not rejected: %v", err)
			}
		})
	}
}

func TestPreservationSnapshotRejectsDeleteBeforeTerminalPage(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	installation, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	request := PreservationSnapshotRequest{ProjectKey: service.ProjectKey(), PluginID: jsonText(installation, "pluginId"),
		InstallationID: jsonText(installation, "installationId"), InstallationRevision: jsonInt(installation, "revision")}
	setting := []byte(`{"projectKey":"` + request.ProjectKey + `","pluginId":"` + request.PluginID + `","settingKey":"private","value":1,"revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, request.InstallationID, request.InstallationRevision); err != nil {
		t.Fatal(err)
	}
	// Page through the last row without accepting EOF yet.
	for range readPreservationRows(t, service) {
		page, err := service.PreservationSnapshot(ctx, request)
		if err != nil || page.CollectionDone || !page.RecordDone {
			t.Fatalf("page = %+v, %v", page, err)
		}
		request.SnapshotRevision = page.SnapshotRevision
		request.AfterRecordID, request.RecordOffset = page.NextAfterRecordID, page.NextRecordOffset
	}
	if _, err := service.DeletePrivateSetting(ctx, request.ProjectKey, request.PluginID, "private"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PreservationSnapshot(ctx, request); err == nil {
		t.Fatal("accepted EOF after deletion")
	}
}

func TestPreservationRevisionTracksLegacyImport(t *testing.T) {
	service := newTestService(t)
	var before, after int64
	query := "SELECT revision FROM vibetable_plugin_preservation_revision WHERE singleton = 1"
	if err := service.app.DB().NewQuery(query).Row(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportLegacySQLite(context.Background(), frozenLegacyFixture(t, nil)); err != nil {
		t.Fatal(err)
	}
	if err := service.app.DB().NewQuery(query).Row(&after); err != nil {
		t.Fatal(err)
	}
	if before != 0 || after <= before {
		t.Fatalf("legacy import revision: %d -> %d", before, after)
	}
}

func TestPreservationSnapshotRejectsSettingWriteBetweenFragments(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	plan, revision := frozenNativeInstallRequest(t)
	installation, err := service.CommitInstall(ctx, plan, revision)
	if err != nil {
		t.Fatal(err)
	}
	request := PreservationSnapshotRequest{ProjectKey: service.ProjectKey(), PluginID: jsonText(installation, "pluginId"),
		InstallationID: jsonText(installation, "installationId"), InstallationRevision: jsonInt(installation, "revision")}
	service.app.OnRecordCreate(recordsCollection).BindFunc(func(event *core.RecordEvent) error {
		if event.Record.GetString("kind") == KindSetting {
			event.Record.Id = "000000000000000"
		}
		return event.Next()
	})
	setting := []byte(`{"projectKey":"` + request.ProjectKey + `","pluginId":"` + request.PluginID + `","settingKey":"private","value":"` + strings.Repeat("x", PreservationChunkBytes) + `","revision":1}`)
	if _, err := service.SavePrivateSetting(ctx, setting, nil, request.InstallationID, request.InstallationRevision); err != nil {
		t.Fatal(err)
	}
	page, err := service.PreservationSnapshot(ctx, request)
	if err != nil || page.Kind != KindSetting || page.RecordDone {
		t.Fatalf("fragment = %+v, %v", page, err)
	}
	request.SnapshotRevision = page.SnapshotRevision
	request.AfterRecordID, request.RecordOffset = page.NextAfterRecordID, page.NextRecordOffset
	setting = bytes.Replace(setting, []byte(`"revision":1`), []byte(`"revision":2`), 1)
	if _, err := service.SavePrivateSetting(ctx, setting, int64Ptr(1), request.InstallationID, request.InstallationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PreservationSnapshot(ctx, request); err == nil {
		t.Fatal("accepted fragments from different setting revisions")
	}
}
