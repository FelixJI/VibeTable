package app

import (
	"context"
	"errors"

	"github.com/pocketbase/pocketbase/core"

	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/schemav2wire"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
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
	input formulaDraftValidateRequest,
) (fieldchange.FormulaDraftInspection, error) {
	tableID, displaySource := input.TableID, input.DisplaySource
	if tableID == "" || displaySource == "" {
		return fieldchange.FormulaDraftInspection{}, formulaRequestError(
			"tableId and displaySource are required",
		)
	}
	catalog := fieldchange.NewCatalog(domain.app)
	var authored *formula.AuthorResult
	var err error
	if input.RestoreSource {
		if input.AuthorDocument != nil {
			return fieldchange.FormulaDraftInspection{}, formulaRequestError("restoreSource and authorDocument are mutually exclusive")
		}
		authored, err = catalog.RestoreFormulaDocument(ctx, tableID, displaySource, 1)
	} else if input.AuthorDocument != nil {
		if input.AuthorDocument.DisplaySource != displaySource {
			return fieldchange.FormulaDraftInspection{}, formulaRequestError("authorDocument displaySource differs")
		}
		authored, err = catalog.AuthorFormulaDocument(ctx, tableID, *input.AuthorDocument)
	}
	if err != nil {
		var formulaErr *formula.Error
		if authored != nil && errors.As(err, &formulaErr) {
			if formulaErr.Details == nil {
				formulaErr.Details = map[string]any{}
			}
			formulaErr.Details["authorDocument"] = authored.Document
		}
		return fieldchange.FormulaDraftInspection{}, err
	}
	if authored != nil {
		displaySource = authored.CanonicalSource
	}
	inspection, err := catalog.InspectFormulaDraft(ctx, tableID, displaySource)
	if err != nil {
		var formulaErr *formula.Error
		if authored != nil && errors.As(err, &formulaErr) && formulaErr.SourceSpan != nil {
			if span, ok := authored.SourceMap.DisplayRange(*formulaErr.SourceSpan); ok {
				if formulaErr.Details == nil {
					formulaErr.Details = map[string]any{}
				}
				formulaErr.Details["range"] = span
			}
		}
		return fieldchange.FormulaDraftInspection{}, err
	}
	if authored != nil {
		document := authored.Document
		if document.Tokens == nil {
			document.Tokens = []workbench.FormulaAuthorToken{}
		}
		inspection.AuthorDocument = &document
		inspection.Functions = formula.FunctionCatalog()
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
	ctx = computed.WithCollectionSources(ctx, domain.app)
	values, formulaErr := plan.Evaluate(ctx, row, input.ChangedFieldIds)
	if formulaErr != nil {
		return nil, formulaErr
	}
	return map[string]any{"values": values}, nil
}
