package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const e2eMigrationFaultFileEnvironment = "VIBETABLE_E2E_MIGRATION_FAULT_FILE"

func newE2EMigrationFaultFromEnvironment() fieldchange.MigrationOption {
	path := strings.TrimSpace(os.Getenv(e2eMigrationFaultFileEnvironment))
	if path == "" {
		return nil
	}
	var mutex sync.Mutex
	return fieldchange.WithMigrationFaultInjector(func(phase v2.MigrationPhase) error {
		mutex.Lock()
		defer mutex.Unlock()
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read product E2E migration fault: %w", err)
		}
		// The explicit E2E control pauses before authority switching so the real
		// Host can cancel a running job without racing a one-row migration.
		if strings.TrimSpace(string(content)) == "hold:"+string(phase) {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					return nil
				} else if err != nil {
					return fmt.Errorf("observe product E2E migration hold: %w", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			return fmt.Errorf("product E2E migration hold timed out at %s", phase)
		}
		if strings.TrimSpace(string(content)) != string(phase) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("consume product E2E migration fault: %w", err)
		}
		return fmt.Errorf("injected product E2E migration fault at %s", phase)
	})
}
