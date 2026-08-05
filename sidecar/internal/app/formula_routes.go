package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type formulaValidateRequest struct {
	Definition schema.TableDefinition `json:"definition"`
}

type formulaPreviewRequest struct {
	Definition      schema.TableDefinition `json:"definition"`
	Row             map[string]any         `json:"row"`
	ChangedFieldIDs []string               `json:"changedFieldIds"`
}

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
		var input formulaValidateRequest
		if err := decodeFormulaRequest(request.Request.Body, &input); err != nil {
			return writeFormulaError(request, err)
		}
		if err := validateFormulaDefinition(input.Definition); err != nil {
			return writeFormulaError(request, err)
		}
		plan, formulaErr := compiler.CompileTable(input.Definition)
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
		var input formulaPreviewRequest
		if err := decodeFormulaRequest(request.Request.Body, &input); err != nil {
			return writeFormulaError(request, err)
		}
		if input.Row == nil {
			return writeFormulaError(request, formulaRequestError("row is required"))
		}
		if err := validateFormulaDefinition(input.Definition); err != nil {
			return writeFormulaError(request, err)
		}
		plan, formulaErr := compiler.CompileTable(input.Definition)
		if formulaErr != nil {
			return writeFormulaError(request, formulaErr)
		}
		values, formulaErr := plan.Evaluate(
			request.Request.Context(), input.Row, input.ChangedFieldIDs,
		)
		if formulaErr != nil {
			return writeFormulaError(request, formulaErr)
		}
		return request.JSON(http.StatusOK, map[string]any{"values": values})
	})
}

func validateFormulaDefinition(definition schema.TableDefinition) *formula.Error {
	if err := schema.Validate(definition); err != nil {
		var productErr *schema.ProductError
		if errors.As(err, &productErr) {
			path := "definition"
			if productErr.Path != "" {
				path += "." + productErr.Path
			}
			return &formula.Error{
				ContractVersion: formula.ContractVersion,
				Code:            "formula.syntax",
				Path:            &path,
				Message:         "formula table definition is invalid",
				Details: map[string]any{
					"schemaCode":    productErr.Code,
					"schemaMessage": productErr.Message,
				},
			}
		}
		return formulaRequestError("formula table definition is invalid")
	}
	return nil
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
	var schemaErr *schema.ProductError
	if errors.As(err, &schemaErr) {
		return formulaProductError(schemaErr.Code, schemaErr.Path, schemaErr.Message, schemaErr.Details)
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
