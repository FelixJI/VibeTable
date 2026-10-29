package app

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const pluginDatasetContract = "vibetable.plugin-dataset.v1"
const pluginDatasetBytes = 512 * 1024
const pluginDatasetRows = 2000

type pluginDatasetSum struct {
	Field string `json:"field"`
	exactdecimal.Format
}
type pluginDatasetSource struct {
	Query   pluginQueryRequest `json:"query"`
	GroupBy []string           `json:"groupBy"`
	Sums    []pluginDatasetSum `json:"sums"`
}
type pluginDatasetVersion struct {
	Schema   string                                   `json:"schemaRevision"`
	Data     int64                                    `json:"dataRevision"`
	Clock    string                                   `json:"clockPeriod"`
	Computed map[string]pluginDatasetComputedRevision `json:"computed"`
}
type pluginDatasetComputedRevision struct {
	Definition int    `json:"definition"`
	Watermark  string `json:"watermark"`
	Ready      bool   `json:"ready"`
	Status     string `json:"status"`
}
type pluginDatasetRequest struct {
	Contract string                          `json:"contract"`
	Sources  []pluginDatasetSource           `json:"sources"`
	Versions map[string]pluginDatasetVersion `json:"versions,omitempty"`
}

// This executes only inside executePluginData's single authority transaction.
func executePluginDataset(ctx context.Context, tx core.App, source *queryschema.Source, installed pluginInstallation, active, operation string, request pluginDatasetRequest) (map[string]any, error) {
	ctx = formula.EnsureEvaluationTime(ctx)
	if request.Contract != pluginDatasetContract || len(request.Sources) < 1 || len(request.Sources) > 3 || operation == "dataset.open" && request.Versions != nil || operation == "dataset.validate" && request.Versions == nil {
		return nil, pluginDataError("plugin_capability_invalid")
	}
	versions := map[string]pluginDatasetVersion{}
	outputs := []any{}
	remainingRows, remainingBytes := pluginDatasetRows, pluginDatasetBytes
	seen := map[string]bool{}
	for _, item := range request.Sources {
		r := item.Query
		if r.Contract != "vibetable.plugin-query.v2" || r.Collection == "" || seen[r.Collection] || len(r.Fields) < 1 || len(r.Fields) > 16 || r.PageSize != nil || len(item.GroupBy) > 2 || len(item.Sums) > 8 {
			return nil, pluginDataError("plugin_capability_invalid")
		}
		seen[r.Collection] = true
		grant := installed.grant(r.Collection, active)
		if grant == nil || pluginHas(grant.Fields, "*") || pluginHas(grant.Fields, "$configured") {
			return nil, pluginDataError("plugin_read_denied")
		}
		// Dataset bindings are explicit canonical fields: extensions never join implicitly.
		for _, id := range append(append([]string{}, item.GroupBy...), datasetSumFields(item.Sums)...) {
			if !pluginHas(r.Fields, id) {
				return nil, pluginDataError("plugin_read_denied")
			}
		}
		for _, sum := range item.Sums {
			if sum.Format.Validate() != nil {
				return nil, pluginDataError("plugin_dataset_decimal_invalid")
			}
		}
		scoped := &pluginQuerySource{source: source, grant: *grant, request: r, readonly: true}
		d, schema, err := scoped.DescribeSelectionTable(ctx, tx, r.Collection)
		if err != nil {
			return nil, err
		}
		for _, sum := range item.Sums {
			field := d.Fields[scoped.names[sum.Field]]
			if sum.Storage == "decimalText" && field.Type != query.FieldTypeText || sum.Storage == "integerUnits" && field.Type != query.FieldTypeNumber {
				return nil, pluginDataError("plugin_dataset_decimal_invalid")
			}
		}
		for _, id := range item.GroupBy {
			if d.Fields[scoped.names[id]].Type == query.FieldTypeJSON {
				return nil, pluginDataError("plugin_dataset_group_unsupported")
			}
		}
		table, err := schemaexecution.Describe(ctx, tx, r.Collection)
		if err != nil {
			return nil, err
		}
		checked := map[string]bool{}
		var authorize func(string) error
		authorize = func(id string) error {
			if id == "id" || checked[id] {
				return nil
			}
			checked[id] = true
			field, ok := table.Field(id)
			if !ok || scoped.names[field.Identity.FieldID] == "" {
				return pluginDataError("plugin_read_denied")
			}
			if field.Lookup != nil {
				return pluginDataError("plugin_dataset_computed_unsupported")
			}
			if field.Formula != nil {
				compiled, err := formula.NewCompiler(formula.Limits{}).Compile(table, field)
				if err != nil || len(compiled.CollectionDependencies) > 0 || len(compiled.ReferencePaths) > 0 || len(compiled.RelationAggregatePaths) > 0 || len(compiled.RelationCountNames) > 0 {
					return pluginDataError("plugin_dataset_computed_unsupported")
				}
				for _, dependency := range compiled.Dependencies {
					if err := authorize(dependency); err != nil {
						return err
					}
				}
			}
			if field.Relation != nil {
				target := field.Relation.TargetTableID
				targetGrant := installed.grant(target, active)
				if targetGrant == nil {
					return pluginDataError("plugin_read_denied")
				}
				t, err := schemaexecution.Describe(ctx, tx, target)
				if err != nil || pluginAllowed(*targetGrant, t.Snapshot)["id"] == "" {
					return pluginDataError("plugin_read_denied")
				}
				if _, exists := versions[target]; !exists {
					versions[target] = pluginDatasetVersion{Schema: t.Snapshot.SchemaRevision, Data: t.Snapshot.DataRevision}
				}
			}
			return nil
		}
		// scoped.computed also contains predicate dependencies; all receive closure checks.
		for name := range d.Fields {
			if name == d.ArchiveField {
				continue
			}
			if err := authorize(name); err != nil {
				return nil, err
			}
		}
		computedVersions := map[string]pluginDatasetComputedRevision{}
		for name, field := range scoped.computed {
			computedVersions[name] = pluginDatasetComputedRevision{field.ComputedDefinitionVersion, field.ComputedDependencyWatermark, field.ComputedReady, field.ComputedStatus}
		}
		versions[r.Collection] = pluginDatasetVersion{d.SchemaRevision, d.DataRevision, d.ClockPeriod, computedVersions}
		if operation == "dataset.validate" {
			continue
		}
		d.DigestFields, d.DigestProjector = nil, nil
		d.ProjectionFields = []string{"id"}
		for _, id := range r.Fields {
			if id != "id" {
				d.ProjectionFields = append(d.ProjectionFields, scoped.names[id])
			}
		}
		rows, count, total, err := query.ReadBounded(ctx, tx, d, scoped.normalized, remainingRows, &remainingBytes)
		if err != nil {
			return nil, err
		}
		remainingRows -= len(rows)
		items := []map[string]any{}
		complete := true
		for _, id := range r.Fields {
			if field := scoped.computed[scoped.names[id]]; field.ComputedEnvelope && !field.ComputedReady {
				complete = false
			}
		}
		for _, row := range rows {
			projected := map[string]any{}
			for _, id := range r.Fields {
				value := row[scoped.names[id]]
				if field := scoped.computed[scoped.names[id]]; field.ComputedEnvelope {
					state, fresh := "updating", false
					if envelope, ok := value.(map[string]any); ok {
						state, _ = envelope["state"].(string)
						fresh = field.ComputedReady && state == "ready"
						value = envelope["value"]
					}
					if !fresh {
						value = nil
						complete = false
					}
					value = map[string]any{"state": state, "fresh": fresh, "value": value}
				}
				projected[id] = value
			}
			items = append(items, projected)
		}
		summary, err := summarizePluginDataset(items, item)
		if err != nil {
			return nil, err
		}
		summaryComplete := complete
		for _, entry := range summary["sums"].([]any) {
			sum := entry.(map[string]any)
			if field := scoped.computed[scoped.names[sum["field"].(string)]]; field.ComputedEnvelope && !field.ComputedReady {
				sum["value"], sum["complete"] = nil, false
			}
			summaryComplete = summaryComplete && sum["complete"] == true
		}
		summary["complete"] = summaryComplete
		outputs = append(outputs, map[string]any{"collection": schema.TableID, "fields": r.Fields, "items": items, "filteredRows": count, "totalRows": total, "complete": complete, "summary": summary})
	}
	if operation == "dataset.validate" {
		if !reflect.DeepEqual(request.Versions, versions) {
			return nil, pluginDataError("plugin_dataset_stale")
		}
		return map[string]any{"valid": true}, nil
	}
	result := map[string]any{"contract": pluginDatasetContract, "sources": outputs, "versions": versions, "scope": request.Sources}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > pluginDatasetBytes {
		return nil, pluginDataError("plugin_dataset_limit")
	}
	return result, ctx.Err()
}

func datasetSumFields(sums []pluginDatasetSum) []string {
	fields := []string{}
	for _, sum := range sums {
		fields = append(fields, sum.Field)
	}
	return fields
}

func datasetCell(value any) (string, any) {
	if envelope, ok := value.(map[string]any); ok {
		if envelope["fresh"] != true {
			return "pending", nil
		}
		value = envelope["value"]
	}
	if value == nil {
		return "null", nil
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return "unallocated", value
	}
	return "value", value
}

func summarizePluginDataset(rows []map[string]any, source pluginDatasetSource) (map[string]any, error) {
	groupRows := map[string][]map[string]any{}
	groupKeys := map[string][]any{}
	order := []string{}
	for _, row := range rows {
		key := []any{}
		for _, field := range source.GroupBy {
			state, value := datasetCell(row[field])
			key = append(key, map[string]any{"state": state, "value": value})
		}
		raw, _ := json.Marshal(key)
		encoded := string(raw)
		if _, ok := groupRows[encoded]; !ok {
			order = append(order, encoded)
			groupKeys[encoded] = key
		}
		groupRows[encoded] = append(groupRows[encoded], row)
	}
	fold := func(items []map[string]any) ([]any, error) {
		values := []any{}
		for _, sum := range source.Sums {
			total := new(big.Int)
			nulls, pending, unknown, unallocated, zero := 0, 0, 0, 0, 0
			for _, row := range items {
				state, value := datasetCell(row[sum.Field])
				switch state {
				case "null":
					nulls++
					continue
				case "pending":
					pending++
					continue
				case "unallocated":
					unallocated++
					continue
				}
				n, err := sum.Format.Parse(value)
				if err != nil {
					unknown++
					continue
				}
				if n.Sign() == 0 {
					zero++
				}
				if sum.Format.Add(total, n) != nil {
					return nil, pluginDataError("plugin_dataset_decimal_overflow")
				}
			}
			text, _ := sum.Format.Text(total)
			var value any = text
			complete := pending == 0 && unknown == 0 && unallocated == 0
			if !complete {
				value = nil
			}
			values = append(values, map[string]any{"field": sum.Field, "scale": sum.Scale, "value": value, "complete": complete, "nullCount": nulls, "pendingCount": pending, "unknownCount": unknown, "unallocatedCount": unallocated, "zeroCount": zero})
		}
		return values, nil
	}
	totals, err := fold(rows)
	if err != nil {
		return nil, err
	}
	groups := []any{}
	for _, key := range order {
		sums, err := fold(groupRows[key])
		if err != nil {
			return nil, err
		}
		groups = append(groups, map[string]any{"key": groupKeys[key], "count": len(groupRows[key]), "sums": sums})
	}
	return map[string]any{"count": len(rows), "sums": totals, "groups": groups}, nil
}
