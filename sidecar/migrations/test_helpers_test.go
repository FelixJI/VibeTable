package migrations

import (
	"os"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
)

func newMigratedTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		for attempt := 0; attempt < 5; attempt++ {
			if err := os.RemoveAll(dataDir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	Register(app)
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap(): %v", err)
	}
	t.Cleanup(func() {
		if err := app.ResetBootstrapState(); err != nil {
			t.Errorf("ResetBootstrapState(): %v", err)
		}
	})
	if err := app.RunAllMigrations(); err != nil {
		t.Fatalf("RunAllMigrations(): %v", err)
	}
	return app
}
