package workspacev2

import (
	"context"
	"encoding/json"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

func (runtime *Runtime) loadAuthorityOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	loaders := []func(
		context.Context,
		string,
		string,
	) (protocolv2.OperationReceipt, bool, error){
		runtime.state.loadOperationReceipt,
		runtime.state.loadExternalFileOperationReceipt,
		runtime.headStore.LoadOperationReceipt,
		runtime.catalog.LoadOperationReceipt,
	}
	if runtime.retention != nil && runtime.retention.store != nil {
		loaders = append(
			loaders,
			runtime.retention.store.LoadOperationReceipt,
		)
	}
	if runtime.replicaConflict != nil {
		loaders = append(
			loaders,
			runtime.replicaConflict.loadOperationReceipt,
		)
	}
	for _, load := range loaders {
		receipt, found, err := load(ctx, workspaceID, operationID)
		if err != nil || found {
			if err == nil && found {
				err = runtime.cleanupAuthorityOperation(
					ctx,
					receipt,
				)
			}
			return receipt, found, err
		}
	}
	return protocolv2.OperationReceipt{}, false, nil
}

func (runtime *Runtime) cleanupAuthorityOperation(
	ctx context.Context,
	receipt protocolv2.OperationReceipt,
) error {
	if receipt.Method != "fileHistory.applyPendingChange" {
		return nil
	}
	var result struct {
		ChangeID string `json:"changeId"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(receipt.Result, &result); err != nil ||
		result.ChangeID == "" ||
		result.State != "applied" {
		if err != nil {
			return err
		}
		return nil
	}
	_, err := runtime.state.db.ExecContext(
		ctx,
		`DELETE FROM pending_file_changes WHERE change_id = ?`,
		result.ChangeID,
	)
	return err
}
