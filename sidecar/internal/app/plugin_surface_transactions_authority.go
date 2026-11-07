package app

import (
	"context"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

type transactionAuthorityKey struct{}

// Private to one transaction operation; any write invalidates the read snapshot,
// including installation/schema changes made by synchronous record hooks.
type transactionAuthorityReads struct {
	workspace       string
	input           pluginSurfaceAPIRequest
	app             core.App
	stamp           transactionAuthorityStamp
	dirty           bool
	installed       *pluginSurfaceInstallation
	grantModel      *pluginmodel.State
	schemaRevisions map[string]string
	model           *pluginmodel.State
}

type transactionAuthorityStamp struct {
	Writes int64 `db:"writes"`
	Schema int64 `db:"schema_version"`
}

func withTransactionAuthority(ctx context.Context, workspace string, input pluginSurfaceAPIRequest) context.Context {
	return context.WithValue(ctx, transactionAuthorityKey{}, &transactionAuthorityReads{workspace: workspace, input: input})
}

func transactionAuthority(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest) (*transactionAuthorityReads, error) {
	reads, _ := ctx.Value(transactionAuthorityKey{}).(*transactionAuthorityReads)
	if reads == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	database, transactionBound := app.NonconcurrentDB().(*dbx.Tx)
	if !transactionBound || !app.IsTransactional() || reads.app != app || reads.dirty || reads.workspace != workspace ||
		reads.input.ProjectKey != input.ProjectKey || reads.input.PluginID != input.PluginID ||
		reads.input.InstallationID != input.InstallationID || reads.input.InstallationRevision != input.InstallationRevision ||
		reads.input.PackageHash != input.PackageHash || reads.input.ActionID != input.ActionID || reads.input.Entry != input.Entry {
		return nil, nil
	}
	stamp, err := readTransactionAuthorityStamp(ctx, database)
	if err != nil {
		return nil, err
	}
	if reads.stamp != stamp {
		// total_changes does not decrease on ROLLBACK TO. Never rebuild a
		// snapshot after a write, even if a later savepoint rolls that write back.
		reads.dirty = true
		reads.installed, reads.grantModel, reads.schemaRevisions, reads.model = nil, nil, nil, nil
		return nil, nil
	}
	return reads, nil
}

// Called before any writes in preflight, ApplyPlan, its InspectPlan overlay,
// or preview persistence. Lookups never establish their own baseline.
func beginTransactionAuthority(ctx context.Context, app core.App) error {
	reads, _ := ctx.Value(transactionAuthorityKey{}).(*transactionAuthorityReads)
	if reads == nil || reads.app == app {
		return nil
	}
	database, ok := app.NonconcurrentDB().(*dbx.Tx)
	if !ok || !app.IsTransactional() || reads.app != nil {
		return pluginDataError("transaction.authority_unavailable")
	}
	stamp, err := readTransactionAuthorityStamp(ctx, database)
	if err != nil {
		return err
	}
	reads.app, reads.stamp, reads.dirty = app, stamp, false
	app.TxInfo().OnComplete(func(error) error {
		reads.app, reads.installed, reads.grantModel, reads.schemaRevisions, reads.model = nil, nil, nil, nil, nil
		return nil
	})
	return nil
}

func readTransactionAuthorityStamp(ctx context.Context, database *dbx.Tx) (transactionAuthorityStamp, error) {
	var stamp transactionAuthorityStamp
	err := database.NewQuery("SELECT total_changes() AS writes, schema_version FROM pragma_schema_version").WithContext(ctx).One(&stamp)
	return stamp, err
}
