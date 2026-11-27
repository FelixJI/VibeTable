package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// #589 data.versions fixture: every table owns two text fields and one business
// row. The plugin is granted id + name + secret on every table unless a test
// installs narrower grants.
type versionsFixture struct {
	pb        *pocketbase.PocketBase
	workspace string
	input     pluginSurfaceAPIRequest
	source    *queryschema.Source
	tables    []versionsTable
}

type versionsTable struct{ id, physical, nameID, secretID, secretPhysical string }

func newVersionsFixture(t *testing.T, count int) *versionsFixture {
	t.Helper()
	ctx := context.Background()
	pb := schemaProductStore(t)
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	grants := []any{}
	f := &versionsFixture{pb: pb, workspace: "11111111-1111-4111-8111-111111111111"}
	for i := 0; i < count; i++ {
		table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: fmt.Sprintf("Versions %d", i), OperationID: fmt.Sprintf("versions-%d", i), Actor: v2.Actor{ID: "local-user", Kind: "user"}})
		if err != nil {
			t.Fatal(err)
		}
		name := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Name", fmt.Sprintf("versions-name-%d", i)).Definition
		secret := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "Secret", fmt.Sprintf("versions-secret-%d", i)).Definition
		schema, err := schemaexecution.Describe(ctx, pb, table.TableID)
		if err != nil {
			t.Fatal(err)
		}
		collection, err := pb.FindCollectionByNameOrId(schema.PhysicalName)
		if err != nil {
			t.Fatal(err)
		}
		row := core.NewRecord(collection)
		row.Set(name.Identity.PhysicalName, "甲")
		row.Set(name.Value.Presence.PhysicalName, true)
		row.Set(secret.Identity.PhysicalName, "机密")
		row.Set(secret.Value.Presence.PhysicalName, true)
		if err = pb.Save(row); err != nil {
			t.Fatal(err)
		}
		f.tables = append(f.tables, versionsTable{id: table.TableID, physical: schema.PhysicalName, nameID: name.Identity.FieldID, secretID: secret.Identity.FieldID, secretPhysical: secret.Identity.PhysicalName})
		grants = append(grants, map[string]any{"collection": table.TableID, "operations": []string{"read", "query"}, "fields": []string{"id", name.Identity.FieldID, secret.Identity.FieldID}})
	}
	f.install(t, grants)
	f.source, err = queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *versionsFixture) install(t *testing.T, grants []any) {
	t.Helper()
	store := pluginstore.New(f.pb, f.workspace)
	pluginID, hash := "com.example.versions", strings.Repeat("a", 64)
	manifest := map[string]any{"$schema": "vibetable.plugin-manifest.v2", "pluginId": pluginID, "version": "1.0.0", "displayName": map[string]any{}, "description": map[string]any{}, "compatibility": map[string]any{"pluginApi": "3.x"},
		"permissions": map[string]any{"data": grants},
		"actions":     []any{map[string]any{"actionId": "open", "risk": "read", "mode": "local", "invocation": "manual", "presentation": "application", "entry": "ui/index.html"}}, "ui": map[string]any{}}
	if _, err := store.SaveInstallation(context.Background(), installationSnapshot(t, store.ProjectKey(), pluginID, hash, manifest, 1, "enabled"), nil); err != nil {
		t.Fatal(err)
	}
	f.input = pluginSurfaceAPIRequest{ProjectKey: store.ProjectKey(), PluginID: pluginID, InstallationID: installationRecordID(t, store, pluginID), InstallationRevision: 1, PackageHash: hash,
		ActionID: "open", Entry: "ui/index.html", RequestID: "versions-request", SessionEpoch: 7, ActiveCollection: f.tables[0].id}
}

// regrant replaces the manifest data grants with a new installation revision.
func (f *versionsFixture) regrant(t *testing.T, grants []pluginReadGrant) {
	t.Helper()
	f.input = changeDataGrants(t, pluginstore.New(f.pb, f.workspace), f.input, grants)
}

func (f *versionsFixture) sources(fields ...string) []pluginVersionsSource {
	sources := []pluginVersionsSource{}
	for _, table := range f.tables {
		sources = append(sources, pluginVersionsSource{Collection: table.id, Fields: fields})
	}
	return sources
}

func versionsRequest(sources []pluginVersionsSource, prior map[string]pluginDatasetVersion) pluginVersionsRequest {
	request := pluginVersionsRequest{Contract: pluginVersionsContract, Sources: sources}
	for collection, version := range prior {
		if request.Versions == nil {
			request.Versions = map[string]json.RawMessage{}
		}
		raw, _ := json.Marshal(version)
		request.Versions[collection] = raw
	}
	return request
}

// versionsRawRequest builds a request whose prior versions are literal JSON so
// incomplete or malformed shapes can be exercised on the wire.
func versionsRawRequest(sources []pluginVersionsSource, versions string) json.RawMessage {
	raw, _ := json.Marshal(sources)
	return json.RawMessage(`{"contract":"` + pluginVersionsContract + `","sources":` + string(raw) + `,"versions":` + versions + `}`)
}

// versionsEchoRequest feeds a batch result back exactly as the wire carried
// it: the versions map is re-marshaled from the host result without passing
// through any Go zero value, so empty clock/status strings stay present.
func versionsEchoRequest(sources []pluginVersionsSource, result map[string]any) json.RawMessage {
	sourcesRaw, _ := json.Marshal(sources)
	versions, _ := json.Marshal(result["versions"])
	return json.RawMessage(`{"contract":"` + pluginVersionsContract + `","sources":` + string(sourcesRaw) + `,"versions":` + string(versions) + `}`)
}

// call executes the real dispatch entry; failures must carry a product code
// and never return a partial result.
func (f *versionsFixture) callCtx(ctx context.Context, t *testing.T, app core.App, request any) (map[string]any, string) {
	t.Helper()
	input := f.input
	input.Method = "data.versions"
	if raw, ok := request.(json.RawMessage); ok {
		input.Request = raw
	} else {
		input.Request, _ = json.Marshal(request)
	}
	result, err := executePluginSurfaceAPI(ctx, app, f.source, nil, f.workspace, input, nil)
	if err == nil {
		versions, _ := result.(map[string]any)
		if versions == nil {
			t.Fatalf("unexpected result %+v", result)
		}
		return versions, ""
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

func (f *versionsFixture) call(t *testing.T, app core.App, request any) (map[string]any, string) {
	t.Helper()
	return f.callCtx(context.Background(), t, app, request)
}

func versionsOf(t *testing.T, result map[string]any, collection string) pluginDatasetVersion {
	t.Helper()
	versions, _ := result["versions"].(map[string]pluginDatasetVersion)
	version, ok := versions[collection]
	if !ok {
		t.Fatalf("missing version for %s: %+v", collection, result)
	}
	return version
}

// bump advances the data revision of several tables in one statement, the way
// the mutation kernel does, without touching business rows.
func (f *versionsFixture) bump(t *testing.T, tables ...versionsTable) {
	t.Helper()
	if err := f.bumpErr(tables...); err != nil {
		t.Fatal(err)
	}
}

func (f *versionsFixture) bumpErr(tables ...versionsTable) error {
	ids := make([]any, 0, len(tables))
	for _, table := range tables {
		ids = append(ids, table.id)
	}
	_, err := f.pb.DB().Update("vibetable_tables", dbx.Params{"data_revision": dbx.NewExp("data_revision + 1")}, dbx.In("table_id", ids...)).Execute()
	return err
}

// versionsSQLRecorder records every SQL statement together with the
// transaction boundary it ran in (dbx logs the bound statement text).
type versionsSQLRecorder struct {
	mu         sync.Mutex
	inTx       bool
	statements []recordedSQL
}

type recordedSQL struct {
	statement string
	inTx      bool
}

func (r *versionsSQLRecorder) log(_ context.Context, _ time.Duration, statement string, _ *sql.Rows, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, recordedSQL{statement: statement, inTx: r.inTx})
}

// versionsTxHook counts top-level transactions on behalf of the recorder.
type versionsTxHook struct {
	core.App
	recorder *versionsSQLRecorder
	count    int32
}

func (h *versionsTxHook) RunInTransaction(fn func(core.App) error) error {
	atomic.AddInt32(&h.count, 1)
	h.recorder.mu.Lock()
	h.recorder.inTx = true
	h.recorder.mu.Unlock()
	defer func() {
		h.recorder.mu.Lock()
		h.recorder.inTx = false
		h.recorder.mu.Unlock()
	}()
	return h.App.RunInTransaction(fn)
}

func (r *versionsSQLRecorder) snapshot() []recordedSQL {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedSQL{}, r.statements...)
}

func TestPluginSurfaceDataVersionsOneTransactionReadsOnlyMetadataInsideIt(t *testing.T) {
	f := newVersionsFixture(t, 2)
	recorder := &versionsSQLRecorder{}
	// dbx copies the log hook when a transaction begins, so both pools are hooked.
	for _, pool := range []dbx.Builder{f.pb.ConcurrentDB(), f.pb.NonconcurrentDB()} {
		pool.(*dbx.DB).QueryLogFunc = recorder.log
	}
	hook := &versionsTxHook{App: f.pb, recorder: recorder}
	// Per-table projections differ: table 0 projects name, table 1 projects secret.
	request := versionsRequest([]pluginVersionsSource{
		{Collection: f.tables[0].id, Fields: []string{"id", f.tables[0].nameID}},
		{Collection: f.tables[1].id, Fields: []string{"id", f.tables[1].secretID}},
	}, nil)
	result, code := f.call(t, hook, request)
	if code != "" {
		t.Fatal(code)
	}
	if result["contract"] != pluginVersionsContract {
		t.Fatalf("contract: %+v", result)
	}
	if _, hasStale := result["stale"]; hasStale {
		t.Fatalf("stale without prior versions: %+v", result)
	}
	if atomic.LoadInt32(&hook.count) != 1 {
		t.Fatalf("transactions opened: %d, want 1", hook.count)
	}
	statements := recorder.snapshot()
	if len(statements) == 0 {
		t.Fatal("no SQL recorded")
	}
	for _, recorded := range statements {
		if !recorded.inTx {
			t.Fatalf("read outside the transaction: %s", recorded.statement)
		}
		for _, table := range f.tables {
			if strings.Contains(recorded.statement, table.physical) {
				t.Fatalf("business table %s was read: %s", table.physical, recorded.statement)
			}
		}
	}
	for _, table := range f.tables {
		if version := versionsOf(t, result, table.id); version.Schema == "" || version.Data != 0 || version.Computed == nil || len(version.Computed) != 0 {
			t.Fatalf("table %s version: %+v", table.id, version)
		}
	}
	// Unchanged prior versions come back verbatim: nothing is stale and still
	// exactly one transaction is opened per call.
	prior := map[string]pluginDatasetVersion{}
	for _, table := range f.tables {
		prior[table.id] = versionsOf(t, result, table.id)
	}
	again, code := f.call(t, hook, versionsRequest(request.Sources, prior))
	if code != "" || fmt.Sprint(again["stale"]) != "[]" || fmt.Sprint(again["versions"]) != fmt.Sprint(result["versions"]) {
		t.Fatalf("unchanged versions reported stale: %+v %s", again, code)
	}
	if atomic.LoadInt32(&hook.count) != 2 {
		t.Fatalf("each call must open exactly one transaction: %d", hook.count)
	}
	// A data revision advance on table 1 only marks that source stale.
	f.bump(t, f.tables[1])
	changed, code := f.call(t, hook, versionsRequest(request.Sources, prior))
	if code != "" {
		t.Fatal(code)
	}
	if fmt.Sprint(changed["stale"]) != fmt.Sprint([]string{f.tables[1].id}) {
		t.Fatalf("stale sources: %+v", changed["stale"])
	}
	if !reflect.DeepEqual(versionsOf(t, changed, f.tables[0].id), prior[f.tables[0].id]) || versionsOf(t, changed, f.tables[1].id).Data != 1 {
		t.Fatalf("versions after write: %+v", changed["versions"])
	}
	// A narrowed grant (only id left) fails the whole batch; previously
	// authorized sensitive columns are never reported reusable, and the
	// failure still opens no extra transaction and returns nothing.
	f.regrant(t, []pluginReadGrant{
		{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id"}},
		{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[1].secretID}},
	})
	if result, code = f.call(t, hook, request); code != "plugin_read_denied" || result != nil {
		t.Fatalf("narrowed grant admitted: %+v %q", result, code)
	}
	if atomic.LoadInt32(&hook.count) != 4 {
		t.Fatalf("failed batch transactions: %d", hook.count)
	}
	narrowed := versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id"}}}, nil)
	if result, code = f.call(t, f.pb, narrowed); code != "" {
		t.Fatal(code)
	}
	// Authorization identity is rechecked inside the same transaction.
	original := f.input
	f.input.InstallationRevision++
	if _, code = f.call(t, f.pb, narrowed); code != "plugin_cursor_stale" {
		t.Fatalf("stale installation admitted: %q", code)
	}
	f.input = original
	f.input.Entry = "ui/other.html"
	if _, code = f.call(t, f.pb, narrowed); code != "plugin_capability_invalid" {
		t.Fatalf("unbound entry admitted: %q", code)
	}
	f.input = original
	// No partial results: a failure never returns the versions of other sources.
	mixed := versionsRequest([]pluginVersionsSource{{Collection: f.tables[1].id, Fields: []string{"id", f.tables[1].secretID}}, {Collection: f.tables[0].id, Fields: []string{"id", f.tables[0].secretID}}}, nil)
	if result, code = f.call(t, f.pb, mixed); code != "plugin_read_denied" || result != nil {
		t.Fatalf("partial failure returned results: %+v %q", result, code)
	}
}

func TestPluginSurfaceDataVersionsFortyTablesAndBatchLimits(t *testing.T) {
	f := newVersionsFixture(t, 40)
	result, code := f.call(t, f.pb, versionsRequest(f.sources("id"), nil))
	if code != "" {
		t.Fatal(code)
	}
	versions, _ := result["versions"].(map[string]pluginDatasetVersion)
	if len(versions) != 40 {
		t.Fatalf("versions: %+v", result["versions"])
	}
	for _, table := range f.tables {
		if version := versionsOf(t, result, table.id); version.Schema == "" || version.Data != 0 {
			t.Fatalf("table %s: %+v", table.id, version)
		}
	}
	one := pluginVersionsSource{Collection: f.tables[0].id, Fields: []string{"id"}}
	overFields := pluginVersionsSource{Collection: f.tables[0].id}
	for i := 0; i < pluginVersionsMaxFields+1; i++ {
		overFields.Fields = append(overFields.Fields, fmt.Sprintf("f%d", i))
	}
	over := f.sources("id")
	for i := len(over); i < pluginVersionsMaxSources+1; i++ {
		over = append(over, pluginVersionsSource{Collection: fmt.Sprintf("extra-%d", i), Fields: []string{"id"}})
	}
	// Known versions must be complete for exactly the requested sources.
	prior := map[string]pluginDatasetVersion{f.tables[0].id: {Schema: "s", Clock: "c", Computed: map[string]pluginDatasetComputedRevision{}}}
	invalid := map[string]pluginVersionsRequest{
		"over-limit-sources":   versionsRequest(over, nil),
		"over-limit-fields":    versionsRequest([]pluginVersionsSource{overFields}, nil),
		"duplicate-collection": versionsRequest([]pluginVersionsSource{one, one}, nil),
		"duplicate-field":      versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", "id"}}}, nil),
		"empty-fields":         versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{}}}, nil),
		"relation-path":        versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", "other.name"}}}, nil),
		"no-sources":           versionsRequest(nil, nil),
		"incomplete-prior":     versionsRequest([]pluginVersionsSource{one, {Collection: f.tables[1].id, Fields: []string{"id"}}}, prior),
		"extra-prior":          versionsRequest([]pluginVersionsSource{{Collection: f.tables[1].id, Fields: []string{"id"}}}, prior),
		"unsafe-prior": versionsRequest([]pluginVersionsSource{one}, map[string]pluginDatasetVersion{
			f.tables[0].id: {Schema: "s", Clock: "c", Data: 1 << 53, Computed: map[string]pluginDatasetComputedRevision{}},
		}),
		"wrong-contract": {Contract: "vibetable.plugin-data.v2", Sources: []pluginVersionsSource{one}},
	}
	for name, request := range invalid {
		if result, code := f.call(t, f.pb, request); code != "plugin_capability_invalid" || result != nil {
			t.Fatalf("%s: %+v %q", name, result, code)
		}
	}
	complete := versionsRequest([]pluginVersionsSource{one}, prior)
	if _, code = f.call(t, f.pb, complete); code != "" {
		t.Fatalf("complete prior versions: %q", code)
	}
	// A prior version is accepted only as the host emitted it. A missing or
	// null scalar must never be read as its Go zero value: a fresh table whose
	// dataRevision really is 0 would then compare equal to an "unknown" one.
	for name, raw := range map[string]string{
		"null-prior":                  "null",
		"duplicate-prior-source":      `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"","computed":{}},"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"","computed":{}}}`,
		"duplicate-revision":          `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"dataRevision":0,"clockPeriod":"","computed":{}}}`,
		"wrong-revision-case":         `{"` + f.tables[0].id + `":{"schemaRevision":"x","DataRevision":0,"clockPeriod":"","computed":{}}}`,
		"missing-data-revision":       `{"` + f.tables[0].id + `":{"schemaRevision":"x","clockPeriod":"c","computed":{}}}`,
		"null-data-revision":          `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":null,"clockPeriod":"c","computed":{}}}`,
		"fractional-revision":         `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0.5,"clockPeriod":"c","computed":{}}}`,
		"negative-revision":           `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":-1,"clockPeriod":"c","computed":{}}}`,
		"missing-schema":              `{"` + f.tables[0].id + `":{"dataRevision":0,"clockPeriod":"c","computed":{}}}`,
		"null-clock":                  `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":null,"computed":{}}}`,
		"null-computed":               `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":null}}`,
		"missing-computed":            `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c"}}`,
		"unknown-key":                 `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{},"fields":["id"]}}`,
		"null-version":                `{"` + f.tables[0].id + `":null}`,
		"computed-missing-ready":      `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"fld":{"definition":1,"watermark":"w","status":"ready"}}}}`,
		"computed-null-ready":         `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"fld":{"definition":1,"watermark":"w","ready":null,"status":"ready"}}}}`,
		"computed-unknown-key":        `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"fld":{"definition":1,"watermark":"w","ready":true,"status":"ready","extra":1}}}}`,
		"computed-duplicate-key":      `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"fld":{"definition":1,"watermark":"w","ready":true,"ready":true,"status":"ready"}}}}`,
		"computed-unsafe-definition":  `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"fld":{"definition":9007199254740992,"watermark":"w","ready":true,"status":"ready"}}}}`,
		"computed-outside-projection": `{"` + f.tables[0].id + `":{"schemaRevision":"x","dataRevision":0,"clockPeriod":"c","computed":{"other":{"definition":1,"watermark":"w","ready":true,"status":"ready"}}}}`,
	} {
		raw = strings.ReplaceAll(raw, `"fld"`, `"`+f.tables[0].nameID+`"`)
		request := versionsRawRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", f.tables[0].nameID}}}, raw)
		if result, code := f.call(t, f.pb, request); code != "plugin_capability_invalid" || result != nil {
			t.Fatalf("%s: %+v %q", name, result, code)
		}
	}
	// The same fresh version, passed back with every key present, is complete
	// and therefore not stale although its dataRevision is 0.
	fresh := versionsOf(t, versionsResultFor(t, f, f.tables[0].id), f.tables[0].id)
	if fresh.Data != 0 {
		t.Fatalf("fixture table is not fresh: %+v", fresh)
	}
	if echoed, code := f.call(t, f.pb, versionsRawRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id"}}},
		`{"`+f.tables[0].id+`":{"schemaRevision":"`+fresh.Schema+`","dataRevision":0,"clockPeriod":"`+fresh.Clock+`","computed":{}}}`)); code != "" || fmt.Sprint(echoed["stale"]) != "[]" {
		t.Fatalf("complete fresh prior version reported stale: %+v %q", echoed, code)
	}
	// The whole 40-source vector survives a verbatim wire round trip.
	if echoed, code := f.call(t, f.pb, versionsEchoRequest(f.sources("id"), result)); code != "" || fmt.Sprint(echoed["stale"]) != "[]" {
		t.Fatalf("verbatim 40-source round trip reported stale: %+v %q", echoed, code)
	}
	for _, raw := range []string{
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[],"extra":1}`,
		`{"Contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","fields":["id"]}]}`,
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","Fields":["id"]}]}`,
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","fields":["id"],"fields":["id"]}]}`,
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","fields":["id",null]}]}`,
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","fields":["id"]}]} {}`,
		`{"contract":"vibetable.plugin-data-versions.v1","sources":[{"collection":"` + f.tables[0].id + `","fields":["id"],"unknown":true}]}`,
	} {
		input := f.input
		input.Method = "data.versions"
		input.Request = json.RawMessage(raw)
		if _, err := executePluginSurfaceAPI(context.Background(), f.pb, f.source, nil, f.workspace, input, nil); err == nil {
			t.Fatalf("malformed wire request admitted: %s", raw)
		}
	}
	// The exact upper bound is admitted: 64 sources, each projecting its own granted column.
	bounded := newVersionsFixture(t, pluginVersionsMaxSources)
	limit := make([]pluginVersionsSource, 0, len(bounded.tables))
	for _, table := range bounded.tables {
		limit = append(limit, pluginVersionsSource{Collection: table.id, Fields: []string{"id", table.secretID}})
	}
	if result, code = bounded.call(t, bounded.pb, versionsRequest(limit, nil)); code != "" || len(result["versions"].(map[string]pluginDatasetVersion)) != pluginVersionsMaxSources {
		t.Fatalf("64 sources: %+v %q", result["versions"], code)
	}
	if result, code = bounded.call(t, bounded.pb, versionsEchoRequest(limit, result)); code != "" || fmt.Sprint(result["stale"]) != "[]" {
		t.Fatalf("64-source verbatim round trip: %+v %q", result, code)
	}
}

func versionsResultFor(t *testing.T, f *versionsFixture, collection string) map[string]any {
	t.Helper()
	result, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: collection, Fields: []string{"id"}}}, nil))
	if code != "" {
		t.Fatal(code)
	}
	return result
}

func TestPluginSurfaceDataVersionsChecksGrantBeforeExistence(t *testing.T) {
	f := newVersionsFixture(t, 2)
	// Only table 0 is granted; table 1 exists but is unauthorized, and a
	// missing collection is unauthorized too: both fail identically.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[0].secretID}}})
	granted := pluginVersionsSource{Collection: f.tables[0].id, Fields: []string{"id"}}
	for name, request := range map[string]pluginVersionsRequest{
		"unauthorized-table": versionsRequest([]pluginVersionsSource{granted, {Collection: f.tables[1].id, Fields: []string{"id"}}}, nil),
		"missing-table":      versionsRequest([]pluginVersionsSource{granted, {Collection: "00000000-0000-4000-8000-000000000000", Fields: []string{"id"}}}, nil),
		"ungranted-field":    versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", f.tables[0].nameID}}}, nil),
		// An unknown field is denied exactly like an ungranted one: no existence probe.
		"unknown-field": versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", "missing"}}}, nil),
	} {
		if result, code := f.call(t, f.pb, request); code != "plugin_read_denied" || result != nil {
			t.Fatalf("%s: %+v %q", name, result, code)
		}
	}
	// A grant without read+query (write-only) authorizes no version read.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[0].id, Operations: []string{"create", "update"}, Fields: []string{"id"}}})
	if result, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{granted}, nil)); code != "plugin_read_denied" || result != nil {
		t.Fatalf("write-only grant: %+v %q", result, code)
	}
	// A granted collection whose table vanished is a query failure, never a
	// capability claim and never a fallback to another resolution path.
	f.regrant(t, []pluginReadGrant{{Collection: "11111111-2222-4333-8444-555555555555", Operations: []string{"read", "query"}, Fields: []string{"id"}}})
	if result, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: "11111111-2222-4333-8444-555555555555", Fields: []string{"id"}}}, nil)); code != "plugin_query_failed" || result != nil {
		t.Fatalf("vanished table: %+v %q", result, code)
	}
	// $active follows ordinary single-table semantics.
	f.regrant(t, []pluginReadGrant{{Collection: "$active", Operations: []string{"read", "query"}, Fields: []string{"*"}}})
	if _, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id", f.tables[0].nameID, f.tables[0].secretID}}}, nil)); code != "" {
		t.Fatalf("wildcard active grant: %q", code)
	}
	if _, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: f.tables[1].id, Fields: []string{"id"}}}, nil)); code != "plugin_read_denied" {
		t.Fatalf("$active resolved to a non-active table: %q", code)
	}
	// Grants by physical column name cover the same canonical field.
	f.regrant(t, []pluginReadGrant{{Collection: f.tables[1].id, Operations: []string{"read", "query"}, Fields: []string{"id", f.tables[1].secretPhysical}}})
	if result, code := f.call(t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: f.tables[1].id, Fields: []string{"id", f.tables[1].secretID}}}, nil)); code != "" || versionsOf(t, result, f.tables[1].id).Schema == "" {
		t.Fatalf("physical-name grant: %+v %q", result, code)
	}
}

func TestPluginSurfaceDataVersionsConcurrentWritesKeepOneConsistentVector(t *testing.T) {
	f := newVersionsFixture(t, 2)
	pair := []versionsTable{f.tables[0], f.tables[1]}
	request := versionsRequest(f.sources("id"), nil)
	first, code := f.call(t, f.pb, request)
	if code != "" {
		t.Fatal(code)
	}
	before := versionsOf(t, first, pair[0].id).Data
	// Both tables always advance together in one statement, so every batch must
	// observe equal data revisions regardless of interleaving with the writer.
	const bumps = 40
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		defer close(stop)
		for i := 0; i < bumps; i++ {
			if err := f.bumpErr(pair...); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	seen, during := map[int64]bool{}, 0
	for {
		result, code := f.call(t, f.pb, request)
		if code != "" {
			t.Fatal(code)
		}
		a, b := versionsOf(t, result, pair[0].id).Data, versionsOf(t, result, pair[1].id).Data
		if a != b || a < before || a > bumps {
			t.Fatalf("inconsistent vector: %d/%d before %d", a, b, before)
		}
		seen[a] = true
		select {
		case <-stop:
			writer.Wait()
			if during == 0 {
				t.Fatalf("no batch overlapped the writer: %v", seen)
			}
			final, code := f.call(t, f.pb, request)
			if code != "" {
				t.Fatal(code)
			}
			if finalA, finalB := versionsOf(t, final, pair[0].id).Data, versionsOf(t, final, pair[1].id).Data; finalA != bumps || finalB != bumps {
				t.Fatalf("final revisions: %d/%d", finalA, finalB)
			}
			prior := map[string]pluginDatasetVersion{}
			for _, table := range pair {
				prior[table.id] = versionsOf(t, final, table.id)
			}
			last, code := f.call(t, f.pb, versionsRequest(request.Sources, prior))
			if code != "" || fmt.Sprint(last["stale"]) != "[]" {
				t.Fatalf("final comparison: %+v %q", last, code)
			}
			// The versions observed before the writes are now both stale, and the
			// stale list covers every requested source without partial results.
			stale, code := f.call(t, f.pb, versionsRequest(request.Sources, map[string]pluginDatasetVersion{pair[0].id: versionsOf(t, first, pair[0].id), pair[1].id: versionsOf(t, first, pair[1].id)}))
			if code != "" || len(stale["stale"].([]string)) != 2 {
				t.Fatalf("pre-write versions not stale: %+v %q", stale, code)
			}
			return
		default:
			during++
		}
	}
}

func createVersionsFormula(t *testing.T, pb *pocketbase.PocketBase, tableID, operationID, name, expression string) v2.FieldDefinition {
	t.Helper()
	ctx := context.Background()
	defaults, err := v2.RecommendedDefaults(v2.LogicalFormula)
	if err != nil {
		t.Fatal(err)
	}
	backfill := jobs.New(pb, nil)
	t.Cleanup(backfill.Shutdown)
	catalog := fieldchange.NewCatalog(pb)
	revisions, err := catalog.Revisions(ctx, tableID)
	if err != nil {
		t.Fatal(err)
	}
	plans := fieldchange.NewPocketBasePlanStore(pb)
	actor := v2.Actor{ID: "local-user", Kind: "user"}
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: tableID, ExpectedSchemaRev: revisions.Schema, Actor: actor, Draft: &v2.FieldDraft{DisplayName: name, LogicalType: v2.LogicalFormula, Value: defaults.Value, Constraints: defaults.Constraints, Storage: defaults.Storage, Display: defaults.Display, Formula: &v2.FormulaDraftSpec{Language: "cel-v2", Source: expression}}}
	plan, err := fieldchange.NewPlanner(catalog, catalog, plans, v2.NewIdentityAllocator(nil)).Plan(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fieldchange.NewExecutor(pb, plans, fieldchange.WithFormulaBackfillScheduler(pluginComputedBackfillQueue{backfill})).Apply(ctx, v2.ApplyRequest{PlanID: plan.PlanID, PlanHash: plan.PlanHash, OperationID: operationID, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := pb.FindFirstRecordByFilter("vibetable_formulas", "table_id={:table} && field_id={:field}", dbx.Params{"table": tableID, "field": receipt.Definition.Identity.FieldID})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Set("status", "ready")
	if err = pb.Save(runtime); err != nil {
		t.Fatal(err)
	}
	return *receipt.Definition
}

func TestPluginSurfaceDataVersionsAuthorizesSameRowFormulaClosure(t *testing.T) {
	f := newVersionsFixture(t, 2)
	local, other := f.tables[0], f.tables[1]
	input := createVersionsFormula(t, f.pb, local.id, "version-local-input", "Local input", local.secretPhysical)
	output := createVersionsFormula(t, f.pb, local.id, "version-local-output", "Local output", input.Identity.PhysicalName)
	number := createSchemaProductField(t, f.pb, other.id, v2.LogicalNumber, "Quantity", "version-foreign-input").Definition
	foreign := createVersionsFormula(t, f.pb, local.id, "version-foreign-output", "Foreign output",
		fmt.Sprintf(`SUM(PROJECT(TABLE("%s"), CurrentValue.%s))`, other.id, number.Identity.PhysicalName))
	grant := func(fields ...string) {
		f.regrant(t, []pluginReadGrant{{Collection: local.id, Operations: []string{"read", "query"}, Fields: fields}})
	}
	project := func(id string) []pluginVersionsSource {
		return []pluginVersionsSource{{Collection: local.id, Fields: []string{id}}}
	}
	grant(output.Identity.FieldID, input.Identity.FieldID, local.secretID)
	result, code := f.call(t, f.pb, versionsRequest(project(output.Identity.FieldID), nil))
	if code != "" {
		t.Fatal(code)
	}
	version := versionsOf(t, result, local.id)
	computed := version.Computed[output.Identity.FieldID]
	if len(version.Computed) != 1 || !computed.Ready || !strings.Contains(computed.Watermark, "|definitions:") || version.Clock != "" {
		t.Fatalf("local formula version or nonvolatile clock: %+v", version)
	}
	if echoed, code := f.call(t, f.pb, versionsEchoRequest(project(output.Identity.FieldID), result)); code != "" || fmt.Sprint(echoed["stale"]) != "[]" {
		t.Fatalf("canonical projected prior: %+v %q", echoed, code)
	}
	// Dataset keys computed fields by their physical names. A data.versions
	// response keys them by canonical field IDs, as its request projection does.
	version.Computed = map[string]pluginDatasetComputedRevision{output.Identity.PhysicalName: computed}
	if _, code := f.call(t, f.pb, versionsRequest(project(output.Identity.FieldID), map[string]pluginDatasetVersion{local.id: version})); code != "plugin_capability_invalid" {
		t.Fatalf("physical computed key admitted as prior: %q", code)
	}
	grant(output.Identity.FieldID, input.Identity.FieldID)
	if _, code := f.call(t, f.pb, versionsRequest(project(output.Identity.FieldID), nil)); code != "plugin_read_denied" {
		t.Fatalf("missing transitive same-row grant: %q", code)
	}
	grant(foreign.Identity.FieldID)
	if _, code := f.call(t, f.pb, versionsRequest(project(foreign.Identity.FieldID), nil)); code != "plugin_query_computed_unsupported" {
		t.Fatalf("unsupported cross-table formula: %q", code)
	}
}

func TestPluginSurfaceDataVersionsPinsOneEvaluationTimePerBatch(t *testing.T) {
	f := newVersionsFixture(t, 3)
	var pins int32
	instants := []time.Time{}
	original := pluginVersionsEvaluationTime
	pluginVersionsEvaluationTime = func(ctx context.Context) context.Context {
		atomic.AddInt32(&pins, 1)
		pinned := formula.EnsureEvaluationTime(ctx)
		instants = append(instants, formula.EvaluationTime(pinned))
		return pinned
	}
	t.Cleanup(func() { pluginVersionsEvaluationTime = original })
	if _, code := f.call(t, f.pb, versionsRequest(f.sources("id"), nil)); code != "" {
		t.Fatal(code)
	}
	if atomic.LoadInt32(&pins) != 1 || len(instants) != 1 || instants[0].IsZero() {
		t.Fatalf("evaluation time pinned %d times for %d sources: %v", pins, len(f.tables), instants)
	}
	// An instant already pinned by the host is kept, never replaced.
	pinned := formula.WithEvaluationTime(context.Background(), time.Date(2025, 1, 1, 2, 3, 0, 0, time.UTC))
	if _, code := f.callCtx(pinned, t, f.pb, versionsRequest(f.sources("id"), nil)); code != "" {
		t.Fatal(code)
	}
	if atomic.LoadInt32(&pins) != 2 || !instants[1].Equal(time.Date(2025, 1, 1, 2, 3, 0, 0, time.UTC)) {
		t.Fatalf("host instant not kept: pins=%d instants=%v", pins, instants)
	}
}

func TestPluginSurfaceDataVersionsComputedEntriesShareOneClockPeriod(t *testing.T) {
	f := newVersionsFixture(t, 2)
	ctx := context.Background()
	minute := make([]v2.FieldDefinition, len(f.tables))
	grants := []any{}
	for i, table := range f.tables {
		minute[i] = createVersionsFormula(t, f.pb, table.id, fmt.Sprintf("versions-minute-%d", i), "Minute", "NOW()")
		grants = append(grants, map[string]any{"collection": table.id, "operations": []string{"read", "query"}, "fields": []string{"id", table.nameID, table.secretID, minute[i].Identity.FieldID}})
	}
	// Re-install with the computed field granted on every table.
	store := pluginstore.New(f.pb, f.workspace)
	raw, err := store.GetInstallation(ctx, f.input.ProjectKey, f.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var installation map[string]any
	if err = json.Unmarshal(raw, &installation); err != nil {
		t.Fatal(err)
	}
	installation["manifest"].(map[string]any)["permissions"].(map[string]any)["data"] = grants
	previous := f.input.InstallationRevision
	f.input.InstallationRevision++
	installation["revision"] = f.input.InstallationRevision
	raw, _ = json.Marshal(installation)
	if _, err = store.SaveInstallation(ctx, raw, &previous); err != nil {
		t.Fatal(err)
	}
	// One explicit instant for the whole batch: every source's NOW() computed
	// version must carry the same minute period, keyed by canonical field ID,
	// and non-projected computed fields must not appear.
	instant := time.Date(2025, 6, 1, 12, 30, 0, 0, time.UTC)
	ctx = formula.WithEvaluationTime(ctx, instant)
	sources := []pluginVersionsSource{
		{Collection: f.tables[0].id, Fields: []string{"id", minute[0].Identity.FieldID}},
		{Collection: f.tables[1].id, Fields: []string{"id", f.tables[1].secretID, minute[1].Identity.FieldID}},
	}
	result, code := f.callCtx(ctx, t, f.pb, versionsRequest(sources, nil))
	if code != "" {
		t.Fatal(code)
	}
	for i, table := range f.tables {
		version := versionsOf(t, result, table.id)
		entry, ok := version.Computed[minute[i].Identity.FieldID]
		if !ok || entry.Definition < 1 || !entry.Ready || !strings.HasSuffix(entry.Watermark, "minute:2025-06-01T12:30:00Z") {
			t.Fatalf("table %d computed version: %+v", i, versionsOf(t, result, table.id).Computed)
		}
		if version.Clock != "minute:2025-06-01T12:30:00Z" {
			t.Fatalf("table %d clock period: %q", i, version.Clock)
		}
	}
	if versionsOf(t, result, f.tables[0].id).Computed[minute[1].Identity.FieldID].Definition != 0 || len(versionsOf(t, result, f.tables[0].id).Computed) != 1 {
		t.Fatalf("foreign computed entries leaked: %+v", versionsOf(t, result, f.tables[0].id).Computed)
	}
	// A verbatim wire round trip (ready entries carry an empty status string)
	// is complete and therefore not stale.
	if echoed, code := f.callCtx(ctx, t, f.pb, versionsEchoRequest(sources, result)); code != "" || fmt.Sprint(echoed["stale"]) != "[]" {
		t.Fatalf("verbatim computed round trip reported stale: %+v %q", echoed, code)
	}
	plain, code := f.callCtx(ctx, t, f.pb, versionsRequest([]pluginVersionsSource{{Collection: f.tables[0].id, Fields: []string{"id"}}}, nil))
	if code != "" || len(versionsOf(t, plain, f.tables[0].id).Computed) != 0 || versionsOf(t, plain, f.tables[0].id).Clock != "" {
		t.Fatalf("non-projected computed field reported: %+v %q", plain, code)
	}
	// A later minute is a new version for every source at once.
	later, code := f.callCtx(formula.WithEvaluationTime(context.Background(), instant.Add(time.Minute)), t, f.pb, versionsRequest(sources, nil))
	if code != "" {
		t.Fatal(code)
	}
	if !strings.HasSuffix(versionsOf(t, later, f.tables[0].id).Computed[minute[0].Identity.FieldID].Watermark, "minute:2025-06-01T12:31:00Z") || !strings.HasSuffix(versionsOf(t, later, f.tables[1].id).Computed[minute[1].Identity.FieldID].Watermark, "minute:2025-06-01T12:31:00Z") {
		t.Fatalf("later instant not reflected: %+v", later["versions"])
	}
	prior := map[string]pluginDatasetVersion{}
	for _, table := range f.tables {
		prior[table.id] = versionsOf(t, result, table.id)
	}
	compared, code := f.callCtx(formula.WithEvaluationTime(context.Background(), instant.Add(time.Minute)), t, f.pb, versionsRequest(sources, prior))
	if code != "" || fmt.Sprint(compared["stale"]) != fmt.Sprint([]string{f.tables[0].id, f.tables[1].id}) {
		t.Fatalf("clock change must mark every computed source stale: %+v %q", compared["stale"], code)
	}
}

func TestPluginSurfaceDataVersionsReuseProgressCacheAndModelGrantsInsideTransaction(t *testing.T) {
	// The fixture commits one model mapping: mapped.FieldID is only readable
	// through the completed binding that surfaceInstallation resolves inside the
	// versions transaction; the unbound replacement column stays denied.
	f := newDataReadFixture(t, true)
	input := f.input
	input.Method = "data.versions"
	call := func(fields ...string) (map[string]any, string) {
		input.Request, _ = json.Marshal(versionsRequest([]pluginVersionsSource{{Collection: f.mapped.TableID, Fields: fields}}, nil))
		result, err := executePluginSurfaceAPI(context.Background(), f.pb, f.source, nil, f.workspace, input, nil)
		if err == nil {
			return result.(map[string]any), ""
		}
		var product *query.ProductError
		if !errors.As(err, &product) || result != nil {
			t.Fatalf("unexpected failure: %v %+v", err, result)
		}
		return nil, product.Code
	}
	// The surface dispatch shares the #588 progress cache: the second batch
	// resolves the same binding from the cache instead of re-reading history.
	before := pluginmodel.ProgressCacheStatsFor(f.pb)
	first, code := call("id", f.mapped.FieldID)
	if code != "" {
		t.Fatal(code)
	}
	if _, code = call("id", f.replacement.FieldID); code != "plugin_read_denied" {
		t.Fatalf("unbound column admitted: %q", code)
	}
	second, code := call("id", f.mapped.FieldID)
	if code != "" || fmt.Sprint(second["versions"]) != fmt.Sprint(first["versions"]) {
		t.Fatalf("second read: %+v %q", second, code)
	}
	after := pluginmodel.ProgressCacheStatsFor(f.pb)
	if after.Reads <= before.Reads || after.Hits <= before.Hits {
		t.Fatalf("progress cache was not used by data.versions: before %+v after %+v", before, after)
	}
	if version := versionsOf(t, first, f.mapped.TableID); version.Schema == "" || version.Data != 0 || version.Computed == nil {
		t.Fatalf("mapped table version: %+v", version)
	}
	table, err := schemaexecution.Describe(t.Context(), f.pb, f.mapped.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := f.pb.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	collection.Fields.GetByName(f.mapped.Definition.Identity.PhysicalName).SetHidden(true)
	if err := f.pb.Save(collection); err != nil {
		t.Fatal(err)
	}
	if result, code := call("id", f.mapped.FieldID); result != nil || code != "plugin_model_binding_stale" {
		t.Fatalf("changed binding returned %+v, %s", result, code)
	}
}

func TestPluginSurfaceDataVersionsHTTPWirePath(t *testing.T) {
	f := newVersionsFixture(t, 1)
	kernel := mutation.New(f.pb, mutation.MetadataSchemaSource{})
	r := router.NewRouter(func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		return &core.RequestEvent{Event: router.Event{Response: w, Request: req}}, nil
	})
	registerPluginSurfaceAPIRoutes(r, f.pb, f.source, kernel, f.workspace, 7, nil)
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	send := func(method string, request any) *httptest.ResponseRecorder {
		t.Helper()
		input := f.input
		input.Method = method
		input.Request, _ = json.Marshal(request)
		wire, _ := json.Marshal(input)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, pluginSurfaceAPIPath, bytes.NewReader(wire)))
		return response
	}
	ok := send("data.versions", versionsRequest(f.sources("id", f.tables[0].secretID), nil))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), pluginVersionsContract) || !strings.Contains(ok.Body.String(), `"dataRevision":0`) || !strings.Contains(ok.Body.String(), `"computed":{}`) {
		t.Fatalf("wire success: %d %s", ok.Code, ok.Body)
	}
	denied := send("data.versions", versionsRequest([]pluginVersionsSource{{Collection: "11111111-2222-4333-8444-555555555555", Fields: []string{"id"}}}, nil))
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), "plugin_read_denied") || strings.Contains(denied.Body.String(), "schemaRevision") {
		t.Fatalf("wire denial leaked versions: %d %s", denied.Code, denied.Body)
	}
	unsupported := send("data.versions.v2", versionsRequest(f.sources("id"), nil))
	if unsupported.Code != http.StatusConflict || !strings.Contains(unsupported.Body.String(), "plugin_api_unsupported") {
		t.Fatalf("unknown method: %d %s", unsupported.Code, unsupported.Body)
	}
}
