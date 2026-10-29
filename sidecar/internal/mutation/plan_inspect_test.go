package mutation

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestPlanOverlayDeletionCannotReloadTheOriginalRecord(t *testing.T) {
	collection := core.NewBaseCollection("inspection_records")
	row := core.NewRecord(collection)
	row.Id = "inspectrecord01"
	// No underlying app is needed: a known tombstone must never fall through.
	overlay := &planOverlay{records: map[RowKey]*core.Record{}}
	if err := overlay.Delete(row); err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.FindRecordById(collection, row.Id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted row reloaded: %v", err)
	}
	if err := overlay.Save(row); err != nil {
		t.Fatal(err)
	}
	if restored, err := overlay.FindRecordById(collection, row.Id); err != nil || restored != row {
		t.Fatalf("new overlay image not visible: %v", err)
	}
}

func TestPlanOverlayRejectsSchemaWritesAndUnknownCollections(t *testing.T) {
	overlay := &planOverlay{records: map[RowKey]*core.Record{}}
	collection := core.NewBaseCollection("never-created")
	if err := overlay.Save(collection); err == nil {
		t.Fatal("inspection accepted a schema write")
	}
	if err := overlay.Delete(collection); err == nil {
		t.Fatal("inspection accepted a schema delete")
	}
	if _, err := overlay.FindRecordById(42, "row"); err == nil {
		t.Fatal("inspection accepted an unknown collection identity")
	}
}
