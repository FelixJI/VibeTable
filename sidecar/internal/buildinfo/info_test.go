package buildinfo

import (
	"strconv"
	"testing"

	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestCurrentContainsPinnedDependencies(t *testing.T) {
	info := Current("abc123")
	if info.PocketBaseVersion != "0.39.9" {
		t.Fatalf("PocketBase version = %q", info.PocketBaseVersion)
	}
	if info.CELVersion != "0.26.1" {
		t.Fatalf("CEL version = %q", info.CELVersion)
	}
	if info.SchemaVersion != "5" {
		t.Fatalf("schema version = %q", info.SchemaVersion)
	}
	if info.MigrationHash != "abc123" {
		t.Fatalf("migration hash = %q", info.MigrationHash)
	}
}

func TestSchemaVersionMatchesEmbeddedMigrationManifest(t *testing.T) {
	manifest, err := migrations.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(manifest.SchemaVersion); SchemaVersion != want {
		t.Fatalf("SchemaVersion = %q, manifest = %q", SchemaVersion, want)
	}
}
