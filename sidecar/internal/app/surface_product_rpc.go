package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	wb "github.com/vibetable/vibetable/sidecar/internal/contracts/workbench"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func surfaceListRegistration(service *metadata.SurfaceService) productrpc.Registration {
	return surfaceRegistration("interface.list", func(ctx context.Context, _ json.RawMessage) (any, error) { return service.List(ctx) })
}
func surfaceLoadRegistration(service *metadata.SurfaceService) productrpc.Registration {
	return surfaceRegistration("interface.load", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request wb.InterfaceLoadRequest
		if err := metadata.DecodeSurfaceParams("interface.load", raw, &request); err != nil {
			return nil, err
		}
		return service.Load(ctx, request.InterfaceId)
	})
}
func surfaceCommitRegistration(service *metadata.SurfaceService, gates ...businessWriteGate) productrpc.Registration {
	return surfaceRegistration("interface.commit", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request wb.InterfaceCommitRequest
		if err := metadata.DecodeSurfaceParams("interface.commit", raw, &request); err != nil {
			return nil, err
		}
		var result wb.InterfaceSnapshot
		err := runIdempotentBusinessWrite(ctx, gates, "metadata.interfaces.upsert", request.IdempotencyKey, func(writeCtx context.Context) error {
			var err error
			result, err = service.Commit(writeCtx, request)
			return err
		})
		return result, err
	})
}
func surfaceDeleteRegistration(service *metadata.SurfaceService, gates ...businessWriteGate) productrpc.Registration {
	return surfaceRegistration("interface.delete", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request wb.InterfaceDeleteRequest
		if err := metadata.DecodeSurfaceParams("interface.delete", raw, &request); err != nil {
			return nil, err
		}
		var result wb.InterfaceDeleteResult
		err := runIdempotentBusinessWrite(ctx, gates, "metadata.interfaces.delete", request.IdempotencyKey, func(writeCtx context.Context) error {
			var err error
			result, err = service.Delete(writeCtx, request)
			return err
		})
		return result, err
	})
}
func surfaceRegistration(method string, handler productrpc.Handler) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope,
		ValidateParams: func(raw json.RawMessage) error { return metadata.DecodeSurfaceParams(method, raw, nil) },
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			result, err := handler(ctx, raw)
			var domain *metadata.SurfaceError
			if errors.As(err, &domain) {
				return nil, &productrpc.SurfaceError{Code: domain.Code, Message: domain.Message, Path: domain.Path}
			}
			return result, err
		},
	}
}
