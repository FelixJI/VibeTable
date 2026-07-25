package buildinfo

import "testing"

func TestCurrentContainsPinnedDependencies(t *testing.T) {
	info := Current("abc123")
	if info.PocketBaseVersion != "0.39.9" {
		t.Fatalf("PocketBase version = %q", info.PocketBaseVersion)
	}
	if info.CELVersion != "0.26.1" {
		t.Fatalf("CEL version = %q", info.CELVersion)
	}
	if info.SchemaVersion != "4" {
		t.Fatalf("schema version = %q", info.SchemaVersion)
	}
	if info.MigrationHash != "abc123" {
		t.Fatalf("migration hash = %q", info.MigrationHash)
	}
}
