package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/schemav2wire"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type formulaDraftValidateRequest struct {
	TableID       string `json:"tableId"`
	DisplaySource string `json:"displaySource"`
}

type formulaMetadata struct {
	FieldID         string   `json:"fieldId"`
	CanonicalSource string   `json:"canonicalSource"`
	ASTHash         string   `json:"astHash"`
	Dependencies    []string `json:"dependencies"`
}

func registerFormulaRoutes(
	r *router.Router[*core.RequestEvent],
	app core.App,
	compiler *formula.Compiler,
) {
	r.POST("/api/vibetable/v1/formulas/draft/validate", func(request *core.RequestEvent) error {
		var input formulaDraftValidateRequest
		if err := decodeFormulaRequest(request.Request.Body, &input); err != nil {
			return writeFormulaError(request, err)
		}
		if input.TableID == "" || input.DisplaySource == "" {
			return writeFormulaError(
				request, formulaRequestError("tableId and displaySource are required"),
			)
		}
		result, err := fieldchange.NewCatalog(request.App).InspectFormulaDraft(
			request.Request.Context(), input.TableID, input.DisplaySource,
		)
		if err != nil {
			return writeFormulaError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})

	r.POST("/api/vibetable/v1/formulas/validate", func(request *core.RequestEvent) error {
		var input schemav2wire.FormulaValidateRequest
		if err := decodeFormulaRequest(request.Request.Body, &input); err != nil {
			return writeFormulaError(request, err)
		}
		definition, err := formulaRequestTable(
			request.Request.Context(), app, input.TableId, input.Field,
		)
		if err != nil {
			return writeFormulaError(request, err)
		}
		plan, formulaErr := compiler.CompileExecutionTable(definition)
		if formulaErr != nil {
			return writeFormulaError(request, formulaErr)
		}
		metadata := make([]formulaMetadata, 0, len(plan.Formulas))
		for _, compiled := range plan.Formulas {
			metadata = append(metadata, formulaMetadata{
				FieldID: compiled.FieldID, CanonicalSource: compiled.CanonicalSource,
				ASTHash: compiled.ASTHash, Dependencies: compiled.Dependencies,
			})
		}
		return request.JSON(http.StatusOK, map[string]any{"formulas": metadata})
	})

	r.POST("/api/vibetable/v1/formulas/preview", func(request *core.RequestEvent) error {
		var input schemav2wire.FormulaPreviewRequest
		if err := decodeFormulaRequest(request.Request.Body, &input); err != nil {
			return writeFormulaError(request, err)
		}
		if input.Row == nil {
			return writeFormulaError(request, formulaRequestError("row is required"))
		}
		definition, err := formulaRequestTable(
			request.Request.Context(), app, input.TableId, input.Field,
		)
		if err != nil {
			return writeFormulaError(request, err)
		}
		plan, formulaErr := compiler.CompileExecutionTable(definition)
		if formulaErr != nil {
			return writeFormulaError(request, formulaErr)
		}
		row, err := decodeFormulaRow(input.Row)
		if err != nil {
			return writeFormulaError(request, err)
		}
		values, formulaErr := plan.Evaluate(
			request.Request.Context(), row, input.ChangedFieldIds,
		)
		if formulaErr != nil {
			return writeFormulaError(request, formulaErr)
		}
		return request.JSON(http.StatusOK, map[string]any{"values": values})
	})
}

func formulaRequestTable(
	ctx context.Context,
	app core.App,
	tableID string,
	wireField schemav2wire.FieldDefinition,
) (schemaexecution.Table, error) {
	if tableID == "" {
		return schemaexecution.Table{}, formulaRequestError("tableId is required")
	}
	raw, err := json.Marshal(wireField)
	if err != nil {
		return schemaexecution.Table{}, formulaRequestError("formula field is invalid")
	}
	var field v2.FieldDefinition
	if err := v2.StrictDecode(raw, &field); err != nil {
		return schemaexecution.Table{}, formulaFieldError(err)
	}
	if err := v2.Validate(field); err != nil {
		return schemaexecution.Table{}, formulaFieldError(err)
	}
	if field.LogicalType != v2.LogicalFormula || field.Formula == nil {
		return schemaexecution.Table{}, formulaProductError(
			"formula.type", "field.logicalType", "formula field is required", nil,
		)
	}
	table, err := schemaexecution.Describe(ctx, app, tableID)
	if err != nil {
		return schemaexecution.Table{}, err
	}
	replaced := false
	for index, existing := range table.Snapshot.Fields {
		if existing.Identity.FieldID == field.Identity.FieldID {
			if existing.Identity.PhysicalName != field.Identity.PhysicalName {
				return schemaexecution.Table{}, formulaProductError(
					"formula.dependency", "field.identity.physicalName",
					"formula field identity does not match the authoritative schema", nil,
				)
			}
			table.Snapshot.Fields[index] = field
			replaced = true
			break
		}
		if existing.Identity.PhysicalName == field.Identity.PhysicalName {
			return schemaexecution.Table{}, formulaProductError(
				"formula.dependency", "field.identity.physicalName",
				"formula field physical name is already in use", nil,
			)
		}
	}
	if !replaced {
		table.Snapshot.Fields = append(table.Snapshot.Fields, field)
	}
	return table, nil
}

func formulaFieldError(err error) *formula.Error {
	var productErr *v2.ProductError
	if errors.As(err, &productErr) {
		path := "field"
		if productErr.Path != "" {
			path += "." + productErr.Path
		}
		return formulaProductError(
			"formula.syntax", path, "formula field definition is invalid",
			map[string]any{
				"schemaCode": productErr.Code, "schemaMessage": productErr.Message,
			},
		)
	}
	return formulaRequestError("formula field definition is invalid")
}

func decodeFormulaRow(raw map[string]json.RawMessage) (map[string]any, error) {
	row := make(map[string]any, len(raw))
	for name, value := range raw {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, formulaRequestError("row is invalid")
		}
		row[name] = decoded
	}
	return row, nil
}

func decodeFormulaRequest(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxSchemaRequestBytes+1))
	if err != nil || len(raw) > maxSchemaRequestBytes {
		return formulaRequestError("formula request body is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return formulaRequestError("formula request body is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return formulaRequestError("formula request must contain one JSON value")
	}
	return nil
}

func formulaRequestError(message string) *formula.Error {
	return &formula.Error{
		ContractVersion: formula.ContractVersion,
		Code:            "formula.syntax",
		Message:         message,
		Details:         map[string]any{},
	}
}

func writeFormulaError(request *core.RequestEvent, err error) error {
	formulaErr := asFormulaError(err)
	status := http.StatusUnprocessableEntity
	if formulaErr.Code == "formula.syntax" && formulaErr.Path == nil {
		status = http.StatusBadRequest
	}
	if writeErr := request.JSON(status, formulaErr); writeErr != nil {
		return fmt.Errorf("write formula error response: %w", writeErr)
	}
	return nil
}

func asFormulaError(err error) *formula.Error {
	var formulaErr *formula.Error
	if errors.As(err, &formulaErr) {
		return formulaErr
	}
	var fieldErr *fieldchange.ProductError
	if errors.As(err, &fieldErr) {
		return formulaProductError(fieldErr.Code, fieldErr.Path, fieldErr.Message, fieldErr.Details)
	}
	return &formula.Error{
		ContractVersion: formula.ContractVersion,
		Code:            "formula.runtime",
		Message:         "formula operation failed",
		Details:         map[string]any{},
	}
}

func formulaProductError(code, path, message string, details map[string]any) *formula.Error {
	var formulaPath *string
	if path != "" {
		formulaPath = &path
	}
	if details == nil {
		details = map[string]any{}
	}
	return &formula.Error{
		ContractVersion: formula.ContractVersion,
		Code:            code, Path: formulaPath, Message: message, Details: details,
	}
}
