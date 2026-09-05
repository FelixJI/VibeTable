package formula

import (
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// V2Table is the field-authoring input. It contains only Schema V2 identities
// and definitions; execution bindings are supplied by schemaexecution.Table
// when formulas run against PocketBase.
type V2Table struct {
	TableID string
	Fields  []v2.FieldDefinition
}

func executionTable(definition V2Table) schemaexecution.Table {
	return schemaexecution.Table{Snapshot: v2.SchemaSnapshot{
		Contract: v2.Contract,
		TableID:  definition.TableID,
		Fields:   definition.Fields,
	}}
}

func (compiler *Compiler) CompileV2Table(definition V2Table) (*Plan, *Error) {
	return compiler.CompileExecutionTable(executionTable(definition))
}

func (compiler *Compiler) InferV2Source(
	definition V2Table,
	source string,
) (v2.LogicalType, bool, *Error) {
	result, err := compiler.InferExecutionSource(executionTable(definition), source)
	if err != nil {
		return "", false, err
	}
	return result.LogicalType, result.OnlyInt, nil
}

func CanonicalizeV2DisplaySource(
	definition V2Table,
	targets map[string]V2Table,
	displaySource string,
) (string, *Error) {
	result, err := AuthorV2Document(definition, targets, workbench.FormulaAuthorDocument{
		DisplaySource: displaySource, DocumentRevision: 1,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.CanonicalSource), nil
}
