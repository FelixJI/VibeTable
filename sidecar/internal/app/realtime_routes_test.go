package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/realtime"
)

func TestDecodeRealtimeBodyIsStrictAndBounded(t *testing.T) {
	var input realtime.ReconcileRequest
	if err := decodeRealtimeBody(strings.NewReader(
		`{"tableId":"orders","schemaRevision":"schema_0001",`+
			`"dataRevision":"data_0002"}`,
	), &input); err != nil {
		t.Fatal(err)
	}
	if input.TableID != "orders" {
		t.Fatalf("input = %#v", input)
	}
	for name, body := range map[string]string{
		"empty": ``,
		"unknown": `{"tableId":"orders","schemaRevision":"schema_0001",` +
			`"dataRevision":"data_0002","rawSql":"select 1"}`,
		"trailing": `{"tableId":"orders","schemaRevision":"schema_0001",` +
			`"dataRevision":"data_0002"} {}`,
		"oversized": `"` + strings.Repeat(
			"x", maxRealtimeRequestBytes,
		) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			var target realtime.ReconcileRequest
			err := decodeRealtimeBody(
				strings.NewReader(body), &target,
			)
			var productErr *realtime.Error
			if !errors.As(err, &productErr) ||
				productErr.Code != "realtime.request.invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}
