package schema

// MaxLookupPathDepth is the product capability advertised to clients and
// enforced by schema validation and lookup traversal.
const MaxLookupPathDepth = 8

// QueryFilterOperators returns the authoritative filter capability exposed to
// clients for one normalized field. The query compiler remains the enforcement
// boundary; this list prevents each client from independently guessing support.
func QueryFilterOperators(field FieldDefinition) []string {
	if field.Kind == FieldKindRelation {
		return []string{"eq", "ne", "in", "is_null", "is_not_null"}
	}
	dataType := field.DataType
	if field.Kind == FieldKindFormula && field.Formula != nil {
		dataType = field.Formula.ResultType
	}
	if field.Kind == FieldKindLookup {
		switch field.StorageType {
		case StorageNumber:
			dataType = DataTypeDecimal
		case StorageBool:
			dataType = DataTypeBoolean
		case StorageDate:
			dataType = DataTypeDateTime
		case StorageJSON:
			dataType = DataTypeJSON
		default:
			dataType = DataTypeShortText
		}
	}
	switch dataType {
	case DataTypeInteger, DataTypeFloat, DataTypeDecimal,
		DataTypeDate, DataTypeDateTime, DataTypeAutoDate, DataTypeTime:
		return []string{
			"eq", "ne", "gt", "gte", "lt", "lte", "between", "in",
			"is_null", "is_not_null",
		}
	case DataTypeBoolean:
		return []string{"eq", "ne", "in", "is_null", "is_not_null"}
	case DataTypeJSON, DataTypeGeoPoint, DataTypeGeoJSON, DataTypeList:
		return []string{"contains", "is_null", "is_not_null"}
	default:
		return []string{
			"eq", "ne", "contains", "starts_with", "ends_with", "in",
			"is_null", "is_not_null",
		}
	}
}
