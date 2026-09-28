package formula

import (
	"context"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// ComputedSourceReader unwraps one stored computed cell into the fresh scalar
// that a relation read, relation aggregate or Lookup projection must observe,
// or fails. The freshness contract lives in relatedcomputation, which already
// imports this package, so computed.Composite injects the implementation
// through the context instead of a reverse import.
type ComputedSourceReader func(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	field v2.FieldDefinition,
	record *core.Record,
) (any, error)

type computedSourceReaderKey struct{}

// WithComputedSourceReader binds one reader to one calculation batch. A nil
// reader keeps the legacy stored-value behavior.
func WithComputedSourceReader(ctx context.Context, reader ComputedSourceReader) context.Context {
	if reader == nil {
		return ctx
	}
	return context.WithValue(ctx, computedSourceReaderKey{}, reader)
}

func ComputedSourceReaderFor(ctx context.Context) ComputedSourceReader {
	reader, _ := ctx.Value(computedSourceReaderKey{}).(ComputedSourceReader)
	return reader
}

// IsComputedSource reports whether the schema itself declares this field as a
// stored computed cell. Envelope-shaped user JSON never matches this gate, so
// only authoritative Formula/Lookup columns are ever unwrapped.
func IsComputedSource(field v2.FieldDefinition) bool {
	return (field.LogicalType == v2.LogicalFormula && field.Formula != nil) ||
		(field.LogicalType == v2.LogicalLookup && field.Lookup != nil)
}

// computedSourceValue reads one materialized relation target field. Only a
// referenced, schema-declared computed source resolves through the freshness
// reader, and a missing reader is an explicit formula.dependency failure
// instead of a silent envelope fallback. Every other field keeps its stored
// value, so unreferenced stale cells cannot block a relation and lookalike
// user JSON survives untouched.
func computedSourceValue(
	ctx context.Context,
	app core.App,
	target schemaexecution.Table,
	field v2.FieldDefinition,
	record *core.Record,
	referenced bool,
) (any, error) {
	if !referenced || !IsComputedSource(field) {
		return record.GetRaw(field.Identity.PhysicalName), nil
	}
	reader := ComputedSourceReaderFor(ctx)
	if reader == nil {
		return nil, formulaError(
			"formula.dependency",
			"computed relation source has no batch freshness reader",
			map[string]any{
				"sourceTableId": target.Snapshot.TableID,
				"sourceFieldId": field.Identity.FieldID,
			},
		)
	}
	return reader(ctx, app, target.Snapshot.TableID, target.Snapshot.Fields, field, record)
}
