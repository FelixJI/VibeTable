package health

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
)

func TestCheckReportsWritableDataDirectory(t *testing.T) {
	started := time.Unix(1, 0).UTC()
	checked := time.Unix(2, 0).UTC()
	response, status := Check(
		t.TempDir(),
		started,
		buildinfo.Current("hash"),
		checked,
		func() error { return nil },
	)
	if status != http.StatusOK || response.Status != "ok" ||
		!response.StorageWritable || !response.SchemaReady {
		t.Fatalf("unexpected health response: status=%d response=%#v", status, response)
	}
}

func TestCheckDoesNotLeakDataDirectoryOnFailure(t *testing.T) {
	root := t.TempDir()
	notDirectory := filepath.Join(root, "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	response, status := Check(
		notDirectory,
		time.Unix(1, 0).UTC(),
		buildinfo.Current("hash"),
		time.Unix(2, 0).UTC(),
		func() error { return nil },
	)
	if status != http.StatusServiceUnavailable || response.Error == nil {
		t.Fatalf("unexpected health response: status=%d response=%#v", status, response)
	}
	if response.Error.Message == "" || response.Error.Message == notDirectory {
		t.Fatalf("unsafe error message: %q", response.Error.Message)
	}
}

func TestCheckReportsDatabaseOrSchemaFailure(t *testing.T) {
	response, status := Check(
		t.TempDir(),
		time.Unix(1, 0).UTC(),
		buildinfo.Current("hash"),
		time.Unix(2, 0).UTC(),
		func() error { return os.ErrNotExist },
	)
	if status != http.StatusServiceUnavailable ||
		response.Error == nil ||
		response.Error.Code != "health.database_unavailable" ||
		response.SchemaReady ||
		response.PocketBase != "degraded" {
		t.Fatalf("unexpected health response: status=%d response=%#v", status, response)
	}
}
