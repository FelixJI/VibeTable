package v2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

const Contract = "vibetable.schema.v2"

var (
	fieldIDPattern         = regexp.MustCompile(`^fld_[A-Za-z0-9_-]{8,}$`)
	optionIDPattern        = regexp.MustCompile(`^opt_[A-Za-z0-9_-]{8,}$`)
	providerFieldIDPattern = regexp.MustCompile(`^pb_[A-Za-z0-9_-]{8,}$`)
	physicalNamePattern    = regexp.MustCompile(`^f_[a-z0-9_]{8,}$`)
)

type LogicalType string

const (
	LogicalText        LogicalType = "text"
	LogicalEditor      LogicalType = "editor"
	LogicalNumber      LogicalType = "number"
	LogicalBool        LogicalType = "bool"
	LogicalDate        LogicalType = "date"
	LogicalDateTime    LogicalType = "dateTime"
	LogicalTime        LogicalType = "time"
	LogicalAutoDate    LogicalType = "autoDate"
	LogicalEmail       LogicalType = "email"
	LogicalURL         LogicalType = "url"
	LogicalSelect      LogicalType = "select"
	LogicalMultiSelect LogicalType = "multiSelect"
	LogicalRelation    LogicalType = "relation"
	LogicalFile        LogicalType = "file"
	LogicalGeoPoint    LogicalType = "geoPoint"
	LogicalJSON        LogicalType = "json"
	LogicalFormula     LogicalType = "formula"
	LogicalLookup      LogicalType = "lookup"
)

var LogicalTypes = []LogicalType{
	LogicalText, LogicalEditor, LogicalNumber, LogicalBool, LogicalDate,
	LogicalDateTime, LogicalTime, LogicalAutoDate, LogicalEmail, LogicalURL,
	LogicalSelect, LogicalMultiSelect, LogicalRelation, LogicalFile,
	LogicalGeoPoint, LogicalJSON, LogicalFormula, LogicalLookup,
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
	JSON        *JSONSpec      `json:"json,omitempty"`
	AutoDate    *AutoDateSpec  `json:"autoDate,omitempty"`
	Formula     *FormulaSpec   `json:"formula,omitempty"`
	Lookup      *LookupSpec    `json:"lookup,omitempty"`
}

type FieldDraft struct {
	DisplayName string         `json:"displayName"`
	Help        string         `json:"help"`
	LogicalType LogicalType    `json:"logicalType"`
	Value       ValueSpec      `json:"value"`
	Constraints ConstraintSpec `json:"constraints"`
	Storage     StorageSpec    `json:"storage"`
	Display     DisplaySpec    `json:"display"`
	Select      *SelectSpec    `json:"select,omitempty"`
	Relation    *RelationSpec  `json:"relation,omitempty"`
	File        *FileSpec      `json:"file,omitempty"`
	JSON        *JSONSpec      `json:"json,omitempty"`
	AutoDate    *AutoDateSpec  `json:"autoDate,omitempty"`
	Formula     *FormulaSpec   `json:"formula,omitempty"`
	Lookup      *LookupSpec    `json:"lookup,omitempty"`
}

func (value *FieldDraft) UnmarshalJSON(raw []byte) error {
	type wire FieldDraft
	var decoded wire
	if err := StrictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = FieldDraft(decoded)
	return nil
}

func (value *FieldDefinition) UnmarshalJSON(raw []byte) error {
	type wire FieldDefinition
	var decoded wire
	if err := StrictDecode(raw, &decoded); err != nil {
		return err
	}
	*value = FieldDefinition(decoded)
	return nil
}

type FieldIdentity struct {
	FieldID         string `json:"fieldId"`
	PhysicalName    string `json:"physicalName"`
	ProviderFieldID string `json:"providerFieldId"`
}

type LifecycleState string

const (
	LifecycleActive  LifecycleState = "active"
	LifecycleRetired LifecycleState = "retired"
)

type Lifecycle struct {
	State     LifecycleState `json:"state"`
	RetiredAt *string        `json:"retiredAt"`
}

type ValueSpec struct {
	Required bool         `json:"required"`
	Default  DefaultSpec  `json:"default"`
	Presence PresenceSpec `json:"presence"`
}

type DefaultSource string

const (
	DefaultRecommended DefaultSource = "recommended"
	DefaultUser        DefaultSource = "user"
)

type DefaultSpec struct {
	Enabled         bool          `json:"enabled"`
	Value           any           `json:"value"`
	Source          DefaultSource `json:"source"`
	DefaultsVersion int           `json:"defaultsVersion"`
}

type PresenceMode string

const (
	PresenceCompanion PresenceMode = "companion"
	PresenceNative    PresenceMode = "native"
	PresenceComputed  PresenceMode = "computed"
)

type PresenceSpec struct {
	Mode            PresenceMode `json:"mode"`
	ProviderFieldID string       `json:"providerFieldId,omitempty"`
	PhysicalName    string       `json:"physicalName,omitempty"`
}

type ConstraintSpec struct {
	Unique    UniqueSpec    `json:"unique"`
	Range     RangeSpec     `json:"range"`
	Length    LengthSpec    `json:"length"`
	Pattern   PatternSpec   `json:"pattern"`
	Domains   DomainSpec    `json:"domains"`
	Selection SelectionSpec `json:"selection"`
}

type UniqueSpec struct {
	Enabled     bool        `json:"enabled"`
	BlankPolicy BlankPolicy `json:"blankPolicy"`
}

type BlankPolicy string

const BlankIgnoreMissing BlankPolicy = "ignoreMissing"

type RangeSpec struct {
	Min any `json:"min"`
	Max any `json:"max"`
}

type LengthSpec struct {
	Min *int `json:"min"`
	Max *int `json:"max"`
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
	Min int  `json:"min"`
	Max *int `json:"max"`
}

type StorageKind string

const (
	StorageText     StorageKind = "pocketbase-text"
	StorageEditor   StorageKind = "pocketbase-editor"
	StorageNumber   StorageKind = "pocketbase-number"
	StorageBool     StorageKind = "pocketbase-bool"
	StorageDate     StorageKind = "pocketbase-date"
	StorageAutoDate StorageKind = "pocketbase-autodate"
	StorageEmail    StorageKind = "pocketbase-email"
	StorageURL      StorageKind = "pocketbase-url"
	StorageSelect   StorageKind = "pocketbase-select"
	StorageRelation StorageKind = "pocketbase-relation"
	StorageFile     StorageKind = "pocketbase-file"
	StorageGeoPoint StorageKind = "pocketbase-geo-point"
	StorageJSON     StorageKind = "pocketbase-json"
	StorageComputed StorageKind = "computed"
)

type StorageSpec struct {
	Kind    StorageKind    `json:"kind"`
	Options StorageOptions `json:"options"`
}

type StorageOptions struct {
	OnlyInt     bool `json:"onlyInt"`
	MaxSize     int  `json:"maxSize"`
	ConvertURLs bool `json:"convertURLs"`
	Presentable bool `json:"presentable"`
}

type DisplayKind string

const (
	DisplayText     DisplayKind = "text"
	DisplayEditor   DisplayKind = "editor"
	DisplayNumber   DisplayKind = "number"
	DisplayBool     DisplayKind = "bool"
	DisplayDate     DisplayKind = "date"
	DisplayDateTime DisplayKind = "dateTime"
	DisplayTime     DisplayKind = "time"
	DisplayEmail    DisplayKind = "email"
	DisplayURL      DisplayKind = "url"
	DisplaySelect   DisplayKind = "select"
	DisplayRelation DisplayKind = "relation"
	DisplayFile     DisplayKind = "file"
	DisplayGeoPoint DisplayKind = "geoPoint"
	DisplayJSON     DisplayKind = "json"
	DisplayReadonly DisplayKind = "readonly"
)

type DisplaySpec struct {
	Kind              DisplayKind `json:"kind"`
	Preset            string      `json:"preset"`
	DisplayScale      int         `json:"displayScale"`
	ScaleMode         string      `json:"scaleMode"`
	TrimTrailingZeros bool        `json:"trimTrailingZeros"`
	UseGrouping       bool        `json:"useGrouping"`
	Currency          string      `json:"currency"`
	PercentStorage    string      `json:"percentStorage"`
	Unit              *string     `json:"unit"`
	Precision         string      `json:"precision"`
	Timezone          string      `json:"timezone"`
	Mode              string      `json:"mode"`
	Indent            int         `json:"indent"`
	TrueLabel         string      `json:"trueLabel"`
	FalseLabel        string      `json:"falseLabel"`
}

type OptionState string

const (
	OptionActive  OptionState = "active"
	OptionRetired OptionState = "retired"
)

type SelectOption struct {
	OptionID string      `json:"optionId"`
	Label    string      `json:"label"`
	Color    string      `json:"color"`
	Order    int         `json:"order"`
	State    OptionState `json:"state"`
}

type SelectSpec struct {
	Options []SelectOption `json:"options"`
}

type RelationSpec struct {
	TargetTableID     string `json:"targetTableId"`
	Cardinality       string `json:"cardinality"`
	DeletePolicy      string `json:"deletePolicy"`
	DisplayField      string `json:"displayFieldId"`
	PairID            string `json:"pairId,omitempty"`
	ReciprocalFieldID string `json:"reciprocalFieldId,omitempty"`
}

type RelationPairDraft struct {
	ReciprocalDisplayName string `json:"reciprocalDisplayName"`
	ReciprocalCardinality string `json:"reciprocalCardinality"`
	SourceDisplayFieldID  string `json:"sourceDisplayFieldId"`
}

type FileSpec struct {
	MaxFiles         int      `json:"maxFiles"`
	MaxBytesPerFile  int64    `json:"maxBytesPerFile"`
	AllowedMIMETypes []string `json:"allowedMimeTypes"`
	Thumbs           []string `json:"thumbs"`
	Protected        bool     `json:"protected"`
}

type JSONSpec struct {
	RootType string         `json:"rootType"`
	MaxSize  int            `json:"maxSize"`
	Schema   map[string]any `json:"schema"`
}

type AutoDateSpec struct {
	Role string `json:"role"`
}

type FormulaSpec struct {
	Language   string      `json:"language"`
	Source     string      `json:"source"`
	ResultType LogicalType `json:"resultType"`
}

type LookupSpec struct {
	Path          []LookupPathStep `json:"path"`
	TargetFieldID string           `json:"targetFieldId"`
}

type LookupPathStep struct {
	RelationFieldID string `json:"relationFieldId"`
}

type Capability struct {
	LogicalType       LogicalType       `json:"logicalType"`
	GeneralSettings   []string          `json:"generalSettings"`
	AdvancedSettings  []string          `json:"advancedSettings"`
	DangerSettings    []string          `json:"dangerSettings"`
	Recommended       RecommendedValues `json:"recommended"`
	SupportsRequired  bool              `json:"supportsRequired"`
	SupportsDefault   bool              `json:"supportsDefault"`
	SupportsUnique    bool              `json:"supportsUnique"`
	NeedsPresence     bool              `json:"needsPresence"`
	DisplayPresets    []string          `json:"displayPresets"`
	ConversionTargets []LogicalType     `json:"conversionTargets"`
	ConversionRules   []string          `json:"conversionRules"`
	CompileStrategy   string            `json:"compileStrategy"`
	UserCreatable     bool              `json:"userCreatable"`
}

type RecommendedValues struct {
	DefaultsVersion int            `json:"defaultsVersion"`
	Value           ValueSpec      `json:"value"`
	Constraints     ConstraintSpec `json:"constraints"`
	Storage         StorageSpec    `json:"storage"`
	Display         DisplaySpec    `json:"display"`
	File            *FileSpec      `json:"file,omitempty"`
	JSON            *JSONSpec      `json:"json,omitempty"`
}

type ChangeAction string

const (
	ActionCreate   ChangeAction = "create"
	ActionUpdate   ChangeAction = "update"
	ActionRetire   ChangeAction = "retire"
	ActionRestore  ChangeAction = "restore"
	ActionPurge    ChangeAction = "purge"
	ActionConvert  ChangeAction = "convert"
	ActionBackfill ChangeAction = "backfill"
)

type Actor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type FieldChangeIntent struct {
	Action               ChangeAction       `json:"action"`
	TableID              string             `json:"tableId"`
	FieldID              string             `json:"fieldId"`
	ExpectedSchemaRev    string             `json:"expectedSchemaRevision"`
	ExpectedDataRevision *int64             `json:"expectedDataRevision"`
	Draft                *FieldDraft        `json:"draft"`
	Actor                Actor              `json:"actor"`
	ConversionRule       string             `json:"conversionRule"`
	Confirmation         string             `json:"confirmation"`
	BackupReceipt        string             `json:"backupReceipt"`
	RelationPair         *RelationPairDraft `json:"relationPair,omitempty"`
}

type RelatedFieldChange struct {
	TableID                string           `json:"tableId"`
	FieldID                string           `json:"fieldId"`
	Before                 *FieldDefinition `json:"before"`
	After                  *FieldDefinition `json:"after"`
	ExpectedSchemaRevision string           `json:"expectedSchemaRevision"`
}

type ChangeClass string

const (
	ClassDisplay    ChangeClass = "display"
	ClassMetadata   ChangeClass = "metadata"
	ClassConstraint ChangeClass = "constraint"
	ClassSchema     ChangeClass = "schema"
	ClassMigration  ChangeClass = "migration"
	ClassDanger     ChangeClass = "danger"
)

type Diagnostic struct {
	Code    string         `json:"code"`
	Path    string         `json:"path"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type Impact struct {
	Records      int64           `json:"records"`
	Missing      int64           `json:"missing"`
	Ambiguous    int64           `json:"ambiguous"`
	Failures     []FailureSample `json:"failures"`
	Dependencies []DependencyRef `json:"dependencies"`
}

type FailureSample struct {
	RecordID string `json:"recordId"`
	Reason   string `json:"reason"`
}

type DependencyRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PlanStep struct {
	Kind    string         `json:"kind"`
	Details map[string]any `json:"details"`
}

type FieldChangePlan struct {
	Contract             string               `json:"contract"`
	PlanID               string               `json:"planId"`
	PlanHash             string               `json:"planHash"`
	ExpiresAt            string               `json:"expiresAt"`
	Intent               FieldChangeIntent    `json:"intent"`
	Before               *FieldDefinition     `json:"before"`
	After                *FieldDefinition     `json:"after"`
	Classes              []ChangeClass        `json:"classes"`
	ExpectedSchemaRev    string               `json:"expectedSchemaRevision"`
	ExpectedDataRevision *int64               `json:"expectedDataRevision"`
	Impact               Impact               `json:"impact"`
	Steps                []PlanStep           `json:"steps"`
	Warnings             []Diagnostic         `json:"warnings"`
	Errors               []Diagnostic         `json:"errors"`
	Confirmations        []string             `json:"confirmations"`
	CreatesMigration     bool                 `json:"createsMigration"`
	CanApply             bool                 `json:"canApply"`
	RelatedChanges       []RelatedFieldChange `json:"relatedChanges,omitempty"`
}

type ApplyRequest struct {
	PlanID               string   `json:"planId"`
	PlanHash             string   `json:"planHash"`
	OperationID          string   `json:"operationId"`
	Actor                Actor    `json:"actor"`
	Confirmations        []string `json:"confirmations"`
	ProtectionSnapshotID string   `json:"protectionSnapshotId"`
}

type ApplyReceipt struct {
	Contract       string                `json:"contract"`
	OperationID    string                `json:"operationId"`
	PlanID         string                `json:"planId"`
	Action         ChangeAction          `json:"action"`
	TableID        string                `json:"tableId"`
	FieldID        string                `json:"fieldId"`
	SchemaRevision string                `json:"schemaRevision"`
	Definition     *FieldDefinition      `json:"definition"`
	MigrationJobID string                `json:"migrationJobId"`
	Related        []RelatedApplyReceipt `json:"related,omitempty"`
}

type RelatedApplyReceipt struct {
	TableID        string           `json:"tableId"`
	FieldID        string           `json:"fieldId"`
	SchemaRevision string           `json:"schemaRevision"`
	Definition     *FieldDefinition `json:"definition"`
}

type MigrationPhase string

const (
	MigrationPlanned    MigrationPhase = "planned"
	MigrationValidating MigrationPhase = "validating"
	MigrationReady      MigrationPhase = "ready"
	MigrationCopying    MigrationPhase = "copying"
	MigrationVerifying  MigrationPhase = "verifying"
	MigrationSwitching  MigrationPhase = "switching"
	MigrationCompleted  MigrationPhase = "completed"
	MigrationCancelled  MigrationPhase = "cancelled"
	MigrationFailed     MigrationPhase = "failed"
	MigrationCleaning   MigrationPhase = "cleaning"
	MigrationRolledBack MigrationPhase = "rolled_back"
)

type MigrationStatus struct {
	Contract  string         `json:"contract"`
	JobID     string         `json:"jobId"`
	PlanID    string         `json:"planId"`
	Phase     MigrationPhase `json:"phase"`
	Processed int64          `json:"processed"`
	Total     int64          `json:"total"`
	CanCancel bool           `json:"canCancel"`
	Error     *Diagnostic    `json:"error"`
	UpdatedAt string         `json:"updatedAt"`
}

func StrictDecode(raw []byte, target any) error {
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

func validRFC3339(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
