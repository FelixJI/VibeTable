package migrations

import (
	"errors"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
)

func init() {
	m.Register(func(app core.App) error {
		return app.RunInTransaction(func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("vibetable_computation_dependencies")
			if err != nil {
				return err
			}
			if existing := collection.Fields.GetByName(relatedcomputation.InputRevisionField); existing != nil {
				field, ok := existing.(*core.NumberField)
				if !ok || !field.OnlyInt || field.Required || field.Min == nil || *field.Min != 0 {
					return errors.New("dependency input revision field is incompatible")
				}
				return nil
			}
			minimum := float64(0)
			collection.Fields.Add(&core.NumberField{Name: relatedcomputation.InputRevisionField, OnlyInt: true, Min: &minimum})
			return app.Save(collection)
		})
	}, func(core.App) error {
		// Keep this additive field when reverting the migration registration.
		return nil
	})
}
