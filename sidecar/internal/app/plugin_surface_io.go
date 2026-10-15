package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	"github.com/vibetable/vibetable/sidecar/internal/importvalue"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

type pluginSurfaceIO struct {
	stages      surfaceAttachmentStages
	plans       *importPlanOwner
	attachments *attachments.Manager
}

func surfaceIOOwner(input pluginSurfaceAPIRequest) string {
	return strings.Join([]string{input.InstallationID, input.PluginID, input.PackageHash, input.ActionID, input.Entry, input.ProjectKey, input.SurfaceToken, strconv.FormatInt(input.InstallationRevision, 10), strconv.FormatUint(input.SessionEpoch, 10)}, "\x00")
}

func surfaceIOGrant(installed pluginSurfaceInstallation, input pluginSurfaceAPIRequest, table, operation string) *pluginReadGrant {
	for _, grant := range installed.Manifest.Permissions.Data {
		if (grant.Collection == table || grant.Collection == "$active" && table == input.ActiveCollection) && pluginHas(grant.Operations, operation) {
			return &grant
		}
	}
	return nil
}

func surfaceImportGrant(installed pluginSurfaceInstallation, input pluginSurfaceAPIRequest, table, mode string) *pluginReadGrant {
	create := surfaceIOGrant(installed, input, table, "create")
	if mode != "upsert" {
		return create
	}
	update := surfaceIOGrant(installed, input, table, "update")
	if create == nil {
		return update
	}
	if update == nil {
		return create
	}
	merged := *create
	merged.Fields = append(append([]string{}, create.Fields...), update.Fields...)
	return &merged
}

func (ports *pluginSurfaceIO) cleanupImport(input pluginSurfaceAPIRequest) (any, error) {
	if ports == nil || ports.plans == nil {
		return nil, pluginDataError("plugin_api_unsupported")
	}
	if input.Method == "import.drop" {
		var request struct {
			GrantID string `json:"grantId"`
		}
		if decodePluginParams(input.Request, &request) != nil || request.GrantID == "" {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		ports.plans.mu.Lock()
		defer ports.plans.mu.Unlock()
		for token, stored := range ports.plans.plans {
			if stored.pluginOwner == surfaceIOOwner(input) && stored.grantID == request.GrantID && stored.idempotencyPrefix == "" {
				delete(ports.plans.plans, token)
			}
		}
		return map[string]any{"dropped": true}, nil
	}
	var request importPlanSettleRequest
	if decodePluginParams(input.Request, &request) != nil {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	ports.plans.mu.Lock()
	stored := ports.plans.plans[request.Token]
	owned := stored != nil && stored.pluginOwner == surfaceIOOwner(input)
	ports.plans.mu.Unlock()
	if !owned {
		return nil, pluginDataError("plugin_plan_stale")
	}
	if request.Outcome == "unknown" {
		ports.plans.mu.Lock()
		defer ports.plans.mu.Unlock()
		stored := ports.plans.plans[request.Token]
		if stored == nil {
			return nil, pluginDataError("plugin_plan_stale")
		}
		if request.Contract != importPlanContract || request.Attempt < 1 || request.Attempt != stored.attempt || stored.idempotencyPrefix == "" {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		return importPlanTokenReply{Token: request.Token, ExpiresAt: float64(stored.expiresAt.UnixNano()) / 1e9, Consumed: stored.consumed}, nil
	}
	reply, err := ports.plans.settle(request)
	if err == nil && request.Outcome != "unknown" {
		ports.plans.mu.Lock()
		delete(ports.plans.plans, request.Token)
		ports.plans.mu.Unlock()
	}
	return reply, err
}

func executeSurfaceIO(ctx context.Context, app core.App, source *queryschema.Source, kernel *mutation.Kernel,
	workspace string, input pluginSurfaceAPIRequest, ports *pluginSurfaceIO) (any, error) {
	if input.SurfaceToken == "" {
		return nil, pluginDataError("plugin_source_denied")
	}
	if ports == nil {
		return nil, pluginDataError("plugin_api_unsupported")
	}
	var result any
	var prepared *mutation.Request
	var pendingMint *importPlanMintRequest
	var pendingPlan json.RawMessage
	err := app.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, workspace, input)
		if err != nil {
			return err
		}
		switch input.Method {
		case "data.ioRelations", "data.ioTargetSchema":
			var request struct {
				Collection string `json:"collection"`
			}
			if decodePluginParams(input.Request, &request) != nil {
				return pluginDataError("plugin_capability_invalid")
			}
			grant := installed.grant(request.Collection, input.ActiveCollection)
			if input.Method == "data.ioRelations" {
				grant = surfaceImportGrant(installed, input, request.Collection, "upsert")
			}
			if grant == nil {
				return pluginDataError("plugin_read_denied")
			}
			table, err := schemaexecution.Describe(ctx, tx, request.Collection)
			if err != nil {
				return err
			}
			allowed := pluginAllowed(*grant, table.Snapshot)
			items := []any{}
			for _, field := range table.Snapshot.Fields {
				id := field.Identity.FieldID
				if allowed[id] == "" {
					continue
				}
				if input.Method == "data.ioTargetSchema" {
					items = append(items, map[string]any{"identity": map[string]any{"fieldId": id, "physicalName": id}, "constraints": field.Constraints})
					continue
				}
				if field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) != nil {
					items = append(items, map[string]any{"relationId": request.Collection + "." + id, "sourceTableId": request.Collection, "physicalName": id, "cardinality": field.Relation.Cardinality, "targetTableId": field.Relation.TargetTableID})
				}
			}
			if input.Method == "data.ioRelations" {
				result = map[string]any{"relations": items}
			} else {
				result = map[string]any{"fields": items}
			}
			return nil
		case "data.ioProfile":
			var request struct {
				Collection string   `json:"collection"`
				Operation  string   `json:"operation"`
				Mode       string   `json:"mode,omitempty"`
				Fields     []string `json:"fields"`
			}
			if decodePluginParams(input.Request, &request) != nil || !pluginHas([]string{"read", "create", "update"}, request.Operation) {
				return pluginDataError("plugin_capability_invalid")
			}
			grant := surfaceIOGrant(installed, input, request.Collection, request.Operation)
			if request.Operation == "create" {
				grant = surfaceImportGrant(installed, input, request.Collection, request.Mode)
			}
			if grant == nil || request.Operation == "read" && installed.grant(request.Collection, input.ActiveCollection) == nil {
				return pluginDataError("plugin_read_denied")
			}
			table, err := schemaexecution.Describe(ctx, tx, request.Collection)
			if err != nil {
				return pluginDataError("plugin_read_denied")
			}
			allowed := pluginAllowed(*grant, table.Snapshot)
			fields := []string{"id"}
			schemas := map[string]any{}
			writable := []string{}
			relations := []any{}
			for _, field := range table.Snapshot.Fields {
				id := field.Identity.FieldID
				if allowed[id] == "" {
					continue
				}
				fields = append(fields, id)
				schemas[id] = map[string]any{"dataType": field.LogicalType, "displayName": field.DisplayName}
				if field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) != nil {
					kind := "m2o"
					if field.Relation.Cardinality != "one" {
						kind = "m2m"
					}
					relations = append(relations, map[string]any{"relation_id": request.Collection + "." + id, "field": id, "kind": kind, "related_collection": field.Relation.TargetTableID, "display_fields": []string{}})
				}
				if !describeFieldReadonly(table.Snapshot, field) {
					writable = append(writable, id)
				}
			}
			if request.Operation == "read" {
				if len(request.Fields) == 0 || len(request.Fields) > 256 {
					return pluginDataError("plugin_capability_invalid")
				}
				seen := map[string]bool{}
				for _, id := range request.Fields {
					if seen[id] {
						return pluginDataError("plugin_capability_invalid")
					}
					seen[id] = true
					if allowed[id] == "" {
						return pluginDataError("plugin_read_denied")
					}
				}
			}
			// id is internal profile bookkeeping only. ExportService receives an explicit output column list.
			result = map[string]any{"collection": request.Collection, "schema_revision": table.Snapshot.SchemaRevision, "fields": fields, "field_schemas": schemas, "relations": relations, "create_fields": writable, "update_fields": writable, "archive_field": nil, "date_updated_field": nil}
			return nil
		case "import.preview":
			var request struct {
				Collection     string           `json:"collection"`
				SchemaRevision string           `json:"schemaRevision"`
				Rows           []map[string]any `json:"rows"`
				Mode           string           `json:"mode"`
			}
			if decodePluginParams(input.Request, &request) != nil || len(request.Rows) > 1000 || len(request.Rows) == 0 || !pluginHas([]string{"create_only", "upsert"}, request.Mode) {
				return pluginDataError("plugin_capability_invalid")
			}
			grant := surfaceImportGrant(installed, input, request.Collection, request.Mode)
			if grant == nil || installed.ActionRisk == "read" {
				return pluginDataError("plugin_write_denied")
			}
			table, err := schemaexecution.Describe(ctx, tx, request.Collection)
			if err != nil {
				return err
			}
			allowed := pluginAllowed(*grant, table.Snapshot)
			preview := importvalue.Request{Contract: importvalue.Contract, TableID: request.Collection, SchemaRevision: request.SchemaRevision, Rows: []importvalue.Row{}}
			for _, row := range request.Rows {
				for key := range row {
					field, ok := table.Field(key)
					if !ok || allowed[field.Identity.FieldID] == "" || field.Relation != nil && installed.grant(field.Relation.TargetTableID, input.ActiveCollection) == nil {
						return pluginDataError("plugin_write_denied")
					}
				}
				mode := fieldvalue.Insert
				if request.Mode == "upsert" {
					mode = fieldvalue.Update
				}
				preview.Rows = append(preview.Rows, importvalue.Row{Values: row, Mode: mode})
			}
			normalized, err := importvalue.New(fieldchange.NewCatalog(tx)).Preview(ctx, preview)
			if err != nil {
				return err
			}
			for _, row := range normalized.Rows {
				for _, diagnostic := range row.Diagnostics {
					if allowed[diagnostic.Field] == "" {
						if field, ok := table.Field(diagnostic.Field); !ok || allowed[field.Identity.FieldID] == "" {
							return pluginDataError("plugin_write_denied")
						}
					}
				}
			}
			result = normalized
			return nil
		case "import.mint":
			if ports.plans == nil {
				return pluginDataError("plugin_api_unsupported")
			}
			var request importPlanMintRequest
			if decodePluginParams(input.Request, &request) != nil || len(request.Rows) == 0 || len(request.Rows) > 1000 || !pluginHas([]string{"create_only", "upsert"}, request.Mode) {
				return pluginDataError("plugin_capability_invalid")
			}
			table, err := schemaexecution.Describe(ctx, tx, request.Collection)
			if err != nil || table.Snapshot.SchemaRevision != request.SchemaRevision {
				return pluginDataError("plugin_cursor_stale")
			}
			if len(input.Request) > 768*1024 {
				return pluginDataError("plugin_plan_limit")
			}
			invalid := false
			for _, raw := range request.Rows {
				var row struct {
					Values      map[string]any    `json:"values"`
					Diagnostics []json.RawMessage `json:"diagnostics"`
				}
				if json.Unmarshal(raw, &row) != nil {
					return pluginDataError("plugin_capability_invalid")
				}
				if len(row.Diagnostics) > 0 {
					invalid = true
				}
			}
			if invalid {
				grant := surfaceImportGrant(installed, input, request.Collection, request.Mode)
				if grant == nil || installed.ActionRisk == "read" {
					return pluginDataError("plugin_write_denied")
				}
				allowed := pluginAllowed(*grant, table.Snapshot)
				for _, raw := range request.Rows {
					var row struct {
						Values map[string]any `json:"values"`
					}
					_ = json.Unmarshal(raw, &row)
					for key := range row.Values {
						field, ok := table.Field(key)
						if !ok || allowed[field.Identity.FieldID] == "" {
							return pluginDataError("plugin_write_denied")
						}
					}
				}
				request.pluginOwner = surfaceIOOwner(input)
				if err := ctx.Err(); err != nil {
					return err
				}
				minted, err := ports.plans.mint(request)
				result = minted
				return err
			}

			operations := []map[string]any{}
			seen := map[string]bool{}
			for _, raw := range request.Rows {
				var row struct {
					SourceRow           int               `json:"sourceRow"`
					Values              map[string]any    `json:"values"`
					Diagnostics         []json.RawMessage `json:"diagnostics"`
					RelationResolutions []json.RawMessage `json:"relationResolutions"`
				}
				if decodePluginParams(raw, &row) != nil || len(row.Diagnostics) > 0 {
					return pluginDataError("plugin_capability_invalid")
				}
				operation := map[string]any{"kind": "create", "values": row.Values}
				if request.Mode == "upsert" {
					if request.UpsertKey == nil || installed.grant(request.Collection, input.ActiveCollection) == nil {
						return pluginDataError("plugin_write_denied")
					}
					field, ok := table.Field(*request.UpsertKey)
					readAllowed := pluginAllowed(*installed.grant(request.Collection, input.ActiveCollection), table.Snapshot)
					if !ok || !field.Constraints.Unique.Enabled || readAllowed[field.Identity.FieldID] == "" || readAllowed["id"] == "" {
						return pluginDataError("plugin_write_denied")
					}
					value, present := row.Values[*request.UpsertKey]
					if !present || value == nil {
						return pluginDataError("plugin_capability_invalid")
					}
					page, err := query.NewPort(tx, source).QueryPage(ctx, request.Collection, query.TableQuery{Limit: 2, Filters: []query.FilterExpression{{Field: field.Identity.PhysicalName, Operator: query.OperatorEqual, Value: value}}})
					if err != nil {
						return err
					}
					if len(page.Rows) > 1 {
						return pluginDataError("plugin_capability_invalid")
					}
					if len(page.Rows) == 1 {
						id, ok := page.Rows[0]["id"].(string)
						digest, _ := page.Rows[0][productrow.DigestField].(string)
						if !ok || digest == "" || seen[id] {
							return pluginDataError("plugin_capability_invalid")
						}
						seen[id] = true
						operation["kind"] = "update"
						operation["primaryKey"] = id
						operation["expectedDigest"] = digest
					}
				}
				operations = append(operations, operation)
			}
			plan := map[string]any{"contract": "vibetable.surface-mutation-plan.v1", "collection": request.Collection, "schemaRevision": request.SchemaRevision, "operations": operations}
			frozen, _ := json.Marshal(plan)
			checked := input
			checked.Request = frozen
			authorization, err := surfaceMutationRequest(ctx, tx, workspace, checked)
			if err != nil {
				return err
			}
			if err = authorization.Authorize(ctx, tx); err != nil {
				return err
			}
			// Matching freezes ids in this read transaction; the configured canonical kernel
			// validates that exact guarded plan in its own authoritative preview transaction.
			prepared = &authorization
			request.pluginOwner = surfaceIOOwner(input)
			pendingMint = &request
			pendingPlan = frozen
			return nil

		case "import.freeze":
			if ports.plans == nil {
				return pluginDataError("plugin_api_unsupported")
			}
			var request struct {
				Token      string `json:"token"`
				GrantID    string `json:"grantId"`
				Collection string `json:"collection"`
			}
			if decodePluginParams(input.Request, &request) != nil {
				return pluginDataError("plugin_capability_invalid")
			}
			ports.plans.mu.Lock()
			stored := ports.plans.plans[request.Token]
			if stored == nil || stored.pluginOwner != surfaceIOOwner(input) || stored.grantID != request.GrantID || stored.collection != request.Collection || stored.pluginPlan == nil {
				ports.plans.mu.Unlock()
				return pluginDataError("plugin_plan_stale")
			}
			stage := importPlanStageRequest{Contract: importPlanContract, Token: request.Token, GrantID: request.GrantID, Collection: request.Collection, Mode: stored.mode, CapabilityHash: stored.capabilityHash}
			frozen := append(json.RawMessage(nil), stored.pluginPlan...)
			ports.plans.mu.Unlock()
			claim, err := ports.plans.stage(stage)
			if err != nil {
				return err
			}
			result = map[string]any{"plan": frozen, "attempt": claim.Attempt}
			return nil
		case "import.bind":
			if ports.plans == nil {
				return pluginDataError("plugin_api_unsupported")
			}
			var request importPlanBindRequest
			if decodePluginParams(input.Request, &request) != nil {
				return pluginDataError("plugin_capability_invalid")
			}
			ports.plans.mu.Lock()
			stored := ports.plans.plans[request.Token]
			owned := stored != nil && stored.pluginOwner == surfaceIOOwner(input)
			ports.plans.mu.Unlock()
			if !owned {
				return pluginDataError("plugin_plan_stale")
			}
			key, err := ports.plans.bind(request)
			result = map[string]any{"idempotencyKey": key}
			return err
		default:
			return pluginDataError("plugin_api_unsupported")
		}
	})
	if err != nil || prepared == nil {
		return result, err
	}
	if _, err = kernel.Preview(ctx, *prepared); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	minted, err := ports.plans.mint(*pendingMint)
	if err != nil {
		return nil, err
	}
	ports.plans.mu.Lock()
	stored := ports.plans.plans[minted.Token]
	if stored == nil {
		ports.plans.mu.Unlock()
		return nil, pluginDataError("plugin_plan_stale")
	}
	stored.pluginPlan = pendingPlan
	ports.plans.mu.Unlock()
	return minted, nil
}
