package productrow_test

import (
	"reflect"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
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
