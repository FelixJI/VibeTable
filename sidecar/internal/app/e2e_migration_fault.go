package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

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
		if strings.TrimSpace(string(content)) != string(phase) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("consume product E2E migration fault: %w", err)
		}
		return fmt.Errorf("injected product E2E migration fault at %s", phase)
	})
}
