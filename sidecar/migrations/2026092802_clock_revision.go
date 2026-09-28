package migrations

import (
	"errors"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// clockRevisionMigrationField counts only clock-derived cache transactions on
// vibetable_tables. data_revision keeps advancing on every write for
// query/realtime; the freshness contract derives the business-input revision
// as data_revision - clock_revision (relatedcomputation.ExpectationFor), so a
// clock-only refresh no longer invalidates ordinary cross-table consumers.
const clockRevisionMigrationField = "clock_revision"

func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("vibetable_tables")
			if err != nil {
				return fmt.Errorf("find table metadata for clock revision: %w", err)
			}
			if existing := collection.Fields.GetByName(clockRevisionMigrationField); existing != nil {
				number, ok := existing.(*core.NumberField)
				// Optional counter: absent means zero for pre-existing rows and
				// old readers, so only an incompatible physical shape fails.
				if !ok || !number.OnlyInt {
					return errors.New("table metadata clock_revision field is incompatible")
				}
				return nil
			}
			minimum := float64(0)
			collection.Fields.Add(&core.NumberField{
				Name: clockRevisionMigrationField, OnlyInt: true, Min: &minimum,
			})
			if err := app.Save(collection); err != nil {
				return fmt.Errorf("add table metadata clock revision: %w", err)
			}
			return nil
		},
		func(core.App) error {
			// Additive rollback keeps the optional counter and its accumulated
			// values: old readers ignore the field and no data is deleted.
			return nil
		},
	)
}
