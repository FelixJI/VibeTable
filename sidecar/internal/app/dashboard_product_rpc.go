package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/productrpc"
)

func dashboardRegistration(method string, service *metadata.DashboardService, gates ...businessWriteGate) productrpc.Registration {
	return productrpc.Registration{Method: method, Scope: productcapabilities.WorkspaceScope, ValidateParams: func(raw json.RawMessage) error { _, err := metadata.DecodeDashboardParams(method, raw); return err }, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := metadata.DecodeDashboardParams(method, raw)
		if err != nil {
			return nil, err
		}
		var result any
		switch method {
		case "insights.listDashboards":
			result, err = service.Read(ctx, "")
		case "insights.readDashboardWorkspace":
			result, err = service.Read(ctx, p["dashboardId"].(string))
		case "insights.executeDashboardQuery":
			result, err = service.Query(ctx, p)
		case "insights.dashboardQueryLimits":
			result = metadata.DashboardQueryLimits()
		case "insights.panelManifest":
			result = metadata.DashboardManifest()
		case "insights.saveDashboardDraft":
			key := p["idempotencyKey"].(string)
			err = runBusinessWrite(ctx, gates, "metadata.dashboards.upsert", key, func(writeCtx context.Context) error {
				var applyErr error
				result, applyErr = service.Save(writeCtx, p)
				return applyErr
			})
		case "insights.deleteDashboardWorkspace":
			key, ok := productrpc.OperationID(ctx)
			if !ok {
				return nil, errors.New("validated Dashboard operation identity is missing")
			}
			err = runBusinessWrite(ctx, gates, "metadata.dashboards.delete", key, func(writeCtx context.Context) error {
				var applyErr error
				result, applyErr = service.Delete(writeCtx, p["dashboardId"].(string), key)
				return applyErr
			})
		}
		var domain *metadata.DashboardError
		if errors.As(err, &domain) {
			return nil, &productrpc.DashboardError{Code: domain.Code, Message: domain.Message, Field: domain.Field}
		}
		return result, err
	}}
}
