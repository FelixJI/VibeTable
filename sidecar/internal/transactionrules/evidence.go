package transactionrules

import (
	"reflect"
	"strings"
)

// EvidenceRecord names the correction-evidence rows: the optional role-bound
// reverse owner relation (empty on request-side disposal events), the type
// marker, the mandatory non-blank note and additional frozen fact fields such
// as the explicit disposal scope. One physical entity may hold both request
// disposals and actual corrections; only events explicitly bound to an owner
// can justify that owner's corrections.
type EvidenceRecord struct {
	Entity string   `json:"entity"`
	Owner  string   `json:"owner"`
	Type   string   `json:"type"`
	Note   string   `json:"note"`
	Fields []string `json:"fields,omitempty"`
}

// ChangeEvidenceSpec is the closed typed shape of a changeEvidence rule.
// Corrections of the listed core fields, or adding, removing and rebinding
// rows under an owner that already existed, are allowed only when the same
// transaction adds one new valid evidence row and binds it to that same owner
// through the owner's latest-evidence pointer. An empty ownerPath declares the
// rule entity as its own owner without any artificial self relation. Recorded
// evidence rows never change or disappear — every declared fact field is
// frozen — evidence is never reused across transactions, and each owner is
// covered separately. Erasing a registration by deleting its owner cannot be
// based on evidence at all, so it stays rejected.
type ChangeEvidenceSpec struct {
	Fields         []string       `json:"fields,omitempty"`
	OwnerPath      []RelationStep `json:"ownerPath,omitempty"`
	LatestEvidence string         `json:"latestEvidence"`
	Types          []string       `json:"types"`
	Evidence       EvidenceRecord `json:"evidence"`
}

func (s *ChangeEvidenceSpec) valid() bool {
	if s == nil || len(s.OwnerPath) > 4 || s.LatestEvidence == "" || s.Evidence.Entity == "" || s.Evidence.Owner == "" ||
		s.Evidence.Type == "" || s.Evidence.Note == "" || len(s.Types) < 1 || len(s.Types) > MaxFrozenStates ||
		len(s.Fields) > MaxParentFreezeFields || len(s.Evidence.Fields) > MaxParentFreezeFields {
		return false
	}
	for _, step := range s.OwnerPath {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	seen := map[string]bool{}
	for _, field := range s.Fields {
		if field == "" || seen[field] {
			return false
		}
		seen[field] = true
	}
	// The evidence entity keeps its own namespace: identical field names on
	// the protected entity and the evidence entity (quantity, net, tax, gross)
	// are legal natural-model facts, so each list deduplicates on its own.
	evidenceSeen := map[string]bool{}
	for _, field := range s.Evidence.Fields {
		if field == "" || evidenceSeen[field] {
			return false
		}
		evidenceSeen[field] = true
	}
	return uniqueStates(s.Types)
}

// ownerEntity returns the authority holding the latest-evidence pointer; an
// empty path keeps the rule entity itself as its own owner.
func (s ChangeEvidenceSpec) ownerEntity(self string) string {
	entity := self
	for _, step := range s.OwnerPath {
		entity = step.Target
	}
	return entity
}

// validateEvidence enforces the atomic correction contract on the final graph.
// The before/after row audit and the changeSet identity itself stay with the
// mutation kernel; this rule only stores and checks the closed evidence
// bindings. Unfreezing first never helps: the justifying evidence must be new
// in the very transaction that changes the facts.
func (rule Rule) validateEvidence(before, after Images) error {
	spec := rule.Evidence
	self := rule.Entity
	ownerEntity := spec.ownerEntity(self)
	// The read projection contains exactly the declared fields, so freezing
	// compares facts only and never trips over audit or system columns.
	frozen := append([]string{spec.Evidence.Owner, spec.Evidence.Type, spec.Evidence.Note}, spec.Evidence.Fields...)
	resolveOwner := func(images Images, rowID string, row map[string]any) (string, error) {
		if len(spec.OwnerPath) == 0 {
			return rowID, nil
		}
		return resolvePath(images, row, spec.OwnerPath, false)
	}
	// Evidence rows are immutable once recorded: any change or deletion is
	// rejected, including rows whose owner binding is unrelated.
	for id, old := range before[spec.Evidence.Entity] {
		next := after[spec.Evidence.Entity][id]
		if next == nil {
			return failure("evidence_frozen")
		}
		for _, field := range frozen {
			if !reflect.DeepEqual(old[field], next[field]) {
				return failure("evidence_frozen")
			}
		}
	}
	// Every surviving evidence row stays typed, explained and — when bound —
	// pointed at a live owner; unbound rows are role-selected request-side
	// events that can never justify a correction.
	latest := map[string]string{}
	for ownerID, owner := range after[ownerEntity] {
		value, present := owner[spec.LatestEvidence]
		if !present || value == nil || value == "" {
			continue
		}
		evidenceID, ok := value.(string)
		if !ok || after[spec.Evidence.Entity][evidenceID] == nil {
			return failure("evidence_invalid")
		}
		latest[ownerID] = evidenceID
	}
	for _, row := range after[spec.Evidence.Entity] {
		note, _ := row[spec.Evidence.Note].(string)
		if strings.TrimSpace(note) == "" {
			return failure("evidence_invalid")
		}
		if bound, err := optionalRef(after, row, spec.Evidence.Owner, ownerEntity); err != nil {
			return failure("evidence_invalid")
		} else if bound != "" {
			// Types lists only the kinds that can prove an actual correction;
			// owner-bound events must carry one of them.
			evidenceType, _ := row[spec.Evidence.Type].(string)
			if !contains(spec.Types, evidenceType) {
				return failure("evidence_invalid")
			}
		}
	}
	requireEvidence := func(ownerID string) error {
		evidenceID, bound := latest[ownerID]
		if !bound {
			return failure("evidence_required")
		}
		if before[spec.Evidence.Entity][evidenceID] != nil {
			return failure("evidence_reused")
		}
		if after[spec.Evidence.Entity][evidenceID][spec.Evidence.Owner] != ownerID {
			return failure("evidence_invalid")
		}
		// Only a correction-capable kind may justify the change; request-side
		// events with a null owner never pass this recheck.
		if evidenceType, _ := after[spec.Evidence.Entity][evidenceID][spec.Evidence.Type].(string); !contains(spec.Types, evidenceType) {
			return failure("evidence_invalid")
		}
		return nil
	}
	for id, old := range before[rule.Entity] {
		oldOwner, err := resolveOwner(before, id, old)
		if err != nil {
			return err
		}
		next := after[rule.Entity][id]
		if next == nil {
			// Removing a row is a correction of its registration. When the
			// owner vanished too, no live owner can carry a new latest
			// evidence pointer, so the erasure is rejected outright.
			if err := requireEvidence(oldOwner); err != nil {
				return err
			}
			continue
		}
		newOwner, err := resolveOwner(after, id, next)
		if err != nil {
			return err
		}
		if newOwner != oldOwner {
			// Leaving a surviving owner is a correction of that registration.
			if err := requireEvidence(oldOwner); err != nil {
				return err
			}
		}
		if before[ownerEntity][newOwner] == nil {
			continue // facts first attached to an owner born in this transaction
		}
		changed := newOwner != oldOwner
		for end := 1; end <= len(spec.OwnerPath); end++ {
			oldTarget, err := resolvePath(before, old, spec.OwnerPath[:end], false)
			if err != nil {
				return err
			}
			newTarget, err := resolvePath(after, next, spec.OwnerPath[:end], false)
			if err != nil {
				return err
			}
			if oldTarget != newTarget {
				changed = true
			}
		}
		for _, field := range spec.Fields {
			if !reflect.DeepEqual(old[field], next[field]) {
				changed = true
			}
		}
		if changed {
			if err := requireEvidence(newOwner); err != nil {
				return err
			}
		}
	}
	for id, row := range after[rule.Entity] {
		if before[rule.Entity][id] != nil {
			continue
		}
		owner, err := resolveOwner(after, id, row)
		if err != nil {
			return err
		}
		// Adding an allocation to an owner that already existed is a covered
		// correction; rows under a same-transaction owner stay free.
		if before[ownerEntity][owner] != nil {
			if err := requireEvidence(owner); err != nil {
				return err
			}
		}
	}
	return nil
}
