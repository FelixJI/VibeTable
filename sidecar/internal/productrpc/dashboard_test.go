package productrpc

import (
	"errors"
	"reflect"
	"testing"
)

func TestDashboardErrorProjectionIsClosedToItsOwnerAndCodes(t *testing.T) {
	domain := &DashboardError{Code: "dashboard_panel_options_invalid", Message: "panel option does not match the renderer schema", Field: "options.fillType"}
	actual, ok := dashboardErrorData("insights.saveDashboardDraft", domain)
	expected := map[string]any{"kind": "insights_error", "code": domain.Code, "message": domain.Message, "field": "options.fillType"}
	if !ok || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("projection: %#v %t", actual, ok)
	}
	for _, test := range []struct {
		method string
		err    error
	}{
		{"preset.save", domain}, {"insights.private", domain}, {"insights.saveDashboardDraft", errors.New("private storage detail")},
		{"insights.saveDashboardDraft", &DashboardError{Code: "private_code", Message: "private detail"}},
		{"insights.saveDashboardDraft", &DashboardError{Code: "dashboard_edit_conflict"}},
	} {
		if data, ok := dashboardErrorData(test.method, test.err); ok || data != nil {
			t.Fatalf("exposed unclassified error: %#v", data)
		}
	}
}
