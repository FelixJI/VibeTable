package productrpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

func AttachmentListRegistration(app core.App, manager *attachments.Manager) Registration {
	return Registration{
		Method: "file.list", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error {
			_, _, _, err := attachmentListParams(raw)
			return err
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			tableID, recordID, fieldID, err := attachmentListParams(raw)
			if err != nil {
				return nil, err
			}
			refs, err := manager.RefsByID(ctx, app, tableID, recordID, fieldID)
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			if err != nil {
				return nil, attachmentProductError(err)
			}
			return map[string]any{"attachments": refs}, nil
		},
	}
}

func attachmentListParams(raw json.RawMessage) (string, string, string, error) {
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) != 3 {
		return "", "", "", errors.New("file.list requires tableId, recordId, and fieldId")
	}
	tableID, recordID, fieldID := values["tableId"], values["recordId"], values["fieldId"]
	if tableID == "" || recordID == "" || fieldID == "" {
		return "", "", "", errors.New("file.list requires non-empty tableId, recordId, and fieldId")
	}
	return tableID, recordID, fieldID, nil
}

func attachmentProductError(err error) error {
	var productError *mutation.ProductError
	if !errors.As(err, &productError) {
		return err
	}
	return &PublicError{
		Code: productError.Code, Path: productError.Path,
		Message: productError.Message, Details: productError.Details,
		Retryable: productError.Retryable,
	}
}
