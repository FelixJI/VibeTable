package app

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
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
) schemaFieldChangeDomain {
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

	domain := schemaFieldChangeDomain{
		fieldSettingsDescribeDomain: fieldSettingsDescribeDomain{schema: schemaCore, fields: catalog},
		core:                        schemaCore, tables: tableLifecycle, catalog: catalog, migration: migration, gates: gates,
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
		receipt, err := domain.CreateTable(request.Request.Context(), intent)
		if err != nil {
			return writeFieldError(request, err)
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
		plan, err := domain.Plan(request.Request.Context(), intent)
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
		receipt, err := domain.Apply(request.Request.Context(), body)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})

	r.GET("/api/vibetable/v2/field-recycle-bin/{tableId}", func(
		request *core.RequestEvent,
	) error {
		result, err := domain.RecycledFields(request.Request.Context(), request.Request.PathValue("tableId"))
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})

	r.GET("/api/vibetable/v2/field-change/status/{jobId}", func(
		request *core.RequestEvent,
	) error {
		status, err := domain.Status(
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
		status, err := domain.Cancel(request.Request.Context(), jobID)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, status)
	})
	return domain
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
	var schemaErr *schemaerror.ProductError
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
	case errors.As(err, &schemaErr):
		code, path, message = schemaErr.Code, schemaErr.Path, schemaErr.Message
		if schemaErr.Details != nil {
			details = schemaErr.Details
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
