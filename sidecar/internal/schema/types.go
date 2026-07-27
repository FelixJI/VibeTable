package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

const ContractVersion = "1.0"

var schemaRevisionPattern = regexp.MustCompile(`^schema_([0-9]+)$`)

type TableKind string

const (
	TableKindBase TableKind = "base"
	TableKindView TableKind = "view"
)

type FieldKind string

const (
	FieldKindScalar     FieldKind = "scalar"
	FieldKindRelation   FieldKind = "relation"
	FieldKindLookup     FieldKind = "lookup"
	FieldKindFormula    FieldKind = "formula"
	FieldKindAttachment FieldKind = "attachment"
	FieldKindSystem     FieldKind = "system"
)

type DataType string

const (
	DataTypeShortText   DataType = "shortText"
	DataTypeLongText    DataType = "longText"
	DataTypeRichText    DataType = "richText"
	DataTypeBoolean     DataType = "boolean"
	DataTypeInteger     DataType = "integer"
	DataTypeFloat       DataType = "float"
	DataTypeDecimal     DataType = "decimal"
	DataTypeDate        DataType = "date"
	DataTypeDateTime    DataType = "dateTime"
	DataTypeAutoDate    DataType = "autoDate"
	DataTypeTime        DataType = "time"
	DataTypeEmail       DataType = "email"
	DataTypeURL         DataType = "url"
	DataTypeUUID        DataType = "uuid"
	DataTypeSelect      DataType = "select"
	DataTypeMultiSelect DataType = "multiSelect"
	DataTypeJSON        DataType = "json"
	DataTypeGeoPoint    DataType = "geoPoint"
	DataTypeGeoJSON     DataType = "geoJson"
	DataTypeFile        DataType = "file"
	DataTypeRelation    DataType = "relation"
	DataTypeLookup      DataType = "lookup"
	DataTypeFormula     DataType = "formula"
	DataTypeList        DataType = "list"
	DataTypeHash        DataType = "hash"
	DataTypeSecret      DataType = "secret"
)

type StorageType string

const (
	StorageText     StorageType = "text"
	StorageEditor   StorageType = "editor"
	StorageBool     StorageType = "bool"
	StorageNumber   StorageType = "number"
	StorageDate     StorageType = "date"
	StorageAutodate StorageType = "autodate"
	StorageEmail    StorageType = "email"
	StorageURL      StorageType = "url"
	StorageSelect   StorageType = "select"
	StorageJSON     StorageType = "json"
	StorageGeoPoint StorageType = "geoPoint"
	StorageFile     StorageType = "file"
	StorageRelation StorageType = "relation"
)

type ArchiveMode string

const (
	ArchiveModeNone      ArchiveMode = "none"
	ArchiveModeStatus    ArchiveMode = "status"
	ArchiveModeDeletedAt ArchiveMode = "deletedAt"
)

type Capability struct {
	Storage StorageType `json:"storage"`
	Exact   bool        `json:"exact"`
	Note    string      `json:"note,omitempty"`
}

type TableDefinition struct {
	ContractVersion string            `json:"contractVersion"`
	TableID         string            `json:"tableId"`
	PhysicalName    string            `json:"physicalName"`
	DisplayName     string            `json:"displayName"`
	Kind            TableKind         `json:"kind"`
	SchemaRevision  string            `json:"schemaRevision"`
	ArchivePolicy   ArchivePolicy     `json:"archivePolicy"`
	View            *ViewSpec         `json:"view,omitempty"`
	Fields          []FieldDefinition `json:"fields"`
	Indexes         []IndexDefinition `json:"indexes"`
}

// ViewSpec intentionally exposes no SQL. Contract v1 views are safe,
// read-only projections over explicitly declared normalized source fields.
// More query operators can be added as typed product AST nodes later without
// accepting arbitrary SQL/PocketBase filters.
type ViewSpec struct {
	SourceTableID string `json:"sourceTableId"`
}

func (value *ViewSpec) UnmarshalJSON(raw []byte) error {
	type wire ViewSpec
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = ViewSpec(decoded)
	return nil
}

type ArchivePolicy struct {
	Mode          ArchiveMode `json:"mode"`
	FieldID       *string     `json:"fieldId"`
	ArchivedValue any         `json:"archivedValue"`
}

func (value *TableDefinition) UnmarshalJSON(raw []byte) error {
	type wire TableDefinition
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = TableDefinition(decoded)
	return nil
}

func (value *ArchivePolicy) UnmarshalJSON(raw []byte) error {
	type wire ArchivePolicy
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = ArchivePolicy(decoded)
	return nil
}

type FieldDefinition struct {
	FieldID          string            `json:"fieldId"`
	PhysicalName     string            `json:"physicalName"`
	DisplayName      string            `json:"displayName"`
	Kind             FieldKind         `json:"kind"`
	DataType         DataType          `json:"dataType"`
	StorageType      StorageType       `json:"storageType"`
	Nullable         bool              `json:"nullable"`
	DefaultValue     any               `json:"defaultValue"`
	Constraints      []FieldConstraint `json:"constraints"`
	Editor           EditorDefinition  `json:"editor"`
	ReadOnly         bool              `json:"readOnly"`
	AutoDate         *AutoDateSpec     `json:"autoDate,omitempty"`
	Formula          *FormulaSpec      `json:"formula"`
	Relation         *RelationSpec     `json:"relation"`
	Lookup           *LookupSpec       `json:"lookup"`
	AttachmentPolicy *AttachmentPolicy `json:"attachmentPolicy"`
}

type AutoDateRole string

const (
	AutoDateRoleCreatedAt AutoDateRole = "createdAt"
	AutoDateRoleUpdatedAt AutoDateRole = "updatedAt"
)

type AutoDateSpec struct {
	Role AutoDateRole `json:"role"`
}

func (value *AutoDateSpec) UnmarshalJSON(raw []byte) error {
	type wire AutoDateSpec
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = AutoDateSpec(decoded)
	return nil
}

type EditorDefinition struct {
	Kind   string         `json:"kind"`
	Config map[string]any `json:"config"`
}

func (value *FieldDefinition) UnmarshalJSON(raw []byte) error {
	type wire FieldDefinition
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = FieldDefinition(decoded)
	return nil
}

func (value *EditorDefinition) UnmarshalJSON(raw []byte) error {
	type wire EditorDefinition
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = EditorDefinition(decoded)
	return nil
}

type ConstraintKind string

const (
	ConstraintRequired       ConstraintKind = "required"
	ConstraintDefault        ConstraintKind = "default"
	ConstraintUnique         ConstraintKind = "unique"
	ConstraintIndex          ConstraintKind = "index"
	ConstraintRange          ConstraintKind = "range"
	ConstraintLength         ConstraintKind = "length"
	ConstraintPattern        ConstraintKind = "pattern"
	ConstraintPrecisionScale ConstraintKind = "precisionScale"
	ConstraintEnum           ConstraintKind = "enum"
	ConstraintJSONSchema     ConstraintKind = "jsonSchema"
	ConstraintRelation       ConstraintKind = "relation"
	ConstraintAttachment     ConstraintKind = "attachment"
)

// FieldConstraint is the typed in-memory representation of the frozen v1
// discriminated union. MarshalJSON emits only the properties allowed by the
// selected kind, including required false, zero, and null values.
type FieldConstraint struct {
	Kind ConstraintKind

	Value any

	FieldIDs []string
	Unique   bool

	Min          *float64
	Max          *float64
	ExclusiveMin bool
	ExclusiveMax bool

	MinLength *int
	MaxLength *int

	Pattern string
	Flags   []string

	Precision int
	Scale     int

	Multiple    bool
	MinSelected int
	MaxSelected *int
	Options     []SelectOption

	Schema map[string]any

	TargetTableID string
	Cardinality   string
	DeletePolicy  string

	Policy *AttachmentPolicy
}

func (constraint FieldConstraint) MarshalJSON() ([]byte, error) {
	var value any
	switch constraint.Kind {
	case ConstraintRequired, ConstraintDefault, ConstraintUnique:
		value = struct {
			Kind  ConstraintKind `json:"kind"`
			Value any            `json:"value"`
		}{constraint.Kind, constraint.Value}
	case ConstraintIndex:
		value = struct {
			Kind     ConstraintKind `json:"kind"`
			FieldIDs []string       `json:"fieldIds"`
			Unique   bool           `json:"unique"`
		}{constraint.Kind, constraint.FieldIDs, constraint.Unique}
	case ConstraintRange:
		value = struct {
			Kind         ConstraintKind `json:"kind"`
			Min          *float64       `json:"min"`
			Max          *float64       `json:"max"`
			ExclusiveMin bool           `json:"exclusiveMin"`
			ExclusiveMax bool           `json:"exclusiveMax"`
		}{constraint.Kind, constraint.Min, constraint.Max, constraint.ExclusiveMin, constraint.ExclusiveMax}
	case ConstraintLength:
		value = struct {
			Kind      ConstraintKind `json:"kind"`
			MinLength *int           `json:"minLength"`
			MaxLength *int           `json:"maxLength"`
		}{constraint.Kind, constraint.MinLength, constraint.MaxLength}
	case ConstraintPattern:
		value = struct {
			Kind    ConstraintKind `json:"kind"`
			Pattern string         `json:"pattern"`
			Flags   []string       `json:"flags"`
		}{constraint.Kind, constraint.Pattern, nonNilStrings(constraint.Flags)}
	case ConstraintPrecisionScale:
		value = struct {
			Kind      ConstraintKind `json:"kind"`
			Precision int            `json:"precision"`
			Scale     int            `json:"scale"`
		}{constraint.Kind, constraint.Precision, constraint.Scale}
	case ConstraintEnum:
		value = struct {
			Kind        ConstraintKind `json:"kind"`
			Multiple    bool           `json:"multiple"`
			MinSelected int            `json:"minSelected"`
			MaxSelected *int           `json:"maxSelected"`
			Options     []SelectOption `json:"options"`
		}{constraint.Kind, constraint.Multiple, constraint.MinSelected, constraint.MaxSelected, constraint.Options}
	case ConstraintJSONSchema:
		value = struct {
			Kind   ConstraintKind `json:"kind"`
			Schema map[string]any `json:"schema"`
		}{constraint.Kind, constraint.Schema}
	case ConstraintRelation:
		value = struct {
			Kind          ConstraintKind `json:"kind"`
			TargetTableID string         `json:"targetTableId"`
			Cardinality   string         `json:"cardinality"`
			DeletePolicy  string         `json:"deletePolicy"`
		}{constraint.Kind, constraint.TargetTableID, constraint.Cardinality, constraint.DeletePolicy}
	case ConstraintAttachment:
		value = struct {
			Kind   ConstraintKind    `json:"kind"`
			Policy *AttachmentPolicy `json:"policy"`
		}{constraint.Kind, constraint.Policy}
	default:
		return nil, fmt.Errorf("unknown constraint kind %q", constraint.Kind)
	}
	return json.Marshal(value)
}

func (constraint *FieldConstraint) UnmarshalJSON(raw []byte) error {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	if err := json.Unmarshal(header["kind"], &constraint.Kind); err != nil {
		return fmt.Errorf("constraint kind: %w", err)
	}
	switch constraint.Kind {
	case ConstraintRequired, ConstraintDefault, ConstraintUnique:
		var decoded struct {
			Kind  ConstraintKind `json:"kind"`
			Value any            `json:"value"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Value = decoded.Value
	case ConstraintIndex:
		var decoded struct {
			Kind     ConstraintKind `json:"kind"`
			FieldIDs []string       `json:"fieldIds"`
			Unique   bool           `json:"unique"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.FieldIDs, constraint.Unique = decoded.FieldIDs, decoded.Unique
	case ConstraintRange:
		var decoded struct {
			Kind         ConstraintKind `json:"kind"`
			Min          *float64       `json:"min"`
			Max          *float64       `json:"max"`
			ExclusiveMin bool           `json:"exclusiveMin"`
			ExclusiveMax bool           `json:"exclusiveMax"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Min, constraint.Max = decoded.Min, decoded.Max
		constraint.ExclusiveMin, constraint.ExclusiveMax = decoded.ExclusiveMin, decoded.ExclusiveMax
	case ConstraintLength:
		var decoded struct {
			Kind      ConstraintKind `json:"kind"`
			MinLength *int           `json:"minLength"`
			MaxLength *int           `json:"maxLength"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.MinLength, constraint.MaxLength = decoded.MinLength, decoded.MaxLength
	case ConstraintPattern:
		var decoded struct {
			Kind    ConstraintKind `json:"kind"`
			Pattern string         `json:"pattern"`
			Flags   []string       `json:"flags"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Pattern, constraint.Flags = decoded.Pattern, decoded.Flags
	case ConstraintPrecisionScale:
		var decoded struct {
			Kind      ConstraintKind `json:"kind"`
			Precision int            `json:"precision"`
			Scale     int            `json:"scale"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Precision, constraint.Scale = decoded.Precision, decoded.Scale
	case ConstraintEnum:
		var decoded struct {
			Kind        ConstraintKind `json:"kind"`
			Multiple    bool           `json:"multiple"`
			MinSelected int            `json:"minSelected"`
			MaxSelected *int           `json:"maxSelected"`
			Options     []SelectOption `json:"options"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Multiple, constraint.MinSelected = decoded.Multiple, decoded.MinSelected
		constraint.MaxSelected, constraint.Options = decoded.MaxSelected, decoded.Options
	case ConstraintJSONSchema:
		var decoded struct {
			Kind   ConstraintKind `json:"kind"`
			Schema map[string]any `json:"schema"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Schema = decoded.Schema
	case ConstraintRelation:
		var decoded struct {
			Kind          ConstraintKind `json:"kind"`
			TargetTableID string         `json:"targetTableId"`
			Cardinality   string         `json:"cardinality"`
			DeletePolicy  string         `json:"deletePolicy"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.TargetTableID, constraint.Cardinality = decoded.TargetTableID, decoded.Cardinality
		constraint.DeletePolicy = decoded.DeletePolicy
	case ConstraintAttachment:
		var decoded struct {
			Kind   ConstraintKind    `json:"kind"`
			Policy *AttachmentPolicy `json:"policy"`
		}
		if err := strictDecode(raw, &decoded); err != nil {
			return err
		}
		constraint.Policy = decoded.Policy
	default:
		return fmt.Errorf("unknown constraint kind %q", constraint.Kind)
	}
	return nil
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

type SelectOption struct {
	Value       any    `json:"value"`
	DisplayName string `json:"displayName"`
}

func (value *SelectOption) UnmarshalJSON(raw []byte) error {
	type wire SelectOption
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = SelectOption(decoded)
	return nil
}

type FormulaSpec struct {
	Language   string   `json:"language"`
	Source     string   `json:"source"`
	ResultType DataType `json:"resultType"`
	Version    int      `json:"version"`
	Status     string   `json:"status"`
}

func (value *FormulaSpec) UnmarshalJSON(raw []byte) error {
	type wire FormulaSpec
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = FormulaSpec(decoded)
	return nil
}

type RelationSpec struct {
	// Mode is optional for v1 compatibility. Empty means "direct" unless a
	// junctionTableId is present, in which case it means "junction".
	Mode                         string   `json:"mode,omitempty"`
	TargetTableID                string   `json:"targetTableId"`
	Cardinality                  string   `json:"cardinality"`
	DeletePolicy                 string   `json:"deletePolicy"`
	JunctionTableID              *string  `json:"junctionTableId"`
	JunctionSourceFieldID        string   `json:"junctionSourceFieldId,omitempty"`
	JunctionTargetFieldID        string   `json:"junctionTargetFieldId,omitempty"`
	JunctionDiscriminatorFieldID string   `json:"junctionDiscriminatorFieldId,omitempty"`
	AllowedTargetTableIDs        []string `json:"allowedTargetTableIds,omitempty"`
}

func (value RelationSpec) EffectiveMode() string {
	if value.Mode != "" {
		return value.Mode
	}
	if value.JunctionTableID != nil {
		return "junction"
	}
	return "direct"
}

func (value *RelationSpec) UnmarshalJSON(raw []byte) error {
	type wire RelationSpec
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = RelationSpec(decoded)
	return nil
}

type LookupSpec struct {
	RelationFieldID string            `json:"relationFieldId"`
	Path            []LookupPathStep  `json:"path,omitempty"`
	TargetFieldID   string            `json:"targetFieldId"`
	JunctionFieldID string            `json:"junctionFieldId,omitempty"`
	TargetFieldIDs  map[string]string `json:"targetFieldIds,omitempty"`
	Aggregate       string            `json:"aggregate"`
}

type LookupPathStep struct {
	RelationFieldID string `json:"relationFieldId"`
	M2ACollection   string `json:"m2aCollection,omitempty"`
}

func (value LookupSpec) EffectivePath() []LookupPathStep {
	if len(value.Path) != 0 {
		return value.Path
	}
	if value.RelationFieldID == "" {
		return nil
	}
	return []LookupPathStep{{RelationFieldID: value.RelationFieldID}}
}

func (value *LookupSpec) UnmarshalJSON(raw []byte) error {
	type wire LookupSpec
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	if len(decoded.Path) != 0 {
		if decoded.RelationFieldID == "" {
			decoded.RelationFieldID = decoded.Path[0].RelationFieldID
		} else if decoded.RelationFieldID != decoded.Path[0].RelationFieldID {
			return fmt.Errorf("lookup relationFieldId must match path[0].relationFieldId")
		}
	}
	*value = LookupSpec(decoded)
	return nil
}

type AttachmentPolicy struct {
	MaxFiles          int      `json:"maxFiles"`
	MaxBytesPerFile   int64    `json:"maxBytesPerFile"`
	AllowedMIMETypes  []string `json:"allowedMimeTypes"`
	ThumbnailVariants []string `json:"thumbnailVariants"`
	Protected         bool     `json:"protected"`
}

func (value *AttachmentPolicy) UnmarshalJSON(raw []byte) error {
	type wire AttachmentPolicy
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = AttachmentPolicy(decoded)
	return nil
}

type IndexDefinition struct {
	Name     string   `json:"name"`
	FieldIDs []string `json:"fieldIds"`
	Unique   bool     `json:"unique"`
}

func (value *IndexDefinition) UnmarshalJSON(raw []byte) error {
	type wire IndexDefinition
	var decoded wire
	if err := strictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = IndexDefinition(decoded)
	return nil
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
		ContractVersion: ContractVersion,
		Code:            e.Code, Path: e.Path, Message: e.Message,
		Details: details, Retryable: e.Retryable,
	})
}

func FormatSchemaRevision(revision int64) string {
	return fmt.Sprintf("schema_%04d", revision)
}

func ParseSchemaRevision(value string) (int64, error) {
	matches := schemaRevisionPattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, fmt.Errorf("schema revision must use schema_<number> format")
	}
	revision, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("invalid schema revision")
	}
	return revision, nil
}
