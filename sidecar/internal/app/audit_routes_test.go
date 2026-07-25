package app

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
)

func TestDecodeHistoryQueryIsStrictAndAppliesDefaults(t *testing.T) {
	request := httptest.NewRequest(
		"GET",
		"/api/vibetable/v1/history/change-sets?collection=notes&scope=row&itemId=record1&action=update&action=restore",
		nil,
	)
	params, err := decodeHistoryQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if params.TableID != "notes" || params.Scope != "row" ||
		params.ItemID == nil || *params.ItemID != "record1" ||
		params.Limit != 50 || params.Offset != 0 ||
		len(params.Actions) != 2 {
		t.Fatalf("decoded query %#v", params)
	}
	defaultScope, err := decodeHistoryQuery(httptest.NewRequest(
		"GET",
		"/api/vibetable/v1/history/change-sets?collection=notes&itemId=record1",
		nil,
	))
	if err != nil || defaultScope.Scope != "row" {
		t.Fatalf("default history scope = %#v err=%v", defaultScope, err)
	}
	for name, rawURL := range map[string]string{
		"unknown":         "/api/vibetable/v1/history/change-sets?collection=notes&sql=x",
		"missing":         "/api/vibetable/v1/history/change-sets",
		"limit":           "/api/vibetable/v1/history/change-sets?collection=notes&limit=x",
		"duplicate scope": "/api/vibetable/v1/history/change-sets?collection=notes&scope=row&scope=table&itemId=r1",
		"duplicate item":  "/api/vibetable/v1/history/change-sets?collection=notes&scope=row&itemId=r1&itemId=r2",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeHistoryQuery(httptest.NewRequest("GET", rawURL, nil))
			var historyErr *audit.Error
			if !errors.As(err, &historyErr) ||
				historyErr.Code != "history.request_invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestAuditErrorHTTPStatusIsExplicit(t *testing.T) {
	for code, want := range map[string]int{
		"history.request_invalid":    400,
		"restore.request_invalid":    400,
		"history.table_not_found":    404,
		"restore_token_unknown":      404,
		"restore_token_expired":      410,
		"schema_drift":               409,
		"restore_conflict":           409,
		"restore_attachment_missing": 422,
		"restore_attachment_corrupt": 422,
		"restore_validation_failed":  422,
		"history.resource_limit":     422,
		"history.storage_failed":     500,
		"restore.capacity_exhausted": 503,
	} {
		if got := auditHTTPStatus(&audit.Error{Code: code}); got != want {
			t.Fatalf("%s status = %d, want %d", code, got, want)
		}
	}
}

func TestDecodeAuditBodyRejectsUnknownAndTrailingFields(t *testing.T) {
	var valid restorePreviewRequest
	if err := decodeAuditBody(strings.NewReader(
		`{"collection":"notes","itemId":"record1","targetRevision":"rev1","scope":"row"}`,
	), &valid); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"collection":"notes","itemId":"record1","targetRevision":"rev1","scope":"row","sql":"x"}`,
		`{"collection":"notes","itemId":"record1","targetRevision":"rev1","scope":"row"} {}`,
	} {
		var input restorePreviewRequest
		err := decodeAuditBody(strings.NewReader(body), &input)
		var historyErr *audit.Error
		if !errors.As(err, &historyErr) ||
			historyErr.Code != "history.request_invalid" {
			t.Fatalf("error = %#v", err)
		}
	}
}
