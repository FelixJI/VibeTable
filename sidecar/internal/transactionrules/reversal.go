package transactionrules

import (
	"math/big"
	"reflect"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

// ReversalSpec compares stored line/owner snapshots and bounds confirmed
// reversals against the original line; it never mutates the fulfillment ledger.
type ReversalSpec struct {
	Original        string         `json:"original"`
	Quantity        string         `json:"quantity"`
	Purpose         string         `json:"purpose"`
	ReversalPurpose string         `json:"reversalPurpose"`
	OriginalPurpose string         `json:"originalPurpose"`
	OwnerPath       []RelationStep `json:"ownerPath"`
	OwnerStateField string         `json:"ownerStateField"`
	RequiredStates  []string       `json:"requiredStates"`
	ConfirmedState  string         `json:"confirmedState"`
	SameFields      []string       `json:"sameFields"`
	SameOwnerFields []string       `json:"sameOwnerFields"`
}

func (s *ReversalSpec) valid() bool {
	if s == nil || s.Original == "" || s.Quantity == "" || s.Purpose == "" || s.ReversalPurpose == "" ||
		s.OriginalPurpose == "" || s.ReversalPurpose == s.OriginalPurpose || s.OwnerStateField == "" || s.ConfirmedState == "" ||
		len(s.OwnerPath) < 1 || len(s.OwnerPath) > 4 || !contains(s.RequiredStates, s.ConfirmedState) ||
		len(s.RequiredStates) < 1 || len(s.RequiredStates) > MaxFrozenStates || s.SameFields == nil || s.SameOwnerFields == nil {
		return false
	}
	for _, step := range s.OwnerPath {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	for _, fields := range [][]string{s.RequiredStates, s.SameFields, s.SameOwnerFields} {
		if len(fields) > MaxParentFreezeFields {
			return false
		}
		seen := map[string]bool{}
		for _, field := range fields {
			if field == "" || seen[field] {
				return false
			}
			seen[field] = true
		}
	}
	return true
}

func (rule Rule) validateReversal(after Images, formats map[string]exactdecimal.Format) error {
	spec := rule.Reversal
	format := formats[rule.Entity+"\x00"+spec.Quantity]
	ownerEntity := spec.OwnerPath[len(spec.OwnerPath)-1].Target
	ownerOf := func(row map[string]any) (map[string]any, error) {
		id, err := resolvePath(after, row, spec.OwnerPath, false)
		if err != nil {
			return nil, failure("reversal_invalid")
		}
		return after[ownerEntity][id], nil
	}
	match := func(left, right map[string]any, fields []string) bool {
		for _, field := range fields {
			a, hasA := left[field]
			b, hasB := right[field]
			if !hasA || !hasB || !reflect.DeepEqual(a, b) {
				return false
			}
		}
		return true
	}
	totals := map[string]*big.Int{}
	for id, row := range after[rule.Entity] {
		if row[spec.Purpose] != spec.ReversalPurpose {
			continue
		}
		owner, err := ownerOf(row)
		if err != nil {
			return err
		}
		state, ok := owner[spec.OwnerStateField].(string)
		if !ok {
			return failure("reversal_invalid")
		}
		if !contains(spec.RequiredStates, state) {
			continue
		}
		originalID, ok := row[spec.Original].(string)
		original := after[rule.Entity][originalID]
		if !ok || originalID == "" || originalID == id || original == nil || original[spec.Purpose] != spec.OriginalPurpose {
			return failure("reversal_invalid")
		}
		originalOwner, err := ownerOf(original)
		if err != nil || originalOwner[spec.OwnerStateField] != spec.ConfirmedState {
			return failure("reversal_invalid")
		}
		if !match(row, original, spec.SameFields) || !match(owner, originalOwner, spec.SameOwnerFields) {
			return failure("reversal_mismatch")
		}
		quantity, err := format.Parse(row[spec.Quantity])
		if err != nil {
			return err
		}
		limit, err := format.Parse(original[spec.Quantity])
		if err != nil {
			return err
		}
		if quantity.Cmp(limit) > 0 {
			return failure("reversal_exceeded")
		}
		if state != spec.ConfirmedState {
			continue
		}
		if totals[originalID] == nil {
			totals[originalID] = new(big.Int)
		}
		totals[originalID].Add(totals[originalID], quantity)
		if totals[originalID].Cmp(limit) > 0 {
			return failure("reversal_exceeded")
		}
	}
	return nil
}
