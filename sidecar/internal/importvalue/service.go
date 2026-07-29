package importvalue

import (
	"context"
	"fmt"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const Contract = "vibetable.import-preview.v1"

type Request struct {
	Contract       string `json:"contract"`
	TableID        string `json:"tableId"`
	SchemaRevision string `json:"schemaRevision"`
	Rows           []Row  `json:"rows"`
}

type Row struct {
	Values map[string]any       `json:"values"`
	Mode   fieldvalue.WriteMode `json:"mode,omitempty"`
}

type Result struct {
	Contract string      `json:"contract"`
	Rows     []ResultRow `json:"rows"`
}

type ResultRow struct {
	Values      map[string]any `json:"values"`
	Diagnostics []Diagnostic   `json:"diagnostics"`
}

type Diagnostic struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Service struct {
	catalog *fieldchange.Catalog
	kernel  *fieldvalue.Kernel
}

func New(catalog *fieldchange.Catalog) *Service {
	return &Service{catalog: catalog, kernel: fieldvalue.New()}
}

func (service *Service) Preview(
	ctx context.Context,
	request Request,
) (Result, error) {
	if request.Contract != Contract || request.TableID == "" ||
		request.SchemaRevision == "" || request.Rows == nil {
		return Result{}, fmt.Errorf("invalid import preview request")
	}
	revisions, err := service.catalog.Revisions(ctx, request.TableID)
	if err != nil {
		return Result{}, err
	}
	if revisions.Schema != request.SchemaRevision {
		return Result{}, fmt.Errorf(
			"schema revision changed: expected %s, actual %s",
			request.SchemaRevision, revisions.Schema,
		)
	}
	fields, err := service.catalog.Fields(ctx, request.TableID, false)
	if err != nil {
		return Result{}, err
	}
	byReference := make(map[string]v2.FieldDefinition, len(fields)*2)
	for _, definition := range fields {
		byReference[definition.Identity.FieldID] = definition
		byReference[definition.Identity.PhysicalName] = definition
	}
	result := Result{Contract: Contract, Rows: make([]ResultRow, len(request.Rows))}
	for rowIndex, row := range request.Rows {
		mode := row.Mode
		if mode == "" {
			mode = fieldvalue.Insert
		}
		if mode != fieldvalue.Insert && mode != fieldvalue.Update {
			return Result{}, fmt.Errorf("invalid import preview row mode")
		}
		normalized := ResultRow{
			Values: map[string]any{}, Diagnostics: []Diagnostic{},
		}
		supplied := map[string]struct{}{}
		for reference, raw := range row.Values {
			definition, exists := byReference[reference]
			if !exists {
				normalized.Diagnostics = append(normalized.Diagnostics, Diagnostic{
					Field: reference, Code: "field.value.unknown",
					Message: "field is not part of the active product schema",
				})
				continue
			}
			supplied[definition.Identity.FieldID] = struct{}{}
			write, normalizeErr := normalizeImportedCell(
				ctx, service.kernel, definition, raw, mode,
			)
			if normalizeErr != nil {
				normalized.Diagnostics = append(normalized.Diagnostics, Diagnostic{
					Field: reference, Code: "field.value.invalid",
					Message: normalizeErr.Error(),
				})
				continue
			}
			if write.Write {
				normalized.Values[reference] = write.ProductValue
			}
		}
		if mode == fieldvalue.Insert {
			for _, definition := range fields {
				if !definition.Value.Required || definition.Value.Default.Enabled {
					continue
				}
				if _, exists := supplied[definition.Identity.FieldID]; exists {
					continue
				}
				normalized.Diagnostics = append(normalized.Diagnostics, Diagnostic{
					Field:   definition.Identity.FieldID,
					Code:    "field.value.required",
					Message: "required field is not supplied",
				})
			}
		}
		result.Rows[rowIndex] = normalized
	}
	return result, nil
}

func normalizeImportedCell(
	ctx context.Context,
	kernel *fieldvalue.Kernel,
	definition v2.FieldDefinition,
	raw any,
	modes ...fieldvalue.WriteMode,
) (fieldvalue.Result, error) {
	mode := fieldvalue.Insert
	if len(modes) > 0 {
		mode = modes[0]
	}
	input, err := kernel.NormalizeRawInput(ctx, definition, raw)
	if err != nil {
		return fieldvalue.Result{}, err
	}
	return kernel.NormalizeWrite(ctx, definition, mode, input)
}
