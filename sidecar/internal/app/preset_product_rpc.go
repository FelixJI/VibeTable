package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func presetRegistration(method string, service *metadata.PresetService, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { _, err := metadata.DecodePresetRequest(method, raw); return err },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			request, err := metadata.DecodePresetRequest(method, raw)
			if err != nil {
				return nil, err
			}
			var result map[string]any
			switch method {
			case "preset.list":
				return service.List(ctx, request.Collection)
			case "preset.save":
				err = runIdempotentBusinessWrite(ctx, gates, "metadata.presets.upsert", "preset:save:"+request.OperationID, func(ctx context.Context) error { var err error; result, err = service.Save(ctx, request); return err })
			case "preset.delete":
				err = runIdempotentBusinessWrite(ctx, gates, "metadata.presets.delete", "preset:delete:"+request.OperationID, func(ctx context.Context) error { var err error; result, err = service.Delete(ctx, request); return err })
			default:
				return nil, errors.New("unknown Preset method")
			}
			var domain *metadata.PresetError
			if errors.As(err, &domain) {
				return nil, &productrpc.PresetError{Code: domain.Code, Message: domain.Message, Field: domain.Field}
			}
			return result, err
		}}
}
