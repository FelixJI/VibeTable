package workspacev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

type historyRestoreService interface {
	PreviewRestore(context.Context, audit.PreviewParams) (audit.Preview, error)
	ApplyRestore(context.Context, audit.ApplyParams) (audit.RestoreResult, error)
}

func (runtime *Runtime) registerHistoryRestoreHandlers() {
	if runtime.historyRestore == nil {
		return
	}
	runtime.dispatcher.Register(
		"history.previewRestore",
		protocolv2.WorkspaceScope,
		runtime.previewHistoryRestore,
	)
	runtime.dispatcher.Register(
		"history.applyRestore",
		protocolv2.WorkspaceScope,
		runtime.applyHistoryRestore,
	)
}

type previewHistoryRestoreParams struct {
	Collection     string          `json:"collection"`
	ItemID         string          `json:"itemId"`
	TargetRevision string          `json:"targetRevision"`
	Scope          string          `json:"scope"`
	Field          json.RawMessage `json:"field"`
}

func (runtime *Runtime) previewHistoryRestore(
	ctx context.Context,
	_ json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	params, err := decodeStrict[previewHistoryRestoreParams](paramsRaw)
	if err != nil {
		return nil, errors.New("history.request_invalid")
	}
	var field *string
	if len(params.Field) == 0 {
		return nil, errors.New("history.request_invalid")
	}
	if !bytes.Equal(bytes.TrimSpace(params.Field), []byte("null")) {
		var value string
		if err := json.Unmarshal(params.Field, &value); err != nil ||
			strings.TrimSpace(value) == "" {
			return nil, errors.New("history.request_invalid")
		}
		field = &value
	}
	result, err := runtime.historyRestore.PreviewRestore(
		ctx,
		audit.PreviewParams{
			TableID:        params.Collection,
			ItemID:         params.ItemID,
			TargetRevision: params.TargetRevision,
			Scope:          params.Scope,
			Field:          field,
		},
	)
	if err != nil {
		return nil, publicHistoryRestoreError(err)
	}
	return result, nil
}

type applyHistoryRestoreParams struct {
	Collection string `json:"collection"`
	ItemID     string `json:"itemId"`
	Token      string `json:"token"`
}

type applyHistoryRestoreResult struct {
	Collection         string         `json:"collection"`
	ItemID             string         `json:"itemId"`
	RestoredToRevision string         `json:"restoredToRevision"`
	NewRevisionID      *string        `json:"newRevisionId"`
	Item               map[string]any `json:"item"`
	MutationRevision   uint64         `json:"mutationRevision"`
}

func (runtime *Runtime) applyHistoryRestore(
	ctx context.Context,
	wireRaw json.RawMessage,
	paramsRaw json.RawMessage,
) (any, error) {
	wire, err := decodeStrictWorkspaceWire(wireRaw)
	if err != nil {
		return nil, errors.New("workspace.scope_required")
	}
	params, err := decodeStrict[applyHistoryRestoreParams](paramsRaw)
	if err != nil {
		return nil, errors.New("history.request_invalid")
	}
	var restored audit.RestoreResult
	receipt, err := runtime.coordinateBusinessWriteReceipt(
		ctx,
		"history.restore",
		wire.OperationID,
		func(businessContext context.Context) error {
			var applyErr error
			restored, applyErr = runtime.historyRestore.ApplyRestore(
				businessContext,
				audit.ApplyParams{
					TableID: params.Collection,
					ItemID:  params.ItemID,
					Token:   params.Token,
				},
			)
			return applyErr
		},
	)
	if err != nil {
		return nil, publicHistoryRestoreError(err)
	}
	return applyHistoryRestoreResult{
		Collection:         restored.Collection,
		ItemID:             restored.ItemID,
		RestoredToRevision: restored.RestoredToRevision,
		NewRevisionID:      restored.NewRevisionID,
		Item:               restored.Item,
		MutationRevision:   receipt.MutationRevision,
	}, nil
}

func publicHistoryRestoreError(err error) error {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var historyError *audit.Error
	if !errors.As(err, &historyError) {
		return err
	}
	code := strings.TrimSpace(historyError.Code)
	if code == "" {
		return errors.New("history.internal_failed")
	}
	if !strings.HasPrefix(code, "history.") {
		code = "history." + strings.ReplaceAll(code, ".", "_")
	}
	return errors.New(code)
}
