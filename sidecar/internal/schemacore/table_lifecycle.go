package schemacore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/vibetable/vibetable/sidecar/internal/relatedcomputation"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaaudit"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type TableLifecycle struct {
	app   core.App
	clock func() time.Time
}

func NewTableLifecycle(app core.App) (*TableLifecycle, error) {
	if app == nil {
		return nil, errors.New("schema table lifecycle app is required")
	}
	return &TableLifecycle{app: app, clock: time.Now}, nil
}

func (lifecycle *TableLifecycle) Create(
	ctx context.Context,
	intent v2.TableCreateIntent,
) (v2.TableCreateReceipt, error) {
	displayName, err := validateTableCreateIntent(intent)
	if err != nil {
		return v2.TableCreateReceipt{}, err
	}
	tableID, physicalName := tableIdentities(intent.OperationID)
	receipt := v2.TableCreateReceipt{
		Contract:       v2.Contract,
		OperationID:    intent.OperationID,
		TableID:        tableID,
		DisplayName:    displayName,
		SchemaRevision: v2.FormatSchemaRevision(1),
	}
	err = lifecycle.app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx, txApp, lifecycle.clock().UTC(),
				)
			}
		}()
		existing, findErr := txApp.FindFirstRecordByFilter(
			"vibetable_tables",
			"table_id={:table}",
			dbx.Params{"table": tableID},
		)
		if findErr == nil {
			if existing.GetString("display_name") != displayName {
				return &fieldError{
					code:    "schema.table.operation_conflict",
					path:    "operationId",
					message: "operationId was already used for another table",
				}
			}
			return nil
		}
		if !errors.Is(findErr, sql.ErrNoRows) {
			return fmt.Errorf("find schema table replay: %w", findErr)
		}
		collection := core.NewBaseCollection(physicalName)
		collection.Fields.Add(&core.NumberField{
			Name: relatedcomputation.RowRevisionField, Hidden: true, OnlyInt: true,
		})
		if err := txApp.Save(collection); err != nil {
			return fmt.Errorf("create table collection: %w", err)
		}
		metadata, err := txApp.FindCollectionByNameOrId("vibetable_tables")
		if err != nil {
			return fmt.Errorf("load table metadata collection: %w", err)
		}
		record := core.NewRecord(metadata)
		record.Set("table_id", tableID)
		record.Set("collection_id", collection.Id)
		record.Set("physical_name", physicalName)
		record.Set("display_name", displayName)
		record.Set("kind", "base")
		record.Set("schema_revision", 1)
		record.Set("data_revision", 0)
		record.Set("archive_policy", `{"mode":"none","fieldId":null,"archivedValue":null}`)
		record.Set("primary_display_field_id", "")
		if err := txApp.Save(record); err != nil {
			return fmt.Errorf("save table metadata: %w", err)
		}
		afterHash := tableReceiptHash(receipt)
		if err := saveTableCreateAudit(
			ctx, txApp, intent, receipt, afterHash, lifecycle.clock().UTC(),
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return v2.TableCreateReceipt{}, err
	}
	return receipt, nil
}

func (lifecycle *TableLifecycle) Configure(
	ctx context.Context,
	intent v2.TableSettingsIntent,
) (v2.TableSettingsReceipt, error) {
	replay, err := tableSettingsReceipt(intent)
	if err != nil {
		return v2.TableSettingsReceipt{}, err
	}
	if found, err := lifecycle.findSettingsReplay(intent, replay); err != nil {
		return v2.TableSettingsReceipt{}, err
	} else if found {
		return replay, nil
	}
	var receipt v2.TableSettingsReceipt
	err = lifecycle.app.RunInTransaction(func(txApp core.App) (transactionErr error) {
		defer func() {
			if transactionErr == nil {
				transactionErr = writecoordinator.PersistPocketBaseReceipt(
					ctx, txApp, lifecycle.clock().UTC(),
				)
			}
		}()
		table, err := txApp.FindFirstRecordByFilter(
			"vibetable_tables", "table_id={:table}", dbx.Params{"table": intent.TableID},
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return tableSettingsError(
					"schema.table.not_found", "tableId", "table was not found", nil,
				)
			}
			return fmt.Errorf("find schema table settings: %w", err)
		}
		currentRevision := v2.FormatSchemaRevision(int64(table.GetInt("schema_revision")))
		if currentRevision != intent.ExpectedSchemaRev {
			return tableSettingsError(
				"schema.table.revision_conflict", "expectedSchemaRevision",
				"table schema revision changed",
				map[string]any{"expected": intent.ExpectedSchemaRev, "actual": currentRevision},
			)
		}
		previousPolicy := v2.ArchivePolicy{Mode: "none"}
		if raw := strings.TrimSpace(table.GetString("archive_policy")); raw != "" {
			if err := json.Unmarshal([]byte(raw), &previousPolicy); err != nil {
				return fmt.Errorf("decode current archive policy: %w", err)
			}
		}
		if err := validateArchivePolicy(ctx, txApp, intent.TableID, intent.ArchivePolicy); err != nil {
			return err
		}
		policyRaw, err := json.Marshal(intent.ArchivePolicy)
		if err != nil {
			return fmt.Errorf("encode archive policy: %w", err)
		}
		nextRevision := int64(table.GetInt("schema_revision")) + 1
		table.Set("archive_policy", string(policyRaw))
		table.Set("schema_revision", nextRevision)
		if err := txApp.Save(table); err != nil {
			return fmt.Errorf("save table settings: %w", err)
		}
		receipt = v2.TableSettingsReceipt{
			Contract: v2.Contract, OperationID: intent.OperationID, TableID: intent.TableID,
			SchemaRevision: v2.FormatSchemaRevision(nextRevision),
			ArchivePolicy:  intent.ArchivePolicy,
		}
		beforeHash := tableSettingsHash(v2.TableSettingsReceipt{
			Contract: v2.Contract, TableID: intent.TableID,
			SchemaRevision: currentRevision, ArchivePolicy: previousPolicy,
		})
		afterHash := tableSettingsHash(receipt)
		return saveTableSettingsAudit(
			ctx, txApp, intent, receipt, beforeHash, afterHash, lifecycle.clock().UTC(),
		)
	})
	if err != nil {
		return v2.TableSettingsReceipt{}, err
	}
	return receipt, nil
}

func tableSettingsReceipt(
	intent v2.TableSettingsIntent,
) (v2.TableSettingsReceipt, error) {
	if err := validateTableSettingsIntent(intent); err != nil {
		return v2.TableSettingsReceipt{}, err
	}
	revision, _ := v2.ParseSchemaRevision(intent.ExpectedSchemaRev)
	return v2.TableSettingsReceipt{
		Contract: v2.Contract, OperationID: intent.OperationID, TableID: intent.TableID,
		SchemaRevision: v2.FormatSchemaRevision(revision + 1),
		ArchivePolicy:  intent.ArchivePolicy,
	}, nil
}

func (lifecycle *TableLifecycle) findSettingsReplay(
	intent v2.TableSettingsIntent,
	receipt v2.TableSettingsReceipt,
) (bool, error) {
	record, err := lifecycle.app.FindFirstRecordByFilter(
		"vibetable_schema_audit", "operation_id={:operation}",
		dbx.Params{"operation": intent.OperationID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find table settings replay: %w", err)
	}
	if record.GetString("action") != "table.settings.update" ||
		record.GetString("table_id") != intent.TableID ||
		record.GetString("after_hash") != tableSettingsHash(receipt) {
		return false, tableSettingsError(
			"schema.table.operation_conflict", "operationId",
			"operationId was already used for other schema settings", nil,
		)
	}
	return true, nil
}

// Replay resolves the deterministic receipt for an already committed table
// create without entering a transaction or touching audit/receipt state.
func (lifecycle *TableLifecycle) Replay(
	intent v2.TableCreateIntent,
) (v2.TableCreateReceipt, error) {
	displayName, err := validateTableCreateIntent(intent)
	if err != nil {
		return v2.TableCreateReceipt{}, err
	}
	tableID, _ := tableIdentities(intent.OperationID)
	return v2.TableCreateReceipt{
		Contract:       v2.Contract,
		OperationID:    intent.OperationID,
		TableID:        tableID,
		DisplayName:    displayName,
		SchemaRevision: v2.FormatSchemaRevision(1),
	}, nil
}

// FindReplay recognizes a completed deterministic create before allocating a
// new coordinator revision. A not-found result is not an error and lets the
// caller enter the normal transactional create path.
func (lifecycle *TableLifecycle) FindReplay(
	intent v2.TableCreateIntent,
) (v2.TableCreateReceipt, bool, error) {
	receipt, err := lifecycle.Replay(intent)
	if err != nil {
		return v2.TableCreateReceipt{}, false, err
	}
	record, err := lifecycle.app.FindFirstRecordByFilter(
		"vibetable_tables",
		"table_id={:table}",
		dbx.Params{"table": receipt.TableID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return v2.TableCreateReceipt{}, false, nil
	}
	if err != nil {
		return v2.TableCreateReceipt{}, false, err
	}
	if record.GetString("display_name") != receipt.DisplayName {
		return v2.TableCreateReceipt{}, false,
			errors.New("schema.table.operation_conflict")
	}
	return receipt, true, nil
}

type fieldError struct {
	code    string
	path    string
	message string
}

func (err *fieldError) Error() string { return err.code + ": " + err.message }

func tableSettingsError(code, path, message string, details map[string]any) *v2.ProductError {
	return &v2.ProductError{Code: code, Path: path, Message: message, Details: details}
}

func validateTableSettingsIntent(intent v2.TableSettingsIntent) error {
	if strings.TrimSpace(intent.TableID) == "" ||
		strings.TrimSpace(intent.ExpectedSchemaRev) == "" ||
		strings.TrimSpace(intent.OperationID) == "" ||
		strings.TrimSpace(intent.Actor.ID) == "" ||
		strings.TrimSpace(intent.Actor.Kind) == "" {
		return tableSettingsError(
			"schema.table.settings_invalid", "", "table settings intent is incomplete", nil,
		)
	}
	if _, err := v2.ParseSchemaRevision(intent.ExpectedSchemaRev); err != nil {
		return tableSettingsError(
			"schema.table.settings_invalid", "expectedSchemaRevision",
			"expected schema revision is invalid", nil,
		)
	}
	return nil
}

func validateArchivePolicy(
	ctx context.Context,
	app core.App,
	tableID string,
	policy v2.ArchivePolicy,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if policy.Mode == "none" {
		if policy.FieldID != nil || policy.ArchivedValue != nil {
			return tableSettingsError(
				"schema.table.archive_policy_invalid", "archivePolicy",
				"none archive policy cannot reference a field or value", nil,
			)
		}
		return nil
	}
	if policy.Mode != "status" && policy.Mode != "deletedAt" {
		return tableSettingsError(
			"schema.table.archive_policy_invalid", "archivePolicy.mode",
			"archive policy mode is invalid", nil,
		)
	}
	if policy.FieldID == nil || strings.TrimSpace(*policy.FieldID) == "" {
		return tableSettingsError(
			"schema.table.archive_policy_invalid", "archivePolicy.fieldId",
			"archive policy field is required", nil,
		)
	}
	record, err := app.FindFirstRecordByFilter(
		"vibetable_fields",
		"table_id={:table} && field_id={:field} && lifecycle_state='active'",
		dbx.Params{"table": tableID, "field": *policy.FieldID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return tableSettingsError(
			"schema.table.archive_policy_invalid", "archivePolicy.fieldId",
			"archive policy field is unavailable", nil,
		)
	}
	if err != nil {
		return fmt.Errorf("find archive policy field: %w", err)
	}
	raw, err := json.Marshal(record.GetRaw("definition_v2_json"))
	if err != nil {
		return fmt.Errorf("encode archive field definition: %w", err)
	}
	var definition v2.FieldDefinition
	if err := v2.StrictDecode(raw, &definition); err != nil {
		return fmt.Errorf("decode archive field definition: %w", err)
	}
	if policy.Mode == "status" {
		if definition.LogicalType != v2.LogicalSelect || definition.Select == nil ||
			policy.ArchivedValue == nil {
			return tableSettingsError(
				"schema.table.archive_policy_invalid", "archivePolicy",
				"status archive policy requires a select field and archived option", nil,
			)
		}
		archived, ok := policy.ArchivedValue.(string)
		if !ok || archived == "" {
			return tableSettingsError(
				"schema.table.archive_policy_invalid", "archivePolicy.archivedValue",
				"archived option id is invalid", nil,
			)
		}
		for _, option := range definition.Select.Options {
			if option.OptionID == archived && option.State == v2.OptionActive {
				return nil
			}
		}
		return tableSettingsError(
			"schema.table.archive_policy_invalid", "archivePolicy.archivedValue",
			"archived option is unavailable", nil,
		)
	}
	if policy.ArchivedValue != nil ||
		(definition.LogicalType != v2.LogicalDateTime &&
			definition.LogicalType != v2.LogicalAutoDate) {
		return tableSettingsError(
			"schema.table.archive_policy_invalid", "archivePolicy",
			"deletedAt archive policy requires a dateTime field and null value", nil,
		)
	}
	return nil
}

func validateTableCreateIntent(intent v2.TableCreateIntent) (string, error) {
	name := strings.TrimSpace(intent.DisplayName)
	if name == "" || len([]rune(name)) > 128 {
		return "", errors.New("schema.table.display_name_invalid")
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", errors.New("schema.table.display_name_invalid")
		}
	}
	if strings.TrimSpace(intent.OperationID) == "" ||
		strings.TrimSpace(intent.Actor.ID) == "" ||
		strings.TrimSpace(intent.Actor.Kind) == "" {
		return "", errors.New("schema.table.intent_invalid")
	}
	return name, nil
}

func tableIdentities(operationID string) (string, string) {
	value := strings.ReplaceAll(
		uuid.NewSHA1(uuid.NameSpaceOID, []byte("vibetable.schema.table:"+operationID)).String(),
		"-",
		"",
	)
	return "tbl_" + value[:20], "t_" + value[:20]
}

func tableReceiptHash(receipt v2.TableCreateReceipt) string {
	raw, _ := json.Marshal(receipt)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func tableSettingsHash(receipt v2.TableSettingsReceipt) string {
	raw, _ := json.Marshal(receipt)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func saveTableCreateAudit(
	ctx context.Context,
	app core.App,
	intent v2.TableCreateIntent,
	receipt v2.TableCreateReceipt,
	afterHash string,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_schema_audit")
	if err != nil {
		return fmt.Errorf("load schema audit collection: %w", err)
	}
	actorRaw, _ := json.Marshal(intent.Actor)
	record := core.NewRecord(collection)
	record.Set("operation_id", intent.OperationID)
	record.Set("plan_id", "table-create:"+intent.OperationID)
	record.Set("action", "table.create")
	record.Set("table_id", receipt.TableID)
	record.Set("field_id", "")
	record.Set("before_hash", "")
	record.Set("after_hash", afterHash)
	record.Set("outcome", "applied")
	record.Set("actor_json", types.JSONRaw(actorRaw))
	record.Set("occurred_at", now)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save table create audit: %w", err)
	}
	return schemaaudit.SaveOutbox(ctx, app, schemaaudit.Event{
		OperationID:    intent.OperationID,
		PlanID:         "table-create:" + intent.OperationID,
		Action:         "table.create",
		TableID:        receipt.TableID,
		SchemaRevision: receipt.SchemaRevision,
		AfterHash:      afterHash,
		Actor:          intent.Actor,
		Outcome:        "applied",
	}, now)
}

func saveTableSettingsAudit(
	ctx context.Context,
	app core.App,
	intent v2.TableSettingsIntent,
	receipt v2.TableSettingsReceipt,
	beforeHash string,
	afterHash string,
	now time.Time,
) error {
	collection, err := app.FindCollectionByNameOrId("vibetable_schema_audit")
	if err != nil {
		return fmt.Errorf("load schema audit collection: %w", err)
	}
	actorRaw, _ := json.Marshal(intent.Actor)
	record := core.NewRecord(collection)
	record.Set("operation_id", intent.OperationID)
	record.Set("plan_id", "table-settings:"+intent.OperationID)
	record.Set("action", "table.settings.update")
	record.Set("table_id", receipt.TableID)
	record.Set("field_id", "")
	record.Set("before_hash", beforeHash)
	record.Set("after_hash", afterHash)
	record.Set("outcome", "applied")
	record.Set("actor_json", types.JSONRaw(actorRaw))
	record.Set("occurred_at", now)
	if err := app.Save(record); err != nil {
		return fmt.Errorf("save table settings audit: %w", err)
	}
	return schemaaudit.SaveOutbox(ctx, app, schemaaudit.Event{
		OperationID: intent.OperationID, PlanID: "table-settings:" + intent.OperationID,
		Action: "table.settings.update", TableID: receipt.TableID,
		SchemaRevision: receipt.SchemaRevision, BeforeHash: beforeHash, AfterHash: afterHash,
		Actor: intent.Actor, Outcome: "applied",
	}, now)
}
