package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// RelationLabelsField is display-only metadata, outside the f_* product namespace.
// It must never be copied into a mutation's business values.
const RelationLabelsField = "__vibetableRelationLabels"
const visibleRelationTargets = 3

type relationLabelGroup struct {
	relation *RelationDescriptor
	fields   []string
	ids      map[string]struct{}
}

func projectRelationLabels(ctx context.Context, app core.App, descriptor TableDescriptor, rows []map[string]any) error {
	groups := make(map[[2]string]*relationLabelGroup)
	for _, name := range sortedFieldNames(descriptor.Fields) {
		relation := descriptor.Fields[name].Relation
		if relation == nil || relation.DisplayField == "" {
			continue
		}
		// Empty maps deliberately replace labels from a previous display definition.
		for _, row := range rows {
			labels, ok := row[RelationLabelsField].(map[string]map[string]string)
			if !ok {
				labels = make(map[string]map[string]string)
				row[RelationLabelsField] = labels
			}
			labels[name] = make(map[string]string)
		}
		field, ok := relation.Fields[relation.DisplayField]
		if !ok || field.Relation != nil || field.Type == FieldTypeRelation || field.Type == FieldTypeMultiRelation {
			continue
		}
		key := [2]string{relation.TableName, relation.DisplayField}
		group := groups[key]
		if group == nil {
			group = &relationLabelGroup{relation: relation, ids: make(map[string]struct{})}
			groups[key] = group
		}
		group.fields = append(group.fields, name)
		for _, row := range rows {
			for _, id := range relationDisplayIDs(row[name]) {
				group.ids[id] = struct{}{}
			}
		}
	}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		relation := group.relation
		fields := map[string]FieldDescriptor{
			relation.PrimaryKey:   {PhysicalName: relation.PrimaryKey, Type: FieldTypeText},
			relation.DisplayField: relation.Fields[relation.DisplayField],
		}
		target := TableDescriptor{TableID: relation.TableName, PhysicalName: relation.TableName, PrimaryKey: relation.PrimaryKey, Fields: fields, RowRevisionName: relation.RowRevisionName}
		if presence := relation.PresenceFields[relation.DisplayField]; presence != "" {
			target.PresenceFields = map[string]string{relation.DisplayField: presence}
		}
		ids := make([]string, 0, len(group.ids))
		for id := range group.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		labels := make(map[string]string, len(ids))
		for start := 0; start < len(ids); start += maxInValues {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(start+maxInValues, len(ids))
			values := make([]any, end-start)
			for index, id := range ids[start:end] {
				values[index] = id
			}
			plan, err := Compile(target, TableQuery{Filters: []FilterExpression{{Field: relation.PrimaryKey, Operator: OperatorIn, Value: values}}, Limit: len(values)})
			if err != nil {
				return err
			}
			targets, err := readQueryRows(ctx, app, plan.SQL, plan.Params, target)
			if err != nil {
				return err
			}
			for _, row := range targets {
				if label := relationDisplayLabel(row[relation.DisplayField]); label != "" {
					labels[fmt.Sprint(row[relation.PrimaryKey])] = label
				}
			}
		}
		for _, row := range rows {
			metadata := row[RelationLabelsField].(map[string]map[string]string)
			for _, name := range group.fields {
				for _, id := range relationDisplayIDs(row[name]) {
					if label := labels[id]; label != "" {
						metadata[name][id] = label
					}
				}
			}
		}
	}
	return nil
}

func relationDisplayIDs(value any) []string {
	var values []any
	switch typed := value.(type) {
	case string:
		values = []any{typed}
	case []any:
		values = typed
	default:
		return nil
	}
	ids := make([]string, 0, visibleRelationTargets)
	for _, value := range values[:min(len(values), visibleRelationTargets)] {
		if id, ok := value.(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func relationDisplayLabel(value any) string {
	// The Grid only displays scalar values. Never stringify stale calculation
	// envelopes, JSON objects/arrays, or follow another relation to invent a label.
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			return typed
		}
	case bool, int64, float64:
		return fmt.Sprint(typed)
	}
	return ""
}
