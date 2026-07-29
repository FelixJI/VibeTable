package fieldvalue

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// NormalizeRawInput converts a supplied external cell value into the typed
// product input consumed by NormalizeWrite. It owns CSV/XLSX-style parsing so
// import, paste, and other raw-input adapters do not define parallel field
// semantics.
func (kernel *Kernel) NormalizeRawInput(
	ctx context.Context,
	definition v2.FieldDefinition,
	raw any,
) (Input, error) {
	if err := ctx.Err(); err != nil {
		return Input{}, err
	}
	value, err := normalizeRawValue(definition, raw)
	if err != nil {
		return Input{}, err
	}
	return Input{Supplied: true, Value: value}, nil
}

func normalizeRawValue(definition v2.FieldDefinition, raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch definition.LogicalType {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail, v2.LogicalURL:
		return rawCellText(raw), nil
	case v2.LogicalNumber:
		return normalizeRawNumber(raw)
	case v2.LogicalBool:
		return normalizeRawBoolean(raw)
	case v2.LogicalDate:
		return normalizeRawDate(raw, false)
	case v2.LogicalDateTime:
		return normalizeRawDate(raw, true)
	case v2.LogicalTime:
		return normalizeRawTime(raw)
	case v2.LogicalSelect:
		if rawCellBlank(raw) {
			return nil, nil
		}
		return resolveRawOption(definition, rawCellText(raw))
	case v2.LogicalMultiSelect:
		if rawCellBlank(raw) {
			return []string{}, nil
		}
		values, err := normalizeRawList(raw)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(values))
		for _, value := range values {
			optionID, resolveErr := resolveRawOption(
				definition, rawCellText(value),
			)
			if resolveErr != nil {
				return nil, resolveErr
			}
			result = append(result, optionID)
		}
		return result, nil
	case v2.LogicalRelation:
		if rawCellBlank(raw) {
			return nil, nil
		}
		if definition.Relation.Cardinality == "one" {
			return strings.TrimSpace(rawCellText(raw)), nil
		}
		values, err := normalizeRawList(raw)
		if err != nil {
			return nil, err
		}
		result := make([]string, len(values))
		for index, value := range values {
			result[index] = strings.TrimSpace(rawCellText(value))
		}
		return result, nil
	case v2.LogicalGeoPoint, v2.LogicalJSON:
		if rawCellBlank(raw) {
			return nil, nil
		}
		if text, ok := raw.(string); ok {
			var decoded any
			if err := json.Unmarshal([]byte(text), &decoded); err != nil {
				return nil, fmt.Errorf("invalid JSON value")
			}
			return decoded, nil
		}
		return raw, nil
	default:
		return raw, nil
	}
}

func normalizeRawNumber(raw any) (any, error) {
	switch value := raw.(type) {
	case json.Number:
		return value, nil
	case float64, float32, int, int32, int64, uint, uint64:
		return value, nil
	}
	text := strings.TrimSpace(rawCellText(raw))
	if text == "" {
		return nil, nil
	}
	for _, token := range []string{
		",", "$", "¥", "￥", "€", "£", "₽", "USD", "CNY", "RMB", "元",
	} {
		text = strings.ReplaceAll(text, token, "")
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, fmt.Errorf("value is not a finite number")
	}
	return number, nil
}

func normalizeRawBoolean(raw any) (any, error) {
	if value, ok := raw.(bool); ok {
		return value, nil
	}
	if rawCellBlank(raw) {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(rawCellText(raw))) {
	case "true", "1", "yes", "y", "是":
		return true, nil
	case "false", "0", "no", "n", "否":
		return false, nil
	default:
		return nil, fmt.Errorf("value is not boolean")
	}
}

func normalizeRawDate(raw any, dateTime bool) (any, error) {
	if rawCellBlank(raw) {
		return nil, nil
	}
	if number, ok := rawCellNumber(raw); ok && number > 30000 {
		parsed := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).
			Add(time.Duration(number * float64(24*time.Hour)))
		if dateTime {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
		return parsed.Format("2006-01-02"), nil
	}
	text := strings.TrimSpace(rawCellText(raw))
	if dateTime {
		for _, layout := range []string{
			time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04",
			"2006/01/02 15:04:05", "2006.01.02 15:04:05",
			"20060102150405",
		} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.UTC().Format(time.RFC3339Nano), nil
			}
		}
	}
	for _, layout := range []string{
		"2006-01-02", "2006/01/02", "2006.01.02", "20060102",
		"02-01-2006", "02/01/2006", "02.01.2006",
		"01-02-2006", "01/02/2006", "2006年01月02日",
		"02-01-06", "02/01/06",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			if dateTime {
				return parsed.UTC().Format(time.RFC3339Nano), nil
			}
			return parsed.Format("2006-01-02"), nil
		}
	}
	return nil, fmt.Errorf("unrecognized date format")
}

func normalizeRawTime(raw any) (any, error) {
	if rawCellBlank(raw) {
		return nil, nil
	}
	text := strings.TrimSpace(rawCellText(raw))
	for _, layout := range []string{"15:04", "15:04:05", "15:04:05.000"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Format("15:04:05"), nil
		}
	}
	return nil, fmt.Errorf("time must use HH:mm[:ss[.fff]]")
}

func resolveRawOption(
	definition v2.FieldDefinition,
	raw string,
) (string, error) {
	match := ""
	for _, option := range definition.Select.Options {
		if option.State != v2.OptionActive {
			continue
		}
		if raw == option.OptionID {
			return option.OptionID, nil
		}
		if raw == option.Label {
			if match != "" {
				return "", fmt.Errorf("option label is ambiguous")
			}
			match = option.OptionID
		}
	}
	if match == "" {
		return "", fmt.Errorf("option is missing or retired")
	}
	return match, nil
}

func normalizeRawList(raw any) ([]any, error) {
	if values, ok := raw.([]any); ok {
		return values, nil
	}
	if values, ok := raw.([]string); ok {
		result := make([]any, len(values))
		for index, value := range values {
			result[index] = value
		}
		return result, nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("value must be an array")
	}
	var decoded []any
	if json.Unmarshal([]byte(text), &decoded) == nil {
		return decoded, nil
	}
	parts := strings.Split(text, ",")
	result := make([]any, 0, len(parts))
	for _, part := range parts {
		result = append(result, strings.TrimSpace(part))
	}
	return result, nil
}

func rawCellNumber(raw any) (float64, bool) {
	switch value := raw.(type) {
	case json.Number:
		number, err := value.Float64()
		return number, err == nil
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	}
	return 0, false
}

func rawCellText(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		if math.Trunc(value) == value {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(raw)
	}
}

func rawCellBlank(raw any) bool {
	if raw == nil {
		return true
	}
	text, ok := raw.(string)
	return ok && text == ""
}
