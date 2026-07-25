package realtime

import (
	"context"
	"fmt"
	"regexp"

	"github.com/vibetable/vibetable/sidecar/internal/schema"
)

var dataRevisionPattern = regexp.MustCompile(`^data_([0-9]{4,})$`)

type RevisionSource interface {
	GetRevision(context.Context, string) (int64, error)
	GetDataRevision(context.Context, string) (int64, error)
}

type ReconcileRequest struct {
	TableID        string `json:"tableId"`
	SchemaRevision string `json:"schemaRevision"`
	DataRevision   string `json:"dataRevision"`
}

type ReconcileResult struct {
	TableID               string `json:"tableId"`
	ClientSchemaRevision  string `json:"clientSchemaRevision"`
	ClientDataRevision    string `json:"clientDataRevision"`
	CurrentSchemaRevision string `json:"currentSchemaRevision"`
	CurrentDataRevision   string `json:"currentDataRevision"`
	Action                string `json:"action"`
}

func Reconcile(
	ctx context.Context,
	source RevisionSource,
	request ReconcileRequest,
) (ReconcileResult, error) {
	if source == nil || request.TableID == "" {
		return ReconcileResult{}, &Error{
			Code:    "realtime.request.invalid",
			Message: "tableId and revision source are required",
		}
	}
	clientSchema, err := schema.ParseSchemaRevision(
		request.SchemaRevision,
	)
	if err != nil {
		return ReconcileResult{}, &Error{
			Code:    "realtime.request.invalid",
			Message: "schemaRevision is invalid",
		}
	}
	clientData, err := parseDataRevision(request.DataRevision)
	if err != nil {
		return ReconcileResult{}, &Error{
			Code:    "realtime.request.invalid",
			Message: "dataRevision is invalid",
		}
	}
	currentSchema, err := source.GetRevision(ctx, request.TableID)
	if err != nil {
		return ReconcileResult{}, &Error{
			Code:      "realtime.storage_failed",
			Message:   "current schema revision is unavailable",
			Retryable: true,
		}
	}
	currentData, err := source.GetDataRevision(ctx, request.TableID)
	if err != nil {
		return ReconcileResult{}, &Error{
			Code:      "realtime.storage_failed",
			Message:   "current data revision is unavailable",
			Retryable: true,
		}
	}
	action := "none"
	if clientSchema != currentSchema {
		action = "reload-schema"
	} else if clientData != currentData {
		action = "refresh-data"
	}
	return ReconcileResult{
		TableID:               request.TableID,
		ClientSchemaRevision:  request.SchemaRevision,
		ClientDataRevision:    request.DataRevision,
		CurrentSchemaRevision: schema.FormatSchemaRevision(currentSchema),
		CurrentDataRevision:   formatDataRevision(currentData),
		Action:                action,
	}, nil
}

func parseDataRevision(value string) (int64, error) {
	match := dataRevisionPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid data revision")
	}
	var revision int64
	if _, err := fmt.Sscanf(match[1], "%d", &revision); err != nil {
		return 0, err
	}
	return revision, nil
}

func formatDataRevision(revision int64) string {
	return fmt.Sprintf("data_%04d", revision)
}
