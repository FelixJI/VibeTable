package workspacev2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
)

type pendingFileChange struct {
	ChangeID             string   `json:"changeId"`
	RelativePath         string   `json:"relativePath"`
	Missing              bool     `json:"missing"`
	ObservedHash         string   `json:"observedHash"`
	ObservedSize         int64    `json:"observedSize"`
	Reason               string   `json:"reason"`
	CandidateDocumentIDs []string `json:"candidateDocumentIds"`
	CreatedAt            string   `json:"createdAt"`
	UpdatedAt            string   `json:"updatedAt"`
}

func (store *stateStore) upsertPendingFileChange(
	ctx context.Context,
	event filehistory.WatchEvent,
) error {
	if event.Confirmation == nil || event.Path == "" ||
		event.ObservedSize < 0 {
		return errors.New("file_history.pending_change_invalid")
	}
	candidates, err := json.Marshal(
		event.Confirmation.CandidateDocumentIDs,
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO pending_file_changes (
			change_id, relative_path, missing, observed_hash, observed_size,
			reason, candidate_document_ids_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(relative_path) DO UPDATE SET
			missing = excluded.missing,
			observed_hash = excluded.observed_hash,
			observed_size = excluded.observed_size,
			reason = excluded.reason,
			candidate_document_ids_json =
				excluded.candidate_document_ids_json,
			updated_at = excluded.updated_at`,
		uuid.NewString(),
		event.Path,
		event.Missing,
		event.ObservedContentHash,
		event.ObservedSize,
		event.Confirmation.Reason,
		string(candidates),
		now,
		now,
	)
	return err
}

func (store *stateStore) listPendingFileChanges(
	ctx context.Context,
) ([]pendingFileChange, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT change_id, relative_path, missing, observed_hash,
		       observed_size, reason, candidate_document_ids_json,
		       created_at, updated_at
		FROM pending_file_changes
		ORDER BY created_at, change_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []pendingFileChange
	for rows.Next() {
		var (
			item       pendingFileChange
			missing    int
			candidates string
		)
		if err := rows.Scan(
			&item.ChangeID,
			&item.RelativePath,
			&missing,
			&item.ObservedHash,
			&item.ObservedSize,
			&item.Reason,
			&candidates,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Missing = missing != 0
		if json.Unmarshal(
			[]byte(candidates),
			&item.CandidateDocumentIDs,
		) != nil {
			return nil, errors.New("file_history.pending_change_corrupt")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *stateStore) pendingFileChange(
	ctx context.Context,
	changeID string,
) (pendingFileChange, error) {
	var (
		item       pendingFileChange
		missing    int
		candidates string
	)
	err := store.db.QueryRowContext(ctx, `
		SELECT change_id, relative_path, missing, observed_hash,
		       observed_size, reason, candidate_document_ids_json,
		       created_at, updated_at
		FROM pending_file_changes WHERE change_id = ?`,
		changeID,
	).Scan(
		&item.ChangeID,
		&item.RelativePath,
		&missing,
		&item.ObservedHash,
		&item.ObservedSize,
		&item.Reason,
		&candidates,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return pendingFileChange{}, errors.New(
			"file_history.pending_change_not_found",
		)
	}
	if err != nil {
		return pendingFileChange{}, err
	}
	item.Missing = missing != 0
	if json.Unmarshal(
		[]byte(candidates),
		&item.CandidateDocumentIDs,
	) != nil {
		return pendingFileChange{}, errors.New(
			"file_history.pending_change_corrupt",
		)
	}
	return item, nil
}

func (store *stateStore) deletePendingFileChange(
	ctx context.Context,
	changeID string,
) error {
	result, err := store.db.ExecContext(
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
			errors.New("file_history.pending_change_not_found"),
			err,
		)
	}
	return nil
}

func (store *stateStore) clearPendingFileChangePath(
	ctx context.Context,
	path string,
) error {
	_, err := store.db.ExecContext(
		ctx,
		`DELETE FROM pending_file_changes WHERE relative_path = ?`,
		path,
	)
	return err
}
