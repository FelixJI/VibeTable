package relation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func committed(receipt mutation.Receipt) bool {
	return receipt.Status == mutation.StatusApplied || receipt.Status == mutation.StatusReplayed
}

func (service *Service) CreateTarget(ctx context.Context, request CreateTargetRequest) (CreateTargetResult, error) {
	prepared, err := service.kernel.ApplyPrepared(ctx, mutation.PreparedIntent{
		Kind: "relation.create-target", RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey, Actor: request.Actor,
		// TargetTableID is a json:"-" guard and must still bind the intent.
		Params: struct {
			Request       CreateTargetRequest
			TargetTableID string
			CallerParams  json.RawMessage
		}{request, request.TargetTableID, request.CallerParams},
	}, func(txApp core.App) (mutation.Request, error) {
		return New(txApp, nil, nil).compileCreateTarget(ctx, request)
	}, func(txApp core.App, receipt mutation.Receipt) (any, error) {
		return New(txApp, nil, nil).createdTarget(ctx, request.RelationID, receipt)
	})
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return CreateTargetResult{}, err
	}
	if !committed(prepared.Receipt) || len(prepared.Receipt.AffectedRows) != 1 {
		return CreateTargetResult{}, relationError("relation.target_create_pending", "target record creation has not committed")
	}
	var target TargetRef
	if mutation.DecodeStrict(prepared.Result, &target) != nil || target.TableID == "" || target.RecordID == "" || target.Label == "" {
		return CreateTargetResult{}, relationError("relation.storage_failed", "committed target result is invalid")
	}
	return CreateTargetResult{Target: target, Receipt: prepared.Receipt}, err
}

func (service *Service) createdTarget(ctx context.Context, relationID string, receipt mutation.Receipt) (TargetRef, error) {
	if len(receipt.AffectedRows) != 1 {
		return TargetRef{}, relationError("relation.storage_failed", "created target receipt is invalid")
	}
	resolved, err := service.resolve(ctx, relationID)
	if err != nil {
		return TargetRef{}, err
	}
	target, err := schemaexecution.Describe(ctx, service.app, resolved.descriptor.TargetTableID)
	if err != nil {
		return TargetRef{}, err
	}
	id := receipt.AffectedRows[0].RecordID
	rows, err := service.readSourceRows(ctx, target.Snapshot.TableID, []string{id})
	if err != nil || len(rows) != 1 {
		return TargetRef{}, relationError("relation.storage_failed", "created target record could not be read")
	}
	label := id
	if value := rows[0][targetLabelField(target)]; value != nil && fmt.Sprint(value) != "" {
		label = fmt.Sprint(value)
	}
	return TargetRef{TableID: target.Snapshot.TableID, RecordID: id, Label: label}, nil
}

type SingleRequest struct {
	RelationID     string          `json:"relationId"`
	SourceRecordID string          `json:"sourceRecordId"`
	SchemaRevision string          `json:"schemaRevision"`
	Target         *TargetRef      `json:"target"`
	RequestID      string          `json:"requestId"`
	IdempotencyKey string          `json:"idempotencyKey"`
	ExpectedDigest *string         `json:"expectedDigest,omitempty"`
	Actor          mutation.Actor  `json:"actor"`
	CallerParams   json.RawMessage `json:"-"`
}

func (service *Service) UpdateSingle(ctx context.Context, request SingleRequest) (mutation.Receipt, error) {
	prepared, err := service.kernel.ApplyPrepared(ctx, mutation.PreparedIntent{
		Kind: "relation.update-single", RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey, Actor: request.Actor,
		Params: struct {
			Request      SingleRequest
			CallerParams json.RawMessage
		}{request, request.CallerParams},
	}, func(txApp core.App) (mutation.Request, error) {
		transaction := New(txApp, nil, nil)
		resolved, err := transaction.resolve(ctx, request.RelationID)
		if err != nil {
			return mutation.Request{}, err
		}
		if resolved.descriptor.Cardinality != "one" {
			return mutation.Request{}, relationError("relation.cardinality", "single relation is unavailable")
		}
		if request.SourceRecordID == "" || request.SchemaRevision != resolved.definition.Snapshot.SchemaRevision {
			return mutation.Request{}, relationError("relation.request.invalid", "single relation request is incomplete or stale")
		}
		rows, err := transaction.readSourceRows(ctx, resolved.definition.Snapshot.TableID, []string{request.SourceRecordID})
		if err != nil {
			return mutation.Request{}, err
		}
		if len(rows) != 1 {
			return mutation.Request{}, relationError("relation.source_not_found", "source record was not found")
		}
		result := []TargetRef{}
		if request.Target != nil {
			if request.Target.TableID != resolved.descriptor.TargetTableID || request.Target.RecordID == "" {
				return mutation.Request{}, relationError("relation.target_invalid", "relation target belongs to another table")
			}
			result = append(result, *request.Target)
		}
		return transaction.deltaMutation(DeltaRequest{RelationID: request.RelationID, SourceRecordID: request.SourceRecordID, SchemaRevision: request.SchemaRevision, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey, Actor: request.Actor, ExpectedDigest: request.ExpectedDigest}, resolved, result), nil
	}, func(core.App, mutation.Receipt) (any, error) { return map[string]any{"target": request.Target}, nil })
	if err != nil && err != writecoordinator.ErrBusinessReplay {
		return mutation.Receipt{}, err
	}
	if !committed(prepared.Receipt) {
		return prepared.Receipt, err
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(prepared.Result, &result) != nil || len(result) != 1 || result["target"] == nil {
		return mutation.Receipt{}, relationError("relation.storage_failed", "committed single relation result is invalid")
	}
	var target *TargetRef
	if mutation.DecodeStrict(result["target"], &target) != nil || target != nil && (target.TableID == "" || target.RecordID == "") {
		return mutation.Receipt{}, relationError("relation.storage_failed", "committed single relation target is invalid")
	}
	return prepared.Receipt, err
}

// Transaction compilation reads from the transaction's app, never a query port
// bound to the outer app. Ordinary response projections retain QueryPort rules.
func (service *Service) readSourceRows(ctx context.Context, tableID string, ids []string) ([]map[string]any, error) {
	if service.queries != nil {
		return service.queries.ReadRows(ctx, tableID, ids)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := queryschema.New(service.app.DataDir())
	if err != nil {
		return nil, err
	}
	// QueryPort and its schema adapter both use this exact app handle, retaining
	// normal visibility rules inside the existing write transaction.
	return query.NewPort(service.app, source).ReadRows(ctx, tableID, ids)
}
