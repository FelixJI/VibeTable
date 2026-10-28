// Package pluginmodel defines the closed logical model contract. It has no schema writer.
package pluginmodel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const Contract = "vibetable.plugin-model.v1"

type Declaration struct {
	Contract string   `json:"contract"`
	ModelID  string   `json:"modelId"`
	Version  int      `json:"version"`
	Entities []Entity `json:"entities"`
}
type Entity struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Operations []string `json:"operations"`
	Fields     []Field  `json:"fields"`
}
type Field struct {
	Key       string         `json:"key"`
	Label     string         `json:"label"`
	Type      v2.LogicalType `json:"type"`
	Precision string         `json:"precision,omitempty"`
	Relation  *Relation      `json:"relation,omitempty"`
}
type Relation struct {
	Entity      string `json:"entity"`
	Field       string `json:"field"`
	Cardinality string `json:"cardinality"`
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func Decode(raw []byte) (*Declaration, error) {
	var result Declaration
	if err := v2.StrictDecode(raw, &result); err != nil {
		return nil, err
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}
func (d Declaration) Validate() error {
	bad := func(path string) error { return fmt.Errorf("plugin.model.declaration_invalid: %s", path) }
	if d.Contract != Contract || !keyPattern.MatchString(d.ModelID) || d.Version < 1 || d.Version > 2147483647 || len(d.Entities) < 1 || len(d.Entities) > 16 {
		return bad("model")
	}
	entities, labels := map[string]bool{}, map[string]bool{}
	for _, e := range d.Entities {
		if !keyPattern.MatchString(e.Key) || entities[e.Key] || !validLabel(e.Label) || labels[e.Label] || len(e.Fields) < 1 || len(e.Fields) > 64 {
			return bad("entities." + e.Key)
		}
		entities[e.Key], labels[e.Label] = true, true
		ops := map[string]bool{}
		for _, op := range e.Operations {
			switch op {
			case "read", "query", "create", "update", "delete":
			default:
				return bad("entities." + e.Key + ".operations")
			}
			if ops[op] {
				return bad("entities." + e.Key + ".operations")
			}
			ops[op] = true
		}
		if !ops["read"] || !ops["query"] {
			return bad("entities." + e.Key + ".operations")
		}
		fields, names := map[string]bool{}, map[string]bool{}
		hasScalar := false
		for _, f := range e.Fields {
			if f.Type != v2.LogicalRelation {
				hasScalar = true
			}
			p := "entities." + e.Key + ".fields." + f.Key
			if !keyPattern.MatchString(f.Key) || f.Key == "id" || fields[f.Key] || !validLabel(f.Label) || names[f.Label] {
				return bad(p)
			}
			fields[f.Key], names[f.Label] = true, true
			switch f.Type {
			case v2.LogicalText, v2.LogicalBool, v2.LogicalDate, v2.LogicalDateTime, v2.LogicalEmail, v2.LogicalURL, v2.LogicalNumber, v2.LogicalRelation:
			default:
				return bad(p + ".type")
			}
			if f.Type == v2.LogicalNumber {
				if f.Precision != "integer" && f.Precision != "float64" {
					return bad(p + ".precision")
				}
			} else if f.Precision != "" {
				return bad(p + ".precision")
			}
			if f.Type == v2.LogicalRelation {
				if f.Relation == nil || (f.Relation.Cardinality != "one" && f.Relation.Cardinality != "many") {
					return bad(p + ".relation")
				}
			} else if f.Relation != nil {
				return bad(p + ".relation")
			}
		}
		if !hasScalar {
			return bad("entities." + e.Key + ".fields: a scalar relation label is required")
		}
	}
	for _, e := range d.Entities {
		for _, f := range e.Fields {
			if f.Relation == nil {
				continue
			}
			paired := false
			for _, target := range d.Entities {
				if target.Key == f.Relation.Entity {
					for _, reverse := range target.Fields {
						if reverse.Key == f.Relation.Field && reverse.Relation != nil && reverse.Relation.Entity == e.Key && reverse.Relation.Field == f.Key && (e.Key != target.Key || f.Key != reverse.Key) {
							paired = true
						}
					}
				}
			}
			if !paired {
				return bad("entities." + e.Key + ".fields." + f.Key + ".relation")
			}
		}
	}
	return nil
}
func validLabel(s string) bool {
	if s == "" || s != strings.TrimSpace(s) || len([]rune(s)) > 100 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (f Field) Draft(tables map[string]string) (v2.FieldDraft, error) {
	r, err := v2.RecommendedDefaults(f.Type)
	if err != nil {
		return v2.FieldDraft{}, err
	}
	d := v2.FieldDraft{DisplayName: f.Label, LogicalType: f.Type, Value: r.Value, Constraints: r.Constraints, Storage: r.Storage, Display: r.Display}
	if f.Type == v2.LogicalNumber {
		d.Storage.Options.OnlyInt = f.Precision == "integer"
	}
	if f.Relation != nil {
		d.Relation = &v2.RelationSpec{TargetTableID: tables[f.Relation.Entity], Cardinality: f.Relation.Cardinality, DeletePolicy: "restrict", DisplayField: "id"}
	}
	return d, nil
}

// Compatibility never converts values or infers identity from a display name.
func (f Field) Compatibility(existing v2.FieldDefinition, tables map[string]string) string {
	if existing.Lifecycle.State != v2.LifecycleActive || existing.LogicalType != f.Type {
		return "plugin_model_type_mismatch"
	}
	if f.Type == v2.LogicalNumber && existing.Storage.Options.OnlyInt != (f.Precision == "integer") {
		return "plugin_model_precision_loss"
	}
	if f.Relation != nil && (existing.Relation == nil || existing.Relation.TargetTableID != tables[f.Relation.Entity] || existing.Relation.Cardinality != f.Relation.Cardinality) {
		return "plugin_model_relation_mismatch"
	}
	return ""
}

func Equal(a, b Declaration) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
