package writecoordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbtypes "github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
)

type businessIntentContextKey struct{}

type BusinessIntent struct {
	WriteIntent
	Kind     string
	Identity string
}

type PocketBaseReceipt struct {
	WriteIntent
	Kind     string
	Identity string
}

// WithBusinessIntent binds a formal PocketBase mutation to the exact durable
// workspace write intent that prepared it. The marker is consumed inside the
// authoritative PocketBase transaction, never by HTTP middleware.
func WithBusinessIntent(
	ctx context.Context,
	intent WriteIntent,
	kind string,
	identity string,
) (context.Context, error) {
	if intent.MutationRevision == 0 ||
		strings.TrimSpace(kind) == "" ||
		strings.TrimSpace(identity) == "" {
		return nil, errors.New("workspace.business_intent_invalid")
	}
	return context.WithValue(ctx, businessIntentContextKey{}, BusinessIntent{
		WriteIntent: intent,
		Kind:        kind,
		Identity:    identity,
	}), nil
}

func businessIntentFrom(ctx context.Context) (BusinessIntent, bool) {
	intent, ok := ctx.Value(businessIntentContextKey{}).(BusinessIntent)
	return intent, ok
}

func EnsurePocketBaseReceiptTable(ctx context.Context, app core.App) error {
	if app == nil {
		return errors.New("workspace.business_receipt_app_required")
	}
	_, err := app.DB().NewQuery(`
		CREATE TABLE IF NOT EXISTS workspace_v2_mutation_receipts (
			mutation_revision INTEGER PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_epoch INTEGER NOT NULL,
			fence_epoch INTEGER NOT NULL,
			claim_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			identity TEXT NOT NULL,
			audit_source_sequence INTEGER NOT NULL,
			committed_at TEXT NOT NULL,
			UNIQUE(workspace_id, session_epoch, fence_epoch, claim_id, kind, identity)
		)
	`).WithContext(ctx).Execute()
	return err
}

// PersistPocketBaseReceipt writes both the exact recovery proof and a generic
// business audit envelope through txApp. Callers must invoke it before their
// PocketBase RunInTransaction callback returns successfully.
func PersistPocketBaseReceipt(
	ctx context.Context,
	txApp core.App,
	now time.Time,
) error {
	intent, ok := businessIntentFrom(ctx)
	if !ok {
		// v1 workspaces and unit tests that do not run under Workspace v2 keep
		// their existing transaction semantics.
		return nil
	}
	if txApp == nil || intent.MutationRevision > math.MaxInt64 {
		return errors.New("workspace.business_receipt_invalid")
	}
	var sourceSequence int64
	if err := txApp.DB().NewQuery(`
		SELECT COALESCE(MAX(source_sequence), 0) + 1
		FROM vibetable_audit_outbox
		WHERE source_epoch = 'business-v2'
	`).WithContext(ctx).Row(&sourceSequence); err != nil || sourceSequence <= 0 {
		return errors.Join(errors.New("workspace.business_audit_sequence_failed"), err)
	}
	result, err := txApp.DB().NewQuery(`
		INSERT INTO workspace_v2_mutation_receipts (
			mutation_revision, workspace_id, session_epoch, fence_epoch,
			claim_id, kind, identity, audit_source_sequence, committed_at
		) VALUES (
			{:revision}, {:workspace}, {:session}, {:fence},
			{:claim}, {:kind}, {:identity}, {:auditSequence}, {:committedAt}
		)
	`).WithContext(ctx).Bind(dbx.Params{
		"revision":      intent.MutationRevision,
		"workspace":     intent.Token.WorkspaceID,
		"session":       intent.Token.SessionEpoch,
		"fence":         intent.Token.FenceEpoch,
		"claim":         intent.Token.ClaimID,
		"kind":          intent.Kind,
		"identity":      intent.Identity,
		"auditSequence": sourceSequence,
		"committedAt":   now.UTC().Format(time.RFC3339Nano),
	}).Execute()
	if err != nil {
		return fmt.Errorf("persist workspace business receipt: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return errors.Join(errors.New("workspace.business_receipt_conflict"), affectedErr)
	}
	payload, err := json.Marshal(map[string]any{
		"type":             "workspace.v2.business-mutation",
		"workspaceId":      intent.Token.WorkspaceID,
		"sessionEpoch":     intent.Token.SessionEpoch,
		"fenceEpoch":       intent.Token.FenceEpoch,
		"claimId":          intent.Token.ClaimID,
		"mutationRevision": intent.MutationRevision,
		"kind":             intent.Kind,
		"identity":         intent.Identity,
	})
	if err != nil {
		return err
	}
	envelope, err := auditledger.NewEnvelope(
		fmt.Sprintf(
			"workspace-business:%s:%d",
			intent.Token.WorkspaceID,
			intent.MutationRevision,
		),
		"business-v2",
		uint64(sourceSequence),
		intent.Identity,
		payload,
		now.UTC(),
	)
	if err != nil {
		return err
	}
	collection, err := txApp.FindCollectionByNameOrId("vibetable_audit_outbox")
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	record.Set("event_id", envelope.EventID)
	record.Set("source_epoch", envelope.SourceEpoch)
	record.Set("source_sequence", envelope.SourceSequence)
	record.Set("mutation_identity", envelope.MutationIdentity)
	record.Set("payload_hash", envelope.PayloadHash)
	record.Set("payload_json", pbtypes.JSONRaw(envelope.Payload))
	record.Set("occurred_at", envelope.OccurredAt)
	record.Set("status", "pending")
	record.Set("attempts", 0)
	return txApp.Save(record)
}

func HasPocketBaseReceipt(
	ctx context.Context,
	app core.App,
	token Token,
	revision uint64,
) (bool, error) {
	if app == nil || revision == 0 || revision > math.MaxInt64 {
		return false, errors.New("workspace.business_receipt_invalid")
	}
	var count int
	err := app.DB().NewQuery(`
		SELECT COUNT(*)
		FROM workspace_v2_mutation_receipts
		WHERE mutation_revision = {:revision}
		  AND workspace_id = {:workspace}
		  AND session_epoch = {:session}
		  AND fence_epoch = {:fence}
		  AND claim_id = {:claim}
	`).WithContext(ctx).Bind(dbx.Params{
		"revision":  revision,
		"workspace": token.WorkspaceID,
		"session":   token.SessionEpoch,
		"fence":     token.FenceEpoch,
		"claim":     token.ClaimID,
	}).Row(&count)
	if err != nil {
		return false, err
	}
	if count < 0 || count > 1 {
		return false, errors.New("workspace.business_receipt_corrupt")
	}
	return count == 1, nil
}

// LoadPocketBaseReceipt returns the exact business identity associated with a
// prepared coordinator revision. Startup recovery uses it to route conflict
// publications back to their original durable stage.
func LoadPocketBaseReceipt(
	ctx context.Context,
	app core.App,
	token Token,
	revision uint64,
) (PocketBaseReceipt, bool, error) {
	if app == nil || revision == 0 || revision > math.MaxInt64 {
		return PocketBaseReceipt{}, false,
			errors.New("workspace.business_receipt_invalid")
	}
	var receipt PocketBaseReceipt
	err := app.DB().NewQuery(`
		SELECT kind, identity
		FROM workspace_v2_mutation_receipts
		WHERE mutation_revision = {:revision}
		  AND workspace_id = {:workspace}
		  AND session_epoch = {:session}
		  AND fence_epoch = {:fence}
		  AND claim_id = {:claim}
	`).WithContext(ctx).Bind(dbx.Params{
		"revision":  revision,
		"workspace": token.WorkspaceID,
		"session":   token.SessionEpoch,
		"fence":     token.FenceEpoch,
		"claim":     token.ClaimID,
	}).Row(&receipt.Kind, &receipt.Identity)
	if errors.Is(err, sql.ErrNoRows) {
		return PocketBaseReceipt{}, false, nil
	}
	if err != nil {
		return PocketBaseReceipt{}, false, err
	}
	if strings.TrimSpace(receipt.Kind) == "" ||
		strings.TrimSpace(receipt.Identity) == "" {
		return PocketBaseReceipt{}, false,
			errors.New("workspace.business_receipt_corrupt")
	}
	receipt.WriteIntent = WriteIntent{
		Token:            token,
		MutationRevision: revision,
	}
	return receipt, true, nil
}
