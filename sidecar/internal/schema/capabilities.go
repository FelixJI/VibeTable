package schema

import "fmt"

var capabilities = map[DataType]Capability{
	DataTypeShortText:   {Storage: StorageText, Exact: true},
	DataTypeLongText:    {Storage: StorageEditor, Exact: true},
	DataTypeRichText:    {Storage: StorageEditor, Exact: true},
	DataTypeBoolean:     {Storage: StorageBool, Exact: true},
	DataTypeInteger:     {Storage: StorageNumber, Exact: true},
	DataTypeFloat:       {Storage: StorageNumber, Exact: false},
	DataTypeDecimal:     {Storage: StorageNumber, Exact: false, Note: "number capability; exact decimal round-trip is not proven"},
	DataTypeDate:        {Storage: StorageDate, Exact: true},
	DataTypeDateTime:    {Storage: StorageDate, Exact: true},
	DataTypeAutoDate:    {Storage: StorageAutodate, Exact: true},
	DataTypeTime:        {Storage: StorageText, Exact: true},
	DataTypeEmail:       {Storage: StorageEmail, Exact: true},
	DataTypeURL:         {Storage: StorageURL, Exact: true},
	DataTypeUUID:        {Storage: StorageText, Exact: true},
	DataTypeSelect:      {Storage: StorageSelect, Exact: true},
	DataTypeMultiSelect: {Storage: StorageSelect, Exact: true},
	DataTypeJSON:        {Storage: StorageJSON, Exact: true},
	DataTypeGeoPoint:    {Storage: StorageGeoPoint, Exact: false},
	DataTypeGeoJSON:     {Storage: StorageJSON, Exact: true},
	DataTypeFile:        {Storage: StorageFile, Exact: true},
	DataTypeRelation:    {Storage: StorageRelation, Exact: true},
	DataTypeLookup:      {Storage: StorageJSON, Exact: false},
	DataTypeFormula:     {Storage: StorageJSON, Exact: false},
	DataTypeList:        {Storage: StorageJSON, Exact: true},
	DataTypeHash:        {Storage: StorageText, Exact: true},
	DataTypeSecret:      {Storage: StorageText, Exact: true},
}

func CapabilityFor(dataType DataType) (Capability, error) {
	capability, ok := capabilities[dataType]
	if !ok {
		return Capability{}, fmt.Errorf("unsupported data type %q", dataType)
	}
	return capability, nil
}
