package workspacev2

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type diagnosticJobCounts struct {
	Queued    int64 `db:"queued" json:"queued"`
	Running   int64 `db:"running" json:"running"`
	Succeeded int64 `db:"succeeded" json:"succeeded"`
	Failed    int64 `db:"failed" json:"failed"`
	Cancelled int64 `db:"cancelled" json:"cancelled"`
}

func (runtime *Runtime) registerWorkspaceDiagnosticsHandler() {
	runtime.dispatcher.Register(
		"workspaceDiagnostics.get",
		protocolv2.WorkspaceScope,
		runtime.getWorkspaceDiagnostics,
	)
}

func (runtime *Runtime) getWorkspaceDiagnostics(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	if _, err := decodeStrict[struct{}](paramsRaw); err != nil {
		return nil, errors.New("diagnostics.request_invalid")
	}
	var jobs diagnosticJobCounts
	if err := runtime.app.DB().NewQuery(`
		SELECT
			COALESCE(SUM(CASE WHEN state='queued' THEN 1 ELSE 0 END),0) AS queued,
			COALESCE(SUM(CASE WHEN state='running' THEN 1 ELSE 0 END),0) AS running,
			COALESCE(SUM(CASE WHEN state='succeeded' THEN 1 ELSE 0 END),0) AS succeeded,
			COALESCE(SUM(CASE WHEN state='failed' THEN 1 ELSE 0 END),0) AS failed,
			COALESCE(SUM(CASE WHEN state='cancelled' THEN 1 ELSE 0 END),0) AS cancelled
		FROM vibetable_jobs
	`).WithContext(ctx).Bind(dbx.Params{}).Row(&jobs); err != nil {
		return nil, errors.New("diagnostics.storage_failed")
	}
	runtime.searchMu.Lock()
	index := runtime.searchStatus
	runtime.searchMu.Unlock()
	if index.State == "" {
		var err error
		index, err = runtime.search.Status(ctx)
		if err != nil {
			return nil, errors.New("diagnostics.index_status_failed")
		}
	}
	recovery := runtime.coordinator.RecoveryState()
	return map[string]any{
		"contractVersion": "1.0",
		"jobs":            jobs,
		"index":           index,
		"recovery": map[string]any{
			"pendingMutationRevision": recovery.PendingMutationRevision,
		},
	}, nil
}
