package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	sidecarapp "github.com/vibetable/vibetable/sidecar/internal/app"
	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
	"github.com/vibetable/vibetable/sidecar/internal/config"
	"github.com/vibetable/vibetable/sidecar/internal/startup"
	"github.com/vibetable/vibetable/sidecar/internal/workspacev2"
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
	if cfg.InitializeWorkspaceRepository {
		result, err := workspacev2.InitializeRepository(
			context.Background(),
			cfg.DataDir,
			cfg.WorkspaceV2.WorkspaceID,
			cfg.WorkspaceV2.FenceEpoch,
			cfg.WorkspaceV2.ClaimID,
		)
		if err != nil {
			logError("initialize workspace repository", err)
			return 1
		}
		recoveryKey := ""
		if len(result.RecoveryKey) != 0 {
			recoveryKey = base64.RawURLEncoding.EncodeToString(
				result.RecoveryKey,
			)
		}
		defer clearSecret(result.RecoveryKey)
		response := map[string]any{
			"workspaceId":    result.WorkspaceID,
			"encryptionMode": result.EncryptionMode,
			"initialized":    true,
		}
		if recoveryKey != "" {
			response["recoveryKey"] = recoveryKey
		}
		if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
			rollbackErr := workspacev2.RollbackRepositoryInitialization(
				context.Background(),
				cfg.DataDir,
				cfg.WorkspaceV2.WorkspaceID,
			)
			logError("write repository initialization result", err)
			if rollbackErr != nil {
				logError(
					"rollback repository initialization",
					rollbackErr,
				)
			}
			return 1
		}
		return 0
	}
	if cfg.UnlockWorkspaceRepository {
		var request struct {
			RecoveryKey string `json:"recoveryKey"`
		}
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4097))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil ||
			request.RecoveryKey == "" ||
			decoder.Decode(&struct{}{}) != io.EOF {
			logError(
				"unlock workspace repository",
				errors.New("repository.recovery_input_invalid"),
			)
			return 2
		}
		key, err := base64.RawURLEncoding.DecodeString(
			request.RecoveryKey,
		)
		if err != nil || len(key) != 32 {
			clearSecret(key)
			logError(
				"unlock workspace repository",
				errors.New("repository.recovery_key_invalid"),
			)
			return 2
		}
		err = workspacev2.RestoreProtectedRepository(
			context.Background(),
			cfg.DataDir,
			cfg.WorkspaceV2.WorkspaceID,
			key,
		)
		clearSecret(key)
		if err != nil {
			logError("unlock workspace repository", err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"workspaceId": cfg.WorkspaceV2.WorkspaceID,
			"unlocked":    true,
		}); err != nil {
			logError("write repository unlock result", err)
			return 1
		}
		return 0
	}
	if cfg.RotateWorkspaceRepository {
		result, err := workspacev2.RotateProtectedRepository(
			context.Background(),
			cfg.DataDir,
			cfg.WorkspaceV2.WorkspaceID,
		)
		if err != nil {
			logError("rotate workspace repository", err)
			return 1
		}
		recoveryKey := base64.RawURLEncoding.EncodeToString(
			result.RecoveryKey,
		)
		defer clearSecret(result.RecoveryKey)
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"workspaceId": result.WorkspaceID,
			"rotated":     true,
			"recoveryKey": recoveryKey,
		}); err != nil {
			logError("write repository rotation result", err)
			return 1
		}
		return 0
	}
	if cfg.InitializeWorkspaceReplica ||
		cfg.RecoverWorkspaceReplica ||
		cfg.VerifyWorkspaceReplica {
		options := workspacev2.ReplicaOneShotOptions{
			DataDir:      cfg.DataDir,
			ActivityRoot: cfg.ActivityRoot,
			ReplicaRoot:  cfg.ReplicaRoot,
			WorkspaceID:  cfg.WorkspaceV2.WorkspaceID,
			SessionEpoch: cfg.WorkspaceV2.SessionEpoch,
			FenceEpoch:   cfg.WorkspaceV2.FenceEpoch,
			ClaimID:      cfg.WorkspaceV2.ClaimID,
		}
		var receipt workspacev2.ReplicaOneShotReceipt
		switch {
		case cfg.InitializeWorkspaceReplica:
			receipt, err = workspacev2.InitializeWorkspaceReplica(
				context.Background(),
				options,
			)
		case cfg.RecoverWorkspaceReplica:
			receipt, err = workspacev2.RecoverWorkspaceReplica(
				context.Background(),
				options,
			)
		default:
			receipt, err = workspacev2.VerifyWorkspaceReplica(
				context.Background(),
				options,
			)
		}
		if err != nil {
			logError("workspace replica one-shot", err)
			return 1
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			logError("encode workspace replica receipt", err)
			return 1
		}
		raw = append(raw, '\n')
		if _, err := os.Stdout.Write(raw); err != nil {
			logError("write workspace replica receipt", err)
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
	restored := false
	restoreCommitted := false
	if cfg.WorkspaceV2 != nil {
		restored, err = workspacev2.ApplyPendingSnapshotRestore(
			context.Background(),
			cfg.DataDir,
			cfg.WorkspaceV2.WorkspaceID,
		)
	}
	if err != nil {
		logger.Error(
			"apply staged restore",
			"error",
			startup.Classify("apply staged restore", err),
		)
		return 1
	}
	if restored {
		logger.Info("staged restore installed")
	}
	appOptions := sidecarapp.Options{
		DataDir:     cfg.DataDir,
		Dev:         cfg.Dev,
		Session:     cfg.Session,
		Logger:      logger,
		ReadyWriter: os.Stdout,
		OnBootstrapReady: func() error {
			return nil
		},
		OnWorkspaceV2Ready: func(runtime *workspacev2.Runtime) error {
			if !restored {
				return nil
			}
			if err := runtime.CompletePendingSnapshotRestore(
				context.Background(),
			); err != nil {
				return err
			}
			restoreCommitted = true
			logger.Info("staged snapshot restore committed")
			return nil
		},
	}
	if cfg.WorkspaceV2 != nil {
		appOptions.WorkspaceV2 = &sidecarapp.WorkspaceV2Options{
			WorkspaceID:  cfg.WorkspaceV2.WorkspaceID,
			SessionEpoch: cfg.WorkspaceV2.SessionEpoch,
			FenceEpoch:   cfg.WorkspaceV2.FenceEpoch,
			ClaimID:      cfg.WorkspaceV2.ClaimID,
			ReplicaRoot:  cfg.ReplicaRoot,
		}
	}
	application, err := sidecarapp.New(appOptions)
	if err != nil {
		if restored && cfg.WorkspaceV2 != nil {
			_, rollbackErr := workspacev2.RollbackPendingSnapshotRestore(
				context.Background(),
				cfg.DataDir,
				cfg.WorkspaceV2.WorkspaceID,
			)
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
		if restored && !restoreCommitted && cfg.WorkspaceV2 != nil {
			_, rollbackErr := workspacev2.RollbackPendingSnapshotRestore(
				context.Background(),
				cfg.DataDir,
				cfg.WorkspaceV2.WorkspaceID,
			)
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

func clearSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
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
