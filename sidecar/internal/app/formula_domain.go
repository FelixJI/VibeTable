package app

import (
	"context"

	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/schemav2wire"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
)

// REST and the Product dispatcher share these formula authorities: one
// app-scoped compiler, the field-change draft catalog and the read-only
// schema snapshot in formulaRequestTable. A new transport must not build its
// own compiler, planner or formula semantics.
type formulaDomain struct {
	app      core.App
	compiler *formula.Compiler
}

func (domain formulaDomain) validateDraft(
	ctx context.Context,
	tableID string,
	displaySource string,
) (fieldchange.FormulaDraftInspection, error) {
	if tableID == "" || displaySource == "" {
		return fieldchange.FormulaDraftInspection{}, formulaRequestError(
			"tableId and displaySource are required",
		)
	}
	inspection, err := fieldchange.NewCatalog(domain.app).InspectFormulaDraft(ctx, tableID, displaySource)
	if err != nil {
		return fieldchange.FormulaDraftInspection{}, err
	}
	// REST renders through PocketBase's JSON writer, which emits empty arrays
	// for nil slices; keep the Product dispatcher's stdlib rendering identical.
	if inspection.Dependencies == nil {
		inspection.Dependencies = []string{}
	}
	if inspection.RelationAggregatePaths == nil {
		inspection.RelationAggregatePaths = []string{}
	}
	return inspection, nil
}

func (domain formulaDomain) validate(
	ctx context.Context,
	tableID string,
	field schemav2wire.FieldDefinition,
) (map[string]any, error) {
	definition, err := formulaRequestTable(ctx, domain.app, tableID, field)
	if err != nil {
		return nil, err
	}
	plan, formulaErr := domain.compiler.CompileExecutionTable(definition)
	if formulaErr != nil {
		return nil, formulaErr
	}
	metadata := make([]formulaMetadata, 0, len(plan.Formulas))
	for _, compiled := range plan.Formulas {
		dependencies := compiled.Dependencies
		if dependencies == nil {
			dependencies = []string{}
		}
		metadata = append(metadata, formulaMetadata{
			FieldID: compiled.FieldID, CanonicalSource: compiled.CanonicalSource,
			ASTHash: compiled.ASTHash, Dependencies: dependencies,
		})
	}
	return map[string]any{"formulas": metadata}, nil
}

func (domain formulaDomain) preview(
	ctx context.Context,
	input schemav2wire.FormulaPreviewRequest,
) (map[string]any, error) {
	if input.Row == nil {
		return nil, formulaRequestError("row is required")
	}
	definition, err := formulaRequestTable(ctx, domain.app, input.TableId, input.Field)
	if err != nil {
		return nil, err
	}
	plan, formulaErr := domain.compiler.CompileExecutionTable(definition)
	if formulaErr != nil {
		return nil, formulaErr
	}
	row, err := decodeFormulaRow(input.Row)
	if err != nil {
		return nil, err
	}
	values, formulaErr := plan.Evaluate(ctx, row, input.ChangedFieldIds)
	if formulaErr != nil {
		return nil, formulaErr
	}
	return map[string]any{"values": values}, nil
}
