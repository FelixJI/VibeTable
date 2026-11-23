package transactionrules

import (
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

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
	walk := func(entity string, path []RelationStep) {
		for _, step := range path {
			visit(entity, step)
			entity = step.Target
		}
	}
	if r.Kind == "groupSameTarget" {
		walk(r.Entity, r.Group)
		for _, target := range r.Targets {
			walk(r.Entity, target.Path)
		}
		if r.Related != nil {
			visit(r.Entity, *r.Related)
		}
		if ref := r.GroupReference; ref != nil {
			walk(ref.Entity, ref.Group)
			for _, target := range ref.Targets {
				walk(ref.Entity, target.Path)
			}
		}
	}
	for _, c := range r.RelationCases {
		for _, steps := range [][]RelationStep{c.Required, c.Optional} {
			for _, step := range steps {
				visit(r.Entity, step)
			}
		}
	}
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
	if r.Kind == "parentFreeze" {
		entity := r.Entity
		for _, step := range r.ParentPath {
			visit(entity, step)
			entity = step.Target
		}
	}
	if spec := r.Reversal; spec != nil {
		visit(r.Entity, RelationStep{spec.Original, r.Entity})
		entity := r.Entity
		for _, step := range spec.OwnerPath {
			visit(entity, step)
			entity = step.Target
		}
	}
	if r.Reservation != nil {
		res := r.Reservation
		visit(r.Entity, RelationStep{res.Scope, res.Obligation.Entity})
		if res.PlanItem != "" {
			visit(r.Entity, RelationStep{res.PlanItem, res.Plans.Entity})
		}
		if res.InstructionSource != "" {
			visit(r.Entity, RelationStep{res.InstructionSource, r.Entity})
		}
		if res.TransferredFrom != "" {
			visit(r.Entity, RelationStep{res.TransferredFrom, r.Entity})
		}
		entity := r.Entity
		for _, step := range res.Phase.Path {
			visit(entity, step)
			entity = step.Target
		}
		purposeEntity := r.Entity
		for _, step := range res.Purpose.Path {
			visit(purposeEntity, step)
			purposeEntity = step.Target
		}
		if res.Plans != nil {
			visit(res.Plans.Entity, RelationStep{res.Plans.Scope, res.Obligation.Entity})
			visit(res.Plans.Entity, RelationStep{res.Plans.LogicalItem.Field, res.Plans.LogicalItem.Target})
			for _, path := range [][]RelationStep{res.Plans.CurrentWhen.Left, res.Plans.CurrentWhen.Right} {
				entity := res.Plans.Entity
				for _, step := range path {
					visit(entity, step)
					entity = step.Target
				}
			}
		}
	}
	if spec := r.Allocation; spec != nil {
		visit(r.Entity, RelationStep{spec.Owner, spec.Parent.Entity})
		for _, ref := range []AuthorityRef{spec.State, spec.Purpose} {
			entity := r.Entity
			for _, step := range ref.Path {
				visit(entity, step)
				entity = step.Target
			}
		}
		if dir := spec.Parent.Direction; dir != nil {
			entity := spec.Parent.Entity
			for _, step := range dir.Path {
				visit(entity, step)
				entity = step.Target
			}
		}
		for _, condition := range spec.RequireCompleteWhen {
			walk(spec.Parent.Entity, condition.Ref.Path)
		}
	}
	if spec := r.Settlement; spec != nil {
		visit(spec.Request.Entity, RelationStep{spec.Request.Capacity, spec.Capacity.Entity})
		visit(spec.Actual.Entity, RelationStep{spec.Actual.Request, spec.Request.Entity})
		visit(spec.Actual.Entity, RelationStep{spec.Actual.Capacity, spec.Capacity.Entity})
		refs := []struct {
			entity string
			ref    AuthorityRef
		}{{spec.Request.Entity, spec.Request.State}, {spec.Request.Entity, spec.Request.Direction},
			{spec.Actual.Entity, spec.Actual.State}, {spec.Actual.Entity, spec.Actual.Direction}}
		for _, pair := range []struct {
			entity string
			ref    AuthorityRef
		}{{spec.Capacity.Entity, spec.Capacity.Currency}, {spec.Request.Entity, spec.Request.Currency}, {spec.Actual.Entity, spec.Actual.Currency}, {spec.Capacity.Entity, spec.Capacity.Unit}, {spec.Request.Entity, spec.Request.Unit}, {spec.Actual.Entity, spec.Actual.Unit}, {spec.Capacity.Entity, spec.Capacity.Material}, {spec.Request.Entity, spec.Request.Material}, {spec.Actual.Entity, spec.Actual.Material}} {
			if pair.ref.Field != "" {
				refs = append(refs, pair)
			}
		}
		if spec.Capacity.Direction.Field != "" {
			refs = append(refs, struct {
				entity string
				ref    AuthorityRef
			}{spec.Capacity.Entity, spec.Capacity.Direction})
		}
		if spec.Request.Basis.Field != "" {
			refs = append(refs, struct {
				entity string
				ref    AuthorityRef
			}{spec.Request.Entity, spec.Request.Basis})
		}
		if spec.Request.Purpose.Field != "" {
			refs = append(refs, struct {
				entity string
				ref    AuthorityRef
			}{spec.Request.Entity, spec.Request.Purpose})
		}
		if spec.Actual.Purpose.Field != "" {
			refs = append(refs, struct {
				entity string
				ref    AuthorityRef
			}{spec.Actual.Entity, spec.Actual.Purpose})
		}
		if adj := spec.Adjustment; adj != nil {
			visit(adj.Entity, RelationStep{adj.Target, spec.Request.Entity})
			if adj.Allocation != "" {
				visit(adj.Entity, RelationStep{adj.Allocation, spec.Actual.Entity})
			}
		}
		if plans := spec.Plans; plans != nil {
			visit(spec.Request.Entity, RelationStep{spec.Request.PlanItem, plans.Entity})
			if spec.Actual.PlanItem != "" {
				visit(spec.Actual.Entity, RelationStep{spec.Actual.PlanItem, plans.Entity})
			}
			visit(plans.Entity, RelationStep{plans.Scope, spec.Capacity.Entity})
			visit(plans.Entity, RelationStep{plans.LogicalItem.Field, plans.LogicalItem.Target})
			for _, path := range [][]RelationStep{plans.CurrentWhen.Left, plans.CurrentWhen.Right} {
				entity := plans.Entity
				for _, step := range path {
					visit(entity, step)
					entity = step.Target
				}
			}
		}
		for _, pair := range refs {
			entity := pair.entity
			for _, step := range pair.ref.Path {
				visit(entity, step)
				entity = step.Target
			}
		}
	}
	if spec := r.Evidence; spec != nil {
		entity := r.Entity
		for _, step := range spec.OwnerPath {
			visit(entity, step)
			entity = step.Target
		}
		visit(entity, RelationStep{spec.LatestEvidence, spec.Evidence.Entity})
		visit(spec.Evidence.Entity, RelationStep{spec.Evidence.Owner, entity})
	}
}

// ValidatePaths binds new path declarations at installation. Existing rule kinds retain their
// original physical validation at native configuration.
func (d Declaration) ValidatePaths(model *pluginmodel.Declaration) error {
	singles := map[[2]string]string{}
	scalars := map[[2]string]bool{}
	textFields := map[[2]string]bool{}
	for _, entity := range model.Entities {
		for _, field := range entity.Fields {
			if field.Relation != nil && field.Relation.Cardinality == "one" {
				singles[[2]string{entity.Key, field.Key}] = field.Relation.Entity
			}
			scalars[[2]string{entity.Key, field.Key}] = true
			if field.Type == v2.LogicalText {
				textFields[[2]string{entity.Key, field.Key}] = true
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
		if rule.Kind == "parentFreeze" {
			for _, key := range rule.Fields {
				if !scalars[[2]string{rule.Entity, key}] {
					return failure("rules_invalid")
				}
			}
			parent := rule.Entity
			for _, step := range rule.ParentPath {
				parent = step.Target
			}
			if !scalars[[2]string{parent, rule.ParentStateField}] {
				return failure("rules_invalid")
			}
		}
		if spec := rule.Reversal; spec != nil {
			for _, field := range append([]string{spec.Quantity, spec.Purpose}, spec.SameFields...) {
				if !scalars[[2]string{rule.Entity, field}] {
					return failure("rules_invalid")
				}
			}
			owner := spec.OwnerPath[len(spec.OwnerPath)-1].Target
			for _, field := range append([]string{spec.OwnerStateField}, spec.SameOwnerFields...) {
				if !scalars[[2]string{owner, field}] {
					return failure("rules_invalid")
				}
			}
		}
		if rule.Reservation != nil {
			res := rule.Reservation
			for _, key := range []string{res.Quantity, res.Scope, res.PlanItem, res.InstructionSource, res.TransferredFrom} {
				if key != "" && !scalars[[2]string{rule.Entity, key}] {
					return failure("rules_invalid")
				}
			}
			purposeEntity := rule.Entity
			for _, step := range res.Purpose.Path {
				purposeEntity = step.Target
			}
			if !scalars[[2]string{purposeEntity, res.Purpose.Field}] {
				return failure("rules_invalid")
			}
			phaseEntity := rule.Entity
			for _, step := range res.Phase.Path {
				phaseEntity = step.Target
			}
			if !scalars[[2]string{phaseEntity, res.Phase.Field}] {
				return failure("rules_invalid")
			}
			if !scalars[[2]string{res.Obligation.Entity, res.Obligation.Quantity}] {
				return failure("rules_invalid")
			}
			if res.Plans != nil {
				for _, key := range []string{res.Plans.Quantity, res.Plans.Scope, res.Plans.LogicalItem.Field, res.Plans.Share} {
					if !scalars[[2]string{res.Plans.Entity, key}] {
						return failure("rules_invalid")
					}
				}
			}
		}
		if spec := rule.Allocation; spec != nil {
			for _, condition := range spec.RequireCompleteWhen {
				entity := spec.Parent.Entity
				for _, step := range condition.Ref.Path {
					entity = step.Target
				}
				if !textFields[[2]string{entity, condition.Ref.Field}] {
					return failure("rules_invalid")
				}
			}
			for _, key := range []string{spec.Owner, spec.Quantity, spec.Net, spec.Tax, spec.Gross} {
				if !scalars[[2]string{rule.Entity, key}] {
					return failure("rules_invalid")
				}
			}
			for _, key := range []string{spec.Parent.Quantity, spec.Parent.Net, spec.Parent.Tax, spec.Parent.Gross} {
				if !scalars[[2]string{spec.Parent.Entity, key}] {
					return failure("rules_invalid")
				}
			}
			for _, ref := range []AuthorityRef{spec.State, spec.Purpose} {
				entity := rule.Entity
				for _, step := range ref.Path {
					entity = step.Target
				}
				if !scalars[[2]string{entity, ref.Field}] {
					return failure("rules_invalid")
				}
			}
			if dir := spec.Parent.Direction; dir != nil {
				entity := spec.Parent.Entity
				for _, step := range dir.Path {
					entity = step.Target
				}
				for _, field := range []string{dir.ConventionField, dir.DirectionField} {
					if !scalars[[2]string{entity, field}] {
						return failure("rules_invalid")
					}
				}
			}
		}
		if spec := rule.Settlement; spec != nil {
			if plans := spec.Plans; plans != nil {
				for _, field := range []string{plans.Quantity, plans.Scope, plans.LogicalItem.Field, plans.Share} {
					if !scalars[[2]string{plans.Entity, field}] {
						return failure("rules_invalid")
					}
				}
			}
			for _, field := range []string{spec.Capacity.Quantity, spec.Capacity.Net, spec.Capacity.Tax, spec.Capacity.Gross} {
				if field != "" && !scalars[[2]string{spec.Capacity.Entity, field}] {
					return failure("rules_invalid")
				}
			}
			for _, field := range []string{spec.Request.Capacity, spec.Request.PlanItem, spec.Request.Quantity, spec.Request.Net, spec.Request.Tax, spec.Request.Gross} {
				if field != "" && !scalars[[2]string{spec.Request.Entity, field}] {
					return failure("rules_invalid")
				}
			}
			for _, field := range []string{spec.Actual.Request, spec.Actual.Capacity, spec.Actual.PlanItem, spec.Actual.Quantity, spec.Actual.Net, spec.Actual.Tax, spec.Actual.Gross} {
				if field != "" && !scalars[[2]string{spec.Actual.Entity, field}] {
					return failure("rules_invalid")
				}
			}
			refs := []struct {
				entity string
				ref    AuthorityRef
			}{{spec.Request.Entity, spec.Request.State}, {spec.Request.Entity, spec.Request.Direction},
				{spec.Actual.Entity, spec.Actual.State}, {spec.Actual.Entity, spec.Actual.Direction}}
			for _, pair := range []struct {
				entity string
				ref    AuthorityRef
			}{{spec.Capacity.Entity, spec.Capacity.Currency}, {spec.Request.Entity, spec.Request.Currency}, {spec.Actual.Entity, spec.Actual.Currency}, {spec.Capacity.Entity, spec.Capacity.Unit}, {spec.Request.Entity, spec.Request.Unit}, {spec.Actual.Entity, spec.Actual.Unit}, {spec.Capacity.Entity, spec.Capacity.Material}, {spec.Request.Entity, spec.Request.Material}, {spec.Actual.Entity, spec.Actual.Material}} {
				if pair.ref.Field != "" {
					refs = append(refs, pair)
				}
			}
			if spec.Capacity.Direction.Field != "" {
				refs = append(refs, struct {
					entity string
					ref    AuthorityRef
				}{spec.Capacity.Entity, spec.Capacity.Direction})
			}
			if spec.Request.Basis.Field != "" {
				refs = append(refs, struct {
					entity string
					ref    AuthorityRef
				}{spec.Request.Entity, spec.Request.Basis})
			}
			if spec.Request.Purpose.Field != "" {
				refs = append(refs, struct {
					entity string
					ref    AuthorityRef
				}{spec.Request.Entity, spec.Request.Purpose})
			}
			if spec.Actual.Purpose.Field != "" {
				refs = append(refs, struct {
					entity string
					ref    AuthorityRef
				}{spec.Actual.Entity, spec.Actual.Purpose})
			}
			if adj := spec.Adjustment; adj != nil {
				for _, field := range []string{adj.Kind, adj.Target, adj.Allocation, adj.CloseRemaining, adj.CloseQuantity, adj.CloseNet, adj.CloseTax, adj.CloseGross} {
					if field != "" && !scalars[[2]string{adj.Entity, field}] {
						return failure("rules_invalid")
					}
				}
			}
			for _, pair := range refs {
				entity := pair.entity
				for _, step := range pair.ref.Path {
					entity = step.Target
				}
				if !scalars[[2]string{entity, pair.ref.Field}] {
					return failure("rules_invalid")
				}
			}
		}
		if spec := rule.Evidence; spec != nil {
			for _, field := range spec.Fields {
				if !scalars[[2]string{rule.Entity, field}] {
					return failure("rules_invalid")
				}
			}
			owner := rule.Entity
			for _, step := range spec.OwnerPath {
				owner = step.Target
			}
			if !scalars[[2]string{owner, spec.LatestEvidence}] {
				return failure("rules_invalid")
			}
			for _, field := range append([]string{spec.Evidence.Owner, spec.Evidence.Type, spec.Evidence.Note}, spec.Evidence.Fields...) {
				if !scalars[[2]string{spec.Evidence.Entity, field}] {
					return failure("rules_invalid")
				}
			}
		}
		if rule.Kind != "groupSameTarget" && rule.Kind != "sameTarget" && rule.Kind != "parentFreeze" && rule.Kind != "reservationBalance" && rule.Kind != "reversalBalance" &&
			rule.Kind != "allocationBalance" && rule.Kind != "settlementBalance" && rule.Kind != "changeEvidence" && rule.CurrentWhen == nil && rule.RelationCases == nil {
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
	skipEmptyRight  bool
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
	right, err := resolvePath(images, row, m.Right, policy.allowEmptyRight || policy.skipEmptyRight)
	return left == "" || policy.skipEmptyRight && right == "" || left == right, err
}
