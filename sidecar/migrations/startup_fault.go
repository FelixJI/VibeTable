package migrations

import (
	"encoding/json"
	"fmt"
	"os"
)

const startupMigrationFaultEnvironment = "VIBETABLE_E2E_STARTUP_MIGRATION_FAULT_FILE"

type startupMigrationFaultSpec struct {
	Migration  string `json:"migration"`
	Checkpoint string `json:"checkpoint"`
}

func triggerStartupMigrationFault(migration string, checkpoint string) error {
	path := os.Getenv(startupMigrationFaultEnvironment)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read E2E startup migration fault: %w", err)
	}
	var spec startupMigrationFaultSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("decode E2E startup migration fault: %w", err)
	}
	if spec.Migration != migration || spec.Checkpoint != checkpoint {
		return fmt.Errorf(
			"E2E startup migration fault targets %s/%s, reached %s/%s",
			spec.Migration,
			spec.Checkpoint,
			migration,
			checkpoint,
		)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("consume E2E startup migration fault: %w", err)
	}
	return fmt.Errorf("injected E2E startup migration interruption at %s/%s", migration, checkpoint)
}
