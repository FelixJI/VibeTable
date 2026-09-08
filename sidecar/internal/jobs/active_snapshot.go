package jobs

import (
	"context"
	"strings"

	"github.com/pocketbase/dbx"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// ReadActiveSnapshots reads the complete formula activity in the caller's
// transaction. Other families sharing vibetable_jobs are not formula tasks.
func ReadActiveSnapshots(ctx context.Context, db dbx.Builder) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rows []struct {
		ID, State      string
		Type           string `db:"job_type"`
		SchemaRevision int64  `db:"schema_revision"`
		Cursor         string `db:"cursor_json"`
		Progress       string `db:"progress_json"`
		Error          string `db:"error_json"`
	}
	err := db.NewQuery(`SELECT id,job_type,state,schema_revision,cursor_json,progress_json,
		COALESCE(error_json,'null') AS error_json FROM vibetable_jobs
		WHERE job_type IN ({:backfill},{:fanout}) AND state IN ('queued','running')
		ORDER BY id LIMIT {:limit}`).WithContext(ctx).Bind(dbx.Params{
		"backfill": formulaBackfillType, "fanout": formulaFanoutType, "limit": maxRetainedDataEvents + 1,
	}).All(&rows)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, jobError("job.storage_failed", "active formula tasks could not be read", true)
	}
	if len(rows) > maxRetainedDataEvents {
		return nil, jobError("job.resume_limit", "pending job recovery exceeds the 10000 job limit", false)
	}
	snapshots := make([]Snapshot, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot, err := snapshotFromJSON(Snapshot{
			JobID: row.ID, Type: row.Type, State: row.State,
			SchemaRevision: v2.FormatSchemaRevision(row.SchemaRevision),
		}, []byte(row.Cursor), []byte(row.Progress), []byte(row.Error))
		if err != nil {
			return nil, err
		}
		if (strings.Trim(row.Error, " \t\r\n") != "null" && snapshot.Error == nil) ||
			snapshot.Progress.Completed < 0 || snapshot.Progress.Total < snapshot.Progress.Completed {
			return nil, jobError("job.storage_corrupt", "active formula task state is invalid", false)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}
