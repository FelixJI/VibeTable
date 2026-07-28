// Package workspacemigrator upgrades workspace formats through a durable,
// preview/apply state machine. It never mutates a newer-than-supported
// workspace and only publishes a fully verified staging copy.
package workspacemigrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNewerFormat   = errors.New("workspace.format_newer")
	ErrPlanNotFound  = errors.New("workspace.upgrade_plan_not_found")
	ErrPlanStale     = errors.New("workspace.upgrade_plan_stale")
	ErrConfirmation  = errors.New("workspace.upgrade_confirmation_required")
	ErrTargetInvalid = errors.New("workspace.upgrade_target_invalid")
	ErrRecovery      = errors.New("workspace.upgrade_recovery_required")
)

type Manifest struct {
	WorkspaceID       string `json:"workspaceId"`
	FormatVersion     int    `json:"formatVersion"`
	WriterVersion     string `json:"writerVersion"`
	MinimumAppVersion string `json:"minimumAppVersion"`
	Fingerprint       string `json:"fingerprint"`
}

type Plan struct {
	PlanID            string `json:"planId"`
	WorkspaceID       string `json:"workspaceId"`
	Root              string `json:"-"`
	SourceFormat      int    `json:"sourceFormat"`
	TargetFormat      int    `json:"targetFormat"`
	SourceFingerprint string `json:"sourceFingerprint"`
	ReadOnly          bool   `json:"readOnly"`
	Reason            string `json:"reason,omitempty"`
}

type Operation struct {
	OperationID string `json:"operationId"`
	WorkspaceID string `json:"workspaceId"`
	State       string `json:"state"`
}

type Backend interface {
	Inspect(ctx context.Context, root string) (Manifest, error)
	Protect(ctx context.Context, root string) error
	Stage(ctx context.Context, root string) (stagingRoot string, err error)
	Upgrade(ctx context.Context, stagingRoot string, targetFormat int) error
	Verify(ctx context.Context, root string, targetFormat int) error
	Publish(ctx context.Context, root string, stagingRoot string) error
	Discard(ctx context.Context, stagingRoot string) error
}

type Migrator struct {
	db             *sql.DB
	backend        Backend
	currentFormat  int
	currentVersion string
	now            func() time.Time
}

func Open(
	statePath string,
	backend Backend,
	currentFormat int,
	currentVersion string,
) (*Migrator, error) {
	if strings.TrimSpace(statePath) == "" || backend == nil ||
		currentFormat <= 0 || strings.TrimSpace(currentVersion) == "" {
		return nil, errors.New("workspace.migrator_dependencies_required")
	}
	db, err := sql.Open("sqlite", statePath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS upgrade_plans (
			plan_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			root TEXT NOT NULL,
			source_format INTEGER NOT NULL,
			target_format INTEGER NOT NULL,
			source_fingerprint TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT NOT NULL,
			staging_root TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Migrator{
		db: db, backend: backend, currentFormat: currentFormat,
		currentVersion: currentVersion, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (migrator *Migrator) Close() error { return migrator.db.Close() }

func (migrator *Migrator) Preview(
	ctx context.Context,
	root string,
	targetFormat int,
) (Plan, error) {
	manifest, err := migrator.backend.Inspect(ctx, root)
	if err != nil {
		return Plan{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return Plan{}, err
	}
	if manifest.FormatVersion > migrator.currentFormat {
		return Plan{
			WorkspaceID:  manifest.WorkspaceID,
			SourceFormat: manifest.FormatVersion,
			TargetFormat: targetFormat,
			ReadOnly:     true,
			Reason:       ErrNewerFormat.Error(),
		}, ErrNewerFormat
	}
	if targetFormat <= manifest.FormatVersion || targetFormat > migrator.currentFormat {
		return Plan{}, ErrTargetInvalid
	}
	plan := Plan{
		PlanID:            planID(manifest, targetFormat),
		WorkspaceID:       manifest.WorkspaceID,
		Root:              root,
		SourceFormat:      manifest.FormatVersion,
		TargetFormat:      targetFormat,
		SourceFingerprint: manifest.Fingerprint,
	}
	now := migrator.now().Format(time.RFC3339Nano)
	_, err = migrator.db.ExecContext(ctx, `
		INSERT INTO upgrade_plans (
			plan_id, workspace_id, root, source_format, target_format,
			source_fingerprint, status, stage, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'planned', 'planned', ?, ?)
		ON CONFLICT(plan_id) DO UPDATE SET updated_at = excluded.updated_at
		WHERE upgrade_plans.status = 'planned'
	`, plan.PlanID, plan.WorkspaceID, root, plan.SourceFormat, plan.TargetFormat,
		plan.SourceFingerprint, now, now)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (migrator *Migrator) Apply(
	ctx context.Context,
	planID string,
	confirmation string,
) (Operation, error) {
	if confirmation != "upgrade-workspace" {
		return Operation{}, ErrConfirmation
	}
	record, err := migrator.loadPlan(ctx, planID)
	if err != nil {
		return Operation{}, err
	}
	if record.Status != "planned" {
		return Operation{}, ErrRecovery
	}
	current, err := migrator.backend.Inspect(ctx, record.Root)
	if err != nil {
		return Operation{}, err
	}
	if current.WorkspaceID != record.WorkspaceID ||
		current.FormatVersion != record.SourceFormat ||
		current.Fingerprint != record.SourceFingerprint {
		return Operation{}, ErrPlanStale
	}
	if err := migrator.setState(ctx, planID, "running", "protecting", ""); err != nil {
		return Operation{}, err
	}
	if err := migrator.backend.Protect(ctx, record.Root); err != nil {
		_ = migrator.setState(context.Background(), planID, "planned", "planned", "")
		return Operation{}, err
	}
	if err := migrator.setState(ctx, planID, "running", "protected", ""); err != nil {
		return Operation{}, err
	}
	stagingRoot, err := migrator.backend.Stage(ctx, record.Root)
	if err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, "", err)
	}
	record.StagingRoot = stagingRoot
	if err := migrator.setState(ctx, planID, "running", "staged", stagingRoot); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	if err := migrator.backend.Upgrade(ctx, stagingRoot, record.TargetFormat); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	if err := migrator.setState(ctx, planID, "running", "upgraded", stagingRoot); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	if err := migrator.backend.Verify(ctx, stagingRoot, record.TargetFormat); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	if err := migrator.setState(ctx, planID, "running", "verified", stagingRoot); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	// Persist intent before the atomic/idempotent publication. Recovery can
	// distinguish a completed publish from a safe-to-discard staging copy.
	if err := migrator.setState(ctx, planID, "running", "publishing", stagingRoot); err != nil {
		return Operation{}, migrator.failBeforePublish(ctx, record, stagingRoot, err)
	}
	if err := migrator.backend.Publish(ctx, record.Root, stagingRoot); err != nil {
		return Operation{}, errors.Join(ErrRecovery, err)
	}
	if err := migrator.setState(ctx, planID, "running", "published", stagingRoot); err != nil {
		return Operation{}, errors.Join(ErrRecovery, err)
	}
	if err := migrator.backend.Verify(ctx, record.Root, record.TargetFormat); err != nil {
		return Operation{}, errors.Join(ErrRecovery, err)
	}
	if err := migrator.setState(ctx, planID, "complete", "committed", stagingRoot); err != nil {
		return Operation{}, errors.Join(ErrRecovery, err)
	}
	_ = migrator.backend.Discard(ctx, stagingRoot)
	return operation(record), nil
}

func (migrator *Migrator) Recover(ctx context.Context) error {
	rows, err := migrator.db.QueryContext(ctx, `
		SELECT plan_id, workspace_id, root, source_format, target_format,
			source_fingerprint, status, stage, staging_root
		FROM upgrade_plans WHERE status = 'running' ORDER BY created_at
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var records []planRecord
	for rows.Next() {
		var record planRecord
		if err := rows.Scan(
			&record.PlanID, &record.WorkspaceID, &record.Root,
			&record.SourceFormat, &record.TargetFormat, &record.SourceFingerprint,
			&record.Status, &record.Stage, &record.StagingRoot,
		); err != nil {
			return err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, record := range records {
		switch record.Stage {
		case "publishing", "published":
			manifest, inspectErr := migrator.backend.Inspect(ctx, record.Root)
			if inspectErr == nil &&
				manifest.WorkspaceID == record.WorkspaceID &&
				manifest.FormatVersion == record.TargetFormat &&
				migrator.backend.Verify(ctx, record.Root, record.TargetFormat) == nil {
				if err := migrator.setState(
					ctx, record.PlanID, "complete", "committed", record.StagingRoot,
				); err != nil {
					return err
				}
				_ = migrator.backend.Discard(ctx, record.StagingRoot)
				continue
			}
			return ErrRecovery
		default:
			if record.StagingRoot != "" {
				if err := migrator.backend.Discard(ctx, record.StagingRoot); err != nil {
					return errors.Join(ErrRecovery, err)
				}
			}
			if err := migrator.setState(
				ctx, record.PlanID, "planned", "planned", "",
			); err != nil {
				return err
			}
		}
	}
	return nil
}

type planRecord struct {
	PlanID            string
	WorkspaceID       string
	Root              string
	SourceFormat      int
	TargetFormat      int
	SourceFingerprint string
	Status            string
	Stage             string
	StagingRoot       string
}

func (migrator *Migrator) loadPlan(ctx context.Context, id string) (planRecord, error) {
	var record planRecord
	err := migrator.db.QueryRowContext(ctx, `
		SELECT plan_id, workspace_id, root, source_format, target_format,
			source_fingerprint, status, stage, staging_root
		FROM upgrade_plans WHERE plan_id = ?
	`, id).Scan(
		&record.PlanID, &record.WorkspaceID, &record.Root,
		&record.SourceFormat, &record.TargetFormat, &record.SourceFingerprint,
		&record.Status, &record.Stage, &record.StagingRoot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return planRecord{}, ErrPlanNotFound
	}
	return record, err
}

func (migrator *Migrator) setState(
	ctx context.Context,
	id string,
	status string,
	stage string,
	stagingRoot string,
) error {
	result, err := migrator.db.ExecContext(ctx, `
		UPDATE upgrade_plans
		SET status = ?, stage = ?, staging_root = ?, updated_at = ?
		WHERE plan_id = ?
	`, status, stage, stagingRoot, migrator.now().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrPlanNotFound
	}
	return nil
}

func (migrator *Migrator) failBeforePublish(
	ctx context.Context,
	record planRecord,
	stagingRoot string,
	cause error,
) error {
	var discardErr error
	if stagingRoot != "" {
		discardErr = migrator.backend.Discard(ctx, stagingRoot)
	}
	stateErr := migrator.setState(
		context.Background(), record.PlanID, "planned", "planned", "",
	)
	return errors.Join(cause, discardErr, stateErr)
}

func operation(record planRecord) Operation {
	return Operation{
		OperationID: "upgrade:" + record.PlanID,
		WorkspaceID: record.WorkspaceID,
		State:       "committed",
	}
}

func validateManifest(manifest Manifest) error {
	if strings.TrimSpace(manifest.WorkspaceID) == "" ||
		manifest.FormatVersion <= 0 ||
		strings.TrimSpace(manifest.WriterVersion) == "" ||
		strings.TrimSpace(manifest.MinimumAppVersion) == "" ||
		strings.TrimSpace(manifest.Fingerprint) == "" {
		return errors.New("workspace.manifest_invalid")
	}
	return nil
}

func planID(manifest Manifest, target int) string {
	raw, _ := json.Marshal([]any{
		manifest.WorkspaceID, manifest.FormatVersion, manifest.Fingerprint, target,
	})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("upgrade_%s", hex.EncodeToString(sum[:16]))
}
