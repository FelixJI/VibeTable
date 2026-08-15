package mutation

import (
	"context"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
)

func checkFieldMigrationFence(
	ctx context.Context,
	app core.App,
	tableID string,
) error {
	err := fieldchange.CheckTableWriteFence(ctx, app, tableID)
	if err == nil {
		return nil
	}
	var productErr *fieldchange.ProductError
	if errors.As(err, &productErr) {
		return mutationError(
			productErr.Code, stringPointer(productErr.Path),
			productErr.Message, productErr.Details, true,
		)
	}
	return err
}
