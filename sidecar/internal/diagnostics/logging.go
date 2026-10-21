// Package diagnostics owns the content-free production log envelope.
package diagnostics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

const maximumTextLength = 160

type writerState struct {
	mu     sync.Mutex
	writer io.Writer
}

type jsonHandler struct {
	state *writerState
	level slog.Level
	attrs map[string]any
}

// NewJSONHandler emits exactly the cross-process support schema. Unknown
// attributes, messages, values, paths, plugin output and error text are never
// serialized.
func NewJSONHandler(writer io.Writer, level slog.Level) slog.Handler {
	return &jsonHandler{
		state: &writerState{writer: writer},
		level: level,
		attrs: map[string]any{},
	}
}

func (handler *jsonHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= handler.level
}

func (handler *jsonHandler) Handle(_ context.Context, record slog.Record) error {
	values := cloneAttrs(handler.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		collect(values, attr)
		return true
	})
	payload := map[string]any{
		"timestamp":    record.Time.UTC().Format(time.RFC3339Nano),
		"level":        strings.ToLower(record.Level.String()),
		"module":       textValue(values["module"], "sidecar"),
		"event":        bounded(record.Message),
		"errorCode":    nullableText(values["errorCode"]),
		"requestId":    nullableText(values["requestId"]),
		"operationId":  nullableText(values["operationId"]),
		"workspaceId":  nullableText(values["workspaceId"]),
		"sessionEpoch": nullableInteger(values["sessionEpoch"]),
		"jobId":        nullableText(values["jobId"]),
		"durationMs":   nullableNumber(values["durationMs"]),
	}
	if detail, ok := values["auditDrain"]; ok {
		payload["auditDrain"] = detail
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	handler.state.mu.Lock()
	defer handler.state.mu.Unlock()
	_, err = handler.state.writer.Write(append(raw, '\n'))
	return err
}

func (handler *jsonHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := &jsonHandler{
		state: handler.state,
		level: handler.level,
		attrs: cloneAttrs(handler.attrs),
	}
	for _, attr := range attrs {
		collect(clone.attrs, attr)
	}
	return clone
}

func (handler *jsonHandler) WithGroup(string) slog.Handler { return handler }

func collect(values map[string]any, attr slog.Attr) {
	switch attr.Key {
	case "auditDrain":
		if detail := auditDrainValue(attr.Value); detail != nil {
			values[attr.Key] = detail
		}
	case "module", "errorCode", "requestId", "operationId", "workspaceId",
		"sessionEpoch", "jobId", "durationMs":
		values[attr.Key] = attr.Value.Any()
	}
}

func cloneAttrs(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func bounded(value string) string {
	runes := []rune(value)
	if len(runes) > maximumTextLength {
		return string(runes[:maximumTextLength])
	}
	return value
}

func textValue(value any, fallback string) string {
	if result, ok := value.(string); ok && result != "" && len(result) <= maximumTextLength {
		return result
	}
	return fallback
}

func nullableText(value any) any {
	if result, ok := value.(string); ok && result != "" && len(result) <= maximumTextLength {
		return result
	}
	return nil
}

func nullableInteger(value any) any {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return typed
	case uint64:
		return typed
	default:
		return nil
	}
}

func nullableNumber(value any) any {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return typed
	case float64:
		return typed
	default:
		return nil
	}
}

// auditDrain accepts only closed source/phase enums and numeric operational measurements.
func auditDrainValue(value slog.Value) map[string]any {
	if value.Kind() != slog.KindGroup {
		return nil
	}
	result := map[string]any{}
	for _, attr := range value.Group() {
		switch attr.Key {
		case "source":
			if attr.Value.Kind() == slog.KindString && (attr.Value.String() == "business" || attr.Value.String() == "file") {
				result[attr.Key] = attr.Value.String()
			}
		case "phase":
			if attr.Value.Kind() == slog.KindString {
				switch attr.Value.String() {
				case "pending", "append", "ack", "between_records", "completed":
					result[attr.Key] = attr.Value.String()
				}
			}
		case "appended", "duplicates", "acknowledged", "pendingCalls", "appendCalls", "ackCalls",
			"pbReadWaitCount", "pbWriteWaitCount":
			if attr.Value.Kind() == slog.KindInt64 && attr.Value.Int64() >= 0 {
				result[attr.Key] = attr.Value.Int64()
			}
		case "pbReadInUseDelta", "pbWriteInUseDelta":
			if attr.Value.Kind() == slog.KindInt64 {
				result[attr.Key] = attr.Value.Int64()
			}
		case "pendingMs", "appendMs", "appendMaxMs", "ackMs", "pbReadWaitMs", "pbWriteWaitMs":
			if attr.Value.Kind() == slog.KindFloat64 {
				number := attr.Value.Float64()
				if !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 {
					result[attr.Key] = number
				}
			}
		}
	}
	if result["source"] == nil || result["phase"] == nil {
		return nil
	}
	return result
}
