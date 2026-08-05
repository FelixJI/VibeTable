package query

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Operator string

const (
	OperatorContains   Operator = "contains"
	OperatorEqual      Operator = "eq"
	OperatorNotEqual   Operator = "ne"
	OperatorStartsWith Operator = "starts_with"
	OperatorEndsWith   Operator = "ends_with"
	OperatorGreater    Operator = "gt"
	OperatorLess       Operator = "lt"
	OperatorGreaterEq  Operator = "gte"
	OperatorLessEq     Operator = "lte"
	OperatorBetween    Operator = "between"
	OperatorIn         Operator = "in"
	OperatorIsNull     Operator = "is_null"
	OperatorIsNotNull  Operator = "is_not_null"
	OperatorRegex      Operator = "regex"
)

type Logic string

const (
	LogicAnd Logic = "AND"
	LogicOr  Logic = "OR"
)

type SortDirection string

const (
	SortAscending  SortDirection = "asc"
	SortDescending SortDirection = "desc"
)

type FieldType string

const (
	FieldTypeText          FieldType = "text"
	FieldTypeNumber        FieldType = "number"
	FieldTypeBool          FieldType = "bool"
	FieldTypeDate          FieldType = "date"
	FieldTypeJSON          FieldType = "json"
	FieldTypeRelation      FieldType = "relation"
	FieldTypeMultiRelation FieldType = "multiRelation"
	// fieldTypeJSONScalar is an internal descriptor produced when resolving a
	// path below a JSON field. It never appears in normalized schema or on the
	// wire; it lets the compiler apply scalar-only operators without treating
	// the containing JSON document as untyped text.
	fieldTypeJSONScalar FieldType = "jsonScalar"
)

type ArchiveMode string

const (
	ArchiveModeNone      ArchiveMode = "none"
	ArchiveModeStatus    ArchiveMode = "status"
	ArchiveModeDeletedAt ArchiveMode = "deletedAt"
)

type FilterExpression struct {
	Field      string             `json:"field,omitempty"`
	Operator   Operator           `json:"operator,omitempty"`
	Value      any                `json:"value,omitempty"`
	Logic      Logic              `json:"logic,omitempty"`
	Filters    []FilterExpression `json:"filters,omitempty"`
	GroupLogic Logic              `json:"groupLogic,omitempty"`
}

type SortCondition struct {
	Field     string        `json:"field"`
	Direction SortDirection `json:"direction,omitempty"`
	NullsLast *bool         `json:"nullsLast,omitempty"`
}

type TableQuery struct {
	Keyword string             `json:"keyword,omitempty"`
	Filters []FilterExpression `json:"filters,omitempty"`
	Sorts   []SortCondition    `json:"sorts,omitempty"`
	Offset  int                `json:"offset"`
	Limit   int                `json:"limit"`
}

// UnmarshalJSON preserves the frozen TableQuery wire distinction between an
// omitted optional/defaulted property and an explicitly invalid zero or null.
// Direct Go callers still use Normalize's zero-value defaults.
func (query *TableQuery) UnmarshalJSON(data []byte) error {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(data, &properties); err != nil {
		return fmt.Errorf("decode table query: %w", err)
	}
	for name := range properties {
		switch name {
		case "keyword", "filters", "sorts", "offset", "limit":
		default:
			return fmt.Errorf("decode table query: unknown field %q", name)
		}
	}

	result := TableQuery{Limit: 100}
	if raw, ok := properties["keyword"]; ok && !isJSONNull(raw) {
		if err := decodeStrictJSON(raw, &result.Keyword); err != nil {
			return fmt.Errorf("decode table query keyword: %w", err)
		}
	}
	if raw, ok := properties["filters"]; ok {
		if isJSONNull(raw) {
			return fmt.Errorf("decode table query filters: null is not allowed")
		}
		if err := decodeStrictJSON(raw, &result.Filters); err != nil {
			return fmt.Errorf("decode table query filters: %w", err)
		}
	} else {
		result.Filters = []FilterExpression{}
	}
	if raw, ok := properties["sorts"]; ok {
		if isJSONNull(raw) {
			return fmt.Errorf("decode table query sorts: null is not allowed")
		}
		var encodedSorts []json.RawMessage
		if err := json.Unmarshal(raw, &encodedSorts); err != nil {
			return fmt.Errorf("decode table query sorts: %w", err)
		}
		result.Sorts = make([]SortCondition, len(encodedSorts))
		for index, encoded := range encodedSorts {
			var sortProperties map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &sortProperties); err != nil {
				return fmt.Errorf("decode table query sorts[%d]: %w", index, err)
			}
			if nullsLast, exists := sortProperties["nullsLast"]; exists &&
				isJSONNull(nullsLast) {
				return fmt.Errorf(
					"decode table query sorts[%d].nullsLast: null is not allowed",
					index,
				)
			}
			if err := decodeStrictJSON(encoded, &result.Sorts[index]); err != nil {
				return fmt.Errorf("decode table query sorts[%d]: %w", index, err)
			}
			if result.Sorts[index].NullsLast == nil {
				defaultNullsLast := true
				result.Sorts[index].NullsLast = &defaultNullsLast
			}
		}
	} else {
		result.Sorts = []SortCondition{}
	}
	if raw, ok := properties["offset"]; ok {
		if isJSONNull(raw) {
			return fmt.Errorf("decode table query offset: null is not allowed")
		}
		if err := json.Unmarshal(raw, &result.Offset); err != nil {
			return fmt.Errorf("decode table query offset: %w", err)
		}
	}
	if raw, ok := properties["limit"]; ok {
		if isJSONNull(raw) {
			return fmt.Errorf("decode table query limit: null is not allowed")
		}
		if err := json.Unmarshal(raw, &result.Limit); err != nil {
			return fmt.Errorf("decode table query limit: %w", err)
		}
		if result.Limit < 1 || result.Limit > 500 {
			return fmt.Errorf("decode table query limit: must be between 1 and 500")
		}
	}

	*query = result
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	return decoder.Decode(target)
}

func isJSONNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

type RelationDescriptor struct {
	TableName  string                     `json:"tableName"`
	PrimaryKey string                     `json:"primaryKey"`
	Fields     map[string]FieldDescriptor `json:"fields"`
	Multiple   bool                       `json:"multiple"`
}

type EnumValueDescriptor struct {
	Value              any    `json:"value"`
	StorageValue       string `json:"storageValue"`
	LegacyStorageValue string `json:"legacyStorageValue,omitempty"`
}

type EnumDescriptor struct {
	Multiple bool                  `json:"multiple"`
	Options  []EnumValueDescriptor `json:"options"`
}

type FieldDescriptor struct {
	PhysicalName string              `json:"physicalName"`
	Type         FieldType           `json:"type"`
	AutoDate     bool                `json:"autoDate,omitempty"`
	Searchable   bool                `json:"searchable,omitempty"`
	Relation     *RelationDescriptor `json:"relation,omitempty"`
	Enum         *EnumDescriptor     `json:"enum,omitempty"`
}

type TableDescriptor struct {
	DatabaseID     string                     `json:"databaseId"`
	TableID        string                     `json:"tableId"`
	PhysicalName   string                     `json:"physicalName"`
	PrimaryKey     string                     `json:"primaryKey"`
	SchemaRevision string                     `json:"schemaRevision"`
	DataRevision   int64                      `json:"dataRevision"`
	Fields         map[string]FieldDescriptor `json:"fields"`
	// DigestFields includes every normalized product field, including
	// non-queryable secrets. QueryPort hashes their raw values server-side but
	// never returns those values.
	DigestFields []string `json:"-"`
	// PresenceFields maps product field aliases to hidden companion columns.
	// QueryCompiler selects them only as transport-internal projection inputs.
	PresenceFields map[string]string `json:"-"`
	ArchiveMode    ArchiveMode       `json:"archiveMode,omitempty"`
	ArchiveField   string            `json:"archiveField,omitempty"`
	ArchiveValue   any               `json:"archiveValue,omitempty"`
}

type CompiledQuery struct {
	SQL      string         `json:"sql"`
	CountSQL string         `json:"countSql"`
	TotalSQL string         `json:"totalSql"`
	Params   map[string]any `json:"params"`
	Fields   []string       `json:"fields"`
}

type QuerySnapshot struct {
	SnapshotID      string     `json:"snapshotId"`
	Digest          string     `json:"digest"`
	DatabaseID      string     `json:"databaseId"`
	Table           string     `json:"table"`
	SchemaRevision  string     `json:"schemaRevision"`
	DataRevision    int64      `json:"dataRevision"`
	NormalizedQuery TableQuery `json:"normalizedQuery"`
}

type SnapshotValidation struct {
	Valid                 bool   `json:"valid"`
	Reason                string `json:"reason,omitempty"`
	CurrentDataRevision   int64  `json:"currentDataRevision"`
	CurrentSchemaRevision string `json:"currentSchemaRevision"`
}

type Page struct {
	Rows         []map[string]any `json:"rows"`
	Offset       int              `json:"offset"`
	Limit        int              `json:"limit"`
	FilteredRows int64            `json:"filteredRows"`
	TotalRows    int64            `json:"totalRows"`
	Snapshot     QuerySnapshot    `json:"querySnapshot"`
}

type AggregateFunction string

const (
	AggregateCount AggregateFunction = "count"
	AggregateSum   AggregateFunction = "sum"
	AggregateAvg   AggregateFunction = "avg"
	AggregateMin   AggregateFunction = "min"
	AggregateMax   AggregateFunction = "max"
)

type AggregateMetric struct {
	Function AggregateFunction `json:"function"`
	Field    string            `json:"field,omitempty"`
	Alias    string            `json:"alias"`
}

type AggregateQuery struct {
	Filters []FilterExpression `json:"filters,omitempty"`
	GroupBy []string           `json:"groupBy,omitempty"`
	Metrics []AggregateMetric  `json:"metrics"`
	Limit   int                `json:"limit,omitempty"`
}

type AggregateResult struct {
	Rows []map[string]any `json:"rows"`
}

type GroupBucket string

const (
	GroupBucketValue   GroupBucket = "value"
	GroupBucketYear    GroupBucket = "year"
	GroupBucketQuarter GroupBucket = "quarter"
	GroupBucketMonth   GroupBucket = "month"
	GroupBucketWeek    GroupBucket = "week"
	GroupBucketDay     GroupBucket = "day"
	GroupBucketHour    GroupBucket = "hour"
)

type GroupSpec struct {
	Field     string        `json:"field"`
	Direction SortDirection `json:"direction,omitempty"`
	Bucket    GroupBucket   `json:"bucket,omitempty"`
}

type SummarySpec struct {
	Field    string            `json:"field"`
	Function AggregateFunction `json:"function"`
}

// ViewQuery is the single authoritative read model used by every product
// layout. Record and group pagination are independent; summaries always use
// the complete filtered result rather than the current record page.
type ViewQuery struct {
	Query       TableQuery    `json:"query"`
	Groups      []GroupSpec   `json:"groups,omitempty"`
	Summaries   []SummarySpec `json:"summaries,omitempty"`
	GroupOffset int           `json:"groupOffset,omitempty"`
	GroupLimit  int           `json:"groupLimit,omitempty"`
}

type GroupRow struct {
	Key       []any `json:"key"`
	Count     int64 `json:"count"`
	Summaries []any `json:"summaries"`
}

type ViewResult struct {
	Page          Page       `json:"page"`
	GroupRows     []GroupRow `json:"groupRows"`
	GroupOffset   int        `json:"groupOffset"`
	GroupLimit    int        `json:"groupLimit"`
	HasMoreGroups bool       `json:"hasMoreGroups"`
}

type ProductError struct {
	Code      string         `json:"code"`
	Path      string         `json:"path"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"-"`
}

func (e *ProductError) Error() string {
	return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Message)
}

func (e ProductError) MarshalJSON() ([]byte, error) {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return json.Marshal(struct {
		ContractVersion string         `json:"contractVersion"`
		Code            string         `json:"code"`
		Path            string         `json:"path"`
		Message         string         `json:"message"`
		Details         map[string]any `json:"details"`
		Retryable       bool           `json:"retryable"`
	}{
		ContractVersion: "1.0",
		Code:            e.Code, Path: e.Path, Message: e.Message,
		Details: details, Retryable: e.Retryable,
	})
}

func (query TableQuery) MarshalCanonical() ([]byte, error) {
	return json.Marshal(query)
}
