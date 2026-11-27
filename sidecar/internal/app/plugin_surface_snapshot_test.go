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
	nullPage := `{"contract":"` + pluginSnapshotContract + `","sources":[{"query":{"contract":"vibetable.plugin-query.v2","collection":"` + table.id + `","fields":["id"],"pageSize":null}}],"versions":{}}`
	if result, code := f.snapshot(t, hook, "snapshot.validate", json.RawMessage(nullPage)); result != nil || code != "plugin_capability_invalid" {
		t.Fatalf("validate with null pageSize returned %+v, %q", result, code)
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
