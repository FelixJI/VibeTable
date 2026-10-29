package query

import (
	"context"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// ReadBounded materializes a read-only projection in the caller's transaction.
// SQLite measures projected cells before they cross the driver allocation boundary.
// The caller also bounds its complete encoded result, including JSON overhead.
func ReadBounded(ctx context.Context, app core.App, descriptor TableDescriptor, input TableQuery, maxRows int, maxBytes *int) ([]map[string]any, int64, int64, error) {
	input.Offset, input.Limit = 0, 500
	plan, err := Compile(descriptor, input)
	if err != nil {
		return nil, 0, 0, err
	}
	var count, total int64
	if err = app.DB().NewQuery("SELECT ("+plan.CountSQL+"), ("+plan.TotalSQL+")").WithContext(ctx).Bind(dbx.Params(plan.Params)).Row(&count, &total); err != nil {
		return nil, 0, 0, operationError(err)
	}
	if count > int64(maxRows) {
		return nil, 0, 0, productError("plugin_dataset_limit", "", "dataset row budget exceeded", nil)
	}
	parts := []string{}
	for _, name := range plan.Fields {
		parts = append(parts, "COALESCE(octet_length("+quote(name)+"),0)")
	}
	result := []map[string]any{}
	for offset := 0; offset < int(count); offset += 500 {
		plan.Params["offset"] = offset
		var bytes int64
		if err = app.DB().NewQuery("SELECT COALESCE(SUM(" + strings.Join(parts, "+") + "),0) FROM (" + plan.SQL + ")").WithContext(ctx).Bind(dbx.Params(plan.Params)).Row(&bytes); err != nil {
			return nil, 0, 0, operationError(err)
		}
		if bytes > int64(*maxBytes) {
			return nil, 0, 0, productError("plugin_dataset_limit", "", "dataset byte budget exceeded", nil)
		}
		*maxBytes -= int(bytes)
		rows, err := readQueryRows(ctx, app, plan.SQL, plan.Params, descriptor)
		if err != nil {
			return nil, 0, 0, operationError(err)
		}
		result = append(result, rows...)
	}
	return result, count, total, nil
}
