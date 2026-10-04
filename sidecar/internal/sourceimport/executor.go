package sourceimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// Reserve space below the existing 1000-operation/1MiB kernel limits. Relation
// work includes reciprocal row writes; a source row is not an atomic operation.
const batchBudget = 400
const batchBytes = 512 << 10

type Executor struct {
	authority   Authority
	journal     Journal
	attachments AttachmentSource
}

func NewExecutor(authority Authority, journal Journal, attachments AttachmentSource) *Executor {
	return &Executor{authority: authority, journal: journal, attachments: attachments}
}

type execution struct {
	owner       *Executor
	plan        Plan
	result      Result
	sequence    int
	mappings    map[Key]Mapping
	revisions   map[string]string
	definitions map[Key]v2.FieldDefinition
}

// Execute only accepts a frozen plan exclusively claimed by the shared Go
// import plan owner. Providers/renderer never call it with arbitrary rows.
func (executor *Executor) Execute(ctx context.Context, plan Plan, jobID string, epoch uint64) (Result, error) {
	if !plan.CanApply || plan.Contract != Contract || !validID(jobID) || len(jobID) > 80 || executor.authority == nil || executor.journal == nil {
		return Result{}, fmt.Errorf("source_import.execute.invalid")
	}
	if len(plan.Attachments) > 0 && executor.attachments == nil {
		return Result{}, fmt.Errorf("source_import.attachments.unavailable")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return Result{}, err
	}
	var frozen Plan
	if err := v2.StrictDecode(raw, &frozen); err != nil {
		return Result{}, err
	}
	plan = frozen
	work := &execution{owner: executor, plan: plan, mappings: map[Key]Mapping{}, revisions: map[string]string{}, definitions: map[Key]v2.FieldDefinition{},
		result: Result{Contract: Contract, JobID: jobID, Provider: plan.Provider, ContainerID: plan.ContainerID, SourceName: plan.DisplayName, State: "interrupted", Stage: "schema",
			Targets: []Target{}, Batches: []Batch{}, Diagnostics: append([]Diagnostic{}, plan.Diagnostics...), Fields: []FieldSummary{}, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), SessionEpoch: epoch, ReadWindow: plan.ReadWindow}}
	for _, table := range plan.Tables {
		work.result.Total += len(table.Records)
		for _, field := range table.Fields {
			work.result.Fields = append(work.result.Fields, FieldSummary{work.key(table.SourceID, field.Source.ID, ""), field.Source.Kind, field.Policy, field.Source.Definition})
		}
	}
	if err := executor.journal.Start(ctx, work.result); err != nil {
		return Result{}, err
	}
	err = work.schema(ctx)
	if err == nil {
		err = work.records(ctx)
	}
	if err == nil {
		err = work.relations(ctx)
	}
	if err == nil {
		err = work.files(ctx)
	}
	if err == nil {
		err = work.finalize(ctx)
	}
	// Cancellation must not prevent persisting the true terminal facts.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if executor.attachments != nil {
		if cleanupErr := executor.attachments.Cleanup(settleCtx, jobID); cleanupErr != nil {
			work.result.Diagnostics = append(work.result.Diagnostics, Diagnostic{Code: "source_import.staging_cleanup", Message: "本任务附件暂存清理失败；已提交业务附件保留", Blocking: false})
			if err == nil {
				err = cleanupErr
			}
		}
	}
	if work.result.State != "unknown" {
		work.result.State = "failed"
		if err == nil {
			work.result.State = "succeeded"
			work.result.Stage = "settled"
		}
		if errors.Is(err, context.Canceled) {
			work.result.State = "cancelled"
		}
	}
	work.result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	work.result.NotSubmitted = work.result.Total - work.result.Created - work.result.UnknownRecords
	if err != nil {
		work.result.Diagnostics = append(work.result.Diagnostics, Diagnostic{Code: "source_import.execution_stopped", Message: "迁移已停止；保留已提交目标和批次，请查看分阶段结果", Blocking: true})
	}
	if finishErr := executor.journal.Finish(settleCtx, work.result); finishErr != nil {
		return work.result, fmt.Errorf("source_import.result_unavailable: %w", finishErr)
	}
	result, readErr := executor.journal.Read(settleCtx, jobID)
	if readErr != nil {
		return work.result, fmt.Errorf("source_import.result_unavailable: %w", readErr)
	}
	return result, err
}

func (work *execution) key(table, field, record string) Key {
	return Key{Provider: work.plan.Provider, ContainerID: work.plan.ContainerID, TableID: table, FieldID: field, RecordID: record}
}

func (work *execution) tableID(source string) string {
	return work.mappings[work.key(source, "", "")].LocalID
}
func (work *execution) fieldID(table, field string) string {
	return work.mappings[work.key(table, field, "")].LocalID
}
func (work *execution) recordID(table, record string) string {
	return work.mappings[work.key(table, "", record)].LocalID
}

// run checks a durable receipt before every submit and after every ambiguous
// error. It never substitutes a new key or relies on the expiring kernel cache.
func (work *execution) run(ctx context.Context, stage, tableID string, created int, submit func(string, func(core.App, Batch) error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	work.sequence++
	id := fmt.Sprintf("%s-b%06d", work.result.JobID, work.sequence)
	batch, found, err := work.owner.journal.FindBatch(ctx, work.result.JobID, id)
	if err != nil {
		work.result.State = "unknown"
		work.result.UnknownBatch = id
		return err
	}
	if found {
		work.accept(batch)
		return nil
	}
	work.result.Stage = stage
	if err := work.owner.journal.Prepare(ctx, work.result.JobID, id, stage, created); err != nil {
		return err
	}
	work.result.UnknownBatch = id
	work.result.UnknownRecords = created
	commit := func(tx core.App, batch Batch) error {
		batch.JobID = work.result.JobID
		batch.ID = id
		batch.Stage = stage
		batch.TableID = tableID
		if batch.TableID == "" {
			for _, mapping := range batch.Mappings {
				if mapping.Kind == "table" {
					batch.TableID = mapping.LocalID
					break
				}
			}
		}
		if batch.Mappings == nil {
			batch.Mappings = []Mapping{}
		}
		if batch.SchemaRevisions == nil {
			batch.SchemaRevisions = map[string]string{}
		}
		return work.owner.journal.Commit(tx, batch)
	}
	submitErr := submit(id, commit)
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	batch, found, readErr := work.owner.journal.FindBatch(readCtx, work.result.JobID, id)
	if readErr != nil {
		work.result.State = "unknown"
		return fmt.Errorf("source_import.commit_unknown: %w", readErr)
	}
	if found {
		work.accept(batch)
		// A committed-but-lost response is reconciled exclusively from this
		// receipt. Continue the next batch rather than replaying business work.
		return nil
	}
	if submitErr == nil {
		work.result.State = "unknown"
		return fmt.Errorf("source_import.receipt.missing")
	}
	// Completed authority call + authoritative absence of its atomic receipt
	// proves this batch did not commit. Earlier receipts stay untouched.
	work.result.UnknownBatch = ""
	work.result.UnknownRecords = 0
	return submitErr
}

func (work *execution) accept(batch Batch) {
	for _, mapping := range batch.Mappings {
		work.mappings[mapping.Source] = mapping
		if mapping.Kind == "table" {
			work.result.Targets = append(work.result.Targets, Target{mapping.Source.TableID, mapping.LocalID, mapping.Name, mapping.Collection})
		}
	}
	for table, revision := range batch.SchemaRevisions {
		work.revisions[table] = revision
	}
	work.result.Batches = append(work.result.Batches, batch)
	work.result.Created += batch.Created
	work.result.UnknownBatch = ""
	work.result.UnknownRecords = 0
}

func (work *execution) schema(ctx context.Context) error {
	actor := v2.Actor{ID: "source-import", Kind: "user"}
	for _, table := range work.plan.Tables {
		err := work.run(ctx, "schema", "", 0, func(id string, commit func(core.App, Batch) error) error {
			_, err := work.owner.authority.CreateTable(ctx, v2.TableCreateIntent{DisplayName: table.Name, OperationID: id, Actor: actor}, func(tx core.App, receipt v2.TableCreateReceipt) error {
				metadata, err := tx.FindFirstRecordByData("vibetable_tables", "table_id", receipt.TableID)
				if err != nil {
					return err
				}
				return commit(tx, Batch{Mappings: []Mapping{{Source: work.key(table.SourceID, "", ""), Kind: "table", LocalID: receipt.TableID, TableID: receipt.TableID, Name: table.Name, Collection: metadata.GetString("physical_name")}}, SchemaRevisions: map[string]string{receipt.TableID: receipt.SchemaRevision}})
			})
			return err
		})
		if err != nil {
			return err
		}
	}
	for _, table := range work.plan.Tables {
		for _, field := range table.Fields {
			if field.Policy == PolicySkip || field.Draft.LogicalType == v2.LogicalRelation {
				continue
			}
			if err := work.createField(ctx, table, field, nil, nil); err != nil {
				return err
			}
		}
	}
	for _, table := range work.plan.Tables {
		for _, field := range table.Fields {
			if field.Policy != PolicyNative || field.Draft.LogicalType != v2.LogicalRelation || work.fieldID(table.SourceID, field.Source.ID) != "" {
				continue
			}
			relation := field.Source.Relation
			target, ok := work.sourceTable(relation.TargetTableID)
			if !ok {
				return fmt.Errorf("source_import.relation.mapping_missing")
			}
			reverseName, reverseCardinality := "迁移反向关联", "many"
			if field.ReciprocalName != "" {
				reverseName = field.ReciprocalName
			}
			var reverse *FieldPlan
			if relation.TargetFieldID != "" {
				for i := range target.Fields {
					if target.Fields[i].Source.ID == relation.TargetFieldID {
						reverse = &target.Fields[i]
						break
					}
				}
				if reverse == nil || reverse.Source.Relation == nil {
					return fmt.Errorf("source_import.relation.pair_missing")
				}
				reverseName = reverse.Draft.DisplayName
				reverseCardinality = reverse.Source.Relation.Cardinality
			}
			pair := &v2.RelationPairDraft{ReciprocalDisplayName: reverseName, ReciprocalCardinality: reverseCardinality, SourceDisplayFieldID: work.fieldID(table.SourceID, table.PrimaryFieldID)}
			field.Draft.Relation.TargetTableID = work.tableID(target.SourceID)
			field.Draft.Relation.DisplayField = work.fieldID(target.SourceID, target.PrimaryFieldID)
			if err := work.createField(ctx, table, field, pair, reverse); err != nil {
				return err
			}
		}
	}
	return work.loadDefinitions(ctx)
}

func (work *execution) sourceTable(id string) (TablePlan, bool) {
	for _, table := range work.plan.Tables {
		if table.SourceID == id {
			return table, true
		}
	}
	return TablePlan{}, false
}

func (work *execution) createField(ctx context.Context, table TablePlan, field FieldPlan, pair *v2.RelationPairDraft, reverse *FieldPlan) error {
	tableID := work.tableID(table.SourceID)
	intent := v2.FieldChangeIntent{Action: v2.ActionCreate, TableID: tableID, ExpectedSchemaRev: work.revisions[tableID], Draft: &field.Draft, Actor: v2.Actor{ID: "source-import", Kind: "user"}, RelationPair: pair}
	return work.run(ctx, "schema", tableID, 0, func(id string, commit func(core.App, Batch) error) error {
		_, err := work.owner.authority.ChangeField(ctx, intent, id, func(tx core.App, receipt v2.ApplyReceipt) error {
			if receipt.Definition == nil || receipt.MigrationJobID != "" {
				return fmt.Errorf("source_import.field.unexpected_receipt")
			}
			batch := Batch{Mappings: []Mapping{{Source: work.key(table.SourceID, field.Source.ID, ""), Kind: "field", LocalID: receipt.FieldID, TableID: tableID}}, SchemaRevisions: map[string]string{tableID: receipt.SchemaRevision}}
			for _, related := range receipt.Related {
				batch.SchemaRevisions[related.TableID] = related.SchemaRevision
				if reverse != nil && related.TableID == work.tableID(field.Source.Relation.TargetTableID) {
					batch.Mappings = append(batch.Mappings, Mapping{Source: work.key(field.Source.Relation.TargetTableID, reverse.Source.ID, ""), Kind: "field", LocalID: related.FieldID, TableID: related.TableID})
				}
			}
			if reverse != nil && len(batch.Mappings) != 2 {
				return fmt.Errorf("source_import.relation.reverse_missing")
			}
			return commit(tx, batch)
		})
		return err
	})
}

func (work *execution) loadDefinitions(ctx context.Context) error {
	for _, table := range work.plan.Tables {
		tableID := work.tableID(table.SourceID)
		snapshot, err := work.owner.authority.Describe(ctx, tableID)
		if err != nil {
			return err
		}
		if snapshot.SchemaRevision != work.revisions[tableID] {
			return fmt.Errorf("source_import.schema_changed")
		}
		for _, field := range table.Fields {
			if field.Policy == PolicySkip {
				continue
			}
			id := work.fieldID(table.SourceID, field.Source.ID)
			found := false
			for _, definition := range snapshot.Fields {
				if definition.Identity.FieldID == id {
					work.definitions[work.key(table.SourceID, field.Source.ID, "")] = definition
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("source_import.field.mapping_missing")
			}
		}
	}
	return nil
}

func (work *execution) request(tableID string, operations []mutation.Operation) mutation.Request {
	return mutation.Request{ContractVersion: mutation.ContractVersion, TableID: tableID, SchemaRevision: work.revisions[tableID], Operations: operations, Actor: mutation.Actor{Type: "user", ID: "source-import"}}
}

func (work *execution) records(ctx context.Context) error {
	for _, table := range work.plan.Tables {
		tableID := work.tableID(table.SourceID)
		for start := 0; start < len(table.Records); {
			operations := []mutation.Operation{}
			sources := []string{}
			for end := start; end < len(table.Records) && len(operations) < batchBudget; end++ {
				row := table.Records[end]
				values := map[string]any{}
				for _, field := range table.Fields {
					if field.Policy == PolicySkip || field.Draft.LogicalType == v2.LogicalRelation || field.Draft.LogicalType == v2.LogicalFile {
						continue
					}
					value, supplied := row.Values[field.Source.ID]
					if !supplied {
						continue
					}
					definition := work.definitions[work.key(table.SourceID, field.Source.ID, "")]
					canonical, err := CanonicalValue(field, definition, value)
					if err != nil {
						return err
					}
					values[definition.Identity.FieldID] = canonical
				}
				candidate := append(operations, mutation.Operation{Kind: mutation.OperationInsert, Values: values})
				raw, err := json.Marshal(work.request(tableID, candidate))
				if err != nil {
					return err
				}
				if len(raw) > batchBytes {
					if len(operations) == 0 {
						return fmt.Errorf("source_import.record.capacity")
					}
					break
				}
				operations = candidate
				sources = append(sources, row.ID)
			}
			request := work.request(tableID, operations)
			err := work.run(ctx, "records", tableID, len(operations), func(id string, commit func(core.App, Batch) error) error {
				request.RequestID = id
				request.IdempotencyKey = id
				_, err := work.owner.authority.Mutate(ctx, request, func(tx core.App, receipt mutation.Receipt) error {
					if len(receipt.AffectedRows) != len(sources) {
						return fmt.Errorf("source_import.records.receipt_mismatch")
					}
					batch := Batch{Created: len(sources), Mappings: []Mapping{}}
					for index, row := range receipt.AffectedRows {
						if row.Operation != mutation.OperationInsert || row.RecordID == "" {
							return fmt.Errorf("source_import.records.receipt_mismatch")
						}
						batch.Mappings = append(batch.Mappings, Mapping{Source: work.key(table.SourceID, "", sources[index]), Kind: "record", LocalID: row.RecordID, TableID: tableID})
					}
					return commit(tx, batch)
				})
				return err
			})
			if err != nil {
				return err
			}
			start += len(operations)
		}
	}
	return nil
}

func (work *execution) relations(ctx context.Context) error {
	for _, table := range work.plan.Tables {
		for _, field := range table.Fields {
			if field.Policy != PolicyNative || field.Source.Relation == nil {
				continue
			}
			r := field.Source.Relation
			if r.TargetFieldID != "" && (table.SourceID > r.TargetTableID || (table.SourceID == r.TargetTableID && field.Source.ID > r.TargetFieldID)) {
				continue
			}
			for _, row := range table.Records {
				refs, err := relationReferences(field.Source, row.Values[field.Source.ID])
				if err != nil {
					return err
				}
				sort.Strings(refs)
				local := make([]string, 0, len(refs))
				for _, ref := range refs {
					id := work.recordID(r.TargetTableID, ref)
					if id == "" {
						return fmt.Errorf("source_import.relation.record_missing")
					}
					local = append(local, id)
				}
				// Every chunk adds at most budget-1 reciprocal rows. Existing
				// edges remain in the cumulative value; reverse endpoints are
				// never independently overwritten with their initially empty set.
				for start := 0; start < len(local); start += batchBudget - 1 {
					end := min(start+batchBudget-1, len(local))
					var value any = append([]string{}, local[:end]...)
					if r.Cardinality == "one" {
						value = local[0]
					}
					recordID := work.recordID(table.SourceID, row.ID)
					operation := mutation.Operation{Kind: mutation.OperationUpdate, RecordID: &recordID, Values: map[string]any{work.fieldID(table.SourceID, field.Source.ID): value}}
					if err := work.mutate(ctx, "relations", work.tableID(table.SourceID), []mutation.Operation{operation}, end-start, 0); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (work *execution) mutate(ctx context.Context, stage, tableID string, operations []mutation.Operation, relations, files int) error {
	request := work.request(tableID, operations)
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(operations)+relations > batchBudget || len(raw) > batchBytes {
		return fmt.Errorf("source_import.batch.capacity")
	}
	return work.run(ctx, stage, tableID, 0, func(id string, commit func(core.App, Batch) error) error {
		request.RequestID = id
		request.IdempotencyKey = id
		_, err := work.owner.authority.Mutate(ctx, request, func(tx core.App, receipt mutation.Receipt) error {
			if receipt.Status != mutation.StatusApplied {
				return fmt.Errorf("source_import.batch.not_applied")
			}
			return commit(tx, Batch{RelationWrites: relations, AttachmentWrites: files})
		})
		return err
	})
}

func (work *execution) files(ctx context.Context) error {
	for _, table := range work.plan.Tables {
		for _, field := range table.Fields {
			if field.Policy != PolicyNative || field.Draft.LogicalType != v2.LogicalFile {
				continue
			}
			for _, row := range table.Records {
				value := row.Values[field.Source.ID]
				if value == nil {
					continue
				}
				ids, err := stringList(value)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					continue
				}
				handles := []string{}
				for _, id := range ids {
					var attachment *Attachment
					for index := range work.plan.Attachments {
						candidate := &work.plan.Attachments[index]
						if candidate.TableID == table.SourceID && candidate.FieldID == field.Source.ID && candidate.RecordID == row.ID && candidate.ID == id {
							attachment = candidate
							break
						}
					}
					if attachment == nil {
						return fmt.Errorf("source_import.attachment.mapping_missing")
					}
					handle, err := work.owner.attachments.Stage(ctx, work.result.JobID, work.tableID(table.SourceID), work.fieldID(table.SourceID, field.Source.ID), *attachment)
					if err != nil {
						return err
					}
					if handle == "" {
						return fmt.Errorf("source_import.attachment.bytes_missing")
					}
					handles = append(handles, handle)
				}
				recordID := work.recordID(table.SourceID, row.ID)
				operation := mutation.Operation{Kind: mutation.OperationSetAttachments, RecordID: &recordID, FieldID: work.fieldID(table.SourceID, field.Source.ID), UploadHandles: handles, RemoveStoredNames: []string{}}
				if err := work.mutate(ctx, "attachments", work.tableID(table.SourceID), []mutation.Operation{operation}, 0, len(handles)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (work *execution) finalize(ctx context.Context) error {
	if err := work.loadDefinitions(ctx); err != nil {
		return err
	}
	for _, table := range work.plan.Tables {
		for _, field := range table.Fields {
			if !field.Deferred {
				continue
			}
			definition := work.definitions[work.key(table.SourceID, field.Source.ID, "")]
			draft := v2.FieldDraft{DisplayName: definition.DisplayName, Help: definition.Help, LogicalType: definition.LogicalType, Value: definition.Value, Constraints: definition.Constraints, Storage: definition.Storage, Display: definition.Display, Select: definition.Select, Relation: definition.Relation, File: definition.File, JSON: definition.JSON}
			draft.Value.Required = true
			tableID := work.tableID(table.SourceID)
			intent := v2.FieldChangeIntent{Action: v2.ActionUpdate, TableID: tableID, FieldID: definition.Identity.FieldID, ExpectedSchemaRev: work.revisions[tableID], Draft: &draft, Actor: v2.Actor{ID: "source-import", Kind: "user"}}
			if err := work.run(ctx, "constraints", tableID, 0, func(id string, commit func(core.App, Batch) error) error {
				_, err := work.owner.authority.ChangeField(ctx, intent, id, func(tx core.App, receipt v2.ApplyReceipt) error {
					return commit(tx, Batch{SchemaRevisions: map[string]string{tableID: receipt.SchemaRevision}})
				})
				return err
			}); err != nil {
				return err
			}
		}
	}
	return work.loadDefinitions(ctx)
}
