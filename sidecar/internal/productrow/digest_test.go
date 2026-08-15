package productrow_test

import (
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestPasswordDigestUsesStableHashGetter(t *testing.T) {
	collection := core.NewBaseCollection("credential_rows")
	collection.Fields.Add(&core.PasswordField{Name: "password_hash"})
	const hash = "$2a$10$.5Elh8fgxypNUWhpUUr/xOa2sZm0VIaE0qWuGGl9otUfobb46T1Pq"

	pending := core.NewRecord(collection)
	pending.Id = "credential00001"
	pending.SetRaw(
		"password_hash",
		&core.PasswordFieldValue{Hash: hash},
	)
	loaded := core.NewRecord(collection)
	loaded.Id = pending.Id
	loaded.SetRaw("password_hash", hash)

	pendingRow := productrow.FromRecord([]string{"password_hash"}, pending)
	loadedRow := productrow.FromRecord([]string{"password_hash"}, loaded)
	if !reflect.DeepEqual(pendingRow, loadedRow) {
		t.Fatalf("password rows differ: pending=%#v loaded=%#v", pendingRow, loadedRow)
	}
	pendingDigest, err := productrow.Digest(pendingRow)
	if err != nil {
		t.Fatal(err)
	}
	loadedDigest, err := productrow.Digest(loadedRow)
	if err != nil {
		t.Fatal(err)
	}
	if pendingDigest != loadedDigest {
		t.Fatalf("password digests differ: %q != %q", pendingDigest, loadedDigest)
	}
}

func TestProjectUsesOnlySchemaV2AndHidesPresenceCompanions(t *testing.T) {
	collection := core.NewBaseCollection("product_rows")
	collection.Fields.Add(&core.NumberField{Name: "f_score"})
	collection.Fields.Add(&core.BoolField{Name: "__vt_has_f_score", Hidden: true})
	record := core.NewRecord(collection)
	record.Id = "productrow00001"
	record.Set("f_score", float64(0))
	record.Set("__vt_has_f_score", false)

	field := v2.FieldDefinition{
		Contract: v2.Contract,
		Identity: v2.FieldIdentity{
			FieldID: "fld_score", PhysicalName: "f_score", ProviderFieldID: "pb_score",
		},
		LogicalType: v2.LogicalNumber,
		Lifecycle:   v2.Lifecycle{State: v2.LifecycleActive},
		Value: v2.ValueSpec{Presence: v2.PresenceSpec{
			Mode: v2.PresenceCompanion, PhysicalName: "__vt_has_f_score",
		}},
	}
	row := productrow.Project([]v2.FieldDefinition{field}, record)
	if row["id"] != record.Id || row["f_score"] != nil {
		t.Fatalf("missing score row = %#v", row)
	}
	if _, leaked := row["__vt_has_f_score"]; leaked {
		t.Fatalf("presence companion leaked: %#v", row)
	}

	record.Set("__vt_has_f_score", true)
	row = productrow.Project([]v2.FieldDefinition{field}, record)
	if row["f_score"] != float64(0) {
		t.Fatalf("explicit zero row = %#v", row)
	}
}
