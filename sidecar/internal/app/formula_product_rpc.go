package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/schemav2wire"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

var formulaProductMethods = []string{"formula.validate", "formula.draft.validate", "formula.preview"}

func formulaProductRegistrations(domain formulaDomain) []productrpc.Registration {
	registrations := make([]productrpc.Registration, 0, len(formulaProductMethods))
	for _, method := range formulaProductMethods {
		registrations = append(registrations, productrpc.Registration{
			Method: method, Scope: productcapabilities.WorkspaceScope,
			ValidateParams: func(raw json.RawMessage) error {
				_, err := decodeFormulaProductParams(method, raw)
				return err
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				object, err := decodeFormulaProductParams(method, raw)
				if err != nil {
					return nil, err
				}
				var result any
				switch method {
				case "formula.draft.validate":
					result, err = domain.validateDraft(
						ctx, object["tableId"].(string), object["displaySource"].(string),
					)
				case "formula.validate", "formula.preview":
					// The retired Python adapter ran its generated_schema_v2 DTO
					// inside the handler; missing, unknown or ill-typed nested
					// fields and empty changedFieldIds stay handler errors here
					// instead of collapsing into the domain decoder boundary.
					if err = validateFormulaNestedParams(method, object); err != nil {
						return nil, err
					}
					// Python forwarded compact UTF-8 JSON. Host escaping and
					// insignificant whitespace must not consume the REST body budget.
					// Keep original keys: tolerated DTO aliases still reach rejection.
					var body strings.Builder
					if err = appendDescribeRevision(&body, object); err != nil {
						return nil, err
					}
					if method == "formula.validate" {
						var input schemav2wire.FormulaValidateRequest
						if err = decodeFormulaRequest(strings.NewReader(body.String()), &input); err == nil {
							result, err = domain.validate(ctx, input.TableId, input.Field)
						}
					} else {
						var input schemav2wire.FormulaPreviewRequest
						if err = decodeFormulaRequest(strings.NewReader(body.String()), &input); err == nil {
							result, err = domain.preview(ctx, input)
						}
					}
				}
				if err != nil {
					return nil, publicFormulaError(err)
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return result, nil
			},
		})
	}
	return registrations
}

// publicFormulaError keeps the former Python PocketBaseProductError projection:
// HTTP formula and field-change failures surface as -32150 Product data errors,
// and unknown internal failures still project as formula.runtime.
func publicFormulaError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	formulaErr := asFormulaError(err)
	var path *string
	if formulaErr.Path != nil {
		path = formulaErr.Path
	}
	return &productrpc.PublicError{
		Code: formulaErr.Code, Path: path, Message: formulaErr.Message,
		Details: formulaErr.Details,
	}
}

// Keep the Python ProductParams transport boundary separate from the domain decoder.
func decodeFormulaProductParams(method string, raw json.RawMessage) (map[string]any, error) {
	invalid := errors.New("invalid formula product parameters")
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !json.Valid(raw) || decoder.Decode(&value) != nil {
		return nil, invalid
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		start := index
		for index++; index < len(raw); index++ {
			if raw[index] == '\\' {
				index++
				continue
			}
			if raw[index] == '"' {
				break
			}
		}
		if !lookupStringHasUnicodeScalars(raw[start : index+1]) {
			return nil, invalid
		}
	}
	if err := validateQueryPageValue(value, 0); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid
	}
	var required []string
	switch method {
	case "formula.validate":
		required = []string{"tableId", "field"}
	case "formula.draft.validate":
		required = []string{"tableId", "displaySource"}
	case "formula.preview":
		required = []string{"tableId", "field", "row", "changedFieldIds"}
	default:
		return nil, invalid
	}
	allowed := make(map[string]bool, len(required))
	for _, key := range required {
		allowed[key] = true
		if _, ok := object[key]; !ok {
			return nil, invalid
		}
	}
	for key, item := range object {
		if !allowed[key] {
			return nil, invalid
		}
		switch key {
		case "tableId", "displaySource":
			text, ok := item.(string)
			if !ok || text == "" {
				return nil, invalid
			}
		case "field", "row":
			if _, ok := item.(map[string]any); !ok {
				return nil, invalid
			}
		case "changedFieldIds":
			if _, ok := item.([]any); !ok {
				return nil, invalid
			}
		}
	}
	var compact strings.Builder
	if err := appendDescribeRevision(&compact, value); err != nil {
		return nil, err
	}
	if compact.Len() > maxFieldRequestBytes {
		return nil, invalid
	}
	return object, nil
}

func validateFormulaNestedParams(method string, object map[string]any) error {
	invalid := errors.New("invalid formula field parameters")
	if method == "formula.validate" || method == "formula.preview" {
		if err := validateFormulaFieldDTO(object["field"]); err != nil {
			return err
		}
	}
	if method == "formula.preview" {
		for _, item := range object["changedFieldIds"].([]any) {
			text, ok := item.(string)
			if !ok || text == "" {
				return invalid
			}
		}
	}
	return nil
}

// The retired Python handler validated the nested field with the generated
// schema-v2 DTO before forwarding the original params. This walker keeps that
// acceptance boundary: closed key sets accepting both the camelCase wire name
// and the snake_case field name, JSON types, string enums and min-length-one
// strings, identity patterns, numeric bounds and list lengths. It does not
// normalize spellings: the domain decoder still receives the original wire.
type formulaDTOField struct {
	spec     string
	kind     string
	nullable bool
	optional bool
	listItem string
	values   []string
	pattern  string
	minimum  string
	maximum  string
	minItems int
	maxItems int
}

var formulaDTOLogicalTypes = []string{
	"text", "editor", "number", "bool", "date", "dateTime", "time", "autoDate",
	"email", "url", "select", "multiSelect", "relation", "file", "geoPoint",
	"json", "formula", "lookup",
}

func formulaDTOSpecs() map[string]map[string]formulaDTOField {
	return map[string]map[string]formulaDTOField{
		"field": {
			"contract":    {kind: "enum", values: []string{"vibetable.schema.v2"}},
			"identity":    {spec: "fieldIdentity"},
			"displayName": {kind: "text", values: []string{"min"}},
			"help":        {kind: "text"},
			"logicalType": {kind: "enum", values: formulaDTOLogicalTypes},
			"lifecycle":   {spec: "lifecycle"},
			"value":       {spec: "valueSpec"},
			"constraints": {spec: "constraintSpec"},
			"storage":     {spec: "storageSpec"},
			"display":     {spec: "displaySpec"},
			"select":      {spec: "selectSpec", optional: true},
			"relation":    {spec: "relationSpec", optional: true},
			"file":        {spec: "fileSpec", optional: true},
			"json":        {spec: "jsonSpec", optional: true},
			"autoDate":    {spec: "autoDateSpec", optional: true},
			"formula":     {spec: "formulaSpec", optional: true},
			"lookup":      {spec: "lookupSpec", optional: true},
		},
		"fieldIdentity": {
			"fieldId":         {kind: "text", pattern: "^fld_[A-Za-z0-9_-]{8,}$"},
			"physicalName":    {kind: "text", pattern: "^f_[a-z0-9_]{8,}$"},
			"providerFieldId": {kind: "text", pattern: "^pb_[A-Za-z0-9_-]{8,}$"},
		},
		"lifecycle": {
			"state":     {kind: "enum", values: []string{"active", "retired"}},
			"retiredAt": {kind: "text", nullable: true},
		},
		"defaultSpec": {
			"enabled":         {kind: "boolean"},
			"value":           {kind: "any"},
			"source":          {kind: "enum", values: []string{"recommended", "user"}},
			"defaultsVersion": {kind: "integer", minimum: "1"},
		},
		"presenceSpec": {
			"mode":            {kind: "enum", values: []string{"companion", "native", "computed"}},
			"providerFieldId": {kind: "text", nullable: true, optional: true},
			"physicalName":    {kind: "text", nullable: true, optional: true},
		},
		"valueSpec": {
			"required": {kind: "boolean"},
			"default":  {spec: "defaultSpec"},
			"presence": {spec: "presenceSpec"},
		},
		"uniqueSpec": {
			"enabled":     {kind: "boolean"},
			"blankPolicy": {kind: "enum", values: []string{"ignoreMissing"}},
		},
		"rangeSpec": {
			"min": {kind: "numberOrText", nullable: true},
			"max": {kind: "numberOrText", nullable: true},
		},
		"lengthSpec": {
			"min": {kind: "integer", minimum: "0", nullable: true},
			"max": {kind: "integer", minimum: "0", nullable: true},
		},
		"patternSpec": {
			"enabled": {kind: "boolean"},
			"value":   {kind: "text"},
		},
		"domainSpec": {
			"only":   {kind: "strings"},
			"except": {kind: "strings"},
		},
		"selectionSpec": {
			"min": {kind: "integer", minimum: "0"},
			"max": {kind: "integer", minimum: "0", nullable: true},
		},
		"constraintSpec": {
			"unique":    {spec: "uniqueSpec"},
			"range":     {spec: "rangeSpec"},
			"length":    {spec: "lengthSpec"},
			"pattern":   {spec: "patternSpec"},
			"domains":   {spec: "domainSpec"},
			"selection": {spec: "selectionSpec"},
		},
		"storageOptions": {
			"onlyInt":     {kind: "boolean"},
			"maxSize":     {kind: "integer", minimum: "0"},
			"convertURLs": {kind: "boolean"},
			"presentable": {kind: "boolean"},
		},
		"storageSpec": {
			"kind":    {kind: "enum", values: []string{"pocketbase-text", "pocketbase-editor", "pocketbase-number", "pocketbase-bool", "pocketbase-date", "pocketbase-autodate", "pocketbase-email", "pocketbase-url", "pocketbase-select", "pocketbase-relation", "pocketbase-file", "pocketbase-geo-point", "pocketbase-json", "computed"}},
			"options": {spec: "storageOptions"},
		},
		"displaySpec": {
			"kind":              {kind: "enum", values: []string{"text", "editor", "number", "bool", "date", "dateTime", "time", "email", "url", "select", "relation", "file", "geoPoint", "json", "readonly"}},
			"preset":            {kind: "text"},
			"displayScale":      {kind: "integer", minimum: "0", maximum: "15"},
			"scaleMode":         {kind: "enum", values: []string{"max", "fixed"}},
			"trimTrailingZeros": {kind: "boolean"},
			"useGrouping":       {kind: "boolean"},
			"currency":          {kind: "text"},
			"percentStorage":    {kind: "enum", values: []string{"ratio", "percent"}},
			"unit":              {kind: "text", nullable: true},
			"precision":         {kind: "enum", values: []string{"exact", "day", "minute", "second", "millisecond"}},
			"timezone":          {kind: "text"},
			"mode":              {kind: "text"},
			"indent":            {kind: "integerEnum", values: []string{"0", "2", "4"}, nullable: true, optional: true},
			"trueLabel":         {kind: "text"},
			"falseLabel":        {kind: "text"},
		},
		"selectOption": {
			"optionId": {kind: "text", pattern: "^opt_[A-Za-z0-9_-]{8,}$"},
			"label":    {kind: "text", values: []string{"min"}},
			"color":    {kind: "text"},
			"order":    {kind: "integer"},
			"state":    {kind: "enum", values: []string{"active", "retired"}},
		},
		"selectSpec": {
			"options": {listItem: "selectOption", minItems: 1},
		},
		"relationSpec": {
			"targetTableId":     {kind: "text", values: []string{"min"}},
			"cardinality":       {kind: "enum", values: []string{"one", "many"}},
			"deletePolicy":      {kind: "enum", values: []string{"setNull", "restrict", "cascade"}},
			"displayFieldId":    {kind: "text"},
			"pairId":            {kind: "text", values: []string{"min"}, nullable: true, optional: true},
			"reciprocalFieldId": {kind: "text", values: []string{"min"}, nullable: true, optional: true},
		},
		"fileSpec": {
			"maxFiles":         {kind: "integer", minimum: "1"},
			"maxBytesPerFile":  {kind: "integer", minimum: "1"},
			"allowedMimeTypes": {kind: "strings"},
			"thumbs":           {kind: "strings"},
			"protected":        {kind: "boolean"},
		},
		"jsonSpec": {
			"rootType": {kind: "enum", values: []string{"any", "object", "array", "string", "number", "boolean", "null"}},
			"maxSize":  {kind: "integer", minimum: "1"},
			"schema":   {kind: "object"},
		},
		"autoDateSpec": {
			"role": {kind: "enum", values: []string{"createdAt", "updatedAt"}},
		},
		"formulaSpec": {
			"language":   {kind: "enum", values: []string{"cel-v1"}},
			"source":     {kind: "text", values: []string{"min"}},
			"resultType": {kind: "enum", values: formulaDTOLogicalTypes},
		},
		"lookupSpec": {
			"path":          {listItem: "lookupPathStep", minItems: 1, maxItems: 8},
			"targetFieldId": {kind: "text", values: []string{"min"}},
		},
		"lookupPathStep": {
			"relationFieldId": {kind: "text", values: []string{"min"}},
		},
	}
}

// Alternate spellings the generated DTO accepted via populate_by_name.
func formulaDTOAliases() map[string]map[string]string {
	return map[string]map[string]string{
		"field": {
			"display_name": "displayName", "logical_type": "logicalType",
			"auto_date": "autoDate", "json_": "json",
		},
		"fieldIdentity": {
			"field_id": "fieldId", "physical_name": "physicalName",
			"provider_field_id": "providerFieldId",
		},
		"lifecycle":   {"retired_at": "retiredAt"},
		"defaultSpec": {"defaults_version": "defaultsVersion"},
		"presenceSpec": {
			"provider_field_id": "providerFieldId", "physical_name": "physicalName",
		},
		"uniqueSpec": {"blank_policy": "blankPolicy"},
		"domainSpec": {"except_": "except"},
		"storageOptions": {
			"only_int": "onlyInt", "max_size": "maxSize", "convert_u_r_ls": "convertURLs",
		},
		"displaySpec": {
			"display_scale": "displayScale", "scale_mode": "scaleMode",
			"trim_trailing_zeros": "trimTrailingZeros", "use_grouping": "useGrouping",
			"percent_storage": "percentStorage", "true_label": "trueLabel",
			"false_label": "falseLabel",
		},
		"selectOption": {"option_id": "optionId"},
		"relationSpec": {
			"target_table_id": "targetTableId", "delete_policy": "deletePolicy",
			"display_field_id": "displayFieldId", "pair_id": "pairId",
			"reciprocal_field_id": "reciprocalFieldId",
		},
		"fileSpec": {
			"max_files": "maxFiles", "max_bytes_per_file": "maxBytesPerFile",
			"allowed_mime_types": "allowedMimeTypes",
		},
		"jsonSpec":       {"root_type": "rootType", "max_size": "maxSize", "schema_": "schema"},
		"formulaSpec":    {"result_type": "resultType"},
		"lookupSpec":     {"target_field_id": "targetFieldId"},
		"lookupPathStep": {"relation_field_id": "relationFieldId"},
	}
}

var (
	errFormulaDTOInvalid = errors.New("formula field definition is invalid")
	formulaDTOFieldSpecs = formulaDTOSpecs()
	formulaDTOFieldAlias = formulaDTOAliases()
)

func validateFormulaFieldDTO(value any) error {
	return validateFormulaDTOSpec("field", value)
}

func validateFormulaDTOSpec(spec string, value any) error {
	if value == nil {
		return errFormulaDTOInvalid
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errFormulaDTOInvalid
	}
	fields := formulaDTOFieldSpecs[spec]
	aliases := formulaDTOFieldAlias[spec]
	seen := make(map[string]bool, len(object))
	for rawKey, item := range object {
		canonical := rawKey
		if _, known := fields[rawKey]; !known {
			alternate, aliased := aliases[rawKey]
			if !aliased {
				return errFormulaDTOInvalid
			}
			canonical = alternate
		}
		if seen[canonical] {
			return errFormulaDTOInvalid
		}
		seen[canonical] = true
		if err := validateFormulaDTOField(fields[canonical], item); err != nil {
			return err
		}
	}
	for canonical, field := range fields {
		if !seen[canonical] && !field.optional {
			return errFormulaDTOInvalid
		}
	}
	return nil
}

func validateFormulaDTOField(field formulaDTOField, value any) error {
	if value == nil {
		// JsonValue members accept null like the generated DTO did.
		if field.nullable || field.optional || field.kind == "any" {
			return nil
		}
		return errFormulaDTOInvalid
	}
	if field.spec != "" {
		return validateFormulaDTOSpec(field.spec, value)
	}
	if field.listItem != "" {
		items, ok := value.([]any)
		if !ok {
			return errFormulaDTOInvalid
		}
		if len(items) < field.minItems || (field.maxItems > 0 && len(items) > field.maxItems) {
			return errFormulaDTOInvalid
		}
		for _, item := range items {
			if err := validateFormulaDTOSpec(field.listItem, item); err != nil {
				return err
			}
		}
		return nil
	}
	switch field.kind {
	case "text":
		text, ok := value.(string)
		if !ok {
			return errFormulaDTOInvalid
		}
		if field.pattern != "" {
			matched, err := regexp.MatchString(field.pattern, text)
			if err != nil || !matched {
				return errFormulaDTOInvalid
			}
		}
		for _, constraint := range field.values {
			if constraint == "min" && text == "" {
				return errFormulaDTOInvalid
			}
		}
	case "strings":
		items, ok := value.([]any)
		if !ok {
			return errFormulaDTOInvalid
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return errFormulaDTOInvalid
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errFormulaDTOInvalid
		}
	case "integerEnum":
		// Pydantic Literal[0, 2, 4] accepts equal float/bool values even in
		// strict mode; the untouched REST wire may reject them at the next stage.
		if boolean, ok := value.(bool); ok {
			if !boolean {
				return nil
			}
			return errFormulaDTOInvalid
		}
		number, ok := value.(json.Number)
		if !ok {
			return errFormulaDTOInvalid
		}
		literal, err := strconv.ParseFloat(number.String(), 64)
		if err != nil || (literal != 0 && literal != 2 && literal != 4) {
			return errFormulaDTOInvalid
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return errFormulaDTOInvalid
		}
		// Python integers are not int64-limited. Preserve DTO acceptance and
		// leave the subsequent wire decoder's int64 rejection in its own stage.
		integer, ok := new(big.Int).SetString(number.String(), 10)
		if !ok {
			return errFormulaDTOInvalid
		}
		if field.minimum != "" {
			minimum, _ := new(big.Int).SetString(field.minimum, 10)
			if integer.Cmp(minimum) < 0 {
				return errFormulaDTOInvalid
			}
		}
		if field.maximum != "" {
			maximum, _ := new(big.Int).SetString(field.maximum, 10)
			if integer.Cmp(maximum) > 0 {
				return errFormulaDTOInvalid
			}
		}
	case "numberOrText":
		if _, ok := value.(string); !ok {
			number, ok := value.(json.Number)
			if !ok {
				return errFormulaDTOInvalid
			}
			if _, err := strconv.ParseFloat(number.String(), 64); err != nil {
				return errFormulaDTOInvalid
			}
		}
	case "enum":
		text, ok := value.(string)
		if !ok || !containsString(field.values, text) {
			return errFormulaDTOInvalid
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return errFormulaDTOInvalid
		}
	case "any":
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
