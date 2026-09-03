package jobs

import (
	"context"
	"encoding/json"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// ReadActiveSnapshots reads the complete formula activity in the caller's App
// transaction. Other families sharing vibetable_jobs are not formula tasks.
func ReadActiveSnapshots(ctx context.Context, app core.App) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := app.FindRecordsByFilter(
		"vibetable_jobs",
		"(job_type={:backfill} || job_type={:fanout}) && (state='queued' || state='running')",
		"+id", maxRetainedDataEvents+1, 0,
		dbx.Params{"backfill": formulaBackfillType, "fanout": formulaFanoutType},
	)
	if err != nil {
		return nil, jobError("job.storage_failed", "active formula tasks could not be read", true)
	}
	if len(records) > maxRetainedDataEvents {
		return nil, jobError("job.resume_limit", "pending job recovery exceeds the 10000 job limit", false)
	}
	snapshots := make([]Snapshot, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot, err := snapshotFromRecord(record)
		if err != nil {
			return nil, err
		}
		storedError, err := json.Marshal(record.GetRaw("error_json"))
		if err != nil || (string(storedError) != "null" && snapshot.Error == nil) ||
			snapshot.Progress.Completed < 0 || snapshot.Progress.Total < snapshot.Progress.Completed {
			return nil, jobError("job.storage_corrupt", "active formula task state is invalid", false)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}
