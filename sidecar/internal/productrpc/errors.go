package productrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const CodeProductData = -32150

var publicErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

var forbiddenDetailKeys = map[string]struct{}{
	"accessToken":     {},
	"legacyToken":     {},
	"password":        {},
	"pocketBaseToken": {},
	"refreshToken":    {},
	"sessionSecret":   {},
}

// PublicError is an explicit, provider-neutral Product rejection. Handler
// adapters must opt into this type; arbitrary errors never become public.
type PublicError struct {
	Code      string
	Path      *string
	Message   string
	Details   map[string]any
	Retryable bool
}

func (productError *PublicError) Error() string {
	if productError == nil {
		return "Product data error"
	}
	return productError.Message
}

func productErrorData(err error) (map[string]any, bool) {
	var productError *PublicError
	if !errors.As(err, &productError) || productError == nil ||
		!publicErrorCodePattern.MatchString(productError.Code) ||
		strings.TrimSpace(productError.Message) == "" ||
		strings.HasPrefix(productError.Code, "pocketbase.") {
		return nil, false
	}
	details, err := sanitizeDetails(productError.Details)
	if err != nil {
		return nil, false
	}
	var path any
	if productError.Path != nil {
		path = *productError.Path
	}
	return map[string]any{
		"kind":      "product_data_error",
		"message":   productError.Message,
		"code":      productError.Code,
		"path":      path,
		"details":   details,
		"retryable": productError.Retryable,
	}, true
}

func sanitizeDetails(details map[string]any) (map[string]any, error) {
	if details == nil {
		return map[string]any{}, nil
	}
	raw, err := safeJSONMarshal(details)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var closed map[string]any
	if err := decoder.Decode(&closed); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing Product error details")
	}
	return scrubDetails(closed).(map[string]any), nil
}

func scrubDetails(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if _, forbidden := forbiddenDetailKeys[key]; forbidden {
				continue
			}
			result[key] = scrubDetails(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = scrubDetails(item)
		}
		return result
	default:
		return typed
	}
}
