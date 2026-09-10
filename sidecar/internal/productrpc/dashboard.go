package productrpc

import "errors"

const CodeDashboard = -32080

type DashboardError struct{ Code, Message, Field string }

func (e *DashboardError) Error() string { return e.Message }
func dashboardErrorData(method string, err error) (map[string]any, bool) {
	switch method {
	case "insights.listDashboards", "insights.readDashboardWorkspace", "insights.saveDashboardDraft", "insights.deleteDashboardWorkspace", "insights.executeDashboardQuery", "insights.dashboardQueryLimits", "insights.panelManifest":
	default:
		return nil, false
	}
	var domain *DashboardError
	if !errors.As(err, &domain) || domain == nil || domain.Message == "" {
		return nil, false
	}
	switch domain.Code {
	case "dashboard_not_found", "dashboard_edit_conflict", "dashboard_panel_type_unknown", "dashboard_panel_size_invalid", "dashboard_panel_options_invalid", "dashboard_manifest_invalid", "dashboard_panel_membership_invalid", "dashboard_storage_invalid", "dashboard_persistence_failed", "dashboard_idempotency_conflict", "dashboard_query_invalid":
	default:
		return nil, false
	}
	data := map[string]any{"kind": "insights_error", "message": domain.Message, "code": domain.Code}
	if domain.Field != "" {
		data["field"] = domain.Field
	}
	return data, true
}
