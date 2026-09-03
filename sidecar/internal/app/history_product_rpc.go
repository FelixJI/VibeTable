package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

type businessHistoryReader interface {
	ReadBusinessHistory(context.Context, audit.ReadParams) (audit.Page, error)
}

const maxHistoryReadProductParamsBytes = 1 << 20

func historyReadRegistration(reader businessHistoryReader) productrpc.Registration {
	return productrpc.Registration{
		Method: "history.read", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: validateHistoryReadProductParams,
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			params, err := decodeHistoryReadProductParams(raw)
			if err != nil {
				return nil, publicHistoryReadParamsError(err)
			}
			page, err := reader.ReadBusinessHistory(ctx, params)
			if err != nil {
				return nil, publicHistoryReadError(err)
			}
			return page, nil
		},
	}
}

func validateHistoryReadProductParams(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("history.read requires an object")
	}
	if err := validateHistoryReadProductValue(value, 0); err != nil {
		return err
	}
	if encodedSize, err := historyReadProductParamsSize(value); err != nil ||
		encodedSize > maxHistoryReadProductParamsBytes {
		return errors.New("history.read parameters exceed the safe size limit")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return errors.New("history.read requires an object")
	}
	allowed := map[string]struct{}{
		"collection": {}, "itemId": {}, "limit": {}, "offset": {}, "scope": {},
		"field": {}, "search": {}, "dateFrom": {}, "dateTo": {}, "actorId": {},
		"actions": {}, "recordId": {},
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return errors.New("history.read has an unknown parameter")
		}
	}
	for _, key := range []string{"collection", "limit", "offset", "scope", "actions"} {
		if _, ok := values[key]; !ok {
			return errors.New("history.read is missing a required parameter")
		}
	}
	return nil
}

func historyReadProductParamsSize(value any) (int, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, err
	}
	// Encoder always escapes U+2028 and U+2029, while Python's ensure_ascii=False
	// writes their three UTF-8 bytes. Count only parsed runes, so literal
	// backslash-u text retains its actual serialized budget.
	return encoded.Len() - 1 - 3*historyReadUnicodeSeparatorCount(value), nil
}

func historyReadUnicodeSeparatorCount(value any) int {
	switch value := value.(type) {
	case string:
		count := 0
		for _, character := range value {
			if character == '\u2028' || character == '\u2029' {
				count++
			}
		}
		return count
	case map[string]any:
		count := 0
		for key, item := range value {
			count += historyReadUnicodeSeparatorCount(key)
			count += historyReadUnicodeSeparatorCount(item)
		}
		return count
	case []any:
		count := 0
		for _, item := range value {
			count += historyReadUnicodeSeparatorCount(item)
		}
		return count
	default:
		return 0
	}
}

func validateHistoryReadProductValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("history.read parameters are too deeply nested")
	}
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			switch key {
			case "accessToken", "legacyToken", "password", "pocketBaseToken", "refreshToken", "sessionSecret":
				return errors.New("history.read parameters contain a forbidden key")
			}
			if err := validateHistoryReadProductValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := validateHistoryReadProductValue(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeHistoryReadProductParams(raw json.RawMessage) (audit.ReadParams, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return audit.ReadParams{}, err
	}
	collection, err := requiredHistoryText(values, "collection")
	if err != nil {
		return audit.ReadParams{}, err
	}
	scope, err := historyText(values, "scope", false)
	if err != nil {
		return audit.ReadParams{}, err
	}
	if scope == "" {
		scope = "row"
	}
	limit, err := historyInteger(values, "limit")
	if err != nil {
		return audit.ReadParams{}, err
	}
	offset, err := historyInteger(values, "offset")
	if err != nil {
		return audit.ReadParams{}, err
	}
	actions, err := historyActions(values)
	if err != nil {
		return audit.ReadParams{}, err
	}
	params := audit.ReadParams{
		TableID: collection, Scope: scope, Limit: limit, Offset: offset, Actions: actions,
	}
	for _, target := range []struct {
		name string
		set  func(*string)
	}{
		{"itemId", func(value *string) { params.ItemID = value }},
		{"field", func(value *string) { params.Field = value }},
		{"search", func(value *string) { params.Search = *value }},
		{"actorId", func(value *string) { params.ActorID = value }},
		{"dateFrom", func(value *string) { params.DateFrom = value }},
		{"dateTo", func(value *string) { params.DateTo = value }},
		{"recordId", func(value *string) { params.RecordID = value }},
	} {
		value, present, textErr := optionalHistoryText(values, target.name)
		if textErr != nil {
			return audit.ReadParams{}, textErr
		}
		if present {
			target.set(&value)
		}
	}
	return params, nil
}

func requiredHistoryText(values map[string]json.RawMessage, name string) (string, error) {
	value, err := historyText(values, name, true)
	if err != nil || value == "" {
		return "", errors.New("history text is invalid")
	}
	return value, nil
}

func historyText(values map[string]json.RawMessage, name string, required bool) (string, error) {
	raw, ok := values[name]
	if !ok && !required {
		return "", nil
	}
	var value string
	if !ok || string(bytes.TrimSpace(raw)) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", errors.New("history text is invalid")
	}
	return value, nil
}

func optionalHistoryText(values map[string]json.RawMessage, name string) (string, bool, error) {
	raw, ok := values[name]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return "", false, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", false, errors.New("history optional text is invalid")
	}
	return value, true, nil
}

func historyInteger(values map[string]json.RawMessage, name string) (int, error) {
	raw := values[name]
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, errors.New("history integer is invalid")
	}
	value, ok := decoded.(json.Number)
	if !ok {
		return 0, errors.New("history integer is invalid")
	}
	lexeme := value.String()
	if !historyIntegerLexeme(lexeme) {
		return 0, errors.New("history integer is invalid")
	}
	parsed, err := strconv.Atoi(lexeme)
	if err != nil {
		return 0, &audit.Error{
			Code: "history.request_invalid", Message: "history " + name + " is invalid",
			Details: map[string]any{},
		}
	}
	return parsed, nil
}

func historyIntegerLexeme(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func historyActions(values map[string]json.RawMessage) ([]string, error) {
	var actions []string
	raw := values["actions"]
	if string(bytes.TrimSpace(raw)) == "null" || json.Unmarshal(raw, &actions) != nil {
		return nil, errors.New("history actions are invalid")
	}
	for _, action := range actions {
		if action == "" {
			return nil, errors.New("history actions are invalid")
		}
	}
	return actions, nil
}

func publicHistoryReadParamsError(err error) error {
	var historyError *audit.Error
	if !errors.As(err, &historyError) {
		return err
	}
	return publicHistoryReadError(historyError)
}

func publicHistoryReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var historyError *audit.Error
	if !errors.As(err, &historyError) {
		return &productrpc.PublicError{
			Code: "history.internal_failed", Message: "history operation failed",
			Details: map[string]any{}, Retryable: true,
		}
	}
	return &productrpc.PublicError{
		Code: historyError.Code, Message: historyError.Message,
		Details: historyError.Details, Retryable: historyError.Retryable,
	}
}
