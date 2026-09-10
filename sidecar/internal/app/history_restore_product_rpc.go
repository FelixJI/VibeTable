package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
)

type businessHistoryRestorer interface {
	PreviewBusinessHistoryRestore(context.Context, audit.PreviewParams) (audit.Preview, error)
	ApplyBusinessHistoryRestore(context.Context, string, audit.ApplyParams) (workspacev2.BusinessHistoryRestoreResult, error)
}

func historyPreviewRestoreRegistration(owner businessHistoryRestorer) productrpc.Registration {
	return productrpc.Registration{
		Method: "history.previewRestore", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { return validateHistoryRestoreParams(raw, true) },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var values map[string]json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, err
			}
			table, err := requiredHistoryText(values, "collection")
			if err != nil {
				return nil, err
			}
			item, err := requiredHistoryText(values, "itemId")
			if err != nil {
				return nil, err
			}
			revision, err := requiredHistoryText(values, "targetRevision")
			if err != nil {
				return nil, err
			}
			scope, err := historyText(values, "scope", true)
			if err != nil {
				return nil, err
			}
			// Python treats an empty scope as the default row scope.
			if scope == "" {
				scope = "row"
			}
			field, present, err := optionalHistoryText(values, "field")
			if err != nil {
				return nil, err
			}
			params := audit.PreviewParams{TableID: table, ItemID: item, TargetRevision: revision, Scope: scope}
			if present {
				params.Field = &field
			}
			result, err := owner.PreviewBusinessHistoryRestore(ctx, params)
			if err != nil {
				return nil, publicHistoryReadError(err)
			}
			return result, nil
		},
	}
}

func historyApplyRestoreRegistration(owner businessHistoryRestorer) productrpc.Registration {
	return productrpc.Registration{
		Method: "history.applyRestore", Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { return validateHistoryRestoreParams(raw, false) },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			identity, ok := productrpc.OperationID(ctx)
			if !ok {
				return nil, errors.New("history submit identity is missing")
			}
			var values map[string]json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, err
			}
			table, err := requiredHistoryText(values, "collection")
			if err != nil {
				return nil, err
			}
			item, err := requiredHistoryText(values, "itemId")
			if err != nil {
				return nil, err
			}
			token, err := requiredHistoryText(values, "token")
			if err != nil {
				return nil, err
			}
			result, err := owner.ApplyBusinessHistoryRestore(ctx, identity, audit.ApplyParams{TableID: table, ItemID: item, Token: token})
			if err != nil {
				return nil, publicHistoryReadError(err)
			}
			// The public Product result predates the Workspace mutation receipt field.
			return map[string]any{
				"collection": result.Collection, "itemId": result.ItemID,
				"restoredToRevision": result.RestoredToRevision,
				"newRevisionId":      result.NewRevisionID, "item": result.Item,
			}, nil
		},
	}
}

func validateHistoryRestoreParams(raw json.RawMessage, preview bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := validateHistoryReadProductValue(value, 0); err != nil {
		return err
	}
	if size, err := historyReadProductParamsSize(value); err != nil || size > maxHistoryReadProductParamsBytes {
		return errors.New("history restore parameters exceed the safe size limit")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return errors.New("history restore requires an object")
	}
	required := []string{"collection", "itemId", "token"}
	if preview {
		required = []string{"collection", "itemId", "targetRevision", "scope"}
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, ok := values[key]; !ok {
			return errors.New("history restore required parameter missing")
		}
	}
	if preview {
		allowed["field"] = true
	}
	for key := range values {
		if !allowed[key] {
			return errors.New("history restore unknown parameter")
		}
	}
	return nil
}
