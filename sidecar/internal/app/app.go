// Package app composes the PocketBase process without exposing PocketBase
// details to the desktop launch protocol.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/fieldresource"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/health"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/launch"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

const (
	healthPath    = "/api/vibetable/v1/health"
	buildInfoPath = "/api/vibetable/v1/build-info"
	shutdownPath  = "/api/vibetable/v1/shutdown"
)

type Options struct {
	DataDir            string
	Dev                bool
	Session            auth.Secret
	Logger             *slog.Logger
	ReadyWriter        io.Writer
	OnBootstrapReady   func() error
	OnWorkspaceV2Ready func(*workspacev2.Runtime) error
	WorkspaceV2        *WorkspaceV2Options
}

type WorkspaceV2Options struct {
	WorkspaceID  string
	SessionEpoch uint64
	FenceEpoch   uint64
	ClaimID      string
	ReplicaRoot  string
}

func New(options Options) (*pocketbase.PocketBase, error) {
	if options.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if options.Session.IsZero() {
		return nil, errors.New("session secret is required")
	}
	if options.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if options.ReadyWriter == nil {
		return nil, errors.New("ready writer is required")
	}
	if options.WorkspaceV2 != nil {
		if err := workspacev2.ValidateStartupBinding(
			options.DataDir,
			options.WorkspaceV2.WorkspaceID,
			options.WorkspaceV2.SessionEpoch,
			options.WorkspaceV2.FenceEpoch,
			options.WorkspaceV2.ClaimID,
		); err != nil {
			return nil, err
		}
	}
	if err := formula.ValidateRuntime(); err != nil {
		return nil, err
	}
	snapshotKey, err := options.Session.DeriveKey("query-snapshot")
	if err != nil {
		return nil, err
	}
	attachmentKey, err := options.Session.DeriveKey("attachment-capability")
	if err != nil {
		return nil, err
	}
	restoreKey, err := options.Session.DeriveKey("history-restore")
	if err != nil {
		return nil, err
	}
	backupReceiptKey, err := options.Session.DeriveKey("backup-receipt")
	if err != nil {
		return nil, err
	}
	attachmentManager, err := attachments.New(attachmentKey)
	if err != nil {
		return nil, err
	}
	querySource, err := queryschema.New(options.DataDir)
	if err != nil {
		return nil, err
	}
	formulaCompiler := formula.NewCompiler(formula.DefaultLimits())
	formulaCalculator := formula.NewCalculator(formulaCompiler)

	pocketbase.Version = buildinfo.PocketBaseVersion
	pb := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  options.DataDir,
		DefaultDev:      options.Dev,
		HideStartBanner: true,
	})
	migrations.Register(pb)
	queryPort := query.NewPort(pb, querySource, snapshotKey)
	realtimeHub := realtime.New(pb)
	jobService := jobs.New(
		pb,
		nil,
		jobs.WithTaskPublisher(realtimeHub),
		jobs.WithDataPublisher(realtimeHub),
	)
	mutationOptions := []mutation.Option{
		mutation.WithFormulaCalculator(computed.New(
			lookup.NewCalculator(),
			formulaCalculator,
		)),
		mutation.WithAttachmentManager(attachmentManager),
		mutation.WithPublisher(jobService),
		mutation.WithPublishContext(jobService.PublishContext()),
	}
	if barrier := newE2EMutationBarrierFromEnvironment(); barrier != nil {
		mutationOptions = append(
			mutationOptions,
			mutation.WithFaultInjector(barrier),
		)
	}
	mutationKernel := mutation.New(
		pb,
		mutation.MetadataSchemaSource{},
		mutationOptions...,
	)
	jobService.SetKernel(mutationKernel)
	ledgerRoot := filepath.Join(filepath.Dir(options.DataDir), "audit")
	ledger, err := auditledger.Open(ledgerRoot)
	if err != nil {
		jobService.Shutdown()
		return nil, err
	}
	auditDrainer, err := auditledger.NewDrainer(ledger, 256)
	if err != nil {
		_ = ledger.Close()
		jobService.Shutdown()
		return nil, err
	}
	auditOutbox, err := auditledger.NewPocketBaseOutbox(pb)
	if err != nil {
		_ = ledger.Close()
		jobService.Shutdown()
		return nil, err
	}
	auditOptions := []audit.Option{
		audit.WithAttachmentHistory(attachmentManager),
	}
	if options.WorkspaceV2 != nil {
		auditOptions = append(
			auditOptions,
			audit.WithLedgerHistory(ledger),
		)
	}
	auditService, err := audit.New(
		pb,
		mutationKernel,
		mutation.MetadataSchemaSource{},
		restoreKey,
		auditOptions...,
	)
	if err != nil {
		_ = ledger.Close()
		jobService.Shutdown()
		return nil, err
	}
	relationService := relation.New(pb, queryPort, mutationKernel)
	migrationOptions := []fieldchange.MigrationOption{
		fieldchange.WithMigrationContext(jobService.Context()),
		fieldchange.WithMigrationLogger(options.Logger),
		fieldchange.WithBackfillWriter(func(
			ctx context.Context,
			plan v2.FieldChangePlan,
			jobID string,
			recordID string,
			value any,
		) error {
			_, err := mutationKernel.Apply(ctx, mutation.Request{
				ContractVersion: mutation.ContractVersion,
				RequestID:       "field-backfill-" + jobID + "-" + recordID,
				IdempotencyKey:  "field-backfill-" + jobID + "-" + recordID,
				TableID:         plan.Intent.TableID,
				SchemaRevision:  plan.ExpectedSchemaRev,
				Operations: []mutation.Operation{{
					Kind:     mutation.OperationUpdate,
					RecordID: &recordID,
					Values: map[string]any{
						plan.After.Identity.FieldID: value,
					},
				}},
				Actor: mutation.Actor{
					Type: "system", ID: "field-backfill",
				},
				InternalBypassMigrationFence: true,
			})
			return err
		}),
	}
	if fault := newE2EMigrationFaultFromEnvironment(); fault != nil {
		migrationOptions = append(migrationOptions, fault)
	}
	fieldMigration := fieldchange.NewMigrationService(
		pb, fieldchange.NewPocketBasePlanStore(pb), migrationOptions...,
	)
	startedAt := time.Now().UTC()
	var workspaceRuntime *workspacev2.Runtime
	pb.OnServe().BindFunc(func(event *core.ServeEvent) error {
		// VibeTable owns the local data lifecycle. Never expose PocketBase's
		// first-run superuser installer to the desktop user.
		event.InstallerFunc = nil
		var shutdownOnce sync.Once
		requestShutdown := func() {
			shutdownOnce.Do(func() {
				go func() {
					// Let the accepted RPC response leave the socket before
					// closing the listener and runtime.
					time.Sleep(50 * time.Millisecond)
					ctx, cancel := context.WithTimeout(
						context.Background(),
						10*time.Second,
					)
					defer cancel()
					if err := event.Server.Shutdown(ctx); err != nil {
						options.Logger.Error(
							"graceful shutdown failed",
							"error",
							err,
						)
					}
				}()
			})
		}

		if options.OnBootstrapReady != nil {
			if err := options.OnBootstrapReady(); err != nil {
				return err
			}
		}
		rawListener, err := launch.OpenLoopback()
		if err != nil {
			return err
		}

		event.Server.Addr = rawListener.Addr().String()
		event.Router.Bind(&hook.Handler[*core.RequestEvent]{
			Id:       "vibetableSessionAuth",
			Priority: -10_000,
			Func: func(request *core.RequestEvent) error {
				if !options.Session.Matches(request.Request.Header.Get(auth.HeaderName)) {
					return request.JSON(http.StatusUnauthorized, map[string]any{
						"code":    "session.unauthorized",
						"message": "valid sidecar session secret required",
					})
				}
				return request.Next()
			},
		})
		if options.WorkspaceV2 != nil {
			bindWorkspaceV2WriteBoundary(event)
		}
		businessGate := businessWriteGate(func(
			ctx context.Context,
			kind string,
			identity string,
			apply func(context.Context) error,
		) error {
			if options.WorkspaceV2 == nil {
				return apply(ctx)
			}
			if workspaceRuntime == nil {
				return errors.New("workspace.business_write_unavailable")
			}
			return workspaceRuntime.CoordinateBusinessWrite(
				ctx,
				kind,
				identity,
				apply,
			)
		})
		idempotentBusinessGate := businessWriteGate(func(
			ctx context.Context,
			kind string,
			identity string,
			apply func(context.Context) error,
		) error {
			if options.WorkspaceV2 == nil {
				return apply(ctx)
			}
			if workspaceRuntime == nil {
				return errors.New("workspace.business_write_unavailable")
			}
			return workspaceRuntime.CoordinateIdempotentBusinessWrite(
				ctx,
				kind,
				identity,
				apply,
			)
		})

		event.Router.GET(healthPath, func(request *core.RequestEvent) error {
			snapshot, status := health.Check(
				request.App.DataDir(),
				startedAt,
				buildinfo.Current(migrations.Hash()),
				time.Now().UTC(),
				func() error {
					if _, err := request.App.DB().NewQuery("SELECT 1").Execute(); err != nil {
						return err
					}
					_, err := request.App.FindCollectionByNameOrId("vibetable_tables")
					return err
				},
			)
			return request.JSON(status, snapshot)
		})
		event.Router.GET(buildInfoPath, func(request *core.RequestEvent) error {
			return request.JSON(http.StatusOK, buildinfo.Current(migrations.Hash()))
		})
		if options.WorkspaceV2 == nil {
			registerAdminRoutes(event)
		}
		registerSchemaRoutes(
			event.Router,
			schemaapi.New(pb),
			jobService,
			businessGate,
			idempotentBusinessGate,
		)
		var fieldProtectionVerifier fieldchange.ProtectionSnapshotVerifier
		if options.WorkspaceV2 != nil {
			fieldProtectionVerifier = func(
				ctx context.Context,
				snapshotID string,
			) error {
				if workspaceRuntime == nil {
					return errors.New("workspace.protection_unavailable")
				}
				return workspaceRuntime.VerifyProtectionSnapshot(
					ctx,
					snapshotID,
				)
			}
		}
		registerFieldRoutes(
			event.Router, pb, fieldMigration, backupReceiptKey, options.Logger,
			fieldProtectionVerifier,
			businessGate,
		)
		registerImportRoutes(
			event.Router,
			importvalue.New(fieldchange.NewCatalog(pb)),
		)
		registerQueryRoutes(event.Router, queryPort)
		registerFormulaRoutes(event.Router, formulaCompiler)
		registerJobRoutes(event.Router, jobService)
		registerMutationRoutes(event.Router, mutationKernel, attachmentManager, businessGate)
		registerAttachmentRoutes(event.Router, attachmentManager)
		registerAuditRoutes(event.Router, auditService)
		registerRelationRoutes(event.Router, relationService, businessGate)
		registerRealtimeRoutes(
			event.Router, realtimeHub, schemaapi.New(pb),
		)
		registerMetadataRoutes(event.Router, metadata.New(pb))
		if options.WorkspaceV2 == nil {
			if err := jobService.ResumePending(jobService.Context()); err != nil {
				_ = rawListener.Close()
				return fmt.Errorf("resume durable jobs: %w", err)
			}
		}
		if _, err := auditDrainer.Drain(context.Background(), auditOutbox); err != nil {
			_ = rawListener.Close()
			return fmt.Errorf("drain audit outbox before readiness: %w", err)
		}
		if options.WorkspaceV2 != nil {
			workspaceRuntime, err = workspacev2.Open(
				context.Background(),
				workspacev2.Options{
					App:             pb,
					DataDir:         options.DataDir,
					WorkspaceID:     options.WorkspaceV2.WorkspaceID,
					SessionEpoch:    options.WorkspaceV2.SessionEpoch,
					FenceEpoch:      options.WorkspaceV2.FenceEpoch,
					ClaimID:         options.WorkspaceV2.ClaimID,
					Ledger:          ledger,
					Audit:           auditService,
					RequestShutdown: requestShutdown,
					ReplicaRoot:     options.WorkspaceV2.ReplicaRoot,
				},
			)
			if err != nil {
				_ = rawListener.Close()
				return fmt.Errorf("open workspace v2 runtime: %w", err)
			}
			jobService.SetBusinessWriteGate(
				workspaceRuntime.CoordinateIdempotentBusinessWrite,
			)
			if err := completeRestoreBeforeResumingJobs(
				workspaceRuntime,
				options.OnWorkspaceV2Ready,
				jobService,
			); err != nil {
				_ = rawListener.Close()
				return err
			}
			registerWorkspaceV2Routes(event.Router, workspaceRuntime)
		}
		if err := fieldMigration.ResumePending(jobService.Context()); err != nil {
			_ = rawListener.Close()
			return fmt.Errorf("resume field migrations: %w", err)
		}
		if err := fieldresource.RunPendingAttachmentCleanup(
			jobService.Context(), pb,
		); err != nil {
			_ = rawListener.Close()
			return fmt.Errorf("resume attachment cleanup: %w", err)
		}

		event.Router.POST(shutdownPath, func(request *core.RequestEvent) error {
			if err := request.JSON(http.StatusAccepted, map[string]any{
				"status": "stopping",
			}); err != nil {
				return err
			}
			requestShutdown()
			return nil
		})

		listener, err := launch.AnnounceOnFirstAccept(
			rawListener,
			func(address string) error {
				ready := launch.ReadyRecord(
					address,
					buildinfo.Current(migrations.Hash()),
				)
				if err := launch.WriteReady(options.ReadyWriter, ready); err != nil {
					return err
				}
				options.Logger.Info("sidecar listener ready", "address", address)
				return nil
			},
		)
		if err != nil {
			_ = rawListener.Close()
			return err
		}
		event.Listener = listener

		if err := event.Next(); err != nil {
			_ = rawListener.Close()
			return err
		}
		return nil
	})

	pb.OnTerminate().BindFunc(func(event *core.TerminateEvent) error {
		fieldMigration.Shutdown()
		jobService.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		workspaceCloseErr := workspaceRuntime.Close(ctx)
		workspaceRuntime = nil
		_, drainErr := auditDrainer.Drain(ctx, auditOutbox)
		closeErr := ledger.Close()
		options.Logger.Info("sidecar graceful shutdown", "restart", event.IsRestart)
		terminateErr := errors.Join(
			workspaceCloseErr,
			drainErr,
			closeErr,
			event.Next(),
		)
		if terminateErr != nil {
			options.Logger.Error(
				"sidecar graceful shutdown failed",
				"error",
				terminateErr,
			)
		}
		return terminateErr
	})

	// PocketBase owns the serve command and its graceful shutdown. The custom
	// listener above makes the --http value only a harmless fallback.
	pb.RootCmd.SetArgs([]string{
		"serve",
		"--http=127.0.0.1:0",
		"--origins=http://127.0.0.1",
	})

	return pb, nil
}
