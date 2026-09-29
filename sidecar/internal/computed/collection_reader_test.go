package computed

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryfilter"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

type countedCollectionField struct {
	core.Field
	prepares *atomic.Int64
}

func (field *countedCollectionField) PrepareValue(record *core.Record, raw any) (any, error) {
	field.prepares.Add(1)
	return field.Field.PrepareValue(record, raw)
}

func TestCollectionPageHydratesOnlyAuthoritativeProjection(t *testing.T) {
	app := computedTestApp(t)
	collection := core.NewBaseCollection("projection_rows")
	collection.Fields.Add(&core.NumberField{Name: relatedcomputation.RowRevisionField},
		&core.DateField{Name: "occurred"}, &core.NumberField{Name: "amount"}, &core.BoolField{Name: "flag"},
		&core.BoolField{Name: "has_occurred"}, &core.BoolField{Name: "has_amount"}, &core.BoolField{Name: "has_flag"},
		&core.NumberField{Name: "unselected"})
	if err := app.Save(collection); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"projection00002", "projection00001"} {
		record := core.NewRecord(collection)
		record.Id = id
		record.Set(relatedcomputation.RowRevisionField, 7)
		record.Set("occurred", "2026-09-29 04:05:06.000Z")
		record.Set("amount", 0)
		record.Set("flag", false)
		for _, name := range []string{"has_occurred", "has_amount", "has_flag"} {
			record.Set(name, id == "projection00001")
		}
		if err := app.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	var prepares atomic.Int64
	for index, field := range collection.Fields {
		if field.GetName() == "unselected" {
			collection.Fields[index] = &countedCollectionField{Field: field, prepares: &prepares}
		}
	}
	original := append(core.FieldsList(nil), collection.Fields...)
	fields := []v2.FieldDefinition{}
	bindings := []collectionFieldBinding{}
	for _, item := range []struct {
		name string
		kind v2.LogicalType
	}{{"occurred", v2.LogicalDateTime}, {"amount", v2.LogicalNumber}, {"flag", v2.LogicalBool}} {
		field := v2.FieldDefinition{Identity: v2.FieldIdentity{FieldID: "fld_" + item.name, PhysicalName: item.name}, LogicalType: item.kind,
			Value: v2.ValueSpec{Presence: v2.PresenceSpec{Mode: v2.PresenceCompanion, PhysicalName: "has_" + item.name}}}
		fields = append(fields, field)
		bindings = append(bindings, collectionFieldBinding{requested: field, authoritative: field})
	}
	descriptor := query.TableDescriptor{TableID: "tbl_projection", PhysicalName: collection.Name, PrimaryKey: "id",
		Fields: map[string]query.FieldDescriptor{"id": {PhysicalName: "id", Type: query.FieldTypeText}}}
	compiled, err := query.CompileMatchBatch(descriptor, [][]queryfilter.FilterExpression{{{Field: "id", Operator: queryfilter.OperatorNotEqual, Value: ""}}}, "", collectionPageSize)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := collectionPageRecords(context.Background(), app, "tbl_projection", collection, compiled, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if got := prepares.Load(); got != 0 {
		t.Errorf("unselected PrepareValue calls = %d, want 0", got)
	}
	if len(collection.Fields) != len(original) {
		t.Fatal("authoritative field count changed")
	}
	for index, field := range original {
		if collection.Fields[index] != field {
			t.Fatalf("authoritative field %d changed", index)
		}
	}
	missing := bindings[0]
	missing.authoritative.Identity.PhysicalName = "missing_storage"
	if _, err := collectionPageRecords(context.Background(), app, "tbl_projection", collection, compiled, []collectionFieldBinding{missing}); err == nil {
		t.Fatal("missing authoritative physical field was accepted")
	}
	for index, row := range rows {
		if row.Id != []string{"projection00001", "projection00002"}[index] || row.GetInt(relatedcomputation.RowRevisionField) != 7 {
			t.Fatalf("row identity/revision = %s/%d", row.Id, row.GetInt(relatedcomputation.RowRevisionField))
		}
		projected, err := projectCollectionRow(context.Background(), app, "tbl_projection", v2.SchemaSnapshot{Fields: fields}, bindings, relatedcomputation.NewSourceReader(), row)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"id": row.Id, "occurred": nil, "amount": nil, "flag": nil}
		if index == 0 {
			want["occurred"] = "2026-09-29T04:05:06Z"
			want["amount"] = float64(0)
			want["flag"] = false
		}
		if !reflect.DeepEqual(projected, want) {
			t.Fatalf("projection = %#v, want %#v", projected, want)
		}
		for _, name := range []string{"id", relatedcomputation.RowRevisionField, "occurred", "amount", "flag", "has_occurred", "has_amount", "has_flag"} {
			if row.Collection().Fields.GetByName(name) != collection.Fields.GetByName(name) {
				t.Errorf("field %s lost authoritative preparation", name)
			}
		}
	}
}
