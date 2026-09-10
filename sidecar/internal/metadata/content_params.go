package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
)

var contentDocumentID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var contentIntegerText = regexp.MustCompile(`^[+-]?[0-9](?:_?[0-9])*(?:\.0+)?$`)

// DecodeContentParams validates the closed workbench DTO before any authority call.
// Snake-case aliases and integral order coercion match the original Pydantic DTO.
func DecodeContentParams(method string, raw json.RawMessage) (any, error) {
	if !utf8.Valid(raw) || !json.Valid(raw) || len(raw) > 1<<20 {
		return nil, errors.New("invalid content JSON")
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, errors.New("content parameters must be an object")
	}
	fields := map[string]string{}
	var result any
	switch method {
	case "contentProfile.load":
		fields = map[string]string{"tableId": "text"}
		result = &workbench.ContentProfileLoadRequest{}
	case "contentProfile.commit":
		fields = map[string]string{"profile": "profile", "expectedRevision": "nullable", "idempotencyKey": "text"}
		result = &workbench.ContentProfileCommitRequest{}
	case "contentProfile.delete":
		fields = map[string]string{"tableId": "text", "expectedRevision": "text", "idempotencyKey": "text"}
		result = &workbench.ContentProfileDeleteRequest{}
	case "recordDocumentLink.list":
		fields = map[string]string{"tableId": "text", "recordId": "text"}
		result = &workbench.RecordDocumentLinkListRequest{}
	case "recordDocumentLink.commit":
		fields = map[string]string{"link": "link", "expectedRevision": "nullable", "idempotencyKey": "text"}
		result = &workbench.RecordDocumentLinkCommitRequest{}
	case "recordDocumentLink.repair":
		fields = map[string]string{"linkId": "text", "documentId": "document", "expectedRevision": "text", "idempotencyKey": "text"}
		result = &workbench.RecordDocumentLinkRepairRequest{}
	case "recordDocumentLink.delete":
		fields = map[string]string{"linkId": "text", "expectedRevision": "text", "idempotencyKey": "text"}
		result = &workbench.RecordDocumentLinkDeleteRequest{}
	default:
		return nil, errors.New("unknown content method")
	}
	if err := validateContentObject(values, fields); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(normalized, result); err != nil {
		return nil, err
	}
	switch value := result.(type) {
	case *workbench.ContentProfileLoadRequest:
		return *value, nil
	case *workbench.ContentProfileCommitRequest:
		return *value, nil
	case *workbench.ContentProfileDeleteRequest:
		return *value, nil
	case *workbench.RecordDocumentLinkListRequest:
		return *value, nil
	case *workbench.RecordDocumentLinkCommitRequest:
		return *value, nil
	case *workbench.RecordDocumentLinkRepairRequest:
		return *value, nil
	case *workbench.RecordDocumentLinkDeleteRequest:
		return *value, nil
	}
	return nil, errors.New("unknown content request")
}

func validateContentObject(values map[string]json.RawMessage, fields map[string]string) error {
	if len(values) != len(fields) {
		return errors.New("unknown or missing content property")
	}
	for name, kind := range fields {
		raw, ok := values[name]
		if !ok {
			var alias strings.Builder
			for _, r := range name {
				if r >= 'A' && r <= 'Z' {
					alias.WriteByte('_')
					alias.WriteRune(r + 32)
				} else {
					alias.WriteRune(r)
				}
			}
			raw, ok = values[alias.String()]
			if ok {
				delete(values, alias.String())
				values[name] = raw
			}
		}
		if !ok {
			return errors.New("required content property missing")
		}
		raw = bytes.TrimSpace(raw)
		if kind == "profile" || kind == "link" {
			var child map[string]json.RawMessage
			if json.Unmarshal(raw, &child) != nil || child == nil {
				return errors.New("invalid content definition")
			}
			childFields := map[string]string{"contractVersion": "version", "tableId": "text", "titleFieldId": "text", "bodyFieldId": "text", "summaryFieldId": "nullable", "searchableFieldIds": "texts"}
			if kind == "link" {
				childFields = map[string]string{"contractVersion": "version", "tableId": "text", "linkId": "text", "recordId": "text", "documentId": "document", "role": "role", "order": "order"}
			}
			if err := validateContentObject(child, childFields); err != nil {
				return err
			}
			values[name], _ = json.Marshal(child)
			continue
		}
		if kind == "nullable" && string(raw) == "null" {
			continue
		}
		if kind == "texts" {
			var list []json.RawMessage
			if json.Unmarshal(raw, &list) != nil || len(list) < 1 || len(list) > 32 {
				return errors.New("invalid searchable fields")
			}
			for _, item := range list {
				var text string
				if json.Unmarshal(item, &text) != nil || text == "" {
					return errors.New("invalid searchable field")
				}
			}
			continue
		}
		if kind == "order" {
			// The original DTO accepts bool and integral decimal strings/numbers.
			value := string(raw)
			if value == "true" {
				value = "1"
			}
			if value == "false" {
				value = "0"
			}
			if strings.HasPrefix(value, `"`) {
				if json.Unmarshal(raw, &value) != nil {
					return errors.New("invalid order")
				}
				value = strings.TrimSpace(value)
				if !contentIntegerText.MatchString(value) {
					return errors.New("invalid order")
				}
				value = strings.ReplaceAll(value, "_", "")
			}
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 10000 || n != math.Trunc(n) {
				return errors.New("invalid order")
			}
			values[name] = json.RawMessage(strconv.FormatInt(int64(n), 10))
			continue
		}
		var text string
		if string(raw) == "null" || json.Unmarshal(raw, &text) != nil {
			return errors.New("content property must be text")
		}
		if kind != "nullable" && text == "" {
			return errors.New("content property must not be empty")
		}
		if kind == "version" && text != "1.0" {
			return errors.New("unsupported content version")
		}
		if kind == "document" && !contentDocumentID.MatchString(text) {
			return errors.New("invalid document identity")
		}
		if kind == "role" && text != "source" && text != "reference" && text != "supporting" && text != "output" {
			return errors.New("invalid document role")
		}
	}
	return nil
}
