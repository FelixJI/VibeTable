package sourceimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const JobsCollection = "vibetable_source_import_jobs"
const BatchesCollection = "vibetable_source_import_batches"

type PocketBaseJournal struct{ app core.App }

func NewJournal(app core.App) *PocketBaseJournal { return &PocketBaseJournal{app: app} }

func (journal *PocketBaseJournal) Start(ctx context.Context, result Result) error {
	if !validID(result.JobID) || len(result.JobID) > 128 || result.Contract != Contract || result.Total < 0 || result.Total > MaxRecords {
		return fmt.Errorf("source_import.job.invalid")
	}
	if result.State != "interrupted" || len(result.Batches) != 0 || len(result.Targets) != 0 || result.Created != 0 || result.FinishedAt != "" || result.UnknownBatch != "" || result.UnknownRecords != 0 || (result.Stage != "preparing" && result.Stage != "schema") {
		return fmt.Errorf("source_import.job.initial_state")
	}
	var replaySignal error
	err := journal.app.RunInTransaction(func(tx core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := findJob(tx, result.JobID)
		if err != nil {
			return err
		}
		if existing != nil {
			var prepared Result
			if err := json.Unmarshal([]byte(existing.GetString("result_json")), &prepared); err != nil {
				return err
			}
			// Only an untouched admission may advance once to execution. Already
			// executing or terminal jobs never become restartable on a new token.
			if prepared.State != "interrupted" || prepared.Stage != "preparing" || prepared.FinishedAt != "" || prepared.UnknownBatch != "" || prepared.Created != 0 || prepared.UnknownRecords != 0 ||
				prepared.Provider != result.Provider || prepared.ContainerID != result.ContainerID || prepared.SourceName != result.SourceName || prepared.SessionEpoch != result.SessionEpoch || prepared.Total != result.Total || prepared.ReadWindow != result.ReadWindow || !reflect.DeepEqual(prepared.Fields, result.Fields) {
				return fmt.Errorf("source_import.job.already_started")
			}
			if result.Stage == "preparing" {
				// The verified admission replay wrote nothing. Abort only its
				// exact outer Runtime intent instead of allocating a revision.
				replaySignal = writecoordinator.ReplayedBusinessWrite(ctx, "source.import.admit", result.JobID)
				return nil
			}
			prepared.Stage = "schema"
			existing.Set("result_json", prepared)
			if err := tx.Save(existing); err != nil {
				return err
			}
			return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
		}
		collection, err := tx.FindCollectionByNameOrId(JobsCollection)
		if err != nil {
			return err
		}
		record := core.NewRecord(collection)
		record.Set("job_id", result.JobID)
		record.Set("started_at", result.StartedAt)
		record.Set("result_json", result)
		if err := tx.Save(record); err != nil {
			return err
		}
		return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
	})
	if err != nil {
		return err
	}
	return replaySignal
}

func findJob(app core.App, id string) (*core.Record, error) {
	record, err := app.FindFirstRecordByFilter(JobsCollection, "job_id={:id}", dbx.Params{"id": id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (journal *PocketBaseJournal) FindBatch(ctx context.Context, jobID, id string) (Batch, bool, error) {
	if err := ctx.Err(); err != nil {
		return Batch{}, false, err
	}
	return readBatch(journal.app, jobID, id)
}

func readBatch(app core.App, jobID, id string) (Batch, bool, error) {
	record, err := app.FindFirstRecordByFilter(BatchesCollection, "job_id={:job} && batch_id={:batch}", dbx.Params{"job": jobID, "batch": id})
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, false, nil
	}
	if err != nil {
		return Batch{}, false, err
	}
	var batch Batch
	err = json.Unmarshal([]byte(record.GetString("receipt_json")), &batch)
	return batch, err == nil, err
}

// Commit must be called with the authority transaction app. It deliberately
// does not open another transaction or update the overall success flag.
func (journal *PocketBaseJournal) Commit(tx core.App, batch Batch) error {
	job, err := findJob(tx, batch.JobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("source_import.job.missing")
	}
	existing, found, err := readBatch(tx, batch.JobID, batch.ID)
	if err != nil {
		return err
	}
	if found {
		if !reflect.DeepEqual(existing, batch) {
			return fmt.Errorf("source_import.batch.conflict")
		}
		return nil
	}
	if batch.ID == "" || len(batch.ID) > 128 || len(batch.Mappings) > 1000 || batch.Created < 0 || batch.RelationWrites < 0 || batch.AttachmentWrites < 0 {
		return fmt.Errorf("source_import.batch.invalid")
	}
	collection, err := tx.FindCollectionByNameOrId(BatchesCollection)
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	record.Set("job_id", batch.JobID)
	record.Set("batch_id", batch.ID)
	record.Set("receipt_json", batch)
	if err := tx.Save(record); err != nil {
		return err
	}
	var result Result
	if err := json.Unmarshal([]byte(job.GetString("result_json")), &result); err != nil {
		return err
	}
	if result.UnknownBatch != batch.ID {
		return fmt.Errorf("source_import.batch.not_prepared")
	}
	result.UnknownBatch = ""
	result.UnknownRecords = 0
	job.Set("result_json", result)
	return tx.Save(job)
}

func (journal *PocketBaseJournal) Prepare(ctx context.Context, jobID, batchID, stage string, count int) error {
	if batchID == "" || len(batchID) > 128 || count < 0 || count > 1000 {
		return fmt.Errorf("source_import.batch.invalid")
	}
	return journal.app.RunInTransaction(func(tx core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := findJob(tx, jobID)
		if err != nil {
			return err
		}
		if job == nil {
			return fmt.Errorf("source_import.job.missing")
		}
		var result Result
		if err := json.Unmarshal([]byte(job.GetString("result_json")), &result); err != nil {
			return err
		}
		if result.State != "interrupted" || result.UnknownBatch != "" {
			return fmt.Errorf("source_import.job.not_active")
		}
		result.UnknownBatch = batchID
		result.UnknownRecords = count
		result.Stage = stage
		job.Set("result_json", result)
		if err := tx.Save(job); err != nil {
			return err
		}
		return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
	})
}

func (journal *PocketBaseJournal) Read(ctx context.Context, jobID string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	job, err := findJob(journal.app, jobID)
	if err != nil {
		return Result{}, err
	}
	if job == nil {
		return Result{}, fmt.Errorf("source_import.job.missing")
	}
	var result Result
	if err := json.Unmarshal([]byte(job.GetString("result_json")), &result); err != nil {
		return Result{}, err
	}
	batches, err := journal.app.FindRecordsByFilter(BatchesCollection, "job_id={:job}", "batch_id", 0, 0, dbx.Params{"job": jobID})
	if err != nil {
		return Result{}, err
	}
	result.Batches = []Batch{}
	result.Targets = []Target{}
	result.Created = 0
	for _, record := range batches {
		var batch Batch
		if err := json.Unmarshal([]byte(record.GetString("receipt_json")), &batch); err != nil {
			return Result{}, err
		}
		result.Batches = append(result.Batches, batch)
		result.Created += batch.Created
		if result.UnknownBatch == batch.ID {
			result.UnknownBatch = ""
			result.UnknownRecords = 0
			if result.State == "unknown" {
				result.State = "interrupted"
			}
		}
		for _, mapping := range batch.Mappings {
			if mapping.Kind == "table" {
				result.Targets = append(result.Targets, Target{mapping.Source.TableID, mapping.LocalID, mapping.Name, mapping.Collection})
			}
		}
	}
	result.NotSubmitted = result.Total - result.Created - result.UnknownRecords
	if result.NotSubmitted < 0 {
		return Result{}, fmt.Errorf("source_import.job.count_conflict")
	}
	return result, nil
}

func (journal *PocketBaseJournal) Finish(ctx context.Context, result Result) error {
	return journal.app.RunInTransaction(func(tx core.App) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := findJob(tx, result.JobID)
		if err != nil {
			return err
		}
		if job == nil {
			return fmt.Errorf("source_import.job.missing")
		}
		var initial Result
		if err := json.Unmarshal([]byte(job.GetString("result_json")), &initial); err != nil {
			return err
		}
		if initial.Provider != result.Provider || initial.ContainerID != result.ContainerID || initial.Total != result.Total || initial.SessionEpoch != result.SessionEpoch {
			return fmt.Errorf("source_import.job.binding_conflict")
		}
		result.StartedAt = initial.StartedAt
		if result.UnknownBatch != "" {
			_, found, err := readBatch(tx, result.JobID, result.UnknownBatch)
			if err != nil {
				return err
			}
			if found {
				result.UnknownBatch = ""
				result.UnknownRecords = 0
				if result.State == "unknown" {
					result.State = "interrupted"
				}
			}
		}
		switch result.State {
		case "succeeded", "failed", "cancelled", "unknown", "interrupted":
		default:
			return fmt.Errorf("source_import.job.state_invalid")
		}
		// Counts and targets are always reconstructed from authority receipts;
		// execution reports cannot fabricate or erase committed business facts.
		result.Batches = []Batch{}
		result.Targets = []Target{}
		result.Created = 0
		result.NotSubmitted = 0
		job.Set("result_json", result)
		if err := tx.Save(job); err != nil {
			return err
		}
		return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
	})
}

func (journal *PocketBaseJournal) List(ctx context.Context) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobs, err := journal.app.FindRecordsByFilter(JobsCollection, "", "-started_at", 200, 0)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(jobs))
	bytes := 0
	for _, job := range jobs {
		result, err := journal.Read(ctx, job.GetString("job_id"))
		if err != nil {
			return nil, err
		}
		projected := Project(result)
		raw, err := json.Marshal(projected)
		if err != nil {
			return nil, err
		}
		// Bound this additive history below the existing 4MiB Product reply
		// budget, leaving space for legacy CSV/XLSX history and its envelope.
		if bytes+len(raw) > 3<<20 {
			if len(results) == 0 {
				return nil, fmt.Errorf("source_import.history.capacity")
			}
			break
		}
		bytes += len(raw)
		results = append(results, projected)
	}
	return results, nil
}
