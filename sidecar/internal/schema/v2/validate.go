package v2

import (
	"encoding/json"
	"errors"
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
)

type ProductError struct {
	Code    string         `json:"code"`
	Path    string         `json:"path"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (err *ProductError) Error() string {
	return err.Code + " at " + err.Path + ": " + err.Message
}

func Validate(definition FieldDefinition) error {
	if definition.Contract != Contract {
		return invalid("contract", "contract must be "+Contract)
	}
	if !fieldIDPattern.MatchString(definition.Identity.FieldID) {
		return invalid("identity.fieldId", "fieldId must be an opaque field identity")
	}
	if !physicalNamePattern.MatchString(definition.Identity.PhysicalName) {
		return invalid("identity.physicalName", "physicalName must be a stable product name")
	}
	if !providerFieldIDPattern.MatchString(definition.Identity.ProviderFieldID) {
		return invalid("identity.providerFieldId", "providerFieldId must be an opaque provider identity")
	}
	if strings.TrimSpace(definition.DisplayName) == "" {
		return invalid("displayName", "displayName is required")
	}
	if len([]rune(definition.Help)) > 300 {
		return invalid("help", "help must contain at most 300 characters")
	}
	capability, err := CapabilityFor(definition.LogicalType)
	if err != nil {
		return unsupported("logicalType", err.Error())
	}
	if definition.Lifecycle.State != LifecycleActive && definition.Lifecycle.State != LifecycleRetired {
		return invalid("lifecycle.state", "lifecycle state must be active or retired")
	}
	if definition.Lifecycle.State == LifecycleActive && definition.Lifecycle.RetiredAt != nil {
		return invalid("lifecycle.retiredAt", "active fields cannot have retiredAt")
	}
	if definition.Lifecycle.State == LifecycleRetired {
		if definition.Lifecycle.RetiredAt == nil || !validRFC3339(*definition.Lifecycle.RetiredAt) {
			return invalid("lifecycle.retiredAt", "retired fields require an RFC3339 retiredAt")
		}
	}
	if definition.Value.Default.DefaultsVersion < 1 {
		return invalid("value.default.defaultsVersion", "defaultsVersion must be positive")
	}
	if definition.Value.Default.Source != DefaultRecommended &&
		definition.Value.Default.Source != DefaultUser {
		return invalid("value.default.source", "default source is invalid")
	}
	if !definition.Value.Default.Enabled && definition.Value.Default.Value != nil {
		return invalid("value.default.value", "disabled default must use null")
	}
	if !capability.SupportsDefault && definition.Value.Default.Enabled {
		return unsupported("value.default.enabled", "logical type does not support a default")
	}
	if !capability.SupportsRequired && definition.Value.Required {
		return unsupported("value.required", "logical type does not support required")
	}
	if !capability.SupportsUnique && definition.Constraints.Unique.Enabled {
		return unsupported("constraints.unique.enabled", "logical type does not support unique")
	}
	if definition.Constraints.Unique.BlankPolicy != BlankIgnoreMissing {
		return invalid("constraints.unique.blankPolicy", "blankPolicy must be ignoreMissing")
	}
	if err := validatePresence(definition); err != nil {
		return err
	}
	if definition.Storage.Kind != capability.Recommended.Storage.Kind {
		return invalid(
			"storage.kind",
			fmt.Sprintf("%s fields require %s storage", definition.LogicalType, capability.Recommended.Storage.Kind),
		)
	}
	if err := validateDisplay(definition, capability); err != nil {
		return err
	}
	if err := validateRange(definition); err != nil {
		return err
	}
	if definition.Constraints.Length.Min != nil && definition.Constraints.Length.Max != nil &&
		*definition.Constraints.Length.Min > *definition.Constraints.Length.Max {
		return invalid("constraints.length", "minimum cannot exceed maximum")
	}
	if definition.Constraints.Pattern.Enabled && definition.Constraints.Pattern.Value == "" {
		return invalid("constraints.pattern.value", "enabled pattern requires a value")
	}
	if err := validateTypeSpecific(definition); err != nil {
		return err
	}
	return validateDefault(definition)
}

func validateDefault(definition FieldDefinition) error {
	spec := definition.Value.Default
	if !spec.Enabled {
		return nil
	}
	if spec.Value == nil {
		if definition.LogicalType == LogicalJSON &&
			(definition.JSON.RootType == "any" ||
				definition.JSON.RootType == "null") &&
			!definition.Value.Required {
			return nil
		}
		return invalid("value.default.value", "null default is invalid for this field")
	}
	if dynamic, ok := spec.Value.(map[string]any); ok &&
		(definition.LogicalType == LogicalDate ||
			definition.LogicalType == LogicalDateTime ||
			definition.LogicalType == LogicalTime) {
		if len(dynamic) != 1 {
			return invalid(
				"value.default.value",
				"dynamic default must contain only kind",
			)
		}
		kind, _ := dynamic["kind"].(string)
		valid := (definition.LogicalType == LogicalDate && kind == "today") ||
			(definition.LogicalType == LogicalDateTime && kind == "now") ||
			(definition.LogicalType == LogicalTime && kind == "currentTime")
		if !valid {
			return invalid(
				"value.default.value.kind",
				"dynamic default does not match logical type",
			)
		}
		return nil
	}
	switch definition.LogicalType {
	case LogicalText, LogicalEditor:
		text, ok := spec.Value.(string)
		if !ok {
			return invalid("value.default.value", "text default must be text")
		}
		length := utf8.RuneCountInString(text)
		if minimum := definition.Constraints.Length.Min; minimum != nil &&
			length < *minimum {
			return invalid("value.default.value", "text default is too short")
		}
		if maximum := definition.Constraints.Length.Max; maximum != nil &&
			length > *maximum {
			return invalid("value.default.value", "text default is too long")
		}
		if definition.Storage.Options.MaxSize > 0 &&
			len([]byte(text)) > definition.Storage.Options.MaxSize {
			return invalid("value.default.value", "text default exceeds maxSize")
		}
		if definition.Constraints.Pattern.Enabled {
			pattern, err := regexp.Compile(definition.Constraints.Pattern.Value)
			if err != nil || !pattern.MatchString(text) {
				return invalid("value.default.value", "text default does not match pattern")
			}
		}
	case LogicalNumber:
		number, _, err := rangeNumber(spec.Value)
		if err != nil {
			return invalid("value.default.value", "number default must be numeric")
		}
		if definition.Storage.Options.OnlyInt && math.Trunc(number) != number {
			return invalid("value.default.value", "number default must be an integer")
		}
		minimum, hasMinimum, _ := rangeNumber(definition.Constraints.Range.Min)
		maximum, hasMaximum, _ := rangeNumber(definition.Constraints.Range.Max)
		if hasMinimum && number < minimum || hasMaximum && number > maximum {
			return invalid("value.default.value", "number default is outside range")
		}
	case LogicalBool:
		if _, ok := spec.Value.(bool); !ok {
			return invalid("value.default.value", "bool default must be boolean")
		}
	case LogicalDate, LogicalDateTime, LogicalTime:
		actual, present, err := rangeTime(
			definition.LogicalType, spec.Value,
		)
		if err != nil || !present {
			return invalid("value.default.value", "temporal default has invalid format")
		}
		minimum, hasMinimum, _ := rangeTime(
			definition.LogicalType, definition.Constraints.Range.Min,
		)
		maximum, hasMaximum, _ := rangeTime(
			definition.LogicalType, definition.Constraints.Range.Max,
		)
		if hasMinimum && actual.Before(minimum) ||
			hasMaximum && actual.After(maximum) {
			return invalid(
				"value.default.value",
				"temporal default is outside range",
			)
		}
	case LogicalEmail:
		text, ok := spec.Value.(string)
		parsed, err := mail.ParseAddress(text)
		if !ok || err != nil || parsed.Address != text {
			return invalid("value.default.value", "email default is invalid")
		}
		if !defaultDomainAllowed(
			definition,
			strings.ToLower(text[strings.LastIndex(text, "@")+1:]),
		) {
			return invalid("value.default.value", "email default domain is not allowed")
		}
	case LogicalURL:
		text, ok := spec.Value.(string)
		parsed, err := url.Parse(text)
		if !ok || err != nil || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return invalid("value.default.value", "URL default is invalid")
		}
		if !defaultDomainAllowed(
			definition, strings.ToLower(parsed.Hostname()),
		) {
			return invalid("value.default.value", "URL default domain is not allowed")
		}
	case LogicalSelect, LogicalMultiSelect:
		values, ok := defaultStringValues(
			spec.Value, definition.LogicalType == LogicalMultiSelect,
		)
		if !ok {
			return invalid("value.default.value", "select default has invalid shape")
		}
		active := map[string]struct{}{}
		for _, option := range definition.Select.Options {
			if option.State == OptionActive {
				active[option.OptionID] = struct{}{}
			}
		}
		for _, value := range values {
			if _, exists := active[value]; !exists {
				return invalid(
					"value.default.value",
					"select default references an inactive option",
				)
			}
		}
		if len(values) < definition.Constraints.Selection.Min ||
			(definition.Constraints.Selection.Max != nil &&
				len(values) > *definition.Constraints.Selection.Max) {
			return invalid(
				"value.default.value",
				"select default violates selection bounds",
			)
		}
	case LogicalGeoPoint:
		point, ok := spec.Value.(map[string]any)
		if !ok || len(point) != 2 {
			return invalid("value.default.value", "geoPoint default must contain lat and lon")
		}
		lat, latOK, _ := rangeNumber(point["lat"])
		lon, lonOK, _ := rangeNumber(point["lon"])
		if !latOK || !lonOK || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return invalid("value.default.value", "geoPoint default is out of range")
		}
	case LogicalJSON:
		raw, err := json.Marshal(spec.Value)
		if err != nil || len(raw) > definition.JSON.MaxSize ||
			!jsonRootMatches(definition.JSON.RootType, spec.Value) {
			return invalid("value.default.value", "JSON default is invalid")
		}
		if len(definition.JSON.Schema) != 0 {
			field := legacyschema.FieldDefinition{
				DataType: legacyschema.DataTypeJSON,
				Constraints: []legacyschema.FieldConstraint{{
					Kind:   legacyschema.ConstraintJSONSchema,
					Schema: definition.JSON.Schema,
				}},
			}
			if err := legacyschema.ValidateFieldValue(field, spec.Value); err != nil {
				return invalid(
					"value.default.value",
					"JSON default does not satisfy schema",
				)
			}
		}
	default:
		return unsupported(
			"value.default.enabled",
			"logical type does not support a default",
		)
	}
	return nil
}

func defaultDomainAllowed(definition FieldDefinition, domain string) bool {
	for _, denied := range definition.Constraints.Domains.Except {
		if strings.EqualFold(denied, domain) {
			return false
		}
	}
	if len(definition.Constraints.Domains.Only) == 0 {
		return true
	}
	for _, allowed := range definition.Constraints.Domains.Only {
		if strings.EqualFold(allowed, domain) {
			return true
		}
	}
	return false
}

func defaultStringValues(value any, multiple bool) ([]string, bool) {
	if !multiple {
		text, ok := value.(string)
		return []string{text}, ok
	}
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func jsonRootMatches(root string, value any) bool {
	if value == nil {
		return root == "any" || root == "null"
	}
	switch root {
	case "any":
		return true
	case "object":
		return reflect.ValueOf(value).Kind() == reflect.Map
	case "array":
		return reflect.ValueOf(value).Kind() == reflect.Slice
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, _, err := rangeNumber(value)
		return err == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func validateDisplay(
	definition FieldDefinition,
	capability Capability,
) error {
	display := definition.Display
	if display.Kind != capability.Recommended.Display.Kind {
		return invalid("display.kind", "display kind does not match logical type")
	}
	if display.DisplayScale < 0 || display.DisplayScale > 15 {
		return invalid("display.displayScale", "display scale must be between 0 and 15")
	}
	if display.ScaleMode != "max" && display.ScaleMode != "fixed" {
		return invalid("display.scaleMode", "scale mode must be max or fixed")
	}
	if display.PercentStorage != "ratio" && display.PercentStorage != "percent" {
		return invalid(
			"display.percentStorage",
			"percent storage must be ratio or percent",
		)
	}
	switch display.Precision {
	case "exact", "day", "minute", "second", "millisecond":
	default:
		return invalid("display.precision", "display precision is invalid")
	}
	if definition.LogicalType != LogicalJSON && display.Indent != 0 {
		return invalid("display.indent", "indent is only supported for JSON")
	}
	return nil
}

func validateRange(definition FieldDefinition) error {
	minimum := definition.Constraints.Range.Min
	maximum := definition.Constraints.Range.Max
	switch definition.LogicalType {
	case LogicalNumber:
		min, hasMin, err := rangeNumber(minimum)
		if err != nil {
			return invalid("constraints.range.min", "number range minimum must be numeric")
		}
		max, hasMax, err := rangeNumber(maximum)
		if err != nil {
			return invalid("constraints.range.max", "number range maximum must be numeric")
		}
		if hasMin && hasMax && min > max {
			return invalid("constraints.range", "minimum cannot exceed maximum")
		}
	case LogicalDate, LogicalDateTime, LogicalTime:
		min, hasMin, err := rangeTime(definition.LogicalType, minimum)
		if err != nil {
			return invalid(
				"constraints.range.min",
				"temporal range minimum has an invalid format",
			)
		}
		max, hasMax, err := rangeTime(definition.LogicalType, maximum)
		if err != nil {
			return invalid(
				"constraints.range.max",
				"temporal range maximum has an invalid format",
			)
		}
		if hasMin && hasMax && min.After(max) {
			return invalid("constraints.range", "minimum cannot exceed maximum")
		}
	default:
		if minimum != nil || maximum != nil {
			return invalid(
				"constraints.range",
				"range is not supported for this logical type",
			)
		}
	}
	return nil
}

func rangeNumber(value any) (float64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false, errors.New("non-finite number")
		}
		return typed, true, nil
	case float32:
		return float64(typed), true, nil
	case int:
		return float64(typed), true, nil
	case int64:
		return float64(typed), true, nil
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil, err
	case *float64:
		if typed == nil {
			return 0, false, nil
		}
		return rangeNumber(*typed)
	default:
		return 0, false, errors.New("not a number")
	}
}

func rangeTime(
	logicalType LogicalType,
	value any,
) (time.Time, bool, error) {
	if value == nil {
		return time.Time{}, false, nil
	}
	text, ok := value.(string)
	if !ok {
		if pointer, pointerOK := value.(*string); pointerOK && pointer != nil {
			text, ok = *pointer, true
		}
	}
	if !ok {
		return time.Time{}, false, errors.New("not temporal text")
	}
	var parsed time.Time
	var err error
	switch logicalType {
	case LogicalDate:
		parsed, err = time.Parse("2006-01-02", text)
	case LogicalDateTime:
		parsed, err = time.Parse(time.RFC3339Nano, text)
	case LogicalTime:
		for _, layout := range []string{"15:04", "15:04:05", "15:04:05.000"} {
			if parsed, err = time.Parse(layout, text); err == nil {
				break
			}
		}
	}
	return parsed, true, err
}

func validatePresence(definition FieldDefinition) error {
	needs := presenceTypes[definition.LogicalType]
	presence := definition.Value.Presence
	if needs {
		if presence.Mode != PresenceCompanion {
			return invalid("value.presence.mode", "logical type requires companion presence")
		}
		if !providerFieldIDPattern.MatchString(presence.ProviderFieldID) {
			return invalid("value.presence.providerFieldId", "companion providerFieldId is invalid")
		}
		expected := "__vt_has_" + definition.Identity.PhysicalName
		if presence.PhysicalName != expected {
			return invalid("value.presence.physicalName", "companion physicalName must be "+expected)
		}
		return nil
	}
	expectedMode := PresenceNative
	if definition.LogicalType == LogicalAutoDate ||
		definition.LogicalType == LogicalFormula ||
		definition.LogicalType == LogicalLookup {
		expectedMode = PresenceComputed
	}
	if presence.Mode != expectedMode {
		return invalid("value.presence.mode", "logical type has an invalid presence mode")
	}
	if presence.ProviderFieldID != "" || presence.PhysicalName != "" {
		return invalid("value.presence", "non-companion presence cannot declare provider identity")
	}
	return nil
}

func validateTypeSpecific(definition FieldDefinition) error {
	if definition.Select != nil &&
		definition.LogicalType != LogicalSelect &&
		definition.LogicalType != LogicalMultiSelect {
		return invalid("select", "select settings are not allowed for this logical type")
	}
	if definition.Relation != nil && definition.LogicalType != LogicalRelation {
		return invalid("relation", "relation settings are not allowed for this logical type")
	}
	if definition.File != nil && definition.LogicalType != LogicalFile {
		return invalid("file", "file settings are not allowed for this logical type")
	}
	if definition.JSON != nil && definition.LogicalType != LogicalJSON {
		return invalid("json", "JSON settings are not allowed for this logical type")
	}
	if definition.AutoDate != nil && definition.LogicalType != LogicalAutoDate {
		return invalid("autoDate", "autoDate settings are not allowed for this logical type")
	}
	if definition.Formula != nil && definition.LogicalType != LogicalFormula {
		return invalid("formula", "formula settings are not allowed for this logical type")
	}
	if definition.Lookup != nil && definition.LogicalType != LogicalLookup {
		return invalid("lookup", "lookup settings are not allowed for this logical type")
	}
	switch definition.LogicalType {
	case LogicalSelect, LogicalMultiSelect:
		if definition.Select == nil || len(definition.Select.Options) == 0 {
			return invalid("select.options", "select fields require at least one option")
		}
		seen := map[string]struct{}{}
		for index, option := range definition.Select.Options {
			path := fmt.Sprintf("select.options[%d]", index)
			if !optionIDPattern.MatchString(option.OptionID) {
				return invalid(path+".optionId", "optionId must be opaque and stable")
			}
			if _, duplicate := seen[option.OptionID]; duplicate {
				return invalid(path+".optionId", "optionId must be unique")
			}
			seen[option.OptionID] = struct{}{}
			if strings.TrimSpace(option.Label) == "" {
				return invalid(path+".label", "option label is required")
			}
			if option.State != OptionActive && option.State != OptionRetired {
				return invalid(path+".state", "option state is invalid")
			}
		}
		if definition.LogicalType == LogicalSelect {
			if definition.Constraints.Selection.Max == nil ||
				*definition.Constraints.Selection.Max != 1 {
				return invalid("constraints.selection.max", "single select max must be 1")
			}
		}
	case LogicalRelation:
		if definition.Relation == nil || definition.Relation.TargetTableID == "" {
			return invalid("relation.targetTableId", "relation target table is required")
		}
		if definition.Relation.DisplayField == "" {
			return invalid(
				"relation.displayFieldId",
				"relation display field is required",
			)
		}
		if definition.Relation.Cardinality != "one" && definition.Relation.Cardinality != "many" {
			return invalid("relation.cardinality", "relation cardinality must be one or many")
		}
		if (definition.Relation.PairID == "") !=
			(definition.Relation.ReciprocalFieldID == "") {
			return invalid(
				"relation.pairId",
				"relation pair identity and reciprocal field must be configured together",
			)
		}
		switch definition.Relation.DeletePolicy {
		case "setNull", "restrict", "cascade":
		default:
			return invalid("relation.deletePolicy", "relation delete policy is invalid")
		}
	case LogicalFile:
		if definition.File == nil || definition.File.MaxFiles < 1 ||
			definition.File.MaxBytesPerFile < 1 {
			return invalid("file", "file limits must be positive")
		}
	case LogicalJSON:
		if definition.JSON == nil || definition.JSON.MaxSize < 1 {
			return invalid("json", "JSON settings are required")
		}
		switch definition.JSON.RootType {
		case "any", "object", "array", "string", "number", "boolean", "null":
		default:
			return invalid("json.rootType", "JSON root type is invalid")
		}
		if definition.Display.Indent != 2 && definition.Display.Indent != 4 {
			return invalid("display.indent", "JSON indent must be 2 or 4")
		}
	case LogicalAutoDate:
		if definition.AutoDate == nil ||
			(definition.AutoDate.Role != "createdAt" && definition.AutoDate.Role != "updatedAt") {
			return invalid("autoDate.role", "autoDate role must be createdAt or updatedAt")
		}
	case LogicalFormula:
		if definition.Formula == nil || strings.TrimSpace(definition.Formula.Source) == "" {
			return invalid("formula.source", "formula source is required")
		}
		if definition.Formula.Language != "cel-v1" {
			return invalid("formula.language", "formula language must be cel-v1")
		}
		if !validComputedResultType(definition.Formula.ResultType, true) {
			return invalid("formula.resultType", "formula result type is invalid")
		}
	case LogicalLookup:
		if definition.Lookup == nil || definition.Lookup.TargetFieldID == "" ||
			len(definition.Lookup.Path) == 0 || len(definition.Lookup.Path) > 8 {
			return invalid("lookup", "lookup requires one to eight relation path steps and a target field")
		}
		for index, step := range definition.Lookup.Path {
			if step.RelationFieldID == "" {
				return invalid(
					fmt.Sprintf("lookup.path[%d].relationFieldId", index),
					"lookup relation field is required",
				)
			}
		}
	}
	return nil
}

func validComputedResultType(logicalType LogicalType, formula bool) bool {
	switch logicalType {
	case LogicalText, LogicalNumber, LogicalBool, LogicalDate,
		LogicalDateTime, LogicalTime, LogicalJSON:
		return true
	case LogicalEmail, LogicalURL:
		return formula
	default:
		return false
	}
}

func invalid(path, message string) *ProductError {
	return &ProductError{Code: "field.contract.invalid", Path: path, Message: message}
}

func unsupported(path, message string) *ProductError {
	return &ProductError{Code: "field.capability.unsupported", Path: path, Message: message}
}
