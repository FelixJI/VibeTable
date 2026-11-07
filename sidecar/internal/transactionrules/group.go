package transactionrules

type GroupTarget struct {
	Path        []RelationStep `json:"path"`
	WhenPresent string         `json:"whenPresent,omitempty"`
}
type GroupReference struct {
	Entity      string         `json:"entity"`
	Group       []RelationStep `json:"group"`
	Targets     []GroupTarget  `json:"targets"`
	WhenPresent string         `json:"whenPresent,omitempty"`
}

func validGroupPath(path []RelationStep) bool {
	return len(path) > 0 && len(path) <= 4 && pathStepsValid(path)
}
func validGroupTargets(targets []GroupTarget, end string) bool {
	if len(targets) < 1 || len(targets) > MaxParentFreezeFields {
		return false
	}
	for _, target := range targets {
		if !validGroupPath(target.Path) || target.Path[len(target.Path)-1].Target != end || target.WhenPresent != "" && target.WhenPresent != target.Path[0].Field {
			return false
		}
	}
	return true
}
func (r Rule) validGroup() bool {
	if !validGroupPath(r.Group) || len(r.Targets) == 0 || len(r.Targets[0].Path) == 0 {
		return false
	}
	end := r.Targets[0].Path[len(r.Targets[0].Path)-1].Target
	if !validGroupTargets(r.Targets, end) {
		return false
	}
	if r.Related != nil && (r.Related.Field == "" || r.Related.Target != r.Entity) {
		return false
	}
	if ref := r.GroupReference; ref != nil {
		if ref.Entity == "" || !validGroupPath(ref.Group) || ref.Group[len(ref.Group)-1].Target != r.Group[len(r.Group)-1].Target || !validGroupTargets(ref.Targets, end) || ref.WhenPresent != "" && ref.WhenPresent != ref.Group[0].Field {
			return false
		}
	}
	return true
}

func groupSkip(row map[string]any, field string) (bool, error) {
	if field == "" {
		return false, nil
	}
	value, present := row[field]
	if !present {
		return false, failure("reference_violation")
	}
	return value == nil || value == "", nil
}

func (r Rule) validateGroup(after Images) error {
	groups := map[string]map[string]bool{}
	add := func(known map[string]bool, row map[string]any, targets []GroupTarget) error {
		for _, target := range targets {
			skip, err := groupSkip(row, target.WhenPresent)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			id, err := resolvePath(after, row, target.Path, true)
			if err != nil {
				return err
			}
			if id != "" {
				known[id] = true
			}
			if len(known) > 1 {
				return failure("same_target_violation")
			}
		}
		return nil
	}
	for _, row := range after[r.Entity] {
		group, err := resolvePath(after, row, r.Group, false)
		if err != nil {
			return err
		}
		if groups[group] == nil {
			groups[group] = map[string]bool{}
		}
		if err = add(groups[group], row, r.Targets); err != nil {
			return err
		}
		if r.Related != nil {
			skip, err := groupSkip(row, r.Related.Field)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			id, err := relationRef(after, row, r.Related.Field, r.Entity)
			if err != nil {
				return err
			}
			if err = add(groups[group], after[r.Entity][id], r.Targets); err != nil {
				return err
			}
		}
	}
	if ref := r.GroupReference; ref != nil {
		for _, row := range after[ref.Entity] {
			skip, err := groupSkip(row, ref.WhenPresent)
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			group, err := resolvePath(after, row, ref.Group, false)
			if err != nil {
				return err
			}
			current, present := groups[group]
			if !present {
				return failure("reference_violation")
			}
			known := map[string]bool{}
			for target := range current {
				known[target] = true
			}
			if err = add(known, row, ref.Targets); err != nil {
				return err
			}
		}
	}
	return nil
}
