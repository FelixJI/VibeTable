package query

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"

	"github.com/vibetable/vibetable/sidecar/internal/autodateobs"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
)

type SchemaSource interface {
	DescribeQueryTable(
		ctx context.Context,
		app core.App,
		tableID string,
	) (TableDescriptor, error)
}

type QueryPort interface {
	QueryPage(ctx context.Context, tableID string, input TableQuery) (Page, error)
	ReadRows(ctx context.Context, tableID string, rowIDs []string) ([]map[string]any, error)
	Aggregate(ctx context.Context, tableID string, input AggregateQuery) (AggregateResult, error)
	ValidateSnapshot(
		ctx context.Context,
		snapshot QuerySnapshot,
		currentQuery *TableQuery,
	) (SnapshotValidation, error)
}

type Port struct {
	app         core.App
	source      SchemaSource
	snapshotKey []byte
}

func NewPort(app core.App, source SchemaSource, snapshotKey []byte) *Port {
	return &Port{
		app: app, source: source,
		snapshotKey: append([]byte(nil), snapshotKey...),
	}
}

func (port *Port) QueryPage(
	ctx context.Context,
	tableID string,
	input TableQuery,
) (Page, error) {
	if err := port.validateConfigured(); err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	var result Page
	err := port.app.RunInTransaction(func(txApp core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		descriptor, err := port.describe(ctx, txApp, tableID)
		if err != nil {
			return err
		}
		normalized, err := Normalize(input)
		if err != nil {
			return err
		}
		plan, err := Compile(descriptor, normalized)
		if err != nil {
			return err
		}
		rows, err := port.queryRows(ctx, txApp, plan.SQL, plan.Params, descriptor)
		if err != nil {
			return operationError(err)
		}
		var filteredRows, totalRows int64
		if err := txApp.DB().NewQuery(plan.CountSQL).
			WithContext(ctx).Bind(dbx.Params(plan.Params)).Row(&filteredRows); err != nil {
			return operationError(err)
		}
		if err := txApp.DB().NewQuery(plan.TotalSQL).
			WithContext(ctx).Bind(dbx.Params(plan.Params)).Row(&totalRows); err != nil {
			return operationError(err)
		}
		snapshot, err := port.buildSnapshot(descriptor, normalized)
		if err != nil {
			return err
		}
		result = Page{
			Rows: rows, Offset: normalized.Offset, Limit: normalized.Limit,
			FilteredRows: filteredRows, TotalRows: totalRows,
			Snapshot: snapshot,
		}
		return nil
	})
	if err != nil {
		return Page{}, operationError(err)
	}
	return result, nil
}

func (port *Port) ReadRows(
	ctx context.Context,
	tableID string,
	rowIDs []string,
) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rowIDs) == 0 {
		return []map[string]any{}, nil
	}
	if len(rowIDs) > maxInValues {
		return nil, productError("query.rows.limit", "rowIds", "at most 200 row ids are allowed", nil)
	}
	descriptor, err := port.describe(ctx, port.app, tableID)
	if err != nil {
		return nil, err
	}
	primaryKey := descriptor.PrimaryKey
	values := make([]any, len(rowIDs))
	for index := range rowIDs {
		values[index] = rowIDs[index]
	}
	page, err := port.QueryPage(ctx, tableID, TableQuery{
		Filters: []FilterExpression{{
			Field: primaryKey, Operator: OperatorIn, Value: values, Logic: LogicAnd,
		}},
		Limit: len(values),
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]any, len(page.Rows))
	for _, row := range page.Rows {
		byID[fmt.Sprint(row[primaryKey])] = row
	}
	result := make([]map[string]any, 0, len(rowIDs))
	for _, id := range rowIDs {
		if row, ok := byID[id]; ok {
			result = append(result, row)
		}
	}
	return result, nil
}

func (port *Port) Aggregate(
	ctx context.Context,
	tableID string,
	input AggregateQuery,
) (AggregateResult, error) {
	if err := port.validateConfigured(); err != nil {
		return AggregateResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AggregateResult{}, err
	}
	var result AggregateResult
	err := port.app.RunInTransaction(func(txApp core.App) error {
		descriptor, err := port.describe(ctx, txApp, tableID)
		if err != nil {
			return err
		}
		sql, params, err := CompileAggregate(descriptor, input)
		if err != nil {
			return err
		}
		groupFields := make(map[string]FieldDescriptor, len(input.GroupBy))
		resolver := &compiler{descriptor: descriptor}
		for index, group := range input.GroupBy {
			resolved, err := resolver.resolve(
				group,
				fmt.Sprintf("groupBy[%d]", index),
			)
			if err != nil {
				return err
			}
			groupFields[group] = resolved.descriptor
		}
		rows, err := queryDynamicRows(
			txApp.DB().NewQuery(sql).WithContext(ctx).Bind(dbx.Params(params)))
		if err != nil {
			return operationError(err)
		}
		for _, row := range rows {
			for _, group := range input.GroupBy {
				if value, ok := row[group]; ok {
					row[group] = decodeFieldValue(value, groupFields[group])
				}
			}
		}
		result = AggregateResult{Rows: rows}
		return nil
	})
	if err != nil {
		return AggregateResult{}, operationError(err)
	}
	return result, nil
}

func (port *Port) ValidateSnapshot(
	ctx context.Context,
	snapshot QuerySnapshot,
	currentQuery *TableQuery,
) (SnapshotValidation, error) {
	if err := port.validateConfigured(); err != nil {
		return SnapshotValidation{}, err
	}
	if err := ctx.Err(); err != nil {
		return SnapshotValidation{}, err
	}
	if err := port.validateSnapshotSignature(snapshot); err != nil {
		return SnapshotValidation{}, err
	}
	descriptor, err := port.describe(ctx, port.app, snapshot.Table)
	if err != nil {
		return SnapshotValidation{}, err
	}
	result := SnapshotValidation{
		Valid: true, CurrentDataRevision: descriptor.DataRevision,
		CurrentSchemaRevision: descriptor.SchemaRevision,
	}
	if descriptor.DatabaseID != snapshot.DatabaseID {
		result.Valid, result.Reason = false, "query_changed"
		return result, nil
	}
	if currentQuery != nil {
		normalized, err := Normalize(*currentQuery)
		if err != nil {
			return SnapshotValidation{}, err
		}
		if !queriesEqual(snapshot.NormalizedQuery, normalized) {
			result.Valid, result.Reason = false, "query_changed"
			return result, nil
		}
	}
	if descriptor.SchemaRevision != snapshot.SchemaRevision {
		result.Valid, result.Reason = false, "schema_changed"
		return result, nil
	}
	if descriptor.DataRevision != snapshot.DataRevision {
		result.Valid, result.Reason = false, "application_write"
		return result, nil
	}
	return result, nil
}

func (port *Port) describe(
	ctx context.Context,
	app core.App,
	tableID string,
) (TableDescriptor, error) {
	if err := port.validateConfigured(); err != nil {
		return TableDescriptor{}, err
	}
	if err := ctx.Err(); err != nil {
		return TableDescriptor{}, err
	}
	descriptor, err := port.source.DescribeQueryTable(ctx, app, tableID)
	if err != nil {
		if contextError(err) != nil {
			return TableDescriptor{}, contextError(err)
		}
		var productErr *ProductError
		if errors.As(err, &productErr) {
			return TableDescriptor{}, productErr
		}
		return TableDescriptor{}, productError(
			"query.schema.failed", "table", "query schema could not be loaded", nil)
	}
	return descriptor, nil
}

func (port *Port) validateConfigured() error {
	if port.app == nil || port.source == nil {
		return productError("query.port.unconfigured", "", "query port is not configured", nil)
	}
	if len(port.snapshotKey) < 32 {
		return productError(
			"query.port.unconfigured",
			"snapshotKey",
			"query snapshot signing key must contain at least 32 bytes",
			nil,
		)
	}
	return nil
}

func (port *Port) queryRows(
	ctx context.Context,
	app core.App,
	sql string,
	params map[string]any,
	descriptor TableDescriptor,
) ([]map[string]any, error) {
	rows, err := queryDynamicRows(
		app.DB().NewQuery(sql).WithContext(ctx).Bind(dbx.Params(params)))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		for name, value := range row {
			field, ok := descriptor.Fields[name]
			if !ok {
				continue
			}
			row[name] = decodeFieldValue(value, field)
		}
	}
	// Provider-neutral/query-compiler tests and virtual sources may not have a
	// PocketBase record collection. Product descriptors always supply
	// DigestFields; an empty list explicitly opts out of row-digest projection.
	if len(descriptor.DigestFields) == 0 {
		return rows, nil
	}
	collection, err := app.FindCollectionByNameOrId(descriptor.PhysicalName)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		recordID := fmt.Sprint(row[descriptor.PrimaryKey])
		if recordID == "" {
			return nil, errors.New("query row omitted its primary key")
		}
		record, err := app.FindRecordById(collection, recordID)
		if err != nil {
			return nil, err
		}
		digestRow := productrow.FromRecord(descriptor.DigestFields, record)
		for name, field := range descriptor.Fields {
			if value, exists := digestRow[name]; exists {
				digestRow[name] = decodeFieldValue(value, field)
			}
		}
		digest, err := productrow.Digest(digestRow)
		if err != nil {
			return nil, err
		}
		row[productrow.DigestField] = digest
	}
	return rows, nil
}

type rowsQuery interface {
	Rows() (*dbx.Rows, error)
}

func queryDynamicRows(query rowsQuery) ([]map[string]any, error) {
	rows, err := query.Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		item := make(map[string]any, len(columns))
		for index, name := range columns {
			item[name] = values[index]
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func decodeValue(value any, fieldType FieldType) any {
	if value == nil {
		return nil
	}
	if fieldType == FieldTypeBool {
		switch raw := value.(type) {
		case int64:
			return raw != 0
		case bool:
			return raw
		}
	}
	if fieldType == FieldTypeJSON || fieldType == FieldTypeMultiRelation {
		var decoded any
		switch raw := value.(type) {
		case string:
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				return decoded
			}
		case []byte:
			if json.Unmarshal(raw, &decoded) == nil {
				return decoded
			}
		}
	}
	return value
}

func decodeFieldValue(value any, field FieldDescriptor) any {
	if field.AutoDate && value != nil {
		var raw string
		switch typed := value.(type) {
		case string:
			raw = typed
		case []byte:
			raw = string(typed)
		}
		if raw != "" {
			parsed, err := time.Parse(pbtypes.DefaultDateLayout, raw)
			if err != nil {
				autodateobs.Increment(autodateobs.ReadParseFailed)
				return value
			}
			value = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return decodeEnumValue(field.Enum, decodeValue(value, field.Type))
}

var snapshotIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (port *Port) buildSnapshot(
	descriptor TableDescriptor,
	normalized TableQuery,
) (QuerySnapshot, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return QuerySnapshot{}, productError(
			"query.snapshot.failed", "", "query snapshot could not be issued", nil)
	}
	snapshot := QuerySnapshot{
		SnapshotID:      hex.EncodeToString(nonce),
		DatabaseID:      descriptor.DatabaseID,
		Table:           descriptor.TableID,
		SchemaRevision:  descriptor.SchemaRevision,
		DataRevision:    descriptor.DataRevision,
		NormalizedQuery: normalized,
	}
	digest, err := snapshotDigest(port.snapshotKey, snapshot)
	if err != nil {
		return QuerySnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func snapshotDigest(key []byte, snapshot QuerySnapshot) (string, error) {
	payload := struct {
		SnapshotID     string     `json:"snapshotId"`
		DatabaseID     string     `json:"databaseId"`
		Table          string     `json:"table"`
		SchemaRevision string     `json:"schemaRevision"`
		DataRevision   int64      `json:"dataRevision"`
		Query          TableQuery `json:"query"`
	}{
		snapshot.SnapshotID,
		snapshot.DatabaseID,
		snapshot.Table,
		snapshot.SchemaRevision,
		snapshot.DataRevision,
		snapshot.NormalizedQuery,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", productError(
			"query.snapshot.failed", "", "query snapshot could not be encoded", nil)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (port *Port) validateSnapshotSignature(snapshot QuerySnapshot) error {
	if !snapshotIDPattern.MatchString(snapshot.SnapshotID) {
		return productError(
			"query.snapshot.invalid", "snapshotId", "query snapshot id is invalid", nil)
	}
	provided, err := hex.DecodeString(snapshot.Digest)
	if err != nil || len(provided) != sha256.Size {
		return productError(
			"query.snapshot.invalid", "digest", "query snapshot signature is invalid", nil)
	}
	expected, err := snapshotDigest(port.snapshotKey, snapshot)
	if err != nil {
		return err
	}
	expectedBytes, _ := hex.DecodeString(expected)
	if !hmac.Equal(provided, expectedBytes) {
		return productError(
			"query.snapshot.invalid", "digest", "query snapshot signature is invalid", nil)
	}
	return nil
}

func queriesEqual(left, right TableQuery) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func storageError(err error) *ProductError {
	return productError(
		"query.storage.failed", "", "query storage operation failed", nil,
	)
}

func contextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func operationError(err error) error {
	if cancellation := contextError(err); cancellation != nil {
		return cancellation
	}
	var productErr *ProductError
	if errors.As(err, &productErr) {
		return productErr
	}
	return storageError(err)
}
