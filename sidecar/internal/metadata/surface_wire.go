package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	wb "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// These are the closed public Interface DTOs, including required nullable fields.
// Normalization preserves Python's accepted snake aliases and scalar coercion;
// aggregate rules and their ordered domain errors remain in surface_definition.go.
var surfaceWireObjects = map[string]map[string]string{
	"interface.list":   {},
	"interface.load":   {"interfaceId": "string+"},
	"interface.commit": {"definition": "definition", "expectedRevision": "string?", "idempotencyKey": "string+"},
	"interface.delete": {"interfaceId": "string+", "expectedRevision": "string+", "idempotencyKey": "string+"},
	"definition":       {"contractVersion": "=1.0", "interfaceId": "string+", "name": "string+", "bindings": "[]binding", "actions": "[]action", "pages": "[]page"},
	"binding":          {"bindingId": "string+", "query": "query", "variables": "[]variable"},
	"query":            {"contractVersion": "=1.0", "tableId": "string+", "fields": "[]string+", "filters": "[]filter", "sorts": "[]sort", "cursor": "string?", "pageSize": "pageSize"},
	"filter":           {"fieldId": "string+", "operator": "operator", "value": "scalar"},
	"sort":             {"fieldId": "string+", "direction": "=asc|desc"},
	"variable":         {"variableId": "string+", "targetFieldId": "string+", "operator": "operator", "source": "=literal|selectedRecordField", "sourceBindingId": "string?", "sourceFieldId": "string?", "value": "scalar"},
	"action":           {"actionId": "string+", "kind": "=record.create|record.update|binding.refresh|navigate|plugin", "bindingId": "string?", "targetPageId": "string?", "pluginId": "string?", "pluginActionId": "string?", "requiresConfirmation": "bool"},
	"page":             {"pageId": "string+", "title": "string+", "elements": "[]element"},
	"element":          {"elementId": "string+", "kind": "=section|columns|tabs|text|metric|chart|record-list|record-detail|form|button|navigation", "bindingId": "string?", "actionId": "string?", "text": "string?", "width": "=full|half|third", "children": "[]element"},
}
var errSurfaceDTO = errors.New("invalid Interface parameters")

// DecodeSurfaceParams validates one of the four public requests before dispatch.
func DecodeSurfaceParams(method string, raw json.RawMessage, target any) error {
	switch method {
	case "interface.list", "interface.load", "interface.commit", "interface.delete":
	default:
		return errSurfaceDTO
	}
	return decodeSurfaceWire(method, raw, target)
}
func DecodeSurfaceDefinition(raw json.RawMessage, target *wb.InterfaceDefinition) error {
	return decodeSurfaceWire("definition", raw, target)
}
func decodeSurfaceWire(kind string, raw json.RawMessage, target any) error {
	if len(raw) > 1<<20 {
		return errSurfaceDTO
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return errSurfaceDTO
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errSurfaceDTO
	}
	normalized, err := normalizeSurfaceWire(kind, value, 0)
	if err != nil {
		return err
	}
	result, err := json.Marshal(normalized)
	if err != nil {
		return errSurfaceDTO
	}
	if target == nil {
		return nil
	}
	if json.Unmarshal(result, target) != nil {
		return errSurfaceDTO
	}
	return nil
}
func normalizeSurfaceWire(kind string, value any, depth int) (any, error) {
	// Bound recursive normalization independently of the aggregate's eight-level
	// public tree limit. This leaves room to validate and report domain depth
	// errors while rejecting pathological JSON before building a typed tree.
	if depth > 256 {
		return nil, errSurfaceDTO
	}
	if itemKind, ok := strings.CutPrefix(kind, "[]"); ok {
		values, ok := value.([]any)
		if !ok {
			return nil, errSurfaceDTO
		}
		result := make([]any, len(values))
		for i, v := range values {
			var err error
			result[i], err = normalizeSurfaceWire(itemKind, v, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	if fields, ok := surfaceWireObjects[kind]; ok {
		values, ok := value.(map[string]any)
		if !ok || len(values) != len(fields) {
			return nil, errSurfaceDTO
		}
		result := make(map[string]any, len(fields))
		for name, fieldKind := range fields {
			v, exists := values[name]
			if !exists {
				v, exists = values[surfaceSnakeName(name)]
			}
			if !exists {
				return nil, errSurfaceDTO
			}
			normalized, err := normalizeSurfaceWire(fieldKind, v, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = normalized
		}
		// No additional properties, including both the alias and its snake spelling.
		for name := range values {
			valid := false
			for field := range fields {
				if name == field || name == surfaceSnakeName(field) {
					valid = true
					break
				}
			}
			if !valid {
				return nil, errSurfaceDTO
			}
		}
		if kind == "definition" && len(result["pages"].([]any)) == 0 {
			return nil, errSurfaceDTO
		}
		if kind == "query" && len(result["fields"].([]any)) == 0 {
			return nil, errSurfaceDTO
		}
		if kind == "binding" && len(result["variables"].([]any)) > 32 {
			return nil, errSurfaceDTO
		}
		return result, nil
	}
	if kind == "operator" {
		kind = "=eq|ne|contains|startsWith|gt|gte|lt|lte|isNull|isNotNull"
	}
	if strings.HasPrefix(kind, "=") {
		text, ok := value.(string)
		if ok {
			for _, allowed := range strings.Split(kind[1:], "|") {
				if text == allowed {
					return text, nil
				}
			}
		}
		return nil, errSurfaceDTO
	}
	switch kind {
	case "string?":
		if value == nil {
			return nil, nil
		}
		fallthrough
	case "string", "string+":
		text, ok := value.(string)
		if !ok || (kind == "string+" && text == "") {
			return nil, errSurfaceDTO
		}
		return text, nil
	case "scalar":
		switch v := value.(type) {
		case nil, string, bool:
			return v, nil
		case json.Number:
			f, err := v.Float64()
			if err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
				return f, nil
			}
		}
		return nil, errSurfaceDTO
	case "bool":
		switch v := value.(type) {
		case bool:
			return v, nil
		case json.Number:
			f, err := v.Float64()
			if err == nil && (f == 0 || f == 1) {
				return f == 1, nil
			}
		case string:
			switch strings.ToLower(v) {
			case "0", "off", "f", "false", "n", "no":
				return false, nil
			case "1", "on", "t", "true", "y", "yes":
				return true, nil
			}
		}
	case "pageSize":
		n, ok := surfaceInteger(value)
		if ok && n >= 1 && n <= 500 {
			return n, nil
		}
	}
	return nil, errSurfaceDTO
}
func surfaceSnakeName(name string) string {
	var result strings.Builder
	for _, r := range name {
		if unicode.IsUpper(r) {
			result.WriteByte('_')
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

var surfaceIntText = regexp.MustCompile(`^[+-]?[0-9]+(?:_?[0-9]+)*$`)
var surfaceIntegralDecimal = regexp.MustCompile(`^[+-]?[0-9]+\.0+$`)

func surfaceInteger(value any) (int64, bool) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case string:
		text := strings.TrimSpace(v)
		if surfaceIntegralDecimal.MatchString(text) {
			text = strings.SplitN(text, ".", 2)[0]
		} else if !surfaceIntText.MatchString(text) {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(text, "_", ""), 10, 64)
		return n, err == nil
	case json.Number:
		f, err := v.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f < 1 || f > 500 {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}
