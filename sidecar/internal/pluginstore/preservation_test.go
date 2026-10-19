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
		AfterRecordID: highest,
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
