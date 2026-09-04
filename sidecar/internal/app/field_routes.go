package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemacore"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const maxFieldRequestBytes = 1 << 20

func registerFieldRoutes(
	r *router.Router[*core.RequestEvent],
	app core.App,
	migration *fieldchange.MigrationService,
	formulaJobs *jobs.Service,
	logger *slog.Logger,
	protectionVerifier fieldchange.ProtectionSnapshotVerifier,
	gates ...businessWriteGate,
) {
	catalog := fieldchange.NewCatalog(app)
	store := fieldchange.NewPocketBasePlanStore(app)
	planner := fieldchange.NewPlanner(
		catalog, catalog, store, v2.NewIdentityAllocator(nil),
		fieldchange.WithPlannerLogger(logger),
	)
	executorOptions := []fieldchange.ExecutorOption{
		fieldchange.WithMigrationScheduler(migration),
		fieldchange.WithExecutorLogger(logger),
	}
	if formulaJobs != nil {
		executorOptions = append(
			executorOptions,
			fieldchange.WithFormulaBackfillScheduler(formulaJobs),
		)
	}
	if protectionVerifier != nil {
		executorOptions = append(
			executorOptions,
			fieldchange.WithProtectionSnapshotVerifier(protectionVerifier),
		)
	}
	executor := fieldchange.NewExecutor(app, store, executorOptions...)
	schemaCore, coreErr := schemacore.New(catalog, planner, executor)
	if coreErr != nil {
		panic(coreErr)
	}
	tableLifecycle, lifecycleErr := schemacore.NewTableLifecycle(app)
	if lifecycleErr != nil {
		panic(lifecycleErr)
	}

	r.GET("/api/vibetable/v2/schema/tables/{tableId}", func(
		request *core.RequestEvent,
	) error {
		table, err := schemaexecution.Describe(
			request.Request.Context(), app, request.Request.PathValue("tableId"),
		)
		if err != nil {
			if errors.Is(err, schemaexecution.ErrTableNotFound) {
				return request.JSON(http.StatusNotFound, map[string]any{
					"contract": v2.Contract,
					"code":     "schema.table.not_found", "path": "tableId",
					"message": "table was not found", "details": map[string]any{},
					"retryable": false, "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
				})
			}
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, table.Snapshot)
	})

	r.POST("/api/vibetable/v2/schema/tables", func(
		request *core.RequestEvent,
	) error {
		var intent v2.TableCreateIntent
		if err := decodeFieldRequest(request.Request.Body, &intent); err != nil {
			return writeFieldError(request, err)
		}
		if replay, found, err := tableLifecycle.FindReplay(intent); err != nil {
			return writeFieldError(request, err)
		} else if found {
			return request.JSON(http.StatusOK, replay)
		}
		var receipt v2.TableCreateReceipt
		err := runIdempotentBusinessWrite(
			request.Request.Context(),
			gates,
			"schema.table.create",
			intent.OperationID,
			func(ctx context.Context) error {
				var createErr error
				receipt, createErr = tableLifecycle.Create(ctx, intent)
				return createErr
			},
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		if receipt.TableID == "" {
			var replayErr error
			receipt, replayErr = tableLifecycle.Replay(intent)
			if replayErr != nil {
				return writeFieldError(request, replayErr)
			}
		}
		return request.JSON(http.StatusOK, receipt)
	})

	r.POST("/api/vibetable/v2/schema/table-settings", func(
		request *core.RequestEvent,
	) error {
		var intent v2.TableSettingsIntent
		if err := decodeFieldRequest(request.Request.Body, &intent); err != nil {
			return writeFieldError(request, err)
		}
		var receipt v2.TableSettingsReceipt
		err := runIdempotentBusinessWrite(
			request.Request.Context(), gates, "schema.table.settings", intent.OperationID,
			func(ctx context.Context) error {
				var configureErr error
				receipt, configureErr = tableLifecycle.Configure(ctx, intent)
				return configureErr
			},
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		if receipt.TableID == "" {
			var replayErr error
			receipt, replayErr = tableLifecycle.Configure(request.Request.Context(), intent)
			if replayErr != nil {
				return writeFieldError(request, replayErr)
			}
		}
		return request.JSON(http.StatusOK, receipt)
	})

	r.GET("/api/vibetable/v2/field-settings/{tableId}", func(
		request *core.RequestEvent,
	) error {
		tableID := request.Request.PathValue("tableId")
		snapshot, err := schemaCore.Describe(request.Request.Context(), tableID)
		if err != nil {
			return writeFieldError(request, err)
		}
		fieldID := request.Request.URL.Query().Get("fieldId")
		var definition *v2.FieldDefinition
		if fieldID != "" {
			definition, err = catalog.Field(
				request.Request.Context(), tableID, fieldID,
			)
			if err != nil {
				return writeFieldError(request, err)
			}
		}
		return request.JSON(http.StatusOK, map[string]any{
			"contract":                   v2.Contract,
			"tableId":                    tableID,
			"fieldId":                    fieldID,
			"schemaRevision":             snapshot.SchemaRevision,
			"dataRevision":               snapshot.DataRevision,
			"definition":                 definition,
			"capabilities":               snapshot.Capabilities,
			"recommendedDefaultsVersion": 1,
		})
	})

	r.POST("/api/vibetable/v2/field-change/plan", func(
		request *core.RequestEvent,
	) error {
		var intent v2.FieldChangeIntent
		if err := decodeFieldRequest(request.Request.Body, &intent); err != nil {
			return writeFieldError(request, err)
		}
		var plan v2.FieldChangePlan
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"field.change.plan",
			fmt.Sprintf(
				"%s:%s:%s:%s",
				intent.TableID,
				intent.FieldID,
				intent.Action,
				intent.ExpectedSchemaRev,
			),
			func(ctx context.Context) error {
				var planErr error
				plan, planErr = schemaCore.Plan(ctx, intent)
				return planErr
			},
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, plan)
	})

	r.POST("/api/vibetable/v2/field-change/apply", func(
		request *core.RequestEvent,
	) error {
		var body v2.ApplyRequest
		if err := decodeFieldRequest(request.Request.Body, &body); err != nil {
			return writeFieldError(request, err)
		}
		var receipt v2.ApplyReceipt
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"field.change.apply",
			body.OperationID,
			func(ctx context.Context) error {
				var applyErr error
				receipt, applyErr = schemaCore.Apply(ctx, body)
				return applyErr
			},
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})

	r.GET("/api/vibetable/v2/field-recycle-bin/{tableId}", func(
		request *core.RequestEvent,
	) error {
		fields, err := catalog.Fields(
			request.Request.Context(),
			request.Request.PathValue("tableId"),
			true,
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		retired := make([]v2.FieldDefinition, 0)
		for _, field := range fields {
			if field.Lifecycle.State == v2.LifecycleRetired {
				retired = append(retired, field)
			}
		}
		return request.JSON(http.StatusOK, map[string]any{
			"contract": v2.Contract,
			"fields":   retired,
		})
	})

	r.GET("/api/vibetable/v2/field-change/status/{jobId}", func(
		request *core.RequestEvent,
	) error {
		status, err := migration.Status(
			request.Request.Context(), request.Request.PathValue("jobId"),
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, status)
	})

	r.POST("/api/vibetable/v2/field-change/cancel/{jobId}", func(
		request *core.RequestEvent,
	) error {
		jobID := request.Request.PathValue("jobId")
		var status v2.MigrationStatus
		err := runBusinessWrite(
			request.Request.Context(),
			gates,
			"field.change.cancel",
			jobID,
			func(ctx context.Context) error {
				var cancelErr error
				status, cancelErr = migration.Cancel(ctx, jobID)
				return cancelErr
			},
		)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, status)
	})
}

func decodeFieldRequest(reader io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, maxFieldRequestBytes+1))
	if err != nil {
		return &fieldchange.ProductError{
			Code: "field.contract.invalid", Path: "",
			Message: "request body could not be read",
		}
	}
	if len(raw) > maxFieldRequestBytes {
		return &fieldchange.ProductError{
			Code: "field.contract.too_large", Path: "",
			Message: "request body exceeds the size limit",
		}
	}
	if err := v2.StrictDecode(raw, target); err != nil {
		return &fieldchange.ProductError{
			Code: "field.contract.invalid", Path: "",
			Message: err.Error(),
		}
	}
	return nil
}

type fieldErrorResponse struct {
	status    int
	code      string
	path      string
	message   string
	details   map[string]any
	retryable bool
}

func classifyFieldError(err error) fieldErrorResponse {
	status := http.StatusUnprocessableEntity
	code := "field.internal.failed"
	path := ""
	message := "field settings operation failed"
	details := map[string]any{}
	var productErr *fieldchange.ProductError
	var contractErr *v2.ProductError
	switch {
	case errors.As(err, &productErr):
		code, path, message = productErr.Code, productErr.Path, productErr.Message
		if productErr.Details != nil {
			details = productErr.Details
		}
	case errors.As(err, &contractErr):
		code, path, message = contractErr.Code, contractErr.Path, contractErr.Message
		if contractErr.Details != nil {
			details = contractErr.Details
		}
	case errors.Is(err, fieldchange.ErrFieldNotFound), errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
		code, path, message = "field.not_found", "fieldId", "field was not found"
	default:
		status = http.StatusInternalServerError
	}
	if code == "field.change.schema_conflict" ||
		code == "field.change.data_conflict" ||
		code == "field.change.operation_conflict" ||
		code == "schema.table.revision_conflict" ||
		code == "schema.table.operation_conflict" {
		status = http.StatusConflict
	}
	if code == "field.change.plan_not_found" ||
		code == "field.migration.not_found" {
		status = http.StatusNotFound
	}
	if code == "field.change.plan_expired" {
		status = http.StatusGone
	}
	return fieldErrorResponse{
		status: status, code: code, path: path, message: message,
		details: details, retryable: status >= http.StatusInternalServerError,
	}
}

func writeFieldError(request *core.RequestEvent, err error) error {
	projected := classifyFieldError(err)
	return request.JSON(projected.status, map[string]any{
		"contract": v2.Contract,
		"code":     projected.code, "path": projected.path, "message": projected.message,
		"details": projected.details, "retryable": projected.retryable,
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
}
