package query

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	pbtypes "github.com/pocketbase/pocketbase/tools/types"
)

const (
	maxFilters           = 64
	maxSorts             = 16
	maxInValues          = 200
	defaultAggregateRows = 1000
	maxAggregateRows     = 5000
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type compiler struct {
	descriptor TableDescriptor
	params     map[string]any
	nextParam  int
}

type resolvedField struct {
	sql           string
	descriptor    FieldDescriptor
	jsonExtracted bool
	jsonTypeSQL   string
}

func Compile(descriptor TableDescriptor, input TableQuery) (CompiledQuery, error) {
	normalized, err := Normalize(input)
	if err != nil {
		return CompiledQuery{}, err
	}
	if err := validateDescriptor(descriptor); err != nil {
		return CompiledQuery{}, err
	}
	c := &compiler{descriptor: descriptor, params: make(map[string]any)}
	where, err := c.compileFilterList(normalized.Filters, "filters")
	if err != nil {
		return CompiledQuery{}, err
	}
	archiveWhere, err := c.compileArchive()
	if err != nil {
		return CompiledQuery{}, err
	}
	if archiveWhere != "" {
		if where == "" {
			where = archiveWhere
		} else {
			where = "(" + where + ") AND (" + archiveWhere + ")"
		}
	}
	if keyword := strings.TrimSpace(normalized.Keyword); keyword != "" {
		pattern := "%" + escapeLike(keyword) + "%"
		parts := make([]string, 0)
		names := sortedFieldNames(descriptor.Fields)
		for _, name := range names {
			field := descriptor.Fields[name]
			if field.Searchable && field.Type == FieldTypeText {
				parts = append(parts, fmt.Sprintf(
					`LOWER(CAST(%s AS TEXT)) LIKE LOWER(%s) ESCAPE '\'`,
					quote(field.PhysicalName), c.bind(pattern),
				))
			}
		}
		if len(parts) == 0 {
			return CompiledQuery{}, productError(
				"query.keyword.unsupported", "keyword", "table has no searchable fields", nil)
		}
		keywordWhere := "(" + strings.Join(parts, " OR ") + ")"
		if where == "" {
			where = keywordWhere
		} else {
			where = "(" + where + ") AND " + keywordWhere
		}
	}

	fields := sortedFieldNames(descriptor.Fields)
	selects := make([]string, 0, len(fields))
	for _, name := range fields {
		selects = append(selects, fmt.Sprintf(
			`%s AS %s`, quote(descriptor.Fields[name].PhysicalName), quote(name)))
	}
	whereSQL := ""
	if where != "" {
		whereSQL = " WHERE " + where
	}
	orderBy, err := c.compileSorts(normalized.Sorts)
	if err != nil {
		return CompiledQuery{}, err
	}
	c.params["offset"] = normalized.Offset
	c.params["limit"] = normalized.Limit
	sql := fmt.Sprintf(
		"SELECT %s FROM %s%s ORDER BY %s LIMIT {:limit} OFFSET {:offset}",
		strings.Join(selects, ", "),
		quote(descriptor.PhysicalName),
		whereSQL,
		orderBy,
	)
	return CompiledQuery{
		SQL: sql, CountSQL: "SELECT COUNT(*) FROM " + quote(descriptor.PhysicalName) + whereSQL,
		TotalSQL: "SELECT COUNT(*) FROM " + quote(descriptor.PhysicalName) +
			optionalWhere(archiveWhere),
		Params: c.params, Fields: fields,
	}, nil
}

func CompileAggregate(
	descriptor TableDescriptor,
	input AggregateQuery,
) (string, map[string]any, error) {
	if err := validateDescriptor(descriptor); err != nil {
		return "", nil, err
	}
	if len(input.Metrics) == 0 || len(input.Metrics) > 32 {
		return "", nil, productError("query.aggregate.invalid", "metrics", "between 1 and 32 metrics are required", nil)
	}
	if len(input.GroupBy) > 8 {
		return "", nil, productError("query.aggregate.invalid", "groupBy", "at most 8 group fields are allowed", nil)
	}
	if input.Limit == 0 {
		input.Limit = defaultAggregateRows
	}
	if input.Limit < 1 || input.Limit > maxAggregateRows {
		return "", nil, productError(
			"query.aggregate.invalid_limit", "limit", "limit must be between 1 and 5000", nil)
	}
	filterCount := 0
	for index := range input.Filters {
		if err := normalizeFilter(
			&input.Filters[index],
			fmt.Sprintf("filters[%d]", index),
			0,
			&filterCount,
		); err != nil {
			return "", nil, err
		}
		if index == 0 {
			input.Filters[index].Logic = LogicAnd
		}
	}
	c := &compiler{descriptor: descriptor, params: make(map[string]any)}
	where, err := c.compileFilterList(input.Filters, "filters")
	if err != nil {
		return "", nil, err
	}
	selects := make([]string, 0, len(input.GroupBy)+len(input.Metrics))
	groups := make([]string, 0, len(input.GroupBy))
	outputNames := make(map[string]struct{}, len(input.GroupBy)+len(input.Metrics))
	for index, name := range input.GroupBy {
		outputName := strings.ToLower(name)
		if _, exists := outputNames[outputName]; exists {
			return "", nil, productError(
				"query.aggregate.duplicate_group",
				fmt.Sprintf("groupBy[%d]", index),
				"group fields must be unique",
				nil,
			)
		}
		field, err := c.resolve(name, fmt.Sprintf("groupBy[%d]", index))
		if err != nil {
			return "", nil, err
		}
		if !aggregateGroupTypeAllowed(field.descriptor.Type) {
			return "", nil, productError(
				"query.aggregate.type_mismatch",
				fmt.Sprintf("groupBy[%d]", index),
				"field type cannot be used for grouping",
				nil,
			)
		}
		selects = append(selects, field.sql+" AS "+quote(name))
		groups = append(groups, field.sql)
		outputNames[outputName] = struct{}{}
	}
	for index, metric := range input.Metrics {
		if !identifierPattern.MatchString(metric.Alias) {
			return "", nil, productError("query.aggregate.invalid_alias", fmt.Sprintf("metrics[%d].alias", index), "invalid metric alias", nil)
		}
		outputName := strings.ToLower(metric.Alias)
		if _, exists := outputNames[outputName]; exists {
			return "", nil, productError(
				"query.aggregate.duplicate_alias",
				fmt.Sprintf("metrics[%d].alias", index),
				"metric alias collides with another output column",
				nil,
			)
		}
		var expression string
		switch metric.Function {
		case AggregateCount:
			if metric.Field == "" {
				expression = "COUNT(*)"
			} else {
				field, err := c.resolve(metric.Field, fmt.Sprintf("metrics[%d].field", index))
				if err != nil {
					return "", nil, err
				}
				expression = "COUNT(" + field.sql + ")"
			}
		case AggregateSum, AggregateAvg, AggregateMin, AggregateMax:
			field, err := c.resolve(metric.Field, fmt.Sprintf("metrics[%d].field", index))
			if err != nil {
				return "", nil, err
			}
			if !aggregateMetricTypeAllowed(metric.Function, field.descriptor.Type) {
				return "", nil, productError(
					"query.aggregate.type_mismatch",
					fmt.Sprintf("metrics[%d].field", index),
					"aggregate function is not supported for the field type",
					nil,
				)
			}
			expression = strings.ToUpper(string(metric.Function)) + "(" + field.sql + ")"
		default:
			return "", nil, productError("query.aggregate.invalid_function", fmt.Sprintf("metrics[%d].function", index), "unsupported aggregate function", nil)
		}
		selects = append(selects, expression+" AS "+quote(metric.Alias))
		outputNames[outputName] = struct{}{}
	}
	sql := "SELECT " + strings.Join(selects, ", ") + " FROM " + quote(descriptor.PhysicalName)
	exclusion, err := c.compileArchive()
	if err != nil {
		return "", nil, err
	}
	if exclusion != "" {
		if where == "" {
			where = exclusion
		} else {
			where = "(" + where + ") AND " + exclusion
		}
	}
	if where != "" {
		sql += " WHERE " + where
	}
	if len(groups) > 0 {
		sql += " GROUP BY " + strings.Join(groups, ", ")
		sql += " ORDER BY " + strings.Join(groups, ", ")
	}
	c.params["aggregate_limit"] = input.Limit
	sql += " LIMIT {:aggregate_limit}"
	return sql, c.params, nil
}

func Normalize(input TableQuery) (TableQuery, error) {
	if input.Offset < 0 {
		return TableQuery{}, productError("query.page.invalid", "offset", "offset cannot be negative", nil)
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 500 {
		return TableQuery{}, productError("query.page.invalid", "limit", "limit must be between 1 and 500", nil)
	}
	if len(input.Filters) > maxFilters {
		return TableQuery{}, productError("query.filter.limit", "filters", "too many filters", nil)
	}
	if len(input.Sorts) > maxSorts {
		return TableQuery{}, productError("query.sort.limit", "sorts", "too many sorts", nil)
	}
	if len(input.Keyword) > 256 {
		return TableQuery{}, productError("query.keyword.invalid", "keyword", "keyword is too long", nil)
	}
	input.Keyword = strings.TrimSpace(input.Keyword)
	if input.Filters == nil {
		input.Filters = []FilterExpression{}
	}
	if input.Sorts == nil {
		input.Sorts = []SortCondition{}
	}
	filterCount := 0
	for index := range input.Filters {
		if err := normalizeFilter(
			&input.Filters[index],
			fmt.Sprintf("filters[%d]", index),
			0,
			&filterCount,
		); err != nil {
			return TableQuery{}, err
		}
		if index == 0 {
			input.Filters[index].Logic = LogicAnd
		}
	}
	canonicalSorts := make([]SortCondition, 0, len(input.Sorts))
	seenSorts := make(map[string]struct{}, len(input.Sorts))
	for index := range input.Sorts {
		sortCondition := input.Sorts[index]
		if _, exists := seenSorts[sortCondition.Field]; exists {
			continue
		}
		if sortCondition.Direction == "" {
			sortCondition.Direction = SortAscending
		}
		if sortCondition.NullsLast == nil {
			defaultNullsLast := true
			sortCondition.NullsLast = &defaultNullsLast
		}
		seenSorts[sortCondition.Field] = struct{}{}
		canonicalSorts = append(canonicalSorts, sortCondition)
	}
	input.Sorts = canonicalSorts
	return input, nil
}

func normalizeFilter(
	filter *FilterExpression,
	path string,
	depth int,
	count *int,
) error {
	*count++
	if *count > maxFilters {
		return productError("query.filter.limit", "filters", "too many filters", nil)
	}
	if depth > 8 {
		return productError("query.filter.depth", path, "filter nesting is too deep", nil)
	}
	if filter.Logic == "" {
		filter.Logic = LogicAnd
	}
	if filter.Logic != LogicAnd && filter.Logic != LogicOr {
		return productError("query.filter.invalid_logic", path+".logic", "logic must be AND or OR", nil)
	}
	if filter.GroupLogic == "" {
		filter.GroupLogic = LogicAnd
	}
	if filter.GroupLogic != LogicAnd && filter.GroupLogic != LogicOr {
		return productError("query.filter.invalid_logic", path+".groupLogic", "groupLogic must be AND or OR", nil)
	}
	for index := 1; index < len(filter.Filters); index++ {
		childLogic := filter.Filters[index].Logic
		if childLogic != "" && childLogic != filter.GroupLogic {
			return productError(
				"query.filter.conflicting_group_logic",
				fmt.Sprintf("%s.filters[%d].logic", path, index),
				"child logic conflicts with the enclosing group logic",
				nil,
			)
		}
	}
	for index := range filter.Filters {
		if err := normalizeFilter(
			&filter.Filters[index],
			fmt.Sprintf("%s.filters[%d]", path, index),
			depth+1,
			count,
		); err != nil {
			return err
		}
		if index == 0 {
			filter.Filters[index].Logic = LogicAnd
		} else {
			filter.Filters[index].Logic = filter.GroupLogic
		}
	}
	return nil
}

func (c *compiler) compileFilterList(
	filters []FilterExpression,
	path string,
) (string, error) {
	var result string
	for index, filter := range filters {
		currentPath := fmt.Sprintf("%s[%d]", path, index)
		predicate, err := c.compileFilter(filter, currentPath)
		if err != nil {
			return "", err
		}
		if result == "" {
			result = predicate
			continue
		}
		switch filter.Logic {
		case "", LogicAnd:
			result = "(" + result + ") AND (" + predicate + ")"
		case LogicOr:
			result = "(" + result + ") OR (" + predicate + ")"
		default:
			return "", productError("query.filter.invalid_logic", currentPath+".logic", "logic must be AND or OR", nil)
		}
	}
	return result, nil
}

func (c *compiler) compileFilter(filter FilterExpression, path string) (string, error) {
	if len(filter.Filters) > 0 {
		if filter.Field != "" || filter.Operator != "" {
			return "", productError("query.filter.invalid_group", path, "filter group cannot also be a predicate", nil)
		}
		children := append([]FilterExpression(nil), filter.Filters...)
		for index := range children {
			if index > 0 {
				children[index].Logic = filter.GroupLogic
			}
		}
		return c.compileFilterList(children, path+".filters")
	}
	field, err := c.resolve(filter.Field, path+".field")
	if err != nil {
		return "", err
	}
	return c.compilePredicate(field, filter, path)
}

func (c *compiler) compilePredicate(
	field resolvedField,
	filter FilterExpression,
	path string,
) (string, error) {
	op := filter.Operator
	switch op {
	case OperatorIsNull:
		if filter.Value != nil {
			return "", invalidValue(path, "is_null takes no value")
		}
		return logicalNullPredicate(field), nil
	case OperatorIsNotNull:
		if filter.Value != nil {
			return "", invalidValue(path, "is_not_null takes no value")
		}
		return "NOT (" + logicalNullPredicate(field) + ")", nil
	case OperatorRegex:
		return "", productError("query.operator.unsupported", path+".operator", "regex is not portable in SQLite", nil)
	}
	if !operatorAllowed(field.descriptor.Type, op) {
		return "", productError(
			"query.operator.type_mismatch",
			path+".operator",
			"operator is not supported for the field type",
			nil,
		)
	}
	if filter.Value == nil {
		return "", invalidValue(path, "operator requires a value")
	}
	if field.descriptor.Enum != nil {
		predicate, handled, enumErr := c.compileEnumPredicate(field, filter, path)
		if enumErr != nil {
			return "", enumErr
		}
		if handled {
			return predicate, nil
		}
	}
	if err := validateTypedFilterValue(
		field.descriptor.Type,
		op,
		filter.Value,
		path,
	); err != nil {
		return "", err
	}
	if field.descriptor.Type == FieldTypeDate {
		normalized, normalizeErr := normalizeDateFilterValue(
			filter.Value,
			path,
			field.descriptor.AutoDate,
		)
		if normalizeErr != nil {
			return "", normalizeErr
		}
		filter.Value = normalized
	}
	if field.descriptor.Type == fieldTypeJSONScalar {
		return c.compileJSONScalar(field, filter, path)
	}
	if field.descriptor.Type == FieldTypeMultiRelation {
		return c.compileMultiRelation(field, filter, path)
	}
	switch op {
	case OperatorEqual:
		if !isScalar(filter.Value) {
			return "", invalidValue(path, "comparison value must be a finite scalar")
		}
		return field.sql + " = " + c.bind(filter.Value), nil
	case OperatorNotEqual:
		if !isScalar(filter.Value) {
			return "", invalidValue(path, "comparison value must be a finite scalar")
		}
		return field.sql + " != " + c.bind(filter.Value), nil
	case OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq:
		if !isScalar(filter.Value) {
			return "", invalidValue(path, "comparison value must be a finite scalar")
		}
		operators := map[Operator]string{
			OperatorGreater: ">", OperatorLess: "<", OperatorGreaterEq: ">=", OperatorLessEq: "<=",
		}
		return field.sql + " " + operators[op] + " " + c.bind(filter.Value), nil
	case OperatorContains, OperatorStartsWith, OperatorEndsWith:
		value, ok := filter.Value.(string)
		if !ok {
			return "", invalidValue(path, "text operator requires a string")
		}
		switch op {
		case OperatorContains:
			return "instr(CAST(" + field.sql + " AS TEXT), " + c.bind(value) + ") > 0", nil
		case OperatorStartsWith:
			return field.sql + ` LIKE ` + c.bind(escapeLike(value)+"%") + ` ESCAPE '\'`, nil
		default:
			return field.sql + ` LIKE ` + c.bind("%"+escapeLike(value)) + ` ESCAPE '\'`, nil
		}
	case OperatorBetween:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) != 2 {
			return "", invalidValue(path, "between requires exactly two values")
		}
		if !isScalar(values[0]) || !isScalar(values[1]) {
			return "", invalidValue(path, "between values must be finite scalars")
		}
		return field.sql + " BETWEEN " + c.bind(values[0]) + " AND " + c.bind(values[1]), nil
	case OperatorIn:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return "", invalidValue(path, "in requires between 1 and 200 values")
		}
		params := make([]string, 0, len(values))
		for _, value := range values {
			if !isScalar(value) {
				return "", invalidValue(path, "in values must be finite scalars")
			}
			params = append(params, c.bind(value))
		}
		return field.sql + " IN (" + strings.Join(params, ", ") + ")", nil
	default:
		return "", productError("query.operator.unknown", path+".operator", "unknown filter operator", nil)
	}
}

func normalizeDateFilterValue(
	value any,
	path string,
	requireTimestamp bool,
) (any, error) {
	if values, ok := asSlice(value); ok {
		normalized := make([]any, len(values))
		for index, item := range values {
			text, textOK := item.(string)
			if !textOK {
				return nil, productError(
					"query.filter.invalid_value",
					fmt.Sprintf("%s.value[%d]", path, index),
					"date comparison value must be a string",
					nil,
				)
			}
			normalizedValue, err := normalizeDateFilterText(
				text,
				path,
				requireTimestamp,
			)
			if err != nil {
				return nil, err
			}
			normalized[index] = normalizedValue
		}
		return normalized, nil
	}
	text, ok := value.(string)
	if !ok {
		return value, nil
	}
	return normalizeDateFilterText(text, path, requireTimestamp)
}

func normalizeDateFilterText(
	value string,
	path string,
	requireTimestamp bool,
) (string, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(pbtypes.DefaultDateLayout), nil
	}
	if requireTimestamp {
		return "", invalidValue(
			path,
			"automatic date comparison value must be an RFC 3339 timestamp",
		)
	}
	if parsed, err := time.Parse(pbtypes.DefaultDateLayout, value); err == nil {
		return parsed.UTC().Format(pbtypes.DefaultDateLayout), nil
	}
	if _, err := time.Parse(time.DateOnly, value); err == nil {
		// Preserve the existing date-only prefix semantics for product date
		// fields while normalizing timestamp/autoDate instants.
		return value, nil
	}
	return "", invalidValue(path, "date comparison value must be an ISO 8601 date or timestamp")
}

func (c *compiler) compileEnumPredicate(
	field resolvedField,
	filter FilterExpression,
	path string,
) (string, bool, error) {
	enum := field.descriptor.Enum
	if enum == nil {
		return "", false, nil
	}
	bindCandidates := func(value any) ([]string, error) {
		candidates, ok := enumStorageCandidates(enum, value)
		if !ok {
			return nil, invalidValue(path, "select value is not an allowed option")
		}
		params := make([]string, len(candidates))
		for index, candidate := range candidates {
			params[index] = c.bind(candidate)
		}
		return params, nil
	}
	if enum.Multiple {
		if filter.Operator != OperatorContains {
			return "", false, nil
		}
		params, err := bindCandidates(filter.Value)
		if err != nil {
			return "", true, err
		}
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM json_each(%s) WHERE value IN (%s))",
			field.sql,
			strings.Join(params, ", "),
		), true, nil
	}
	switch filter.Operator {
	case OperatorEqual, OperatorNotEqual:
		params, err := bindCandidates(filter.Value)
		if err != nil {
			return "", true, err
		}
		operator := "IN"
		if filter.Operator == OperatorNotEqual {
			operator = "NOT IN"
		}
		return field.sql + " " + operator + " (" +
			strings.Join(params, ", ") + ")", true, nil
	case OperatorIn:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return "", true, invalidValue(path, "in requires between 1 and 200 values")
		}
		params := make([]string, 0, len(values))
		seen := map[string]struct{}{}
		for _, value := range values {
			candidates, err := bindCandidates(value)
			if err != nil {
				return "", true, err
			}
			for _, param := range candidates {
				if _, duplicate := seen[param]; duplicate {
					continue
				}
				seen[param] = struct{}{}
				params = append(params, param)
			}
		}
		return field.sql + " IN (" + strings.Join(params, ", ") + ")", true, nil
	default:
		return "", false, nil
	}
}

func validateTypedFilterValue(
	fieldType FieldType,
	operator Operator,
	value any,
	path string,
) error {
	values := []any{value}
	if operator == OperatorIn || operator == OperatorBetween {
		parsed, ok := asSlice(value)
		if !ok {
			return nil // Shape-specific validation emits the stable error below.
		}
		values = parsed
	}
	for _, item := range values {
		valid := false
		switch fieldType {
		case FieldTypeText, FieldTypeDate, FieldTypeRelation, FieldTypeMultiRelation:
			_, valid = item.(string)
		case FieldTypeNumber:
			valid = isNumber(item)
		case FieldTypeBool:
			_, valid = item.(bool)
		case FieldTypeJSON:
			_, valid = item.(string)
		case fieldTypeJSONScalar:
			switch operator {
			case OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq,
				OperatorBetween:
				valid = isNumber(item)
			case OperatorContains, OperatorStartsWith, OperatorEndsWith:
				_, valid = item.(string)
			case OperatorEqual, OperatorNotEqual, OperatorIn:
				valid = jsonScalarClass(item) != ""
			}
		}
		if !valid {
			return invalidValue(path, "value type does not match the field type")
		}
	}
	if fieldType == fieldTypeJSONScalar &&
		(operator == OperatorIn || operator == OperatorBetween) &&
		len(values) > 1 {
		class := jsonScalarClass(values[0])
		for _, item := range values[1:] {
			if jsonScalarClass(item) != class {
				return invalidValue(path, "JSON scalar list values must have one type")
			}
		}
	}
	return nil
}

func (c *compiler) compileJSONScalar(
	field resolvedField,
	filter FilterExpression,
	path string,
) (string, error) {
	typeGuard := func(class string) string {
		switch class {
		case "number":
			return field.jsonTypeSQL + " IN ('integer', 'real')"
		case "bool":
			return field.jsonTypeSQL + " IN ('true', 'false')"
		default:
			return field.jsonTypeSQL + " = 'text'"
		}
	}
	guarded := func(class, predicate string) string {
		return "(" + typeGuard(class) + " AND " + predicate + ")"
	}

	switch filter.Operator {
	case OperatorEqual, OperatorNotEqual:
		operator := "="
		if filter.Operator == OperatorNotEqual {
			operator = "!="
		}
		return guarded(
			jsonScalarClass(filter.Value),
			field.sql+" "+operator+" "+c.bind(filter.Value),
		), nil
	case OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq:
		operators := map[Operator]string{
			OperatorGreater: ">", OperatorLess: "<", OperatorGreaterEq: ">=", OperatorLessEq: "<=",
		}
		return guarded(
			"number",
			field.sql+" "+operators[filter.Operator]+" "+c.bind(filter.Value),
		), nil
	case OperatorContains, OperatorStartsWith, OperatorEndsWith:
		value := filter.Value.(string)
		var predicate string
		switch filter.Operator {
		case OperatorContains:
			predicate = "instr(" + field.sql + ", " + c.bind(value) + ") > 0"
		case OperatorStartsWith:
			predicate = field.sql + ` LIKE ` + c.bind(escapeLike(value)+"%") + ` ESCAPE '\'`
		default:
			predicate = field.sql + ` LIKE ` + c.bind("%"+escapeLike(value)) + ` ESCAPE '\'`
		}
		return guarded("text", predicate), nil
	case OperatorBetween:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) != 2 {
			return "", invalidValue(path, "between requires exactly two values")
		}
		return guarded(
			"number",
			field.sql+" BETWEEN "+c.bind(values[0])+" AND "+c.bind(values[1]),
		), nil
	case OperatorIn:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return "", invalidValue(path, "in requires between 1 and 200 values")
		}
		params := make([]string, 0, len(values))
		for _, value := range values {
			params = append(params, c.bind(value))
		}
		return guarded(
			jsonScalarClass(values[0]),
			field.sql+" IN ("+strings.Join(params, ", ")+")",
		), nil
	default:
		return "", productError(
			"query.operator.unsupported",
			path+".operator",
			"operator is not supported for a JSON scalar",
			nil,
		)
	}
}

func jsonScalarClass(value any) string {
	switch value.(type) {
	case string:
		return "text"
	case bool:
		return "bool"
	default:
		if isNumber(value) {
			return "number"
		}
		return ""
	}
}

func (c *compiler) compileArchive() (string, error) {
	mode := c.descriptor.ArchiveMode
	if mode == "" {
		mode = ArchiveModeNone
	}
	if mode == ArchiveModeNone {
		if c.descriptor.ArchiveField != "" {
			return "", productError(
				"query.archive.invalid",
				"archiveField",
				"archive field requires an explicit archive mode",
				nil,
			)
		}
		return "", nil
	}
	archive, ok := c.descriptor.Fields[c.descriptor.ArchiveField]
	if c.descriptor.ArchiveField == "" || !ok {
		return "", productError(
			"query.archive.invalid",
			"archiveField",
			"archive field is missing from the query descriptor",
			nil,
		)
	}
	fieldSQL := quote(archive.PhysicalName)
	switch mode {
	case ArchiveModeStatus:
		if archive.Type != FieldTypeText || !isScalar(c.descriptor.ArchiveValue) {
			return "", productError(
				"query.archive.invalid",
				"archiveValue",
				"status archive requires a text field and scalar archived value",
				nil,
			)
		}
		value, ok := c.descriptor.ArchiveValue.(string)
		if !ok || value == "" {
			return "", productError(
				"query.archive.invalid",
				"archiveValue",
				"status archive value must be a non-empty string",
				nil,
			)
		}
		return "(" + fieldSQL + " IS NULL OR " + fieldSQL + " = '' OR " +
			fieldSQL + " != " + c.bind(value) + ")", nil
	case ArchiveModeDeletedAt:
		if archive.Type != FieldTypeDate && archive.Type != FieldTypeText {
			return "", productError(
				"query.archive.invalid",
				"archiveField",
				"deletedAt archive requires a date or text field",
				nil,
			)
		}
		return "(" + fieldSQL + " IS NULL OR " + fieldSQL + " = '')", nil
	default:
		return "", productError(
			"query.archive.invalid",
			"archiveMode",
			"unsupported archive mode",
			nil,
		)
	}
}

func (c *compiler) compileMultiRelation(
	field resolvedField,
	filter FilterExpression,
	path string,
) (string, error) {
	switch filter.Operator {
	case OperatorEqual:
		if !isScalar(filter.Value) {
			return "", invalidValue(path, "relation value must be a finite scalar")
		}
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM json_each(%s) WHERE value = %s)",
			field.sql, c.bind(filter.Value),
		), nil
	case OperatorNotEqual:
		if !isScalar(filter.Value) {
			return "", invalidValue(path, "relation value must be a finite scalar")
		}
		return fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM json_each(%s) WHERE value = %s)",
			field.sql, c.bind(filter.Value),
		), nil
	case OperatorIn:
		values, ok := asSlice(filter.Value)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return "", invalidValue(path, "in requires between 1 and 200 values")
		}
		params := make([]string, 0, len(values))
		for _, value := range values {
			if !isScalar(value) {
				return "", invalidValue(path, "relation values must be finite scalars")
			}
			params = append(params, c.bind(value))
		}
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM json_each(%s) WHERE value IN (%s))",
			field.sql, strings.Join(params, ", "),
		), nil
	default:
		return "", productError("query.operator.unsupported", path+".operator", "operator is not supported for a multi relation", nil)
	}
}

func logicalNullPredicate(field resolvedField) string {
	switch field.descriptor.Type {
	case FieldTypeText, FieldTypeDate, FieldTypeRelation:
		return "(" + field.sql + " IS NULL OR " + field.sql + " = '')"
	case FieldTypeMultiRelation:
		return "(" + field.sql + " IS NULL OR " + field.sql + " = '' OR " +
			"(json_valid(" + field.sql + ") AND json_type(" + field.sql + ") = 'array' AND " +
			"json_array_length(" + field.sql + ") = 0))"
	case FieldTypeJSON:
		if field.jsonExtracted {
			return field.sql + " IS NULL"
		}
		return "(" + field.sql + " IS NULL OR " + field.sql + " = '' OR " +
			"(json_valid(" + field.sql + ") AND json_type(" + field.sql + ") = 'null'))"
	default:
		return field.sql + " IS NULL"
	}
}

func operatorAllowed(fieldType FieldType, operator Operator) bool {
	switch fieldType {
	case FieldTypeText:
		switch operator {
		case OperatorEqual, OperatorNotEqual, OperatorIn,
			OperatorContains, OperatorStartsWith, OperatorEndsWith:
			return true
		}
	case FieldTypeNumber:
		switch operator {
		case OperatorEqual, OperatorNotEqual, OperatorIn,
			OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq,
			OperatorBetween:
			return true
		}
	case FieldTypeBool:
		return operator == OperatorEqual || operator == OperatorNotEqual || operator == OperatorIn
	case FieldTypeDate:
		switch operator {
		case OperatorEqual, OperatorNotEqual, OperatorIn,
			OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq,
			OperatorBetween:
			return true
		}
	case FieldTypeJSON:
		return operator == OperatorContains
	case fieldTypeJSONScalar:
		switch operator {
		case OperatorEqual, OperatorNotEqual, OperatorIn,
			OperatorGreater, OperatorLess, OperatorGreaterEq, OperatorLessEq,
			OperatorBetween, OperatorContains, OperatorStartsWith, OperatorEndsWith:
			return true
		}
	case FieldTypeRelation, FieldTypeMultiRelation:
		return operator == OperatorEqual || operator == OperatorNotEqual || operator == OperatorIn
	}
	return false
}

func aggregateGroupTypeAllowed(fieldType FieldType) bool {
	switch fieldType {
	case FieldTypeText, FieldTypeNumber, FieldTypeBool, FieldTypeDate, FieldTypeRelation:
		return true
	default:
		return false
	}
}

func aggregateMetricTypeAllowed(function AggregateFunction, fieldType FieldType) bool {
	switch function {
	case AggregateSum, AggregateAvg:
		return fieldType == FieldTypeNumber
	case AggregateMin, AggregateMax:
		return fieldType == FieldTypeText || fieldType == FieldTypeNumber || fieldType == FieldTypeDate
	default:
		return false
	}
}

func (c *compiler) resolve(fieldPath string, path string) (resolvedField, error) {
	segments := strings.Split(fieldPath, ".")
	if len(segments) == 0 || !identifierPattern.MatchString(segments[0]) {
		return resolvedField{}, unknownField(path, fieldPath)
	}
	field, ok := c.descriptor.Fields[segments[0]]
	if !ok {
		return resolvedField{}, unknownField(path, fieldPath)
	}
	sql := quote(field.PhysicalName)
	if len(segments) == 1 {
		return resolvedField{sql: sql, descriptor: field}, nil
	}
	for _, segment := range segments[1:] {
		if !identifierPattern.MatchString(segment) {
			return resolvedField{}, unknownField(path, fieldPath)
		}
	}
	switch field.Type {
	case FieldTypeJSON:
		jsonPath := "$." + strings.Join(segments[1:], ".")
		return resolvedField{
			sql:           "json_extract(" + sql + ", '" + jsonPath + "')",
			descriptor:    FieldDescriptor{Type: fieldTypeJSONScalar},
			jsonExtracted: true,
			jsonTypeSQL:   "json_type(" + sql + ", '" + jsonPath + "')",
		}, nil
	case FieldTypeRelation:
		if field.Relation == nil || field.Relation.Multiple || len(segments) != 2 {
			return resolvedField{}, unknownField(path, fieldPath)
		}
		target, ok := field.Relation.Fields[segments[1]]
		if !ok {
			return resolvedField{}, unknownField(path, fieldPath)
		}
		return resolvedField{
			sql: fmt.Sprintf(
				"(SELECT r0.%s FROM %s r0 WHERE r0.%s = %s LIMIT 1)",
				quote(target.PhysicalName),
				quote(field.Relation.TableName),
				quote(field.Relation.PrimaryKey),
				sql,
			),
			descriptor: target,
		}, nil
	default:
		return resolvedField{}, unknownField(path, fieldPath)
	}
}

func (c *compiler) compileSorts(sorts []SortCondition) (string, error) {
	parts := make([]string, 0, len(sorts)*2+1)
	seen := make(map[string]struct{})
	for index, sortCondition := range sorts {
		if _, ok := seen[sortCondition.Field]; ok {
			continue
		}
		field, err := c.resolve(sortCondition.Field, fmt.Sprintf("sorts[%d].field", index))
		if err != nil {
			return "", err
		}
		direction := sortCondition.Direction
		if direction != SortAscending && direction != SortDescending {
			return "", productError("query.sort.invalid_direction", fmt.Sprintf("sorts[%d].direction", index), "direction must be asc or desc", nil)
		}
		nullsLast := true
		if sortCondition.NullsLast != nil {
			nullsLast = *sortCondition.NullsLast
		}
		nullDirection := "ASC"
		if !nullsLast {
			nullDirection = "DESC"
		}
		parts = append(parts, field.sql+" IS NULL "+nullDirection)
		parts = append(parts, field.sql+" "+strings.ToUpper(string(direction)))
		seen[sortCondition.Field] = struct{}{}
	}
	if _, ok := seen[c.descriptor.PrimaryKey]; !ok {
		primary, err := c.resolve(c.descriptor.PrimaryKey, "primaryKey")
		if err != nil {
			return "", productError("query.primary_key.invalid", "primaryKey", "primary key is not queryable", nil)
		}
		parts = append(parts, primary.sql+" ASC")
	}
	return strings.Join(parts, ", "), nil
}

func validateDescriptor(descriptor TableDescriptor) error {
	if !identifierPattern.MatchString(descriptor.PhysicalName) {
		return productError("query.table.invalid", "table", "invalid physical table", nil)
	}
	for name, field := range descriptor.Fields {
		if !identifierPattern.MatchString(name) || !identifierPattern.MatchString(field.PhysicalName) {
			return productError("query.schema.invalid", "fields", "query schema contains an invalid field", nil)
		}
		if field.Relation != nil {
			if !identifierPattern.MatchString(field.Relation.TableName) ||
				!identifierPattern.MatchString(field.Relation.PrimaryKey) {
				return productError("query.schema.invalid", "fields."+name+".relation", "relation schema contains an invalid identifier", nil)
			}
			for targetName, targetField := range field.Relation.Fields {
				if !identifierPattern.MatchString(targetName) ||
					!identifierPattern.MatchString(targetField.PhysicalName) {
					return productError("query.schema.invalid", "fields."+name+".relation.fields", "relation schema contains an invalid field", nil)
				}
			}
		}
	}
	return nil
}

func (c *compiler) bind(value any) string {
	name := fmt.Sprintf("p%d", c.nextParam)
	c.nextParam++
	c.params[name] = databaseValue(value)
	return "{:" + name + "}"
}

func databaseValue(value any) any {
	number, ok := value.(json.Number)
	if !ok {
		return value
	}
	if integer, err := number.Int64(); err == nil {
		return integer
	}
	floatingPoint, err := number.Float64()
	if err == nil && !math.IsNaN(floatingPoint) && !math.IsInf(floatingPoint, 0) {
		return floatingPoint
	}
	return value
}

func sortedFieldNames(fields map[string]FieldDescriptor) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func quote(identifier string) string { return `"` + identifier + `"` }

func optionalWhere(where string) string {
	if where == "" {
		return ""
	}
	return " WHERE " + where
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func asSlice(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	case []string:
		result := make([]any, len(values))
		for index := range values {
			result[index] = values[index]
		}
		return result, true
	default:
		return nil, false
	}
}

func isScalar(value any) bool {
	switch typed := value.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		_, err := typed.Float64()
		return err == nil
	default:
		return false
	}
}

func isNumber(value any) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32:
		return true
	case uint64:
		return typed <= math.MaxInt64
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		number, err := typed.Float64()
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return false
	}
}

func invalidValue(path, message string) *ProductError {
	return productError("query.filter.invalid_value", path+".value", message, nil)
}

func unknownField(path, field string) *ProductError {
	return productError("query.field.unknown", path, "field is not queryable", map[string]any{"field": field})
}

func productError(code, path, message string, details map[string]any) *ProductError {
	return &ProductError{Code: code, Path: path, Message: message, Details: details}
}
