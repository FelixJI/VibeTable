package workspacev2

import (
	"context"
	"strings"
	"testing"
)

func TestWorkspaceSettingsSnapshotUsesOnlyAuthoritativeRetention(t *testing.T) {
	store, err := openStateStore(t.TempDir() + "/workspace-v2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()

	raw, err := snapshotWorkspaceSettings(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := decodeWorkspaceSettingsSnapshot(raw)
	if err != nil {
		t.Fatalf("decode settings = %#v, err=%v", settings, err)
	}
	if settings.FormatVersion != 1 ||
		settings.Retention.SnapshotDays != 30 ||
		settings.Retention.FileRevisionCount != 100 {
		t.Fatalf("settings projection = %#v", settings)
	}
	text := string(raw)
	for _, excluded := range []string{
		"theme", "locale", "recent", "device", "lease", "claim",
		"key", "health", "sync",
	} {
		if strings.Contains(text, excluded) {
			t.Fatalf("settings projection leaked %q: %s", excluded, text)
		}
	}
}

func TestWorkspaceSettingsSnapshotRejectsUnversionedSettings(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`{"theme":"dark"}`)} {
		if _, err := decodeWorkspaceSettingsSnapshot(raw); err == nil ||
			err.Error() != "workspace.settings_invalid" {
			t.Fatalf("unversioned settings error = %v", err)
		}
	}
	if _, err := decodeWorkspaceSettingsSnapshot([]byte(
		`{"formatVersion":1,"retention":{"snapshotDays":30,` +
			`"snapshotCount":50,"snapshotBuckets":["daily"],` +
			`"fileRevisionDays":30,"fileRevisionCount":100,` +
			`"fileRevisionBuckets":["daily"],` +
			`"repositoryLimitBytes":null,"theme":"dark"}}`,
	)); err == nil {
		t.Fatal("unknown workspace setting was accepted")
	}

	store, err := openStateStore(t.TempDir() + "/workspace-v2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	before, _, err := store.retention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = replaceWorkspaceSettings(
		context.Background(),
		store,
		[]byte(`{}`),
		99,
	)
	if err == nil || err.Error() != "workspace.settings_invalid" {
		t.Fatalf("direct settings load error = %v", err)
	}
	after, _, err := store.retention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.PolicyRevision != after.PolicyRevision ||
		before.SnapshotDays != after.SnapshotDays {
		t.Fatalf("legacy snapshot changed retention: before=%#v after=%#v", before, after)
	}
}
