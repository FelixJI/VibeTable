package conflict

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
	_ "modernc.org/sqlite"
)

var (
	ErrConflictNotFound     = errors.New("conflict.not_found")
	ErrConflictState        = errors.New("conflict.state_invalid")
	ErrDependencyIncomplete = errors.New("conflict.dependencies_incomplete")
	ErrPlanNotFound         = errors.New("conflict.plan_not_found")
	ErrApplyUnproven        = errors.New("conflict.apply_unproven")
	ErrResolutionInvalid    = errors.New("conflict.resolution_invalid")
)

type State string

const (
	StatePending  State = "pending"
	StateApplying State = "applying"
	StateApplied  State = "applied"
)

type DependencyGraph struct {
	// Complete is set only by a production relationship/automation/plugin
	// scanner after all three candidate snapshots were inspected.
	Complete bool                `json:"complete"`
	Edges    map[string][]string `json:"edges"`
}

type Set struct {
	ConflictID     string          `json:"conflictId"`
	WorkspaceID    string          `json:"workspaceId"`
	State          State           `json:"state"`
	Revision       uint64          `json:"revision"`
	Base           Candidate       `json:"base"`
	Local          Candidate       `json:"local"`
	Replica        Candidate       `json:"replica"`
	Dependencies   DependencyGraph `json:"dependencies"`
	RootPinIDs     []string        `json:"rootPinIds"`
	ReplanRequired bool            `json:"replanRequired,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type Choice struct {
	ItemID string   `json:"itemId"`
	Kind   ItemKind `json:"kind"`
	Side   Side     `json:"side"`

	// DocumentID keeps source-level compatibility for internal callers while
	// the wire contract uses the itemId+kind discriminated choice above.
	DocumentID string `json:"-"`
}

type Preview struct {
	PlanID      string   `json:"planId"`
	Diagnostics []string `json:"diagnostics"`
	Valid       bool     `json:"valid"`
}

type ApplyStage struct {
	StageID     string `json:"stageId"`
	OperationID string `json:"operationId"`
	PlanID      string `json:"planId"`
}

type ApplyReceipt struct {
	OperationID         string   `json:"operationId"`
	State               string   `json:"state"`
	RecoverySnapshotIDs []string `json:"recoverySnapshotIds"`
	AuthorityRevision   uint64   `json:"authorityRevision"`
}

// StagedAppender makes the visible head publication, audit/outbox append, and
// exact protocol operation receipt one authoritative transaction. Stage may
// create immutable objects, but must not expose a new effective revision.
type StagedAppender interface {
	Stage(
		context.Context,
		string,
		string,
		Plan,
		[]ResolvedChange,
		Candidate,
	) (ApplyStage, error)
	Commit(context.Context, ApplyStage) (ApplyReceipt, error)
	Probe(context.Context, string) (ApplyReceipt, bool, error)
}

type Engine struct {
	db  *sql.DB
	now func() time.Time
}

func OpenEngine(path string) (*Engine, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("conflict.store_path_required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=FULL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS conflict_sets (
			conflict_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN (
				'pending','applying','applied'
			)),
			revision INTEGER NOT NULL,
			set_json BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS conflict_sets_workspace
		ON conflict_sets(workspace_id, state, conflict_id);
		CREATE TABLE IF NOT EXISTS conflict_plans (
			plan_id TEXT PRIMARY KEY,
			conflict_id TEXT NOT NULL,
			set_revision INTEGER NOT NULL,
			resolver_plan_json BLOB NOT NULL,
			resolution_json BLOB NOT NULL,
			stage_json BLOB,
			receipt_json BLOB,
			state TEXT NOT NULL CHECK(state IN (
				'prepared','applying','applied'
			))
		);
		CREATE UNIQUE INDEX IF NOT EXISTS conflict_plan_active
		ON conflict_plans(conflict_id)
		WHERE state IN ('prepared','applying');
		CREATE TABLE IF NOT EXISTS conflict_operation_receipts (
			operation_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			method TEXT NOT NULL,
			scope TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			result_json BLOB NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Engine{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (engine *Engine) Add(
	ctx context.Context,
	set Set,
) error {
	if engine == nil || engine.db == nil ||
		!validSet(set) {
		return ErrConflictState
	}
	if set.ConflictID == "" {
		set.ConflictID = uuid.NewString()
	}
	if set.Revision == 0 {
		set.Revision = 1
	}
	if set.State == "" {
		set.State = StatePending
	}
	if set.CreatedAt.IsZero() {
		set.CreatedAt = engine.now().UTC()
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	_, err = engine.db.ExecContext(ctx, `
		INSERT INTO conflict_sets(
			conflict_id, workspace_id, state, revision, set_json
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(conflict_id) DO NOTHING`,
		set.ConflictID, set.WorkspaceID, set.State, set.Revision, raw,
	)
	if err != nil {
		return err
	}
	var existing []byte
	if err := engine.db.QueryRowContext(ctx, `
		SELECT set_json FROM conflict_sets WHERE conflict_id = ?`,
		set.ConflictID,
	).Scan(&existing); err != nil {
		return err
	}
	if string(existing) != string(raw) {
		return ErrConflictState
	}
	return nil
}

func (engine *Engine) List(
	ctx context.Context,
	workspaceID string,
	cursor *string,
	limit int,
) ([]Set, *string, error) {
	if limit < 1 || limit > 200 || strings.TrimSpace(workspaceID) == "" {
		return nil, nil, ErrConflictState
	}
	after := ""
	if cursor != nil {
		after = *cursor
	}
	rows, err := engine.db.QueryContext(ctx, `
		SELECT set_json FROM conflict_sets
		WHERE workspace_id = ? AND conflict_id > ?
		ORDER BY conflict_id LIMIT ?`,
		workspaceID, after, limit+1,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []Set
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		var set Set
		if err := decodeStrict(raw, &set); err != nil ||
			!validSet(set) {
			return nil, nil, errors.Join(ErrConflictState, err)
		}
		result = append(result, set)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *string
	if len(result) > limit {
		value := result[limit-1].ConflictID
		next = &value
		result = result[:limit]
	}
	return result, next, nil
}

func (engine *Engine) Inspect(
	ctx context.Context,
	conflictID string,
) (Set, error) {
	var raw []byte
	err := engine.db.QueryRowContext(ctx, `
		SELECT set_json FROM conflict_sets WHERE conflict_id = ?`,
		conflictID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Set{}, ErrConflictNotFound
	}
	if err != nil {
		return Set{}, err
	}
	var set Set
	if err := decodeStrict(raw, &set); err != nil ||
		!validSet(set) {
		return Set{}, errors.Join(ErrConflictState, err)
	}
	return set, nil
}

func (engine *Engine) Preview(
	ctx context.Context,
	conflictID string,
	choices []Choice,
) (Preview, error) {
	return engine.preview(ctx, conflictID, choices, nil)
}

func (engine *Engine) PreviewWithReceipt(
	ctx context.Context,
	conflictID string,
	choices []Choice,
	build func(Preview) (protocolv2.OperationReceipt, error),
) (Preview, error) {
	if build == nil {
		return Preview{}, errors.New(
			"conflict.receipt_builder_required",
		)
	}
	return engine.preview(ctx, conflictID, choices, build)
}

func (engine *Engine) preview(
	ctx context.Context,
	conflictID string,
	choices []Choice,
	build func(Preview) (protocolv2.OperationReceipt, error),
) (Preview, error) {
	set, err := engine.Inspect(ctx, conflictID)
	if err != nil {
		return Preview{}, err
	}
	if set.State != StatePending {
		return Preview{}, ErrConflictState
	}
	resolverPlan := BuildPlan(set.Base, set.Local, set.Replica)
	resolution, diagnostics := validateChoices(
		resolverPlan, set.Dependencies, choices,
	)
	if len(diagnostics) > 0 {
		preview := Preview{
			PlanID:      uuid.NewString(),
			Diagnostics: diagnostics,
			Valid:       false,
		}
		if build == nil {
			return preview, nil
		}
		receipt, err := build(preview)
		if err != nil {
			return Preview{}, err
		}
		if err := validatePreviewReceipt(receipt, set.WorkspaceID); err != nil {
			return Preview{}, err
		}
		if err := engine.storeOperationReceipt(ctx, receipt); err != nil {
			return Preview{}, err
		}
		return preview, nil
	}
	planID := uuid.NewString()
	planRaw, err := json.Marshal(resolverPlan)
	if err != nil {
		return Preview{}, err
	}
	resolutionRaw, err := json.Marshal(resolution)
	if err != nil {
		return Preview{}, err
	}
	preview := Preview{
		PlanID:      planID,
		Diagnostics: []string{},
		Valid:       true,
	}
	var receipt protocolv2.OperationReceipt
	if build != nil {
		receipt, err = build(preview)
		if err != nil {
			return Preview{}, err
		}
		if err := validatePreviewReceipt(receipt, set.WorkspaceID); err != nil {
			return Preview{}, err
		}
	}
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return Preview{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO conflict_plans(
			plan_id, conflict_id, set_revision,
			resolver_plan_json, resolution_json, state
		) VALUES (?, ?, ?, ?, ?, 'prepared')`,
		planID, set.ConflictID, set.Revision, planRaw, resolutionRaw,
	)
	if err != nil {
		return Preview{}, err
	}
	if build != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO conflict_operation_receipts(
				operation_id, workspace_id, method, scope,
				request_hash, result_json
			) VALUES (?, ?, ?, ?, ?, ?)`,
			receipt.OperationID,
			receipt.WorkspaceID,
			receipt.Method,
			string(receipt.Scope),
			receipt.RequestHash,
			[]byte(receipt.Result),
		); err != nil {
			return Preview{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func validatePreviewReceipt(
	receipt protocolv2.OperationReceipt,
	workspaceID string,
) error {
	if receipt.OperationID == "" ||
		receipt.WorkspaceID != workspaceID ||
		receipt.Method != "conflict.preview" ||
		receipt.Scope != protocolv2.WorkspaceScope ||
		len(receipt.Result) == 0 {
		return errors.New("conflict.operation_receipt_invalid")
	}
	return nil
}

func (engine *Engine) storeOperationReceipt(
	ctx context.Context,
	receipt protocolv2.OperationReceipt,
) error {
	_, err := engine.db.ExecContext(ctx, `
		INSERT INTO conflict_operation_receipts(
			operation_id, workspace_id, method, scope,
			request_hash, result_json
		) VALUES (?, ?, ?, ?, ?, ?)`,
		receipt.OperationID,
		receipt.WorkspaceID,
		receipt.Method,
		string(receipt.Scope),
		receipt.RequestHash,
		[]byte(receipt.Result),
	)
	return err
}

func (engine *Engine) LoadOperationReceipt(
	ctx context.Context,
	workspaceID string,
	operationID string,
) (protocolv2.OperationReceipt, bool, error) {
	if engine == nil || engine.db == nil {
		return protocolv2.OperationReceipt{}, false, nil
	}
	var receipt protocolv2.OperationReceipt
	var scope string
	var result []byte
	err := engine.db.QueryRowContext(ctx, `
		SELECT operation_id, workspace_id, method, scope,
		       request_hash, result_json
		FROM conflict_operation_receipts
		WHERE workspace_id = ? AND operation_id = ?`,
		workspaceID, operationID,
	).Scan(
		&receipt.OperationID,
		&receipt.WorkspaceID,
		&receipt.Method,
		&scope,
		&receipt.RequestHash,
		&result,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocolv2.OperationReceipt{}, false, nil
	}
	if err != nil {
		return protocolv2.OperationReceipt{}, false, err
	}
	receipt.Scope = protocolv2.ScopeKind(scope)
	receipt.Result = append(json.RawMessage(nil), result...)
	return receipt, true, nil
}

func (engine *Engine) Apply(
	ctx context.Context,
	planID string,
	operationID string,
	appender StagedAppender,
) (ApplyReceipt, error) {
	if appender == nil ||
		strings.TrimSpace(operationID) == "" {
		return ApplyReceipt{}, ErrConflictState
	}
	plan, set, resolution, state, stage, receipt, err :=
		engine.loadPlan(ctx, planID)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if state == "applied" {
		if receipt.OperationID != operationID {
			return ApplyReceipt{}, ErrStalePlan
		}
		return receipt, nil
	}
	if state == "applying" {
		if stage.OperationID != operationID {
			return ApplyReceipt{}, ErrStalePlan
		}
		return engine.finishApply(ctx, planID, set, stage, appender)
	}
	if set.State != StatePending {
		return ApplyReceipt{}, ErrStalePlan
	}
	changes, err := ResolveChanges(
		plan, set.Local, set.Replica, resolution,
	)
	if err != nil {
		return ApplyReceipt{}, err
	}
	stage, err = appender.Stage(
		ctx, operationID, planID, plan, changes, set.Replica,
	)
	if err != nil {
		switch {
		case errors.Is(err, ErrResolutionInvalid):
			if discardErr := engine.discardPreparedPlan(
				context.WithoutCancel(ctx), planID, set, false,
			); discardErr != nil {
				return ApplyReceipt{}, errors.Join(err, discardErr)
			}
		case errors.Is(err, ErrStalePlan):
			if discardErr := engine.discardPreparedPlan(
				context.WithoutCancel(ctx), planID, set, true,
			); discardErr != nil {
				return ApplyReceipt{}, errors.Join(err, discardErr)
			}
		}
		return ApplyReceipt{}, err
	}
	if stage.OperationID != operationID ||
		stage.PlanID != planID ||
		strings.TrimSpace(stage.StageID) == "" {
		return ApplyReceipt{}, ErrApplyUnproven
	}
	stageRaw, err := json.Marshal(stage)
	if err != nil {
		return ApplyReceipt{}, err
	}
	set.State = StateApplying
	set.Revision++
	setRaw, err := json.Marshal(set)
	if err != nil {
		return ApplyReceipt{}, err
	}
	tx, err := engine.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return ApplyReceipt{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(context.WithoutCancel(ctx), `
		UPDATE conflict_plans
		SET state = 'applying', stage_json = ?
		WHERE plan_id = ? AND state = 'prepared'`,
		stageRaw, planID,
	)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if err := requireOne(result); err != nil {
		return ApplyReceipt{}, err
	}
	result, err = tx.ExecContext(context.WithoutCancel(ctx), `
		UPDATE conflict_sets
		SET state = 'applying', revision = ?, set_json = ?
		WHERE conflict_id = ? AND state = 'pending'
		      AND revision = ?`,
		set.Revision, setRaw, set.ConflictID, set.Revision-1,
	)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if err := requireOne(result); err != nil {
		return ApplyReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyReceipt{}, err
	}
	return engine.finishApply(ctx, planID, set, stage, appender)
}

func (engine *Engine) discardPreparedPlan(
	ctx context.Context,
	planID string,
	set Set,
	replanRequired bool,
) error {
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM conflict_plans
		WHERE plan_id = ? AND state = 'prepared'`,
		planID,
	)
	if err != nil {
		return err
	}
	if err := requireOne(result); err != nil {
		return err
	}
	if replanRequired {
		set.ReplanRequired = true
		set.Revision++
		raw, err := json.Marshal(set)
		if err != nil {
			return err
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE conflict_sets
			SET revision = ?, set_json = ?
			WHERE conflict_id = ? AND state = 'pending'
			  AND revision = ?`,
			set.Revision, raw, set.ConflictID, set.Revision-1,
		)
		if err != nil {
			return err
		}
		if err := requireOne(result); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (engine *Engine) Recover(
	ctx context.Context,
	appender StagedAppender,
) error {
	rows, err := engine.db.QueryContext(ctx, `
		SELECT plan_id FROM conflict_plans
		WHERE state = 'applying' ORDER BY plan_id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		_, set, _, _, stage, _, err := engine.loadPlan(ctx, id)
		if err != nil {
			return err
		}
		if _, err := engine.finishApply(
			ctx, id, set, stage, appender,
		); err != nil {
			return err
		}
	}
	return nil
}

// RecoverOperation resumes exactly one applying operation. It prevents a
// prepared coordinator revision from being accidentally consumed by another
// older applying plan during startup recovery.
func (engine *Engine) RecoverOperation(
	ctx context.Context,
	operationID string,
	appender StagedAppender,
) error {
	if strings.TrimSpace(operationID) == "" || appender == nil {
		return ErrConflictState
	}
	rows, err := engine.db.QueryContext(ctx, `
		SELECT plan_id FROM conflict_plans
		WHERE state = 'applying' ORDER BY plan_id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		_, set, _, _, stage, _, err := engine.loadPlan(ctx, id)
		if err != nil {
			return err
		}
		if stage.OperationID != operationID {
			continue
		}
		_, err = engine.finishApply(
			ctx, id, set, stage, appender,
		)
		return err
	}
	return ErrConflictState
}

func (engine *Engine) finishApply(
	ctx context.Context,
	planID string,
	set Set,
	stage ApplyStage,
	appender StagedAppender,
) (ApplyReceipt, error) {
	receipt, found, err := appender.Probe(ctx, stage.OperationID)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if !found {
		receipt, err = appender.Commit(ctx, stage)
		if err != nil {
			if errors.Is(err, ErrStalePlan) {
				if discardErr := engine.discardStaleApply(
					context.WithoutCancel(ctx), planID, set,
				); discardErr != nil {
					return ApplyReceipt{}, errors.Join(err, discardErr)
				}
			}
			return ApplyReceipt{}, err
		}
	}
	if receipt.OperationID != stage.OperationID ||
		receipt.State != "applied" ||
		receipt.AuthorityRevision == 0 {
		return ApplyReceipt{}, ErrApplyUnproven
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return ApplyReceipt{}, err
	}
	set.State = StateApplied
	set.Revision++
	setRaw, err := json.Marshal(set)
	if err != nil {
		return ApplyReceipt{}, err
	}
	tx, err := engine.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return ApplyReceipt{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(context.WithoutCancel(ctx), `
		UPDATE conflict_plans
		SET state = 'applied', receipt_json = ?
		WHERE plan_id = ? AND state = 'applying'`,
		receiptRaw, planID,
	)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if err := requireOne(result); err != nil {
		return ApplyReceipt{}, err
	}
	result, err = tx.ExecContext(context.WithoutCancel(ctx), `
		UPDATE conflict_sets
		SET state = 'applied', revision = ?, set_json = ?
		WHERE conflict_id = ? AND state = 'applying'
		      AND revision = ?`,
		set.Revision, setRaw, set.ConflictID, set.Revision-1,
	)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if err := requireOne(result); err != nil {
		return ApplyReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyReceipt{}, err
	}
	return receipt, nil
}

func (engine *Engine) discardStaleApply(
	ctx context.Context,
	planID string,
	set Set,
) error {
	tx, err := engine.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM conflict_plans
		WHERE plan_id = ? AND state = 'applying'`,
		planID,
	); err != nil {
		return err
	}
	set.State = StatePending
	set.ReplanRequired = true
	set.Revision++
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conflict_sets
		SET state = 'pending', revision = ?, set_json = ?
		WHERE conflict_id = ? AND state = 'applying'`,
		set.Revision, raw, set.ConflictID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (engine *Engine) DeleteReplanRequired(
	ctx context.Context,
	conflictID string,
) error {
	result, err := engine.db.ExecContext(ctx, `
		DELETE FROM conflict_sets
		WHERE conflict_id = ? AND state = 'pending'
		  AND json_extract(set_json, '$.replanRequired') = 1`,
		conflictID,
	)
	if err != nil {
		return err
	}
	return requireOne(result)
}

func (engine *Engine) SetForPlan(
	ctx context.Context,
	planID string,
) (Set, error) {
	var conflictID string
	err := engine.db.QueryRowContext(ctx, `
		SELECT conflict_id FROM conflict_plans WHERE plan_id = ?`,
		planID,
	).Scan(&conflictID)
	if errors.Is(err, sql.ErrNoRows) {
		return Set{}, ErrPlanNotFound
	}
	if err != nil {
		return Set{}, err
	}
	return engine.Inspect(ctx, conflictID)
}

func (engine *Engine) ClearRootPins(
	ctx context.Context,
	conflictID string,
) error {
	set, err := engine.Inspect(ctx, conflictID)
	if err != nil {
		return err
	}
	if len(set.RootPinIDs) == 0 {
		return nil
	}
	set.RootPinIDs = nil
	set.Revision++
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	result, err := engine.db.ExecContext(ctx, `
		UPDATE conflict_sets
		SET revision = ?, set_json = ?
		WHERE conflict_id = ? AND revision = ?`,
		set.Revision, raw, conflictID, set.Revision-1,
	)
	if err != nil {
		return err
	}
	return requireOne(result)
}

func (engine *Engine) loadPlan(
	ctx context.Context,
	planID string,
) (
	Plan,
	Set,
	Resolution,
	string,
	ApplyStage,
	ApplyReceipt,
	error,
) {
	var (
		conflictID    string
		setRevision   uint64
		planRaw       []byte
		resolutionRaw []byte
		stageRaw      []byte
		receiptRaw    []byte
		state         string
	)
	err := engine.db.QueryRowContext(ctx, `
		SELECT conflict_id, set_revision, resolver_plan_json,
		       resolution_json, COALESCE(stage_json, X''),
		       COALESCE(receipt_json, X''), state
		FROM conflict_plans WHERE plan_id = ?`,
		planID,
	).Scan(
		&conflictID, &setRevision, &planRaw, &resolutionRaw,
		&stageRaw, &receiptRaw, &state,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, err
	}
	set, err := engine.Inspect(ctx, conflictID)
	if err != nil {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, err
	}
	if state == "prepared" && set.Revision != setRevision {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, ErrStalePlan
	}
	var plan Plan
	var resolution Resolution
	var stage ApplyStage
	var receipt ApplyReceipt
	if err := decodeStrict(planRaw, &plan); err != nil {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, err
	}
	if err := decodeStrict(resolutionRaw, &resolution); err != nil {
		return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
			ApplyReceipt{}, err
	}
	if len(stageRaw) > 0 {
		if err := decodeStrict(stageRaw, &stage); err != nil {
			return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
				ApplyReceipt{}, err
		}
	}
	if len(receiptRaw) > 0 {
		if err := decodeStrict(receiptRaw, &receipt); err != nil {
			return Plan{}, Set{}, Resolution{}, "", ApplyStage{},
				ApplyReceipt{}, err
		}
	}
	return plan, set, resolution, state, stage, receipt, nil
}

func (engine *Engine) Close() error {
	if engine == nil || engine.db == nil {
		return nil
	}
	_, checkpointErr := engine.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := engine.db.Close()
	engine.db = nil
	return errors.Join(checkpointErr, closeErr)
}

func validateChoices(
	plan Plan,
	graph DependencyGraph,
	choices []Choice,
) (Resolution, []string) {
	if !graph.Complete {
		return Resolution{}, []string{
			ErrDependencyIncomplete.Error(),
		}
	}
	conflicts := make(
		map[string]ItemKind,
		len(plan.Files)+len(plan.Tables)+1,
	)
	for _, item := range plan.Files {
		conflicts[item.DocumentID] = FileItem
	}
	for _, item := range plan.Tables {
		if _, duplicate := conflicts[item.TableID]; duplicate {
			return Resolution{}, []string{"conflict.item_id_collision"}
		}
		conflicts[item.TableID] = TableItem
	}
	if plan.Settings != nil {
		if _, duplicate := conflicts[plan.Settings.ItemID]; duplicate {
			return Resolution{}, []string{"conflict.item_id_collision"}
		}
		conflicts[plan.Settings.ItemID] = SettingsItem
	}
	resolution := Resolution{Choices: map[string]Side{}}
	for _, choice := range choices {
		itemID := choice.ItemID
		if itemID == "" {
			itemID = choice.DocumentID
		}
		expectedKind, exists := conflicts[itemID]
		kind := choice.Kind
		if kind == "" && choice.DocumentID != "" {
			kind = FileItem
		}
		sideValid := choice.Side == Local || choice.Side == Replica ||
			(choice.Side == Both && expectedKind == FileItem)
		if !exists || kind != expectedKind || !sideValid {
			return Resolution{}, []string{
				"conflict.choice_invalid",
			}
		}
		if _, duplicate := resolution.Choices[itemID]; duplicate {
			return Resolution{}, []string{
				"conflict.choice_duplicate",
			}
		}
		resolution.Choices[itemID] = choice.Side
	}
	if len(resolution.Choices) != len(conflicts) {
		return Resolution{}, []string{
			"conflict.choice_missing",
		}
	}
	for documentID := range resolution.Choices {
		closure, ok := dependencyClosure(documentID, graph)
		if !ok {
			return Resolution{}, []string{
				ErrDependencyIncomplete.Error(),
			}
		}
		for _, dependency := range closure {
			if _, isConflict := conflicts[dependency]; isConflict {
				if _, chosen := resolution.Choices[dependency]; !chosen {
					return Resolution{}, []string{
						fmt.Sprintf(
							"conflict.dependency_choice_missing:%s",
							dependency,
						),
					}
				}
			}
		}
	}
	return resolution, nil
}

func dependencyClosure(
	root string,
	graph DependencyGraph,
) ([]string, bool) {
	if !graph.Complete {
		return nil, false
	}
	seen := map[string]bool{root: true}
	queue := []string{root}
	var result []string
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		dependencies, known := graph.Edges[current]
		if !known {
			return nil, false
		}
		sorted := append([]string(nil), dependencies...)
		sort.Strings(sorted)
		for _, dependency := range sorted {
			if strings.TrimSpace(dependency) == "" {
				return nil, false
			}
			if seen[dependency] {
				continue
			}
			seen[dependency] = true
			result = append(result, dependency)
			queue = append(queue, dependency)
		}
	}
	return result, true
}

func validSet(set Set) bool {
	return strings.TrimSpace(set.ConflictID) != "" &&
		strings.TrimSpace(set.WorkspaceID) != "" &&
		(set.State == StatePending ||
			set.State == StateApplying ||
			set.State == StateApplied) &&
		set.Revision > 0 &&
		strings.TrimSpace(set.Base.SnapshotID) != "" &&
		strings.TrimSpace(set.Local.SnapshotID) != "" &&
		strings.TrimSpace(set.Replica.SnapshotID) != "" &&
		set.CreatedAt.After(time.Time{})
}

func requireOne(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrStalePlan
	}
	return nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("conflict.trailing_json")
		}
		return err
	}
	return nil
}
