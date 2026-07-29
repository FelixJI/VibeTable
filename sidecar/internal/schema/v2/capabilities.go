package v2

import "fmt"

const DefaultsVersion = 1

var presenceTypes = map[LogicalType]bool{
	LogicalText: true, LogicalEditor: true, LogicalNumber: true, LogicalBool: true,
	LogicalDate: true, LogicalDateTime: true, LogicalTime: true, LogicalEmail: true,
	LogicalURL: true, LogicalSelect: true, LogicalMultiSelect: true,
	LogicalRelation: true, LogicalFile: true, LogicalGeoPoint: true,
}

func CapabilityFor(logicalType LogicalType) (Capability, error) {
	recommended, err := RecommendedDefaults(logicalType)
	if err != nil {
		return Capability{}, err
	}
	capability := Capability{
		LogicalType:       logicalType,
		GeneralSettings:   []string{"displayName", "help"},
		AdvancedSettings:  []string{},
		DangerSettings:    []string{"retire", "purge"},
		Recommended:       recommended,
		SupportsRequired:  true,
		SupportsDefault:   true,
		SupportsUnique:    supportsUnique(logicalType),
		NeedsPresence:     presenceTypes[logicalType],
		DisplayPresets:    []string{},
		ConversionTargets: conversionTargets(logicalType),
		ConversionRules:   []string{},
		CompileStrategy:   string(recommended.Storage.Kind),
		UserCreatable:     true,
	}
	if logicalType == LogicalAutoDate || logicalType == LogicalFormula || logicalType == LogicalLookup {
		capability.SupportsRequired = false
		capability.SupportsDefault = false
		capability.SupportsUnique = false
		capability.DangerSettings = []string{"retire"}
		// autoDate is system-owned. Formula and Lookup use typed editors in the
		// unified drawer and still cross the same FieldChangePlanner boundary.
		capability.UserCreatable = logicalType != LogicalAutoDate
	}
	if logicalType == LogicalRelation || logicalType == LogicalFile {
		capability.SupportsDefault = false
	}
	if capability.SupportsRequired {
		capability.GeneralSettings = append(capability.GeneralSettings, "required")
	}
	if capability.SupportsDefault {
		capability.GeneralSettings = append(capability.GeneralSettings, "default")
	}
	if capability.SupportsUnique {
		capability.GeneralSettings = append(capability.GeneralSettings, "unique")
	}
	switch logicalType {
	case LogicalText:
		capability.AdvancedSettings = []string{"length", "pattern"}
		capability.ConversionRules = []string{
			"strict", "round", "floor", "ceil", "truncate", "clear", "block",
		}
	case LogicalEditor:
		capability.AdvancedSettings = []string{"maxSize", "convertURLs"}
	case LogicalNumber:
		capability.AdvancedSettings = []string{
			"range", "onlyInt", "displayScale", "scaleMode", "trimTrailingZeros",
			"useGrouping", "currency", "percentStorage", "unit",
		}
		capability.DisplayPresets = []string{"number", "integer", "currency", "percent", "unit"}
		capability.ConversionRules = []string{"round", "floor", "ceil", "truncate", "block"}
	case LogicalBool:
		capability.AdvancedSettings = []string{"displayMode", "trueLabel", "falseLabel"}
	case LogicalDate, LogicalDateTime, LogicalTime:
		capability.AdvancedSettings = []string{"range", "precision", "timezone"}
		capability.ConversionRules = []string{"timezone", "dateFill", "truncate", "block"}
	case LogicalEmail, LogicalURL:
		capability.AdvancedSettings = []string{"onlyDomains", "exceptDomains"}
	case LogicalSelect, LogicalMultiSelect:
		capability.AdvancedSettings = []string{"options", "selectionBounds", "retiredOptions"}
		capability.ConversionRules = []string{"first", "last", "clear", "block"}
	case LogicalRelation:
		capability.AdvancedSettings = []string{
			"targetTable", "cardinality", "displayField", "setNull", "restrict",
		}
		capability.DangerSettings = []string{"cascade", "retire", "purge"}
		capability.ConversionRules = []string{"first", "last", "clear", "block"}
	case LogicalFile:
		capability.AdvancedSettings = []string{"maxFiles", "maxBytes", "mime", "thumbs", "protected"}
	case LogicalGeoPoint:
		capability.AdvancedSettings = []string{"displayScale"}
	case LogicalJSON:
		capability.AdvancedSettings = []string{"rootType", "maxSize", "jsonSchema", "editorMode", "indent"}
	case LogicalAutoDate:
		capability.AdvancedSettings = []string{"role"}
	case LogicalFormula, LogicalLookup:
		capability.AdvancedSettings = []string{"resultType", "source"}
	}
	return capability, nil
}

func RecommendedDefaults(logicalType LogicalType) (RecommendedValues, error) {
	storage, display, err := recommendedStorageDisplay(logicalType)
	if err != nil {
		return RecommendedValues{}, err
	}
	required := false
	defaultSpec := DefaultSpec{
		Enabled: false, Value: nil, Source: DefaultRecommended, DefaultsVersion: DefaultsVersion,
	}
	presence := PresenceSpec{Mode: PresenceNative}
	if presenceTypes[logicalType] {
		presence.Mode = PresenceCompanion
	}
	if logicalType == LogicalAutoDate || logicalType == LogicalFormula || logicalType == LogicalLookup {
		presence.Mode = PresenceComputed
	}
	recommended := RecommendedValues{
		DefaultsVersion: DefaultsVersion,
		Value:           ValueSpec{Required: required, Default: defaultSpec, Presence: presence},
		Constraints: ConstraintSpec{
			Unique: UniqueSpec{Enabled: false, BlankPolicy: BlankIgnoreMissing},
			Range:  RangeSpec{}, Length: LengthSpec{},
			Pattern:   PatternSpec{Enabled: false},
			Domains:   DomainSpec{Only: []string{}, Except: []string{}},
			Selection: SelectionSpec{},
		},
		Storage: storage,
		Display: display,
	}
	if logicalType == LogicalFile {
		recommended.File = &FileSpec{
			MaxFiles: 1, MaxBytesPerFile: 5 * 1024 * 1024,
			AllowedMIMETypes: []string{}, Thumbs: []string{}, Protected: false,
		}
	}
	if logicalType == LogicalJSON {
		recommended.JSON = &JSONSpec{
			RootType: "any", MaxSize: 1024 * 1024, Schema: map[string]any{},
		}
	}
	if logicalType == LogicalSelect {
		maximum := 1
		recommended.Constraints.Selection.Max = &maximum
	}
	return recommended, nil
}

func recommendedStorageDisplay(logicalType LogicalType) (StorageSpec, DisplaySpec, error) {
	storage := StorageSpec{Options: StorageOptions{}}
	display := DisplaySpec{
		Preset: "", DisplayScale: 2, ScaleMode: "max", TrimTrailingZeros: true,
		UseGrouping: true, Currency: "CNY", PercentStorage: "ratio",
		Precision: "minute", Timezone: "system", Mode: "default",
		TrueLabel: "是", FalseLabel: "否",
	}
	switch logicalType {
	case LogicalText:
		storage.Kind, storage.Options.MaxSize, display.Kind = StorageText, 5000, DisplayText
	case LogicalEditor:
		storage.Kind, storage.Options.MaxSize, display.Kind = StorageEditor, 1024*1024, DisplayEditor
	case LogicalNumber:
		storage.Kind, display.Kind, display.Preset = StorageNumber, DisplayNumber, "number"
	case LogicalBool:
		storage.Kind, display.Kind = StorageBool, DisplayBool
	case LogicalDate:
		storage.Kind, display.Kind, display.Precision = StorageDate, DisplayDate, "day"
	case LogicalDateTime:
		storage.Kind, display.Kind = StorageDate, DisplayDateTime
	case LogicalTime:
		storage.Kind, display.Kind = StorageText, DisplayTime
	case LogicalAutoDate:
		storage.Kind, display.Kind = StorageAutoDate, DisplayReadonly
	case LogicalEmail:
		storage.Kind, display.Kind = StorageEmail, DisplayEmail
	case LogicalURL:
		storage.Kind, display.Kind = StorageURL, DisplayURL
	case LogicalSelect, LogicalMultiSelect:
		storage.Kind, display.Kind = StorageSelect, DisplaySelect
	case LogicalRelation:
		storage.Kind, display.Kind = StorageRelation, DisplayRelation
	case LogicalFile:
		storage.Kind, display.Kind = StorageFile, DisplayFile
	case LogicalGeoPoint:
		storage.Kind, display.Kind, display.DisplayScale = StorageGeoPoint, DisplayGeoPoint, 6
	case LogicalJSON:
		storage.Kind, storage.Options.MaxSize, display.Kind, display.Mode =
			StorageJSON, 1024*1024, DisplayJSON, "code"
		display.Indent = 2
	case LogicalFormula, LogicalLookup:
		storage.Kind, display.Kind = StorageComputed, DisplayReadonly
	default:
		return StorageSpec{}, DisplaySpec{}, fmt.Errorf("unsupported logical type %q", logicalType)
	}
	return storage, display, nil
}

func supportsUnique(logicalType LogicalType) bool {
	switch logicalType {
	case LogicalText, LogicalNumber, LogicalBool, LogicalDate, LogicalDateTime,
		LogicalTime, LogicalEmail, LogicalURL, LogicalSelect:
		return true
	default:
		return false
	}
}

func conversionTargets(logicalType LogicalType) []LogicalType {
	switch logicalType {
	case LogicalText:
		return []LogicalType{LogicalEmail, LogicalURL, LogicalNumber}
	case LogicalEmail, LogicalURL:
		return []LogicalType{LogicalText}
	case LogicalNumber:
		return []LogicalType{LogicalText}
	case LogicalSelect:
		return []LogicalType{LogicalMultiSelect}
	case LogicalMultiSelect:
		return []LogicalType{LogicalSelect}
	case LogicalDate:
		return []LogicalType{LogicalDateTime, LogicalTime}
	case LogicalDateTime:
		return []LogicalType{LogicalDate, LogicalTime}
	case LogicalTime:
		return []LogicalType{LogicalDate, LogicalDateTime}
	default:
		return []LogicalType{}
	}
}
