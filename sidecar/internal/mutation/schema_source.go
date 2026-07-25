package mutation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

type MetadataSchemaSource struct{}

func (MetadataSchemaSource) Describe(
	ctx context.Context,
	app core.App,
	tableID string,
) (schema.TableDefinition, error) {
	if err := ctx.Err(); err != nil {
		return schema.TableDefinition{}, err
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID},
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return schema.TableDefinition{}, mutationError(
				"mutation.table.not_found", stringPointer("tableId"),
				"table was not found", nil, false,
			)
		}
		return schema.TableDefinition{}, storageFailure()
	}
	raw, err := json.Marshal(record.GetRaw("definition_json"))
	if err != nil {
		return schema.TableDefinition{}, storageFailure()
	}
	var definition schema.TableDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		return schema.TableDefinition{}, storageFailure()
	}
	return definition, nil
}
