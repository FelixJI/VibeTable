package mutation

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
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

func loadV2FieldDefinitions(
	ctx context.Context,
	app core.App,
	tableID string,
) ([]v2.FieldDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Provider-neutral Preview unit tests intentionally omit a storage app.
	// They exercise the legacy schema adapter and therefore have no v2 metadata.
	if app == nil {
		return []v2.FieldDefinition{}, nil
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_fields",
		"table_id={:table} && schema_model_version=2 && lifecycle_state='active'",
		"id",
		0,
		0,
		dbx.Params{"table": tableID},
	)
	if err != nil {
		return nil, storageFailure()
	}
	result := make([]v2.FieldDefinition, 0, len(records))
	for _, record := range records {
		raw, marshalErr := json.Marshal(record.GetRaw("definition_v2_json"))
		if marshalErr != nil {
			return nil, storageFailure()
		}
		var definition v2.FieldDefinition
		if decodeErr := v2.StrictDecode(raw, &definition); decodeErr != nil {
			return nil, mutationError(
				"mutation.schema.v2_invalid", nil,
				"stored field v2 definition is invalid",
				map[string]any{"fieldId": record.GetString("field_id")},
				false,
			)
		}
		result = append(result, definition)
	}
	return result, nil
}
