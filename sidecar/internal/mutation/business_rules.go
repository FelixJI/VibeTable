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
	touched := transactionrules.WriteSet{}
	for _, op := range preview.Operations {
		attachment := ""
		if op.Attachment != nil {
			attachment = op.Attachment.FieldID
		}
		touched.AddNative(preview.Definition.Snapshot, op.Kind != OperationUpdate && op.Kind != OperationSetAttachments, op.Values, attachment)
	}
	guards, err := transactionrules.Capture(ctx, app, touched)
	if err != nil {
		return nil, businessRuleError(err)
	}
	return guards, nil
}
