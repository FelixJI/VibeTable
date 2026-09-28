package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.clockNow = now
		}
	}
}

// StartClockUpdates is owned by the same cancellation/join lifecycle as
// backfills. A one-second wake check reads no rows or metadata unless the
// wall-clock minute changed; a resumed machine catches up on its next tick.
func (service *Service) StartClockUpdates(ctx context.Context) error {
	service.mu.Lock()
	if service.clockStarted || service.stopping {
		service.mu.Unlock()
		return nil
	}
	service.clockStarted = true
	service.runWait.Add(1)
	service.mu.Unlock()
	instant := service.clockNow()
	ids, err := service.RefreshClock(ctx, instant)
	if err != nil {
		service.mu.Lock()
		service.clockStarted = false
		service.mu.Unlock()
		service.runWait.Done()
		return err
	}
	for _, id := range ids {
		service.Start(id)
	}
	go func() {
		defer service.runWait.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		last := instant.UTC().Truncate(time.Minute)
		for {
			select {
			case <-service.runContext.Done():
				return
			case <-ticker.C:
				now := service.clockNow()
				minute := now.UTC().Truncate(time.Minute)
				if minute.Equal(last) {
					continue
				}
				last = minute
				ids, err := service.RefreshClock(service.runContext, now)
				if err != nil {
					if service.runContext.Err() == nil {
						service.app.Logger().Error("formula clock refresh failed", "error", err)
					}
					continue
				}
				for _, id := range ids {
					service.Start(id)
				}
			}
		}
	}()
	return nil
}

// RefreshClock discovers direct volatile roots only. Existing data-change
// fan-out handles their related tables; ordinary tables are never backfilled.
// It returns durable jobs, leaving dispatch to the lifecycle owner or tests.
func (service *Service) RefreshClock(ctx context.Context, instant time.Time) ([]string, error) {
	service.clockMu.Lock()
	defer service.clockMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service.mu.Lock()
	stopping := service.stopping
	gate := service.businessGate
	service.mu.Unlock()
	if stopping {
		return nil, context.Canceled
	}
	ctx = formula.WithEvaluationTime(ctx, instant)
	// ponytail: scan at most 10000 definitions per minute; use schema-change
	// notifications only if measured workspace size needs a larger bound.
	records, err := service.app.FindRecordsByFilter("vibetable_formulas", "", "+table_id,+field_id", 10001, 0)
	if err != nil {
		return nil, err
	}
	if len(records) > 10000 {
		return nil, jobError("job.clock_limit", "clock discovery exceeds 10000 formula definitions", false)
	}
	tables := map[string]bool{}
	for _, record := range records {
		source := record.GetString("source")
		if strings.Contains(source, "TODAY") || strings.Contains(source, "NOW") {
			tables[record.GetString("table_id")] = true
		}
	}
	var ids []string
	for tableID := range tables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		definition, err := schemaexecution.Describe(ctx, service.app, tableID)
		if err != nil {
			return nil, err
		}
		plan, compileErr := formula.CompilerFor(service.app).CompileExecutionTable(definition)
		if compileErr != nil {
			return nil, compileErr
		}
		var references []formula.ClockReference
		for _, compiled := range plan.Formulas {
			references = append(references, compiled.ClockReferences...)
		}
		if len(references) == 0 {
			continue
		}
		period := definition.Snapshot.SchemaRevision + "/" + formula.ClockSignature(ctx, references)
		if service.clockPeriods[tableID] == period {
			continue
		}
		// A definition backfill or previous period must finish before its next
		// clock job. The next minute checks again rather than running two writers.
		if existing, ok := service.findExistingFormulaJob(ctx, tableID, definition.Snapshot.SchemaRevision); ok {
			if existing.State == "queued" {
				ids = append(ids, existing.JobID)
			}
			if existing.State != "cancelled" || existing.ClockInstant == nil {
				continue
			}
		}
		var jobID string
		enqueue := func(writeCtx context.Context) error {
			return service.app.RunInTransaction(func(txApp core.App) error {
				var err error
				jobID, err = service.EnqueueFormulaBackfill(writeCtx, txApp, tableID, definition.Snapshot.SchemaRevision)
				if err != nil {
					return err
				}
				record, err := txApp.FindRecordById("vibetable_jobs", jobID)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(map[string]any{"tableId": tableID, "lastRecordId": "", "clockInstant": instant.UTC()})
				if err != nil {
					return err
				}
				record.Set("cursor_json", types.JSONRaw(raw))
				if err := txApp.Save(record); err != nil {
					return err
				}
				// Keep the two newest terminal clock backfills — complete, failed or
				// cancelled — for status inspection, plus this in-flight job. Ordinary
				// backfills and queued/running jobs are never removed here; a failed
				// period is retried as a fresh job next minute, so it cannot pile up.
				_, err = txApp.DB().NewQuery(`DELETE FROM vibetable_jobs WHERE id IN (
                    SELECT id FROM vibetable_jobs WHERE job_type={:type} AND source_table_id={:table}
                    AND state IN ('complete','failed','cancelled')
                    AND json_extract(cursor_json,'$.clockInstant') IS NOT NULL
                    ORDER BY rowid DESC LIMIT -1 OFFSET 2
                )`).WithContext(writeCtx).Bind(dbx.Params{"type": formulaBackfillType, "table": tableID}).Execute()
				if err != nil {
					return err
				}
				return writecoordinator.PersistPocketBaseReceipt(writeCtx, txApp, instant.UTC())
			})
		}
		if gate == nil {
			err = enqueue(ctx)
		} else {
			err = gate(ctx, "formula.clock.enqueue", fmt.Sprintf("%s:%d:%s", tableID, instant.Unix(), security.RandomString(15)), enqueue)
		}
		if err != nil {
			return nil, err
		}
		if jobID != "" {
			ids = append(ids, jobID)
		}
		service.clockPeriods[tableID] = period
	}
	return ids, nil
}
