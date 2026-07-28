package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	"github.com/vibetable/vibetable/sidecar/internal/snapshot"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func (runtime *Runtime) protectionSnapshotForOperation(
	ctx context.Context,
	token writecoordinator.Token,
	parentOperationID string,
	method string,
) (snapshot.Record, error) {
	parent, err := uuid.Parse(parentOperationID)
	if err != nil || method == "" {
		return snapshot.Record{}, errors.New(
			"snapshot.protection_operation_invalid",
		)
	}
	operationID := uuid.NewSHA1(
		parent,
		[]byte(method+":protection"),
	).String()
	params, err := json.Marshal(map[string]any{
		"parentOperationId": parentOperationID,
	})
	if err != nil {
		return snapshot.Record{}, err
	}
	if receipt, found, err := runtime.catalog.LoadOperationReceipt(
		ctx,
		token.WorkspaceID,
		operationID,
	); err != nil {
		return snapshot.Record{}, err
	} else if found {
		var result struct {
			SnapshotID string `json:"snapshotId"`
		}
		if json.Unmarshal(receipt.Result, &result) != nil ||
			!validUUID(result.SnapshotID) {
			return snapshot.Record{},
				errors.New("snapshot.protection_receipt_corrupt")
		}
		expected, buildErr :=
			protocolv2.BuildOperationReceiptForSession(
				method,
				protocolv2.WorkspaceScope,
				token.WorkspaceID,
				operationID,
				token.SessionEpoch,
				params,
				result,
			)
		if buildErr != nil ||
			receipt.Method != expected.Method ||
			receipt.Scope != expected.Scope ||
			receipt.RequestHash != expected.RequestHash ||
			!bytes.Equal(receipt.Result, expected.Result) {
			return snapshot.Record{},
				protocolv2.ErrOperationConflict
		}
		record, loadErr := runtime.snapshotRecord(
			ctx,
			result.SnapshotID,
		)
		if loadErr != nil || !record.Pinned {
			return snapshot.Record{}, errors.Join(
				errors.New("snapshot.protection_receipt_corrupt"),
				loadErr,
			)
		}
		return record, nil
	}
	captureContext, err := snapshot.WithOperationReceiptBuilder(
		ctx,
		func(
			record snapshot.Record,
		) (protocolv2.OperationReceipt, error) {
			return protocolv2.BuildOperationReceiptForSession(
				method,
				protocolv2.WorkspaceScope,
				token.WorkspaceID,
				operationID,
				token.SessionEpoch,
				params,
				map[string]any{
					"snapshotId": record.SnapshotID,
				},
			)
		},
	)
	if err != nil {
		return snapshot.Record{}, err
	}
	record, created, err := runtime.snapshots.Capture(
		captureContext,
		snapshot.CaptureRequest{
			WorkspaceID: runtime.manifest.WorkspaceID,
			Authority:   token.Authority(),
			Trigger:     snapshot.TriggerProtection,
			Pinned:      true,
		},
	)
	if err != nil || !created ||
		record.SnapshotID == "" || !record.Pinned {
		return snapshot.Record{}, errors.Join(
			errors.New("snapshot.protection_snapshot_failed"),
			err,
		)
	}
	runtime.enqueueReplicaSnapshots(ctx)
	return record, nil
}
