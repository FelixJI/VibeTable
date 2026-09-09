package relationpair

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	maxSamples   = 50
	maxCellBytes = 65536
	maxPageLinks = 2000
)

var ErrRevisionChanged = errors.New("relation.inspect.revision_changed")
var ErrInvalidRequest = errors.New("relation.inspect.invalid_request")
var physicalField = regexp.MustCompile(`^f_[a-z0-9_]{8,}$`)

type endpointState struct {
	Endpoint
	physical   string
	definition v2.FieldDefinition
	readable   bool
}

// Inspect reads one bounded page of both directed endpoints. All findings are
// page-local (including metadata findings repeated on continuation). Complete
// means every page was readable, not that the pair is healthy; clients must
// retain findings from earlier pages. It never authorizes or performs repair.
func Inspect(ctx context.Context, app core.App, request Request) (Report, error) {
	report := Report{Counts: map[string]int{}, Samples: []Finding{}, PageComplete: true}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if app == nil || request.TableID == "" || request.FieldID == "" || request.Limit < 0 || request.Limit > 200 {
		return Report{}, ErrInvalidRequest
	}
	if request.Cursor != nil {
		if request.Cursor.Done[0] && request.Cursor.Done[1] {
			return Report{}, ErrInvalidRequest
		}
		for _, after := range request.Cursor.After {
			if len(after) > 200 || strings.IndexByte(after, 0) >= 0 {
				return Report{}, ErrInvalidRequest
			}
		}
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	source, err := loadEndpoint(ctx, app, request.TableID, request.FieldID, 0, &report)
	if err != nil {
		return Report{}, err
	}
	report.Endpoints[0] = source.Endpoint
	if request.Cursor != nil && request.Cursor.Endpoints[0] != source.Endpoint {
		return Report{}, ErrRevisionChanged
	}
	if source.definition.Relation == nil || source.definition.Relation.TargetTableID == "" || source.definition.Relation.ReciprocalFieldID == "" {
		report.add(Finding{Code: "metadata_asymmetric", Detail: "pair endpoint is missing"})
		report.PageComplete, report.Finished = false, true
		return finishIncomplete(ctx, app, report)
	}
	spec := source.definition.Relation
	report.PairID = spec.PairID
	if request.Cursor != nil && request.Cursor.PairID != report.PairID {
		return Report{}, ErrRevisionChanged
	}
	target, err := loadEndpoint(ctx, app, spec.TargetTableID, spec.ReciprocalFieldID, 1, &report)
	if err != nil {
		return Report{}, err
	}
	report.Endpoints[1] = target.Endpoint
	if request.Cursor != nil && request.Cursor.Endpoints[1] != target.Endpoint {
		return Report{}, ErrRevisionChanged
	}
	states := [2]endpointState{source, target}
	if spec.PairID == "" || target.definition.Relation == nil ||
		target.definition.Relation.PairID != spec.PairID ||
		target.definition.Relation.ReciprocalFieldID != request.FieldID ||
		target.definition.Relation.TargetTableID != request.TableID ||
		(request.TableID == spec.TargetTableID && request.FieldID == spec.ReciprocalFieldID) {
		report.add(Finding{Code: "metadata_asymmetric", Detail: "pair IDs or reciprocal pointers disagree"})
		report.PageComplete, report.Finished = false, true
		return finishIncomplete(ctx, app, report)
	}
	if spec.DeletePolicy != target.definition.Relation.DeletePolicy {
		report.add(Finding{Code: "metadata_asymmetric", Detail: "shared delete policies disagree"})
	}
	cursor := Cursor{PairID: report.PairID, Endpoints: report.Endpoints}
	if request.Cursor != nil {
		if request.Cursor.PairID != cursor.PairID || request.Cursor.Endpoints != cursor.Endpoints {
			return Report{}, ErrRevisionChanged
		}
		cursor = *request.Cursor
	}
	for side, state := range states {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if !state.readable || !states[1-side].readable {
			report.PageComplete = false
			cursor.Done[side] = true
			continue
		}
		if cursor.Done[side] {
			continue
		}
		rows, err := readRows(ctx, app, state, `id > {:after} ORDER BY id LIMIT {:limit}`,
			dbx.Params{"after": cursor.After[side], "limit": request.Limit + 1})
		if err != nil {
			return Report{}, err
		}
		cursor.Done[side] = len(rows) <= request.Limit
		if len(rows) > request.Limit {
			rows = rows[:request.Limit]
		}
		if err := inspectRows(ctx, app, side, rows, states, &report); err != nil {
			return Report{}, err
		}
		report.RowsScanned[side] = len(rows)
		if len(rows) > 0 {
			cursor.After[side] = rows[len(rows)-1].ID
		}
	}
	if err := checkRevisions(ctx, app, report.Endpoints); err != nil {
		return Report{}, err
	}
	cursor.Incomplete = cursor.Incomplete || !report.PageComplete
	report.Finished = cursor.Done[0] && cursor.Done[1]
	report.Complete = report.Finished && !cursor.Incomplete
	if !report.Finished {
		report.Next = &cursor
	}
	return report, nil
}

func (report *Report) add(f Finding) {
	report.Counts[f.Code]++
	if len(report.Samples) < maxSamples {
		report.Samples = append(report.Samples, f)
	} else {
		report.SamplesTruncated = true
	}
}

func tableBinding(ctx context.Context, app core.App, tableID string) (Endpoint, string, error) {
	var row struct {
		Schema     int64  `db:"schema_revision"`
		Data       int64  `db:"data_revision"`
		Physical   string `db:"physical_name"`
		Collection string `db:"collection_id"`
		Kind       string `db:"kind"`
	}
	err := app.DB().NewQuery(`SELECT schema_revision,data_revision,physical_name,collection_id,kind FROM vibetable_tables WHERE table_id={:id}`).
		WithContext(ctx).Bind(dbx.Params{"id": tableID}).One(&row)
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("read inspection table binding: %w", err)
	}
	if row.Schema < 0 || row.Data < 0 || row.Physical == "" || (row.Kind != "base" && row.Kind != "") {
		return Endpoint{}, "", errors.New("relation.inspect.invalid_table_binding")
	}
	collection := &core.Collection{}
	err = app.CollectionQuery().WithContext(ctx).AndWhere(dbx.HashExp{"id": row.Collection}).One(collection)
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("read inspection collection: %w", err)
	}
	if collection.Name != row.Physical {
		return Endpoint{}, "", errors.New("relation.inspect.invalid_table_binding")
	}
	return Endpoint{TableID: tableID, SchemaRevision: v2.FormatSchemaRevision(row.Schema), DataRevision: row.Data}, row.Physical, nil
}

func loadEndpoint(ctx context.Context, app core.App, tableID, fieldID string, side int, report *Report) (endpointState, error) {
	endpoint, physical, err := tableBinding(ctx, app, tableID)
	if err != nil {
		return endpointState{}, err
	}
	endpoint.FieldID = fieldID
	state := endpointState{Endpoint: endpoint, physical: physical}
	var row struct {
		Definition string `db:"definition_v2_json"`
		Physical   string `db:"physical_name"`
		Lifecycle  string `db:"lifecycle_state"`
	}
	err = app.DB().NewQuery(`SELECT definition_v2_json,physical_name,lifecycle_state FROM vibetable_fields WHERE table_id={:table} AND field_id={:field}`).
		WithContext(ctx).Bind(dbx.Params{"table": tableID, "field": fieldID}).One(&row)
	if errors.Is(err, sql.ErrNoRows) {
		report.add(Finding{Code: "metadata_asymmetric", Endpoint: side, Detail: "field is missing"})
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read inspection field: %w", err)
	}
	if err := v2.StrictDecode([]byte(row.Definition), &state.definition); err != nil {
		report.add(Finding{Code: "metadata_invalid", Endpoint: side, Detail: "field definition cannot be decoded"})
		return state, nil
	}
	def := state.definition
	if err := v2.Validate(def); err != nil {
		report.add(Finding{Code: "metadata_invalid", Endpoint: side, Detail: "field definition violates schema contract"})
		report.PageComplete = false
	}
	if def.Identity.FieldID != fieldID || def.Identity.PhysicalName != row.Physical ||
		!physicalField.MatchString(row.Physical) || def.LogicalType != v2.LogicalRelation || def.Relation == nil ||
		row.Lifecycle != string(def.Lifecycle.State) || def.Lifecycle.State != v2.LifecycleActive {
		report.add(Finding{Code: "metadata_invalid", Endpoint: side, Detail: "field binding or lifecycle is inconsistent"})
		return state, nil
	}
	state.readable = true
	var mirrored struct {
		Relation     string `db:"relation_id"`
		Target       string `db:"target_table_id"`
		Cardinality  string `db:"cardinality"`
		DeletePolicy string `db:"delete_policy"`
		Pair         string `db:"pair_id"`
		Reciprocal   string `db:"reciprocal_field_id"`
	}
	err = app.DB().NewQuery(`SELECT relation_id,target_table_id,cardinality,delete_policy,pair_id,reciprocal_field_id FROM vibetable_relations WHERE source_table_id={:table} AND source_field_id={:field}`).
		WithContext(ctx).Bind(dbx.Params{"table": tableID, "field": fieldID}).One(&mirrored)
	if errors.Is(err, sql.ErrNoRows) {
		report.add(Finding{Code: "metadata_asymmetric", Endpoint: side, Detail: "relation metadata mirror is missing"})
	} else if err != nil {
		return state, fmt.Errorf("read relation metadata mirror: %w", err)
	} else if mirrored.Relation != tableID+"."+fieldID || mirrored.Target != def.Relation.TargetTableID ||
		mirrored.Cardinality != def.Relation.Cardinality || mirrored.DeletePolicy != def.Relation.DeletePolicy ||
		mirrored.Pair != def.Relation.PairID || mirrored.Reciprocal != def.Relation.ReciprocalFieldID {
		report.add(Finding{Code: "metadata_asymmetric", Endpoint: side, Detail: "relation metadata mirror disagrees"})
	}
	return state, nil
}

type storedRow struct {
	ID          string         `db:"id"`
	Value       sql.NullString `db:"value"`
	StorageType string         `db:"storage_type"`
	Bytes       int            `db:"bytes"`
	Present     int            `db:"present"`
}

func quote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func readRows(ctx context.Context, app core.App, state endpointState, predicate string, params dbx.Params) ([]storedRow, error) {
	field := "r." + quote(state.definition.Identity.PhysicalName)
	presence := "1"
	if state.definition.Value.Presence.Mode == v2.PresenceCompanion {
		name := state.definition.Value.Presence.PhysicalName
		if name != "__vt_has_"+state.definition.Identity.PhysicalName {
			return nil, errors.New("relation.inspect.invalid_presence_binding")
		}
		presence = "r." + quote(name)
	}
	// Do not fetch an unbounded corrupt blob into Go merely to reject it.
	sqlText := fmt.Sprintf(`SELECT id, CASE WHEN length(CAST(%s AS BLOB))<=%d THEN %s ELSE NULL END AS value,
        typeof(%s) AS storage_type, COALESCE(length(CAST(%s AS BLOB)),0) AS bytes, %s AS present FROM %s AS r WHERE %s`,
		field, maxCellBytes, field, field, field, presence, quote(state.physical), predicate)
	rows := []storedRow{}
	if err := app.DB().NewQuery(sqlText).WithContext(ctx).Bind(params).All(&rows); err != nil {
		return nil, fmt.Errorf("read relation inspection rows: %w", err)
	}
	return rows, nil
}

func links(row storedRow) ([]string, string) {
	if row.Bytes > maxCellBytes {
		return nil, "scan_limit"
	}
	if row.StorageType != "text" && row.StorageType != "null" {
		return nil, "invalid_value"
	}
	if !row.Value.Valid || row.Value.String == "" {
		return nil, ""
	}
	raw := strings.TrimSpace(row.Value.String)
	if strings.HasPrefix(raw, "[") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, "invalid_value"
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return nil, "invalid_value"
			}
		}
		return values, ""
	}
	if strings.HasPrefix(raw, "{") || raw == "" || raw != row.Value.String {
		return nil, "invalid_value"
	}
	return []string{raw}, ""
}

func inspectRows(ctx context.Context, app core.App, side int, rows []storedRow, states [2]endpointState, report *Report) error {
	source, target := states[side], states[1-side]
	edges := map[string][]string{}
	targets := map[string]struct{}{}
	used := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, code := links(row)
		if code == "" && used+len(ids) > maxPageLinks {
			code = "scan_limit"
		}
		if code != "" {
			report.add(Finding{Code: code, Endpoint: side, RecordID: row.ID})
			report.PageComplete = false
			continue
		}
		used += len(ids)
		if row.Present != 0 && row.Present != 1 {
			report.add(Finding{Code: "presence_mismatch", Endpoint: side, RecordID: row.ID})
		} else if row.Present == 0 && len(ids) > 0 {
			report.add(Finding{Code: "presence_mismatch", Endpoint: side, RecordID: row.ID})
		}
		if source.definition.Relation.Cardinality == "one" && len(ids) > 1 {
			report.add(Finding{Code: "one_conflict", Endpoint: side, RecordID: row.ID})
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				report.add(Finding{Code: "duplicate", Endpoint: side, RecordID: row.ID, TargetID: id})
				continue
			}
			seen[id], targets[id] = true, struct{}{}
			edges[row.ID] = append(edges[row.ID], id)
		}
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type reverseLinks struct {
		ids         map[string]bool
		unavailable bool
	}
	found := map[string]reverseLinks{}
	reverseUsed := 0
	for offset := 0; offset < len(ids); offset += 200 {
		end := min(offset+200, len(ids))
		params := dbx.Params{}
		placeholders := make([]string, 0, end-offset)
		for i, id := range ids[offset:end] {
			key := fmt.Sprintf("target%d", i)
			params[key] = id
			placeholders = append(placeholders, "{:"+key+"}")
		}
		matches, err := readRows(ctx, app, target, "id IN ("+strings.Join(placeholders, ",")+")", params)
		if err != nil {
			return err
		}
		for _, row := range matches {
			values, code := links(row)
			if code == "" && reverseUsed+len(values) > maxPageLinks {
				code = "scan_limit"
			}
			reverse := reverseLinks{ids: map[string]bool{}, unavailable: code != ""}
			if code != "" {
				report.add(Finding{Code: code, Endpoint: 1 - side, RecordID: row.ID})
				report.PageComplete = false
			} else {
				reverseUsed += len(values)
				for _, id := range values {
					reverse.ids[id] = true
				}
			}
			found[row.ID] = reverse
		}
	}
	// Iterate source rows in key order, not map order, for reproducible samples.
	for _, row := range rows {
		for _, id := range edges[row.ID] {
			if err := ctx.Err(); err != nil {
				return err
			}
			other, exists := found[id]
			if !exists {
				report.add(Finding{Code: "dangling", Endpoint: side, RecordID: row.ID, TargetID: id})
				continue
			}
			if other.unavailable {
				continue
			}
			reciprocal := other.ids[row.ID]
			if !reciprocal {
				report.add(Finding{Code: "missing_reciprocal", Endpoint: side, RecordID: row.ID, TargetID: id})
				if target.definition.Relation.Cardinality == "one" && len(other.ids) > 0 {
					report.add(Finding{Code: "one_conflict", Endpoint: 1 - side, RecordID: id, TargetID: row.ID})
				}
			}
		}
	}
	return nil
}

func checkRevisions(ctx context.Context, app core.App, endpoints [2]Endpoint) error {
	for _, endpoint := range endpoints {
		if endpoint.TableID == "" {
			continue
		}
		latest, _, err := tableBinding(ctx, app, endpoint.TableID)
		if err != nil {
			return err
		}
		if latest.SchemaRevision != endpoint.SchemaRevision || latest.DataRevision != endpoint.DataRevision {
			return ErrRevisionChanged
		}
	}
	return ctx.Err()
}

func finishIncomplete(ctx context.Context, app core.App, report Report) (Report, error) {
	if err := checkRevisions(ctx, app, report.Endpoints); err != nil {
		return Report{}, err
	}
	return report, nil
}
