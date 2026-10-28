package mutation

import (
	"context"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
)

func businessRuleError(err error) error {
	if strings.HasPrefix(err.Error(), "transaction.") {
		return mutationError(err.Error(), nil, "registered business rule rejected the write", nil, false)
	}
	return err
}

func captureBusinessRules(ctx context.Context, app core.App, preview PreviewResult) ([]transactionrules.Guard, error) {
	touched := map[string]map[string]bool{preview.Definition.Snapshot.TableID: {}}
	fields := touched[preview.Definition.Snapshot.TableID]
	for _, op := range preview.Operations {
		if op.Kind != OperationUpdate && op.Kind != OperationSetAttachments {
			fields["*"] = true
		}
		if op.Attachment != nil {
			fields[op.Attachment.FieldID] = true
		}
		for _, field := range preview.Definition.Snapshot.Fields {
			_, set := op.Values[field.Identity.PhysicalName]
			if set {
				fields[field.Identity.FieldID] = true
			}
			if field.Relation != nil && field.Relation.ReciprocalFieldID != "" && (set || fields["*"]) {
				target := field.Relation.TargetTableID
				if touched[target] == nil {
					touched[target] = map[string]bool{}
				}
				touched[target][field.Relation.ReciprocalFieldID] = true
			}
		}
	}
	guards, err := transactionrules.Capture(ctx, app, touched)
	if err != nil {
		return nil, businessRuleError(err)
	}
	return guards, nil
}
