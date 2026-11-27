package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// #590 snapshot fixture reuses the data.versions fixture: every table owns two
// text fields (name, secret) and one business row, and the plugin is granted
// id + both fields on every table unless a test installs narrower grants.

func snapshotQuery(table versionsTable, fields ...string) pluginQueryRequest {
	return pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: table.id, Fields: fields}
}

func snapshotRequest(sources ...pluginQueryRequest) pluginSnapshotRequest {
	request := pluginSnapshotRequest{Contract: pluginSnapshotContract}
	for _, source := range sources {
		request.Sources = append(request.Sources, pluginSnapshotSource{Query: source})
	}
	return request
}

// snapshotEchoRequest feeds an open result back the way the Host does: the
// original sources plus the open result's versions, re-marshaled verbatim.
func snapshotEchoRequest(request pluginSnapshotRequest, open map[string]any) json.RawMessage {
	sources, _ := json.Marshal(request.Sources)
	versions, _ := json.Marshal(open["versions"])
	return json.RawMessage(`{"contract":"` + pluginSnapshotContract + `","sources":` + string(sources) + `,"versions":` + string(versions) + `}`)
}

func (f *versionsFixture) snapshotCtx(ctx context.Context, t *testing.T, app core.App, method string, request any) (map[string]any, string) {
	t.Helper()
	input := f.input
	input.Method = method
	switch value := request.(type) {
	case json.RawMessage:
		input.Request = value
	default:
		input.Request, _ = json.Marshal(request)
	}
	result, err := executePluginSurfaceAPI(ctx, app, f.source, nil, f.workspace, input, nil)
	if err == nil {
		opened, _ := result.(map[string]any)
		if opened == nil {
			t.Fatalf("unexpected result %+v", result)
		}
		return opened, ""
	}
	if result != nil {
		t.Fatalf("failure leaked a result: %+v", result)
	}
	var product *query.ProductError
	if !errors.As(err, &product) {
		t.Fatalf("non-product error: %v", err)
	}
	return nil, product.Code
}

func (f *versionsFixture) snapshot(t *testing.T, app core.App, method string, request any) (map[string]any, string) {
	t.Helper()
	return f.snapshotCtx(context.Background(), t, app, method, request)
}

// snapshotRecorder hooks both dbx pools (a transaction copies the log hook)
// and wraps the app so top-level transactions are counted.
func snapshotRecorder(f *versionsFixture) (*versionsSQLRecorder, *versionsTxHook) {
	recorder := &versionsSQLRecorder{}
	for _, pool := range []dbx.Builder{f.pb.ConcurrentDB(), f.pb.NonconcurrentDB()} {
		pool.(*dbx.DB).QueryLogFunc = recorder.log
	}
	return recorder, &versionsTxHook{App: f.pb, recorder: recorder}
}

func snapshotSource(t *testing.T, open map[string]any, collection string) map[string]any {
	t.Helper()
	sources, _ := open["sources"].([]any)
	for _, item := range sources {
		if source, ok := item.(map[string]any); ok && source["collection"] == collection {
			return source
		}
	}
	t.Fatalf("missing source %s: %+v", collection, open)
	return nil
}

// legacyQuery walks the ordinary data.query cursor pages exactly like the SDK
// does and returns every item and row guard in order.
func (f *versionsFixture) legacyQuery(t *testing.T, app core.App, r pluginQueryRequest, pageSize int) ([]any, map[string]any) {
	t.Helper()
	items, guards := []any{}, map[string]any{}
	input := f.input
	input.Method = "data.query"
	request := r
	request.PageSize = &pageSize
	for {
		input.Request, _ = json.Marshal(request)
		result, err := executePluginSurfaceAPI(context.Background(), app, f.source, nil, f.workspace, input, nil)
		if err != nil {
			t.Fatal(err)
		}
		page := result.(map[string]any)
		items = append(items, page["items"].([]any)...)
		for id, guard := range page["rowGuards"].(map[string]any) {
			guards[id] = guard
		}
		var next string
		if cursor, ok := page["nextCursor"].(*string); ok && cursor != nil {
			next = *cursor
		}
		if next == "" {
			return items, guards
		}
		input.Cursor = next
		input.Dependencies = page["dependencies"].(map[string]pluginComputedRevision)
	}
}

func (f *versionsFixture) addRow(t *testing.T, table versionsTable, name, secret string) {
	t.Helper()
	ctx := context.Background()
	schema, err := schemaexecution.Describe(ctx, f.pb, table.id)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := f.pb.FindCollectionByNameOrId(table.physical)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	for id, value := range map[string]string{table.nameID: name, table.secretID: secret} {
		field, ok := schema.Field(id)
		if !ok {
			t.Fatalf("field %s missing", id)
		}
		row.Set(field.Identity.PhysicalName, value)
		row.Set(field.Value.Presence.PhysicalName, true)
	}
	if err = f.pb.Save(row); err != nil {
		t.Fatal(err)
	}
}

func TestPluginSurfaceSnapshotMatchesLegacyQueryRowsGuardsAndVersions(t *testing.T) {
	f := newVersionsFixture(t, 3)
	f.grantAll(t)
	for i := 1; i <= 4; i++ {
		f.addRow(t, f.tables[0], fmt.Sprintf("row-%d", i), fmt.Sprintf("secret-%d", i))
	}
	full := func(table versionsTable) []string { return []string{"id", table.nameID, table.secretID} }
	request := snapshotRequest(snapshotQuery(f.tables[0], full(f.tables[0])...), snapshotQuery(f.tables[1], full(f.tables[1])...), snapshotQuery(f.tables[2], "id"))
	open, code := f.snapshot(t, f.pb, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	if open["contract"] != pluginSnapshotContract || len(open["sources"].([]any)) != 3 {
		t.Fatalf("open shape: %+v", open)
	}
	// Row by row and guard by guard the snapshot is the legacy query, in order.
	for i, table := range f.tables {
		source := snapshotSource(t, open, table.id)
		projection := full(table)
		if i == 2 {
			projection = []string{"id"}
		}
		items, guards := f.legacyQuery(t, f.pb, snapshotQuery(table, projection...), 2)
		if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(source["rowGuards"], guards) {
			t.Fatalf("source %s diverged from data.query:\n%v\n%v", table.id, source["items"], items)
		}
		if fmt.Sprint(source["fields"]) != fmt.Sprint(projection) || fmt.Sprint(source["computedFields"]) != "[]" || source["complete"] != true {
			t.Fatalf("projection echo %s: %+v", table.id, source)
		}
		expected := int64(1)
		if i == 0 {
			expected = 5
		}
		if source["filteredRows"] != expected || source["totalRows"] != expected || int64(len(source["items"].([]map[string]any))) != expected {
			t.Fatalf("source counts %s: %+v", table.id, source)
		}
	}
	// The version vector is the data.versions vector for the same projection.
	versions, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: full(f.tables[0])}, {Collection: f.tables[1].id, Fields: full(f.tables[1])}, {Collection: f.tables[2].id, Fields: []string{"id"}}}, nil))
	if code != "" || !reflect.DeepEqual(open["versions"], versions["versions"]) {
		t.Fatalf("snapshot versions differ from data.versions: %+v vs %+v (%s)", open["versions"], versions["versions"], code)
	}
	// Filters, sorts and ids follow the query contract and the legacy result.
	filtered := snapshotQuery(f.tables[0], full(f.tables[0])...)
	filterValue := []any{"row-2", "row-4"}
	filtered.Filters = []query.FilterExpression{{Field: f.tables[0].nameID, Operator: query.OperatorIn, Value: filterValue}}
	filtered.Sorts = []query.SortCondition{{Field: f.tables[0].nameID, Direction: query.SortDescending}}
	open, code = f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(filtered))
	if code != "" {
		t.Fatal(code)
	}
	items, guards := f.legacyQuery(t, f.pb, filtered, 1)
	source := snapshotSource(t, open, f.tables[0].id)
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(source["rowGuards"], guards) || source["filteredRows"] != int64(2) || source["totalRows"] != int64(5) {
		t.Fatalf("filtered snapshot diverged: %+v vs %v", source, items)
	}
	byID := snapshotQuery(f.tables[0], full(f.tables[0])...)
	ids := []string{}
	nameColumn := f.physical(t, f.tables[0], f.tables[0].nameID)
	for _, row := range f.rows(t, f.tables[0]) {
		if name := row.GetString(nameColumn); name == "row-2" || name == "row-4" {
			ids = append(ids, row.Id)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("fixture rows: %v", ids)
	}
	byID.IDs = &ids
	open, code = f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(byID))
	if code != "" {
		t.Fatal(code)
	}
	items, guards = f.legacyQuery(t, f.pb, byID, 1)
	source = snapshotSource(t, open, f.tables[0].id)
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(source["rowGuards"], guards) || source["filteredRows"] != int64(2) || source["totalRows"] != int64(5) {
		t.Fatalf("ids snapshot diverged: %+v vs %v", source, items)
	}
}

// physical resolves a canonical field id to its stored column name.
func (f *versionsFixture) physical(t *testing.T, table versionsTable, fieldID string) string {
	t.Helper()
	schema, err := schemaexecution.Describe(context.Background(), f.pb, table.id)
	if err != nil {
		t.Fatal(err)
	}
	field, ok := schema.Field(fieldID)
	if !ok {
		t.Fatalf("field %s missing", fieldID)
	}
	return field.Identity.PhysicalName
}

// rows lists a fixture table's business records through the ordinary record API.
func (f *versionsFixture) rows(t *testing.T, table versionsTable) []*core.Record {
	t.Helper()
	records, err := f.pb.FindRecordsByFilter(table.physical, "id != ''", "id", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestPluginSurfaceSnapshotValidateIgnoresWritesButNotSchemaOrGrants(t *testing.T) {
	f := newVersionsFixture(t, 2)
	f.grantAll(t)
	request := snapshotRequest(f.snapshotQueries("id", "name")...)
	open, code := f.snapshot(t, f.pb, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	echo := snapshotEchoRequest(request, open)
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); code != "" || result["valid"] != true {
		t.Fatalf("fresh validate: %+v %q", result, code)
	}
	// A later row write is a new read point, not a stale snapshot: the open
	// handle keeps validating and a fresh open sees the write.
	f.addRow(t, f.tables[0], "later", "later-secret")
	f.bump(t, f.tables[0])
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); code != "" || result["valid"] != true {
		t.Fatalf("write invalidated an open snapshot: %+v %q", result, code)
	}
	again, code := f.snapshot(t, f.pb, "snapshot.open", request)
	if code != "" || snapshotSource(t, again, f.tables[0].id)["filteredRows"] != int64(2) {
		t.Fatalf("fresh open missed the write: %+v %q", again, code)
	}
	// A narrowed grant fails the page before any row is served.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id"}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); result != nil || code != "plugin_read_denied" {
		t.Fatalf("narrowed grant returned %+v, %q", result, code)
	}
	if result, code := f.snapshot(t, f.pb, "snapshot.open", request); result != nil || code != "plugin_read_denied" {
		t.Fatalf("narrowed open returned %+v, %q", result, code)
	}
	// The fixture's input now carries the new installation revision; a stale
	// Host still on the previous revision is told so instead of being served.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[0].nameID}},
		{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[1].nameID}}})
	request = snapshotRequest(f.snapshotQueries("id", "name")...)
	open, code = f.snapshot(t, f.pb, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	echo = snapshotEchoRequest(request, open)
	f.input.InstallationRevision--
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); result != nil || code != "plugin_cursor_stale" {
		t.Fatalf("stale installation returned %+v, %q", result, code)
	}
	f.input.InstallationRevision++
	// A schema change (new field) is a new projection universe: stale.
	createSchemaProductField(t, f.pb, f.tables[1].id, v2.LogicalText, "Extra", "snapshot-extra")
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); result != nil || code != "plugin_snapshot_stale" {
		t.Fatalf("schema change returned %+v, %q", result, code)
	}
	// Wildcard grants never open snapshots.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"*"}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(snapshotQuery(f.tables[0], "id", f.tables[0].nameID))); result != nil || code != "plugin_read_denied" {
		t.Fatalf("wildcard grant returned %+v, %q", result, code)
	}
}

// grantAll gives every fixture table the same explicit read+query grant on id,
// name and secret so a snapshot of any projection of those columns is readable.
func (f *versionsFixture) grantAll(t *testing.T) {
	t.Helper()
	grants := []pluginReadGrant{}
	for _, table := range f.tables {
		grants = append(grants, pluginReadGrant{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID, table.secretID}})
	}
	f.regrant(t, grants)
}

func (f *versionsFixture) snapshotQueries(fields ...string) []pluginQueryRequest {
	sources := []pluginQueryRequest{}
	for _, table := range f.tables {
		// besides "id", the symbolic tokens name/secret resolve to each
		// table's own field IDs.
		ids := []string{}
		for _, id := range fields {
			switch id {
			case "name":
				id = table.nameID
			case "secret":
				id = table.secretID
			}
			ids = append(ids, id)
		}
		sources = append(sources, snapshotQuery(table, ids...))
	}
	return sources
}

func TestPluginSurfaceSnapshotMalformedRequestsFailBeforeAnyTransaction(t *testing.T) {
	f := newVersionsFixture(t, 1)
	recorder, hook := snapshotRecorder(f)
	table := f.tables[0]
	valid := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}]}`
	for name, raw := range map[string]string{
		"empty":          `[]`,
		"no sources":     `{"contract":"` + pluginSnapshotContract + `","sources":[]}`,
		"unknown key":    `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"extra":1}`,
		"cursor":         `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"cursor":"c"}}]}`,
		"pageSize":       `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"pageSize":7}}]}`,
		"null filters":   `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"filters":null}}]}`,
		"duplicate":      `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}},{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}]}`,
		"relation path":  `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id","a.b"]}}]}`,
		"active alias":   `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"$active","fields":["id"]}}]}`,
		"wrong contract": `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v1","collection":"` + table.id + `","fields":["id"]}}]}`,
		"open versions":  `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"versions":{}}`,
	} {
		if result, code := f.snapshot(t, hook, "snapshot.open", json.RawMessage(raw)); result != nil || code != "plugin_capability_invalid" {
			t.Fatalf("%s returned %+v, %q", name, result, code)
		}
	}
	// Too many ids is the query limit, not a capability failure.
	ids := make([]string, pluginSnapshotMaxIDs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	many := snapshotQuery(table, "id")
	many.IDs = &ids
	if result, code := f.snapshot(t, hook, "snapshot.open", snapshotRequest(many)); result != nil || code != "plugin_query_limit" {
		t.Fatalf("too many ids returned %+v, %q", result, code)
	}
	// validate without versions, or with a missing/null member, is invalid.
	if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(valid)); result != nil || code != "plugin_capability_invalid" {
		t.Fatalf("validate without versions returned %+v, %q", result, code)
	}
	incomplete := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"versions":{"` + table.id + `":{"schemaRevision":"schema_0001","clockPeriod":"","computed":{}}}}`
	if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(incomplete)); result != nil || code != "plugin_capability_invalid" {
		t.Fatalf("incomplete versions returned %+v, %q", result, code)
	}
	if atomic.LoadInt32(&hook.count) != 0 || len(recorder.snapshot()) != 0 {
		t.Fatalf("malformed requests reached storage: tx=%d statements=%d", hook.count, len(recorder.snapshot()))
	}
	// The valid shape opens inside exactly one transaction.
	if result, code := f.snapshot(t, hook, "snapshot.open", json.RawMessage(valid)); code != "" || result["contract"] != pluginSnapshotContract {
		t.Fatalf("valid open: %+v %q", result, code)
	}
	if atomic.LoadInt32(&hook.count) != 1 {
		t.Fatalf("open transactions: %d", hook.count)
	}
}

func TestPluginSurfaceSnapshotFortySourcesValidateWithOneBatchedRead(t *testing.T) {
	f := newVersionsFixture(t, 40)
	f.grantAll(t)
	request := snapshotRequest(f.snapshotQueries("id", "name")...)
	recorder, hook := snapshotRecorder(f)
	open, code := f.snapshot(t, hook, "snapshot.open", request)
	if code != "" || len(open["sources"].([]any)) != 40 {
		t.Fatalf("open: %+v %q", open, code)
	}
	before := len(recorder.snapshot())
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(request, open)); code != "" || result["valid"] != true {
		t.Fatalf("validate: %+v %q", result, code)
	}
	var batched, described int
	for _, statement := range recorder.snapshot()[before:] {
		if strings.Contains(statement.statement, "typeof(schema_revision)") {
			batched++
		}
		if strings.Contains(statement.statement, "`vibetable_fields`") || strings.Contains(statement.statement, "`vibetable_formulas`") {
			described++
		}
	}
	if batched != 1 || described != 0 {
		t.Fatalf("validate replayed per-source describes: batched=%d described=%d", batched, described)
	}
	// Physical-name grants take the per-source describe path and still
	// authorize correctly.
	nameColumn := f.physical(t, f.tables[0], f.tables[0].nameID)
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", nameColumn}}})
	single := snapshotRequest(snapshotQuery(f.tables[0], "id", f.tables[0].nameID))
	open, code = f.snapshot(t, hook, "snapshot.open", single)
	if code != "" {
		t.Fatal(code)
	}
	before = len(recorder.snapshot())
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(single, open)); code != "" || result["valid"] != true {
		t.Fatalf("slow-path validate: %+v %q", result, code)
	}
	described = 0
	for _, statement := range recorder.snapshot()[before:] {
		if strings.Contains(statement.statement, "`vibetable_fields`") {
			described++
		}
	}
	if described == 0 {
		t.Fatal("physical-name grant never described the source")
	}
	// Removing a field the snapshot projects is denied on the slow path too.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[0].secretPhysical}}})
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(single, open)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("physical-name narrowed grant returned %+v, %q", result, code)
	}
	// A schema change behind a physical-name grant is stale too.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", nameColumn}}})
	open, code = f.snapshot(t, hook, "snapshot.open", single)
	if code != "" {
		t.Fatal(code)
	}
	createSchemaProductField(t, f.pb, f.tables[0].id, v2.LogicalText, "Extra", "snapshot-forty-extra")
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(single, open)); result != nil || code != "plugin_snapshot_stale" {
		t.Fatalf("schema change behind physical grant returned %+v, %q", result, code)
	}
}

func TestPluginSurfaceSnapshotByteBudgetAndEmptySources(t *testing.T) {
	f := newVersionsFixture(t, 2)
	f.grantAll(t)
	// An empty source is a legitimate snapshot member with no rows.
	if _, err := f.pb.DB().NewQuery("DELETE FROM `" + f.tables[1].physical + "`").Execute(); err != nil {
		t.Fatal(err)
	}
	request := snapshotRequest(f.snapshotQueries("id", "name", "secret")...)
	open, code := f.snapshot(t, f.pb, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	if source := snapshotSource(t, open, f.tables[1].id); source["filteredRows"] != int64(0) || len(source["items"].([]map[string]any)) != 0 || source["complete"] != true || len(source["rowGuards"].(map[string]any)) != 0 {
		t.Fatalf("empty source: %+v", source)
	}
	// Bytes are bounded across all sources: 110 maximal text rows encode past
	// 512 KiB, so the open fails without a partial result.
	for i := 0; i < 110; i++ {
		f.addRow(t, f.tables[0], strings.Repeat("x", 5000), "x")
	}
	f.bump(t, f.tables[0])
	if result, code := f.snapshot(t, f.pb, "snapshot.open", request); result != nil || code != "plugin_snapshot_limit" {
		t.Fatalf("oversized snapshot returned %+v, %q", result, code)
	}
	// The same rows projected narrowly still open: the budget counts bytes,
	// not rows.
	narrow := snapshotRequest(snapshotQuery(f.tables[0], "id"), snapshotQuery(f.tables[1], "id"))
	if result, code := f.snapshot(t, f.pb, "snapshot.open", narrow); code != "" || snapshotSource(t, result, f.tables[0].id)["totalRows"] != int64(111) {
		t.Fatalf("narrow oversized projection: %+v %q", result, code)
	}
}

func TestPluginSurfaceSnapshotComputedClosureFollowsVersionsSemantics(t *testing.T) {
	f := newVersionsFixture(t, 1)
	table := f.tables[0]
	input := createVersionsFormula(t, f.pb, table.id, "snapshot-input", "Input", table.secretPhysical)
	output := createVersionsFormula(t, f.pb, table.id, "snapshot-output", "Output", input.Identity.PhysicalName)
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID, table.secretID, input.Identity.FieldID, output.Identity.FieldID}}})
	project := snapshotQuery(table, "id", output.Identity.FieldID)
	open, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(project))
	if code != "" {
		t.Fatal(code)
	}
	source := snapshotSource(t, open, table.id)
	if fmt.Sprint(source["computedFields"]) != fmt.Sprint([]string{output.Identity.FieldID}) {
		t.Fatalf("computed fields: %+v", source)
	}
	items, _ := f.legacyQuery(t, f.pb, project, 200)
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) {
		t.Fatalf("computed snapshot diverged from data.query: %+v vs %v", source["items"], items)
	}
	versions, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: table.id, Fields: []string{"id", output.Identity.FieldID}}}, nil))
	if code != "" || !reflect.DeepEqual(open["versions"], versions["versions"]) || len(versionsOf(t, open, table.id).Computed) != 1 {
		t.Fatalf("computed versions differ: %+v vs %+v (%s)", open["versions"], versions["versions"], code)
	}
	// A transitive same-row operand without a grant denies the whole snapshot.
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID, input.Identity.FieldID, output.Identity.FieldID}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(project)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("missing transitive grant returned %+v, %q", result, code)
	}
	// An unready computed runtime is plugin_query_failed, exactly like #589.
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID, table.secretID, input.Identity.FieldID, output.Identity.FieldID}}})
	runtime, err := f.pb.FindFirstRecordByData("vibetable_formulas", "field_id", output.Identity.FieldID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Set("status", "backfilling")
	if err = f.pb.Save(runtime); err != nil {
		t.Fatal(err)
	}
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(project)); result != nil || code != "plugin_query_failed" {
		t.Fatalf("unready computed returned %+v, %q", result, code)
	}
}
