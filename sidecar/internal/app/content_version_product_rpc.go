package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func contentVersionRegistration(method string, owner *metadata.ContentVersions, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(raw json.RawMessage) error { _, err := metadata.DecodeVersionParams(method, raw); return err }, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		params, err := metadata.DecodeVersionParams(method, raw)
		if err != nil {
			return nil, err
		}
		var result any
		switch method {
		case "version.list":
			result, err = owner.List(ctx, params)
		case "version.compare":
			result, err = owner.Compare(ctx, params)
		default:
			err = runBusinessWrite(ctx, gates, metadata.VersionWriteKind(method), params.OperationID, func(writeCtx context.Context) error {
				var applyErr error
				result, applyErr = owner.Write(writeCtx, method, params)
				return applyErr
			})
		}
		var domain *metadata.VersionError
		if errors.As(err, &domain) {
			return nil, &productrpc.VersionError{Code: domain.Code, Message: domain.Message}
		}
		return result, err
	}}
}
