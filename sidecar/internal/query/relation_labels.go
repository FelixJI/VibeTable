package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// RelationLabelsField is display-only metadata, outside the f_* product namespace.
// It must never be copied into a mutation's business values. Each label entry
// carries the raw typed scalar (numbers stay numbers, 0 and false are valid)
// resolved through the frozen chain "relation display value -> target global
// primary display value"; clients render it through the shared display
// contract and fall back to the record ID when an entry is absent.
const RelationLabelsField = "__vibetableRelationLabels"
const visibleRelationTargets = 3

// Controlled label sources: the relation's configured display field, or the
// target table's global primary display field used as fallback.
const (
	relationLabelSourceDisplay = "display"
	relationLabelSourcePrimary = "primary"
)

type relationLabelGroup struct {
	relation      *RelationDescriptor
	displayField  string
	displaySource string
	fallbackField string
	fields        []string
	ids           map[string]struct{}
}

// RelationLabelEntry is one typed label projection entry: the raw scalar
// value (numbers stay numbers; 0 and false are valid) plus the controlled
// source that produced it, so clients format each label with the DisplaySpec
// of the field it actually came from — never a second numeric authority.
type RelationLabelEntry struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

func projectRelationLabels(ctx context.Context, app core.App, descriptor TableDescriptor, rows []map[string]any) error {
	groups := make(map[[2]string]*relationLabelGroup)
	for _, name := range sortedFieldNames(descriptor.Fields) {
		relation := descriptor.Fields[name].Relation
		if relation == nil {
			continue
		}
		// Empty maps deliberately replace labels from a previous display definition.
		for _, row := range rows {
			labels, ok := row[RelationLabelsField].(map[string]map[string]RelationLabelEntry)
			if !ok {
				labels = make(map[string]map[string]RelationLabelEntry)
				row[RelationLabelsField] = labels
			}
			labels[name] = make(map[string]RelationLabelEntry)
		}
		display := relationLabelSource(relation)
		if display == "" {
			continue
		}
		key := [2]string{relation.TableName, display}
		group := groups[key]
		if group == nil {
			source := relationLabelSourceDisplay
			if display != relation.DisplayField {
				// The configured display field is unavailable; the label is the
				// target's global primary display value and formats with that
				// field's contract.
				source = relationLabelSourcePrimary
			}
			group = &relationLabelGroup{
				relation: relation, displayField: display, displaySource: source,
				fallbackField: relationLabelFallback(relation, display),
				ids:           make(map[string]struct{}),
			}
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
			relation.PrimaryKey: {PhysicalName: relation.PrimaryKey, Type: FieldTypeText},
			group.displayField:  relation.Fields[group.displayField],
		}
		if group.fallbackField != "" {
			fields[group.fallbackField] = relation.Fields[group.fallbackField]
		}
		target := TableDescriptor{TableID: relation.TableName, PhysicalName: relation.TableName, PrimaryKey: relation.PrimaryKey, Fields: fields, RowRevisionName: relation.RowRevisionName}
		for field, presence := range relation.PresenceFields {
			if field == group.displayField || field == group.fallbackField {
				if target.PresenceFields == nil {
					target.PresenceFields = map[string]string{}
				}
				target.PresenceFields[field] = presence
			}
		}
		ids := make([]string, 0, len(group.ids))
		for id := range group.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		labels := make(map[string]RelationLabelEntry, len(ids))
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
				if label, ok := relationLabelScalar(row[group.displayField]); ok {
					labels[fmt.Sprint(row[relation.PrimaryKey])] = RelationLabelEntry{Value: label, Source: group.displaySource}
					continue
				}
				// Empty relation display labels fall back to the target's valid
				// global primary display value (marked as the fallback source)
				// before the client resolves the record ID.
				if group.fallbackField != "" {
					if label, ok := relationLabelScalar(row[group.fallbackField]); ok {
						labels[fmt.Sprint(row[relation.PrimaryKey])] = RelationLabelEntry{Value: label, Source: relationLabelSourcePrimary}
					}
				}
			}
		}
		for _, row := range rows {
			metadata := row[RelationLabelsField].(map[string]map[string]RelationLabelEntry)
			for _, name := range group.fields {
				for _, id := range relationDisplayIDs(row[name]) {
					if label, ok := labels[id]; ok {
						metadata[name][id] = label
					}
				}
			}
		}
	}
	return nil
}

// relationLabelSource resolves the relation's own display field; a missing,
// retired or relation-typed display field degrades to the global primary
// display field of the target table.
func relationLabelSource(relation *RelationDescriptor) string {
	if field, ok := relation.Fields[relation.DisplayField]; ok &&
		field.Type != FieldTypeRelation && field.Type != FieldTypeMultiRelation &&
		field.Relation == nil {
		return relation.DisplayField
	}
	if relation.PrimaryDisplayField != "" {
		return relation.PrimaryDisplayField
	}
	return ""
}

func relationLabelFallback(relation *RelationDescriptor, display string) string {
	if relation.PrimaryDisplayField == "" || relation.PrimaryDisplayField == display {
		return ""
	}
	return relation.PrimaryDisplayField
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

// relationLabelScalar keeps the raw typed scalar for label rendering. Empty
// and whitespace-only text is missing; 0 and false are valid values. Stale
// calculation envelopes, JSON objects/arrays and nested relations never
// surface as labels; a fresh envelope only contributes its ready scalar.
func relationLabelScalar(value any) (any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case string:
		if strings.TrimSpace(typed) != "" {
			return typed, true
		}
		return nil, false
	case bool, int64, float64:
		return typed, true
	case map[string]any:
		if state, _ := typed["state"].(string); state == "ready" || state == "ok" {
			return relationLabelScalar(typed["value"])
		}
		return nil, false
	default:
		return nil, false
	}
}
