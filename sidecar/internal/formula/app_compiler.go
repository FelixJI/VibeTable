package formula

import (
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const appCompilerKey = "vibetable.formula.compiler"

// NewAppCompiler installs the shared compiler before the application starts
// serving requests or opening transactions. The PocketBase store is shared by
// transaction clones, while the authority reader stays bound to the root app.
func NewAppCompiler(app core.App) *Compiler {
	compiler := NewCompiler(DefaultLimits())
	compiler.cache.currentRevision = func(tableID string) (string, error) {
		record, err := app.FindFirstRecordByFilter("vibetable_tables", "table_id={:table}", dbx.Params{"table": tableID})
		if errors.Is(err, sql.ErrNoRows) {
			// A table may exist only in the caller's uncommitted transaction.
			// Compile its supplied definition without admitting a shared plan.
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read formula schema revision: %w", err)
		}
		revision := record.GetFloat("schema_revision")
		if math.IsNaN(revision) || math.IsInf(revision, 0) || revision < 0 || revision > 1<<53-1 || math.Trunc(revision) != revision {
			return "", fmt.Errorf("invalid formula schema revision")
		}
		return v2.FormatSchemaRevision(int64(revision)), nil
	}
	app.OnRecordAfterUpdateSuccess("vibetable_tables").BindFunc(func(event *core.RecordEvent) error {
		if err := compiler.cache.refreshTable(event.Record.GetString("table_id")); err != nil {
			// The update has committed and refreshTable has evicted its old plans.
			// A cache read failure must not turn that success into a failed write.
			app.Logger().Warn("Formula plan cache revision refresh failed",
				"tableId", event.Record.GetString("table_id"), "error", err)
		}
		return event.Next()
	})
	app.OnRecordAfterDeleteSuccess("vibetable_tables").BindFunc(func(event *core.RecordEvent) error {
		compiler.cache.invalidateTable(event.Record.GetString("table_id"))
		return event.Next()
	})
	app.Store().Set(appCompilerKey, compiler)
	return compiler
}

// CompilerFor reuses the application compiler in catalogs and transaction
// clones. Standalone catalog callers retain the pure compiler behavior.
func CompilerFor(app core.App) *Compiler {
	if compiler, ok := app.Store().Get(appCompilerKey).(*Compiler); ok {
		return compiler
	}
	return NewCompiler(DefaultLimits())
}
