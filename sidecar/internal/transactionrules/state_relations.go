package transactionrules

type StateRelationCase struct {
	Required []RelationStep `json:"required"`
	Optional []RelationStep `json:"optional"`
}

func (r Rule) validRelationCases() bool {
	if r.RelationCases == nil {
		return true
	}
	if r.Kind != "state" || len(r.RelationCases) != len(r.Transitions) {
		return false
	}
	targets := map[string]string{}
	for state, c := range r.RelationCases {
		if _, ok := r.Transitions[state]; !ok || c.Required == nil || c.Optional == nil || len(c.Required)+len(c.Optional) > 64 {
			return false
		}
		seen := map[string]bool{}
		for _, steps := range [][]RelationStep{c.Required, c.Optional} {
			for _, step := range steps {
				if step.Field == "" || step.Target == "" || seen[step.Field] || targets[step.Field] != "" && targets[step.Field] != step.Target {
					return false
				}
				seen[step.Field] = true
				targets[step.Field] = step.Target
			}
		}
	}
	return true
}

func (r Rule) validateRelationCase(after Images, row map[string]any, state string) error {
	if r.RelationCases == nil {
		return nil
	}
	controlled := map[string]bool{}
	for _, c := range r.RelationCases {
		for _, steps := range [][]RelationStep{c.Required, c.Optional} {
			for _, step := range steps {
				controlled[step.Field] = true
			}
		}
	}
	selected := map[string]bool{}
	check := func(step RelationStep, required bool) error {
		value, present := row[step.Field]
		if !present {
			return failure("reference_violation")
		}
		if value == nil || value == "" {
			if required {
				return failure("reference_violation")
			}
			return nil
		}
		id, ok := value.(string)
		if !ok || after[step.Target][id] == nil {
			return failure("reference_violation")
		}
		return nil
	}
	c := r.RelationCases[state]
	for _, step := range c.Required {
		selected[step.Field] = true
		if err := check(step, true); err != nil {
			return err
		}
	}
	for _, step := range c.Optional {
		selected[step.Field] = true
		if err := check(step, false); err != nil {
			return err
		}
	}
	for field := range controlled {
		if !selected[field] {
			value, present := row[field]
			if !present || value != nil && value != "" {
				return failure("state_invalid")
			}
		}
	}
	return nil
}
