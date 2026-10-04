package buildinfo

import (
	"strconv"
	"testing"

	"github.com/vibetable/vibetable/sidecar/migrations"
)

func TestCurrentContainsPinnedDependencies(t *testing.T) {
	info := Current("abc123")
	if info.PocketBaseVersion != "0.40.4" {
		t.Fatalf("PocketBase version = %q", info.PocketBaseVersion)
	}
	if info.CELVersion != "0.32.0" {
		t.Fatalf("CEL version = %q", info.CELVersion)
	}
	if info.SchemaVersion != "16" {
		t.Fatalf("schema version = %q", info.SchemaVersion)
	}
	if info.MigrationHash != "abc123" {
		t.Fatalf("migration hash = %q", info.MigrationHash)
	}
	if info.ProtocolV2Version != "2.0" ||
		info.WorkspaceFormat != "3" ||
		info.RepositoryFormat != "kopia-v4" ||
		info.SnapshotFormat != "2" ||
		info.PackageFormat != "2" ||
		info.KopiaVersion != "v0.23.1" ||
		info.AgeVersion != "v1.3.2" {
		t.Fatalf("workspace release metadata is incomplete: %#v", info)
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
