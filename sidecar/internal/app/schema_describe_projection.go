package app

import (
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// projectSchemaDescribe projects validated SchemaExecution/Relation read results.
// The loader retains the caller's context and error classification; no RPC is registered here.
func projectSchemaDescribe(snapshot v2.SchemaSnapshot, catalog relation.CatalogResult, generation json.Number, load func(string) (v2.SchemaSnapshot, error)) (map[string]any, error) {
	definition, err := schemaSnapshotProductResult(snapshot)
	if err != nil {
		return nil, err
	}
	if err := normalizeDescribeRanges(definition); err != nil {
		return nil, err
	}
	capabilityHash, err := describeRevision(definition)
	if err != nil {
		return nil, err
	}
	lookupRevision, err := describeRevision(map[string]any{"schemaRevision": snapshot.SchemaRevision, "lookups": catalog.Lookups})
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"contract": "vibetable.schema-describe.v1", "collection": snapshot.TableID, "requestGeneration": generation,
		"schema": map[string]any{
			"collection": snapshot.TableID, "primaryKey": "id", "primaryDisplayFieldId": "",
			"columns": []any{map[string]any{
				"name": "id", "title": "ID", "fieldId": "id", "kind": "system",
				"relationId": nil, "lookupId": nil, "dataType": "text", "editable": false, "nullable": false,
				"scale": nil, "precision": nil, "attachmentPolicy": nil,
				"filterOperators": []string{"eq", "ne", "in", "contains", "starts_with", "ends_with", "is_null", "is_not_null"},
			}},
			"normalizedRelations": []any{}, "schemaRevision": snapshot.SchemaRevision, "permissionRevision": snapshot.SchemaRevision,
			"capabilityHash": capabilityHash, "lookupRevision": lookupRevision,
		},
		"capabilities": map[string]any{
			"contract": "vibetable.relation-capabilities.v1", "relationReadV1": true, "relationEditV1": true,
			"lookupQueryV1": true, "lookupMaxDepth": catalog.LookupMaxDepth, "reason": nil,
		},
	}
	schema := result["schema"].(map[string]any)
	columns := schema["columns"].([]any)
	capabilities := map[v2.LogicalType]v2.Capability{}
	for _, capability := range snapshot.Capabilities {
		capabilities[capability.LogicalType] = capability
	}
	primary := ""
	tables := map[string]v2.SchemaSnapshot{snapshot.TableID: snapshot}
	for _, field := range snapshot.Fields {
		kind := "scalar"
		var relationID, lookupID any
		effectiveType := field.LogicalType
		switch field.LogicalType {
		case v2.LogicalRelation:
			kind, relationID = "relation", snapshot.TableID+"."+field.Identity.FieldID
		case v2.LogicalFormula:
			if field.Formula == nil {
				return nil, errors.New("Formula field omitted its formula definition")
			}
			kind, effectiveType = "formula", field.Formula.ResultType
		case v2.LogicalLookup:
			kind, lookupID = "lookup", snapshot.TableID+"."+field.Identity.FieldID
		}
		if field.LogicalType == v2.LogicalFile {
			kind = "attachment"
		}
		if field.LogicalType == v2.LogicalAutoDate {
			kind = "system"
		}
		readonly := describeFieldReadonly(snapshot, field)
		if primary == "" && !readonly && field.LogicalType != v2.LogicalRelation {
			primary = field.Identity.FieldID
		}
		dataType, err := describeDataType(effectiveType)
		if err != nil {
			return nil, err
		}
		if kind == "formula" && effectiveType == v2.LogicalNumber && field.Storage.Options.OnlyInt {
			dataType = "integer"
		}
		if kind == "lookup" {
			effectiveType, err = describeLookupType(snapshot, field, tables, load)
			if err != nil {
				return nil, err
			}
		}
		operators, err := describeFilterOperators(effectiveType)
		if err != nil {
			return nil, err
		}
		var attachment any
		if kind == "attachment" && field.File != nil {
			attachment = map[string]any{
				"maxFiles": field.File.MaxFiles, "maxBytesPerFile": field.File.MaxBytesPerFile,
				"allowedMimeTypes": field.File.AllowedMIMETypes, "thumbnailVariants": field.File.Thumbs, "protected": field.File.Protected,
			}
		}
		capability := capabilities[field.LogicalType]
		summaries := capability.SummaryOperations
		if summaries == nil {
			summaries = []string{}
		}
		columns = append(columns, map[string]any{
			"name": field.Identity.PhysicalName, "title": field.DisplayName, "fieldId": field.Identity.FieldID,
			"kind": kind, "relationId": relationID, "lookupId": lookupID, "dataType": dataType,
			"editable": !readonly, "nullable": !field.Value.Required, "scale": field.Display.DisplayScale,
			"precision": nil, "attachmentPolicy": attachment, "filterOperators": operators,
			"groupable": capability.Groupable, "summaryOperations": summaries,
		})
	}
	if primary == "" && len(snapshot.Fields) > 0 {
		primary = snapshot.Fields[0].Identity.FieldID
	}
	schema["primaryDisplayFieldId"], schema["columns"] = primary, columns
	relations := []any{}
	for _, descriptor := range catalog.Relations {
		field, err := describeFieldByID(snapshot, descriptor.SourceFieldID)
		if err != nil {
			return nil, err
		}
		kind := "m2m"
		if descriptor.Cardinality == "one" {
			kind = "m2o"
		}
		policy := "restrict"
		switch descriptor.DeletePolicy {
		case "set-null", "setNull", "nullify":
			policy = "nullify"
		case "cascade":
			policy = "cascade"
		}
		relations = append(relations, map[string]any{
			"relationId": descriptor.RelationID, "fieldRef": field.Identity.PhysicalName,
			"sourceCollection": descriptor.SourceTableID, "kind": kind, "relatedCollection": descriptor.TargetTableID,
			"manyField": field.Identity.PhysicalName, "oneField": nil, "unique": descriptor.Cardinality == "one",
			"nullable": !field.Value.Required, "onDelete": policy, "preset": "standard",
			"selfRelation": descriptor.TargetTableID == descriptor.SourceTableID, "managed": true,
			"pairId": descriptor.PairID, "reciprocalFieldId": descriptor.ReciprocalFieldID,
			"quickCreateEligible": descriptor.QuickCreateEligible, "quickCreateReason": descriptor.QuickCreateReason,
			"state": "valid", "displayTemplate": nil, "diagnostics": []any{},
		})
	}
	schema["normalizedRelations"] = relations
	return result, nil
}

func describeLookupType(source v2.SchemaSnapshot, field v2.FieldDefinition, tables map[string]v2.SchemaSnapshot, load func(string) (v2.SchemaSnapshot, error)) (v2.LogicalType, error) {
	if field.Lookup == nil {
		return "", errors.New("Lookup field omitted its lookup definition")
	}
	if len(field.Lookup.Path) == 0 {
		return "", errors.New("Lookup field omitted its relation path")
	}
	current, many := source, false
	for _, step := range field.Lookup.Path {
		related, err := describeFieldByID(current, step.RelationFieldID)
		if err != nil {
			return "", err
		}
		if related.Relation == nil {
			return "", errors.New("Lookup path relation metadata is unavailable")
		}
		if related.Relation.Cardinality != "one" && related.Relation.Cardinality != "many" {
			return "", errors.New("Lookup path relation cardinality is invalid")
		}
		many = many || related.Relation.Cardinality == "many"
		targetID := related.Relation.TargetTableID
		target, found := tables[targetID]
		if !found {
			target, err = load(targetID)
			if err != nil {
				return "", err
			}
			tables[targetID] = target
		}
		current = target
	}
	target, err := describeFieldByID(current, field.Lookup.TargetFieldID)
	if err != nil {
		return "", err
	}
	if many || target.LogicalType == v2.LogicalLookup {
		return v2.LogicalJSON, nil
	}
	if target.LogicalType == v2.LogicalFormula {
		if target.Formula == nil {
			return "", errors.New("Formula field omitted its formula definition")
		}
		return target.Formula.ResultType, nil
	}
	return target.LogicalType, nil
}

func describeFieldByID(table v2.SchemaSnapshot, id string) (v2.FieldDefinition, error) {
	for _, field := range table.Fields {
		if field.Identity.FieldID == id {
			return field, nil
		}
	}
	return v2.FieldDefinition{}, errors.New("Lookup target field is unavailable")
}

func describeFieldReadonly(table v2.SchemaSnapshot, field v2.FieldDefinition) bool {
	return table.Kind == "view" || field.Lifecycle.State != v2.LifecycleActive ||
		field.LogicalType == v2.LogicalAutoDate || field.LogicalType == v2.LogicalFormula || field.LogicalType == v2.LogicalLookup
}

func describeDataType(kind v2.LogicalType) (string, error) {
	switch kind {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalEmail, v2.LogicalURL, v2.LogicalSelect, v2.LogicalMultiSelect, v2.LogicalRelation, v2.LogicalFile:
		return "text", nil
	case v2.LogicalNumber:
		return "decimal", nil
	case v2.LogicalBool:
		return "boolean", nil
	case v2.LogicalDate:
		return "date", nil
	case v2.LogicalDateTime, v2.LogicalAutoDate:
		return "datetime", nil
	case v2.LogicalTime:
		return "time", nil
	case v2.LogicalJSON, v2.LogicalGeoPoint, v2.LogicalLookup:
		return "json", nil
	default:
		return "", errors.New("PocketBase returned an unknown data type")
	}
}

func describeFilterOperators(kind v2.LogicalType) ([]string, error) {
	var operators []string
	switch kind {
	case v2.LogicalText, v2.LogicalEditor, v2.LogicalTime, v2.LogicalEmail, v2.LogicalURL, v2.LogicalSelect:
		operators = []string{"eq", "ne", "in", "contains", "starts_with", "ends_with"}
	case v2.LogicalNumber, v2.LogicalDate, v2.LogicalDateTime, v2.LogicalAutoDate:
		operators = []string{"eq", "ne", "in", "gt", "lt", "gte", "lte", "between"}
	case v2.LogicalBool, v2.LogicalRelation:
		operators = []string{"eq", "ne", "in"}
	case v2.LogicalJSON, v2.LogicalGeoPoint, v2.LogicalFile, v2.LogicalMultiSelect:
		operators = []string{"contains"}
	default:
		return nil, errors.New("PocketBase returned an unsupported query field type")
	}
	return append(operators, "is_null", "is_not_null"), nil
}
