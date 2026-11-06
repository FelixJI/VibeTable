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
const MaxRules = 192
const MaxRegistrations = 64
const MaxDecimals = 64
const MaxParentFreezeFields = 64
const MaxFrozenStates = 32

// These options distinguish an omitted policy from an invalid explicit value.
type decimalSign string

func (s *decimalSign) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) != nil || (value != "nonnegative" && value != "positive") {
		return failure("decimal_invalid")
	}
	*s = decimalSign(value)
	return nil
}

type enabledOption bool

func (flag *enabledOption) UnmarshalJSON(raw []byte) error {
	if string(raw) != "true" {
		return failure("rules_invalid")
	}
	*flag = true
	return nil
}

type Decimal struct {
	Entity   string `json:"entity"`
	Field    string `json:"field"`
	Nullable bool   `json:"nullable,omitempty"`
	exactdecimal.Format
	Sign decimalSign `json:"sign,omitempty"`
}

// Rule is a closed union; Validate rejects fields belonging to a different rule kind.
type Rule struct {
	PathMatch
	SkipEmptyLeftEnd enabledOption       `json:"skipEmptyLeftEnd,omitempty"`
	WhenPresent      string              `json:"whenPresent,omitempty"`
	CurrentWhen      *PathMatch          `json:"currentWhen,omitempty"`
	ID               string              `json:"id"`
	Kind             string              `json:"kind"`
	Entity           string              `json:"entity"`
	Fields           []string            `json:"fields,omitempty"`
	Field            string              `json:"field,omitempty"`
	Target           string              `json:"target,omitempty"`
	Relation         string              `json:"relation,omitempty"`
	Amount           string              `json:"amount,omitempty"`
	Limit            string              `json:"limit,omitempty"`
	Transitions      map[string][]string `json:"transitions,omitempty"`
	FrozenStates     []string            `json:"frozenStates,omitempty"`
	ParentPath       []RelationStep      `json:"parentPath,omitempty"`
	ParentStateField string              `json:"parentStateField,omitempty"`
	Reservation      *ReservationSpec    `json:"reservation,omitempty"`
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
	if d.Contract != Contract || d.ModelID == "" || d.Version < 1 || len(d.Rules) == 0 || len(d.Rules) > MaxRules || len(d.Decimals) > MaxDecimals {
		return failure("rules_invalid")
	}
	decimals := map[string]Decimal{}
	for _, f := range d.Decimals {
		key := f.Entity + "\x00" + f.Field
		if f.Entity == "" || f.Field == "" || f.Format.Validate() != nil || (f.Sign != "" && f.Sign != "nonnegative" && f.Sign != "positive") {
			return failure("decimal_invalid")
		}
		if _, duplicate := decimals[key]; duplicate {
			return failure("decimal_invalid")
		}
		decimals[key] = f
	}
	ids := map[string]bool{}
	for _, r := range d.Rules {
		if r.ID == "" || len(r.ID) > 64 || ids[r.ID] || r.Entity == "" {
			return failure("rules_invalid")
		}
		ids[r.ID] = true
		if (r.Left != nil || r.Right != nil || r.WhenPresent != "" || bool(r.SkipEmptyLeftEnd)) && r.Kind != "sameTarget" || r.CurrentWhen != nil && (r.Kind != "balance" || !r.CurrentWhen.valid("")) {
			return failure("rules_invalid")
		}
		valid := false
		switch r.Kind {
		case "sameTarget":
			valid = r.PathMatch.valid(r.Entity) && (r.WhenPresent == "" || r.WhenPresent == r.Left[0].Field) && len(r.Fields) == 0 && r.Field == "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0
		case "unique", "atMostOne":
			valid = (r.Kind != "atMostOne" || len(r.Fields) == 2) && len(r.Fields) > 0 && len(r.Fields) <= 8 && r.Field == "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0
		case "reference":
			valid = r.Field != "" && r.Target != "" && len(r.Fields) == 0 && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0
		case "conservation", "balance":
			amount, hasAmount := decimals[r.Entity+"\x00"+r.Amount]
			limit, hasLimit := decimals[r.Target+"\x00"+r.Limit]
			valid = r.Target != "" && r.Relation != "" && r.Amount != "" && r.Limit != "" && r.Field == "" && len(r.Fields) == 0 && len(r.Transitions) == 0 && len(r.FrozenStates) == 0 && hasAmount && !amount.Nullable && hasLimit && !limit.Nullable
		case "state":
			valid = r.Field != "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) > 0 && len(r.Transitions) <= 32 && len(r.Fields) <= 64 && len(r.FrozenStates) <= 32
		case "parentFreeze":
			valid = len(r.ParentPath) > 0 && len(r.ParentPath) <= 4 && r.ParentStateField != "" && len(r.FrozenStates) > 0 && len(r.FrozenStates) <= MaxFrozenStates && len(r.Fields) <= MaxParentFreezeFields && r.Field == "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && r.Left == nil && r.Right == nil && r.WhenPresent == "" && !bool(r.SkipEmptyLeftEnd) && r.CurrentWhen == nil && r.Reservation == nil
		case "reservationBalance":
			valid = r.Reservation != nil && r.Reservation.valid() && r.Field == "" && r.Target == "" && r.Relation == "" && r.Amount == "" && r.Limit == "" && len(r.Transitions) == 0 && len(r.FrozenStates) == 0 && len(r.Fields) == 0 && r.Left == nil && r.Right == nil && r.WhenPresent == "" && !bool(r.SkipEmptyLeftEnd) && r.CurrentWhen == nil && len(r.ParentPath) == 0 && r.ParentStateField == ""
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
	for _, r := range d.Rules {
		if r.Kind != "reservationBalance" || r.Reservation == nil {
			continue
		}
		res := r.Reservation
		keys := [][2]string{{r.Entity, res.Quantity}, {res.Obligation.Entity, res.Obligation.Quantity}}
		if res.Plans != nil {
			keys = append(keys, [2]string{res.Plans.Entity, res.Plans.Quantity})
		}
		first := exactdecimal.Format{}
		for i, key := range keys {
			declared, ok := decimals[key[0]+"\x00"+key[1]]
			// positive is a strictly stronger non-negative policy: formal plugins
			// keep their existing declarations instead of relaxing them.
			if !ok || declared.Nullable || (declared.Sign != "nonnegative" && declared.Sign != "positive") {
				return failure("rules_invalid")
			}
			if i == 0 {
				first = declared.Format
			} else if declared.Format != first {
				return failure("rules_invalid")
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
		rule.relationSteps(func(entity string, step RelationStep) {
			add(entity, step.Field)
			add(step.Target, "")
		})
		add(rule.Entity, rule.Amount)
		if rule.Kind == "parentFreeze" {
			parent := rule.Entity
			for _, step := range rule.ParentPath {
				parent = step.Target
			}
			add(parent, rule.ParentStateField)
		}
		if rule.Reservation != nil {
			res := rule.Reservation
			add(res.Obligation.Entity, res.Obligation.Quantity)
			add(rule.Entity, res.Quantity)
			purposeEntity := rule.Entity
			for _, step := range res.Purpose.Path {
				purposeEntity = step.Target
			}
			add(purposeEntity, res.Purpose.Field)
			add(rule.Entity, res.Scope)
			add(rule.Entity, res.PlanItem)
			add(rule.Entity, res.InstructionSource)
			add(rule.Entity, res.TransferredFrom)
			if res.Plans != nil {
				add(res.Plans.Entity, res.Plans.Quantity)
				add(res.Plans.Entity, res.Plans.Scope)
				add(res.Plans.Entity, res.Plans.LogicalItem.Field)
				add(res.Plans.Entity, res.Plans.Share)
				add(res.Plans.LogicalItem.Target, "")
			}
			entity := rule.Entity
			for _, step := range res.Phase.Path {
				entity = step.Target
			}
			add(entity, res.Phase.Field)
		}
	}
	return result
}

// ProtectsBinding reports whether a physical identity participates in this registration.
func (r Registration) ProtectsBinding(table, field string) bool {
	for entity, fields := range r.dependencies() {
		binding := r.Bindings[entity]
		if binding.TableID != table {
			continue
		}
		if field == "" {
			return true
		}
		for key := range fields {
			if binding.Fields[key] == field {
				return true
			}
		}
	}
	return false
}

// MatchesBindings compares only the canonical objects consulted during enforcement.
func (r Registration) MatchesBindings(bindings map[string]pluginmodel.Binding) bool {
	for entity, fields := range r.dependencies() {
		old, current := r.Bindings[entity], bindings[entity]
		if old.TableID == "" || old.TableID != current.TableID {
			return false
		}
		for field := range fields {
			if old.Fields[field] == "" || old.Fields[field] != current.Fields[field] {
				return false
			}
		}
	}
	return true
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
			if rule.Kind == "atMostOne" && rule.Entity == entity {
				for _, key := range rule.Fields {
					field, _ := table.Field(binding.Fields[key])
					if field.Relation == nil || field.Relation.Cardinality != "one" {
						return nil, failure("reference_mapping_invalid")
					}
				}
			}
			valid := true
			rule.relationSteps(func(source string, step RelationStep) {
				if source != entity {
					return
				}
				field, _ := table.Field(binding.Fields[step.Field])
				if field.Relation == nil || field.Relation.TargetTableID != r.Bindings[step.Target].TableID || field.Relation.Cardinality != "one" {
					valid = false
				}
			})
			if !valid {
				return nil, failure("reference_mapping_invalid")
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
			if d.Nullable && row[d.Field] == nil {
				continue
			}
			n, err := d.Parse(row[d.Field])
			if err != nil {
				return err
			}
			if d.Sign == "nonnegative" && n.Sign() < 0 || d.Sign == "positive" && n.Sign() <= 0 {
				return failure("decimal_sign_violation")
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
		case "sameTarget":
			for id, row := range rows {
				if rule.WhenPresent != "" {
					if value, present := row[rule.WhenPresent]; present && (value == nil || value == "") {
						continue
					}
				}
				match, err := rule.matches(after, row, id, pathEndPolicy{skipEmptyLeft: bool(rule.SkipEmptyLeftEnd)})
				if err != nil {
					return err
				}
				if !match {
					return failure("same_target_violation")
				}
			}
		case "atMostOne":
			for _, row := range rows {
				occupied := false
				for _, field := range rule.Fields {
					value, found := row[field]
					if !found {
						return failure("reference_violation")
					}
					if value == nil || value == "" {
						continue
					}
					if _, ok := value.(string); !ok {
						return failure("reference_violation")
					}
					if occupied {
						return failure("at_most_one_violation")
					}
					occupied = true
				}
			}
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
				if rule.CurrentWhen != nil {
					current, err := rule.CurrentWhen.matches(after, row, id, pathEndPolicy{allowEmptyRight: true})
					if err != nil {
						return err
					}
					if !current {
						continue
					}
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
		case "parentFreeze":
			if err := rule.validateParentFreeze(before, after); err != nil {
				return err
			}
		case "reservationBalance":
			if err := rule.validateReservation(before, after, formats); err != nil {
				return err
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

// ValidateAdmission checks a normal write on each affected table against the same
// effective registrations and combined before/after image budget used at commit.
func (candidate Registration) ValidateAdmission(ctx context.Context, app core.App, history []Registration) error {
	type trigger struct {
		writes WriteSet
		budget imageBudget
	}
	// Inserting or deleting a row also touches its reciprocal fields. A candidate
	// can therefore be selected by writes originating outside its dependency tables.
	sources := map[string]bool{}
	tables := map[string]schemaexecution.Table{}
	for entity, fields := range candidate.dependencies() {
		binding := candidate.Bindings[entity]
		table, err := schemaexecution.Describe(ctx, app, binding.TableID)
		if err != nil {
			return err
		}
		tables[binding.TableID] = table
		sources[binding.TableID] = true
		for key := range fields {
			field, ok := table.Field(binding.Fields[key])
			if !ok {
				return failure("binding_stale")
			}
			if rel := field.Relation; rel != nil && rel.ReciprocalFieldID != "" {
				sources[rel.TargetTableID] = true
			}
		}
	}
	var triggers []trigger
	for source := range sources {
		table, ok := tables[source]
		if !ok {
			var err error
			table, err = schemaexecution.Describe(ctx, app, source)
			if err != nil {
				return err
			}
		}
		writes := WriteSet{}
		writes.AddNative(table.Snapshot, true, nil, "")
		if candidate.selectedBy(writes) {
			triggers = append(triggers, trigger{writes: writes})
		}
	}
	for i, guard := range activeGuards(append(history, candidate)) {
		var affected []*imageBudget
		for j := range triggers {
			if guard.selectedBy(triggers[j].writes) {
				affected = append(affected, &triggers[j].budget)
			}
		}
		if len(affected) == 0 {
			continue
		}
		images, err := guard.Read(ctx, app)
		if err != nil {
			return err
		}
		if i == 0 { // activeGuards puts the newly appended registration first.
			if err := candidate.Validate(nil, images); err != nil {
				return err
			}
		}
		var image imageBudget
		if err := image.count(images); err != nil {
			return err
		}
		for _, budget := range affected {
			if err := budget.add(2*image.rows, 2*image.bytes); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load reads immutable platform events; disabling or removing an installation never removes protection.
func Load(ctx context.Context, app core.App) ([]Registration, error) {
	if _, err := app.FindCollectionByNameOrId("vibetable_plugin_records"); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rows, err := app.FindRecordsByFilter("vibetable_plugin_records", "kind='audit' && payload_json.eventType='transaction.rules'", "seq", MaxRegistrations+1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxRegistrations {
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

// Retains allows an identical declaration or a strictly newer version that keeps
// each previously approved rule and exact decimal mapping.
func (d Declaration) Retains(old Declaration) bool {
	if old.ModelID != d.ModelID || old.Contract != d.Contract || d.Version < old.Version {
		return false
	}
	if d.Version == old.Version {
		return reflect.DeepEqual(d, old)
	}
	rules := map[string]Rule{}
	for _, rule := range d.Rules {
		rules[rule.ID] = rule
	}
	for _, rule := range old.Rules {
		if !reflect.DeepEqual(rule, rules[rule.ID]) {
			return false
		}
	}
	decimals := map[Decimal]bool{}
	for _, decimal := range d.Decimals {
		decimals[decimal] = true
	}
	for _, decimal := range old.Decimals {
		if !decimals[decimal] {
			return false
		}
	}
	return true
}

// Cumulative upgrades keep their immutable registrations. Execute the superset once so
// previously readable workspaces do not spend the bounded image budget twice on the same rows.
func activeGuards(history []Registration) []Registration {
	active := make([]Registration, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		old := history[i]
		covered := false
		for _, next := range active {
			covered = old.WorkspaceID == next.WorkspaceID && old.PluginID == next.PluginID && old.InstallationID == next.InstallationID &&
				next.Declaration.Version > old.Declaration.Version && next.Declaration.Retains(old.Declaration) && old.MatchesBindings(next.Bindings)
			if covered {
				break
			}
		}
		if !covered {
			active = append(active, old)
		}
	}
	return active
}

type WriteSet map[string]map[string]bool

// AddNative includes the same single-table and reciprocal writes for native mutation
// and for admission's largest single-row operation (insert/delete).
func (w WriteSet) AddNative(table v2.SchemaSnapshot, whole bool, values map[string]any, attachment string) {
	mark := func(table, field string) {
		if w[table] == nil {
			w[table] = map[string]bool{}
		}
		w[table][field] = true
	}
	if whole {
		mark(table.TableID, "*")
	}
	if attachment != "" {
		mark(table.TableID, attachment)
	}
	for _, f := range table.Fields {
		_, set := values[f.Identity.PhysicalName]
		if set {
			mark(table.TableID, f.Identity.FieldID)
		}
		if r := f.Relation; r != nil && r.ReciprocalFieldID != "" && (whole || set) {
			mark(r.TargetTableID, r.ReciprocalFieldID)
		}
	}
}

func (r Registration) selectedBy(writes WriteSet) bool {
	for entity, fields := range r.dependencies() {
		binding := r.Bindings[entity]
		touched := writes[binding.TableID]
		if touched["*"] {
			return true
		}
		for field := range fields {
			if touched[binding.Fields[field]] {
				return true
			}
		}
	}
	return false
}

// Capture selects by actual canonical writes, including reciprocal targets, never a caller's rule list.
func Capture(ctx context.Context, app core.App, touched map[string]map[string]bool) ([]Guard, error) {
	registrations, err := Load(ctx, app)
	if err != nil {
		return nil, err
	}
	guards := []Guard{}
	var budget imageBudget
	for _, r := range activeGuards(registrations) {
		if !r.selectedBy(touched) {
			continue
		}
		before, err := r.Read(ctx, app)
		if err != nil {
			return nil, err
		}
		if err := budget.count(before); err != nil {
			return nil, err
		}
		guards = append(guards, Guard{r, before})
	}
	return guards, nil
}

func ValidateGuards(ctx context.Context, app core.App, guards []Guard) error {
	var budget imageBudget
	for _, guard := range guards {
		if err := budget.count(guard.Before); err != nil {
			return err
		}
	}
	for _, guard := range guards {
		after, err := guard.Registration.Read(ctx, app)
		if err != nil {
			return err
		}
		if err := budget.count(after); err != nil {
			return err
		}
		if err := guard.Registration.Validate(guard.Before, after); err != nil {
			return err
		}
	}
	return nil
}

type imageBudget struct{ rows, bytes int }

func (b *imageBudget) count(images Images) error {
	rows := 0
	for _, records := range images {
		rows += len(records)
	}
	raw, err := json.Marshal(images)
	if err != nil {
		return err
	}
	return b.add(rows, len(raw))
}

func (b *imageBudget) add(rows, bytes int) error {
	b.rows += rows
	b.bytes += bytes
	if b.rows > MaxRows || b.bytes > MaxBytes {
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
