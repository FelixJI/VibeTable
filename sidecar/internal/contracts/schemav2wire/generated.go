// Code generated from contracts/schema-v2/schema.schema.json; DO NOT EDIT.
package schemav2wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func StrictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

type LogicalType string

const (
	LogicalTypeText        LogicalType = "text"
	LogicalTypeEditor      LogicalType = "editor"
	LogicalTypeNumber      LogicalType = "number"
	LogicalTypeBool        LogicalType = "bool"
	LogicalTypeDate        LogicalType = "date"
	LogicalTypeDateTime    LogicalType = "dateTime"
	LogicalTypeTime        LogicalType = "time"
	LogicalTypeAutoDate    LogicalType = "autoDate"
	LogicalTypeEmail       LogicalType = "email"
	LogicalTypeUrl         LogicalType = "url"
	LogicalTypeSelect      LogicalType = "select"
	LogicalTypeMultiSelect LogicalType = "multiSelect"
	LogicalTypeRelation    LogicalType = "relation"
	LogicalTypeFile        LogicalType = "file"
	LogicalTypeGeoPoint    LogicalType = "geoPoint"
	LogicalTypeJson        LogicalType = "json"
	LogicalTypeFormula     LogicalType = "formula"
	LogicalTypeLookup      LogicalType = "lookup"
)

type FieldIdentity struct {
	FieldId         string `json:"fieldId"`
	PhysicalName    string `json:"physicalName"`
	ProviderFieldId string `json:"providerFieldId"`
}

type Lifecycle struct {
	State     string  `json:"state"`
	RetiredAt *string `json:"retiredAt"`
}

type DefaultSpec struct {
	Enabled         bool            `json:"enabled"`
	Value           json.RawMessage `json:"value"`
	Source          string          `json:"source"`
	DefaultsVersion int64           `json:"defaultsVersion"`
}

type PresenceSpec struct {
	Mode            string  `json:"mode"`
	ProviderFieldId *string `json:"providerFieldId,omitempty"`
	PhysicalName    *string `json:"physicalName,omitempty"`
}

type ValueSpec struct {
	Required bool         `json:"required"`
	Default  DefaultSpec  `json:"default"`
	Presence PresenceSpec `json:"presence"`
}

type UniqueSpec struct {
	Enabled     bool   `json:"enabled"`
	BlankPolicy string `json:"blankPolicy"`
}

type RangeSpec struct {
	Min *json.RawMessage `json:"min"`
	Max *json.RawMessage `json:"max"`
}

type LengthSpec struct {
	Min *int64 `json:"min"`
	Max *int64 `json:"max"`
}

type PatternSpec struct {
	Enabled bool   `json:"enabled"`
	Value   string `json:"value"`
}

type DomainSpec struct {
	Only   []string `json:"only"`
	Except []string `json:"except"`
}

type SelectionSpec struct {
	Min int64  `json:"min"`
	Max *int64 `json:"max"`
}

type ConstraintSpec struct {
	Unique    UniqueSpec    `json:"unique"`
	Range     RangeSpec     `json:"range"`
	Length    LengthSpec    `json:"length"`
	Pattern   PatternSpec   `json:"pattern"`
	Domains   DomainSpec    `json:"domains"`
	Selection SelectionSpec `json:"selection"`
}

type StorageOptions struct {
	OnlyInt     bool  `json:"onlyInt"`
	MaxSize     int64 `json:"maxSize"`
	ConvertURLs bool  `json:"convertURLs"`
	Presentable bool  `json:"presentable"`
}

type StorageSpec struct {
	Kind    string         `json:"kind"`
	Options StorageOptions `json:"options"`
}

type DisplaySpec struct {
	Kind              string  `json:"kind"`
	Preset            string  `json:"preset"`
	DisplayScale      int64   `json:"displayScale"`
	ScaleMode         string  `json:"scaleMode"`
	TrimTrailingZeros bool    `json:"trimTrailingZeros"`
	UseGrouping       bool    `json:"useGrouping"`
	Currency          string  `json:"currency"`
	PercentStorage    string  `json:"percentStorage"`
	Unit              *string `json:"unit"`
	Precision         string  `json:"precision"`
	Timezone          string  `json:"timezone"`
	Mode              string  `json:"mode"`
	Indent            *int64  `json:"indent,omitempty"`
	TrueLabel         string  `json:"trueLabel"`
	FalseLabel        string  `json:"falseLabel"`
}

type SelectOption struct {
	OptionId string `json:"optionId"`
	Label    string `json:"label"`
	Color    string `json:"color"`
	Order    int64  `json:"order"`
	State    string `json:"state"`
}

type SelectSpec struct {
	Options []SelectOption `json:"options"`
}

type SelectOptionDraft struct {
	OptionId string `json:"optionId"`
	Label    string `json:"label"`
	Color    string `json:"color"`
	Order    int64  `json:"order"`
	State    string `json:"state"`
}

type SelectDraftSpec struct {
	Options []SelectOptionDraft `json:"options"`
}

type RelationSpec struct {
	TargetTableId     string  `json:"targetTableId"`
	Cardinality       string  `json:"cardinality"`
	DeletePolicy      string  `json:"deletePolicy"`
	DisplayFieldId    string  `json:"displayFieldId"`
	PairId            *string `json:"pairId,omitempty"`
	ReciprocalFieldId *string `json:"reciprocalFieldId,omitempty"`
}

type FileSpec struct {
	MaxFiles         int64    `json:"maxFiles"`
	MaxBytesPerFile  int64    `json:"maxBytesPerFile"`
	AllowedMimeTypes []string `json:"allowedMimeTypes"`
	Thumbs           []string `json:"thumbs"`
	Protected        bool     `json:"protected"`
}

type JSONSpec struct {
	RootType string                     `json:"rootType"`
	MaxSize  int64                      `json:"maxSize"`
	Schema   map[string]json.RawMessage `json:"schema"`
}

type AutoDateSpec struct {
	Role string `json:"role"`
}

type FormulaSpec struct {
	Language   string      `json:"language"`
	Source     string      `json:"source"`
	ResultType LogicalType `json:"resultType"`
}

type FormulaDraftSpec struct {
	Language string `json:"language"`
	Source   string `json:"source"`
}

type LookupSpec struct {
	Path          []LookupPathStep `json:"path"`
	TargetFieldId string           `json:"targetFieldId"`
}

type LookupPathStep struct {
	RelationFieldId string `json:"relationFieldId"`
}

type FieldDefinition struct {
	Contract    string         `json:"contract"`
	Identity    FieldIdentity  `json:"identity"`
	DisplayName string         `json:"displayName"`
	Help        string         `json:"help"`
	LogicalType LogicalType    `json:"logicalType"`
	Lifecycle   Lifecycle      `json:"lifecycle"`
	Value       ValueSpec      `json:"value"`
	Constraints ConstraintSpec `json:"constraints"`
	Storage     StorageSpec    `json:"storage"`
	Display     DisplaySpec    `json:"display"`
	Select      *SelectSpec    `json:"select,omitempty"`
	Relation    *RelationSpec  `json:"relation,omitempty"`
	File        *FileSpec      `json:"file,omitempty"`
	Json        *JSONSpec      `json:"json,omitempty"`
	AutoDate    *AutoDateSpec  `json:"autoDate,omitempty"`
	Formula     *FormulaSpec   `json:"formula,omitempty"`
	Lookup      *LookupSpec    `json:"lookup,omitempty"`
}

type FieldDraft struct {
	DisplayName string            `json:"displayName"`
	Help        string            `json:"help"`
	LogicalType LogicalType       `json:"logicalType"`
	Value       ValueSpec         `json:"value"`
	Constraints ConstraintSpec    `json:"constraints"`
	Storage     StorageSpec       `json:"storage"`
	Display     DisplaySpec       `json:"display"`
	Select      *SelectDraftSpec  `json:"select,omitempty"`
	Relation    *RelationSpec     `json:"relation,omitempty"`
	File        *FileSpec         `json:"file,omitempty"`
	Json        *JSONSpec         `json:"json,omitempty"`
	AutoDate    *AutoDateSpec     `json:"autoDate,omitempty"`
	Formula     *FormulaDraftSpec `json:"formula,omitempty"`
	Lookup      *LookupSpec       `json:"lookup,omitempty"`
}

type RecommendedValues struct {
	DefaultsVersion int64          `json:"defaultsVersion"`
	Value           ValueSpec      `json:"value"`
	Constraints     ConstraintSpec `json:"constraints"`
	Storage         StorageSpec    `json:"storage"`
	Display         DisplaySpec    `json:"display"`
	File            *FileSpec      `json:"file,omitempty"`
	Json            *JSONSpec      `json:"json,omitempty"`
}

type Capability struct {
	LogicalType               LogicalType       `json:"logicalType"`
	GeneralSettings           []string          `json:"generalSettings"`
	AdvancedSettings          []string          `json:"advancedSettings"`
	DangerSettings            []string          `json:"dangerSettings"`
	Recommended               RecommendedValues `json:"recommended"`
	SupportsRequired          bool              `json:"supportsRequired"`
	SupportsDefault           bool              `json:"supportsDefault"`
	SupportsUnique            bool              `json:"supportsUnique"`
	NeedsPresence             bool              `json:"needsPresence"`
	DisplayPresets            []string          `json:"displayPresets"`
	ConversionTargets         []LogicalType     `json:"conversionTargets"`
	ConversionRules           []string          `json:"conversionRules"`
	CompileStrategy           string            `json:"compileStrategy"`
	UserCreatable             bool              `json:"userCreatable"`
	FilterOperators           []string          `json:"filterOperators"`
	Groupable                 bool              `json:"groupable"`
	SummaryOperations         []string          `json:"summaryOperations"`
	RelationCardinalities     []string          `json:"relationCardinalities"`
	RelationDeletePolicies    []string          `json:"relationDeletePolicies"`
	LookupMaxDepth            int64             `json:"lookupMaxDepth"`
	FormulaResultTypeInferred bool              `json:"formulaResultTypeInferred"`
	FormulaRelationAggregates []string          `json:"formulaRelationAggregates"`
}

type SchemaSnapshot struct {
	Contract       string            `json:"contract"`
	TableId        string            `json:"tableId"`
	DisplayName    string            `json:"displayName"`
	Kind           string            `json:"kind"`
	SchemaRevision string            `json:"schemaRevision"`
	DataRevision   int64             `json:"dataRevision"`
	ArchivePolicy  ArchivePolicy     `json:"archivePolicy"`
	Fields         []FieldDefinition `json:"fields"`
	Capabilities   []Capability      `json:"capabilities"`
}

type FormulaValidateRequest struct {
	TableId string          `json:"tableId"`
	Field   FieldDefinition `json:"field"`
}

type FormulaPreviewRequest struct {
	TableId         string                     `json:"tableId"`
	Field           FieldDefinition            `json:"field"`
	Row             map[string]json.RawMessage `json:"row"`
	ChangedFieldIds []string                   `json:"changedFieldIds"`
}

type TableCreateIntent struct {
	DisplayName string `json:"displayName"`
	OperationId string `json:"operationId"`
	Actor       Actor  `json:"actor"`
}

type TableCreateReceipt struct {
	Contract       string `json:"contract"`
	OperationId    string `json:"operationId"`
	TableId        string `json:"tableId"`
	DisplayName    string `json:"displayName"`
	SchemaRevision string `json:"schemaRevision"`
}

type ArchivePolicy struct {
	Mode          string          `json:"mode"`
	FieldId       *string         `json:"fieldId"`
	ArchivedValue json.RawMessage `json:"archivedValue"`
}

type TableSettingsIntent struct {
	TableId                string        `json:"tableId"`
	ExpectedSchemaRevision string        `json:"expectedSchemaRevision"`
	ArchivePolicy          ArchivePolicy `json:"archivePolicy"`
	OperationId            string        `json:"operationId"`
	Actor                  Actor         `json:"actor"`
}

type TableSettingsReceipt struct {
	Contract       string        `json:"contract"`
	OperationId    string        `json:"operationId"`
	TableId        string        `json:"tableId"`
	SchemaRevision string        `json:"schemaRevision"`
	ArchivePolicy  ArchivePolicy `json:"archivePolicy"`
}

type Actor struct {
	Id   string `json:"id"`
	Kind string `json:"kind"`
}

type RelationPairDraft struct {
	ReciprocalDisplayName string `json:"reciprocalDisplayName"`
	ReciprocalCardinality string `json:"reciprocalCardinality"`
	SourceDisplayFieldId  string `json:"sourceDisplayFieldId"`
}

type FieldChangeIntent struct {
	Action                 string             `json:"action"`
	TableId                string             `json:"tableId"`
	FieldId                string             `json:"fieldId"`
	ExpectedSchemaRevision string             `json:"expectedSchemaRevision"`
	ExpectedDataRevision   *int64             `json:"expectedDataRevision"`
	Draft                  *FieldDraft        `json:"draft"`
	Actor                  Actor              `json:"actor"`
	ConversionRule         string             `json:"conversionRule"`
	Confirmation           string             `json:"confirmation"`
	BackupReceipt          string             `json:"backupReceipt"`
	RelationPair           *RelationPairDraft `json:"relationPair,omitempty"`
}

type Diagnostic struct {
	Code    string                     `json:"code"`
	Path    string                     `json:"path"`
	Message string                     `json:"message"`
	Details map[string]json.RawMessage `json:"details"`
}

type FailureSample struct {
	RecordId string `json:"recordId"`
	Reason   string `json:"reason"`
}

type DependencyRef struct {
	Kind string `json:"kind"`
	Id   string `json:"id"`
	Name string `json:"name"`
}

type Impact struct {
	Records      int64           `json:"records"`
	Missing      int64           `json:"missing"`
	Ambiguous    int64           `json:"ambiguous"`
	Failures     []FailureSample `json:"failures"`
	Dependencies []DependencyRef `json:"dependencies"`
}

type PlanStep struct {
	Kind    string                     `json:"kind"`
	Details map[string]json.RawMessage `json:"details"`
}

type RelatedFieldChange struct {
	TableId                string           `json:"tableId"`
	FieldId                string           `json:"fieldId"`
	Before                 *FieldDefinition `json:"before"`
	After                  *FieldDefinition `json:"after"`
	ExpectedSchemaRevision string           `json:"expectedSchemaRevision"`
}

type FieldChangePlan struct {
	Contract               string                `json:"contract"`
	PlanId                 string                `json:"planId"`
	PlanHash               string                `json:"planHash"`
	ExpiresAt              string                `json:"expiresAt"`
	Intent                 FieldChangeIntent     `json:"intent"`
	Before                 *FieldDefinition      `json:"before"`
	After                  *FieldDefinition      `json:"after"`
	Classes                []string              `json:"classes"`
	ExpectedSchemaRevision string                `json:"expectedSchemaRevision"`
	ExpectedDataRevision   *int64                `json:"expectedDataRevision"`
	Impact                 Impact                `json:"impact"`
	Steps                  []PlanStep            `json:"steps"`
	Warnings               []Diagnostic          `json:"warnings"`
	Errors                 []Diagnostic          `json:"errors"`
	Confirmations          []string              `json:"confirmations"`
	CreatesMigration       bool                  `json:"createsMigration"`
	CanApply               bool                  `json:"canApply"`
	RelatedChanges         *[]RelatedFieldChange `json:"relatedChanges,omitempty"`
}

type RelatedApplyReceipt struct {
	TableId        string           `json:"tableId"`
	FieldId        string           `json:"fieldId"`
	SchemaRevision string           `json:"schemaRevision"`
	Definition     *FieldDefinition `json:"definition"`
}

type ApplyReceipt struct {
	Contract       string                 `json:"contract"`
	OperationId    string                 `json:"operationId"`
	PlanId         string                 `json:"planId"`
	Action         string                 `json:"action"`
	TableId        string                 `json:"tableId"`
	FieldId        string                 `json:"fieldId"`
	SchemaRevision string                 `json:"schemaRevision"`
	Definition     *FieldDefinition       `json:"definition"`
	MigrationJobId string                 `json:"migrationJobId"`
	Related        *[]RelatedApplyReceipt `json:"related,omitempty"`
}

type ApplyRequest struct {
	PlanId               string   `json:"planId"`
	PlanHash             string   `json:"planHash"`
	OperationId          string   `json:"operationId"`
	Actor                Actor    `json:"actor"`
	Confirmations        []string `json:"confirmations"`
	ProtectionSnapshotId *string  `json:"protectionSnapshotId,omitempty"`
}

type MigrationStatus struct {
	Contract  string      `json:"contract"`
	JobId     string      `json:"jobId"`
	PlanId    string      `json:"planId"`
	Phase     string      `json:"phase"`
	Processed int64       `json:"processed"`
	Total     int64       `json:"total"`
	CanCancel bool        `json:"canCancel"`
	Error     *Diagnostic `json:"error"`
	UpdatedAt string      `json:"updatedAt"`
}

type FieldSettingsDescribeResult struct {
	Contract                   string           `json:"contract"`
	TableId                    string           `json:"tableId"`
	FieldId                    string           `json:"fieldId"`
	SchemaRevision             string           `json:"schemaRevision"`
	DataRevision               int64            `json:"dataRevision"`
	Definition                 *FieldDefinition `json:"definition"`
	Capabilities               []Capability     `json:"capabilities"`
	RecommendedDefaultsVersion int64            `json:"recommendedDefaultsVersion"`
}

type FieldRecycleBinResult struct {
	Contract string            `json:"contract"`
	Fields   []FieldDefinition `json:"fields"`
}

type FieldValueCorpusOption struct {
	OptionId string `json:"optionId"`
	Label    string `json:"label"`
}

type FieldValueCorpusCase struct {
	Id            string                    `json:"id"`
	Field         string                    `json:"field"`
	LogicalType   LogicalType               `json:"logicalType"`
	RawValue      string                    `json:"rawValue"`
	ProductValue  json.RawMessage           `json:"productValue"`
	SelectOptions *[]FieldValueCorpusOption `json:"selectOptions,omitempty"`
}

type FieldValueEntryCorpus struct {
	Schema      string                 `json:"$schema"`
	Description string                 `json:"description"`
	Cases       []FieldValueCorpusCase `json:"cases"`
}
