package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DashboardParams is the normalized, closed Dashboard DTO. Dynamic JSON remains
// only where the public contract permits options, filter values and query rows.
type DashboardParams map[string]any

type dashboardNormalizer func(any) (any, error)
type dashboardField struct {
	normalize dashboardNormalizer
	fallback  any
	optional  bool
}

func requiredDashboard(n dashboardNormalizer) dashboardField       { return dashboardField{normalize: n} }
func defaultDashboard(n dashboardNormalizer, v any) dashboardField { return dashboardField{n, v, true} }
func dashboardInvalidParams() error                                { return errors.New("invalid Dashboard parameters") }
func dashboardText(min, max int) dashboardNormalizer {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok || utf8.RuneCountInString(s) < min || utf8.RuneCountInString(s) > max {
			return nil, dashboardInvalidParams()
		}
		return s, nil
	}
}
func dashboardEnum(values ...string) dashboardNormalizer {
	return func(v any) (any, error) {
		s, ok := v.(string)
		for _, item := range values {
			if ok && s == item {
				return s, nil
			}
		}
		return nil, dashboardInvalidParams()
	}
}

var dashboardIntegerText = regexp.MustCompile(`^[+-]?[0-9](?:_?[0-9])*(?:\.0+)?$`)

func dashboardInt(min, max int) dashboardNormalizer {
	return func(v any) (any, error) {
		var n float64
		var err error
		switch x := v.(type) {
		case json.Number:
			n, err = x.Float64()
		case string:
			text := strings.TrimSpace(x)
			if !dashboardIntegerText.MatchString(text) {
				return nil, dashboardInvalidParams()
			}
			n, err = strconv.ParseFloat(strings.ReplaceAll(text, "_", ""), 64)
		case bool:
			if x {
				n = 1
			}
		case int:
			n = float64(x)
		default:
			err = dashboardInvalidParams()
		}
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(min) || n > float64(max) {
			return nil, dashboardInvalidParams()
		}
		return int(n), nil
	}
}
func dashboardBool(v any) (any, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case json.Number:
		number, err := x.Float64()
		if err != nil {
			return nil, dashboardInvalidParams()
		}
		if number == 0 {
			return false, nil
		}
		if number == 1 {
			return true, nil
		}
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "on", "yes", "y", "t":
			return true, nil
		case "false", "0", "off", "no", "n", "f":
			return false, nil
		}
	}
	return nil, dashboardInvalidParams()
}
func dashboardNullable(n dashboardNormalizer) dashboardNormalizer {
	return func(v any) (any, error) {
		if v == nil {
			return nil, nil
		}
		return n(v)
	}
}
func dashboardList(n dashboardNormalizer, min, max int) dashboardNormalizer {
	return func(v any) (any, error) {
		a, ok := v.([]any)
		if !ok || len(a) < min || len(a) > max {
			return nil, dashboardInvalidParams()
		}
		out := make([]any, len(a))
		for i, x := range a {
			var err error
			out[i], err = n(x)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}
}
func dashboardJSON(v any) (any, error) { return v, nil }
func dashboardMap(v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil, dashboardInvalidParams()
	}
	return m, nil
}
func dashboardObject(fields map[string]dashboardField) dashboardNormalizer {
	return func(v any) (any, error) {
		m, ok := v.(map[string]any)
		if !ok || m == nil {
			return nil, dashboardInvalidParams()
		}
		out := map[string]any{}
		remaining := len(m)
		for key, field := range fields {
			value, present := m[key]
			alias := dashboardSnake(key)
			if !present && alias != key {
				value, present = m[alias]
			}
			if !present {
				if !field.optional {
					return nil, dashboardInvalidParams()
				}
				value = field.fallback
			} else {
				remaining--
			}
			normalized, err := field.normalize(value)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		if remaining != 0 {
			return nil, dashboardInvalidParams()
		}
		return out, nil
	}
}
func dashboardSnake(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('_')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var dashboardUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var dashboardRevisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func dashboardPattern(pattern *regexp.Regexp) dashboardNormalizer {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok || !pattern.MatchString(s) {
			return nil, dashboardInvalidParams()
		}
		return s, nil
	}
}

var dashboardPanelType = dashboardEnum("label", "metric", "metric-list", "list", "time-series", "bar", "line", "donut", "pie", "custom")

func dashboardPosition(v any) (any, error) {
	return dashboardObject(map[string]dashboardField{"x": requiredDashboard(dashboardInt(0, math.MaxInt32)),
		"y":      requiredDashboard(dashboardInt(0, math.MaxInt32)),
		"width":  requiredDashboard(dashboardInt(1, math.MaxInt32)),
		"height": requiredDashboard(dashboardInt(1, math.MaxInt32))})(v)
}
func dashboardConfig(v any) (any, error) {
	text := dashboardText(1, 128)
	ids := dashboardList(text, 0, 64)
	filter := dashboardObject(map[string]dashboardField{
		"key": requiredDashboard(dashboardText(1, 64)),
		"label": defaultDashboard(dashboardText(0, 128),
			""),
		"type": requiredDashboard(dashboardEnum("date-range",
			"enum",
			"user",
			"relation",
			"number-range")),
		"defaultValue":  defaultDashboard(dashboardJSON, nil),
		"allowedFields": defaultDashboard(dashboardList(dashboardText(0, 1<<20), 0, 32), []any{}),
		"targetPanels":  defaultDashboard(ids, []any{}),
		"fieldBindings": defaultDashboard(func(v any) (any, error) {
			m, err := dashboardMap(v)
			if err != nil {
				return nil, err
			}
			if len(m.(map[string]any)) > 64 {
				return nil, dashboardInvalidParams()
			}
			for _, value := range m.(map[string]any) {
				if _, err := dashboardText(0, 1<<20)(value); err != nil {
					return nil, err
				}
			}
			return m, nil
		}, map[string]any{}),
	})
	interaction := dashboardObject(map[string]dashboardField{"sourcePanelId": requiredDashboard(text),
		"sourceField":    defaultDashboard(dashboardNullable(text), nil),
		"targetPanelIds": requiredDashboard(dashboardList(text, 1, 64)),
		"targetField":    requiredDashboard(text)})
	out, err := dashboardObject(map[string]dashboardField{"configVersion": defaultDashboard(dashboardInt(1, math.MaxInt32), 1),
		"globalFilters":   defaultDashboard(dashboardList(filter, 0, 32), []any{}),
		"interactions":    defaultDashboard(dashboardList(interaction, 0, 128), []any{}),
		"refreshInterval": defaultDashboard(dashboardInt(0, 900), 0)})(v)
	if err != nil {
		return nil, err
	}
	n := out.(map[string]any)["refreshInterval"].(int)
	if n != 0 && n != 30 && n != 60 && n != 300 && n != 900 {
		return nil, dashboardInvalidParams()
	}
	return out, nil
}
func dashboardQuery(v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, dashboardInvalidParams()
	}
	filter := dashboardObject(map[string]dashboardField{"field": requiredDashboard(dashboardText(1, 128)),
		"operator": requiredDashboard(dashboardEnum("contains",
			"eq",
			"ne",
			"starts_with",
			"ends_with",
			"gt",
			"lt",
			"gte",
			"lte",
			"between",
			"in",
			"is_null",
			"is_not_null",
			"regex")),
		"value": defaultDashboard(dashboardJSON, nil),
		"logic": defaultDashboard(dashboardEnum("AND",
			"OR"),
			"AND")})
	common := map[string]dashboardField{"kind": requiredDashboard(dashboardEnum("records",
		"aggregate")),
		"collection": requiredDashboard(dashboardText(1, 128)),
		"filters":    defaultDashboard(dashboardList(filter, 0, 64), []any{})}
	if m["kind"] == "records" {
		common["fields"] = requiredDashboard(dashboardList(dashboardText(0, 1<<20), 1, 20))
		common["sorts"] = defaultDashboard(dashboardList(dashboardObject(map[string]dashboardField{"field": requiredDashboard(dashboardText(1, 128)),
			"direction": defaultDashboard(dashboardEnum("asc",
				"desc"),
				"asc"),
			"nullsLast": defaultDashboard(dashboardBool, true)}), 0, 16), []any{})
		common["limit"] = defaultDashboard(dashboardInt(1, 100), 20)
	} else if m["kind"] == "aggregate" {
		measure := func(v any) (any, error) {
			out, err := dashboardObject(map[string]dashboardField{"key": requiredDashboard(dashboardPattern(regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`))),
				"op": requiredDashboard(dashboardEnum("count",
					"countDistinct",
					"sum",
					"avg",
					"min",
					"max")),
				"field": defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil)})(v)
			if err != nil {
				return nil, err
			}
			a := out.(map[string]any)
			if a["op"] != "count" && (a["field"] == nil || a["field"] == "") {
				return nil, dashboardInvalidParams()
			}
			return out, nil
		}
		common["dimensions"] = defaultDashboard(dashboardList(dashboardText(0, 1<<20), 0, 2), []any{})
		common["measures"] = requiredDashboard(dashboardList(measure, 1, 8))
		common["timeBucket"] = defaultDashboard(dashboardNullable(dashboardObject(map[string]dashboardField{"field": requiredDashboard(dashboardText(1, 128)),
			"unit": requiredDashboard(dashboardEnum("day",
				"week",
				"month")),
			"timezone": defaultDashboard(dashboardText(1, 64),
				"UTC")})), nil)
		common["limit"] = defaultDashboard(dashboardInt(1, 100000), 100)
		common["topN"] = defaultDashboard(dashboardNullable(dashboardInt(1, 5000)), nil)
	} else {
		return nil, dashboardInvalidParams()
	}
	out, err := dashboardObject(common)(v)
	if err != nil {
		return nil, err
	}
	if m["kind"] == "aggregate" {
		seen := map[string]bool{}
		for _, item := range out.(map[string]any)["measures"].([]any) {
			key := item.(map[string]any)["key"].(string)
			if seen[key] {
				return nil, dashboardInvalidParams()
			}
			seen[key] = true
		}
	}
	return out, nil
}
func dashboardPanelDraft(v any) (any, error) {
	out, err := dashboardObject(map[string]dashboardField{"clientId": requiredDashboard(dashboardText(1, 128)),
		"panelId": defaultDashboard(dashboardNullable(dashboardPattern(dashboardUUIDPattern)), nil),
		"name": defaultDashboard(dashboardText(0, 255),
			""),
		"note":       defaultDashboard(dashboardNullable(dashboardText(0, 2048)), nil),
		"icon":       defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil),
		"color":      defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil),
		"showHeader": defaultDashboard(dashboardBool, true),
		"type": defaultDashboard(dashboardPanelType,
			"metric"),
		"position": defaultDashboard(dashboardPosition, map[string]any{"x": 0,
			"y":      0,
			"width":  4,
			"height": 4}),
		"options": defaultDashboard(dashboardMap, map[string]any{}),
		"query":   defaultDashboard(dashboardNullable(dashboardQuery), nil)})(v)
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	p := m["position"].(map[string]any)
	if p["x"].(int)+p["width"].(int) > 12 || p["y"].(int) > 100000 || p["height"].(int) > 1000 {
		return nil, dashboardInvalidParams()
	}
	raw, err := dashboardCanonicalJSON(m["options"])
	if err != nil || len(raw) > 65536 {
		return nil, dashboardInvalidParams()
	}
	return out, nil
}

func DecodeDashboardParams(method string, raw json.RawMessage) (DashboardParams, error) {
	if !utf8.Valid(raw) || !json.Valid(raw) || len(raw) > 1<<20 {
		return nil, dashboardInvalidParams()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, dashboardInvalidParams()
	}
	var normalize dashboardNormalizer
	switch method {
	case "insights.listDashboards", "insights.dashboardQueryLimits", "insights.panelManifest":
		normalize = dashboardObject(map[string]dashboardField{})
	case "insights.readDashboardWorkspace", "insights.deleteDashboardWorkspace":
		normalize = dashboardObject(map[string]dashboardField{"dashboardId": requiredDashboard(dashboardPattern(dashboardUUIDPattern))})
	case "insights.executeDashboardQuery":
		normalize = dashboardObject(map[string]dashboardField{"panelType": requiredDashboard(dashboardPanelType),
			"query":     requiredDashboard(dashboardQuery),
			"requestId": defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil)})
	case "insights.saveDashboardDraft":
		normalize = dashboardObject(map[string]dashboardField{"dashboardId": defaultDashboard(dashboardNullable(dashboardPattern(dashboardUUIDPattern)), nil),
			"expectedRevision": defaultDashboard(dashboardNullable(dashboardPattern(dashboardRevisionPattern)), nil),
			"idempotencyKey":   requiredDashboard(dashboardPattern(dashboardUUIDPattern)),
			"name":             requiredDashboard(dashboardText(1, 255)),
			"note": defaultDashboard(dashboardText(0, 2048),
				""),
			"icon":            defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil),
			"color":           defaultDashboard(dashboardNullable(dashboardText(0, 128)), nil),
			"panels":          defaultDashboard(dashboardList(dashboardPanelDraft, 0, 100), []any{}),
			"deletedPanelIds": defaultDashboard(dashboardList(dashboardPattern(dashboardUUIDPattern), 0, 100), []any{}),
			"config":          defaultDashboard(dashboardConfig, map[string]any{})})
	default:
		return nil, dashboardInvalidParams()
	}
	out, err := normalize(value)
	if err != nil {
		return nil, err
	}
	params := DashboardParams(out.(map[string]any))
	if method == "insights.saveDashboardDraft" {
		clients, ids := map[string]bool{}, map[string]bool{}
		for _, item := range params["panels"].([]any) {
			p := item.(map[string]any)
			id := p["clientId"].(string)
			if clients[id] {
				return nil, dashboardInvalidParams()
			}
			clients[id] = true
			if p["panelId"] != nil {
				id = p["panelId"].(string)
				if ids[id] {
					return nil, dashboardInvalidParams()
				}
				ids[id] = true
			}
		}
		for _, item := range params["deletedPanelIds"].([]any) {
			id := item.(string)
			if ids[id] {
				return nil, dashboardInvalidParams()
			}
			ids[id] = true
		}
		raw, err := dashboardCanonicalJSON(params["config"])
		if err != nil || len(raw) > 262144 {
			return nil, dashboardInvalidParams()
		}
	}
	return params, nil
}
