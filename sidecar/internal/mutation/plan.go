package mutation

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/autonumber"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type RowKey struct {
	TableID  string
	RecordID string
}
type pendingPlanKey struct{}
type stagingPlanKey struct{}
type inspectPlanKey struct{}

type PlanEffect struct {
	TableID        string        `json:"tableId"`
	RecordID       string        `json:"recordId"`
	Operation      OperationKind `json:"operation"`
	SchemaRevision string        `json:"schemaRevision"`
	ExpectedDigest *string       `json:"expectedDigest,omitempty"`
}

type PlanRow struct {
	TableID string `json:"tableId"`
	AffectedRow
}
type PlanReceipt struct {
	Effects       []PlanEffect      `json:"-"`
	BaseRevisions map[string]string `json:"-"`
	Status        string            `json:"status"`
	ChangeSetID   string            `json:"changeSetId"`
	Rows          []PlanRow         `json:"rows"`
	Revisions     map[string]string `json:"revisions"`
	EmittedEvents []string          `json:"emittedEvents"`
	Warnings      []string          `json:"warnings"`
}

type sessionRow struct {
	request    Request
	definition schemaexecution.Table
	operation  NormalizedOperation
	record     *core.Record
	before     map[string]any
	after      map[string]any
	revision   int64
	insert     bool
	deleted    bool
	// Explicit relation values are compared with the derived final graph.
	relations map[string]any
}

// ApplyPlan uses the same normalizer, operation executor, history and outbox as Apply.
// The caller owns stable IDs and persists its durable result through commit.
func (kernel *Kernel) ApplyPlan(ctx context.Context, requests []Request, authorize func(context.Context, core.App, string, string) error, commit func(core.App, PlanReceipt) error) (PlanReceipt, error) {
	result := PlanReceipt{Status: "applied", Rows: []PlanRow{}, Revisions: map[string]string{}, BaseRevisions: map[string]string{}, EmittedEvents: []string{}, Warnings: []string{}}
	if len(requests) == 0 || authorize == nil || commit == nil {
		return result, mutationError("transaction.plan_invalid", nil, "plan authorization and result persistence are required", nil, false)
	}
	count := 0
	pending := map[RowKey]bool{}
	for _, request := range requests {
		if err := validateRequestShape(request); err != nil {
			return result, err
		}
		count += len(request.Operations)
		for _, operation := range request.Operations {
			if operation.Kind == OperationInsert {
				if operation.RecordID == nil {
					return result, mutationError("transaction.id_required", nil, "plan inserts require allocated IDs", nil, false)
				}
				pending[RowKey{request.TableID, *operation.RecordID}] = true
			}
		}
	}
	raw, err := json.Marshal(requests)
	if err != nil || len(raw) > maxRequestBytes || count > maxOperations {
		return result, mutationError("transaction.budget_exceeded", nil, "plan exceeds its total budget", nil, false)
	}
	if err := kernel.coordinator.acquire(ctx); err != nil {
		return result, err
	}
	locked := true
	defer func() {
		if locked {
			kernel.coordinator.release()
		}
	}()
	applyNow := kernel.now()
	ctx = formula.EnsureEvaluationTimeAt(ctx, applyNow)
	ctx = relatedcomputation.WithClockCache(ctx)
	ctx = context.WithValue(ctx, pendingPlanKey{}, pending)
	staging := context.WithValue(ctx, stagingPlanKey{}, true)
	err = kernel.app.RunInTransaction(func(tx core.App) error {
		rows := map[RowKey]*sessionRow{}
		keys := []RowKey{}
		touched := map[string]map[string]bool{}
		numbers := map[string][]*autonumber.Field{}
		for _, request := range requests {
			preview, err := kernel.preview(ctx, tx, request)
			if err != nil {
				return err
			}
			_, collection, err := loadTableMetadata(tx, request.TableID)
			if err != nil {
				return err
			}
			if err := kernel.validateGlobalGuard(ctx, tx, preview.Definition, collection, request, preview.Operations); err != nil {
				return err
			}
			if touched[request.TableID] == nil {
				touched[request.TableID] = map[string]bool{}
			}
			for i, op := range preview.Operations {
				record, before, err := kernel.resolveOperationRecord(tx, collection, preview.Definition, op, map[string]*core.Record{}, map[string]bool{})
				if err != nil {
					return err
				}
				key := RowKey{request.TableID, record.Id}
				if rows[key] != nil {
					return mutationError("transaction.duplicate_row", nil, "one final operation per table and row is required", nil, false)
				}
				rev, err := committedRowRevision(ctx, tx, key.TableID, key.RecordID)
				if err != nil {
					return err
				}
				if err := validateOperationGuard(before, rev, op, i); err != nil {
					return err
				}
				if err := authorize(ctx, tx, key.TableID, string(op.Kind)); err != nil {
					return err
				}
				relations := map[string]any{}
				if op.Kind != OperationUpdate {
					touched[key.TableID]["*"] = true
				}
				for _, f := range preview.Definition.Snapshot.Fields {
					if op.Kind == OperationDelete && f.Relation != nil && f.Relation.ReciprocalFieldID != "" {
						target := f.Relation.TargetTableID
						if touched[target] == nil {
							touched[target] = map[string]bool{}
						}
						touched[target][f.Relation.ReciprocalFieldID] = true
					}
					if value, set := op.Values[f.Identity.PhysicalName]; set {
						touched[key.TableID][f.Identity.FieldID] = true
						if f.Relation != nil {
							_, byID := request.Operations[i].Values[f.Identity.FieldID]
							_, byName := request.Operations[i].Values[f.Identity.PhysicalName]
							if byID || byName {
								relations[f.Identity.PhysicalName] = value
							}
							if f.Relation.ReciprocalFieldID != "" {
								target := f.Relation.TargetTableID
								if touched[target] == nil {
									touched[target] = map[string]bool{}
								}
								touched[target][f.Relation.ReciprocalFieldID] = true
							}
						}
					}
				}
				rows[key] = &sessionRow{request: request, definition: preview.Definition, operation: op, record: record, before: before, revision: rev, insert: op.Kind == OperationInsert, deleted: op.Kind == OperationDelete, relations: relations}
				keys = append(keys, key)
			}
			if _, exists := numbers[request.TableID]; !exists {
				var err error
				numbers[request.TableID], err = autonumber.Load(ctx, tx, preview.Definition.Snapshot.TableID)
				if err != nil {
					return err
				}
			}
		}
		var guards []transactionrules.Guard
		if ctx.Value(inspectPlanKey{}) != true {
			var err error
			guards, err = transactionrules.Capture(ctx, tx, touched)
			if err != nil {
				return businessRuleError(err)
			}
		}
		// First materialize valid base rows. Relationship validation is against the whole final graph.
		for _, key := range keys {
			row := rows[key]
			if !row.insert {
				continue
			}
			op := row.operation
			op.Values = map[string]any{}
			for name, value := range row.operation.Values {
				field, found := row.definition.Field(name)
				if !found || field.Relation == nil {
					op.Values[name] = value
				}
			}
			if _, err := kernel.applyOperation(context.WithValue(ctx, stagingPlanKey{}, true), tx, row.definition, row.record, op, nil, row.revision+1, map[string]map[string]any{}, numbers[key.TableID], applyNow); err != nil {
				return err
			}
		}
		for _, key := range keys {
			row := rows[key]
			op := row.operation
			if row.insert {
				op.Kind = OperationUpdate
			}
			if row.deleted {
				continue
			}
			after, err := kernel.applyOperation(staging, tx, row.definition, row.record, op, row.before, row.revision+1, map[string]map[string]any{}, nil, applyNow)
			if err != nil {
				return err
			}
			row.after = after
			if err := kernel.injectFault("after_record"); err != nil {
				return err
			}
		}
		// All explicit rows exist now; reciprocal writes share the same session images.
		for _, key := range append([]RowKey(nil), keys...) {
			row := rows[key]
			after := row.after
			if row.deleted {
				after = nil
			}
			changes, err := kernel.syncReciprocalRelations(staging, tx, row.definition, key.RecordID, row.before, after)
			if err != nil {
				return err
			}
			for _, change := range changes {
				target := RowKey{change.definition.Snapshot.TableID, change.recordID}
				other := rows[target]
				if err := authorize(ctx, tx, target.TableID, string(OperationUpdate)); err != nil {
					return err
				}
				if other == nil {
					if len(rows) >= maxOperations {
						return mutationError("transaction.budget_exceeded", nil, "derived writes exceed the plan budget", nil, false)
					}
					rev, err := committedRowRevision(ctx, tx, target.TableID, target.RecordID)
					if err != nil {
						return err
					}
					request := row.request
					request.TableID = target.TableID
					request.SchemaRevision = change.definition.Snapshot.SchemaRevision
					other = &sessionRow{request: request, definition: change.definition, operation: NormalizedOperation{Kind: OperationUpdate}, before: change.before, revision: rev, relations: map[string]any{}}
					rows[target] = other
					keys = append(keys, target)
				}
				if other.deleted {
					for name, value := range row.relations {
						field, _ := row.definition.Field(name)
						if field.Relation.TargetTableID != target.TableID {
							continue
						}
						ids, err := normalizeRelationIDs(value)
						if err != nil {
							return err
						}
						if stringIn(ids, target.RecordID) {
							return mutationError("transaction.relation_conflict", nil, "explicit relation targets a deleted row", nil, false)
						}
					}
					continue
				}
				for name, value := range other.relations {
					if !reflect.DeepEqual(value, change.after[name]) {
						return mutationError("transaction.relation_conflict", nil, "explicit and reciprocal relations disagree", nil, false)
					}
				}
				other.record = change.record
				other.after = change.after
			}
		}
		for _, key := range keys {
			row := rows[key]
			if row.deleted {
				if _, err := kernel.applyOperation(ctx, tx, row.definition, row.record, row.operation, row.before, row.revision+1, nil, nil, applyNow); err != nil {
					return err
				}
			}
		}
		for _, fields := range numbers {
			if err := autonumber.Save(tx, fields); err != nil {
				return err
			}
		}
		imageBytes := 0
		for _, key := range keys {
			row := rows[key]
			raw, err := json.Marshal([]map[string]any{row.before, row.after})
			if err != nil {
				return err
			}
			imageBytes += len(raw)
			if imageBytes > maxRequestBytes {
				return mutationError("transaction.budget_exceeded", nil, "final write images exceed the plan budget", nil, false)
			}
		}
		if err := transactionrules.ValidateGuards(ctx, tx, guards); err != nil {
			return businessRuleError(err)
		}
		result.ChangeSetID = kernel.newID("changeSet")
		tableRows := map[string][]string{}
		tableDefinitions := map[string]schemaexecution.Table{}
		for _, key := range keys {
			tableRows[key.TableID] = append(tableRows[key.TableID], key.RecordID)
			tableDefinitions[key.TableID] = rows[key].definition
		}
		tables := make([]string, 0, len(tableRows))
		for table := range tableRows {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		revisions := map[string]int64{}
		for _, table := range tables {
			meta, _, err := loadTableMetadata(tx, table)
			if err != nil {
				return err
			}
			revision, err := storedNonNegativeInteger(meta.GetRaw("data_revision"), "mutation.metadata.invalid_data_revision")
			if err != nil {
				return err
			}
			if revision >= maxSafeCounter {
				return mutationError("mutation.metadata.data_revision_exhausted", nil, "table revision exhausted", nil, false)
			}
			result.BaseRevisions[table] = formatRevision("data", revision)
			revision++
			revisions[table] = revision
			for _, key := range keys {
				if key.TableID != table {
					continue
				}
				row := rows[key]
				if err := relatedcomputation.AdvanceInputRevisions(ctx, tx, table, row.definition.Snapshot.Fields, row.before, row.after, string(row.operation.Kind), revision); err != nil {
					return err
				}
			}
			meta.Set("data_revision", revision)
			if err := tx.Save(meta); err != nil {
				return err
			}
			result.Revisions[table] = formatRevision("data", revision)
			event := DataChangedEvent{ContractVersion: ContractVersion, Topic: "data.changed", EventID: kernel.newID("event"), Sequence: revision, OccurredAt: applyNow.UTC().Format(time.RFC3339), SchemaRevision: tableDefinitions[table].Snapshot.SchemaRevision, DataRevision: result.Revisions[table], ChangeSetID: &result.ChangeSetID, TableID: table, RecordIDs: tableRows[table], Operation: DataChangeUpdate}
			if err := saveOutbox(tx, event); err != nil {
				return err
			}
			if kernel.invalidator != nil {
				if _, err := kernel.invalidator.EnqueueInvalidations(ctx, tx, event); err != nil {
					return err
				}
			}
			result.EmittedEvents = append(result.EmittedEvents, event.EventID)
		}
		// Every table clock is final before formulas observe the transaction graph.
		ctx = relatedcomputation.WithClockCache(formula.WithSourceRecalculation(ctx))
		imageBytes = 0
		for _, table := range tables {
			for index, key := range keys {
				if key.TableID != table {
					continue
				}
				row := rows[key]
				if !row.deleted {
					row.record.Set(relatedcomputation.RowRevisionField, row.revision+1)
					if err := kernel.calculateRelatedFormulas(ctx, tx, row.definition, row.record); err != nil {
						return err
					}
					if err := tx.Save(row.record); err != nil {
						return err
					}
					row.after = ProductRow(tx, row.definition, row.record)
				}
				raw, err := json.Marshal([]map[string]any{row.before, row.after})
				if err != nil {
					return err
				}
				imageBytes += len(raw)
				if imageBytes > maxRequestBytes {
					return mutationError("transaction.budget_exceeded", nil, "computed write images exceed the plan budget", nil, false)
				}
				if err := saveAudit(ctx, tx, result.ChangeSetID, index+1, row.request, row.definition, key.RecordID, row.operation.Kind, row.before, row.after, revisions[table], applyNow); err != nil {
					return err
				}
				digestRow := row.after
				if digestRow == nil {
					digestRow = map[string]any{"id": key.RecordID}
				}
				digest, err := canonicalDigest(digestRow)
				if err != nil {
					return err
				}
				result.Rows = append(result.Rows, PlanRow{table, AffectedRow{RecordID: key.RecordID, Operation: row.operation.Kind, Revision: formatRevision("row", row.revision+1), Digest: digest}})
				effect := PlanEffect{TableID: table, RecordID: key.RecordID, Operation: row.operation.Kind, SchemaRevision: row.definition.Snapshot.SchemaRevision}
				if row.before != nil {
					digest, err := canonicalDigest(row.before)
					if err != nil {
						return err
					}
					effect.ExpectedDigest = &digest
				}
				result.Effects = append(result.Effects, effect)
			}
		}
		sort.Slice(result.Effects, func(i, j int) bool {
			a, b := result.Effects[i], result.Effects[j]
			return a.TableID < b.TableID || a.TableID == b.TableID && a.RecordID < b.RecordID
		})
		if err := kernel.injectFault("after_audit"); err != nil {
			return err
		}
		if err := kernel.injectFault("after_outbox"); err != nil {
			return err
		}
		if err := commit(tx, result); err != nil {
			return err
		}
		if err := writecoordinator.PersistPocketBaseReceipt(ctx, tx, applyNow); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return kernel.injectFault("before_commit")
	})
	err = writecoordinator.ClassifyPocketBaseTransactionError(ctx, kernel.app, err)
	kernel.coordinator.release()
	locked = false
	if err != nil {
		return PlanReceipt{}, err
	}
	if kernel.publisher != nil {
		if err := kernel.publishCommitted(ctx, result.EmittedEvents); err != nil {
			result.Warnings = append(result.Warnings, "mutation.realtime.publish_pending")
		}
	}
	return result, nil
}
