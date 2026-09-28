package lookup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	"github.com/vibetable/vibetable/sidecar/internal/query"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

func calculateConditionCells(
	ctx context.Context, app core.App, source schemaexecution.Table, records []*core.Record,
	fields []v2.FieldDefinition, offset, limit int, readBudget, valueBudget *materializationBudget,
) (map[string]map[string]CellValue, error) {
	result := make(map[string]map[string]CellValue, len(records))
	for _, record := range records {
		result[record.Id] = map[string]CellValue{}
	}
	for _, field := range fields {
		plan, err := queryschema.PrepareLookupCondition(ctx, app, source, *field.Lookup)
		if err != nil {
			return nil, err
		}
		// Equal operands share one match group. Each bounded batch reads the source
		// once, with QueryCompiler producing all typed predicates and match flags.
		groups := [][]query.FilterExpression{}
		rows := [][]string{}
		groupByKey := map[string]int{}
		for _, record := range records {
			filters := plan.Filters(record)
			encoded, err := json.Marshal(filters)
			if err != nil {
				return nil, err
			}
			key := string(encoded)
			index, exists := groupByKey[key]
			if !exists {
				index = len(groups)
				groupByKey[key] = index
				groups = append(groups, filters)
				rows = append(rows, nil)
			}
			rows[index] = append(rows[index], record.Id)
		}
		for start := 0; start < len(groups); start += 64 {
			end := min(start+64, len(groups))
			cells := make([]CellValue, end-start)
			seen := make([]map[string]bool, end-start)
			for i := range cells {
				cells[i] = CellValue{State: "ok", Value: []any{}, Provenance: []ValueProvenance{}, ProvenanceTotalKnown: true, ProvenanceOffset: offset, ProvenanceLimit: limit}
				seen[i] = map[string]bool{}
			}
			afterID := ""
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				compiled, err := query.CompileMatchBatch(plan.Descriptor, groups[start:end], afterID, lookupTraversalBatch)
				if err != nil {
					return nil, err
				}
				var matches []struct {
					ID      string `db:"id"`
					Matches string `db:"matches"`
				}
				if err = app.DB().NewQuery(compiled.SQL).WithContext(ctx).Bind(dbx.Params(compiled.Params)).All(&matches); err != nil {
					return nil, err
				}
				if len(matches) == 0 {
					break
				}
				ids := make([]string, len(matches))
				for i, row := range matches {
					ids[i] = row.ID
				}
				loaded, err := queryLookupRecords(ctx, app, plan.Target, ids)
				if err != nil {
					return nil, err
				}
				for _, row := range matches {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					record := loaded[row.ID]
					if record == nil {
						return nil, lookupError("lookup.value.source_missing", "matched source record disappeared")
					}
					if err := readBudget.consume(record.PublicExport()); err != nil {
						return nil, err
					}
					var flags []int
					if err := json.Unmarshal([]byte(row.Matches), &flags); err != nil {
						return nil, err
					}
					if len(flags) != len(cells) {
						return nil, fmt.Errorf("lookup match group count changed")
					}
					value := productrow.Project([]v2.FieldDefinition{plan.Field}, record)[plan.Field.Identity.PhysicalName]
					encoded, err := json.Marshal(value)
					if err != nil {
						return nil, err
					}
					_, provenance := resolvedValues([]lookupPathValue{describedLookupValue(plan.Target, record, plan.Field, value)})
					for i, flag := range flags {
						if flag == 0 {
							continue
						}
						cell := &cells[i]
						if !plan.Spec.Condition.Distinct || !seen[i][string(encoded)] {
							if err := valueBudget.consume(value); err != nil {
								return nil, err
							}
							cell.Value = append(cell.Value.([]any), value)
							seen[i][string(encoded)] = true
						}
						if cell.ProvenanceTotal >= offset && len(cell.Provenance) < limit {
							if err := valueBudget.consume(provenance[0]); err != nil {
								return nil, err
							}
							cell.Provenance = append(cell.Provenance, provenance[0])
						}
						cell.ProvenanceTotal++
					}
				}
				afterID = matches[len(matches)-1].ID
				if len(matches) < lookupTraversalBatch {
					break
				}
			}
			for i, cell := range cells {
				cell.ProvenanceHasMore = cell.ProvenanceTotal > offset+len(cell.Provenance)
				for copyIndex, id := range rows[start+i] {
					if copyIndex > 0 {
						if err := valueBudget.consume(cell.Value); err != nil {
							return nil, err
						}
						if err := valueBudget.consume(cell.Provenance); err != nil {
							return nil, err
						}
					}
					result[id][field.Identity.PhysicalName] = cell
				}
			}
		}
	}
	return result, nil
}
