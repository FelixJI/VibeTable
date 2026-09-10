package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PresetRequest is the normalized public command. View is authored configuration,
// not a compiled query: it must not consult live schema or normalize field values.
type PresetRequest struct {
	Collection       string         `json:"collection,omitempty"`
	Name             string         `json:"name,omitempty"`
	View             map[string]any `json:"view,omitempty"`
	PresetID         *string        `json:"presetId"`
	ExpectedRevision *string        `json:"expectedRevision"`
	OperationID      string         `json:"operationId,omitempty"`
}

var errPresetParams = errors.New("invalid Preset parameters")

func DecodePresetRequest(method string, raw []byte) (PresetRequest, error) {
	var request PresetRequest
	if len(raw) > 1<<20 {
		return request, errPresetParams
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil {
		return request, errPresetParams
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return request, errPresetParams
	}
	fields := []string{"collection"}
	switch method {
	case "preset.list":
	case "preset.save":
		fields = []string{"collection", "name", "view", "presetId", "expectedRevision", "operationId"}
	case "preset.delete":
		fields = []string{"presetId", "expectedRevision", "operationId"}
	default:
		return request, errPresetParams
	}
	object, err := presetObject(value, fields)
	if err != nil || len(object) != len(fields) {
		return request, errPresetParams
	}
	for key, value := range object {
		if key == "view" {
			request.View, err = normalizePresetView(value)
			if err != nil {
				return request, err
			}
			continue
		}
		if value == nil && method == "preset.save" && (key == "presetId" || key == "expectedRevision") {
			continue
		}
		max := 128
		if key == "name" {
			max = 256
		}
		text, ok := presetString(value, 1, max)
		if !ok {
			return request, errPresetParams
		}
		switch key {
		case "collection":
			request.Collection = text
		case "name":
			request.Name = text
		case "operationId":
			request.OperationID = text
		case "presetId":
			request.PresetID = &text
		case "expectedRevision":
			request.ExpectedRevision = &text
		}
	}
	if method == "preset.save" && (request.PresetID == nil) != (request.ExpectedRevision == nil) {
		return request, errPresetParams
	}
	return request, nil
}

func presetObject(value any, fields []string) (map[string]any, error) {
	source, ok := value.(map[string]any)
	if !ok {
		return nil, errPresetParams
	}
	result := map[string]any{}
	for key, value := range source {
		matched := ""
		for _, field := range fields {
			if key == field || key == presetSnake(field) {
				matched = field
				break
			}
		}
		if matched == "" {
			return nil, errPresetParams
		}
		if _, exists := result[matched]; exists {
			return nil, errPresetParams
		}
		result[matched] = value
	}
	return result, nil
}
func presetSnake(value string) string {
	var result strings.Builder
	for _, r := range value {
		if unicode.IsUpper(r) {
			result.WriteByte('_')
			r = unicode.ToLower(r)
		}
		result.WriteRune(r)
	}
	return result.String()
}
func presetString(value any, min, max int) (string, bool) {
	text, ok := value.(string)
	length := utf8.RuneCountInString(text)
	return text, ok && length >= min && length <= max
}
func presetBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f == 1, err == nil && (f == 0 || f == 1)
	case string:
		switch strings.ToLower(v) {
		case "1", "on", "t", "true", "y", "yes":
			return true, true
		case "0", "off", "f", "false", "n", "no":
			return false, true
		}
	}
	return false, false
}
func presetEnum(value any, values ...string) (string, bool) {
	text, ok := value.(string)
	return text, ok && slices.Contains(values, text)
}

func normalizePresetView(value any) (map[string]any, error) {
	defaults := map[string]any{"filters": []any{}, "sorts": []any{}, "groups": []any{}, "summaries": []any{}, "search": "", "visibleFields": []any{}, "layout": "table", "kind": "table", "dateField": nil, "endDateField": nil, "titleField": nil, "groupField": nil, "coverField": nil, "columns": []any{}, "density": "comfortable", "isDefault": false}
	fields := []string{"filters", "sorts", "groups", "summaries", "collapsedGroupKeys", "search", "visibleFields", "layout", "kind", "dateField", "endDateField", "titleField", "groupField", "coverField", "columns", "density", "isDefault"}
	object, err := presetObject(value, fields)
	if err != nil {
		return nil, err
	}
	conditionCount := 0
	for key, value := range object {
		normalized := value
		valid := true
		switch key {
		case "filters", "sorts", "groups", "summaries", "visibleFields", "columns", "collapsedGroupKeys":
			array, ok := value.([]any)
			if !ok {
				return nil, errPresetParams
			}
			max := map[string]int{"filters": 50, "sorts": 16, "groups": 2, "summaries": 3, "visibleFields": 128, "columns": 128, "collapsedGroupKeys": 512}[key]
			if len(array) > max {
				return nil, errPresetParams
			}
			converted := make([]any, len(array))
			for i, entry := range array {
				switch key {
				case "filters":
					converted[i], err = normalizePresetFilter(entry, 0, &conditionCount)
				case "sorts", "groups", "summaries":
					converted[i], err = normalizePresetQueryPart(key, entry)
				case "visibleFields", "collapsedGroupKeys":
					_, valid = entry.(string)
					converted[i] = entry
				case "columns":
					_, valid = entry.(map[string]any)
					converted[i] = entry
				}
				if err != nil || !valid {
					return nil, errPresetParams
				}
			}
			if key == "collapsedGroupKeys" && len(converted) == 0 {
				continue
			}
			normalized = converted
		case "search":
			normalized, valid = presetString(value, 0, 256)
		case "layout":
			normalized, valid = presetString(value, 0, 32)
		case "kind":
			normalized, valid = presetEnum(value, "table", "calendar", "timeline", "kanban", "gallery")
		case "density":
			normalized, valid = presetEnum(value, "compact", "comfortable", "cozy")
		case "isDefault":
			normalized, valid = presetBool(value)
		default:
			if value != nil {
				normalized, valid = presetString(value, 0, 128)
			}
		}
		if !valid {
			return nil, errPresetParams
		}
		defaults[key] = normalized
	}
	return defaults, nil
}
func normalizePresetQueryPart(kind string, value any) (map[string]any, error) {
	fields := []string{"field", "direction", "nullsLast"}
	defaults := map[string]any{"direction": "asc", "nullsLast": true}
	if kind == "groups" {
		fields = []string{"field", "direction", "bucket", "numberInterval"}
		defaults = map[string]any{"direction": "asc", "bucket": "value"}
	}
	if kind == "summaries" {
		fields = []string{"field", "function"}
		defaults = map[string]any{}
	}
	object, err := presetObject(value, fields)
	if err != nil {
		return nil, err
	}
	if _, ok := presetString(object["field"], 1, 128); !ok {
		return nil, errPresetParams
	}
	for key, value := range object {
		normalized := value
		ok := true
		switch key {
		case "field":
		case "direction":
			normalized, ok = presetEnum(value, "asc", "desc")
		case "nullsLast":
			normalized, ok = presetBool(value)
		case "bucket":
			normalized, ok = presetEnum(value, "value", "year", "quarter", "month", "week", "day", "hour", "number")
		case "function":
			normalized, ok = presetEnum(value, "sum", "avg", "min", "max")
		case "numberInterval":
			if value == nil {
				continue
			}
			var f float64
			switch v := value.(type) {
			case json.Number:
				f, err = v.Float64()
			case float64:
				f = v
			case string:
				number := json.Number(v)
				f, err = number.Float64()
			default:
				err = errPresetParams
			}
			ok = err == nil && f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
			normalized = f
		}
		if !ok {
			return nil, errPresetParams
		}
		defaults[key] = normalized
	}
	if kind == "summaries" && defaults["function"] == nil {
		return nil, errPresetParams
	}
	return defaults, nil
}
func normalizePresetFilter(value any, depth int, count *int) (map[string]any, error) {
	source, ok := value.(map[string]any)
	if !ok {
		return nil, errPresetParams
	}
	if _, group := source["filters"]; group {
		if depth >= 3 {
			return nil, errPresetParams
		}
		object, err := presetObject(value, []string{"filters", "groupLogic", "logic"})
		if err != nil {
			return nil, err
		}
		array, ok := object["filters"].([]any)
		if !ok || len(array) == 0 || len(array) > 50 {
			return nil, errPresetParams
		}
		result := map[string]any{"groupLogic": "AND"}
		children := make([]any, len(array))
		for i, child := range array {
			children[i], err = normalizePresetFilter(child, depth+1, count)
			if err != nil {
				return nil, err
			}
		}
		result["filters"] = children
		for _, key := range []string{"groupLogic", "logic"} {
			if entry, exists := object[key]; exists {
				if entry == nil && key == "logic" {
					continue
				}
				text, valid := presetEnum(entry, "AND", "OR")
				if !valid {
					return nil, errPresetParams
				}
				result[key] = text
			}
		}
		return result, nil
	}
	object, err := presetObject(value, []string{"field", "operator", "value", "logic"})
	if err != nil {
		return nil, err
	}
	if _, ok := presetString(object["field"], 1, 128); !ok {
		return nil, errPresetParams
	}
	if _, ok := presetEnum(object["operator"], "contains", "eq", "ne", "starts_with", "ends_with", "gt", "lt", "gte", "lte", "between", "in", "is_null", "is_not_null", "regex"); !ok {
		return nil, errPresetParams
	}
	if logic, exists := object["logic"]; exists {
		if _, ok := presetEnum(logic, "AND", "OR"); !ok {
			return nil, errPresetParams
		}
	} else {
		object["logic"] = "AND"
	}
	if _, exists := object["value"]; !exists {
		object["value"] = nil
	}
	*count++
	if *count > 50 {
		return nil, errPresetParams
	}
	return object, nil
}
