package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	sidecarapp "github.com/vibetable/vibetable/sidecar/internal/app"
	"github.com/vibetable/vibetable/sidecar/internal/backup"
	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
	"github.com/vibetable/vibetable/sidecar/internal/config"
	"github.com/vibetable/vibetable/sidecar/internal/startup"
	"github.com/vibetable/vibetable/sidecar/migrations"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Parse(args, os.Getenv)
	if err != nil {
		logError("invalid sidecar configuration", err)
		return 2
	}

	if cfg.BuildInfoOnly {
		if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Current(migrations.Hash())); err != nil {
			logError("write build information", err)
			return 1
		}
		return 0
	}

	logger := newLogger(cfg.Dev)
	if err := startup.CheckDataDirectory(cfg.DataDir); err != nil {
		logger.Error("sidecar startup preflight", "error", err)
		return 1
	}
	if err := startup.ValidateMigrationManifest(func() error {
		_, loadErr := migrations.LoadManifest()
		return loadErr
	}); err != nil {
		logger.Error("sidecar startup preflight", "error", err)
		return 1
	}
	restored, err := backup.ApplyPendingRestore(cfg.DataDir)
	if err != nil {
		logger.Error(
			"apply staged restore",
			"error",
			startup.Classify("apply staged restore", err),
		)
		return 1
	}
	if restored {
		logger.Info("staged backup restored")
	}
	restoreCommitted := false
	application, err := sidecarapp.New(sidecarapp.Options{
		DataDir:     cfg.DataDir,
		Dev:         cfg.Dev,
		Session:     cfg.Session,
		Logger:      logger,
		ReadyWriter: os.Stdout,
		OnBootstrapReady: func() error {
			if !restored {
				return nil
			}
			if err := backup.CommitPendingRestore(cfg.DataDir); err != nil {
				return err
			}
			restoreCommitted = true
			logger.Info("staged backup restore committed")
			return nil
		},
	})
	if err != nil {
		if restored {
			_, rollbackErr := backup.RollbackPendingRestore(cfg.DataDir)
			err = errors.Join(err, rollbackErr)
		}
		logger.Error(
			"initialize sidecar",
			"error",
			startup.Classify("initialize sidecar", err),
		)
		return 1
	}

	logger.Info("sidecar starting",
		"version", buildinfo.Version,
		"pocketbaseVersion", buildinfo.PocketBaseVersion,
		"migrationHash", migrations.Hash(),
	)
	if err := application.Start(); err != nil {
		if restored && !restoreCommitted {
			_, rollbackErr := backup.RollbackPendingRestore(cfg.DataDir)
			err = errors.Join(err, rollbackErr)
		}
		logger.Error(
			"sidecar stopped with error",
			"error",
			startup.Classify("start sidecar", err),
		)
		return 1
	}
	logger.Info("sidecar stopped")
	return 0
}

func newLogger(dev bool) *slog.Logger {
	level := slog.LevelInfo
	if dev {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func logError(message string, err error) {
	newLogger(false).Error(message, "error", err)
}
