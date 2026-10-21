package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestJSONHandlerEmitsClosedContentFreeSchema(t *testing.T) {
	var output bytes.Buffer
	handler := NewJSONHandler(&output, slog.LevelInfo)
	record := slog.NewRecord(
		time.Date(2026, 8, 12, 1, 2, 3, 4, time.UTC),
		slog.LevelError,
		"search.rebuild_failed",
		0,
	)
	record.AddAttrs(
		slog.String("errorCode", "workspace_search.storage_failed"),
		slog.String("workspaceId", "workspace-1"),
		slog.Any("error", errors.New("secret正文 C:\\private\\workspace.db")),
		slog.String("query", "customer password"),
	)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{
		"durationMs", "errorCode", "event", "jobId", "level", "module",
		"operationId", "requestId", "sessionEpoch", "timestamp", "workspaceId",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %#v", keys)
	}
	if payload["event"] != "search.rebuild_failed" ||
		payload["errorCode"] != "workspace_search.storage_failed" ||
		payload["workspaceId"] != "workspace-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if bytes.Contains(output.Bytes(), []byte("secret")) ||
		bytes.Contains(output.Bytes(), []byte("private")) ||
		bytes.Contains(output.Bytes(), []byte("password")) {
		t.Fatalf("unsafe value escaped: %s", output.String())
	}
}

func TestJSONHandlerAuditDrainAllowsOnlyEnumsAndMeasurements(t *testing.T) {
	for _, test := range []struct {
		name    string
		detail  slog.Attr
		present bool
	}{
		{"measurements", slog.Group("auditDrain",
			slog.String("source", "business"), slog.String("phase", "ack"),
			slog.Int("appended", 1), slog.Int("duplicates", 2), slog.Int("acknowledged", 0),
			slog.Int("pendingCalls", 1), slog.Int("appendCalls", 3), slog.Int("ackCalls", 3),
			slog.Float64("pendingMs", 1.5), slog.Float64("appendMs", 4.5), slog.Float64("appendMaxMs", 2.5), slog.Float64("ackMs", 9),
			slog.Int64("pbWriteWaitCount", 1), slog.Float64("pbWriteWaitMs", 9),
			slog.Int("pbWriteInUseDelta", -1),
			slog.String("path", "secret C:/private/database"), slog.String("eventId", "secret-id"),
			slog.String("payload", "secret-value"), slog.String("error", "secret-error"),
			slog.String("pbReadWaitCount", "secret-number"), slog.Float64("pbReadWaitMs", math.NaN()),
		), true},
		{"bad source", slog.Group("auditDrain", slog.String("source", "secret-source"), slog.String("phase", "ack")), false},
		{"bad phase", slog.Group("auditDrain", slog.String("source", "file"), slog.String("phase", "secret-phase")), false},
		{"arbitrary object", slog.Any("auditDrain", map[string]string{"source": "secret-source"}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(NewJSONHandler(&output, slog.LevelInfo))
			logger.Info("snapshot.freeze.audit.drain.returned", test.detail)
			var payload map[string]any
			if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			detail, ok := payload["auditDrain"].(map[string]any)
			if ok != test.present {
				t.Fatalf("unexpected auditDrain: %s", output.String())
			}
			if test.present {
				want := map[string]any{
					"source": "business", "phase": "ack", "appended": float64(1), "duplicates": float64(2),
					"acknowledged": float64(0), "pendingCalls": float64(1), "appendCalls": float64(3),
					"ackCalls": float64(3), "pendingMs": 1.5, "appendMs": 4.5, "appendMaxMs": 2.5, "ackMs": float64(9),
					"pbWriteWaitCount": float64(1), "pbWriteWaitMs": float64(9), "pbWriteInUseDelta": float64(-1),
				}
				if !reflect.DeepEqual(detail, want) {
					t.Fatalf("detail = %#v", detail)
				}
			}
			if bytes.Contains(output.Bytes(), []byte("secret")) || bytes.Contains(output.Bytes(), []byte("private")) {
				t.Fatalf("content escaped: %s", output.String())
			}
		})
	}
}
