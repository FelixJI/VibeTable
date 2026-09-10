package metadata

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func DashboardManifest() map[string]any {
	panels := []any{}
	for _, kind := range []string{"label", "metric", "metric-list", "list", "time-series", "bar", "line", "donut", "pie"} {
		width, height := 4, 3
		properties := map[string]any{}
		switch kind {
		case "label":
			width, height = 1, 1
			properties["text"] = map[string]any{"type": "string"}
		case "metric":
			width, height = 2, 2
		case "metric-list":
			width, height = 3, 3
		case "list":
			width, height = 4, 3
		case "time-series":
			width, height = 6, 4
		}
		if kind == "list" || kind == "time-series" || kind == "bar" || kind == "line" || kind == "donut" || kind == "pie" {
			properties["drilldownFields"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		}
		if kind == "time-series" || kind == "line" {
			properties["fillType"] = map[string]any{"type": "string", "enum": []any{"solid", "gradient"}}
		}
		panels = append(panels, map[string]any{"type": kind,
			"minSize": map[string]any{"x": 0,
				"y":      0,
				"width":  width,
				"height": height},
			"optionsSchema": map[string]any{"type": "object",
				"properties":           properties,
				"additionalProperties": false},
			"rendererVersion": "2"})
	}
	return map[string]any{"manifestVersion": "dashboard-panel-manifest.v2", "queryContract": "product-query-port.v1", "panels": panels}
}
func DashboardQueryLimits() map[string]any { return dashboardLimits() }
func validateDashboardPanels(params DashboardParams) error {
	for _, raw := range params["panels"].([]any) {
		panel := raw.(map[string]any)
		var definition map[string]any
		for _, entry := range DashboardManifest()["panels"].([]any) {
			m := entry.(map[string]any)
			if m["type"] == panel["type"] {
				definition = m
			}
		}
		if definition == nil {
			return dashboardErrorCode("dashboard_panel_type_unknown", "panel type is not in the runtime manifest")
		}
		position := panel["position"].(map[string]any)
		min := definition["minSize"].(map[string]any)
		if position["width"].(int) < min["width"].(int) || position["height"].(int) < min["height"].(int) {
			return dashboardErrorCode("dashboard_panel_size_invalid", "panel position is smaller than the renderer minimum", "position")
		}
		properties := definition["optionsSchema"].(map[string]any)["properties"].(map[string]any)
		options := panel["options"].(map[string]any)
		unknown := []string{}
		for key := range options {
			if properties[key] == nil {
				unknown = append(unknown, key)
			}
		}
		sort.Strings(unknown)
		if len(unknown) > 0 {
			return dashboardErrorCode("dashboard_panel_options_invalid", "panel options contain fields not declared by the renderer", "options."+unknown[0])
		}
		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := options[key]
			valid := false
			switch key {
			case "text":
				_, valid = value.(string)
			case "fillType":
				valid = value == "solid" || value == "gradient"
			case "drilldownFields":
				if values, ok := value.([]any); ok {
					valid = true
					for _, v := range values {
						if _, ok := v.(string); !ok {
							valid = false
						}
					}
				}
			}
			if !valid {
				return dashboardErrorCode("dashboard_panel_options_invalid", "panel option does not match the renderer schema", "options."+key)
			}
		}
	}
	return nil
}
func (s *DashboardService) Query(ctx context.Context, p DashboardParams) (any, error) {
	if p["panelType"] == "custom" {
		return nil, dashboardErrorCode("dashboard_panel_type_unknown", "panel type cannot execute queries")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s.query == nil {
		return nil, dashboardErrorCode("dashboard_query_invalid", "Dashboard query service is unavailable.")
	}
	q := p["query"].(map[string]any)
	limit := q["limit"].(int)
	var rows []map[string]any
	if q["kind"] == "records" {
		raw, err := json.Marshal(map[string]any{"filters": q["filters"], "sorts": q["sorts"], "offset": 0, "limit": limit})
		if err != nil {
			return nil, err
		}
		var input query.TableQuery
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		page, err := s.query.QueryPage(ctx, q["collection"].(string), input)
		if err != nil {
			return nil, dashboardErrorCode("dashboard_query_invalid", "Dashboard query is invalid.")
		}
		rows = make([]map[string]any, 0, len(page.Rows))
		for _, row := range page.Rows {
			projected := map[string]any{}
			for _, field := range q["fields"].([]any) {
				key := field.(string)
				projected[key] = row[key]
			}
			rows = append(rows, projected)
		}
	} else {
		metrics := []any{}
		for _, v := range q["measures"].([]any) {
			m := v.(map[string]any)
			metrics = append(metrics, map[string]any{"function": m["op"], "field": dashboardTextOr(m["field"], ""), "alias": m["key"]})
		}
		input := map[string]any{"filters": q["filters"], "groupBy": q["dimensions"], "metrics": metrics, "limit": limit}
		if q["timeBucket"] != nil {
			input["timeBucket"] = q["timeBucket"]
		}
		if q["topN"] != nil {
			input["topN"] = q["topN"]
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		var request query.AggregateQuery
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		result, err := s.query.Aggregate(ctx, q["collection"].(string), request)
		if err != nil {
			return nil, dashboardErrorCode("dashboard_query_invalid", "Dashboard query is invalid.")
		}
		rows = result.Rows
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return map[string]any{"rows": rows, "truncated": truncated, "maxPoints": limit}, nil
}
