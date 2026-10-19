package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func preservationTestService(t *testing.T) *pluginstore.Service {
	t.Helper()
	pb := schemaProductStore(t)
	service := pluginstore.New(pb, "0f8f4a3b-2c1d-4e5f-8091-a2b3c4d5e6f7")
	manifest := map[string]any{
		"$schema":       "vibetable.plugin-manifest.v2",
		"pluginId":      "com.example.preserved",
		"version":       "1.0.0",
		"displayName":   map[string]any{},
		"description":   map[string]any{},
		"compatibility": map[string]any{"pluginApi": "3.x"},
		"permissions":   map[string]any{"data": []any{}, "privateStorage": true},
		"ui": map[string]any{"customViews": []any{map[string]any{
			"viewId": "open", "actionId": "open", "entry": "ui/index.html",
		}}},
		"actions": []any{map[string]any{
			"actionId": "open", "displayName": map[string]any{}, "description": map[string]any{},
			"mode": "local", "risk": "read", "invocation": "manual",
		}},
	}
	snapshot, err := json.Marshal(map[string]any{
		"projectKey": service.ProjectKey(), "pluginId": "com.example.preserved",
		"version": "1.0.0", "packageHash": "sha256:" + strings.Repeat("a", 64),
		"sourceType": "package", "sourceLocation": "host-managed",
		"developmentSourceLocation": nil, "sourceChanged": false,
		"manifest": manifest, "schemas": map[string]any{},
		"status": "disabled", "disabledReason": "disabled_by_user",
		"blockingReasons": []string{}, "revision": 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveInstallation(context.Background(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPluginPreservationSnapshotRegistrationContract(t *testing.T) {
	service := preservationTestService(t)
	registration := pluginPreservationSnapshotRegistration(service)
	if registration.Method != "pluginPreservation.snapshot" {
		t.Fatalf("method = %s", registration.Method)
	}
	valid := fmt.Sprintf(
		`{"projectKey":%q,"pluginId":"com.example.preserved","installationId":"%s","installationRevision":4}`,
		service.ProjectKey(), installationRecordID(t, service, "com.example.preserved"),
	)
	if registration.ValidateParams(json.RawMessage(valid)) != nil {
		t.Fatal("valid request rejected")
	}
	cursor := strings.Replace(valid, `}`, `,"afterRecordId":"","recordOffset":0}`, 1)
	if registration.ValidateParams(json.RawMessage(cursor)) != nil {
		t.Fatal("empty cursor rejected")
	}
	boundary := strings.Replace(valid, `}`, `,"recordOffset":1073741824}`, 1)
	if registration.ValidateParams(json.RawMessage(boundary)) != nil {
		t.Fatal("1 GiB cursor boundary rejected")
	}
	for _, invalid := range []string{
		`{}`,
		`{"projectKey":"local:0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7"}`,
		strings.Replace(valid, `"installationRevision":4`, `"installationRevision":"4"`, 1),
		strings.Replace(valid, `"installationRevision":4`, `"installationRevision":4.5`, 1),
		strings.Replace(valid, `"installationRevision":4`, `"installationRevision":null`, 1),
		valid[:len(valid)-1] + `,"extra":true}`,
		valid[:len(valid)-1] + `,"afterRecordId":"../bad"}`,
		valid[:len(valid)-1] + `,"afterRecordId":null}`,
		valid[:len(valid)-1] + `,"recordOffset":1}`,
		valid[:len(valid)-1] + `,"recordOffset":-262144}`,
		valid[:len(valid)-1] + `,"recordOffset":1073741825}`,
		valid[:len(valid)-1] + `,"recordOffset":null}`,
		`{"projectKey":"local:00000000000000000000000000000099","pluginId":"com.example.preserved","installationId":"aaaaaaaaaaaaaaaa","installationRevision":4}`,
		`[]`,
	} {
		if registration.ValidateParams(json.RawMessage(invalid)) == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestPluginPreservationSnapshotHandlerStreamsAndFailsClosed(t *testing.T) {
	service := preservationTestService(t)
	registration := pluginPreservationSnapshotRegistration(service)
	ctx := context.Background()
	installationID := installationRecordID(t, service, "com.example.preserved")
	valid := fmt.Sprintf(
		`{"projectKey":%q,"pluginId":"com.example.preserved","installationId":"%s","installationRevision":4}`,
		service.ProjectKey(), installationID,
	)
	first, err := registration.Handler(ctx, json.RawMessage(valid))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := first.(pluginstore.PreservationSnapshotPage)
	if !ok {
		t.Fatalf("page type = %T", first)
	}
	if page.ContractVersion != pluginstore.PreservationContractVersion ||
		page.RecordID == "" || page.Kind != pluginstore.KindInstallation ||
		page.ItemKey != "current" || page.Revision != "4" ||
		len(page.Chunk) == 0 || page.Chunk[len(page.Chunk)-1] != '}' {
		t.Fatalf("first page = %+v", page)
	}
	var document struct {
		RawPayload string `json:"rawPayload"`
	}
	if err := json.Unmarshal(page.Chunk, &document); err != nil || document.RawPayload == "" {
		t.Fatalf("first chunk = %s err = %v", page.Chunk, err)
	}
	// Only one record exists: the next page terminates the collection.
	terminal, err := registration.Handler(ctx, json.RawMessage(fmt.Sprintf(
		`{"projectKey":%q,"pluginId":"com.example.preserved","installationId":"%s","installationRevision":4,"afterRecordId":"%s"}`,
		service.ProjectKey(), installationID, page.RecordID,
	)))
	if err != nil {
		t.Fatal(err)
	}
	terminalPage, ok := terminal.(pluginstore.PreservationSnapshotPage)
	if !ok || !terminalPage.CollectionDone || len(terminalPage.Chunk) != 0 {
		t.Fatalf("terminal page = %+v", terminal)
	}
	missing, err := registration.Handler(ctx, json.RawMessage(strings.Replace(
		valid, "com.example.preserved", "com.example.missing", 1,
	)))
	if err == nil {
		t.Fatalf("missing plugin returned %+v", missing)
	}
	var public *productrpc.PublicError
	if !errors.As(err, &public) || public.Code != "plugin_not_found" {
		t.Fatalf("missing plugin error = %v", err)
	}
	stale, err := registration.Handler(ctx, json.RawMessage(strings.Replace(
		valid, `"installationRevision":4`, `"installationRevision":5`, 1,
	)))
	if err == nil {
		t.Fatalf("stale installation returned %+v", stale)
	}
	if !errors.As(err, &public) || public.Code != "plugin.revision_conflict" {
		t.Fatalf("stale installation error = %v", err)
	}
}
