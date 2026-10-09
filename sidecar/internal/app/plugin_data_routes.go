package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const pluginDataPath = "/api/vibetable/v2/plugins/data"
const pluginDataContract = "vibetable.plugin-data.v2"

type pluginDataRequest struct {
	Operation            string                            `json:"operation"`
	ProjectKey           string                            `json:"projectKey"`
	PluginID             string                            `json:"pluginId"`
	SessionEpoch         uint64                            `json:"sessionEpoch"`
	InstallationRevision int64                             `json:"installationRevision"`
	ActiveCollection     string                            `json:"activeCollection"`
	Request              json.RawMessage                   `json:"request"`
	Cursor               string                            `json:"cursor,omitempty"`
	Dependencies         map[string]pluginComputedRevision `json:"dependencies,omitempty"`
}
type pluginDescribeRequest struct {
	Accepts    []string `json:"accepts"`
	Collection string   `json:"collection,omitempty"`
}
type pluginQueryRequest struct {
	Contract   string                   `json:"contract"`
	Collection string                   `json:"collection"`
	Fields     []string                 `json:"fields"`
	IDs        *[]string                `json:"ids,omitempty"`
	Filters    []query.FilterExpression `json:"filters,omitempty"`
	Sorts      []query.SortCondition    `json:"sorts,omitempty"`
	PageSize   *int                     `json:"pageSize,omitempty"`
}
type pluginComputedRevision struct {
	Definition int    `json:"definition"`
	Watermark  string `json:"watermark"`
}
type pluginInstallation struct {
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
	Manifest struct {
		Compatibility struct {
			PluginAPI string `json:"pluginApi"`
		} `json:"compatibility"`
		Permissions struct {
			Data []pluginReadGrant `json:"data"`
		} `json:"permissions"`
	} `json:"manifest"`
}
type pluginReadGrant struct {
	Collection string   `json:"collection"`
	Operations []string `json:"operations"`
	Fields     []string `json:"fields"`
}

func pluginDataError(code string) error {
	return &query.ProductError{Code: code, Message: "plugin data request could not be completed"}
}
func pluginHas(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func (installation pluginInstallation) grant(table, active string) *pluginReadGrant {
	for _, grant := range installation.Manifest.Permissions.Data {
		if pluginHas(grant.Operations, "read") && pluginHas(grant.Operations, "query") &&
			(grant.Collection == table || grant.Collection == "$active" && table == active) {
			return &grant
		}
	}
	return nil
}

// This route has no renderer registration. Host supplies execution identity;
// grants are read from the existing authoritative installation, never the wire.
func registerPluginDataRoutes(r *router.Router[*core.RequestEvent], app core.App, source *queryschema.Source, workspaceID string, epoch uint64) {
	r.POST(pluginDataPath, func(event *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(event.Request.Body, maxQueryRequestBytes+1))
		var input pluginDataRequest
		if err != nil || len(raw) > maxQueryRequestBytes || decodePluginParams(raw, &input) != nil || input.SessionEpoch != epoch || epoch == 0 {
			return event.JSON(http.StatusBadRequest, map[string]any{"code": "plugin_capability_invalid", "message": "plugin data request is invalid"})
		}
		result, err := executePluginData(event.Request.Context(), app, source, workspaceID, input)
		if err != nil {
			code := "plugin_query_failed"
			var domain *query.ProductError
			if errors.As(err, &domain) {
				code = domain.Code
			}
			if errors.Is(err, context.Canceled) {
				code = "plugin_cancel_requested"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				code = "plugin_timeout"
			}
			return event.JSON(http.StatusConflict, map[string]any{"code": code, "message": "plugin data request could not be completed"})
		}
		return event.JSON(http.StatusOK, result)
	})
}
func executePluginData(ctx context.Context, app core.App, source *queryschema.Source, workspaceID string, input pluginDataRequest) (map[string]any, error) {
	var result map[string]any
	err := app.RunInTransaction(func(tx core.App) error {
		store := pluginstore.New(tx, workspaceID)
		raw, err := store.GetInstallation(ctx, input.ProjectKey, input.PluginID)
		var installed pluginInstallation
		if err != nil || len(raw) == 0 || json.Unmarshal(raw, &installed) != nil || installed.Status != "enabled" {
			return pluginDataError("plugin_read_denied")
		}
		if installed.Revision != input.InstallationRevision {
			return pluginDataError("plugin_cursor_stale")
		}
		if installed.Manifest.Compatibility.PluginAPI != "2.x" {
			return pluginDataError("plugin_api_unsupported")
		}
		switch input.Operation {
		case "describe":
			var request pluginDescribeRequest
			if decodePluginParams(input.Request, &request) != nil || !reflect.DeepEqual(request.Accepts, []string{pluginDataContract}) {
				return pluginDataError("plugin_api_unsupported")
			}
			if request.Collection != "" {
				grant := installed.grant(request.Collection, input.ActiveCollection)
				if grant == nil {
					return pluginDataError("plugin_read_denied")
				}
				descriptor, schema, err := source.DescribeSelectionTable(ctx, tx, request.Collection)
				if err != nil {
					return pluginDataError("plugin_query_failed")
				}
				result = pluginTableMetadata(schema, descriptor, *grant)
				return nil
			}
			tables := []any{}
			seen := map[string]bool{}
			for _, grant := range installed.Manifest.Permissions.Data {
				id := grant.Collection
				if id == "$active" {
					id = input.ActiveCollection
				}
				if id == "" || seen[id] || installed.grant(id, input.ActiveCollection) == nil {
					continue
				}
				table, err := schemaexecution.Describe(ctx, tx, id)
				if err != nil {
					return pluginDataError("plugin_query_failed")
				}
				tables = append(tables, map[string]any{"collection": id, "displayName": table.Snapshot.DisplayName, "schemaRevision": table.Snapshot.SchemaRevision})
				seen[id] = true
			}
			result = map[string]any{"contract": pluginDataContract, "tables": tables}
			return nil
		case "query":
			var request pluginQueryRequest
			if decodePluginParams(input.Request, &request) != nil || request.Contract != "vibetable.plugin-query.v2" || request.Collection == "" || request.Fields == nil {
				return pluginDataError("plugin_capability_invalid")
			}
			var rawFields map[string]json.RawMessage
			_ = json.Unmarshal(input.Request, &rawFields)
			for _, key := range []string{"ids", "filters", "sorts", "pageSize"} {
				if value, ok := rawFields[key]; ok && string(value) == "null" {
					return pluginDataError("plugin_capability_invalid")
				}
			}
			size := 100
			if request.PageSize != nil {
				size = *request.PageSize
			}
			if size < 1 || size > 200 || request.IDs != nil && len(*request.IDs) > 200 {
				return pluginDataError("plugin_query_limit")
			}
			grant := installed.grant(request.Collection, input.ActiveCollection)
			if grant == nil {
				return pluginDataError("plugin_read_denied")
			}
			scoped := &pluginQuerySource{source: source, grant: *grant, request: request, expected: input.Dependencies}
			_, _, err := scoped.DescribeSelectionTable(ctx, tx, request.Collection)
			if err != nil {
				return err
			}
			q := scoped.normalized
			port := query.NewPort(tx, scoped)
			var page query.CursorWindow
			if input.Cursor == "" {
				page, err = port.OpenCursor(ctx, request.Collection, q)
			} else {
				page, err = port.FetchCursor(ctx, input.Cursor)
			}
			if err != nil {
				return err
			}
			if input.Cursor != "" && !reflect.DeepEqual(page.Snapshot.NormalizedQuery, q) {
				return pluginDataError("plugin_cursor_invalid")
			}
			items := []any{}
			complete := true
			for _, row := range page.Rows {
				item := map[string]any{}
				for _, id := range request.Fields {
					name := scoped.names[id]
					value := row[name]
					field := scoped.computed[name]
					if field.ComputedEnvelope {
						state, fresh := "updating", false
						if envelope, ok := value.(map[string]any); ok {
							if status, exists := envelope["state"].(string); exists {
								state, fresh = status, field.ComputedReady && status == "ready"
								value = envelope["value"]
							}
						}
						if !fresh {
							value = nil
							complete = false
						}
						value = map[string]any{"state": state, "value": value, "fresh": fresh}
					}
					item[id] = value
				}
				items = append(items, item)
			}
			result = map[string]any{"contract": "vibetable.plugin-query-page.v2", "items": items, "nextCursor": page.NextCursor, "filteredRows": page.FilteredRows, "totalRows": page.TotalRows,
				"schemaRevision": page.Snapshot.SchemaRevision, "dataRevision": page.Snapshot.DataRevision, "complete": complete, "dependencies": scoped.current}
			return nil
		default:
			return pluginDataError("plugin_capability_invalid")
		}
	})
	return result, err
}
func pluginAllowed(grant pluginReadGrant, schema v2.SchemaSnapshot) map[string]string {
	result := map[string]string{}
	all := pluginHas(grant.Fields, "*") || pluginHas(grant.Fields, "$configured")
	if all || pluginHas(grant.Fields, "id") {
		result["id"] = "id"
	}
	for _, field := range schema.Fields {
		if all || pluginHas(grant.Fields, field.Identity.PhysicalName) || pluginHas(grant.Fields, field.Identity.FieldID) {
			result[field.Identity.FieldID] = field.Identity.PhysicalName
		}
	}
	return result
}
func pluginFieldOperators(field query.FieldDescriptor) []string {
	if field.ComputedEnvelope {
		return []string{}
	}
	result := []string{"is_null", "is_not_null"}
	if field.Enum != nil {
		if field.Enum.Multiple {
			return append(result, "contains")
		}
		return append(result, "eq", "ne", "in")
	}
	switch field.Type {
	case query.FieldTypeText:
		return append(result, "eq", "ne", "in", "contains", "starts_with", "ends_with")
	case query.FieldTypeNumber, query.FieldTypeDate:
		return append(result, "eq", "ne", "in", "gt", "lt", "gte", "lte", "between")
	case query.FieldTypeBool, query.FieldTypeRelation, query.FieldTypeMultiRelation:
		return append(result, "eq", "ne", "in")
	case query.FieldTypeJSON:
		return append(result, "contains")
	default:
		return result
	}
}
func pluginSortable(field query.FieldDescriptor) bool {
	return !field.ComputedEnvelope && field.Enum == nil && (field.Type == query.FieldTypeText || field.Type == query.FieldTypeNumber || field.Type == query.FieldTypeBool || field.Type == query.FieldTypeDate)
}
func pluginTableMetadata(schema v2.SchemaSnapshot, descriptor query.TableDescriptor, grant pluginReadGrant) map[string]any {
	allowed := pluginAllowed(grant, schema)
	fields := []any{}
	if _, ok := allowed["id"]; ok {
		fields = append(fields, map[string]any{"fieldId": "id", "displayName": "ID", "logicalType": "text", "resultType": "text", "readonly": true, "nullable": false, "display": nil, "options": []any{}, "constraints": nil, "filterOperators": pluginFieldOperators(descriptor.Fields["id"]), "sortable": true})
	}
	for _, field := range schema.Fields {
		name, ok := allowed[field.Identity.FieldID]
		if !ok {
			continue
		}
		d := descriptor.Fields[name]
		options := []any{}
		if field.Select != nil {
			for _, option := range field.Select.Options {
				options = append(options, option)
			}
		}
		resultType := string(d.Type)
		if field.Formula != nil {
			resultType = string(field.Formula.ResultType)
		}
		item := map[string]any{"fieldId": field.Identity.FieldID, "displayName": field.DisplayName, "logicalType": field.LogicalType, "resultType": resultType, "readonly": describeFieldReadonly(schema, field), "nullable": !field.Value.Required, "display": field.Display, "options": options, "constraints": field.Constraints, "filterOperators": pluginFieldOperators(d), "sortable": pluginSortable(d)}
		if field.Formula != nil && field.Formula.ResultElementType != "" {
			item["resultElementType"] = field.Formula.ResultElementType
		}
		fields = append(fields, item)
	}
	return map[string]any{"contract": pluginDataContract, "collection": schema.TableID, "displayName": schema.DisplayName, "schemaRevision": schema.SchemaRevision, "fields": fields}
}

// Narrow only this plugin port. Ordinary product queries keep their existing behavior.
type pluginQuerySource struct {
	source     *queryschema.Source
	grant      pluginReadGrant
	request    pluginQueryRequest
	expected   map[string]pluginComputedRevision
	current    map[string]pluginComputedRevision
	names      map[string]string
	computed   map[string]query.FieldDescriptor
	normalized query.TableQuery
}

func (s *pluginQuerySource) DescribeQueryTable(ctx context.Context, app core.App, table string) (query.TableDescriptor, error) {
	d, _, err := s.DescribeSelectionTable(ctx, app, table)
	return d, err
}
func (s *pluginQuerySource) DescribeSelectionTable(ctx context.Context, app core.App, table string) (query.TableDescriptor, v2.SchemaSnapshot, error) {
	if table != s.request.Collection {
		return query.TableDescriptor{}, v2.SchemaSnapshot{}, pluginDataError("plugin_cursor_invalid")
	}
	d, schema, err := s.source.DescribeSelectionTable(ctx, app, table)
	if err != nil {
		return d, schema, pluginDataError("plugin_query_failed")
	}
	s.names = pluginAllowed(s.grant, schema)
	used := map[string]bool{}
	require := func(id string) (string, error) {
		name, ok := s.names[id]
		if !ok || strings.Contains(id, ".") {
			return "", pluginDataError("plugin_read_denied")
		}
		used[name] = true
		return name, nil
	}
	for _, id := range s.request.Fields {
		if _, err := require(id); err != nil {
			return d, schema, err
		}
	}
	q := query.TableQuery{Limit: 100, Filters: s.request.Filters, Sorts: append([]query.SortCondition(nil), s.request.Sorts...)}
	if s.request.PageSize != nil {
		q.Limit = *s.request.PageSize
	}
	raw, _ := json.Marshal(q.Filters)
	q.Filters = nil
	if json.Unmarshal(raw, &q.Filters) != nil {
		return d, schema, pluginDataError("plugin_capability_invalid")
	}
	var walk func([]query.FilterExpression) error
	walk = func(filters []query.FilterExpression) error {
		for i := range filters {
			f := &filters[i]
			if len(f.Filters) > 0 {
				if err := walk(f.Filters); err != nil {
					return err
				}
				continue
			}
			name, err := require(f.Field)
			if err != nil {
				return err
			}
			if d.Fields[name].ComputedEnvelope {
				return pluginDataError("plugin_query_computed_unsupported")
			}
			if !pluginHas(pluginFieldOperators(d.Fields[name]), string(f.Operator)) {
				return pluginDataError("query.operator.unsupported")
			}
			f.Field = name
		}
		return nil
	}
	if err := walk(q.Filters); err != nil {
		return d, schema, err
	}
	for i := range q.Sorts {
		name, err := require(q.Sorts[i].Field)
		if err != nil {
			return d, schema, err
		}
		if !pluginSortable(d.Fields[name]) {
			return d, schema, pluginDataError("query.cursor.unsupported_sort")
		}
		q.Sorts[i].Field = name
	}
	if s.request.IDs != nil {
		if _, err := require("id"); err != nil {
			return d, schema, err
		}
		values := make([]any, len(*s.request.IDs))
		for i, id := range *s.request.IDs {
			if id == "" {
				return d, schema, pluginDataError("plugin_capability_invalid")
			}
			values[i] = id
		}
		if len(values) == 0 {
			q.Filters = []query.FilterExpression{{Field: "id", Operator: query.OperatorEqual, Value: ""}}
		} else {
			q.Filters = []query.FilterExpression{{GroupLogic: query.LogicAnd, Filters: []query.FilterExpression{{Filters: q.Filters, GroupLogic: query.LogicAnd}, {Field: "id", Operator: query.OperatorIn, Value: values}}}}
			if len(s.request.Filters) == 0 {
				q.Filters = []query.FilterExpression{{Field: "id", Operator: query.OperatorIn, Value: values}}
			}
		}
	}
	// The primary key is internal keyset transport; it is never projected unless granted.
	used["id"] = true
	s.current = map[string]pluginComputedRevision{}
	s.computed = map[string]query.FieldDescriptor{}
	narrowed := map[string]query.FieldDescriptor{}
	presence := map[string]string{}
	for name := range used {
		field := d.Fields[name]
		if field.ComputedEnvelope {
			s.current[name] = pluginComputedRevision{field.ComputedDefinitionVersion, field.ComputedDependencyWatermark}
			s.computed[name] = field
			field.PreserveComputedEnvelope = true
		}
		if field.Relation != nil {
			copy := *field.Relation
			// Strip every label source so this port returns raw relation IDs only.
			copy.DisplayField = ""
			copy.PrimaryDisplayField = ""
			copy.Fields = map[string]query.FieldDescriptor{}
			copy.PresenceFields = nil
			field.Relation = &copy
		}
		narrowed[name] = field
		if value := d.PresenceFields[name]; value != "" {
			presence[name] = value
		}
	}
	if s.expected != nil && !reflect.DeepEqual(s.expected, s.current) {
		return d, schema, pluginDataError("plugin_cursor_stale")
	}
	archiveField := d.Fields[d.ArchiveField]
	archivePresence := d.PresenceFields[d.ArchiveField]
	d.Fields = narrowed
	d.PresenceFields = presence
	d.DigestFields = nil
	d.DigestProjector = nil
	// Archive filtering remains internal; it must not turn into a visible field.
	if d.ArchiveField != "" {
		if _, ok := d.Fields[d.ArchiveField]; !ok {
			d.Fields[d.ArchiveField] = archiveField
			if archivePresence != "" {
				d.PresenceFields[d.ArchiveField] = archivePresence
			}
		}
	}
	s.normalized, err = query.Normalize(q)
	if err != nil {
		return d, schema, err
	}
	return d, schema, nil
}

var _ query.Source = (*pluginQuerySource)(nil)
