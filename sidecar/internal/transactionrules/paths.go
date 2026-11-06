package transactionrules

import "github.com/vibetable/vibetable/sidecar/internal/pluginmodel"

type RelationStep struct {
	Field  string `json:"field"`
	Target string `json:"target"`
}

type PathMatch struct {
	Left  []RelationStep `json:"left,omitempty"`
	Right []RelationStep `json:"right,omitzero"`
}

func (m PathMatch) valid(self string) bool {
	for _, path := range [][]RelationStep{m.Left, m.Right} {
		if path == nil || len(path) > 4 {
			return false
		}
		for _, step := range path {
			if step.Field == "" || step.Target == "" {
				return false
			}
		}
	}
	if len(m.Left) == 0 {
		return false
	}
	if len(m.Right) == 0 {
		return self != "" && m.Left[len(m.Left)-1].Target == self
	}
	return m.Left[len(m.Left)-1].Target == m.Right[len(m.Right)-1].Target
}

// Each hop participates in write selection and in binding protection, including intermediate tables.
func (r Rule) relationSteps(visit func(string, RelationStep)) {
	if r.Relation != "" {
		visit(r.Entity, RelationStep{r.Relation, r.Target})
	}
	if r.Kind == "reference" {
		visit(r.Entity, RelationStep{r.Field, r.Target})
	}
	match := r.PathMatch
	if r.CurrentWhen != nil {
		match = *r.CurrentWhen
	}
	for _, path := range [][]RelationStep{match.Left, match.Right} {
		entity := r.Entity
		for _, step := range path {
			visit(entity, step)
			entity = step.Target
		}
	}
}

// ValidatePaths binds new path declarations at installation. Existing rule kinds retain their
// original physical validation at native configuration.
func (d Declaration) ValidatePaths(model *pluginmodel.Declaration) error {
	singles := map[[2]string]string{}
	for _, entity := range model.Entities {
		for _, field := range entity.Fields {
			if field.Relation != nil && field.Relation.Cardinality == "one" {
				singles[[2]string{entity.Key, field.Key}] = field.Relation.Entity
			}
		}
	}
	for _, rule := range d.Rules {
		if rule.Kind == "atMostOne" {
			for _, key := range rule.Fields {
				if singles[[2]string{rule.Entity, key}] == "" {
					return failure("reference_mapping_invalid")
				}
			}
		}
		if rule.Kind != "sameTarget" && rule.CurrentWhen == nil {
			continue
		}
		valid := true
		rule.relationSteps(func(entity string, step RelationStep) {
			if singles[[2]string{entity, step.Field}] != step.Target {
				valid = false
			}
		})
		if !valid {
			return failure("reference_mapping_invalid")
		}
	}
	return nil
}

func resolvePath(images Images, row map[string]any, path []RelationStep, allowEmptyEnd bool) (string, error) {
	for i, step := range path {
		value, present := row[step.Field]
		if !present {
			return "", failure("reference_violation")
		}
		if allowEmptyEnd && i == len(path)-1 && (value == nil || value == "") {
			return "", nil
		}
		id, ok := value.(string)
		if !ok || id == "" || images[step.Target][id] == nil {
			return "", failure("reference_violation")
		}
		if i == len(path)-1 {
			return id, nil
		}
		row = images[step.Target][id]
	}
	return "", failure("rules_invalid")
}

type pathEndPolicy struct {
	skipEmptyLeft   bool
	allowEmptyRight bool
}

func (m PathMatch) matches(images Images, row map[string]any, rowID string, policy pathEndPolicy) (bool, error) {
	left, err := resolvePath(images, row, m.Left, policy.skipEmptyLeft)
	if err != nil {
		return false, err
	}
	if len(m.Right) == 0 {
		return left == "" || left == rowID, nil
	}
	right, err := resolvePath(images, row, m.Right, policy.allowEmptyRight)
	return left == "" || left == right, err
}
