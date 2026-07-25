package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/metadata"
)

func TestDecodeMetadataBodyIsStrictAndBounded(t *testing.T) {
	var body metadataUpsertBody
	if err := decodeMetadataBody(strings.NewReader(
		`{"logicalId":"layout","payload":{"a":1},`+
			`"expectedRevision":"","idempotencyKey":"create-layout"}`,
	), &body); err != nil {
		t.Fatal(err)
	}
	if body.LogicalID != "layout" ||
		string(body.Payload) != `{"a":1}` {
		t.Fatalf("body = %#v", body)
	}
	for name, raw := range map[string]string{
		"empty": "",
		"unknown": `{"logicalId":"x","payload":{},` +
			`"expectedRevision":"","idempotencyKey":"x","collection":"_superusers"}`,
		"trailing": `{"logicalId":"x","payload":{},` +
			`"expectedRevision":"","idempotencyKey":"x"} {}`,
		"oversized": `"` + strings.Repeat(
			"x", maxMetadataRequestBytes,
		) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			var target metadataUpsertBody
			err := decodeMetadataBody(
				strings.NewReader(raw), &target,
			)
			var productErr *metadata.Error
			if !errors.As(err, &productErr) ||
				productErr.Code != "metadata.request.invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestMetadataHTTPStatusUsesSafeProductProjection(t *testing.T) {
	for code, want := range map[string]int{
		"metadata.namespace.invalid":    http.StatusBadRequest,
		"metadata.dashboard.invalid":    http.StatusBadRequest,
		"metadata.not_found":            http.StatusNotFound,
		"metadata.revision_conflict":    http.StatusConflict,
		"metadata.idempotency_conflict": http.StatusConflict,
		"metadata.storage.failed":       http.StatusInternalServerError,
		"metadata.unclassified.product": http.StatusUnprocessableEntity,
	} {
		if got := metadataHTTPStatus(&metadata.Error{
			Code: code,
		}); got != want {
			t.Fatalf("%s status = %d, want %d", code, got, want)
		}
	}
}
