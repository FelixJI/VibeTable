// Package transactionrules validates persisted model rules at the authoritative write boundary.
package transactionrules

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/productrow"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

const Contract = "vibetable.plugin-transactions.v1"
const MaxRows = 10000
const MaxBytes = 1 << 20
const MaxRules = 64

type Decimal struct {
	Entity string `json:"entity"`
	Field  string `json:"field"`
	exactdecimal.Format
}

// Rule is a closed union; Validate rejects fields belonging to a different rule kind.
type Rule struct {
	ID           string              `json:"id"`
	Kind         string              `json:"kind"`
	Entity       string              `json:"entity"`
	Fields       []string            `json:"fields,omitempty"`
	Field        string              `json:"field,omitempty"`
	Target       string              `json:"target,omitempty"`
	Relation     string              `json:"relation,omitempty"`
	Amount       string              `json:"amount,omitempty"`
	Limit        string              `json:"limit,omitempty"`
	Transitions  map[string][]string `json:"transitions,omitempty"`
	FrozenStates []string            `json:"frozenStates,omitempty"`
}

type Declaration struct {
	Contract string    `json:"contract"`
	ModelID  string    `json:"modelId"`
	Version  int       `json:"version"`
	Decimals []Decimal `json:"decimals"`
	Rules    []Rule    `json:"rules"`
}

type Registration struct {
	WorkspaceID    string                         `json:"workspaceId"`
	PluginID       string                         `json:"pluginId"`
	InstallationID string                         `json:"installationId"`
	Declaration    Declaration                    `json:"declaration"`
	Bindings       map[string]pluginmodel.Binding `json:"bindings"`
}

func failure(code string) error { return fmt.Errorf("transaction.%s", code) }

func Decode(raw []byte) (Declaration, error) {
	var declaration Declaration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > MaxBytes || decoder.Decode(&declaration) != nil {
		return declaration, failure("rules_invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return declaration, failure("rules_invalid")
	}
	return declaration, declaration.Validate()
}

func (d Declaration) Validate() error {
	if d.Contract != Contract || d.ModelID == "" || d.Version < 1 || len(d.Rules) == 0 || len(d.Rules) > MaxRules || len(d.Decimals) > MaxRules {
		return failure("rules_invalid")
	}
	decimals := map[string]bool{}
	for _, f := range d.Decimals {
		key := f.Entity + "\x00" + f.Field
		if f.Entity == "" || f.Field == "" || f.Format.Validate() != nil || decimals[key] {
			return failure("decimal_invalid")
		}
		decimals[key] = true
	}
	ids := map[string]bool{}
	for _, r := range d.Rules {
		if r.ID == "" || len(r.ID) > 64 || ids[r.ID] || r.Entity == "" {
			return failure("rules_invalid")
		}
		ids[r.ID] = true
		valid := false
		switch r.Kind {
		case "unique":
			valid = len(r.Fields) > 0 && len(r.Fields) <= 8 && r.Field == "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0
		case "reference":
			valid = r.Field != "" && r.Target != "" && len(r.Fields) == 0 && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0
		case "conservation", "balance":
			valid = r.Target != "" && r.Relation != "" && r.Amount != "" && r.Limit != "" && r.Field == "" && len(r.Fields) == 0 && len(r.Transitions) == 0 && len(r.FrozenStates) == 0 && decimals[r.Entity+"\x00"+r.Amount] && decimals[r.Target+"\x00"+r.Limit]
		case "state":
			valid = r.Field != "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) > 0 && len(r.Transitions) <= 32 && len(r.Fields) <= 64 && len(r.FrozenStates) <= 32
		}
		if !valid {
			return failure("rules_invalid")
		}
		seen := map[string]bool{}
		for _, f := range r.Fields {
			if f == "" || seen[f] {
				return failure("rules_invalid")
			}
			seen[f] = true
		}
		for state, next := range r.Transitions {
			if state == "" || len(next) > 32 {
				return failure("rules_invalid")
			}
			for _, s := range next {
				if s == "" {
					return failure("rules_invalid")
				}
			}
		}
	}
	return nil
}

type Images map[string]map[string]map[string]any

func (r Registration) dependencies() map[string]map[string]bool {
	result := map[string]map[string]bool{}
	add := func(entity, field string) {
		if result[entity] == nil {
			result[entity] = map[string]bool{}
		}
		if field != "" {
			result[entity][field] = true
		}
	}
	for _, d := range r.Declaration.Decimals {
		add(d.Entity, d.Field)
	}
	for _, rule := range r.Declaration.Rules {
		add(rule.Entity, rule.Field)
		for _, f := range rule.Fields {
			add(rule.Entity, f)
		}
		if rule.Target != "" {
			add(rule.Target, rule.Limit)
		}
		add(rule.Entity, rule.Relation)
		add(rule.Entity, rule.Amount)
	}
	return result
}

// Read projects only rule dependencies; absent and null values remain distinct from zero.
func (r Registration) Read(ctx context.Context, app core.App) (Images, error) {
	if r.WorkspaceID == "" || r.PluginID == "" || r.InstallationID == "" || r.Declaration.Validate() != nil {
		return nil, failure("rules_corrupt")
	}
	images := Images{}
	count, bytes := 0, 0
	for entity, fields := range r.dependencies() {
		binding, ok := r.Bindings[entity]
		if !ok || binding.TableID == "" {
			return nil, failure("binding_stale")
		}
		table, err := schemaexecution.Describe(ctx, app, binding.TableID)
		if err != nil {
			return nil, err
		}
		selected := []v2.FieldDefinition{}
		for field := range fields {
			definition, ok := table.Field(binding.Fields[field])
			if !ok || definition.Lifecycle.State != v2.LifecycleActive || definition.LogicalType == v2.LogicalFormula || definition.LogicalType == v2.LogicalLookup {
				return nil, failure("binding_stale")
			}
			selected = append(selected, definition)
		}
		for _, decimal := range r.Declaration.Decimals {
			if decimal.Entity != entity {
				continue
			}
			field, _ := table.Field(binding.Fields[decimal.Field])
			if decimal.Storage == "decimalText" && field.LogicalType != v2.LogicalText || decimal.Storage == "integerUnits" && (field.LogicalType != v2.LogicalNumber || !field.Storage.Options.OnlyInt) {
				return nil, failure("decimal_mapping_invalid")
			}
		}
		for _, rule := range r.Declaration.Rules {
			if rule.Entity != entity {
				continue
			}
			name := rule.Relation
			if rule.Kind == "reference" {
				name = rule.Field
			}
			if name != "" {
				field, _ := table.Field(binding.Fields[name])
				target := r.Bindings[rule.Target]
				if field.Relation == nil || field.Relation.TargetTableID != target.TableID || field.Relation.Cardinality != "one" {
					return nil, failure("reference_mapping_invalid")
				}
			}
		}
		collection, err := app.FindCollectionByNameOrId(table.PhysicalName)
		if err != nil {
			return nil, err
		}
		columns := map[string]bool{"id": true}
		for _, field := range selected {
			columns[field.Identity.PhysicalName] = true
			if name := field.Value.Presence.PhysicalName; name != "" {
				columns[name] = true
			}
		}
		names := make([]string, 0, len(columns))
		for name := range columns {
			names = append(names, name)
		}
		sort.Strings(names)
		sizes := make([]string, 0, len(names))
		for _, name := range names {
			sizes = append(sizes, "COALESCE(octet_length("+app.DB().QuoteSimpleColumnName(name)+"),0)")
		}
		sizeSQL := "(" + strings.Join(sizes, "+") + ")"
		selects := []string{sizeSQL + " AS [[__transaction_rule_bytes]]"}
		for _, name := range names {
			quoted := app.DB().QuoteSimpleColumnName(name)
			selects = append(selects, "CASE WHEN "+sizeSQL+" <= "+strconv.Itoa(MaxBytes-bytes)+" THEN "+quoted+" ELSE NULL END AS "+quoted)
		}
		// octet_length inspects stored byte lengths; oversized cells never cross
		// the driver boundary. Rows are consumed one at a time, not materialized.
		rows, err := app.RecordQuery(collection).Select(selects...).OrderBy("id").Limit(int64(MaxRows - count + 1)).WithContext(ctx).Rows()
		if err != nil {
			return nil, err
		}
		images[entity] = map[string]map[string]any{}
		readErr := func() error {
			defer rows.Close()
			for rows.Next() {
				if err := ctx.Err(); err != nil {
					return err
				}
				data := dbx.NullStringMap{}
				if err := rows.ScanMap(data); err != nil {
					return err
				}
				rawSize, err := strconv.Atoi(data["__transaction_rule_bytes"].String)
				if err != nil {
					return err
				}
				count++
				if count > MaxRows || rawSize > MaxBytes-bytes {
					return failure("read_budget_exceeded")
				}
				row := core.NewRecord(collection)
				for _, name := range names {
					field := collection.Fields.GetByName(name)
					if field == nil {
						return failure("binding_stale")
					}
					var value any
					if stored := data[name]; stored.Valid {
						value = stored.String
					}
					decoded, err := field.PrepareValue(row, value)
					if err != nil {
						return err
					}
					row.SetRaw(name, decoded)
				}
				physical := productrow.Project(selected, row)
				values := map[string]any{}
				for field := range fields {
					definition, _ := table.Field(binding.Fields[field])
					values[field] = physical[definition.Identity.PhysicalName]
				}
				raw, err := json.Marshal(values)
				if err != nil {
					return err
				}
				bytes += len(raw) + len(row.Id)
				if bytes > MaxBytes {
					return failure("read_budget_exceeded")
				}
				images[entity][row.Id] = values
			}
			return rows.Err()
		}()
		if readErr != nil {
			return nil, readErr
		}
	}
	return images, nil
}

func (r Registration) Validate(before, after Images) error {
	formats := map[string]exactdecimal.Format{}
	for _, d := range r.Declaration.Decimals {
		formats[d.Entity+"\x00"+d.Field] = d.Format
		for _, row := range after[d.Entity] {
			n, err := d.Parse(row[d.Field])
			if err != nil {
				return err
			}
			if d.Storage == "decimalText" {
				text, _ := d.Text(n)
				if row[d.Field] != text {
					return failure("decimal_noncanonical")
				}
			}
		}
	}
	for _, rule := range r.Declaration.Rules {
		rows := after[rule.Entity]
		switch rule.Kind {
		case "unique":
			seen := map[string]bool{}
			for _, row := range rows {
				values := make([]any, len(rule.Fields))
				for i, field := range rule.Fields {
					values[i] = row[field]
					if values[i] == nil {
						return failure("unique_null")
					}
				}
				raw, err := json.Marshal(values)
				if err != nil {
					return err
				}
				if seen[string(raw)] {
					return failure("unique_violation")
				}
				seen[string(raw)] = true
			}
		case "reference":
			for _, row := range rows {
				id, ok := row[rule.Field].(string)
				if !ok || id == "" || after[rule.Target][id] == nil {
					return failure("reference_violation")
				}
			}
		case "conservation", "balance":
			format := formats[rule.Entity+"\x00"+rule.Amount]
			targetFormat := formats[rule.Target+"\x00"+rule.Limit]
			if format.Scale != targetFormat.Scale {
				return failure("decimal_scale_mismatch")
			}
			totals := map[string]*big.Int{}
			ids := make([]string, 0, len(rows))
			for id := range rows {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				row := rows[id]
				target, ok := row[rule.Relation].(string)
				if !ok || after[rule.Target][target] == nil {
					return failure("reference_violation")
				}
				amount, err := format.Parse(row[rule.Amount])
				if err != nil {
					return err
				}
				if totals[target] == nil {
					totals[target] = new(big.Int)
				}
				if err := targetFormat.Add(totals[target], amount); err != nil {
					return err
				}
			}
			for id, row := range after[rule.Target] {
				limit, err := targetFormat.Parse(row[rule.Limit])
				if err != nil {
					return err
				}
				total := totals[id]
				if total == nil {
					total = new(big.Int)
				}
				comparison := total.Cmp(limit)
				if rule.Kind == "conservation" && comparison != 0 || rule.Kind == "balance" && comparison > 0 {
					return failure(rule.Kind + "_violation")
				}
			}
		case "state":
			for id, old := range before[rule.Entity] {
				state, ok := old[rule.Field].(string)
				if !ok {
					return failure("state_invalid")
				}
				next := rows[id]
				if next == nil {
					if contains(rule.FrozenStates, state) {
						return failure("state_frozen")
					}
					continue
				}
				newState, ok := next[rule.Field].(string)
				if !ok {
					return failure("state_invalid")
				}
				if newState != state && !contains(rule.Transitions[state], newState) {
					return failure("state_transition_denied")
				}
				if contains(rule.FrozenStates, state) {
					for _, field := range rule.Fields {
						if !reflect.DeepEqual(old[field], next[field]) {
							return failure("state_frozen")
						}
					}
				}
			}
			for _, row := range rows {
				state, ok := row[rule.Field].(string)
				if !ok {
					return failure("state_invalid")
				}
				if _, known := rule.Transitions[state]; !known {
					return failure("state_invalid")
				}
			}
		}
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

// Load reads immutable platform events; disabling or removing an installation never removes protection.
func Load(ctx context.Context, app core.App) ([]Registration, error) {
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rows, err := app.FindRecordsByFilter("vibetable_plugin_records", "kind='audit' && payload_json.eventType='transaction.rules'", "seq", MaxRules+1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxRules {
		return nil, failure("rule_budget_exceeded")
	}
	result := []Registration{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(row.GetRaw("payload_json"))
		if err != nil {
			return nil, err
		}
		var event struct {
			Details Registration `json:"details"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if event.Details.Declaration.Validate() != nil {
			return nil, failure("rules_corrupt")
		}
		result = append(result, event.Details)
	}
	return result, nil
}

type Guard struct {
	Registration Registration
	Before       Images
}

// IntegerInputs is read before normalization can turn an inexact wire value into a float.
func IntegerInputs(ctx context.Context, app core.App, table string) (map[string][]exactdecimal.Format, error) {
	registrations, err := Load(ctx, app)
	if err != nil {
		return nil, err
	}
	formats := map[string][]exactdecimal.Format{}
	for _, registration := range registrations {
		for _, decimal := range registration.Declaration.Decimals {
			binding := registration.Bindings[decimal.Entity]
			if binding.TableID == table && decimal.Storage == "integerUnits" {
				field := binding.Fields[decimal.Field]
				if field == "" {
					return nil, failure("binding_stale")
				}
				formats[field] = append(formats[field], decimal.Format)
			}
		}
	}
	return formats, nil
}

// Capture selects by actual canonical writes, including reciprocal targets, never a caller's rule list.
func Capture(ctx context.Context, app core.App, touched map[string]map[string]bool) ([]Guard, error) {
	registrations, err := Load(ctx, app)
	if err != nil {
		return nil, err
	}
	guards := []Guard{}
	rowsRead, bytesRead := 0, 0
	for _, r := range registrations {
		selected := false
		for entity, fields := range r.dependencies() {
			b := r.Bindings[entity]
			writes := touched[b.TableID]
			if writes["*"] {
				selected = true
			}
			for field := range fields {
				if writes[b.Fields[field]] {
					selected = true
				}
			}
		}
		if !selected {
			continue
		}
		before, err := r.Read(ctx, app)
		if err != nil {
			return nil, err
		}
		if err := countImages(before, &rowsRead, &bytesRead); err != nil {
			return nil, err
		}
		guards = append(guards, Guard{r, before})
	}
	return guards, nil
}

func ValidateGuards(ctx context.Context, app core.App, guards []Guard) error {
	rowsRead, bytesRead := 0, 0
	for _, guard := range guards {
		if err := countImages(guard.Before, &rowsRead, &bytesRead); err != nil {
			return err
		}
	}
	for _, guard := range guards {
		after, err := guard.Registration.Read(ctx, app)
		if err != nil {
			return err
		}
		if err := countImages(after, &rowsRead, &bytesRead); err != nil {
			return err
		}
		if err := guard.Registration.Validate(guard.Before, after); err != nil {
			return err
		}
	}
	return nil
}

func countImages(images Images, rows, size *int) error {
	for _, records := range images {
		*rows += len(records)
	}
	raw, err := json.Marshal(images)
	if err != nil {
		return err
	}
	*size += len(raw)
	if *rows > MaxRows || *size > MaxBytes {
		return failure("read_budget_exceeded")
	}
	return nil
}

func ProtectReplacement(ctx context.Context, app core.App, tables map[string]bool) error {
	registrations, err := Load(ctx, app)
	if err != nil {
		return err
	}
	for _, r := range registrations {
		for entity := range r.dependencies() {
			if tables[r.Bindings[entity].TableID] {
				return failure("protected_replacement_unsupported")
			}
		}
	}
	return nil
}
