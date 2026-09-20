package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// VersionParams is the closed command shared by all six named revision methods.
// Field admission remains method-specific; values is deliberately always empty.
type VersionParams struct {
	Collection       string                     `json:"collection"`
	ItemID           string                     `json:"itemId"`
	VersionID        string                     `json:"versionId,omitempty"`
	Key              string                     `json:"key,omitempty"`
	Name             string                     `json:"name,omitempty"`
	OperationID      string                     `json:"operationId,omitempty"`
	ExpectedRevision string                     `json:"expectedRevision,omitempty"`
	MainHash         string                     `json:"mainHash,omitempty"`
	Values           map[string]json.RawMessage `json:"values,omitempty"`
}

func DecodeVersionParams(method string, raw json.RawMessage) (VersionParams, error) {
	var params VersionParams
	invalid := errors.New("invalid named revision parameters")
	if len(raw) > 1<<20 || !utf8.Valid(raw) || !json.Valid(raw) {
		return params, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return params, invalid
	}
	allowed := map[string]int{"collection": 128, "itemId": 128}
	required := []string{"collection", "itemId"}
	switch method {
	case "version.list":
	case "version.create":
		allowed["key"] = 128
		allowed["name"] = 256
		allowed["operationId"] = 128
		required = append(required, "operationId")
	case "version.compare":
		allowed["versionId"] = 128
		allowed["operationId"] = 128
		required = append(required, "versionId")
	case "version.save", "version.delete", "version.promote":
		allowed["versionId"] = 128
		allowed["operationId"] = 128
		allowed["expectedRevision"] = 128
		required = append(required, "versionId", "operationId", "expectedRevision")
		if method == "version.save" {
			allowed["values"] = 0
		}
		if method == "version.promote" {
			allowed["mainHash"] = 128
			required = append(required, "mainHash")
		}
	default:
		return params, invalid
	}
	aliases := map[string]string{"item_id": "itemId", "version_id": "versionId", "operation_id": "operationId", "expected_revision": "expectedRevision", "main_hash": "mainHash"}
	for alias, canonical := range aliases {
		if value, present := fields[alias]; present {
			if _, duplicate := fields[canonical]; duplicate {
				return params, invalid
			}
			fields[canonical] = value
			delete(fields, alias)
		}
	}
	for key, value := range fields {
		limit, ok := allowed[key]
		if !ok {
			return params, invalid
		}
		if key == "values" {
			var values map[string]json.RawMessage
			if json.Unmarshal(value, &values) != nil || values == nil {
				return params, invalid
			}
			continue
		}
		if method == "version.compare" && key == "operationId" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(fields, key)
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || utf8.RuneCountInString(text) > limit {
			return params, invalid
		}
	}
	for _, key := range required {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || value == "" {
			return params, invalid
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return params, invalid
	}
	if json.Unmarshal(normalized, &params) != nil {
		return params, invalid
	}
	return params, nil
}
