package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
)

func init() {
	m.Register(func(app core.App) error {
		for _, definition := range internalCollections {
			if definition.name != "vibetable_computation_dependencies" {
				continue
			}
			existing, err := app.FindCollectionByNameOrId(definition.name)
			if err == nil {
				return validateInternalCollection(existing, definition)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := app.Save(buildInternalCollection(definition)); err != nil {
				return fmt.Errorf("create legacy computation dependency graph: %w", err)
			}
			return schemaapi.New(app).RebuildComputationDependencies(context.Background())
		}
		return errors.New("computation dependency collection definition is missing")
	}, func(core.App) error {
		// The derived graph may predate this migration. Preserve it on rollback;
		// old readers ignore the additive collection and business data is unchanged.
		return nil
	})
}
