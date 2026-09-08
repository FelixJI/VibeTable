package integration_test

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/query"
)

func TestQueryRelationDisplayLabelsPreserveIDsAndLimitVisibleTargets(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	for _, sql := range []string{
		`CREATE TABLE label_rows (id TEXT PRIMARY KEY, title TEXT, owner TEXT, watchers JSON)`,
		`INSERT INTO label_rows VALUES ('a','Alpha','b','["b","c","missing","a"]'), ('b','Beta','a','[]'), ('c','','','[]')`,
	} {
		if _, err := app.DB().NewQuery(sql).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	var relation query.RelationDescriptor
	if err := json.Unmarshal([]byte(`{"tableName":"label_rows","primaryKey":"id","displayField":"title","fields":{"title":{"physicalName":"title","type":"text"}}}`), &relation); err != nil {
		t.Fatal(err)
	}
	source := &staticQuerySource{descriptor: query.TableDescriptor{
		DatabaseID: "local", TableID: "labels", PhysicalName: "label_rows", PrimaryKey: "id", SchemaRevision: "schema-1", DataRevision: 1,
		Fields: map[string]query.FieldDescriptor{
			"id":       {PhysicalName: "id", Type: query.FieldTypeText},
			"title":    {PhysicalName: "title", Type: query.FieldTypeText},
			"owner":    {PhysicalName: "owner", Type: query.FieldTypeRelation, Relation: &relation},
			"watchers": {PhysicalName: "watchers", Type: query.FieldTypeMultiRelation, Relation: &relation},
		},
	}}
	port := query.NewPort(app, source)
	page, err := port.QueryPage(context.Background(), "labels", query.TableQuery{Filters: []query.FilterExpression{{Field: "owner", Operator: query.OperatorEqual, Value: "b"}}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("ID filter rows = %#v", page.Rows)
	}
	row := page.Rows[0]
	if row["owner"] != "b" || !reflect.DeepEqual(row["watchers"], []any{"b", "c", "missing", "a"}) {
		t.Fatalf("relation IDs changed: %#v", row)
	}
	want := map[string]map[string]string{"owner": {"b": "Beta"}, "watchers": {"b": "Beta"}}
	if !reflect.DeepEqual(row["__vibetableRelationLabels"], want) {
		t.Fatalf("labels = %#v; want %#v", row["__vibetableRelationLabels"], want)
	}
	rows, err := port.ReadRows(context.Background(), "labels", []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0]["__vibetableRelationLabels"]; !reflect.DeepEqual(got, map[string]map[string]string{"owner": {"a": "Alpha"}, "watchers": {}}) {
		t.Fatalf("self relation read labels = %#v", got)
	}
}

func TestQueryRelationDisplayLabelsBatchOnlyVisibleIDs(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	for _, sql := range []string{
		`CREATE TABLE batch_targets(id TEXT PRIMARY KEY,title TEXT)`,
		`CREATE TABLE batch_rows(id TEXT PRIMARY KEY, watchers JSON, duplicate JSON)`,
		`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<900) INSERT INTO batch_targets SELECT printf('t%04d',x),printf('Label %d',x) FROM n`,
	} {
		if _, err := app.DB().NewQuery(sql).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		ids := []string{fmt.Sprintf("t%04d", i*3+1), fmt.Sprintf("t%04d", i*3+2), fmt.Sprintf("t%04d", i*3+3)}
		for j := 301; j <= 900; j++ {
			ids = append(ids, fmt.Sprintf("t%04d", j))
		}
		raw, _ := json.Marshal(ids)
		if _, err := app.DB().NewQuery(`INSERT INTO batch_rows VALUES ({:id},{:ids},{:ids})`).Bind(dbx.Params{"id": fmt.Sprintf("r%03d", i), "ids": string(raw)}).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	relation := &query.RelationDescriptor{TableName: "batch_targets", PrimaryKey: "id", DisplayField: "title", Fields: map[string]query.FieldDescriptor{"title": {PhysicalName: "title", Type: query.FieldTypeText}}}
	source := &staticQuerySource{descriptor: query.TableDescriptor{DatabaseID: "local", TableID: "batch", PhysicalName: "batch_rows", PrimaryKey: "id", SchemaRevision: "s", Fields: map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText}, "watchers": {PhysicalName: "watchers", Type: query.FieldTypeMultiRelation, Relation: relation}, "duplicate": {PhysicalName: "duplicate", Type: query.FieldTypeMultiRelation, Relation: relation},
	}}}
	var statements []string
	db := app.NonconcurrentDB().(*dbx.DB)
	previous := db.QueryLogFunc
	db.QueryLogFunc = func(_ context.Context, _ time.Duration, sql string, _ *stdsql.Rows, _ error) {
		if strings.Contains(sql, `FROM "batch_targets"`) {
			statements = append(statements, sql)
		}
	}
	defer func() { db.QueryLogFunc = previous }()
	page, err := query.NewPort(app, source).QueryPage(context.Background(), "batch", query.TableQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 {
		t.Fatalf("target reads=%d, want 2 for 300 distinct visible IDs shared by two fields", len(statements))
	}
	for _, sql := range statements {
		if strings.Contains(sql, "t0301") || strings.Contains(sql, "t0900") {
			t.Fatalf("loaded hidden targets: %s", sql)
		}
	}
	for _, row := range page.Rows {
		labels := row[query.RelationLabelsField].(map[string]map[string]string)
		if len(labels["watchers"]) != 3 || len(labels["duplicate"]) != 3 {
			t.Fatalf("visible labels=%#v", labels)
		}
	}
}

func TestQueryRelationDisplayLabelsRespectPresenceComputedFreshnessAndScalarFallback(t *testing.T) {
	app := newBootstrappedApp(t)
	defer resetApp(t, app)
	for _, sql := range []string{
		`CREATE TABLE scalar_targets(id TEXT PRIMARY KEY, title TEXT, present BOOLEAN, payload JSON, computed JSON, revision INTEGER)`,
		`INSERT INTO scalar_targets VALUES
   ('a','Visible',1,'false','{"state":"ready","value":"Fresh","version":{"definitionVersion":2,"sourceDataRevision":7,"dependencyWatermark":"dependency"}}',7),
   ('b','Hidden',0,'{"secret":"object"}','{"state":"ready","value":"STALE","version":{"definitionVersion":2,"sourceDataRevision":6,"dependencyWatermark":"dependency"}}',7),
   ('c','  ',1,'["nested"]','{"state":"failed","value":"FAILED"}',7)`,
		`CREATE TABLE scalar_rows(id TEXT PRIMARY KEY, links JSON)`,
		`INSERT INTO scalar_rows VALUES ('source','["a","b","c"]')`,
	} {
		if _, err := app.DB().NewQuery(sql).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	relation := &query.RelationDescriptor{
		TableName: "scalar_targets", PrimaryKey: "id", RowRevisionName: "revision", PresenceFields: map[string]string{"title": "present"},
		Fields: map[string]query.FieldDescriptor{
			"title":    {PhysicalName: "title", Type: query.FieldTypeText},
			"payload":  {PhysicalName: "payload", Type: query.FieldTypeJSON},
			"computed": {PhysicalName: "computed", Type: query.FieldTypeText, ComputedEnvelope: true, ComputedReady: true, ComputedDefinitionVersion: 2, ComputedDependencyWatermark: "dependency"},
		},
	}
	source := &staticQuerySource{descriptor: query.TableDescriptor{DatabaseID: "local", TableID: "scalar", PhysicalName: "scalar_rows", PrimaryKey: "id", SchemaRevision: "s", Fields: map[string]query.FieldDescriptor{
		"id": {PhysicalName: "id", Type: query.FieldTypeText}, "links": {PhysicalName: "links", Type: query.FieldTypeMultiRelation, Relation: relation},
	}}}
	port := query.NewPort(app, source)
	for _, test := range []struct {
		field string
		want  map[string]string
	}{
		{"title", map[string]string{"a": "Visible"}},
		{"payload", map[string]string{"a": "false"}},
		{"computed", map[string]string{"a": "Fresh"}},
		{"missing", map[string]string{}},
	} {
		t.Run(test.field, func(t *testing.T) {
			relation.DisplayField = test.field
			page, err := port.QueryPage(context.Background(), "scalar", query.TableQuery{Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			got := page.Rows[0][query.RelationLabelsField].(map[string]map[string]string)["links"]
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("labels=%#v, want %#v", got, test.want)
			}
		})
	}
	relation.DisplayField = "computed"
	computed := relation.Fields["computed"]
	computed.ComputedReady = false
	computed.ComputedStatus = "updating"
	relation.Fields["computed"] = computed
	page, err := port.QueryPage(context.Background(), "scalar", query.TableQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Rows[0][query.RelationLabelsField].(map[string]map[string]string)["links"]; len(got) != 0 {
		t.Fatalf("pending formula leaked labels: %#v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := port.QueryPage(ctx, "scalar", query.TableQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled label query: %v", err)
	}
}
