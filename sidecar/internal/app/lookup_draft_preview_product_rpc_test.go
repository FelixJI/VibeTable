package app

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func TestConditionalLookupDraftPreviewIsClosedReadOnlyAndRevisionBound(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "预览测试", OperationID: "lookup-preview-table", Actor: v2.Actor{ID: "local", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "名称", "preview-label")
	described, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	lookup := map[string]any{"path": []any{}, "targetFieldId": label.FieldID, "condition": map[string]any{
		"sourceTableId": table.TableID, "match": "all", "distinct": false, "rules": []any{map[string]any{"sourceFieldId": label.FieldID, "operator": "eq", "operand": map[string]any{"kind": "constant", "value": "中文"}}},
	}}
	params := map[string]any{"tableId": table.TableID, "schemaRevision": described.Snapshot.SchemaRevision, "sourceSchemaRevision": described.Snapshot.SchemaRevision, "lookup": lookup}
	registration := lookupDraftPreviewRegistration(pb)
	mux := relationWriteMux(t, relationWriteFixture{registrations: map[string]productrpc.Registration{"lookup.draft.preview": registration}})
	invoke := func(values map[string]any) productrpc.ResponseEnvelope {
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return schemaProductRequestForMethod(t, mux, ctx, "lookup.draft.preview", string(raw), schemaListWire)
	}
	empty := invoke(params)
	if empty.Error != nil || string(empty.Result) != "{\"cell\":null}" {
		t.Fatalf("empty = %+v", empty)
	}
	collection, err := pb.FindCollectionByNameOrId(described.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(label.Definition.Identity.PhysicalName, "中文")
	row.Set(label.Definition.Value.Presence.PhysicalName, true)
	if err := pb.Save(row); err != nil {
		t.Fatal(err)
	}
	before, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	response := invoke(params)
	if response.Error != nil {
		t.Fatalf("preview = %+v", response.Error)
	}
	var result struct {
		Cell struct {
			Value           []any
			ProvenanceTotal int
		}
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Cell.Value, []any{"中文"}) || result.Cell.ProvenanceTotal != 1 {
		t.Fatalf("preview = %s", response.Result)
	}
	after, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Snapshot, after.Snapshot) {
		t.Fatal("preview mutated schema/data revision")
	}
	for _, key := range []string{"schemaRevision", "sourceSchemaRevision"} {
		saved := params[key]
		params[key] = "stale"
		if invoke(params).Error == nil {
			t.Fatal("stale accepted", key)
		}
		params[key] = saved
	}
	for key, saved := range params {
		delete(params, key)
		if registration.ValidateParams(valuePageJSON(t, params)) == nil {
			t.Fatal("missing accepted", key)
		}
		params[key] = saved
	}
	params["extra"] = true
	if registration.ValidateParams(valuePageJSON(t, params)) == nil {
		t.Fatal("unknown key accepted")
	}
	delete(params, "extra")
	condition := lookup["condition"].(map[string]any)
	delete(condition, "distinct")
	if registration.ValidateParams(valuePageJSON(t, params)) == nil {
		t.Fatal("missing distinct accepted")
	}
	condition["distinct"] = false
	lookup["path"] = []any{map[string]any{"relationFieldId": "unexpected"}}
	if registration.ValidateParams(valuePageJSON(t, params)) == nil {
		t.Fatal("mixed modes accepted")
	}
	lookup["path"] = []any{}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := registration.Handler(cancelled, valuePageJSON(t, params)); err == nil {
		t.Fatal("cancelled preview accepted")
	}
}
