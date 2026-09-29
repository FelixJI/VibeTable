package computed

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type Calculator interface {
	Calculate(
		context.Context,
		core.App,
		schemaexecution.Table,
		*core.Record,
	) (map[string]any, error)
}

type Composite struct {
	calculators []Calculator
}

func New(calculators ...Calculator) *Composite {
	filtered := make([]Calculator, 0, len(calculators))
	for _, calculator := range calculators {
		if calculator != nil {
			filtered = append(filtered, calculator)
		}
	}
	return &Composite{calculators: filtered}
}

func (composite *Composite) Calculate(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
	record *core.Record,
) (map[string]any, error) {
	// Pin one evaluation instant and one dependency-graph cache for this call
	// even when a standalone caller passed a bare context, so the reader's
	// memoized expectations and Plan.Evaluate cannot drift apart across
	// fields. Production mutation/jobs contexts already carry both and keep
	// them untouched.
	ctx = relatedcomputation.EnsureClockCache(formula.EnsureEvaluationTime(ctx))
	// Relation aggregates and Lookup projections read stored computed sources
	// through the shared freshness reader. It is scoped to this one Calculate
	// call (mutation and jobs invoke it per record); the instant pinned above
	// keeps its memoized expectations coherent for every record and field
	// this call touches.
	if formula.SourceRecalculationEnabled(ctx) {
		ctx = formula.EnsureSourceEvaluationBudget(ctx)
	}
	if formula.ComputedSourceReaderFor(ctx) == nil {
		// Every calculation root shares its complete authoritative schema
		// between planning and TABLE reads, before applying field selection.
		evaluator := newSourceEvaluator(composite, definition)
		ctx = formula.WithCollectionSchemaResolver(ctx, func(ctx context.Context, id string) (schemaexecution.Table, error) {
			return evaluator.resolve(ctx, app, id)
		})
		reader := evaluator.reader.Read
		if formula.SourceRecalculationEnabled(ctx) {
			reader = evaluator.read
		}
		ctx = formula.WithComputedSourceReader(ctx, reader)
	}
	ctx = WithCollectionSources(ctx, app)
	result := map[string]any{}
	if selected := formula.EvaluationFields(ctx); selected != nil {
		selection := make(map[string]bool, len(selected))
		for id, enabled := range selected {
			selection[id] = enabled
		}
		preserved := map[string]any{}
		rowRevision := int64(record.GetInt(relatedcomputation.RowRevisionField))
		for _, field := range definition.Snapshot.Fields {
			if field.Formula == nil && field.Lookup == nil {
				continue
			}
			name := field.Identity.PhysicalName
			original := record.GetRaw(name)
			envelope, valid := relatedcomputation.Decode(original)
			if !selection[field.Identity.FieldID] && !exactSelection(ctx) {
				expectation, err := relatedcomputation.ExpectationFor(ctx, app, definition.Snapshot.TableID,
					definition.Snapshot.Fields, field.Identity.FieldID, rowRevision)
				if err != nil {
					return nil, err
				}
				if !valid || !envelope.Fresh(expectation) {
					selection[field.Identity.FieldID] = true
				} else {
					// A clock refresh changes no input of this fresh ordinary
					// cell. Retain its complete envelope and row binding.
					// The clock refresh retains the business row revision.
					preserved[name] = envelope
				}
			}
			record.Set(name, relatedcomputation.ProjectStored(original))
		}
		ctx = formula.WithEvaluationFields(ctx, selection)
		defer func() {
			for name, value := range preserved {
				record.Set(name, value)
			}
		}()
	}
	for _, calculator := range composite.calculators {
		values, err := calculator.Calculate(ctx, app, definition, record)
		if err != nil {
			return nil, err
		}
		for field, value := range values {
			if _, duplicate := result[field]; duplicate {
				return nil, fmt.Errorf(
					"computed field %q was produced by multiple calculators",
					field,
				)
			}
			result[field] = value
			// Later calculators may depend on values materialized by an earlier
			// calculator (for example a formula reading a Lookup). Keep the
			// in-transaction record activation in the same order as persistence.
			record.Set(field, value)
		}
	}
	return result, nil
}
