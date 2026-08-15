package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
