package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	wb "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

type surfaceOracleCase struct {
	Name string `json:"name"`
	Seed []struct {
		LogicalID string          `json:"logicalId"`
		Revision  string          `json:"revision"`
		Values    json.RawMessage `json:"values"`
	} `json:"seed"`
	Failure *string `json:"failure"`
	Calls   []struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	} `json:"calls"`
	Responses []struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		} `json:"error"`
	} `json:"responses"`
}

func surfaceOracle(t *testing.T) []surfaceOracleCase {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/v2/surface-python-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Producer string              `json:"producer"`
		Cases    []surfaceOracleCase `json:"cases"`
	}
	if json.Unmarshal(raw, &corpus) != nil || corpus.Producer != "12556e5db81dd49592d69b5af1780007ccd36c37" || len(corpus.Cases) != 78 {
		t.Fatal("invalid frozen oracle")
	}
	return corpus.Cases
}
func terminateSurfaceTestApp(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	if !app.IsBootstrapped() {
		return
	}
	event := &core.TerminateEvent{App: app}
	if err := app.OnTerminate().Trigger(event, func(event *core.TerminateEvent) error {
		return event.App.ResetBootstrapState()
	}); err != nil {
		t.Fatalf("terminate fixture: %v", err)
	}
}
func bootstrapSurfaceTestApp(t *testing.T, directory string) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: directory, HideStartBanner: true})
	migrations.Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminateSurfaceTestApp(t, app) })
	return app
}
func newSurfaceTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := bootstrapSurfaceTestApp(t, t.TempDir())
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}
func surfaceOracleAppFactory(t *testing.T) func(*testing.T) *pocketbase.PocketBase {
	t.Helper()
	baseline := newSurfaceTestApp(t)
	// Close both databases before capturing the migrated, empty fixture. Each
	// sample gets separate database files and a fresh PocketBase lifecycle.
	terminateSurfaceTestApp(t, baseline)
	databases := make(map[string][]byte)
	for _, name := range []string{"data.db", "auxiliary.db"} {
		raw, err := os.ReadFile(filepath.Join(baseline.DataDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		databases[name] = raw
	}
	return func(t *testing.T) *pocketbase.PocketBase {
		t.Helper()
		directory := t.TempDir()
		for name, raw := range databases {
			if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return bootstrapSurfaceTestApp(t, directory)
	}
}
func seedSurface(t *testing.T, app core.App, id string, raw json.RawMessage) Item {
	t.Helper()
	collection, err := resolveCollection(app, NamespaceInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(collection)
	record.Set("logical_id", id)
	record.Set("payload_json", pbtypes.JSONRaw(raw))
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	if record.Id == id {
		t.Fatal("fixture must distinguish PB identity from business identity")
	}
	item, err := itemFromRecord(NamespaceInterfaces, record)
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func invokeSurfaceTest(ctx context.Context, s *SurfaceService, method string, raw json.RawMessage) (any, error) {
	if err := DecodeSurfaceParams(method, raw, nil); err != nil {
		return nil, err
	}
	switch method {
	case "interface.list":
		return s.List(ctx)
	case "interface.load":
		var p wb.InterfaceLoadRequest
		_ = DecodeSurfaceParams(method, raw, &p)
		return s.Load(ctx, p.InterfaceId)
	case "interface.commit":
		var p wb.InterfaceCommitRequest
		_ = DecodeSurfaceParams(method, raw, &p)
		return s.Commit(ctx, p)
	case "interface.delete":
		var p wb.InterfaceDeleteRequest
		_ = DecodeSurfaceParams(method, raw, &p)
		return s.Delete(ctx, p)
	}
	return nil, errors.New("unexpected oracle method")
}
func TestSurfaceFrozenPythonOracle(t *testing.T) {
	newOracleApp := surfaceOracleAppFactory(t)
	for _, sample := range surfaceOracle(t) {
		t.Run(sample.Name, func(t *testing.T) {
			// The old adapter's injected failures are not database fixtures. Check their
			// public projection separately; real CAS/rollback/durable tests follow below.
			if sample.Failure != nil {
				var source error = errors.New("private storage failure")
				if *sample.Failure == "conflict" {
					source = &Error{Code: "metadata.revision_conflict"}
				}
				assertSurfaceOracleError(t, surfacePersistence(source), sample.Responses[0].Error.Data)
				return
			}
			if len(sample.Seed) == 0 && len(sample.Calls) == 1 && len(sample.Responses) == 1 && sample.Responses[0].Error != nil && sample.Responses[0].Error.Code == -32602 {
				if err := DecodeSurfaceParams(sample.Calls[0].Method, sample.Calls[0].Params, nil); !errors.Is(err, errSurfaceDTO) {
					t.Fatalf("want invalid DTO before persistence; got %v", err)
				}
				return
			}
			app := newOracleApp(t)
			service := NewSurface(app)
			revisions := map[string]string{}
			if sample.Name == "load-storage-duplicate-identities" {
				seed := sample.Seed[0]
				seedSurface(t, app, seed.LogicalID, seed.Values)
				collection, _ := resolveCollection(app, NamespaceInterfaces)
				duplicate := core.NewRecord(collection)
				duplicate.Set("logical_id", seed.LogicalID)
				duplicate.Set("payload_json", pbtypes.JSONRaw(seed.Values))
				if app.Save(duplicate) == nil {
					t.Fatal("PB must reject duplicate business identities before load")
				}
				return
			}
			for _, seed := range sample.Seed {
				item := seedSurface(t, app, seed.LogicalID, seed.Values)
				revisions[seed.Revision] = item.Revision
				revisions["id:"+seed.LogicalID] = item.Revision
			}
			var first any
			for i, call := range sample.Calls {
				raw := string(call.Params)
				for frozen, actual := range revisions {
					raw = strings.ReplaceAll(raw, `"`+frozen+`"`, `"`+actual+`"`)
				}
				got, err := invokeSurfaceTest(context.Background(), service, call.Method, json.RawMessage(raw))
				expected := sample.Responses[i]
				if i > 0 && strings.Contains(sample.Name, "replay-old-current-rejects") {
					if err != nil || !reflect.DeepEqual(got, first) {
						t.Fatalf("durable replay must return original result after old current CAS would reject: %v %#v", err, got)
					}
					continue
				}
				if expected.Error != nil {
					if expected.Error.Code == -32602 {
						if !errors.Is(err, errSurfaceDTO) {
							t.Fatalf("want invalid DTO; got %v", err)
						}
						continue
					}
					assertSurfaceOracleError(t, err, expected.Error.Data)
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = got
				}
				actualRaw, _ := json.Marshal(got)
				var actual, want any
				_ = json.Unmarshal(actualRaw, &actual)
				_ = json.Unmarshal(expected.Result, &want)
				// Receipt revisions are produced by metadata, not by the scripted oracle.
				pairSurfaceRevisions(want, actual, revisions)
				rewritten, _ := json.Marshal(want)
				for frozen, real := range revisions {
					rewritten = []byte(strings.ReplaceAll(string(rewritten), `"`+frozen+`"`, `"`+real+`"`))
				}
				_ = json.Unmarshal(rewritten, &want)
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("public result differs:\ngot %s\nwant %s", actualRaw, rewritten)
				}
			}
		})
	}
}
func pairSurfaceRevisions(want, actual any, revisions map[string]string) {
	switch w := want.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return
		}
		if id, ok := w["interfaceId"].(string); ok && w["revision"] != nil && revisions["id:"+id] != "" {
			w["revision"] = revisions["id:"+id]
		}
		if token, ok := w["revision"].(string); ok {
			if real, ok := a["revision"].(string); ok && real != "" {
				if _, seeded := revisions[token]; !seeded {
					revisions[token] = real
				}
			}
		}
		for k, v := range w {
			pairSurfaceRevisions(v, a[k], revisions)
		}
	case []any:
		a, ok := actual.([]any)
		if ok && len(a) == len(w) {
			for i, v := range w {
				pairSurfaceRevisions(v, a[i], revisions)
			}
		}
	}
}
func assertSurfaceOracleError(t *testing.T, err error, want map[string]any) {
	t.Helper()
	var domain *SurfaceError
	if !errors.As(err, &domain) {
		t.Fatalf("want domain error %#v; got %v", want, err)
	}
	got := map[string]any{"kind": "surface_error", "message": domain.Message, "code": domain.Code}
	if domain.Path != nil {
		got["path"] = *domain.Path
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domain error differs: got %#v want %#v", got, want)
	}
}
func TestSurfaceDurableReplayAfterRestartAndDelete(t *testing.T) {
	app := newSurfaceTestApp(t)
	s := NewSurface(app)
	var request wb.InterfaceCommitRequest
	for _, sample := range surfaceOracle(t) {
		if sample.Name == "create" {
			if err := DecodeSurfaceParams("interface.commit", sample.Calls[0].Params, &request); err != nil {
				t.Fatal(err)
			}
		}
	}
	created, err := s.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	updatedRequest := request
	updatedRequest.IdempotencyKey = "updated"
	updatedRequest.ExpectedRevision = &created.Revision
	updatedRequest.Definition.Name = "New name"
	updated, err := s.Commit(context.Background(), updatedRequest)
	if err != nil {
		t.Fatal(err)
	}
	deletion := wb.InterfaceDeleteRequest{InterfaceId: created.Definition.InterfaceId, ExpectedRevision: updated.Revision, IdempotencyKey: "deleted"}
	deleted, err := s.Delete(context.Background(), deletion)
	if err != nil {
		t.Fatal(err)
	}
	terminateSurfaceTestApp(t, app)
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	s = NewSurface(app)
	for _, r := range []wb.InterfaceCommitRequest{request, updatedRequest} {
		result, err := s.Commit(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		want := created
		if r.IdempotencyKey == "updated" {
			want = updated
		}
		if !reflect.DeepEqual(result, want) {
			t.Fatal("replay result changed")
		}
	}
	replay, err := s.Delete(context.Background(), deletion)
	if err != nil || replay != deleted {
		t.Fatalf("delete replay: %#v %v", replay, err)
	}
	changed := request
	changed.Definition.Name = "different request"
	_, err = s.Commit(context.Background(), changed)
	var domain *SurfaceError
	if !errors.As(err, &domain) || domain.Code != "surface.idempotency_conflict" {
		t.Fatalf("key conflict = %v", err)
	}
	list, err := s.List(context.Background())
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("replay resurrected data: %#v %v", list, err)
	}
	traces, err := app.FindRecordsByFilter("vibetable_audit_events", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 3 {
		t.Fatalf("replay emitted duplicate audit: %d", len(traces))
	}
}

func TestSurfaceDTOResourceBoundAndPublicDepthBoundary(t *testing.T) {
	for _, depth := range []int{8, 9, 1000} {
		var children []wb.InterfaceElement = []wb.InterfaceElement{}
		for i := 0; i < depth; i++ {
			children = []wb.InterfaceElement{{ElementId: fmt.Sprint(i), Kind: "section", Width: "full", Children: children}}
		}
		definition := wb.InterfaceDefinition{ContractVersion: "1.0", InterfaceId: "depth", Name: "Depth", Bindings: []wb.DataBinding{}, Actions: []wb.InterfaceAction{}, Pages: []wb.InterfacePage{{PageId: "main", Title: "Main", Elements: children}}}
		raw, _ := json.Marshal(wb.InterfaceCommitRequest{Definition: definition, IdempotencyKey: "depth"})
		var request wb.InterfaceCommitRequest
		err := DecodeSurfaceParams("interface.commit", raw, &request)
		if depth == 1000 {
			if err == nil {
				t.Fatal("pathological tree must fail bounded DTO decoding")
			}
			continue
		}
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		err = validateSurfaceDefinition(request.Definition)
		if depth == 8 {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var domain *SurfaceError
			if !errors.As(err, &domain) || domain.Code != "surface.element_depth" {
				t.Fatalf("depth %d domain = %v", depth, err)
			}
		}
	}
	for _, sample := range []struct {
		value any
		valid bool
	}{{"1.0", true}, {"1_0", true}, {"1e0", false}, {"0x1p0", false}, {"1.00000000000000001", false}, {"1.", false}, {true, true}, {json.Number("1.0"), true}} {
		_, valid := surfaceInteger(sample.value)
		if valid != sample.valid {
			t.Fatalf("integer %v accepted=%v", sample.value, valid)
		}
	}
}
func TestSurfaceMetadataKeyValidationFollowsCurrentCAS(t *testing.T) {
	app := newSurfaceTestApp(t)
	service := NewSurface(app)
	var request wb.InterfaceCommitRequest
	for _, sample := range surfaceOracle(t) {
		if sample.Name == "create" {
			_ = DecodeSurfaceParams("interface.commit", sample.Calls[0].Params, &request)
		}
	}
	request.IdempotencyKey = "invalid key"
	_, err := service.Commit(context.Background(), request)
	var domain *SurfaceError
	if !errors.As(err, &domain) || domain.Code != "surface.persistence_failed" {
		t.Fatalf("generic key failure = %v", err)
	}
	stale := "stale"
	request.ExpectedRevision = &stale
	_, err = service.Commit(context.Background(), request)
	if !errors.As(err, &domain) || domain.Code != "surface.edit_conflict" {
		t.Fatalf("current CAS must precede generic key rejection = %v", err)
	}
}

func TestSurfaceStoreTerminatesBeforeReset(t *testing.T) {
	var app *pocketbase.PocketBase
	terminated, bootstrappedAtTermination := false, false
	t.Run("fixture", func(t *testing.T) {
		app = newSurfaceTestApp(t)
		app.OnTerminate().BindFunc(func(event *core.TerminateEvent) error {
			terminated = true
			bootstrappedAtTermination = event.App.IsBootstrapped()
			return event.Next()
		})
	})
	if !terminated || !bootstrappedAtTermination {
		t.Errorf("termination before reset: called=%v bootstrapped=%v", terminated, bootstrappedAtTermination)
	}
	if app == nil || app.IsBootstrapped() {
		t.Error("fixture did not reset bootstrap state")
	}
}

func TestSurfaceOracleFixturesKeepIndependentPersistence(t *testing.T) {
	newApp := surfaceOracleAppFactory(t)
	first, second := newApp(t), newApp(t)
	seedSurface(t, first, "independent", json.RawMessage(`{"id":"independent"}`))
	collection, err := resolveCollection(second, NamespaceInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	records, err := second.FindAllRecords(collection)
	if err != nil || len(records) != 0 || first.DataDir() == second.DataDir() {
		t.Fatalf("oracle fixtures share persistence: records=%d error=%v", len(records), err)
	}
}
