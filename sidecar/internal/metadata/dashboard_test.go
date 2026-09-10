package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type dashboardOracleQuery struct{}

func (dashboardOracleQuery) QueryPage(context.Context, string, query.TableQuery) (query.Page, error) {
	return query.Page{Rows: []map[string]any{{"title": "A", "private": 1}, {"title": "B", "private": 2}}}, nil
}
func (dashboardOracleQuery) Aggregate(context.Context, string, query.AggregateQuery) (query.AggregateResult, error) {
	return query.AggregateResult{Rows: []map[string]any{{"total": 2}}}, nil
}
func dashboardTestStore(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	pb := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	migrations.Register(pb)
	if err := pb.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pb.OnTerminate().Trigger(&core.TerminateEvent{App: pb}, func(e *core.TerminateEvent) error { return e.App.ResetBootstrapState() }); err != nil {
			t.Error(err)
		}
	})
	if err := pb.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return pb
}
func TestDashboardFrozenPythonOracle(t *testing.T) {
	var corpus struct {
		Producer string `json:"producer"`
		Cases    []struct {
			Name, Method string
			Params       json.RawMessage
			Initial      map[string][]Item
			Response     map[string]any
		}
	}
	raw, err := os.ReadFile("../../../contracts/v2/dashboard-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &corpus) != nil || corpus.Producer != "a3ca78b9181a529d978f9fba46586fbb924ecada" || len(corpus.Cases) != 49 {
		t.Fatal("invalid original producer corpus")
	}
	for _, sample := range corpus.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			params, decodeErr := DecodeDashboardParams(sample.Method, sample.Params)
			expectedError, _ := sample.Response["error"].(map[string]any)
			if expectedError != nil && (expectedError["code"] == float64(-32602) || expectedError["code"] == float64(-32600)) {
				if decodeErr == nil {
					t.Fatal("accepted DTO rejected by Python")
				}
				return
			}
			if decodeErr != nil {
				t.Fatalf("rejected Python DTO: %v", decodeErr)
			}
			pb := dashboardTestStore(t)
			for ns, items := range sample.Initial {
				collection, err := pb.FindCollectionByNameOrId(collectionByNamespace[Namespace(ns)])
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					r := core.NewRecord(collection)
					r.Set("logical_id", item.LogicalID)
					r.Set("payload_json", item.Payload)
					if ns == "panels" {
						m, _ := dashboardJSONMap(item.Payload)
						r.Set("parent_id", m["dashboardId"])
					}
					if err := pb.Save(r); err != nil {
						t.Fatal(err)
					}
				}
			}
			service := NewDashboard(pb, dashboardOracleQuery{})
			ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
			service.newUUID = func() string { id := ids[0]; ids = ids[1:]; return id }
			var result any
			var callErr error
			switch sample.Method {
			case "insights.listDashboards":
				result, callErr = service.Read(context.Background(), "")
			case "insights.readDashboardWorkspace":
				result, callErr = service.Read(context.Background(), params["dashboardId"].(string))
			case "insights.saveDashboardDraft":
				result, callErr = service.Save(context.Background(), params)
			case "insights.deleteDashboardWorkspace":
				result, callErr = service.Delete(context.Background(), params["dashboardId"].(string), "delete-oracle")
			case "insights.executeDashboardQuery":
				result, callErr = service.Query(context.Background(), params)
			case "insights.panelManifest":
				result = DashboardManifest()
			case "insights.dashboardQueryLimits":
				result = DashboardQueryLimits()
			}
			if sample.Name == "draft-binding-missing" {
				var domain *DashboardError
				if !errors.As(callErr, &domain) || domain.Code != "dashboard_panel_membership_invalid" {
					t.Fatalf("unknown panel reference must be rejected explicitly: %v", callErr)
				}
				return
			}
			if expectedError != nil {
				var domain *DashboardError
				if !errors.As(callErr, &domain) {
					t.Fatalf("missing Python error: %v", callErr)
				}
				data := expectedError["data"].(map[string]any)
				if domain.Code != data["code"] || domain.Message != data["message"] {
					t.Fatalf("error differs: %#v want %#v", domain, data)
				}
				return
			}
			if callErr != nil {
				t.Fatal(callErr)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var normalized any
			if json.Unmarshal(encoded, &normalized) != nil {
				t.Fatal("invalid result")
			}
			if !reflect.DeepEqual(normalized, sample.Response["result"]) {
				t.Fatalf("projection differs\nGo: %s\nPython: %#v", encoded, sample.Response["result"])
			}
		})
	}
}

func TestDashboardScalarCompatibilityAndStoredJSON(t *testing.T) {
	for _, value := range []string{"3e0", "3.", "3.00_0"} {
		if _, err := dashboardInt(1, 100)(value); err == nil {
			t.Errorf("accepted invalid integer %q", value)
		}
	}
	for value, expected := range map[string]int{"3.0": 3, "+03": 3, "1_0": 10, " 3 ": 3} {
		actual, err := dashboardInt(1, 100)(value)
		if err != nil || actual != expected {
			t.Errorf("integer %q: %v %v", value, actual, err)
		}
	}
	for _, raw := range []string{`{"name":"valid"} {"name":"trailing"}`, `{"name":"valid"},"revision":"injected"`, `null`, `[]`} {
		_, err := dashboardJSONMap(json.RawMessage(raw))
		var domain *DashboardError
		if !errors.As(err, &domain) || domain.Code != "dashboard_storage_invalid" {
			t.Errorf("accepted invalid stored JSON %q: %v", raw, err)
		}
	}
	actual, err := dashboardJSONMap(json.RawMessage(`{"name":"中文\"\\","zero":0,"false":false}`))
	if err != nil || actual["name"] != "中文\"\\" || actual["zero"] != json.Number("0") || actual["false"] != false {
		t.Fatalf("stored JSON changed: %#v %v", actual, err)
	}
}

func TestDashboardJSONPreservesPythonNumericAndTextProjection(t *testing.T) {
	raw, err := dashboardCanonicalJSON(map[string]any{"negativeZero": json.Number("-0"), "float": json.Number("1.0"), "small": json.Number("1e-5"), "text": "<中文>\u2028\\\"", "false": false})
	if err != nil || string(raw) != `{"false":false,"float":1.0,"negativeZero":0,"small":1e-05,"text":"<中文> \\\""}` {
		t.Fatalf("canonical JSON: %s %v", raw, err)
	}
	for _, number := range []json.Number{"1.0", "1e0"} {
		value, err := dashboardBool(number)
		if err != nil || value != true {
			t.Fatalf("boolean coercion: %v %v", value, err)
		}
	}
}
