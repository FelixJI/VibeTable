package transactionrules

import (
	"bytes"
	"encoding/json"
	"math/big"
	"slices"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

// AuthorityRef selects one authoritative scalar: an empty path reads the
// row's own field; 1-4 single-value relation hops read through owners. The
// protected row never supplies a forgeable copy of an owner fact.
type AuthorityRef struct {
	Path  []RelationStep `json:"path"`
	Field string         `json:"field"`
}

func (ref AuthorityRef) MarshalJSON() ([]byte, error) {
	if ref.Field == "" {
		return []byte("null"), nil
	}
	type wireRef AuthorityRef
	if ref.Path == nil {
		ref.Path = []RelationStep{}
	}
	return json.Marshal(wireRef(ref))
}

func (ref *AuthorityRef) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		*ref = AuthorityRef{}
		return nil
	}
	type wireRef AuthorityRef
	var value wireRef
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || !AuthorityRef(value).valid() {
		return failure("rules_invalid")
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil || containsJSONNull(fields) {
		return failure("rules_invalid")
	}
	*ref = AuthorityRef(value)
	return nil
}

func (ref AuthorityRef) valid() bool {
	if len(ref.Path) > 4 || ref.Field == "" {
		return false
	}
	for _, step := range ref.Path {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	return true
}

// AllocationDirection is the one-shot signed/magnitude carrier mapping for
// owners. The authoritative amountConvention value is "signed" (raw face
// values already carry the internal direction and are never re-signed) or
// "magnitude" (raw face values are positive magnitudes; a hit in
// redDirections converts them exactly once to the signed internal
// direction). The plugin never precomputes a second effects copy: anomalous
// signed originals keep their raw signs so the direction checks expose them
// instead of abs()ing them into legal effects.
type AllocationDirection struct {
	Path            []RelationStep `json:"path"`
	ConventionField string         `json:"conventionField"`
	DirectionField  string         `json:"directionField"`
	RedDirections   []string       `json:"redDirections"`
}

func (dir *AllocationDirection) valid() bool {
	return dir == nil || len(dir.Path) >= 1 && len(dir.Path) <= 4 && dir.ConventionField != "" &&
		dir.DirectionField != "" && len(dir.RedDirections) >= 1 && len(dir.RedDirections) <= MaxFrozenStates &&
		uniqueStates(dir.RedDirections) && pathStepsValid(dir.Path)
}

func pathStepsValid(path []RelationStep) bool {
	for _, step := range path {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	return true
}

// AllocationParent names the owner entity, its four stored measures and the
// optional direction mapping. The owner relation itself is single-valued on
// the allocation row.
type AllocationParent struct {
	Entity    string               `json:"entity"`
	Quantity  string               `json:"quantity"`
	Net       string               `json:"net"`
	Tax       string               `json:"tax"`
	Gross     string               `json:"gross"`
	Direction *AllocationDirection `json:"direction,omitempty"`
}

// AllocationCoverageCondition compares a text field reached from the parent row.
type AllocationCoverageCondition struct {
	Ref    AuthorityRef `json:"ref"`
	Equals string       `json:"equals"`
}

// Only the new condition uses exact wire keys. Older AuthorityRef users keep their decoder.
func validCoverageWire(entry any) bool {
	condition, ok := entry.(map[string]any)
	if !ok || len(condition) != 2 || condition["equals"] == nil {
		return false
	}
	ref, ok := condition["ref"].(map[string]any)
	if !ok || ref["field"] == nil {
		return false
	}
	rawPath, supplied := ref["path"]
	if !supplied {
		return len(ref) == 1
	}
	path, ok := rawPath.([]any)
	if !ok || len(ref) != 2 {
		return false
	}
	for _, entry := range path {
		step, ok := entry.(map[string]any)
		if !ok || len(step) != 2 || step["field"] == nil || step["target"] == nil {
			return false
		}
	}
	return true
}

// AllocationSpec is the closed typed shape of an allocationBalance rule:
// field and relation names only; no formulas, SQL or scripts are accepted.
type AllocationSpec struct {
	RequireCompleteWhenAllocated enabledOption                 `json:"requireCompleteWhenAllocated,omitempty"`
	RequireCompleteWhen          []AllocationCoverageCondition `json:"requireCompleteWhen,omitempty"`
	Owner                        string                        `json:"owner"`
	Parent                       AllocationParent              `json:"parent"`
	Quantity                     string                        `json:"quantity"`
	Net                          string                        `json:"net"`
	Tax                          string                        `json:"tax"`
	Gross                        string                        `json:"gross"`
	State                        AuthorityRef                  `json:"state"`
	DraftStates                  []string                      `json:"draftStates,omitempty"`
	Purpose                      AuthorityRef                  `json:"purpose"`
	ZeroQuantityPurposes         []string                      `json:"zeroQuantityPurposes,omitempty"`
}

func uniqueStates(states []string) bool {
	seen := map[string]bool{}
	for _, state := range states {
		if state == "" || seen[state] || len(state) > 64 {
			return false
		}
		seen[state] = true
	}
	return true
}

func (s *AllocationSpec) valid() bool {
	if s == nil || s.Owner == "" || s.Parent.Entity == "" || s.Parent.Quantity == "" || s.Parent.Net == "" ||
		s.Parent.Tax == "" || s.Parent.Gross == "" || s.Quantity == "" || s.Net == "" || s.Tax == "" || s.Gross == "" ||
		!s.State.valid() || !s.Purpose.valid() || len(s.DraftStates) > MaxFrozenStates || len(s.ZeroQuantityPurposes) > MaxFrozenStates {
		return false
	}
	if s.RequireCompleteWhen != nil && (!bool(s.RequireCompleteWhenAllocated) || len(s.RequireCompleteWhen) == 0 || len(s.RequireCompleteWhen) > 4) {
		return false
	}
	for i, condition := range s.RequireCompleteWhen {
		if !condition.Ref.valid() || condition.Equals == "" || len(condition.Equals) > 64 {
			return false
		}
		for _, previous := range s.RequireCompleteWhen[:i] {
			if condition.Ref.Field == previous.Ref.Field && slices.Equal(condition.Ref.Path, previous.Ref.Path) {
				return false
			}
		}
	}
	return uniqueStates(s.DraftStates) && uniqueStates(s.ZeroQuantityPurposes) && s.Parent.Direction.valid()
}

// measures returns the child and parent measure field names in the fixed
// quantity, net, tax, gross order shared by every allocation rule.
func (s AllocationSpec) measures() [4][2]string {
	return [4][2]string{{s.Quantity, s.Parent.Quantity}, {s.Net, s.Parent.Net}, {s.Tax, s.Parent.Tax}, {s.Gross, s.Parent.Gross}}
}

type allocationOwnerTotals struct {
	child  [4]*big.Int
	parent [4]*big.Int
}

// validateAllocation enforces the closed four-measure internal-allocation model
// on the final graph: every allocation carries exactly one owner, net+tax
// equals gross on raw child and parent rows wherever money is known, each
// allocation keeps the owner's normalized direction with a magnitude that
// never exceeds it, and the signed per-owner aggregate stays inside the owner.
// Only internal allocations are checked; missing or over-real registrations
// elsewhere stay registrable facts. Explicit drafts may stay fully unknown or
// know the quantity while all three money measures are still unknown; any
// other partial nullness is rejected and null is never read as zero.
func (rule Rule) validateAllocation(after Images, decimals map[string]exactdecimal.Format) error {
	spec := rule.Allocation
	formats := [4]exactdecimal.Format{
		decimals[rule.Entity+"\x00"+spec.Quantity],
		decimals[rule.Entity+"\x00"+spec.Net],
		decimals[rule.Entity+"\x00"+spec.Tax],
		decimals[rule.Entity+"\x00"+spec.Gross],
	}
	totals := map[string]*allocationOwnerTotals{}
	ownerParentMoney := map[string][3]*big.Int{}
	for _, row := range after[rule.Entity] {
		ownerID, err := relationRef(after, row, spec.Owner, spec.Parent.Entity)
		if err != nil {
			return failure("allocation_invalid")
		}
		state, err := authorityValue(after, row, spec.State)
		if err != nil {
			return failure("allocation_invalid")
		}
		purpose, purposeNull, err := authorityNullAware(after, row, spec.Purpose)
		if err != nil {
			return failure("allocation_invalid")
		}
		draft := contains(spec.DraftStates, state)
		money := [3]any{row[spec.Net], row[spec.Tax], row[spec.Gross]}
		known, unknown := true, true
		for _, value := range money {
			if value == nil {
				known = false
			} else {
				unknown = false
			}
		}
		if !draft && (!known || row[spec.Quantity] == nil) {
			return failure("allocation_invalid")
		}
		if bool(spec.RequireCompleteWhenAllocated) && (!known || row[spec.Quantity] == nil) {
			return failure("allocation_invalid")
		}
		if draft && (!known && !unknown || row[spec.Quantity] == nil && !unknown) {
			// Money without a quantity, or partially known money, is never a
			// valid draft state; a known zero stays a real zero.
			return failure("allocation_invalid")
		}
		if row[spec.Quantity] == nil && unknown {
			continue
		}
		parent := after[spec.Parent.Entity][ownerID]
		parentQuantity, err := parsePresent(formats[0], parent[spec.Parent.Quantity])
		if err != nil {
			return err
		}
		// The carrier mapping is decided once per owner row and then applied to
		// every parsed measure: magnitude red owners flip raw positives exactly
		// once; signed owners keep raw signs so anomalies stay visible.
		flip, red, err := ownerFlipsMagnitude(after, parent, spec.Parent.Direction)
		if err != nil {
			return err
		}
		signed := func(value *big.Int) *big.Int {
			if flip && value != nil {
				return new(big.Int).Neg(value)
			}
			return value
		}
		quantity, err := parsePresent(formats[0], row[spec.Quantity])
		if err != nil {
			return err
		}
		if !purposeNull && contains(spec.ZeroQuantityPurposes, purpose) && quantity.Sign() != 0 {
			return failure("allocation_mismatch")
		}
		parentQuantity = signed(parentQuantity)
		if spec.Parent.Direction != nil && (purposeNull || !contains(spec.ZeroQuantityPurposes, purpose)) &&
			(red && parentQuantity.Sign() > 0 || !red && parentQuantity.Sign() < 0) {
			return failure("allocation_mismatch")
		}
		childValues := [4]*big.Int{quantity, nil, nil, nil}
		if quantity.Sign() != 0 && directionMismatch(quantity, parentQuantity) {
			return failure("allocation_mismatch")
		}
		if quantity.Sign() != 0 && new(big.Int).Abs(quantity).Cmp(new(big.Int).Abs(parentQuantity)) > 0 {
			return failure("allocation_exceeded")
		}
		if known {
			var childMoney [3]*big.Int
			for i, value := range money {
				parsed, err := parsePresent(formats[i+1], value)
				if err != nil {
					return err
				}
				childMoney[i] = parsed
			}
			if new(big.Int).Add(childMoney[0], childMoney[1]).Cmp(childMoney[2]) != 0 {
				return failure("allocation_mismatch")
			}
			parentMoney := [3]*big.Int{}
			for i, names := range [][2]string{{spec.Net, spec.Parent.Net}, {spec.Tax, spec.Parent.Tax}, {spec.Gross, spec.Parent.Gross}} {
				parsed, err := parsePresent(formats[i+1], parent[names[1]])
				if err != nil {
					return err
				}
				parentMoney[i] = signed(parsed)
				if spec.Parent.Direction != nil && red && parentMoney[i].Sign() > 0 {
					return failure("allocation_mismatch")
				}
			}
			if new(big.Int).Add(parentMoney[0], parentMoney[1]).Cmp(parentMoney[2]) != 0 {
				return failure("allocation_mismatch")
			}
			for i, value := range childMoney {
				childValues[i+1] = value
				if directionMismatch(value, parentMoney[i]) {
					return failure("allocation_mismatch")
				}
				if new(big.Int).Abs(value).Cmp(new(big.Int).Abs(parentMoney[i])) > 0 {
					return failure("allocation_exceeded")
				}
			}
			stored := ownerParentMoney[ownerID]
			for i := range parentMoney {
				stored[i] = parentMoney[i]
			}
			ownerParentMoney[ownerID] = stored
		}
		total, ok := totals[ownerID]
		if !ok {
			total = &allocationOwnerTotals{}
			for i := range total.child {
				total.child[i] = new(big.Int)
			}
			totals[ownerID] = total
		}
		for i, value := range childValues {
			if value == nil {
				continue
			}
			if err := formats[i].Add(total.child[i], value); err != nil {
				return err
			}
			if total.parent[i] == nil {
				if i == 0 {
					total.parent[0] = parentQuantity
				} else if moneyParent, stored := ownerParentMoney[ownerID]; stored {
					total.parent[i] = moneyParent[i-1]
				}
			}
		}
	}
	for _, total := range totals {
		for i, sum := range total.child {
			if total.parent[i] == nil {
				if bool(spec.RequireCompleteWhenAllocated) {
					return failure("allocation_invalid")
				}
				continue
			}
			if directionMismatch(sum, total.parent[i]) || new(big.Int).Abs(sum).Cmp(new(big.Int).Abs(total.parent[i])) > 0 {
				return failure("allocation_exceeded")
			}
			if bool(spec.RequireCompleteWhenAllocated) && sum.Cmp(total.parent[i]) != 0 {
				return failure("allocation_mismatch")
			}
		}
	}
	if len(spec.RequireCompleteWhen) != 0 {
		for id, parent := range after[spec.Parent.Entity] {
			matches := true
			for _, condition := range spec.RequireCompleteWhen {
				value, err := authorityValue(after, parent, condition.Ref)
				if err != nil {
					return failure("allocation_invalid")
				}
				matches = matches && value == condition.Equals
			}
			if matches && totals[id] == nil {
				return failure("allocation_mismatch")
			}
		}
	}
	return nil
}

// ownerFlipsMagnitude decides the one-shot carrier mapping for an owner row:
// magnitude red owners flip their raw positive magnitudes to the signed
// internal direction; signed owners keep raw values untouched so anomalous
// signs stay visible to the direction checks.
func ownerFlipsMagnitude(images Images, parent map[string]any, dir *AllocationDirection) (bool, bool, error) {
	if dir == nil {
		return false, false, nil
	}
	id, err := resolvePath(images, parent, dir.Path, false)
	if err != nil {
		return false, false, failure("allocation_invalid")
	}
	holder := images[dir.Path[len(dir.Path)-1].Target][id]
	convention, ok := holder[dir.ConventionField].(string)
	if !ok || (convention != "signed" && convention != "magnitude") {
		return false, false, failure("allocation_invalid")
	}
	direction, ok := holder[dir.DirectionField].(string)
	if !ok {
		return false, false, failure("allocation_invalid")
	}
	red := contains(dir.RedDirections, direction)
	return convention == "magnitude" && red, red, nil
}

// authorityValue resolves one authoritative scalar; an empty path reads the
// row's own field.
func authorityValue(images Images, row map[string]any, ref AuthorityRef) (string, error) {
	if len(ref.Path) == 0 {
		value, ok := row[ref.Field].(string)
		if !ok {
			return "", failure("rules_invalid")
		}
		return value, nil
	}
	id, err := resolvePath(images, row, ref.Path, false)
	if err != nil {
		return "", err
	}
	value, ok := images[ref.Path[len(ref.Path)-1].Target][id][ref.Field].(string)
	if !ok {
		return "", failure("rules_invalid")
	}
	return value, nil
}

// parsePresent parses a required non-null measure; null never becomes zero.
func parsePresent(format exactdecimal.Format, value any) (*big.Int, error) {
	if value == nil {
		return nil, failure("allocation_invalid")
	}
	return format.Parse(value)
}

// authorityNullAware resolves a scalar whose explicit JSON null is a legal
// unknown state; missing keys, wrong types and dangling paths fail closed.
func authorityNullAware(images Images, row map[string]any, ref AuthorityRef) (string, bool, error) {
	read := func(holder map[string]any) (string, bool, error) {
		value, present := holder[ref.Field]
		if !present {
			return "", false, failure("rules_invalid")
		}
		if value == nil {
			return "", true, nil
		}
		text, ok := value.(string)
		if !ok {
			return "", false, failure("rules_invalid")
		}
		return text, false, nil
	}
	if len(ref.Path) == 0 {
		return read(row)
	}
	id, err := resolvePath(images, row, ref.Path, false)
	if err != nil {
		return "", false, err
	}
	return read(images[ref.Path[len(ref.Path)-1].Target][id])
}

// directionMismatch reports whether a signed child value left the owner's
// normalized direction; zero is direction-neutral for any owner value.
func directionMismatch(child, parent *big.Int) bool {
	if child.Sign() == 0 {
		return false
	}
	if parent.Sign() == 0 {
		return true
	}
	return child.Sign() != parent.Sign()
}
