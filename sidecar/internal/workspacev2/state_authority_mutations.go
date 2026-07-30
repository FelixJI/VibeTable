package workspacev2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/vibetable/vibetable/sidecar/internal/protocolv2"
)

func (store *stateStore) deletePendingFileChangeWithOperationReceipt(
	ctx context.Context,
	changeID string,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			result, err := transaction.ExecContext(
				ctx,
				`DELETE FROM pending_file_changes WHERE change_id = ?`,
				changeID,
			)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return errors.Join(
					errors.New(
						"file_history.pending_change_not_found",
					),
					err,
				)
			}
			return nil
		},
	)
}

func (store *stateStore) updateRetentionWithOperationReceipt(
	ctx context.Context,
	expectedRevision uint64,
	next RetentionPolicy,
	mutationRevision uint64,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	var limit any
	if next.RepositoryLimitBytes != nil {
		limit = *next.RepositoryLimitBytes
	}
	snapshotBuckets, err := json.Marshal(next.SnapshotBuckets)
	if err != nil {
		return err
	}
	fileBuckets, err := json.Marshal(next.FileRevisionBuckets)
	if err != nil {
		return err
	}
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			result, err := transaction.ExecContext(ctx, `
				UPDATE retention_policy
				SET policy_revision = ?, snapshot_days = ?,
				    snapshot_count = ?, snapshot_buckets_json = ?,
				    file_revision_days = ?, file_revision_count = ?,
				    file_revision_buckets_json = ?,
				    repository_limit_bytes = ?, mutation_revision = ?
				WHERE singleton = 1 AND policy_revision = ?`,
				next.PolicyRevision,
				next.SnapshotDays,
				next.SnapshotCount,
				string(snapshotBuckets),
				next.FileRevisionDays,
				next.FileRevisionCount,
				string(fileBuckets),
				limit,
				mutationRevision,
				expectedRevision,
			)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return errors.New("retention.policy_revision_stale")
			}
			return nil
		},
	)
}

func (store *stateStore) putSnapshotImportPlanWithOperationReceipt(
	ctx context.Context,
	plan snapshotImportPlan,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			_, err := transaction.ExecContext(ctx, `
				INSERT INTO snapshot_import_plans (
					plan_id, source_path, source_hash, source_size,
					source_identity, expires_at
				) VALUES (?, ?, ?, ?, ?, ?)`,
				plan.PlanID,
				plan.SourcePath,
				plan.SourceHash,
				plan.SourceSize,
				plan.SourceIdentity,
				plan.ExpiresAt,
			)
			return err
		},
	)
}

func (store *stateStore) putSnapshotRestorePlanWithOperationReceipt(
	ctx context.Context,
	plan snapshotRestorePlan,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			_, err := transaction.ExecContext(ctx, `
				INSERT INTO snapshot_restore_plans (
					plan_id, snapshot_id, catalog_revision,
					mutation_revision, diff_hash, target_mode, expires_at
				) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				plan.PlanID,
				plan.SnapshotID,
				plan.CatalogRevision,
				plan.MutationRevision,
				plan.DiffHash,
				plan.TargetMode,
				plan.ExpiresAt,
			)
			return err
		},
	)
}

func (store *stateStore) putSnapshotExtractPlanWithOperationReceipt(
	ctx context.Context,
	plan snapshotExtractPlan,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			_, err := transaction.ExecContext(ctx, `
				INSERT INTO snapshot_extract_plans (
					plan_id, snapshot_id, catalog_revision, document_id,
					revision_id, relative_path, object_id, content_hash,
					content_size, expires_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				plan.PlanID,
				plan.SnapshotID,
				plan.CatalogRevision,
				plan.DocumentID,
				plan.RevisionID,
				plan.RelativePath,
				plan.ObjectID,
				plan.ContentHash,
				plan.ContentSize,
				plan.ExpiresAt,
			)
			return err
		},
	)
}

func (store *stateStore) putRepositoryKeyRotationPlanWithOperationReceipt(
	ctx context.Context,
	plan repositoryKeyRotationPlan,
	session protocolv2.Session,
	receipt protocolv2.OperationReceipt,
) error {
	return store.withOperationReceiptTransaction(
		ctx,
		session,
		receipt,
		func(transaction *sql.Tx) error {
			_, err := transaction.ExecContext(ctx, `
				INSERT INTO repository_key_rotation_plans (
					plan_id, session_epoch, fence_epoch, claim_id,
					mutation_revision, catalog_revision, expires_at
				) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				plan.PlanID,
				plan.SessionEpoch,
				plan.FenceEpoch,
				plan.ClaimID,
				plan.MutationRevision,
				plan.CatalogRevision,
				plan.ExpiresAt,
			)
			return err
		},
	)
}
