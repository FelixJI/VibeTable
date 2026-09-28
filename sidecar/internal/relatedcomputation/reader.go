package relatedcomputation

import (
	"context"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// SourceReader exposes the scalar that one stored computed cell must
// contribute to a formula relation read, relation aggregate or Lookup
// projection. It fails closed: a missing or stale envelope becomes an explicit
// formula.dependency error, never a silent old value or null. One reader
// belongs to one calculation batch and its transaction.
type SourceReader struct {
	expectations map[sourceFieldKey]sourceFieldVersion
}

type sourceFieldKey struct {
	tableID, fieldID string
	// The dependency watermark includes the batch clock signature, so a
	// memoized version is only reusable within one evaluation instant.
	instant time.Time
}

type sourceFieldVersion struct {
	definitionVersion   int
	dependencyWatermark string
}

func NewSourceReader() *SourceReader {
	return &SourceReader{expectations: map[sourceFieldKey]sourceFieldVersion{}}
}

// EnsureClockCache pins one dependency-graph cache for a standalone read
// batch whose caller has no transaction-scoped cache yet (Lookup grid
// batches and paged source details). An existing cache is kept untouched.
func EnsureClockCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(clockCacheKey{}).(*clockCache); ok {
		return ctx
	}
	return WithClockCache(ctx)
}

// Read returns the fresh scalar of one source cell. Callers gate on the
// schema (formula.IsComputedSource); the check is repeated here so the whole
// freshness contract stays in this package.
func (reader *SourceReader) Read(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	field v2.FieldDefinition,
	record *core.Record,
) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !formula.IsComputedSource(field) {
		return record.GetRaw(field.Identity.PhysicalName), nil
	}
	envelope, valid := Decode(record.GetRaw(field.Identity.PhysicalName))
	if !valid {
		return nil, staleSourceError(tableID, field, "computed source cell has no readable envelope")
	}
	version, versionErr := reader.fieldVersion(ctx, app, tableID, fields, field)
	if versionErr != nil {
		return nil, versionErr
	}
	expectation := Expectation{
		DefinitionVersion:   version.definitionVersion,
		SourceDataRevision:  int64(record.GetInt(RowRevisionField)),
		DependencyWatermark: version.dependencyWatermark,
	}
	if !envelope.Fresh(expectation) {
		return nil, staleSourceError(tableID, field, "computed source cell is stale for this evaluation")
	}
	return envelope.Value, nil
}

// fieldVersion memoizes the record-independent part of ExpectationFor for one
// batch: dependency revisions and clock signatures are transaction-stable, so
// only the row revision varies between records of the same field.
func (reader *SourceReader) fieldVersion(
	ctx context.Context,
	app core.App,
	tableID string,
	fields []v2.FieldDefinition,
	field v2.FieldDefinition,
) (sourceFieldVersion, error) {
	key := sourceFieldKey{tableID, field.Identity.FieldID, formula.EvaluationTime(ctx)}
	if cached, ok := reader.expectations[key]; ok {
		return cached, nil
	}
	expectation, err := ExpectationFor(ctx, app, tableID, fields, field.Identity.FieldID, 0)
	if err != nil {
		return sourceFieldVersion{}, err
	}
	cached := sourceFieldVersion{
		definitionVersion:   expectation.DefinitionVersion,
		dependencyWatermark: expectation.DependencyWatermark,
	}
	reader.expectations[key] = cached
	return cached, nil
}

func staleSourceError(tableID string, field v2.FieldDefinition, message string) *formula.Error {
	return &formula.Error{
		ContractVersion: formula.ContractVersion,
		Code:            "formula.dependency",
		Message:         message,
		Details: map[string]any{
			"sourceTableId": tableID,
			"sourceFieldId": field.Identity.FieldID,
		},
	}
}
