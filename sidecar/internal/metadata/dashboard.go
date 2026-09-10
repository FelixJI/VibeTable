package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type DashboardError struct{ Code, Message, Field string }

func (e *DashboardError) Error() string { return e.Code + ": " + e.Message }
func dashboardErrorCode(code, message string, field ...string) *DashboardError {
	e := &DashboardError{Code: code, Message: message}
	if len(field) > 0 {
		e.Field = field[0]
	}
	return e
}
func dashboardPersistence(err error) error {
	if err == nil || err == writecoordinator.ErrBusinessReplay {
		return err
	}
	var domain *DashboardError
	if errors.As(err, &domain) {
		return domain
	}
	if IsError(err, "metadata.revision_conflict") {
		return dashboardErrorCode("dashboard_edit_conflict", "dashboard revision does not match")
	}
	if IsError(err, "metadata.idempotency_conflict") {
		return dashboardErrorCode("dashboard_idempotency_conflict", "Dashboard request identity was used for another request.", "idempotencyKey")
	}
	return dashboardErrorCode("dashboard_persistence_failed", "Dashboard persistence failed.")
}

type DashboardQueryPort interface {
	QueryPage(context.Context, string, query.TableQuery) (query.Page, error)
	Aggregate(context.Context, string, query.AggregateQuery) (query.AggregateResult, error)
}
type DashboardService struct {
	metadata *Service
	query    DashboardQueryPort
	slots    chan struct{}
	newUUID  func() string
}

func NewDashboard(app core.App, port DashboardQueryPort) *DashboardService {
	return &DashboardService{metadata: New(app), query: port, slots: make(chan struct{}, 6), newUUID: func() string { return uuid.NewString() }}
}
func dashboardLimits() map[string]any {
	return map[string]any{"maxConcurrentRequests": 6,
		"maxSeriesPoints":   50000,
		"maxPanelPoints":    100000,
		"maxCategoryPoints": 5000,
		"defaultTopN":       100,
		"maxPieSlices":      50,
		"maxListRows":       100}
}
func dashboardTextOr(v any, fallback string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fallback
}
func dashboardOptionalText(v any) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return nil
}
func dashboardJSONMap(raw json.RawMessage) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, dashboardErrorCode("dashboard_storage_invalid", "Stored dashboard is invalid.")
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&m) != nil || m == nil {
		return nil, dashboardErrorCode("dashboard_storage_invalid", "Stored dashboard is invalid.")
	}
	return m, nil
}
func dashboardRows(ctx context.Context, app core.App, ns Namespace) ([]map[string]any, error) {
	items, err := New(app).List(ctx, ns)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, err := dashboardJSONMap(item.Payload)
		if err != nil {
			return nil, err
		}
		m["id"] = item.LogicalID
		m["revision"] = item.Revision
		rows = append(rows, m)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a := dashboardTextOr(rows[i]["key"], "")
		if a == "" {
			a = rows[i]["id"].(string)
		}
		b := dashboardTextOr(rows[j]["key"], "")
		if b == "" {
			b = rows[j]["id"].(string)
		}
		return a < b
	})
	return rows, nil
}
func dashboardPanel(row map[string]any, parent string) (map[string]any, error) {
	kind := dashboardTextOr(row["type"], "custom")
	if kind == "custom" {
		kind = "custom"
	} else if _, err := dashboardPanelType(kind); err != nil {
		kind = "custom"
	}
	position, ok := row["position"].(map[string]any)
	if !ok {
		position = map[string]any{"x": 0, "y": 0, "width": 4, "height": 4}
	}
	normalized, err := dashboardPosition(position)
	if err != nil {
		return nil, dashboardErrorCode("dashboard_storage_invalid", "Stored dashboard is invalid.")
	}
	options, ok := row["options"].(map[string]any)
	if !ok {
		options = map[string]any{}
	}
	q, ok := row["query"].(map[string]any)
	if !ok {
		q = map[string]any{}
	}
	return map[string]any{"id": dashboardTextOr(row["id"],
		""),
		"dashboardId": parent,
		"name": dashboardTextOr(row["name"],
			""),
		"note": dashboardTextOr(row["note"],
			""),
		"icon":       dashboardOptionalText(row["icon"]),
		"color":      dashboardOptionalText(row["color"]),
		"showHeader": row["showHeader"] != false,
		"type":       kind,
		"position":   normalized,
		"options":    options,
		"query":      q}, nil
}
func dashboardEntry(row map[string]any, panels []map[string]any) (map[string]any, error) {
	id := row["id"].(string)
	out := []any{}
	for _, panel := range panels {
		if panel["dashboardId"] != id {
			continue
		}
		p, err := dashboardPanel(panel, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return map[string]any{"id": id,
		"name": dashboardTextOr(row["name"],
			""),
		"note": dashboardTextOr(row["note"],
			""),
		"icon":   dashboardOptionalText(row["icon"]),
		"color":  dashboardOptionalText(row["color"]),
		"panels": out}, nil
}
func dashboardWorkspace(row map[string]any, panels []map[string]any) (map[string]any, error) {
	entry, err := dashboardEntry(row, panels)
	if err != nil {
		return nil, err
	}
	raw := row["config"]
	if raw == nil {
		raw = map[string]any{}
	}
	config, err := dashboardConfig(raw)
	if err != nil {
		return nil, dashboardErrorCode("dashboard_storage_invalid", "Stored dashboard is invalid.")
	}
	revision, err := dashboardRevision(map[string]any{"dashboard": entry, "config": config})
	if err != nil {
		return nil, err
	}
	return map[string]any{"dashboard": entry, "config": config, "revision": revision, "atomicSaveEndpoint": "vibetable-dashboard-atomic.v1", "queryLimits": dashboardLimits()}, nil
}
func (s *DashboardService) Read(ctx context.Context, id string) (any, error) {
	var result any
	err := s.metadata.app.RunInTransaction(func(tx core.App) error {
		rows, err := dashboardRows(ctx, tx, NamespaceDashboards)
		if err != nil {
			return err
		}
		panels, err := dashboardRows(ctx, tx, NamespacePanels)
		if err != nil {
			return err
		}
		if id == "" {
			entries := []any{}
			for _, row := range rows {
				entry, err := dashboardEntry(row, panels)
				if err != nil {
					return err
				}
				entries = append(entries, entry)
			}
			result = map[string]any{"dashboards": entries}
			return nil
		}
		for _, row := range rows {
			if row["id"] == id {
				result, err = dashboardWorkspace(row, panels)
				return err
			}
		}
		return dashboardErrorCode("dashboard_not_found", "dashboard was not found")
	})
	return result, dashboardPersistence(err)
}
func dashboardRemap(config map[string]any, ids map[string]any, desired map[string]bool) error {
	resolve := func(value any) (string, error) {
		id := value.(string)
		if v, ok := ids[id]; ok {
			id = v.(string)
		}
		if !desired[id] {
			return "", dashboardErrorCode("dashboard_panel_membership_invalid", "Dashboard config references an unknown panel.")
		}
		return id, nil
	}
	remapList := func(values []any) error {
		for i, v := range values {
			id, err := resolve(v)
			if err != nil {
				return err
			}
			values[i] = id
		}
		return nil
	}
	for _, v := range config["globalFilters"].([]any) {
		f := v.(map[string]any)
		if err := remapList(f["targetPanels"].([]any)); err != nil {
			return err
		}
		bindings := map[string]any{}
		for k, v := range f["fieldBindings"].(map[string]any) {
			id, err := resolve(k)
			if err != nil {
				return err
			}
			bindings[id] = v
		}
		f["fieldBindings"] = bindings
	}
	for _, v := range config["interactions"].([]any) {
		i := v.(map[string]any)
		id, err := resolve(i["sourcePanelId"])
		if err != nil {
			return err
		}
		i["sourcePanelId"] = id
		if err := remapList(i["targetPanelIds"].([]any)); err != nil {
			return err
		}
	}
	return nil
}
func (s *DashboardService) Save(ctx context.Context, p DashboardParams) (map[string]any, error) {
	key := p["idempotencyKey"].(string)
	digest, err := hashValue(map[string]any{"operation": "dashboard.save", "params": p})
	if err != nil {
		return nil, err
	}
	result, err := executeIdempotent(s.metadata, ctx, "dashboard:save:"+key, digest, func(tx core.App) (map[string]any, []metadataChange, error) {
		if err := validateDashboardPanels(p); err != nil {
			return nil, nil, err
		}
		rows, err := dashboardRows(ctx, tx, NamespaceDashboards)
		if err != nil {
			return nil, nil, err
		}
		allPanels, err := dashboardRows(ctx, tx, NamespacePanels)
		if err != nil {
			return nil, nil, err
		}
		id := dashboardTextOr(p["dashboardId"], "")
		if id == "" {
			id = s.newUUID()
		}
		var current map[string]any
		for _, row := range rows {
			if row["id"] == id {
				current = row
			}
		}
		if current == nil {
			if p["expectedRevision"] != nil {
				return nil, nil, dashboardErrorCode("dashboard_edit_conflict", "dashboard revision does not match")
			}
		} else {
			workspace, err := dashboardWorkspace(current, allPanels)
			if err != nil {
				return nil, nil, err
			}
			if p["expectedRevision"] != workspace["revision"] {
				return nil, nil, dashboardErrorCode("dashboard_edit_conflict", "dashboard revision does not match")
			}
		}
		existing := map[string]map[string]any{}
		for _, panel := range allPanels {
			if panel["dashboardId"] == id {
				existing[panel["id"].(string)] = panel
			}
		}
		desired := map[string]bool{}
		mapping := map[string]any{}
		panels := []map[string]any{}
		changes := []metadataChange{}
		for _, v := range p["panels"].([]any) {
			draft := v.(map[string]any)
			panelID := dashboardTextOr(draft["panelId"], "")
			if panelID == "" {
				panelID = s.newUUID()
			}
			desired[panelID] = true
			mapping[draft["clientId"].(string)] = panelID
			row := map[string]any{}
			for _, field := range []string{"name", "note", "icon", "color", "showHeader", "type", "position", "options", "query"} {
				row[field] = draft[field]
			}
			row["id"] = panelID
			row["dashboardId"] = id
			if row["note"] == nil {
				row["note"] = ""
			}
			if row["query"] == nil {
				row["query"] = map[string]any{}
			}
			payload, err := json.Marshal(row)
			if err != nil {
				return nil, nil, err
			}
			revision := ""
			if old := existing[panelID]; old != nil {
				revision = old["revision"].(string)
			}
			_, change, err := s.metadata.upsert(tx, NamespacePanels, ItemMutation{LogicalID: panelID, Payload: payload, ExpectedRevision: revision}, "panels", id)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, change)
			panels = append(panels, row)
		}
		deleted := map[string]bool{}
		for _, v := range p["deletedPanelIds"].([]any) {
			panelID := v.(string)
			old := existing[panelID]
			if old == nil {
				return nil, nil, dashboardErrorCode("dashboard_panel_membership_invalid", "Deleted panel was not found.")
			}
			change, err := s.metadata.delete(tx, NamespacePanels, ItemDelete{LogicalID: panelID, ExpectedRevision: old["revision"].(string)}, "deletedPanelIds", id)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, change)
			deleted[panelID] = true
		}
		// A full draft cannot silently retain panels absent from the returned workspace.
		for panelID := range existing {
			if !desired[panelID] && !deleted[panelID] {
				return nil, nil, dashboardErrorCode("dashboard_panel_membership_invalid", "Every existing panel must be present or explicitly deleted.")
			}
		}
		normalizedConfig, err := dashboardConfig(p["config"])
		if err != nil {
			return nil, nil, err
		}
		config := normalizedConfig.(map[string]any)
		if err := dashboardRemap(config, mapping, desired); err != nil {
			return nil, nil, err
		}
		row := map[string]any{"id": id, "name": p["name"], "note": p["note"], "icon": p["icon"], "color": p["color"], "config": config}
		payload, err := json.Marshal(row)
		if err != nil {
			return nil, nil, err
		}
		revision := ""
		if current != nil {
			revision = current["revision"].(string)
		}
		_, change, err := s.metadata.upsert(tx, NamespaceDashboards, ItemMutation{LogicalID: id, Payload: payload, ExpectedRevision: revision}, "dashboard", "")
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, change)
		sort.Slice(panels, func(i, j int) bool { return panels[i]["id"].(string) < panels[j]["id"].(string) })
		workspace, err := dashboardWorkspace(row, panels)
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"workspace": workspace, "clientPanelIds": mapping, "atomic": true}, changes, nil
	}, func(*map[string]any, string, []string) {}, func(*map[string]any) {}, func() error { return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.dashboards.upsert", key) })
	return result, dashboardPersistence(err)
}
func (s *DashboardService) Delete(ctx context.Context, id, key string) (map[string]any, error) {
	digest, err := hashValue(map[string]any{"operation": "dashboard.delete", "dashboardId": id})
	if err != nil {
		return nil, err
	}
	result, err := executeIdempotent(s.metadata, ctx, "dashboard:delete:"+key, digest, func(tx core.App) (map[string]any, []metadataChange, error) {
		rows, err := dashboardRows(ctx, tx, NamespaceDashboards)
		if err != nil {
			return nil, nil, err
		}
		var row map[string]any
		for _, v := range rows {
			if v["id"] == id {
				row = v
			}
		}
		if row == nil {
			return nil, nil, dashboardErrorCode("dashboard_not_found", "dashboard was not found")
		}
		panels, err := dashboardRows(ctx, tx, NamespacePanels)
		if err != nil {
			return nil, nil, err
		}
		changes := []metadataChange{}
		for _, p := range panels {
			if p["dashboardId"] != id {
				continue
			}
			c, err := s.metadata.delete(tx, NamespacePanels, ItemDelete{LogicalID: p["id"].(string), ExpectedRevision: p["revision"].(string)}, "panels", id)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, c)
		}
		c, err := s.metadata.delete(tx, NamespaceDashboards, ItemDelete{LogicalID: id, ExpectedRevision: row["revision"].(string)}, "dashboard", "")
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, c)
		return map[string]any{"deleted": id}, changes, nil
	}, func(*map[string]any, string, []string) {}, func(*map[string]any) {}, func() error { return writecoordinator.ReplayedBusinessWrite(ctx, "metadata.dashboards.delete", key) })
	return result, dashboardPersistence(err)
}
