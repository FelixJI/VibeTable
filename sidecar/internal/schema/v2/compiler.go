package v2

import (
	"fmt"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

type RelationCollectionResolver func(tableID string) (string, error)

type CompiledField struct {
	Value    core.Field
	Presence core.Field
}

func CompileField(
	definition FieldDefinition,
	resolveRelation RelationCollectionResolver,
) (CompiledField, error) {
	if err := Validate(definition); err != nil {
		return CompiledField{}, err
	}
	value, err := compileValueField(definition, resolveRelation)
	if err != nil {
		return CompiledField{}, err
	}
	value.SetId(definition.Identity.ProviderFieldID)
	var presence core.Field
	if definition.Value.Presence.Mode == PresenceCompanion {
		presence = &core.BoolField{
			Id:     definition.Value.Presence.ProviderFieldID,
			Name:   definition.Value.Presence.PhysicalName,
			Hidden: true,
		}
	}
	return CompiledField{Value: value, Presence: presence}, nil
}

func compileValueField(
	definition FieldDefinition,
	resolveRelation RelationCollectionResolver,
) (core.Field, error) {
	name := definition.Identity.PhysicalName
	options := definition.Storage.Options
	minimum := numericRangePointer(definition.Constraints.Range.Min)
	maximum := numericRangePointer(definition.Constraints.Range.Max)
	minLength := definition.Constraints.Length.Min
	maxLength := definition.Constraints.Length.Max
	pattern := ""
	if definition.Constraints.Pattern.Enabled {
		pattern = definition.Constraints.Pattern.Value
	}
	// Product required semantics are enforced through presence. PocketBase
	// Required is intentionally false for all nullable product fields so zero,
	// false, empty containers, and (0,0) remain valid explicit values.
	switch definition.LogicalType {
	case LogicalText, LogicalTime:
		return &core.TextField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			Min: dereferenceInt(minLength), Max: dereferenceInt(maxLength), Pattern: pattern,
		}, nil
	case LogicalEditor:
		return &core.EditorField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			MaxSize: int64(options.MaxSize), ConvertURLs: options.ConvertURLs,
		}, nil
	case LogicalNumber:
		return &core.NumberField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			OnlyInt: options.OnlyInt, Min: minimum, Max: maximum,
		}, nil
	case LogicalBool:
		return &core.BoolField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
		}, nil
	case LogicalDate, LogicalDateTime:
		return &core.DateField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
		}, nil
	case LogicalAutoDate:
		return &core.AutodateField{
			Name:     name,
			OnCreate: true, OnUpdate: definition.AutoDate.Role == "updatedAt",
		}, nil
	case LogicalEmail:
		return &core.EmailField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			OnlyDomains:   append([]string(nil), definition.Constraints.Domains.Only...),
			ExceptDomains: append([]string(nil), definition.Constraints.Domains.Except...),
		}, nil
	case LogicalURL:
		return &core.URLField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			OnlyDomains:   append([]string(nil), definition.Constraints.Domains.Only...),
			ExceptDomains: append([]string(nil), definition.Constraints.Domains.Except...),
		}, nil
	case LogicalSelect, LogicalMultiSelect:
		values := make([]string, 0, len(definition.Select.Options))
		for _, option := range definition.Select.Options {
			values = append(values, option.OptionID)
		}
		maxSelect := 1
		if definition.LogicalType == LogicalMultiSelect {
			maxSelect = len(values)
			if definition.Constraints.Selection.Max != nil {
				maxSelect = *definition.Constraints.Selection.Max
			}
		}
		return &core.SelectField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			Values: values, MaxSelect: maxSelect,
		}, nil
	case LogicalRelation:
		if resolveRelation == nil {
			return nil, fmt.Errorf("relation collection resolver is required")
		}
		collectionID, err := resolveRelation(definition.Relation.TargetTableID)
		if err != nil {
			return nil, err
		}
		// PocketBase treats MaxSelect <= 1 as scalar. Product-level many
		// relations intentionally have no fixed record-count cap, so represent
		// that contract with PocketBase's maximum JSON-safe integer.
		maxSelect := int(1<<53 - 1)
		if definition.Relation.Cardinality == "one" {
			maxSelect = 1
		}
		return &core.RelationField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			CollectionId: collectionID, MaxSelect: maxSelect,
			CascadeDelete: definition.Relation.DeletePolicy == "cascade",
		}, nil
	case LogicalFile:
		return &core.FileField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			MaxSelect: definition.File.MaxFiles, MaxSize: definition.File.MaxBytesPerFile,
			MimeTypes: append([]string(nil), definition.File.AllowedMIMETypes...),
			Thumbs:    append([]string(nil), definition.File.Thumbs...),
			Protected: definition.File.Protected,
		}, nil
	case LogicalGeoPoint:
		return &core.GeoPointField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
		}, nil
	case LogicalJSON:
		return &core.JSONField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
			MaxSize: int64(definition.JSON.MaxSize),
		}, nil
	case LogicalFormula, LogicalLookup:
		return &core.JSONField{
			Name: name, Help: definition.Help, Presentable: options.Presentable,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported logical type %q", definition.LogicalType)
	}
}

func numericRangePointer(value any) *float64 {
	number, present, _ := rangeNumber(value)
	if !present {
		return nil
	}
	return &number
}

func CompileUniqueIndex(
	tablePhysicalName string,
	definition FieldDefinition,
) (string, bool, error) {
	if !definition.Constraints.Unique.Enabled {
		return "", false, nil
	}
	capability, err := CapabilityFor(definition.LogicalType)
	if err != nil {
		return "", false, err
	}
	if !capability.SupportsUnique {
		return "", false, unsupported(
			"constraints.unique.enabled", "logical type cannot enforce a stable unique index",
		)
	}
	if !physicalNamePattern.MatchString(definition.Identity.PhysicalName) {
		return "", false, invalid("identity.physicalName", "invalid physical name")
	}
	if tablePhysicalName == "" || strings.ContainsAny(tablePhysicalName, "`\x00") {
		return "", false, invalid("table.physicalName", "invalid table physical name")
	}
	indexName := "uniq_" + tablePhysicalName + "_" + definition.Identity.PhysicalName
	where := ""
	if definition.Value.Presence.Mode == PresenceCompanion {
		where = fmt.Sprintf(
			" WHERE `%s` = 1",
			definition.Value.Presence.PhysicalName,
		)
	}
	return fmt.Sprintf(
		"CREATE UNIQUE INDEX `%s` ON `%s` (`%s`)%s",
		indexName,
		tablePhysicalName,
		definition.Identity.PhysicalName,
		where,
	), true, nil
}

func dereferenceInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
