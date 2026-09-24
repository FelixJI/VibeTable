package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const pasteTTL = 300 * time.Second
const maxPasteCells = 10_000

var pasteTokenPattern = regexp.MustCompile(`^pst1\.[A-Za-z0-9_-]{40,48}$`)
var pasteMutationPath = regexp.MustCompile(`^operations\[(\d+)\]\.rawValues\.([^.]+)`)

type pasteCell struct {
	RowIndex    int     `json:"rowIndex"`
	ColumnIndex int     `json:"columnIndex"`
	Column      *string `json:"column"`
	RawValue    string  `json:"rawValue"`
	ParsedValue any     `json:"parsedValue"`
}

type pasteStartCell struct {
	RowKey any    `json:"rowKey"`
	Column string `json:"column"`
}

type pastePreviewParams struct {
	Collection     string          `json:"collection"`
	SchemaRevision string          `json:"schemaRevision"`
	Selection      json.RawMessage `json:"selection"`
	StartCell      pasteStartCell  `json:"startCell"`
	Cells          [][]pasteCell   `json:"cells"`
}

type pasteApplyParams struct {
	Collection     string `json:"collection"`
	Token          string `json:"token"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type pasteDiagnostic struct {
	RowIndex    int    `json:"rowIndex"`
	ColumnIndex int    `json:"columnIndex"`
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

type pasteRow struct {
	Kind                string                    `json:"kind"`
	TargetRowKey        any                       `json:"targetRowKey"`
	ExpectedDateUpdated *string                   `json:"expectedDateUpdated"`
	Changes             map[string]map[string]any `json:"changes"`
	Diagnostics         []pasteDiagnostic         `json:"diagnostics"`
}

type pastePlan struct {
	Collection     string            `json:"collection"`
	SchemaRevision string            `json:"schemaRevision"`
	CapabilityHash string            `json:"capabilityHash"`
	Summary        map[string]int    `json:"summary"`
	Rows           []pasteRow        `json:"rows"`
	Diagnostics    []pasteDiagnostic `json:"diagnostics"`
	Token          struct {
		Token     string  `json:"token"`
		ExpiresAt float64 `json:"expiresAt"`
		Consumed  bool    `json:"consumed"`
	} `json:"token"`
	Overflow bool `json:"overflow"`
}

type pasteConflict struct {
	RowKey              any            `json:"rowKey"`
	CurrentValue        map[string]any `json:"currentValue"`
	ExpectedDateUpdated *string        `json:"expectedDateUpdated"`
}

type pasteApplyResult struct {
	Collection     string          `json:"collection"`
	Outcome        string          `json:"outcome"`
	CreatedRowKeys []any           `json:"createdRowKeys"`
	UpdatedRowKeys []any           `json:"updatedRowKeys"`
	SkippedRowKeys []any           `json:"skippedRowKeys"`
	Conflicts      []pasteConflict `json:"conflicts"`
	RequestID      string          `json:"requestId"`
}

type pasteStoredPlan struct {
	collection        string
	schemaRevision    string
	workspaceID       string
	userID            string
	expiresAt         time.Time
	rows              []pasteRow
	rawRows           []map[string]any
	guards            map[string]string
	consumed          bool
	idempotencyKey    string
	mutationRequestID string
}

type pasteSchemaPort func(context.Context, string) (schemaexecution.Table, error)
type pasteRowsPort interface {
	ReadRows(context.Context, string, []string) ([]map[string]any, error)
}

type pasteOwner struct {
	schema      pasteSchemaPort
	rows        pasteRowsPort
	values      *importvalue.Service
	kernel      mutationKernel
	gate        businessWriteGate
	workspaceID string
	now         func() time.Time
	mu          sync.Mutex
	plans       map[string]*pasteStoredPlan
}

func newPasteOwner(schema pasteSchemaPort, rows pasteRowsPort, values *importvalue.Service, kernel mutationKernel, gate businessWriteGate, workspaceID string) *pasteOwner {
	return &pasteOwner{schema: schema, rows: rows, values: values, kernel: kernel, gate: gate, workspaceID: workspaceID, now: time.Now, plans: map[string]*pasteStoredPlan{}}
}

func pasteError(code, message string, details map[string]any) error {
	return &productrpc.PasteError{Code: code, Message: message, Details: details}
}

func decodePasteObject(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if !json.Valid(raw) || json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, errors.New("paste parameters must be an object")
	}
	names := map[string]bool{}
	for _, name := range allowed {
		names[name] = true
	}
	for name := range value {
		if !names[name] {
			return nil, errors.New("unknown paste parameter")
		}
	}
	return value, nil
}

func pasteField(value map[string]json.RawMessage, camel, snake string, required bool) (json.RawMessage, error) {
	raw, found := value[camel]
	if alias, has := value[snake]; has && snake != camel {
		if found {
			return nil, errors.New("duplicate paste field alias")
		}
		raw, found = alias, true
	}
	if required && !found {
		return nil, errors.New("missing paste parameter")
	}
	return raw, nil
}

func pasteString(raw json.RawMessage, maximum int) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("paste text is required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	if value == "" || (maximum > 0 && utf8.RuneCountInString(value) > maximum) {
		return "", errors.New("invalid paste text length")
	}
	return value, nil
}

func decodePastePreview(raw json.RawMessage) (pastePreviewParams, error) {
	var p pastePreviewParams
	object, err := decodePasteObject(raw, "collection", "schemaRevision", "schema_revision", "selection", "startCell", "start_cell", "cells")
	if err != nil {
		return p, err
	}
	collection, err := pasteField(object, "collection", "collection", true)
	if err != nil {
		return p, err
	}
	p.Collection, err = pasteString(collection, 128)
	if err != nil {
		return p, err
	}
	schema, err := pasteField(object, "schemaRevision", "schema_revision", true)
	if err != nil {
		return p, err
	}
	p.SchemaRevision, err = pasteString(schema, 0)
	if err != nil {
		return p, err
	}
	p.Selection, err = pasteField(object, "selection", "selection", true)
	if err != nil {
		return p, err
	}
	start, err := pasteField(object, "startCell", "start_cell", true)
	if err != nil {
		return p, err
	}
	startObject, err := decodePasteObject(start, "rowKey", "row_key", "column")
	if err != nil {
		return p, err
	}
	column, err := pasteField(startObject, "column", "column", true)
	if err != nil {
		return p, err
	}
	p.StartCell.Column, err = pasteString(column, 128)
	if err != nil {
		return p, err
	}
	rowKey, err := pasteField(startObject, "rowKey", "row_key", false)
	if err != nil {
		return p, err
	}
	if len(rowKey) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(rowKey)))
		decoder.UseNumber()
		if err := decoder.Decode(&p.StartCell.RowKey); err != nil {
			return p, err
		}
		switch key := p.StartCell.RowKey.(type) {
		case nil, string:
		case json.Number:
			integer, err := pasteInteger(json.RawMessage(key.String()))
			if err != nil {
				return p, errors.New("paste row key must be an integer")
			}
			p.StartCell.RowKey = integer
		default:
			return p, errors.New("paste row key must be text or integer")
		}
	}
	cells, err := pasteField(object, "cells", "cells", true)
	if err != nil {
		return p, err
	}
	var cellRows [][]json.RawMessage
	if err := json.Unmarshal(cells, &cellRows); err != nil || len(cellRows) == 0 || len(cellRows) > maxPasteCells {
		return p, errors.New("invalid paste rows")
	}
	p.Cells = make([][]pasteCell, len(cellRows))
	for rowIndex, rawRow := range cellRows {
		if rawRow == nil {
			return p, errors.New("paste row must be an array")
		}
		p.Cells[rowIndex] = make([]pasteCell, len(rawRow))
		for columnIndex, rawCell := range rawRow {
			cellObject, err := decodePasteObject(rawCell, "rowIndex", "row_index", "columnIndex", "column_index", "column", "rawValue", "raw_value", "parsedValue", "parsed_value")
			if err != nil {
				return p, err
			}
			rowIndexRaw, err := pasteField(cellObject, "rowIndex", "row_index", true)
			if err != nil {
				return p, err
			}
			columnIndexRaw, err := pasteField(cellObject, "columnIndex", "column_index", true)
			if err != nil {
				return p, err
			}
			rawValue, err := pasteField(cellObject, "rawValue", "raw_value", true)
			if err != nil {
				return p, err
			}
			cell := pasteCell{}
			cell.RowIndex, err = pasteInteger(rowIndexRaw)
			if err != nil || cell.RowIndex < 0 {
				return p, errors.New("invalid paste row index")
			}
			cell.ColumnIndex, err = pasteInteger(columnIndexRaw)
			if err != nil || cell.ColumnIndex < 0 {
				return p, errors.New("invalid paste column index")
			}
			if string(rawValue) == "null" || json.Unmarshal(rawValue, &cell.RawValue) != nil {
				return p, errors.New("invalid paste raw value")
			}
			if rawColumn, found := cellObject["column"]; found && string(rawColumn) != "null" {
				var value string
				if err := json.Unmarshal(rawColumn, &value); err != nil || utf8.RuneCountInString(value) > 128 {
					return p, errors.New("invalid paste column")
				}
				cell.Column = &value
			}
			if parsed, err := pasteField(cellObject, "parsedValue", "parsed_value", false); err != nil {
				return p, err
			} else if len(parsed) > 0 {
				decoder := json.NewDecoder(strings.NewReader(string(parsed)))
				decoder.UseNumber()
				if err := decoder.Decode(&cell.ParsedValue); err != nil {
					return p, err
				}
			}
			p.Cells[rowIndex][columnIndex] = cell
		}
	}
	return p, nil
}

func pasteInteger(raw json.RawMessage) (int, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	switch number := value.(type) {
	case string:
		return strconv.Atoi(number)
	case json.Number:
		if parsed, err := strconv.Atoi(number.String()); err == nil {
			return parsed, nil
		} else if !strings.ContainsAny(number.String(), ".eE") {
			return 0, err
		}
		parsed, err := number.Float64()
		upperBound := math.Ldexp(1, strconv.IntSize-1)
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || math.Trunc(parsed) != parsed || parsed >= upperBound || parsed < -upperBound {
			return 0, errors.New("paste index must be an integer")
		}
		return int(parsed), nil
	default:
		return 0, errors.New("paste index must be an integer")
	}
}

func decodePasteApply(raw json.RawMessage) (pasteApplyParams, error) {
	var p pasteApplyParams
	object, err := decodePasteObject(raw, "collection", "token", "idempotencyKey", "idempotency_key")
	if err != nil {
		return p, err
	}
	collection, err := pasteField(object, "collection", "collection", true)
	if err != nil {
		return p, err
	}
	p.Collection, err = pasteString(collection, 128)
	if err != nil {
		return p, err
	}
	token, err := pasteField(object, "token", "token", true)
	if err != nil {
		return p, err
	}
	p.Token, err = pasteString(token, 2048)
	if err != nil {
		return p, err
	}
	key, err := pasteField(object, "idempotencyKey", "idempotency_key", true)
	if err != nil {
		return p, err
	}
	p.IdempotencyKey, err = pasteString(key, 128)
	if err != nil {
		return p, err
	}
	return p, nil
}

func pastePreviewRegistration(owner *pasteOwner) productrpc.Registration {
	return productrpc.Registration{Method: "table.previewPaste", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodePastePreview(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			p, err := decodePastePreview(raw)
			if err != nil {
				return nil, err
			}
			return owner.preview(ctx, p)
		},
	}
}

func pasteApplyRegistration(owner *pasteOwner) productrpc.Registration {
	return productrpc.Registration{Method: "table.applyPaste", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := decodePasteApply(raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			p, err := decodePasteApply(raw)
			if err != nil {
				return nil, err
			}
			return owner.apply(ctx, p)
		},
	}
}

func (owner *pasteOwner) describe(ctx context.Context, collection string) (schemaexecution.Table, error) {
	table, err := owner.schema(ctx, collection)
	var schemaErr *schemaerror.ProductError
	if errors.Is(err, schemaexecution.ErrTableNotFound) || (errors.As(err, &schemaErr) && schemaErr.Code == "schema.table.not_found") {
		return table, pasteError("schema_unknown", fmt.Sprintf("collection %q is not in the product schema", collection), nil)
	}
	if err != nil {
		return table, publicMutationProductError(err)
	}
	if table.Snapshot.TableID != collection {
		return table, pasteError("schema_mismatch", "product schema identity mismatch", nil)
	}
	return table, nil
}

func pasteEditable(table schemaexecution.Table) ([]string, []string, map[string]string) {
	editable := []string{}
	readonly := []string{"id"}
	fieldIDs := map[string]string{}
	for _, field := range table.Snapshot.Fields {
		name := field.Identity.PhysicalName
		fieldIDs[field.Identity.FieldID] = name
		if table.Kind == "base" && field.Lifecycle.State == v2.LifecycleActive &&
			field.LogicalType != v2.LogicalFormula && field.LogicalType != v2.LogicalLookup && field.LogicalType != v2.LogicalAutoDate {
			editable = append(editable, name)
		} else {
			readonly = append(readonly, name)
		}
	}
	return editable, readonly, fieldIDs
}

func pasteSelectionKeys(raw json.RawMessage) ([]any, error) {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return []any{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return []any{}, nil
	}
	value := object["rowKeys"]
	if len(value) == 0 {
		value = object["row_keys"]
	}
	if len(value) == 0 {
		return []any{}, nil
	}
	var keys []any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if err := decoder.Decode(&keys); err != nil {
		return []any{}, nil
	}
	if keys == nil {
		return []any{}, nil
	}
	return keys, nil
}

func pasteKey(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func pasteToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "pst1." + base64.RawURLEncoding.EncodeToString(raw), nil
}

func pasteSummary(rows []pasteRow) map[string]int {
	summary := map[string]int{"updateRows": 0, "insertRows": 0, "skipRows": 0, "errorCount": 0, "warningCount": 0}
	for _, row := range rows {
		switch {
		case row.Kind == "insert":
			summary["insertRows"]++
		case row.Kind == "update" && len(row.Changes) > 0:
			summary["updateRows"]++
		default:
			summary["skipRows"]++
		}
		for _, diagnostic := range row.Diagnostics {
			if diagnostic.Severity == "error" {
				summary["errorCount"]++
			} else if diagnostic.Severity == "warning" {
				summary["warningCount"]++
			}
		}
	}
	return summary
}
