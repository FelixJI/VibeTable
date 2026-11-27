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
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
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
// original sources plus the open result's versions and the binding record it
// was opened under, re-marshaled verbatim.
func snapshotEchoRequest(request pluginSnapshotRequest, open map[string]any) json.RawMessage {
	binding, _ := json.Marshal(open["binding"])
	return snapshotEchoBinding(request, open, string(binding))
}

// snapshotEchoBinding echoes an open result with an explicit binding member.
func snapshotEchoBinding(request pluginSnapshotRequest, open map[string]any, binding string) json.RawMessage {
	sources, _ := json.Marshal(request.Sources)
	versions, _ := json.Marshal(open["versions"])
	return json.RawMessage(`{"contract":"` + pluginSnapshotContract + `","sources":` + string(sources) + `,"versions":` + string(versions) + `,"binding":` + binding + `}`)
}

// snapshotBinding reads the Host-only binding member of an open result: the
// audit sequence of the completed model record the handle was opened under.
func snapshotBinding(t *testing.T, open map[string]any) (int64, bool) {
	t.Helper()
	switch binding := open["binding"].(type) {
	case *int64:
		if binding == nil {
			return 0, false
		}
		return *binding, true
	case nil:
		return 0, false
	}
	t.Fatalf("binding member %v (%T)", open["binding"], open["binding"])
	return 0, false
}

// snapshotCall runs one snapshot operation through the surface API the way the
// Host does and returns the result or the product code.
func (f *dataReadFixture) snapshotCall(t *testing.T, app core.App, method string, request any) (map[string]any, string) {
	t.Helper()
	input := f.input
	input.Method = method
	switch value := request.(type) {
	case json.RawMessage:
		input.Request = value
	default:
		input.Request, _ = json.Marshal(request)
	}
	result, err := executePluginSurfaceAPI(context.Background(), app, f.source, nil, f.workspace, input, nil)
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

// snapshotGuards folds a corpus source's aligned [id, digest] pairs back into the
// data.query rowGuards map (rows the plugin may not address carry null).
func snapshotGuards(source map[string]any) map[string]any {
	guards := map[string]any{}
	for _, entry := range source["rowGuards"].([]any) {
		if pair, ok := entry.([]any); ok {
			guards[pair[0].(string)] = pair[1]
		}
	}
	return guards
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
		if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(snapshotGuards(source), guards) {
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
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(snapshotGuards(source), guards) || source["filteredRows"] != int64(2) || source["totalRows"] != int64(5) {
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
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(snapshotGuards(source), guards) || source["filteredRows"] != int64(2) || source["totalRows"] != int64(5) {
		t.Fatalf("ids snapshot diverged: %+v vs %v", source, items)
	}
	// data.query guards every row by id whenever id is granted, projected or
	// not. The corpus keeps the same guards aligned with items (one pair per
	// row, no id added to the public rows) so the Host can slice them at any
	// offset without exposing an unprojected id.
	bare := snapshotQuery(f.tables[0], f.tables[0].nameID)
	bare.Filters = filtered.Filters
	bare.Sorts = filtered.Sorts
	open, code = f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(bare))
	if code != "" {
		t.Fatal(code)
	}
	source = snapshotSource(t, open, f.tables[0].id)
	bareItems, guards := f.legacyQuery(t, f.pb, bare, 1)
	addressed, _ := f.legacyQuery(t, f.pb, filtered, 1)
	pairs := source["rowGuards"].([]any)
	rows := source["items"].([]map[string]any)
	if !reflect.DeepEqual(fmt.Sprint(rows), fmt.Sprint(bareItems)) || len(pairs) != len(rows) || len(rows) != 2 || len(addressed) != 2 || !reflect.DeepEqual(snapshotGuards(source), guards) {
		t.Fatalf("unprojected id snapshot diverged: %+v vs %v", rows, bareItems)
	}
	for i, row := range rows {
		id := addressed[i].(map[string]any)["id"].(string)
		pair := pairs[i].([]any)
		if _, leaked := row["id"]; leaked || pair[0] != id || pair[1] != guards[id] {
			t.Fatalf("row %d leaked id or lost its guard: %+v %v", i, row, pair)
		}
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

// Groups are structure, not reads: exactly like data.query only the leaves of a
// filter tree need a grant, a group node's own field is ignored, and a leaf's
// grant is checked at open and again by the fast path before every page.
func TestPluginSurfaceSnapshotGroupedFiltersFollowQueryGrants(t *testing.T) {
	f := newVersionsFixture(t, 1)
	f.grantAll(t)
	table := f.tables[0]
	for i := 1; i <= 4; i++ {
		f.addRow(t, table, fmt.Sprintf("row-%d", i), fmt.Sprintf("secret-%d", i))
	}
	// name in (row-2, row-4) AND (secret = secret-2 OR secret = secret-4),
	// nested two levels: the shape data.query normalizes.
	grouped := snapshotQuery(table, "id", table.nameID)
	grouped.Filters = []query.FilterExpression{{Filters: []query.FilterExpression{
		{Field: table.nameID, Operator: query.OperatorIn, Value: []any{"row-2", "row-4"}},
		{GroupLogic: query.LogicOr, Filters: []query.FilterExpression{
			{Field: table.secretID, Operator: query.OperatorEqual, Value: "secret-2"},
			{Field: table.secretID, Operator: query.OperatorEqual, Value: "secret-4"},
		}},
	}}}
	recorder, hook := snapshotRecorder(f)
	open, code := f.snapshot(t, hook, "snapshot.open", snapshotRequest(grouped))
	if code != "" {
		t.Fatal(code)
	}
	items, guards := f.legacyQuery(t, f.pb, grouped, 1)
	source := snapshotSource(t, open, table.id)
	if !reflect.DeepEqual(fmt.Sprint(source["items"]), fmt.Sprint(items)) || !reflect.DeepEqual(snapshotGuards(source), guards) || source["filteredRows"] != int64(2) {
		t.Fatalf("grouped snapshot diverged from data.query: %+v vs %v", source, items)
	}
	// Every leaf is granted, so the plain grouped source stays on the fast
	// path (no per-source describe) and reports readable row identity.
	before := len(recorder.snapshot())
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(snapshotRequest(grouped), open)); code != "" || result["valid"] != true || result["identity"].(map[string]bool)[table.id] != true {
		t.Fatalf("grouped validate: %+v %q", result, code)
	}
	for _, statement := range recorder.snapshot()[before:] {
		if strings.Contains(statement.statement, "`vibetable_fields`") || strings.Contains(statement.statement, "`vibetable_formulas`") {
			t.Fatalf("grouped filter took the describe path: %s", statement.statement)
		}
	}
	// Narrowing a leaf's grant anywhere in the tree fails the page.
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", snapshotEchoRequest(snapshotRequest(grouped), open)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("narrowed leaf grant returned %+v, %q", result, code)
	}
	// A hidden leaf denies the open at any depth.
	deep := snapshotQuery(table, "id", table.nameID)
	deep.Filters = []query.FilterExpression{{Filters: []query.FilterExpression{{GroupLogic: query.LogicOr, Filters: []query.FilterExpression{
		{Field: table.nameID, Operator: query.OperatorEqual, Value: "row-1"},
		{Field: table.secretID, Operator: query.OperatorEqual, Value: "secret-1"},
	}}}}}
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(deep)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("hidden nested leaf returned %+v, %q", result, code)
	}
	// A group node cannot double as a predicate: the shared query compiler
	// rejects it, so a group's own field is never a read to authorize.
	stray := snapshotQuery(table, "id", table.nameID)
	stray.Filters = []query.FilterExpression{{Field: table.secretID, Filters: []query.FilterExpression{{Field: table.nameID, Operator: query.OperatorEqual, Value: "row-1"}}}}
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(stray)); result != nil || code != "query.filter.invalid_group" {
		t.Fatalf("group with a predicate returned %+v, %q", result, code)
	}
}

// ids is an id filter: it needs the id grant at open (like data.query) and,
// because snapshotUsedFields now lists it, again before every page even when
// id is not projected and the source takes the fast path.
func TestPluginSurfaceSnapshotIdsNeedTheIdGrantOnEveryPage(t *testing.T) {
	f := newVersionsFixture(t, 1)
	table := f.tables[0]
	for i := 1; i <= 3; i++ {
		f.addRow(t, table, fmt.Sprintf("row-%d", i), fmt.Sprintf("secret-%d", i))
	}
	ids := []string{}
	for _, row := range f.rows(t, table) {
		ids = append(ids, row.Id)
	}
	byID := snapshotQuery(table, table.nameID)
	byID.IDs = &ids
	// Without the id grant the open is denied: the ids filter reads id.
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{table.nameID}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(byID)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("ids without id grant returned %+v, %q", result, code)
	}
	f.grantAll(t)
	open, code := f.snapshot(t, f.pb, "snapshot.open", snapshotRequest(byID))
	if code != "" || snapshotSource(t, open, table.id)["filteredRows"] != int64(4) {
		t.Fatalf("ids open: %+v %q", open, code)
	}
	// Revoking only id leaves the projection (name) readable but fails the
	// page: the ids filter may not be evaluated without a current id grant.
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{table.nameID}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", snapshotEchoRequest(snapshotRequest(byID), open)); result != nil || code != "plugin_read_denied" {
		t.Fatalf("revoked id grant returned %+v, %q", result, code)
	}
	// Granting id back validates the same handle again.
	f.grantAll(t)
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", snapshotEchoRequest(snapshotRequest(byID), open)); code != "" || result["valid"] != true {
		t.Fatalf("restored id grant: %+v %q", result, code)
	}
}

// validate reports, per source, whether the plugin may currently address rows
// by id. A projection that does not use id keeps serving pages after id is
// revoked, but without row guards; the fast path (canonical grants) and the
// slow path (physical-name grants) agree.
func TestPluginSurfaceSnapshotValidateReportsRowIdentityPerSource(t *testing.T) {
	f := newVersionsFixture(t, 2)
	f.grantAll(t)
	f.addRow(t, f.tables[0], "later", "later-secret")
	bare := snapshotRequest(snapshotQuery(f.tables[0], f.tables[0].nameID), snapshotQuery(f.tables[1], "id"))
	open, code := f.snapshot(t, f.pb, "snapshot.open", bare)
	if code != "" {
		t.Fatal(code)
	}
	// The corpus keeps one guard per row although no row projects id.
	source := snapshotSource(t, open, f.tables[0].id)
	if pairs := source["rowGuards"].([]any); len(pairs) != 2 || pairs[0] == nil || pairs[1] == nil {
		t.Fatalf("guards for unprojected id: %+v", pairs)
	}
	_, guards := f.legacyQuery(t, f.pb, snapshotQuery(f.tables[0], f.tables[0].nameID), 1)
	if !reflect.DeepEqual(snapshotGuards(source), guards) {
		t.Fatalf("unprojected guards diverged from data.query: %v vs %v", snapshotGuards(source), guards)
	}
	echo := snapshotEchoRequest(bare, open)
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); code != "" || !reflect.DeepEqual(result["identity"], map[string]bool{f.tables[0].id: true, f.tables[1].id: true}) {
		t.Fatalf("identity with full grants: %+v %q", result, code)
	}
	// Revoking id on table 0 keeps its pages valid (name only) but leaves no
	// row identity to serve; table 1, which projects id, is unaffected.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{f.tables[0].nameID}},
		{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id"}}})
	result, code := f.snapshot(t, f.pb, "snapshot.validate", echo)
	if code != "" || result["valid"] != true || !reflect.DeepEqual(result["identity"], map[string]bool{f.tables[0].id: false, f.tables[1].id: true}) {
		t.Fatalf("identity with revoked id: %+v %q", result, code)
	}
	// A corpus opened under the narrowed grant carries no identity at all.
	open, code = f.snapshot(t, f.pb, "snapshot.open", bare)
	if code != "" {
		t.Fatal(code)
	}
	if pairs := snapshotSource(t, open, f.tables[0].id)["rowGuards"].([]any); pairs[0] != nil || pairs[1] != nil {
		t.Fatalf("guards without id grant: %+v", pairs)
	}
	echo = snapshotEchoRequest(bare, open)
	// A physical-name grant takes the slow path and reports the same answer.
	nameColumn := f.physical(t, f.tables[0], f.tables[0].nameID)
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", nameColumn}},
		{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id"}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); code != "" || !reflect.DeepEqual(result["identity"], map[string]bool{f.tables[0].id: true, f.tables[1].id: true}) {
		t.Fatalf("slow-path identity: %+v %q", result, code)
	}
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{nameColumn}},
		{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id"}}})
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); code != "" || !reflect.DeepEqual(result["identity"], map[string]bool{f.tables[0].id: false, f.tables[1].id: true}) {
		t.Fatalf("slow-path identity with revoked id: %+v %q", result, code)
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
		"null versions":  `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"versions":null}`,
		"open binding":   `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"binding":null}`,
		"null ids":       `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"ids":null}}]}`,
		"null sorts":     `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"sorts":null}}]}`,
		"null pageSize":  `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"pageSize":null}}]}`,
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
	// validate without versions, or with a missing/null member, is invalid, and
	// the query-level null rules apply to validate exactly like open.
	if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(valid)); result != nil || code != "plugin_capability_invalid" {
		t.Fatalf("validate without versions returned %+v, %q", result, code)
	}
	withVersions := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"versions":{}`
	for name, member := range map[string]string{
		"without binding": "",
		"string binding":  `,"binding":"1"`,
		"zero binding":    `,"binding":0`,
		"float binding":   `,"binding":1.5`,
	} {
		if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(withVersions+member+`}`)); result != nil || code != "plugin_capability_invalid" {
			t.Fatalf("validate %s returned %+v, %q", name, result, code)
		}
	}
	nullPage := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"pageSize":null}}],"versions":{},"binding":null}`
	if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(nullPage)); result != nil || code != "plugin_capability_invalid" {
		t.Fatalf("validate with null pageSize returned %+v, %q", result, code)
	}
	incomplete := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"]}}],"versions":{"` + table.id + `":{"schemaRevision":"schema_0001","clockPeriod":"","computed":{}}}},"binding":null}`
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
	if source := snapshotSource(t, open, f.tables[1].id); source["filteredRows"] != int64(0) || len(source["items"].([]map[string]any)) != 0 || source["complete"] != true || len(source["rowGuards"].([]any)) != 0 {
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

// 句柄的绑定身份只有一个值：open 在授权事务内固定最新 completed 记录的审计序号（pluginstore 追加
// 审计行时按项目分配 MAX(seq)+1，审计行从不改写，序号从不复用），validate 在同一授权事务内重新
// 解析后比对。合法重映射即使发生在同一安装修订、同一结构修订、投影仍可读时也使旧句柄失效；
// A→B→A 得到的是新记录，原句柄不会复活；没有 completed 模型时为 null，且 none 只等于 none。
func TestPluginSurfaceSnapshotBindingFollowsCompletedRecordSequence(t *testing.T) {
	// 有数据的表不允许重映射字段（plugin_model_history_present），所以合法重映射发生在这张
	// 尚无业务行的已绑定表上：句柄打开时投影可读，重映射后它仍可读，但授权记录已换。
	f := newDataReadFixture(t, false)
	idGrants := []pluginReadGrant{
		{Collection: "$active", Operations: []string{"read", "query"}, Fields: []string{"id"}},
		{Collection: f.mapped.TableID, Operations: []string{"read", "query"}, Fields: []string{"id"}},
	}
	request := snapshotRequest(pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: f.mapped.TableID, Fields: []string{"id"}})
	validate := func(name, want string, open map[string]any) {
		t.Helper()
		result, code := f.snapshotCall(t, f.pb, "snapshot.validate", snapshotEchoRequest(request, open))
		if want == "" {
			if code != "" || result["valid"] != true {
				t.Fatalf("%s: %+v %q", name, result, code)
			}
			return
		}
		if result != nil || code != want {
			t.Fatalf("%s returned %+v, %q", name, result, code)
		}
	}
	first := f.latestCompleted(t)
	open, code := f.snapshotCall(t, f.pb, "snapshot.open", request)
	if seq, bound := snapshotBinding(t, open); code != "" || !bound || seq != first.Seq {
		t.Fatalf("open binding: %+v %q", open, code)
	}
	validate("fresh handle", "", open)
	// 合法重映射到另一个既有字段：安装修订与结构修订都不变，id 投影仍可读。
	f.remap(t, f.replacement.FieldID)
	second := f.latestCompleted(t)
	if second.Seq <= first.Seq || second.State.InstallationRevision != first.State.InstallationRevision {
		t.Fatalf("remap must append a record under the same installation revision: %+v -> %+v", first.State, second.State)
	}
	validate("remapped binding", "plugin_snapshot_stale", open)
	reopened, code := f.snapshotCall(t, f.pb, "snapshot.open", request)
	if seq, _ := snapshotBinding(t, reopened); code != "" || seq != second.Seq || !reflect.DeepEqual(open["versions"], reopened["versions"]) {
		t.Fatalf("reopened: %+v %q", reopened, code)
	}
	validate("reopened handle", "", reopened)
	// 映射回原字段（A→B→A）：绑定内容与最初相同，但它是新的 completed 记录；两个旧句柄都不复活。
	f.remap(t, f.mapped.FieldID)
	third := f.latestCompleted(t)
	if third.Seq <= second.Seq || !reflect.DeepEqual(third.State.Bindings, first.State.Bindings) {
		t.Fatalf("A→B→A must append a record with the original bindings: %+v", third.State)
	}
	validate("first handle after A→B→A", "plugin_snapshot_stale", open)
	validate("second handle after A→B→A", "plugin_snapshot_stale", reopened)
	// 回传值被篡改：声称 none、旧序号或尚未出现的序号都不能通过。
	current, code := f.snapshotCall(t, f.pb, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	for name, binding := range map[string]string{"none": "null", "older": fmt.Sprint(first.Seq), "future": fmt.Sprint(third.Seq + 1)} {
		if result, code := f.snapshotCall(t, f.pb, "snapshot.validate", snapshotEchoBinding(request, current, binding)); result != nil || code != "plugin_snapshot_stale" {
			t.Fatalf("%s binding returned %+v, %q", name, result, code)
		}
	}
	// 安装修订变化后还没有适用的 completed 记录：句柄为 none，validate 通过；绑定完成后 none 句柄失效。
	f.input = changeDataGrants(t, pluginstore.New(f.pb, f.workspace), f.input, idGrants)
	none, code := f.snapshotCall(t, f.pb, "snapshot.open", request)
	if _, bound := snapshotBinding(t, none); code != "" || bound {
		t.Fatalf("unbound open: %+v %q", none, code)
	}
	validate("unbound handle", "", none)
	f.remap(t, f.mapped.FieldID)
	validate("unbound handle after binding completed", "plugin_snapshot_stale", none)
	bound, code := f.snapshotCall(t, f.pb, "snapshot.open", request)
	if seq, isBound := snapshotBinding(t, bound); code != "" || !isBound || seq != f.latestCompleted(t).Seq {
		t.Fatalf("bound open: %+v %q", bound, code)
	}
	validate("bound handle", "", bound)
	// 记录不再适用于当前安装修订（none）：绑定句柄同样失效。
	f.input = changeDataGrants(t, pluginstore.New(f.pb, f.workspace), f.input, idGrants)
	validate("bound handle without an applicable record", "plugin_snapshot_stale", bound)
}

// 没有 id 授权时快照连行摘要都不读：未绑定表的字段授权不含 id，业务记录不再为了必将被丢弃的
// 守卫整行加载，回执里也没有 id；同一投影一旦授予 id 就附加行摘要（整行按批加载）。
func TestPluginSurfaceSnapshotWithoutIdGrantSkipsDigests(t *testing.T) {
	f := newVersionsFixture(t, 1)
	table := f.tables[0]
	recorder := &versionsSQLRecorder{}
	for _, pool := range []dbx.Builder{f.pb.ConcurrentDB(), f.pb.NonconcurrentDB()} {
		pool.(*dbx.DB).QueryLogFunc = recorder.log
	}
	hook := &versionsTxHook{App: f.pb, recorder: recorder}
	// The digest batch reads complete records: `SELECT <table>.* FROM <table>`.
	full := "SELECT `" + table.physical + "`.* FROM `" + table.physical + "`"
	digestRuns := func(statements []recordedSQL) int {
		runs := 0
		for _, statement := range statements {
			if strings.Contains(statement.statement, full) {
				runs++
			}
		}
		return runs
	}
	request := snapshotRequest(snapshotQuery(table, table.nameID))
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{table.nameID}}})
	before := len(recorder.snapshot())
	open, code := f.snapshot(t, hook, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	source := snapshotSource(t, open, table.id)
	items := source["items"].([]map[string]any)
	if runs := digestRuns(recorder.snapshot()[before:]); runs != 0 || len(items) != 1 || len(items[0]) != 1 || fmt.Sprint(items[0][table.nameID]) != "甲" {
		t.Fatalf("id-less open read full records or projected id: %+v digest runs=%d", source, runs)
	}
	if guards := source["rowGuards"].([]any); len(guards) != 1 || guards[0] != nil {
		t.Fatalf("id-less guards: %+v", guards)
	}
	// 守卫不可用不影响 validate：没有 id 的投影本身不依赖 id 授权。
	if result, code := f.snapshot(t, hook, "snapshot.validate", snapshotEchoRequest(request, open)); code != "" || result["valid"] != true || result["identity"].(map[string]bool)[table.id] != false {
		t.Fatalf("id-less validate: %+v %q", result, code)
	}
	// 对照：同一投影授予 id 后附加行摘要，整行按批加载一次。
	f.regrant(t, []pluginReadGrant{{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID}}})
	before = len(recorder.snapshot())
	open, code = f.snapshot(t, hook, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	source = snapshotSource(t, open, table.id)
	guards, _ := source["rowGuards"].([]any)[0].([]any)
	if runs := digestRuns(recorder.snapshot()[before:]); runs != 1 || len(guards) != 2 || guards[0] == "" || !strings.HasPrefix(fmt.Sprint(guards[1]), "sha256:") {
		t.Fatalf("control open must attach a digest by loading the record: %+v digest runs=%d", source, runs)
	}
}

// 直接授权的普通表在句柄打开之后被权威 DeleteTable 正常删除（非模型绑定）：这是语料的结构变化，
// 复核按 plugin_snapshot_stale 报告，而不是把 ErrTableNotFound 误报成 plugin_query_failed；
// 描述遇到的其它错误（这里用损坏的修订字面量制造）仍然保持 plugin_query_failed。
func TestPluginSurfaceSnapshotValidateReportsDeletedSourceAsStale(t *testing.T) {
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
	removed, err := schemaexecution.Describe(context.Background(), f.pb, f.tables[1].id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = schemaapi.New(f.pb).DeleteTable(context.Background(), f.tables[1].id, mustModelRevision(t, removed.Snapshot.SchemaRevision)); err != nil {
		t.Fatalf("ordinary native schema blocked: %v", err)
	}
	// 删除后的首次读取：批量修订读取因缺行不完整，复核回退到 Describe，缺表是结构变化。
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); result != nil || code != "plugin_snapshot_stale" {
		t.Fatalf("deleted source returned %+v, %q", result, code)
	}
	// 对照：非缺表的描述错误不被归并成 stale。
	if _, err = f.pb.DB().Update("vibetable_tables", dbx.Params{"schema_revision": "corrupt"}, dbx.HashExp{"table_id": f.tables[0].id}).Execute(); err != nil {
		t.Fatal(err)
	}
	if result, code := f.snapshot(t, f.pb, "snapshot.validate", echo); result != nil || code != "plugin_query_failed" {
		t.Fatalf("corrupt metadata returned %+v, %q", result, code)
	}
}

// 打开在同一事务内共享一张依赖图：授权遍历（data.versions 语义）已经证明为同行的公式，描述阶段不再
// 重复读取 computation_dependencies；关系目标上的公式不在同行授权范围内，由完整缓存首次解析，
// 且同一目标在多个来源中重复出现时只解析一次（描述阶段直接使用事务的完整缓存，不按来源复制）；
// 原始关系字段及其目标计算字段的描述行为不变。
func TestPluginSurfaceSnapshotOpenSharesResolvedDependencyGraphWithDescribe(t *testing.T) {
	f := newVersionsFixture(t, 3)
	table, other, target := f.tables[0], f.tables[1], f.tables[2]
	input := createVersionsFormula(t, f.pb, table.id, "share-input", "Input", table.secretPhysical)
	output := createVersionsFormula(t, f.pb, table.id, "share-output", "Output", input.Identity.PhysicalName)
	targetFormula := createVersionsFormula(t, f.pb, target.id, "share-target", "Target", target.secretPhysical)
	defaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	toTarget := func(owner versionsTable, operationID string) v2.ApplyReceipt {
		return applySchemaProductField(t, f.pb, v2.FieldChangeIntent{
			Action: v2.ActionCreate, TableID: owner.id,
			Draft: &v2.FieldDraft{DisplayName: "共享关系", LogicalType: v2.LogicalRelation, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display,
				Relation: &v2.RelationSpec{TargetTableID: target.id, Cardinality: "one", DeletePolicy: "setNull", DisplayField: target.nameID}},
			RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "反向关系 " + operationID, ReciprocalCardinality: "many", SourceDisplayFieldID: owner.nameID},
		}, operationID)
	}
	relation := toTarget(table, "share-relation")
	sameTarget := toTarget(other, "share-relation-other")
	f.regrant(t, []pluginReadGrant{
		{Collection: table.id, Operations: []string{"read", "query"}, Fields: []string{"id", table.nameID, table.secretID, input.Identity.FieldID, output.Identity.FieldID, relation.FieldID}},
		{Collection: other.id, Operations: []string{"read", "query"}, Fields: []string{"id", other.nameID, sameTarget.FieldID}},
		{Collection: target.id, Operations: []string{"read", "query"}, Fields: []string{"id", target.nameID, target.secretID, targetFormula.Identity.FieldID}},
	})
	request := snapshotRequest(snapshotQuery(table, "id", output.Identity.FieldID, relation.FieldID), snapshotQuery(other, "id", sameTarget.FieldID))
	recorder, hook := snapshotRecorder(f)
	open, code := f.snapshot(t, hook, "snapshot.open", request)
	if code != "" {
		t.Fatal(code)
	}
	if len(open["sources"].([]any)) != 2 {
		t.Fatalf("sources: %+v", open)
	}
	source := snapshotSource(t, open, table.id)
	if fmt.Sprint(source["computedFields"]) != fmt.Sprint([]string{output.Identity.FieldID}) {
		t.Fatalf("computed fields: %+v", source)
	}
	var dependencyReads int
	for _, statement := range recorder.snapshot() {
		if strings.Contains(statement.statement, "`vibetable_computation_dependencies`") {
			dependencyReads++
		}
	}
	// output 及其同行操作数 input：授权遍历各读一次，描述阶段复用；两个来源指向同一关系目标，
	// 目标公式只由完整缓存解析一次。按来源复制缓存时为 4 次，最初为 6 次。
	if dependencyReads != 3 {
		t.Fatalf("computation_dependencies reads during open: %d", dependencyReads)
	}
}
