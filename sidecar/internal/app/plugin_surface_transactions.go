package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
	"github.com/vibetable/vibetable/sidecar/internal/transactionrules"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

type transactionOperation struct {
	Entity         string         `json:"entity"`
	Kind           string         `json:"kind"`
	TempID         string         `json:"tempId,omitempty"`
	RecordID       string         `json:"recordId,omitempty"`
	ExpectedDigest *string        `json:"expectedDigest,omitempty"`
	Values         map[string]any `json:"values,omitempty"`
}
type transactionPlan struct {
	Contract   string                 `json:"contract"`
	ModelID    string                 `json:"modelId"`
	Operations []transactionOperation `json:"operations"`
}
type transactionPreview struct {
	PreviewRevision      int                          `json:"previewRevision"`
	Effects              []mutation.PlanEffect        `json:"effects"`
	DataRevisions        map[string]string            `json:"dataRevisions"`
	Contract             string                       `json:"contract"`
	WorkspaceID          string                       `json:"workspaceId"`
	InstallationID       string                       `json:"installationId"`
	InstallationRevision int64                        `json:"installationRevision"`
	RequestID            string                       `json:"requestId"`
	Epoch                uint64                       `json:"epoch"`
	Status               string                       `json:"status"`
	Plan                 transactionPlan              `json:"plan"`
	Model                pluginmodel.State            `json:"model"`
	Declaration          transactionrules.Declaration `json:"declaration"`
	NewIDs               map[string]map[string]string `json:"newIds"`
}
type transactionResult struct {
	Contract       string                       `json:"contract"`
	WorkspaceID    string                       `json:"workspaceId"`
	InstallationID string                       `json:"installationId"`
	RequestID      string                       `json:"requestId"`
	Status         string                       `json:"status"`
	NewIDs         map[string]map[string]string `json:"newIds"`
	Receipt        mutation.PlanReceipt         `json:"receipt"`
}

var errTransactionReplay = errors.New("transaction replay")

func transactionAuthorize(input pluginSurfaceAPIRequest, workspace string) func(context.Context, core.App, string, string) error {
	return func(ctx context.Context, app core.App, table, kind string) error {
		current, err := surfaceInstallation(ctx, app, workspace, input)
		if err != nil {
			return err
		}
		operation := "update"
		if kind == "insert" {
			operation = "create"
		} else if kind == "delete" {
			operation = "delete"
		}
		if current.grantFor(table, input.ActiveCollection, operation) == nil {
			return pluginDataError("plugin_write_denied")
		}
		return nil
	}
}

func validTransactionResult(result transactionResult, workspace, installation, request string) bool {
	if result.Contract != transactionrules.Contract || result.WorkspaceID != workspace || result.InstallationID != installation || result.RequestID != request || result.Status != "applied" || result.Receipt.Status != "applied" || result.Receipt.ChangeSetID == "" || len(result.Receipt.Rows) == 0 || len(result.Receipt.EmittedEvents) == 0 {
		return false
	}
	for _, row := range result.Receipt.Rows {
		if row.TableID == "" || row.RecordID == "" || row.Revision == "" || row.Digest == "" || result.Receipt.Revisions[row.TableID] == "" {
			return false
		}
	}
	return true
}

func transactionUnknown(id string) map[string]any {
	return map[string]any{"contract": transactionrules.Contract, "requestId": id, "status": "unknown"}
}

func loadTransactionEvent(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, id, kind string, target any) (found bool, err error) {
	err = app.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		raw, err := pluginstore.New(tx, workspace).GetAudit(ctx, input.ProjectKey, input.PluginID, "transaction:"+kind+":"+id)
		if kind == "preview" {
			rows, readErr := tx.FindRecordsByFilter("vibetable_plugin_records", "kind='audit' && project_key={:project} && plugin_id={:plugin} && payload_json.eventType='transaction.preview' && payload_json.details.requestId={:request}", "-seq", 1, 0, dbx.Params{"project": input.ProjectKey, "plugin": input.PluginID, "request": id})
			if readErr != nil {
				return readErr
			}
			if len(rows) == 0 {
				return nil
			}
			raw, err = json.Marshal(rows[0].GetRaw("payload_json"))
		}
		if err != nil || len(raw) == 0 {
			return err
		}
		var event struct {
			Details json.RawMessage `json:"details"`
		}
		if json.Unmarshal(raw, &event) != nil || mutation.DecodeStrict(event.Details, target) != nil {
			return pluginDataError("transaction.result_corrupt")
		}
		if result, ok := target.(*transactionResult); kind == "result" && ok && validTransactionResult(*result, workspace, input.InstallationID, id) {
			if err := authorizeTransactionRecovery(ctx, tx, workspace, input, installed, *result); err != nil {
				return err
			}
		}
		found = true
		return nil
	})
	return found, err
}

// A completed receipt survives enable/disable revisions. Recovery uses the
// current manifest's read grants for the original, still valid model bindings;
// this does not make that old model ready for any new transaction.
func authorizeTransactionRecovery(ctx context.Context, app core.App, workspace string, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, result transactionResult) error {
	var preview transactionPreview
	found, err := loadTransactionEvent(ctx, app, workspace, input, result.RequestID, "preview", &preview)
	if err != nil {
		return err
	}
	if !found || preview.WorkspaceID != workspace || preview.InstallationID != input.InstallationID || installed.Manifest.Permissions.Model == nil || !pluginmodel.Equal(preview.Model.Model, *installed.Manifest.Permissions.Model) || !reflect.DeepEqual(installed.Manifest.Permissions.Transactions, &preview.Declaration) {
		return pluginDataError("plugin_read_denied")
	}
	states, err := pluginmodel.States(ctx, app, input.ProjectKey, input.PluginID)
	if err != nil {
		return err
	}
	compatible := false
	for i := len(states) - 1; i >= 0; i-- {
		state := states[i]
		if state.Status != "completed" || state.InstallationID != input.InstallationID || !pluginmodel.Equal(state.Model, *installed.Manifest.Permissions.Model) {
			continue
		}
		if !reflect.DeepEqual(state.Bindings, preview.Model.Bindings) {
			return pluginDataError("plugin_read_denied")
		}
		if err := modelValidateBindings(ctx, app, state); err != nil {
			return err
		}
		for _, entity := range state.Model.Entities {
			binding := state.Bindings[entity.Key]
			installed.ModelGrants = append(installed.ModelGrants, pluginReadGrant{Collection: binding.TableID, Fields: []string{"id"}, Operations: entity.Operations})
		}
		compatible = true
		break
	}
	if !compatible {
		return pluginDataError("plugin_read_denied")
	}
	for _, row := range result.Receipt.Rows {
		if grant := installed.grant(row.TableID, input.ActiveCollection); grant == nil || !pluginHas(grant.Fields, "id") && !pluginHas(grant.Fields, "*") {
			return pluginDataError("plugin_read_denied")
		}
	}
	return nil
}

func transactionModel(ctx context.Context, app core.App, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation) (pluginmodel.State, error) {
	d := installed.Manifest.Permissions.Transactions
	m := installed.Manifest.Permissions.Model
	if d == nil || d.Validate() != nil || m == nil || d.ModelID != m.ModelID {
		return pluginmodel.State{}, pluginDataError("transaction.declaration_invalid")
	}
	states, err := pluginmodel.States(ctx, app, input.ProjectKey, input.PluginID)
	if err != nil {
		return pluginmodel.State{}, err
	}
	for i := len(states) - 1; i >= 0; i-- {
		s := states[i]
		if s.Status == "completed" && s.InstallationID == input.InstallationID && s.InstallationRevision == input.InstallationRevision && pluginmodel.Equal(s.Model, *m) {
			if err := modelValidateBindings(ctx, app, s); err != nil {
				return s, err
			}
			for _, binding := range s.Bindings {
				table, err := schemaexecution.Describe(ctx, app, binding.TableID)
				if err != nil {
					return s, err
				}
				s.Revisions[binding.TableID] = table.Snapshot.SchemaRevision
			}
			return s, nil
		}
	}
	return pluginmodel.State{}, pluginDataError("transaction.model_required")
}

func executeSurfaceTransaction(ctx context.Context, app core.App, kernel *mutation.Kernel, workspace string, input pluginSurfaceAPIRequest, gate businessWriteGate, ports *pluginSurfaceIO) (output any, returnedErr error) {
	defer func() {
		var mutationErr *mutation.ProductError
		var queryErr *query.ProductError
		if returnedErr != nil && !errors.As(returnedErr, &mutationErr) && !errors.As(returnedErr, &queryErr) && strings.HasPrefix(returnedErr.Error(), "transaction.") {
			returnedErr = pluginDataError(returnedErr.Error())
		}
	}()
	if len(input.Request) > maxMutationRequestBytes {
		return nil, pluginDataError("transaction.budget_exceeded")
	}
	if ports == nil || ports.transactionEpoch == nil || ports.transactionEpoch() == 0 {
		return nil, pluginDataError("transaction.authority_unavailable")
	}
	installed, err := surfaceInstallation(ctx, app, workspace, input)
	if err != nil {
		return nil, err
	}
	if d := installed.Manifest.Permissions.Transactions; d == nil || d.Validate() != nil || installed.Manifest.Permissions.Model == nil || d.ModelID != installed.Manifest.Permissions.Model.ModelID {
		return nil, pluginDataError("transaction.declaration_invalid")
	}
	declaration := *installed.Manifest.Permissions.Transactions
	if input.Method == "transaction.configure" {
		model, err := transactionModel(ctx, app, input, installed)
		if err != nil {
			return nil, err
		}
		var request struct {
			Declaration transactionrules.Declaration `json:"declaration"`
		}
		if decodePluginParams(input.Request, &request) != nil || !reflect.DeepEqual(request.Declaration, declaration) || installed.ActionRisk == "read" {
			return nil, pluginDataError("transaction.configuration_denied")
		}
		registration := transactionrules.Registration{WorkspaceID: workspace, PluginID: input.PluginID, InstallationID: input.InstallationID, Declaration: declaration, Bindings: model.Bindings}
		id := fmt.Sprintf("%s:%s:%d", input.InstallationID, declaration.ModelID, declaration.Version)
		err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.transaction.configure", input.RequestID, func(ctx context.Context) error {
			return app.RunInTransaction(func(tx core.App) error {
				current, err := surfaceInstallation(ctx, tx, workspace, input)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(current.Manifest.Permissions.Transactions, &declaration) {
					return pluginDataError("transaction.plan_stale")
				}
				currentModel, err := transactionModel(ctx, tx, input, current)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(currentModel.Bindings, model.Bindings) {
					return pluginDataError("transaction.plan_stale")
				}
				var previous transactionrules.Registration
				found, err := loadTransactionEvent(ctx, tx, workspace, input, id, "rules", &previous)
				if err != nil {
					return err
				}
				if found {
					if !reflect.DeepEqual(previous, registration) {
						return pluginDataError("transaction.configuration_conflict")
					}
					return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
				}
				registered, err := transactionrules.Load(ctx, tx)
				if err != nil {
					return err
				}
				if len(registered) >= transactionrules.MaxRules {
					return pluginDataError("transaction.rule_budget_exceeded")
				}
				images, err := registration.Read(ctx, tx)
				if err != nil {
					return err
				}
				if err := registration.Validate(nil, images); err != nil {
					return err
				}
				_, err = pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, modelAudit(input, installed, "transaction:rules:"+id, "transaction.rules", registration))
				if err != nil {
					return err
				}
				return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
			})
		})
		return map[string]any{"contract": transactionrules.Contract, "status": "configured", "modelId": declaration.ModelID, "version": declaration.Version}, err
	}
	var request struct {
		Refresh         bool             `json:"refresh,omitempty"`
		PreviewRevision int              `json:"previewRevision,omitempty"`
		RequestID       string           `json:"requestId,omitempty"`
		Plan            *transactionPlan `json:"plan,omitempty"`
	}
	if mutation.DecodeStrict(input.Request, &request) != nil || len(request.RequestID) > 128 {
		return nil, pluginDataError("transaction.request_invalid")
	}
	if request.Refresh && (input.Method != "transaction.preview" || request.RequestID == "") || input.Method == "transaction.commit" && request.PreviewRevision < 1 {
		return nil, pluginDataError("transaction.request_invalid")
	}
	if request.Plan != nil {
		for i := range request.Plan.Operations {
			op := &request.Plan.Operations[i]
			for _, decimal := range declaration.Decimals {
				if decimal.Entity != op.Entity {
					continue
				}
				value, supplied := op.Values[decimal.Field]
				if !supplied {
					continue
				}
				coefficient, err := decimal.Format.Parse(value)
				if err != nil {
					return nil, pluginDataError("transaction.decimal_invalid")
				}
				if decimal.Storage == "decimalText" {
					canonical, err := decimal.Format.Text(coefficient)
					if err != nil {
						return nil, err
					}
					op.Values[decimal.Field] = canonical
				} else {
					op.Values[decimal.Field] = json.Number(coefficient.String())
				}
			}
		}
	}
	if input.Method != "transaction.preview" && (request.RequestID == "" || request.Plan != nil) {
		return nil, pluginDataError("transaction.request_invalid")
	}
	if request.RequestID != "" {
		var result transactionResult
		found, err := loadTransactionEvent(ctx, app, workspace, input, request.RequestID, "result", &result)
		if err != nil {
			return nil, err
		}
		if found {
			if !validTransactionResult(result, workspace, input.InstallationID, request.RequestID) {
				return transactionUnknown(request.RequestID), nil
			}
			if request.Plan != nil {
				var original transactionPreview
				found, err := loadTransactionEvent(ctx, app, workspace, input, request.RequestID, "preview", &original)
				if err != nil {
					return nil, err
				}
				if !found || !reflect.DeepEqual(original.Plan, *request.Plan) {
					return nil, pluginDataError("transaction.input_conflict")
				}
			}
			return result, nil
		}
	}
	if input.Method == "transaction.preview" && request.Plan == nil {
		return nil, pluginDataError("transaction.request_invalid")
	}
	model, err := transactionModel(ctx, app, input, installed)
	if err != nil {
		return nil, err
	}
	var preview transactionPreview
	if request.RequestID != "" {
		found, err := loadTransactionEvent(ctx, app, workspace, input, request.RequestID, "preview", &preview)
		if err != nil {
			return nil, err
		}
		if !found {
			return transactionUnknown(request.RequestID), nil
		}
		if preview.WorkspaceID != workspace || preview.InstallationID != input.InstallationID || preview.Epoch != ports.transactionEpoch() {
			return transactionUnknown(request.RequestID), nil
		}
		if request.Plan != nil && !reflect.DeepEqual(preview.Plan, *request.Plan) {
			return nil, pluginDataError("transaction.input_conflict")
		}
		if preview.InstallationRevision != input.InstallationRevision || !reflect.DeepEqual(preview.Declaration, declaration) {
			return nil, pluginDataError("transaction.plan_stale")
		}
		if input.Method == "transaction.preview" || input.Method == "transaction.status" {
			if !request.Refresh {
				return preview, nil
			}
		}
		if input.Method == "transaction.commit" && request.PreviewRevision != preview.PreviewRevision {
			return nil, pluginDataError("transaction.plan_stale")
		}
	} else {
		if input.Method != "transaction.preview" || installed.ActionRisk == "read" {
			return nil, pluginDataError("plugin_write_denied")
		}
		preview = transactionPreview{Contract: transactionrules.Contract, WorkspaceID: workspace, InstallationID: input.InstallationID, InstallationRevision: input.InstallationRevision, RequestID: security.RandomString(24), Epoch: ports.transactionEpoch(), Status: "prepared", Plan: *request.Plan, Model: model, Declaration: declaration, NewIDs: map[string]map[string]string{}}
		if err := prepareTransactionPlan(ctx, app, input, installed, &preview); err != nil {
			return nil, err
		}
	}
	if input.Method == "transaction.preview" {
		preview.Model = model
		requests, err := transactionRequests(ctx, app, input, installed, preview)
		if err != nil {
			return nil, err
		}
		inspection, err := kernel.InspectPlan(ctx, requests, transactionAuthorize(input, workspace))
		if err != nil {
			return nil, err
		}
		preview.Effects, preview.DataRevisions = inspection.Effects, inspection.BaseRevisions
		if preview.PreviewRevision >= 1<<31-1 {
			return nil, pluginDataError("transaction.budget_exceeded")
		}
		preview.PreviewRevision++
		if raw, err := json.Marshal(preview); err != nil || len(raw) > maxMutationRequestBytes {
			return nil, pluginDataError("transaction.budget_exceeded")
		}
		previewEventID := "transaction:preview:" + preview.RequestID + ":" + security.RandomString(12)
		err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.transaction.preview", previewEventID, func(ctx context.Context) error {
			return app.RunInTransaction(func(tx core.App) error {
				if _, err := surfaceInstallation(ctx, tx, workspace, input); err != nil {
					return err
				}
				if preview.Epoch != ports.transactionEpoch() {
					return pluginDataError("transaction.plan_stale")
				}
				var result transactionResult
				if found, err := loadTransactionEvent(ctx, tx, workspace, input, preview.RequestID, "result", &result); err != nil {
					return err
				} else if found {
					return pluginDataError("transaction.plan_stale")
				}
				var previous transactionPreview
				if found, err := loadTransactionEvent(ctx, tx, workspace, input, preview.RequestID, "preview", &previous); err != nil {
					return err
				} else if found && previous.PreviewRevision != preview.PreviewRevision-1 {
					return pluginDataError("transaction.plan_stale")
				}
				_, err := pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, modelAudit(input, installed, previewEventID, "transaction.preview", preview))
				if err != nil {
					return err
				}
				return writecoordinator.PersistPocketBaseReceipt(ctx, tx, time.Now().UTC())
			})
		})
		return preview, err
	}
	if input.Method != "transaction.commit" || installed.ActionRisk == "read" {
		return nil, pluginDataError("plugin_write_denied")
	}
	requests, err := transactionRequests(ctx, app, input, installed, preview)
	if err != nil {
		return nil, err
	}
	var result transactionResult
	for i := range requests {
		requests[i].Authorize = func(ctx context.Context, tx core.App) error {
			current, err := surfaceInstallation(ctx, tx, workspace, input)
			if err != nil {
				return err
			}
			currentModel, err := transactionModel(ctx, tx, input, current)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(currentModel.Bindings, preview.Model.Bindings) {
				return pluginDataError("transaction.plan_stale")
			}
			if preview.Epoch != ports.transactionEpoch() {
				return pluginDataError("transaction.unknown")
			}
			found, err := loadTransactionEvent(ctx, tx, workspace, input, preview.RequestID, "result", &result)
			if err != nil {
				return err
			}
			if found {
				if !validTransactionResult(result, workspace, input.InstallationID, preview.RequestID) {
					return pluginDataError("transaction.unknown")
				}
				if replay := writecoordinator.ReplayedBusinessWrite(ctx, "plugin.transaction.commit", preview.RequestID); replay != nil {
					return replay
				}
				return errTransactionReplay
			}
			return nil
		}
	}
	err = runBusinessWrite(ctx, []businessWriteGate{gate}, "plugin.transaction.commit", preview.RequestID, func(ctx context.Context) error {
		_, err := kernel.ApplyPlan(ctx, requests, transactionAuthorize(input, workspace), func(tx core.App, receipt mutation.PlanReceipt) error {
			var current transactionPreview
			found, err := loadTransactionEvent(ctx, tx, workspace, input, preview.RequestID, "preview", &current)
			if err != nil {
				return err
			}
			if !found || current.PreviewRevision != preview.PreviewRevision || !reflect.DeepEqual(receipt.Effects, preview.Effects) || !reflect.DeepEqual(receipt.BaseRevisions, preview.DataRevisions) {
				return pluginDataError("transaction.plan_stale")
			}
			receipt.Effects, receipt.BaseRevisions = nil, nil
			result = transactionResult{Contract: transactionrules.Contract, WorkspaceID: workspace, InstallationID: input.InstallationID, RequestID: preview.RequestID, Status: "applied", NewIDs: preview.NewIDs, Receipt: receipt}
			raw, err := json.Marshal(result)
			if err != nil || len(raw) > maxMutationRequestBytes {
				return pluginDataError("transaction.result_budget_exceeded")
			}
			_, err = pluginstore.New(tx, workspace).RecordAuditInTransaction(ctx, modelAudit(input, installed, "transaction:result:"+preview.RequestID, "transaction.result", result))
			return err
		})
		return err
	})
	if errors.Is(err, errTransactionReplay) {
		return result, nil
	}
	if err != nil {
		// Acknowledgement and notification failures cannot erase an already committed result.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, authErr := surfaceInstallation(recovery, app, workspace, input); authErr == nil {
			var persisted transactionResult
			if found, readErr := loadTransactionEvent(recovery, app, workspace, input, preview.RequestID, "result", &persisted); readErr == nil && found && validTransactionResult(persisted, workspace, input.InstallationID, preview.RequestID) {
				return persisted, nil
			}
		}
		return nil, err
	}
	return result, nil
}

func prepareTransactionPlan(ctx context.Context, app core.App, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, p *transactionPreview) error {
	if p.Plan.Contract != "vibetable.transaction-plan.v1" || p.Plan.ModelID != p.Model.Model.ModelID || len(p.Plan.Operations) == 0 || len(p.Plan.Operations) > 1000 {
		return pluginDataError("transaction.plan_invalid")
	}
	seen := map[string]bool{}
	for _, op := range p.Plan.Operations {
		if op.Entity == "" || p.Model.Bindings[op.Entity].TableID == "" {
			return pluginDataError("transaction.binding_invalid")
		}
		switch op.Kind {
		case "create":
			if op.TempID == "" || len(op.TempID) > 64 || op.RecordID != "" || op.ExpectedDigest != nil || op.Values == nil {
				return pluginDataError("transaction.operation_invalid")
			}
			if p.NewIDs[op.Entity] == nil {
				p.NewIDs[op.Entity] = map[string]string{}
			}
			if p.NewIDs[op.Entity][op.TempID] != "" {
				return pluginDataError("transaction.temp_duplicate")
			}
			p.NewIDs[op.Entity][op.TempID] = security.RandomStringWithAlphabet(15, "abcdefghijklmnopqrstuvwxyz0123456789")
		case "update", "delete", "archive", "restore":
			if op.RecordID == "" || op.TempID != "" || op.ExpectedDigest == nil || op.Kind == "update" && op.Values == nil || op.Kind != "update" && len(op.Values) != 0 {
				return pluginDataError("transaction.operation_invalid")
			}
			key := op.Entity + "\x00" + op.RecordID
			if seen[key] {
				return pluginDataError("transaction.duplicate_row")
			}
			seen[key] = true
		default:
			return pluginDataError("transaction.operation_invalid")
		}
	}
	_, err := transactionRequests(ctx, app, input, installed, *p)
	return err
}

func transactionRequests(ctx context.Context, app core.App, input pluginSurfaceAPIRequest, installed pluginSurfaceInstallation, p transactionPreview) ([]mutation.Request, error) {
	registrations, err := transactionrules.Load(ctx, app)
	if err != nil {
		return nil, err
	}
	configured := false
	for _, r := range registrations {
		if r.WorkspaceID == p.WorkspaceID && r.InstallationID == p.InstallationID && reflect.DeepEqual(r.Declaration, p.Declaration) && reflect.DeepEqual(r.Bindings, p.Model.Bindings) {
			configured = true
		}
	}
	if !configured {
		return nil, pluginDataError("transaction.configuration_required")
	}
	byTable := map[string]*mutation.Request{}
	tables := []string{}
	for _, op := range p.Plan.Operations {
		binding := p.Model.Bindings[op.Entity]
		table, err := schemaexecution.Describe(ctx, app, binding.TableID)
		if err != nil {
			return nil, err
		}
		if table.Snapshot.SchemaRevision != p.Model.Revisions[binding.TableID] {
			return nil, pluginDataError("transaction.plan_stale")
		}
		request := byTable[binding.TableID]
		if request == nil {
			request = &mutation.Request{ContractVersion: mutation.ContractVersion, RequestID: p.RequestID, IdempotencyKey: p.RequestID, TableID: binding.TableID, SchemaRevision: table.Snapshot.SchemaRevision, Actor: mutation.Actor{Type: "plugin", ID: input.PluginID}}
			byTable[binding.TableID] = request
			tables = append(tables, binding.TableID)
		}
		kind := mutation.OperationKind(op.Kind)
		id := op.RecordID
		operationName := op.Kind
		if kind == "create" {
			kind = mutation.OperationInsert
			id = p.NewIDs[op.Entity][op.TempID]
		}
		if op.Kind == "archive" || op.Kind == "restore" {
			operationName = "update"
		}
		grant := installed.grantFor(binding.TableID, input.ActiveCollection, operationName)
		if grant == nil {
			return nil, pluginDataError("plugin_write_denied")
		}
		allowed := pluginAllowed(*grant, table.Snapshot)
		// Derived reciprocal writes have the same current field permissions as explicit writes.
		for _, field := range table.Snapshot.Fields {
			if field.Relation == nil || field.Relation.ReciprocalFieldID == "" {
				continue
			}
			_, supplied := op.Values[logicalFieldName(binding, field.Identity.FieldID)]
			if !supplied && op.Kind != "delete" {
				continue
			}
			target, err := schemaexecution.Describe(ctx, app, field.Relation.TargetTableID)
			if err != nil {
				return nil, err
			}
			targetGrant := installed.grantFor(field.Relation.TargetTableID, input.ActiveCollection, "update")
			if targetGrant == nil || pluginAllowed(*targetGrant, target.Snapshot)[field.Relation.ReciprocalFieldID] == "" {
				return nil, pluginDataError("plugin_write_denied")
			}
		}
		var values map[string]any
		if kind == mutation.OperationInsert || kind == mutation.OperationUpdate {
			values = map[string]any{}
		}
		for name, value := range op.Values {
			fieldID := binding.Fields[name]
			field, ok := table.Field(fieldID)
			if !ok || fieldID == "" || allowed[fieldID] == "" {
				return nil, pluginDataError("plugin_write_denied")
			}
			if field.Relation != nil {
				var convert func(any) (any, error)
				convert = func(v any) (any, error) {
					if ref, ok := v.(map[string]any); ok {
						if len(ref) != 2 {
							return nil, pluginDataError("transaction.reference_invalid")
						}
						entity, eok := ref["entity"].(string)
						temp, tok := ref["tempId"].(string)
						real := p.NewIDs[entity][temp]
						if !eok || !tok || real == "" || p.Model.Bindings[entity].TableID != field.Relation.TargetTableID {
							return nil, pluginDataError("transaction.reference_invalid")
						}
						return real, nil
					}
					if list, ok := v.([]any); ok {
						out := make([]any, len(list))
						for i, item := range list {
							converted, err := convert(item)
							if err != nil {
								return nil, err
							}
							out[i] = converted
						}
						return out, nil
					}
					if _, ok := v.(string); ok || v == nil {
						return v, nil
					}
					return nil, pluginDataError("transaction.reference_invalid")
				}
				value, err = convert(value)
				if err != nil {
					return nil, err
				}
				if installed.grant(field.Relation.TargetTableID, input.ActiveCollection) == nil {
					return nil, pluginDataError("plugin_write_denied")
				}
			}
			values[fieldID] = value
		}
		request.Operations = append(request.Operations, mutation.Operation{Kind: kind, RecordID: &id, Values: values, ExpectedDigest: op.ExpectedDigest})
	}
	sort.Strings(tables)
	result := []mutation.Request{}
	for _, table := range tables {
		result = append(result, *byTable[table])
	}
	return result, nil
}

func logicalFieldName(binding pluginmodel.Binding, fieldID string) string {
	for logical, physical := range binding.Fields {
		if physical == fieldID {
			return logical
		}
	}
	return ""
}
