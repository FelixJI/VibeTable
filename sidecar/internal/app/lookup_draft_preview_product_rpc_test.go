package app

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestPathLookupDraftPreviewAggregatesFullSet pins the path draft preview:
// an aggregation draft resolves the relation path, previews the complete
// matched set (nulls ignored by numeric modes) and stays revision bound.
func TestPathLookupDraftPreviewAggregatesFullSet(t *testing.T) {
	pb := schemaProductStore(t)
	ctx := context.Background()
	lifecycle, err := schemacore.NewTableLifecycle(pb)
	if err != nil {
		t.Fatal(err)
	}
	table, err := lifecycle.Create(ctx, v2.TableCreateIntent{DisplayName: "路径预览", OperationID: "lookup-path-preview-table", Actor: v2.Actor{ID: "local", Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	label := createSchemaProductField(t, pb, table.TableID, v2.LogicalText, "名称", "path-preview-label")
	amount := createSchemaProductField(t, pb, table.TableID, v2.LogicalNumber, "数量", "path-preview-amount")
	relationDefaults, err := v2.RecommendedDefaults(v2.LogicalRelation)
	if err != nil {
		t.Fatal(err)
	}
	related := applySchemaProductField(t, pb, v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: table.TableID,
		Draft: &v2.FieldDraft{DisplayName: "明细", LogicalType: v2.LogicalRelation,
			Value: relationDefaults.Value, Constraints: relationDefaults.Constraints, Storage: relationDefaults.Storage, Display: relationDefaults.Display,
			Relation: &v2.RelationSpec{TargetTableID: table.TableID, Cardinality: "many", DeletePolicy: "setNull", DisplayField: label.FieldID}},
		RelationPair: &v2.RelationPairDraft{ReciprocalDisplayName: "订单", ReciprocalCardinality: "many", SourceDisplayFieldID: label.FieldID},
	}, "path-preview-relation")
	described, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := pb.FindCollectionByNameOrId(described.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]*core.Record, 0, 3)
	for index, value := range []any{float64(10), float64(20), nil} {
		record := core.NewRecord(collection)
		record.Id = fmt.Sprintf("pathpreviewrw%02d", index)
		record.Set(label.Definition.Identity.PhysicalName, fmt.Sprintf("行%d", index))
		record.Set(label.Definition.Value.Presence.PhysicalName, true)
		if value != nil {
			record.Set(amount.Definition.Identity.PhysicalName, value)
			record.Set(amount.Definition.Value.Presence.PhysicalName, true)
		}
		if err := pb.Save(record); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, record)
	}
	order := core.NewRecord(collection)
	order.Id = "pathprevieword1"
	order.SetRaw(related.Definition.Identity.PhysicalName, []string{rows[0].Id, rows[1].Id, rows[2].Id})
	if err := pb.Save(order); err != nil {
		t.Fatal(err)
	}
	revision, err := schemaexecution.Describe(ctx, pb, table.TableID)
	if err != nil {
		t.Fatal(err)
	}
	registration := lookupDraftPreviewRegistration(pb)
	preview := func(aggregation string) productrpc.ResponseEnvelope {
		params := map[string]any{
			"tableId": table.TableID, "schemaRevision": revision.Snapshot.SchemaRevision,
			"sourceSchemaRevision": revision.Snapshot.SchemaRevision,
			"lookup": map[string]any{
				"path":          []any{map[string]any{"relationFieldId": related.FieldID}},
				"targetFieldId": amount.FieldID, "aggregation": aggregation,
			},
		}
		return schemaProductRequestForMethod(t, relationWriteMux(t, relationWriteFixture{registrations: map[string]productrpc.Registration{"lookup.draft.preview": registration}}), ctx, "lookup.draft.preview", string(valuePageJSON(t, params)), schemaListWire)
	}
	sum := preview("sum")
	if sum.Error != nil {
		t.Fatalf("sum preview = %+v", sum.Error)
	}
	var cell struct {
		Cell struct {
			Value           any
			ProvenanceTotal int
		} `json:"cell"`
	}
	if err := json.Unmarshal(sum.Result, &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Cell.Value != 30.0 || cell.Cell.ProvenanceTotal != 3 {
		t.Fatalf("sum preview = %s", sum.Result)
	}
	if response := preview("countNonEmpty"); response.Error != nil {
		t.Fatalf("count preview = %+v", response.Error)
	} else if err := json.Unmarshal(response.Result, &cell); err != nil {
		t.Fatal(err)
	} else if cell.Cell.Value != 2.0 {
		t.Fatalf("countNonEmpty preview = %s", response.Result)
	}
	stale := map[string]any{
		"tableId": table.TableID, "schemaRevision": "schema_9999",
		"sourceSchemaRevision": revision.Snapshot.SchemaRevision,
		"lookup": map[string]any{
			"path":          []any{map[string]any{"relationFieldId": related.FieldID}},
			"targetFieldId": amount.FieldID, "aggregation": "sum",
		},
	}
	if response := schemaProductRequestForMethod(t, relationWriteMux(t, relationWriteFixture{registrations: map[string]productrpc.Registration{"lookup.draft.preview": registration}}), ctx, "lookup.draft.preview", string(valuePageJSON(t, stale)), schemaListWire); response.Error == nil {
		t.Fatal("stale path preview accepted")
	}
}
