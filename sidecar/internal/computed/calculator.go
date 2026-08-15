package computed

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
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
	result := map[string]any{}
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
