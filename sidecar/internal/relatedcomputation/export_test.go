package relatedcomputation

import (
	"context"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

var (
	DefinitionVersionForTest = definitionVersion
	DependencyTablesForTest  = func(ctx context.Context, app core.App, tableID string, fields []v2.FieldDefinition, field v2.FieldDefinition) ([]string, error) {
		tables, _, _, err := dependencyInputs(ctx, app, tableID, fields, field)
		return tables, err
	}
	DescribeTableFieldsForTest = describeTableFields
)
