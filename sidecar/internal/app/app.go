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
	"runtime"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/backup"
	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
	"github.com/vibetable/vibetable/sidecar/internal/computed"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/health"
	"github.com/vibetable/vibetable/sidecar/internal/jobs"
	"github.com/vibetable/vibetable/sidecar/internal/launch"
	"github.com/vibetable/vibetable/sidecar/internal/lookup"
	"github.com/vibetable/vibetable/sidecar/internal/metadata"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/realtime"
	"github.com/vibetable/vibetable/sidecar/internal/relation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaapi"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

const (
	healthPath    = "/api/vibetable/v1/health"
	buildInfoPath = "/api/vibetable/v1/build-info"
	shutdownPath  = "/api/vibetable/v1/shutdown"
)

type Options struct {
	DataDir          string
	Dev              bool
	Session          auth.Secret
	Logger           *slog.Logger
	ReadyWriter      io.Writer
	OnBootstrapReady func() error
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
	backupService := backup.New(pb, attachmentManager)
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
	auditService, err := audit.New(
		pb,
		mutationKernel,
		mutation.MetadataSchemaSource{},
		restoreKey,
		audit.WithAttachmentHistory(attachmentManager),
	)
	if err != nil {
		jobService.Shutdown()
		return nil, err
	}
	relationService := relation.New(pb, queryPort, mutationKernel)
	startedAt := time.Now().UTC()
	pb.OnServe().BindFunc(func(event *core.ServeEvent) error {
		// VibeTable owns the local data lifecycle. Never expose PocketBase's
		// first-run superuser installer to the desktop user.
		event.InstallerFunc = nil

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
		registerAdminRoutes(event)
		registerSchemaRoutes(event.Router, schemaapi.New(pb), jobService)
		registerQueryRoutes(event.Router, queryPort)
		registerFormulaRoutes(event.Router, formulaCompiler)
		registerJobRoutes(event.Router, jobService)
		registerMutationRoutes(event.Router, mutationKernel, attachmentManager)
		registerAttachmentRoutes(event.Router, attachmentManager)
		registerAuditRoutes(event.Router, auditService)
		registerRelationRoutes(event.Router, relationService)
		registerRealtimeRoutes(
			event.Router, realtimeHub, schemaapi.New(pb),
		)
		if runtime.GOOS == "windows" {
			// PocketBase's app.Restart uses execve and explicitly returns an
			// error on Windows. The desktop supervisor already owns process
			// restart, so a restore stages its marker and then exits cleanly;
			// the next launch applies and commits the two-phase restore before
			// readiness is announced.
			backupService.WithRestart(func() error {
				go func() {
					// Let the 202 response leave the socket before initiating
					// shutdown. The marker is already durable at this point.
					time.Sleep(50 * time.Millisecond)
					ctx, cancel := context.WithTimeout(
						context.Background(), 10*time.Second,
					)
					defer cancel()
					if err := event.Server.Shutdown(ctx); err != nil {
						options.Logger.Error(
							"backup restore shutdown failed", "error", err,
						)
					}
				}()
				return nil
			})
		}
		registerBackupRoutes(event.Router, backupService)
		registerMetadataRoutes(event.Router, metadata.New(pb))
		if err := jobService.ResumePending(jobService.Context()); err != nil {
			_ = rawListener.Close()
			return fmt.Errorf("resume durable jobs: %w", err)
		}

		var shutdownOnce sync.Once
		event.Router.POST(shutdownPath, func(request *core.RequestEvent) error {
			if err := request.JSON(http.StatusAccepted, map[string]any{
				"status": "stopping",
			}); err != nil {
				return err
			}
			shutdownOnce.Do(func() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := event.Server.Shutdown(ctx); err != nil {
						options.Logger.Error("graceful shutdown failed", "error", err)
					}
				}()
			})
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
		jobService.Shutdown()
		options.Logger.Info("sidecar graceful shutdown", "restart", event.IsRestart)
		return event.Next()
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
