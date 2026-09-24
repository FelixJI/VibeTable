package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func (owner *pasteOwner) preview(ctx context.Context, p pastePreviewParams) (pastePlan, error) {
	var result pastePlan
	requestID, ok := productrpc.OperationID(ctx)
	if !ok {
		return result, errors.New("verified Product RPC operation is required")
	}
	table, err := owner.describe(ctx, p.Collection)
	if err != nil {
		return result, err
	}
	revision := table.Snapshot.SchemaRevision
	if p.SchemaRevision != revision {
		return result, pasteError("schema_mismatch", "schema changed since the grid was rendered", map[string]any{"currentSchemaRevision": revision, "expectedSchemaRevision": p.SchemaRevision})
	}
	count := 0
	for _, cells := range p.Cells {
		count += len(cells)
	}
	if count > maxPasteCells {
		return result, pasteError("paste_overflow", fmt.Sprintf("clipboard exceeds the %d cell limit; use file import", maxPasteCells), map[string]any{"maxCells": maxPasteCells, "cellCount": count})
	}
	editable, readonly, fieldIDs := pasteEditable(table)
	anchorColumn := -1
	for index, name := range editable {
		if name == p.StartCell.Column {
			anchorColumn = index
			break
		}
	}
	if anchorColumn < 0 {
		return result, pasteError("anchor_column_readonly", fmt.Sprintf("paste anchor column %q is not editable", p.StartCell.Column), nil)
	}
	keys, err := pasteSelectionKeys(p.Selection)
	if err != nil {
		return result, err
	}
	start := len(keys)
	if p.StartCell.RowKey != nil {
		start = -1
		for index, key := range keys {
			if pasteKey(key) == pasteKey(p.StartCell.RowKey) {
				start = index
				break
			}
		}
		if start < 0 {
			return result, pasteError("anchor_not_selected", "paste anchor row is not part of the current selection", nil)
		}
	}
	rows := make([]pasteRow, 0, len(p.Cells))
	rawRows := make([]map[string]any, 0, len(p.Cells))
	coordinates := make([]map[string][2]int, 0, len(p.Cells))
	guards := map[string]string{}
	for offset, cells := range p.Cells {
		index := start + offset
		row := pasteRow{Kind: "skip", Changes: map[string]map[string]any{}, Diagnostics: []pasteDiagnostic{}}
		current := map[string]any{}
		if index < len(keys) {
			row.Kind = "update"
			row.TargetRowKey = keys[index]
			records, readErr := owner.rows.ReadRows(ctx, p.Collection, []string{pasteKey(row.TargetRowKey)})
			if readErr != nil {
				return result, publicMutationProductError(readErr)
			}
			if len(records) != 1 || pasteKey(records[0]["id"]) != pasteKey(row.TargetRowKey) {
				return result, pasteError("paste_row_not_found", "selected paste row no longer exists", nil)
			}
			current = records[0]
			if guard, valid := current["__vibetableDigest"].(string); valid && guard != "" {
				row.ExpectedDateUpdated = &guard
				guards[pasteKey(row.TargetRowKey)] = guard
			}
		} else if p.StartCell.RowKey == nil && index == len(keys) {
			row.Kind = "insert"
		} else {
			row.Diagnostics = append(row.Diagnostics, pasteDiagnostic{RowIndex: offset, ColumnIndex: 0, Severity: "error", Code: "anchor_out_of_range", Message: "paste anchor is past the last selected row and appends are not supported"})
			rows = append(rows, row)
			rawRows = append(rawRows, map[string]any{})
			coordinates = append(coordinates, map[string][2]int{})
			continue
		}
		coords := map[string][2]int{}
		for _, cell := range cells {
			column := ""
			columnIndex := anchorColumn + cell.ColumnIndex
			if cell.Column != nil {
				column = *cell.Column
				if cell.ColumnIndex != 0 {
					row.Diagnostics = append(row.Diagnostics, pasteDiagnostic{RowIndex: cell.RowIndex, ColumnIndex: cell.ColumnIndex, Severity: "warning", Code: "column_override_ignored", Message: "cell column override ignored; columns are resolved from the anchor"})
				}
			} else if columnIndex >= 0 && columnIndex < len(editable) {
				column = editable[columnIndex]
			} else {
				diagnostic := pasteDiagnostic{RowIndex: cell.RowIndex, ColumnIndex: cell.ColumnIndex, Severity: "error"}
				if columnIndex < len(editable)+len(readonly) {
					diagnostic.Code = "column_readonly"
					diagnostic.Message = "target column is read-only"
				} else {
					diagnostic.Code = "column_out_of_range"
					diagnostic.Message = "target column is outside the editable range"
				}
				row.Diagnostics = append(row.Diagnostics, diagnostic)
				continue
			}
			before := current[column]
			after := any(cell.RawValue)
			if reflect.DeepEqual(before, after) {
				continue
			}
			row.Changes[column] = map[string]any{"before": before, "after": after}
			coords[column] = [2]int{cell.RowIndex, cell.ColumnIndex}
		}
		raw := map[string]any{}
		for name, change := range row.Changes {
			raw[name] = change["after"]
		}
		rows = append(rows, row)
		rawRows = append(rawRows, raw)
		coordinates = append(coordinates, coords)
	}
	writable := []int{}
	importRows := []importvalue.Row{}
	for index, row := range rows {
		if row.Kind != "insert" && row.Kind != "update" {
			continue
		}
		mode := fieldvalue.Insert
		if row.Kind == "update" {
			mode = fieldvalue.Update
		}
		writable = append(writable, index)
		importRows = append(importRows, importvalue.Row{Values: rawRows[index], Mode: mode})
	}
	if len(writable) > 0 {
		preview, previewErr := owner.values.Preview(ctx, importvalue.Request{Contract: importvalue.Contract, TableID: p.Collection, SchemaRevision: revision, Rows: importRows})
		if previewErr != nil {
			return result, publicMutationProductError(previewErr)
		}
		if len(preview.Rows) != len(writable) {
			return result, pasteError("paste_preview_invalid", "invalid authoritative paste preview", nil)
		}
		for pos, normalized := range preview.Rows {
			rowIndex := writable[pos]
			row := &rows[rowIndex]
			for field, value := range normalized.Values {
				if change, has := row.Changes[field]; has {
					change["after"] = value
					if row.Kind == "update" {
						same, compareErr := pasteJSONEquivalent(change["before"], value)
						if compareErr != nil {
							return result, pasteError("paste_preview_invalid", "invalid authoritative paste preview", nil)
						}
						if same {
							delete(row.Changes, field)
							delete(rawRows[rowIndex], field)
						}
					}
				}
			}
			for _, diagnostic := range normalized.Diagnostics {
				field := diagnostic.Field
				if name, exists := fieldIDs[field]; exists {
					field = name
				}
				coordinate, has := coordinates[rowIndex][field]
				if !has {
					coordinate = [2]int{rowIndex, 0}
				}
				row.Diagnostics = append(row.Diagnostics, pasteDiagnostic{RowIndex: coordinate[0], ColumnIndex: coordinate[1], Severity: "error", Code: diagnostic.Code, Message: diagnostic.Message})
			}
		}
	}
	operations := pasteOperations(rows, rawRows, guards)
	operationRows := []int{}
	for index, row := range rows {
		if row.Kind != "skip" && (row.Kind != "update" || len(row.Changes) != 0) {
			operationRows = append(operationRows, index)
		}
	}
	if len(operations) > 0 && !pasteHasErrors(rows) {
		_, previewErr := owner.kernel.Preview(ctx, pasteMutationRequest(requestID, requestID, p.Collection, revision, operations))
		if previewErr != nil {
			var productErr *mutation.ProductError
			if errors.As(previewErr, &productErr) && productErr.Path != nil {
				matches := pasteMutationPath.FindStringSubmatch(*productErr.Path)
				if len(matches) == 3 {
					index, _ := strconv.Atoi(matches[1])
					if index >= 0 && index < len(operationRows) {
						index = operationRows[index]
						coordinate, has := coordinates[index][matches[2]]
						if !has {
							coordinate = [2]int{index, 0}
						}
						rows[index].Diagnostics = append(rows[index].Diagnostics, pasteDiagnostic{RowIndex: coordinate[0], ColumnIndex: coordinate[1], Severity: "error", Code: productErr.Code, Message: productErr.Message})
						previewErr = nil
					}
				}
			}
			if previewErr != nil {
				return result, publicMutationProductError(previewErr)
			}
		}
	}
	token, err := pasteToken()
	if err != nil {
		return result, err
	}
	expires := owner.now().Add(pasteTTL)
	owner.mu.Lock()
	owner.plans[token] = &pasteStoredPlan{collection: p.Collection, schemaRevision: revision, workspaceID: owner.workspaceID, userID: "local-user", expiresAt: expires, rows: rows, rawRows: rawRows, guards: guards}
	owner.mu.Unlock()
	result = pastePlan{Collection: p.Collection, SchemaRevision: revision, CapabilityHash: revision, Summary: pasteSummary(rows), Rows: rows, Diagnostics: []pasteDiagnostic{}, Overflow: false}
	result.Token.Token = token
	result.Token.ExpiresAt = float64(expires.UnixNano()) / 1e9
	return result, nil
}

func pasteHasErrors(rows []pasteRow) bool {
	for _, row := range rows {
		for _, diagnostic := range row.Diagnostics {
			if diagnostic.Severity == "error" {
				return true
			}
		}
	}
	return false
}
func pasteOperations(rows []pasteRow, rawRows []map[string]any, guards map[string]string) []mutation.Operation {
	operations := []mutation.Operation{}
	for index, row := range rows {
		if row.Kind == "skip" || (row.Kind == "update" && len(row.Changes) == 0) {
			continue
		}
		op := mutation.Operation{Values: map[string]any{}, RawValues: rawRows[index]}
		if row.Kind == "insert" {
			op.Kind = mutation.OperationInsert
		} else {
			op.Kind = mutation.OperationUpdate
			key := pasteKey(row.TargetRowKey)
			op.RecordID = &key
			if guard := guards[key]; guard != "" {
				if len(guard) > 4 && guard[:4] == "row_" {
					op.ExpectedRevision = &guard
				} else if len(guard) > 7 && guard[:7] == "sha256:" {
					op.ExpectedDigest = &guard
				}
			}
		}
		operations = append(operations, op)
	}
	return operations
}
func pasteMutationRequest(requestID, idempotencyKey, tableID, revision string, operations []mutation.Operation) mutation.Request {
	return mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: requestID, IdempotencyKey: idempotencyKey, TableID: tableID, SchemaRevision: revision, Operations: operations, Actor: mutation.Actor{Type: "user", ID: "local-user"}}
}

func (owner *pasteOwner) apply(ctx context.Context, p pasteApplyParams) (pasteApplyResult, error) {
	result := pasteApplyResult{Collection: p.Collection, CreatedRowKeys: []any{}, UpdatedRowKeys: []any{}, SkippedRowKeys: []any{}, Conflicts: []pasteConflict{}, RequestID: p.IdempotencyKey}
	requestID, ok := productrpc.OperationID(ctx)
	if !ok {
		return result, errors.New("verified Product RPC operation is required")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	table, err := owner.describe(ctx, p.Collection)
	if err != nil {
		return result, err
	}
	if !pasteTokenPattern.MatchString(p.Token) {
		return result, pasteError("paste_token_invalid", "invalid paste token", nil)
	}
	stored := owner.plans[p.Token]
	if stored == nil {
		return result, pasteError("paste_token_unknown", "paste token not found", nil)
	}
	if !owner.now().Before(stored.expiresAt) {
		return result, pasteError("paste_token_expired", "paste token expired", nil)
	}
	if stored.consumed {
		return result, pasteError("paste_token_consumed", "paste token already used", nil)
	}
	if stored.collection != p.Collection || stored.workspaceID != owner.workspaceID || stored.userID != "local-user" {
		return result, pasteError("paste_token_invalid", "paste token does not match collection", nil)
	}
	revision := table.Snapshot.SchemaRevision
	if stored.schemaRevision != revision {
		return result, pasteError("schema_mismatch", "schema changed since the plan was prepared", map[string]any{"currentSchemaRevision": revision, "expectedSchemaRevision": stored.schemaRevision})
	}
	if stored.idempotencyKey == "" {
		stored.idempotencyKey = p.IdempotencyKey
	} else if stored.idempotencyKey != p.IdempotencyKey {
		return result, pasteError("paste_idempotency_mismatch", "paste token is bound to a different idempotency key", nil)
	}
	if pasteHasErrors(stored.rows) {
		return result, pasteError("paste_plan_invalid", "paste plan contains validation errors", nil)
	}
	editable, _, _ := pasteEditable(table)
	allowed := map[string]bool{}
	for _, field := range editable {
		allowed[field] = true
	}
	for _, row := range stored.rows {
		for field := range row.Changes {
			if !allowed[field] {
				return result, pasteError("schema_mismatch", "field policy changed since the plan was prepared", nil)
			}
		}
	}
	for _, row := range stored.rows {
		if row.Kind == "skip" || (row.Kind == "update" && len(row.Changes) == 0) {
			if row.TargetRowKey != nil {
				result.SkippedRowKeys = append(result.SkippedRowKeys, row.TargetRowKey)
			}
		}
	}
	operations := pasteOperations(stored.rows, stored.rawRows, stored.guards)
	if len(operations) == 0 {
		result.Outcome = "committed"
		stored.consumed = true
		return result, nil
	}
	var receipt mutation.Receipt
	invoked := false
	err = runBusinessWrite(ctx, []businessWriteGate{owner.gate}, "mutation.apply", p.IdempotencyKey, func(writeCtx context.Context) error {
		invoked = true
		var applyErr error
		receipt, applyErr = owner.kernel.Apply(writeCtx, pasteMutationRequest(requestID, p.IdempotencyKey, p.Collection, revision, operations))
		return applyErr
	})
	if err != nil {
		if !invoked {
			return result, publicMutationProductError(err)
		}
		var productErr *mutation.ProductError
		if errors.As(err, &productErr) {
			switch productErr.Code {
			case "mutation.digest_conflict", "mutation.revision_conflict", "mutation.schema_revision_conflict", "mutation.record.not_found":
				var key any = ""
				var guard *string
				updates := 0
				for _, row := range stored.rows {
					if row.Kind == "update" {
						updates++
						key = row.TargetRowKey
						guard = row.ExpectedDateUpdated
					}
				}
				if updates != 1 {
					key = ""
					guard = nil
				}
				details := productErr.Details
				if details == nil {
					details = map[string]any{}
				}
				result.Outcome = "conflict"
				result.Conflicts = append(result.Conflicts, pasteConflict{RowKey: key, CurrentValue: details, ExpectedDateUpdated: guard})
				return result, nil
			}
			return result, pasteError(productErr.Code, "PocketBase rejected the mutation", map[string]any{"details": productErr.Details})
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Outcome = "pending"
			return result, nil
		}
		result.Outcome = "pending"
		return result, nil
	}
	switch receipt.Status {
	case mutation.StatusPending:
		result.Outcome = "pending"
		return result, nil
	case mutation.StatusApplied, mutation.StatusReplayed:
	default:
		return result, pasteError("mutation_invalid_response", "PocketBase returned an invalid mutation receipt", nil)
	}
	if receipt.AffectedRows == nil {
		return result, pasteError("mutation_invalid_response", "PocketBase returned an invalid mutation receipt", nil)
	}
	for _, affected := range receipt.AffectedRows {
		if affected.RecordID == "" {
			return result, pasteError("mutation_invalid_response", "PocketBase returned an invalid mutation receipt", nil)
		}
		switch affected.Operation {
		case mutation.OperationInsert:
			result.CreatedRowKeys = append(result.CreatedRowKeys, affected.RecordID)
		case mutation.OperationUpdate:
			result.UpdatedRowKeys = append(result.UpdatedRowKeys, affected.RecordID)
		}
	}
	result.Outcome = "committed"
	stored.consumed = true
	return result, nil
}

func pasteJSONEquivalent(left, right any) (bool, error) {
	leftRaw, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightRaw, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(leftRaw))
	leftDecoder.UseNumber()
	if err := leftDecoder.Decode(&leftValue); err != nil {
		return false, err
	}
	rightDecoder := json.NewDecoder(bytes.NewReader(rightRaw))
	rightDecoder.UseNumber()
	if err := rightDecoder.Decode(&rightValue); err != nil {
		return false, err
	}
	return pasteCanonicalEqual(leftValue, rightValue), nil
}

func pasteCanonicalEqual(left, right any) bool {
	switch left := left.(type) {
	case nil:
		return right == nil
	case bool:
		other, ok := right.(bool)
		return ok && left == other
	case string:
		other, ok := right.(string)
		return ok && left == other
	case json.Number:
		other, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftRat, leftOK := new(big.Rat).SetString(left.String())
		rightRat, rightOK := new(big.Rat).SetString(other.String())
		return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
	case []any:
		other, ok := right.([]any)
		if !ok || len(left) != len(other) {
			return false
		}
		for index, value := range left {
			if !pasteCanonicalEqual(value, other[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := right.(map[string]any)
		if !ok || len(left) != len(other) {
			return false
		}
		for key, value := range left {
			otherValue, exists := other[key]
			if !exists || !pasteCanonicalEqual(value, otherValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
