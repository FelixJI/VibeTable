package computed

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryfilter"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// collectionPageSize is the fixed authoritative scan window. It matches the
// lookup traversal batch and CompileMatchBatch's hard 256-row page ceiling.
const collectionPageSize = 256

// NewCollectionSourceReader returns the authoritative TABLE() source reader
// for one app handle. Each read reuses the calculation's authoritative schema
// resolver and queryschema descriptor, walks the complete collection in stable
// primary-key order via query.CompileMatchBatch and streams one projected row
// per matched record. The caller's context keeps owning the formula budget;
// yield and cancellation errors propagate unchanged, and no state is cached
// between reads.
func NewCollectionSourceReader(app core.App) formula.CollectionSourceReader {
	return func(ctx context.Context, request formula.CollectionReadRequest, yield func(map[string]any) error) error {
		return readCollectionSource(ctx, app, request, yield)
	}
}

// WithCollectionSources binds the authoritative collection reader into a
// calculation context. Wiring and budget ownership stay with the caller.
func WithCollectionSources(ctx context.Context, app core.App) context.Context {
	return formula.WithCollectionSourceReader(ctx, NewCollectionSourceReader(app))
}

// collectionFieldBinding pairs the caller's field contract (stable field id
// plus the physical name its compiled formula looks rows up by) with the
// authoritative definition that owns storage access and freshness.
type collectionFieldBinding struct {
	requested     v2.FieldDefinition
	authoritative v2.FieldDefinition
	computed      bool
}

func readCollectionSource(
	ctx context.Context,
	app core.App,
	request formula.CollectionReadRequest,
	yield func(map[string]any) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if app == nil {
		return collectionDependencyError(request.TableID, "", "collection source app is unavailable", nil)
	}
	// Pin one evaluation instant and dependency-graph cache for this read so
	// the freshness reader's memoized expectations cannot drift mid-scan.
	ctx = relatedcomputation.EnsureClockCache(formula.EnsureEvaluationTime(ctx))
	source, err := queryschema.New(app.DataDir())
	if err != nil {
		return collectionDependencyError(request.TableID, "", "collection source database identity is unavailable", err)
	}
	definition, err := formula.LoadCollectionSchema(ctx, app, request.TableID)
	if err != nil {
		return collectionReadError(request.TableID, "collection source schema is unavailable", err)
	}
	descriptor, snapshot, err := source.DescribeResolvedSelectionTable(ctx, app, definition)
	if err != nil {
		return collectionReadError(request.TableID, "collection source schema is unavailable", err)
	}
	if snapshot.TableID != request.TableID || descriptor.TableID != request.TableID {
		return collectionDependencyError(request.TableID, "", "collection source table identity differs from the request", nil)
	}
	bindings, err := collectionBindings(request, snapshot)
	if err != nil {
		return err
	}
	collection, err := app.FindCollectionByNameOrId(descriptor.PhysicalName)
	if err != nil {
		return collectionReadError(request.TableID, "collection source storage is unavailable", err)
	}
	group := request.Filters
	if len(group) == 0 {
		// An absent predicate list is TABLE()'s complete-collection range. A
		// nil group would compile to zero matches, so the unfiltered scan
		// uses one legal typed primary-key predicate instead; the stable
		// id > afterID keyset appended by CompileMatchBatch supplies the
		// "id > ''" lower bound on every page.
		group = []queryfilter.FilterExpression{{
			Field:    descriptor.PrimaryKey,
			Operator: queryfilter.OperatorNotEqual,
			Value:    "",
			Logic:    queryfilter.LogicAnd,
		}}
	}
	freshness := relatedcomputation.NewSourceReader()
	afterID := ""
	for {
		compiled, err := query.CompileMatchBatch(
			descriptor, [][]queryfilter.FilterExpression{group}, afterID, collectionPageSize,
		)
		if err != nil {
			return collectionDependencyError(request.TableID, "", "collection source query could not be compiled", err)
		}
		records, err := collectionPageRecords(ctx, app, request.TableID, collection, compiled, bindings)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			row, err := projectCollectionRow(ctx, app, request.TableID, snapshot, bindings, freshness, record)
			if err != nil {
				return err
			}
			if err := yield(row); err != nil {
				return err
			}
		}
		if len(records) < collectionPageSize {
			return nil
		}
		afterID = records[len(records)-1].Id
	}
}

// collectionBindings validates every requested field against the
// authoritative snapshot by stable field id. The caller's physical names are
// never trusted for storage access; missing fields or logical types that
// drifted from the schema fail closed.
func collectionBindings(request formula.CollectionReadRequest, snapshot v2.SchemaSnapshot) ([]collectionFieldBinding, error) {
	bindings := make([]collectionFieldBinding, 0, len(request.Fields))
	seen := map[string]bool{}
	for _, requested := range request.Fields {
		fieldID := requested.Identity.FieldID
		if fieldID == "" {
			return nil, collectionDependencyError(
				request.TableID, requested.Identity.PhysicalName,
				"collection source field has no stable identity", nil,
			)
		}
		if seen[fieldID] {
			continue
		}
		authoritative, ok := snapshotFieldByID(snapshot.Fields, fieldID)
		if !ok {
			return nil, collectionDependencyError(
				request.TableID, fieldID,
				"collection source field is missing from the authoritative schema", nil,
			)
		}
		if authoritative.LogicalType != requested.LogicalType ||
			authoritative.Identity.PhysicalName != requested.Identity.PhysicalName ||
			authoritative.Storage.Options.OnlyInt != requested.Storage.Options.OnlyInt ||
			!reflect.DeepEqual(authoritative.Formula, requested.Formula) {
			return nil, collectionDependencyError(
				request.TableID, fieldID,
				"collection source field type differs from the authoritative schema", nil,
			)
		}
		seen[fieldID] = true
		bindings = append(bindings, collectionFieldBinding{
			requested:     requested,
			authoritative: authoritative,
			computed:      formula.IsComputedSource(authoritative),
		})
	}
	return bindings, nil
}

func snapshotFieldByID(fields []v2.FieldDefinition, fieldID string) (v2.FieldDefinition, bool) {
	for _, field := range fields {
		if field.Identity.FieldID == fieldID {
			return field, true
		}
	}
	return v2.FieldDefinition{}, false
}

func collectionPageRecords(
	ctx context.Context, app core.App, tableID string,
	collection *core.Collection, compiled query.CompiledQuery, bindings []collectionFieldBinding,
) ([]*core.Record, error) {
	names := []string{"id", relatedcomputation.RowRevisionField}
	for _, binding := range bindings {
		names = append(names, binding.authoritative.Identity.PhysicalName)
		if name := binding.authoritative.Value.Presence.PhysicalName; name != "" {
			names = append(names, name)
		}
	}
	// PocketBase prepares every collection field even with a narrow SELECT.
	// These records are read-only projections: retain authoritative field
	// objects in a private list without mutating the shared collection. Stale
	// computed sources still reload a complete record in sourceEvaluator.
	projection := *collection
	projection.Fields = make(core.FieldsList, 0, len(names))
	for _, name := range names {
		field := collection.Fields.GetByName(name)
		if field == nil {
			return nil, collectionDependencyError(tableID, name, "collection source physical field is unavailable", nil)
		}
		projection.Fields = append(projection.Fields, field)
	}
	// Keep QueryCompiler's typed predicates, archive policy and keyset limit
	// inside the same statement as PocketBase's authoritative value decoding.
	// The outer ordering preserves the page's stable id traversal.
	// Rows are scanned with the standard database/sql scanner and one reusable
	// NullString buffer (no per-row reflection map); values still pass through
	// the authoritative field.PrepareValue, and the records are internal
	// read-only projections that must never be saved.
	rows, err := app.RecordQuery(&projection).Select(names...).WithContext(ctx).
		AndWhere(dbx.NewExp("id IN (SELECT id FROM ("+compiled.SQL+"))", dbx.Params(compiled.Params))).
		OrderBy("id").Rows()
	if err != nil {
		return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
	}
	defer rows.Close()
	scanBuffer := make([]sql.NullString, len(names))
	scanRefs := make([]any, len(scanBuffer))
	for index := range scanBuffer {
		scanRefs[index] = &scanBuffer[index]
	}
	records := make([]*core.Record, 0, collectionPageSize)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := rows.Scan(scanRefs...); err != nil {
			return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
		}
		record := core.NewRecord(&projection)
		for index, field := range projection.Fields {
			var raw any
			if scanBuffer[index].Valid {
				raw = scanBuffer[index].String
			}
			value, err := field.PrepareValue(record, raw)
			if err != nil {
				return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
			}
			record.SetRaw(field.GetName(), value)
		}
		if err := record.BaseModel.PostScan(); err != nil {
			return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
	}
	if err := rows.Close(); err != nil {
		return nil, collectionReadError(tableID, "collection source records could not be loaded", err)
	}
	return records, nil
}

func projectCollectionRow(
	ctx context.Context,
	app core.App,
	tableID string,
	snapshot v2.SchemaSnapshot,
	bindings []collectionFieldBinding,
	freshness *relatedcomputation.SourceReader,
	record *core.Record,
) (map[string]any, error) {
	base := make([]v2.FieldDefinition, 0, len(bindings))
	for _, binding := range bindings {
		if !binding.computed {
			base = append(base, binding.authoritative)
		}
	}
	// productrow.Project keeps base-field presence semantics authoritative
	// and always contributes the row id, so empty field lists still COUNT.
	projectedBase := productrow.Project(base, record)
	row := map[string]any{"id": record.Id}
	for _, binding := range bindings {
		name := binding.requested.Identity.PhysicalName
		if binding.computed {
			// Referenced computed cells resolve through the batch freshness
			// reader; stale envelopes fail closed instead of projecting an
			// old value. Unreferenced computed fields are never read.
			reader := formula.ComputedSourceReaderFor(ctx)
			if reader == nil {
				reader = freshness.Read
			}
			value, err := reader(ctx, app, tableID, snapshot.Fields, binding.authoritative, record)
			if err != nil {
				return nil, err
			}
			row[name] = value
			continue
		}
		row[name] = projectedBase[binding.authoritative.Identity.PhysicalName]
	}
	return row, nil
}

// collectionReadError maps storage failures while preserving cancellation as
// the caller's own context error.
func collectionReadError(tableID, message string, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return collectionDependencyError(tableID, "", message, cause)
}

func collectionDependencyError(tableID, fieldID, message string, cause error) *formula.Error {
	details := map[string]any{}
	if tableID != "" {
		details["sourceTableId"] = tableID
	}
	if fieldID != "" {
		details["sourceFieldId"] = fieldID
	}
	if cause != nil {
		details["reason"] = cause.Error()
	}
	return &formula.Error{
		ContractVersion: formula.ContractVersion,
		Code:            "formula.dependency",
		Message:         message,
		Details:         details,
	}
}
