package fieldvalue

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	legacyschema "github.com/vibetable/vibetable/sidecar/internal/schema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type WriteMode string

const (
	Insert WriteMode = "insert"
	Update WriteMode = "update"
)

type Input struct {
	Supplied bool
	Value    any
}

type Result struct {
	Write          bool
	ProductValue   any
	Present        bool
	PhysicalValues map[string]any
}

type Kernel struct {
	clock func() time.Time
}

type Option func(*Kernel)

func WithClock(clock func() time.Time) Option {
	return func(kernel *Kernel) {
		kernel.clock = clock
	}
}

func New(options ...Option) *Kernel {
	kernel := &Kernel{clock: time.Now}
	for _, option := range options {
		option(kernel)
	}
	return kernel
}

func (kernel *Kernel) NormalizeWrite(
	ctx context.Context,
	definition v2.FieldDefinition,
	mode WriteMode,
	input Input,
) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if mode != Insert && mode != Update {
		return Result{}, productError("field.value.invalid_mode", "", "write mode is invalid")
	}
	if definition.LogicalType == v2.LogicalAutoDate ||
		definition.LogicalType == v2.LogicalFormula ||
		definition.LogicalType == v2.LogicalLookup {
		if input.Supplied {
			return Result{}, productError(
				"field.value.read_only", "value", "read-only field cannot be supplied",
			)
		}
		return Result{Write: false}, nil
	}

	value := input.Value
	supplied := input.Supplied
	if !supplied && mode == Insert && definition.Value.Default.Enabled {
		resolved, err := kernel.resolveDefault(
			definition.LogicalType,
			definition.Value.Default.Value,
		)
		if err != nil {
			return Result{}, err
		}
		value = resolved
		supplied = true
	}
	if !supplied {
		if mode == Insert && definition.Value.Required {
			return Result{}, productError("field.value.required", "value", "value is required")
		}
		return Result{Write: false}, nil
	}

	present := value != nil
	if !present {
		if definition.Value.Required {
			return Result{}, productError("field.value.required", "value", "value is required")
		}
		return missingResult(definition), nil
	}
	normalized, err := normalizePresentValue(definition, value)
	if err != nil {
		return Result{}, err
	}
	physical := map[string]any{definition.Identity.PhysicalName: normalized}
	if definition.Value.Presence.Mode == v2.PresenceCompanion {
		physical[definition.Value.Presence.PhysicalName] = true
	}
	return Result{
		Write: true, ProductValue: normalized, Present: true, PhysicalValues: physical,
	}, nil
}

func (kernel *Kernel) resolveDefault(
	logicalType v2.LogicalType,
	value any,
) (any, error) {
	dynamic, ok := value.(map[string]any)
	if !ok || (logicalType != v2.LogicalDate &&
		logicalType != v2.LogicalDateTime &&
		logicalType != v2.LogicalTime) {
		return clone(value)
	}
	kind, _ := dynamic["kind"].(string)
	now := kernel.clock()
	switch kind {
	case "":
		return clone(value)
	case "today":
		if logicalType != v2.LogicalDate {
			break
		}
		return now.Format("2006-01-02"), nil
	case "now":
		if logicalType != v2.LogicalDateTime {
			break
		}
		return now.UTC().Format(time.RFC3339Nano), nil
	case "currentTime":
		if logicalType != v2.LogicalTime {
			break
		}
		return now.Format("15:04:05"), nil
	}
	return nil, productError(
		"field.default.invalid", "value.default.value.kind",
		"dynamic default does not match the field type",
	)
}

func missingResult(definition v2.FieldDefinition) Result {
	physical := map[string]any{definition.Identity.PhysicalName: zeroValue(definition.LogicalType)}
	if definition.Value.Presence.Mode == v2.PresenceCompanion {
		physical[definition.Value.Presence.PhysicalName] = false
	}
	return Result{Write: true, ProductValue: nil, Present: false, PhysicalValues: physical}
}

func zeroValue(logicalType v2.LogicalType) any {
	switch logicalType {
	case v2.LogicalNumber:
		return float64(0)
	case v2.LogicalBool:
		return false
	case v2.LogicalGeoPoint:
		return map[string]any{"lat": float64(0), "lon": float64(0)}
	case v2.LogicalMultiSelect, v2.LogicalRelation, v2.LogicalFile:
		return []string{}
	case v2.LogicalJSON:
		return nil
	default:
		return ""
	}
}

func normalizePresentValue(definition v2.FieldDefinition, value any) (any, error) {
	switch definition.LogicalType {
	case v2.LogicalText, v2.LogicalEditor:
		return normalizeText(definition, value)
	case v2.LogicalNumber:
		return normalizeNumber(definition, value)
	case v2.LogicalBool:
		boolean, ok := value.(bool)
		if !ok {
			return nil, invalid("value", "value must be boolean")
		}
		return boolean, nil
	case v2.LogicalDate:
		return normalizeDate(definition, value)
	case v2.LogicalDateTime:
		return normalizeDateTime(definition, value)
	case v2.LogicalTime:
		return normalizeTime(definition, value)
	case v2.LogicalEmail:
		return normalizeEmail(definition, value)
	case v2.LogicalURL:
		return normalizeURL(definition, value)
	case v2.LogicalSelect:
		return normalizeSelect(definition, value, false)
	case v2.LogicalMultiSelect:
		return normalizeSelect(definition, value, true)
	case v2.LogicalRelation:
		return normalizeRelation(definition, value)
	case v2.LogicalFile:
		return normalizeFiles(definition, value)
	case v2.LogicalGeoPoint:
		return normalizeGeoPoint(value)
	case v2.LogicalJSON:
		return normalizeJSON(definition, value)
	default:
		return nil, invalid("logicalType", "logical type is not writable")
	}
}

func normalizeText(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "value must be text")
	}
	length := utf8.RuneCountInString(text)
	if minimum := definition.Constraints.Length.Min; minimum != nil && length < *minimum {
		return "", invalid("value", "text is shorter than the minimum")
	}
	if maximum := definition.Constraints.Length.Max; maximum != nil && length > *maximum {
		return "", invalid("value", "text is longer than the maximum")
	}
	if definition.Storage.Options.MaxSize > 0 && len([]byte(text)) > definition.Storage.Options.MaxSize {
		return "", invalid("value", "text exceeds maxSize")
	}
	if definition.Constraints.Pattern.Enabled {
		pattern, err := regexp.Compile(definition.Constraints.Pattern.Value)
		if err != nil || !pattern.MatchString(text) {
			return "", invalid("value", "text does not match pattern")
		}
	}
	return text, nil
}

func normalizeNumber(definition v2.FieldDefinition, value any) (float64, error) {
	number, err := numberValue(value)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, invalid("value", "value must be a finite number")
	}
	if definition.Storage.Options.OnlyInt && math.Trunc(number) != number {
		return 0, invalid("value", "value must be an integer")
	}
	if minimum, ok := numericConstraint(definition.Constraints.Range.Min); ok &&
		number < minimum {
		return 0, invalid("value", "number is below the minimum")
	}
	if maximum, ok := numericConstraint(definition.Constraints.Range.Max); ok &&
		number > maximum {
		return 0, invalid("value", "number is above the maximum")
	}
	return number, nil
}

func normalizeDate(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "date must be text")
	}
	if _, err := time.Parse("2006-01-02", text); err != nil {
		return "", invalid("value", "date must use YYYY-MM-DD")
	}
	return text, validateTemporalConstraint(definition, text)
}

func normalizeDateTime(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "dateTime must be text")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", invalid("value", "dateTime must use RFC3339 with timezone")
	}
	normalized := parsed.UTC().Format(time.RFC3339Nano)
	return normalized, validateTemporalConstraint(definition, normalized)
}

func normalizeTime(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "time must be text")
	}
	for _, layout := range []string{"15:04", "15:04:05", "15:04:05.000"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			normalized := parsed.Format("15:04:05")
			return normalized, validateTemporalConstraint(definition, normalized)
		}
	}
	return "", invalid("value", "time must use HH:mm[:ss[.fff]]")
}

func numericConstraint(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case *float64:
		if typed != nil {
			return *typed, true
		}
	}
	return 0, false
}

func validateTemporalConstraint(
	definition v2.FieldDefinition,
	value string,
) error {
	parse := func(candidate any) (time.Time, bool) {
		text, ok := candidate.(string)
		if !ok {
			if pointer, pointerOK := candidate.(*string); pointerOK && pointer != nil {
				text, ok = *pointer, true
			}
		}
		if !ok {
			return time.Time{}, false
		}
		var parsed time.Time
		var err error
		switch definition.LogicalType {
		case v2.LogicalDate:
			parsed, err = time.Parse("2006-01-02", text)
		case v2.LogicalDateTime:
			parsed, err = time.Parse(time.RFC3339Nano, text)
		case v2.LogicalTime:
			for _, layout := range []string{"15:04", "15:04:05", "15:04:05.000"} {
				if parsed, err = time.Parse(layout, text); err == nil {
					break
				}
			}
		}
		return parsed, err == nil
	}
	actual, ok := parse(value)
	if !ok {
		return invalid("value", "temporal value is invalid")
	}
	if minimum, present := parse(definition.Constraints.Range.Min); present &&
		actual.Before(minimum) {
		return invalid("value", "temporal value is below the minimum")
	}
	if maximum, present := parse(definition.Constraints.Range.Max); present &&
		actual.After(maximum) {
		return invalid("value", "temporal value is above the maximum")
	}
	return nil
}

func normalizeEmail(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "email must be text")
	}
	address, err := mail.ParseAddress(text)
	if err != nil || address.Address != text {
		return "", invalid("value", "email is invalid")
	}
	domain := strings.ToLower(text[strings.LastIndex(text, "@")+1:])
	if err := validateDomain(definition, domain); err != nil {
		return "", err
	}
	return text, nil
}

func normalizeURL(definition v2.FieldDefinition, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", invalid("value", "URL must be text")
	}
	parsed, err := url.ParseRequestURI(text)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", invalid("value", "URL must use HTTP or HTTPS")
	}
	if err := validateDomain(definition, strings.ToLower(parsed.Hostname())); err != nil {
		return "", err
	}
	return text, nil
}

func validateDomain(definition v2.FieldDefinition, domain string) error {
	if containsFold(definition.Constraints.Domains.Except, domain) {
		return invalid("value", "domain is denied")
	}
	if len(definition.Constraints.Domains.Only) != 0 &&
		!containsFold(definition.Constraints.Domains.Only, domain) {
		return invalid("value", "domain is not allowed")
	}
	return nil
}

func normalizeSelect(
	definition v2.FieldDefinition,
	value any,
	multiple bool,
) (any, error) {
	values := []string{}
	if multiple {
		var ok bool
		values, ok = stringSlice(value)
		if !ok {
			return nil, invalid("value", "multiSelect must be a string array")
		}
	} else {
		single, ok := value.(string)
		if !ok {
			return nil, invalid("value", "select must be an optionId")
		}
		values = []string{single}
	}
	if len(values) < definition.Constraints.Selection.Min {
		return nil, invalid("value", "selection has fewer items than allowed")
	}
	if maximum := definition.Constraints.Selection.Max; maximum != nil && len(values) > *maximum {
		return nil, invalid("value", "selection has more items than allowed")
	}
	active := map[string]struct{}{}
	for _, option := range definition.Select.Options {
		if option.State == v2.OptionActive {
			active[option.OptionID] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	for _, optionID := range values {
		if _, ok := active[optionID]; !ok {
			return nil, invalid("value", "option is missing or retired")
		}
		if _, duplicate := seen[optionID]; duplicate {
			return nil, invalid("value", "selection contains duplicate options")
		}
		seen[optionID] = struct{}{}
	}
	if multiple {
		return values, nil
	}
	return values[0], nil
}

func normalizeRelation(definition v2.FieldDefinition, value any) (any, error) {
	if definition.Relation.Cardinality == "one" {
		recordID, ok := value.(string)
		if !ok || recordID == "" {
			return nil, invalid("value", "single relation must be a record ID")
		}
		return recordID, nil
	}
	values, ok := stringSlice(value)
	if !ok {
		return nil, invalid("value", "multi relation must be a record ID array")
	}
	if len(values) < definition.Constraints.Selection.Min {
		return nil, invalid("value", "relation has fewer targets than allowed")
	}
	if maximum := definition.Constraints.Selection.Max; maximum != nil &&
		len(values) > *maximum {
		return nil, invalid("value", "relation has more targets than allowed")
	}
	return validateUniqueStrings(values, "relation target")
}

func normalizeFiles(definition v2.FieldDefinition, value any) (any, error) {
	values, ok := stringSlice(value)
	if !ok {
		return nil, invalid("value", "file value must be a stored-name array")
	}
	if len(values) > definition.File.MaxFiles {
		return nil, invalid("value", "file count exceeds maxFiles")
	}
	return validateUniqueStrings(values, "stored file")
}

func normalizeGeoPoint(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("value", "geoPoint must contain lat and lon")
	}
	lat, latErr := numberValue(object["lat"])
	lon, lonErr := numberValue(object["lon"])
	if latErr != nil || lonErr != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, invalid("value", "geoPoint coordinates are invalid")
	}
	if len(object) != 2 {
		return nil, invalid("value", "geoPoint accepts only lat and lon")
	}
	return map[string]any{"lat": lat, "lon": lon}, nil
}

func normalizeJSON(definition v2.FieldDefinition, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > definition.JSON.MaxSize {
		return nil, invalid("value", "JSON value is invalid or exceeds maxSize")
	}
	switch definition.JSON.RootType {
	case "any":
	case "object":
		if reflect.ValueOf(value).Kind() != reflect.Map {
			return nil, invalid("value", "JSON root must be object")
		}
	case "array":
		if reflect.ValueOf(value).Kind() != reflect.Slice {
			return nil, invalid("value", "JSON root must be array")
		}
	case "string":
		if _, ok := value.(string); !ok {
			return nil, invalid("value", "JSON root must be string")
		}
	case "number":
		if _, err := numberValue(value); err != nil {
			return nil, invalid("value", "JSON root must be number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return nil, invalid("value", "JSON root must be boolean")
		}
	case "null":
		return nil, invalid("value", "non-null JSON does not match null root type")
	default:
		return nil, invalid("json.rootType", "unknown JSON root type")
	}
	if len(definition.JSON.Schema) != 0 {
		field := legacyschema.FieldDefinition{
			DataType: legacyschema.DataTypeJSON,
			Constraints: []legacyschema.FieldConstraint{{
				Kind:   legacyschema.ConstraintJSONSchema,
				Schema: definition.JSON.Schema,
			}},
		}
		if err := legacyschema.ValidateFieldValue(field, value); err != nil {
			return nil, invalid("value", "JSON value does not satisfy schema")
		}
	}
	return clone(value)
}

func numberValue(value any) (float64, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil || math.Abs(number) > float64(1<<53-1) {
			return 0, fmt.Errorf("number exceeds safe wire range")
		}
		return number, nil
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int:
		if int64(typed) > 1<<53-1 || int64(typed) < -(1<<53-1) {
			return 0, fmt.Errorf("integer exceeds safe wire range")
		}
		return float64(typed), nil
	case int64:
		if typed > 1<<53-1 || typed < -(1<<53-1) {
			return 0, fmt.Errorf("integer exceeds safe wire range")
		}
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case uint:
		if uint64(typed) > 1<<53-1 {
			return 0, fmt.Errorf("integer exceeds safe wire range")
		}
		return float64(typed), nil
	case uint64:
		if typed > 1<<53-1 {
			return 0, fmt.Errorf("integer exceeds safe wire range")
		}
		return float64(typed), nil
	default:
		return 0, fmt.Errorf("not numeric")
	}
}

func stringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		values := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values[index] = text
		}
		return values, true
	default:
		return nil, false
	}
}

func validateUniqueStrings(values []string, label string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" {
			return nil, invalid("value", label+" must not be empty")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, invalid("value", label+" must not be duplicated")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func clone(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("value", "value is not JSON serializable")
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, invalid("value", "value is not JSON serializable")
	}
	return decoded, nil
}

type ProductError struct {
	Code    string
	Path    string
	Message string
}

func (err *ProductError) Error() string {
	return err.Code + " at " + err.Path + ": " + err.Message
}

func invalid(path, message string) *ProductError {
	return productError("field.value.invalid", path, message)
}

func productError(code, path, message string) *ProductError {
	return &ProductError{Code: code, Path: path, Message: message}
}
