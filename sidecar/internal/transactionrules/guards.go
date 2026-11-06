package transactionrules

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"slices"
	"strconv"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

// ReservationObligation anchors the capacity quantity and the scope of every
// reservation row. Obligations are plain rows; deletion dangles scope references.
type ReservationObligation struct {
	Entity   string `json:"entity"`
	Quantity string `json:"quantity"`
}

// ReservationPhase resolves the authoritative phase through owner relations;
// child self-reported state is never consulted.
type ReservationPhase struct {
	Path                   []RelationStep `json:"path"`
	Field                  string         `json:"field"`
	InstructionDraft       string         `json:"instructionDraft"`
	InstructionOpen        string         `json:"instructionOpen"`
	InstructionClosed      string         `json:"instructionClosed"`
	InstructionTransferred string         `json:"instructionTransferred"`
	DeliveryDraft          string         `json:"deliveryDraft"`
	DeliveryConfirmed      string         `json:"deliveryConfirmed"`
}

type ReservationLogical struct {
	Field  string `json:"field"`
	Target string `json:"target"`
}

// ReservationFieldRef selects the authoritative scalar through 1–4 single-value
// relation hops. The protected row itself never supplies a forgeable copy.
type ReservationFieldRef struct {
	Path  []RelationStep `json:"path"`
	Field string         `json:"field"`
}

// ReservationPlans declares the stable plan bucket structure: quantity, scope,
// the current-version pointer test, and the stable key (logical item + share).
type ReservationPlans struct {
	Entity      string             `json:"entity"`
	Quantity    string             `json:"quantity"`
	Scope       string             `json:"scope"`
	LogicalItem ReservationLogical `json:"logicalItem"`
	Share       string             `json:"share"`
	CurrentWhen PathMatch          `json:"currentWhen"`
}

// ReservationSpec is the closed typed shape of a reservationBalance rule. It
// names fields and relation mappings only; no formulas or scripts are accepted.
type ReservationSpec struct {
	Obligation        ReservationObligation `json:"obligation"`
	Scope             string                `json:"scope"`
	Quantity          string                `json:"quantity"`
	Purpose           ReservationFieldRef   `json:"purpose"`
	NormalPurpose     string                `json:"normalPurpose"`
	Phase             ReservationPhase      `json:"phase"`
	PlanItem          string                `json:"planItem,omitempty"`
	InstructionSource string                `json:"instructionSource,omitempty"`
	TransferredFrom   string                `json:"transferredFrom,omitempty"`
	Plans             *ReservationPlans     `json:"plans,omitempty"`
}

func (s *ReservationSpec) UnmarshalJSON(raw []byte) error {
	type wireReservation ReservationSpec
	var decoded wireReservation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return failure("rules_invalid")
	}
	// plans is nullable in the Python contract; optional string fields are not.
	if fields["plans"] == nil {
		delete(fields, "plans")
	}
	if containsJSONNull(fields) {
		return failure("rules_invalid")
	}
	*s = ReservationSpec(decoded)
	return nil
}

func (s *ReservationSpec) valid() bool {
	if s == nil || s.Obligation.Entity == "" || s.Obligation.Quantity == "" || s.Scope == "" ||
		s.Quantity == "" || s.NormalPurpose == "" {
		return false
	}
	if len(s.Purpose.Path) < 1 || len(s.Purpose.Path) > 4 || s.Purpose.Field == "" {
		return false
	}
	for _, step := range s.Purpose.Path {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	phase := s.Phase
	if len(phase.Path) < 1 || len(phase.Path) > 4 || phase.Field == "" {
		return false
	}
	for _, step := range phase.Path {
		if step.Field == "" || step.Target == "" {
			return false
		}
	}
	states := []string{phase.InstructionDraft, phase.InstructionOpen, phase.InstructionClosed,
		phase.InstructionTransferred, phase.DeliveryDraft, phase.DeliveryConfirmed}
	seen := map[string]bool{}
	for _, state := range states {
		if state == "" || seen[state] {
			return false
		}
		seen[state] = true
	}
	if s.InstructionSource == "" && s.TransferredFrom != "" {
		return false
	}
	if s.Plans != nil {
		plans := s.Plans
		if plans.Entity == "" || plans.Quantity == "" || plans.Scope == "" ||
			plans.LogicalItem.Field == "" || plans.LogicalItem.Target == "" || plans.Share == "" ||
			s.PlanItem == "" || !plans.CurrentWhen.valid(plans.Entity) {
			return false
		}
	} else if s.PlanItem != "" {
		return false
	}
	return true
}

// validateParentFreeze freezes declared child core fields, deletion and moves
// while the resolved parent was frozen before this transaction. A parent that
// only appears in the after image (created or confirmed in the same transaction)
// never freezes existing child facts; extensions outside fields stay editable.
func (rule Rule) validateParentFreeze(before, after Images) error {
	parentEntity := rule.Entity
	for _, step := range rule.ParentPath {
		parentEntity = step.Target
	}
	frozenIn := func(images Images, parentID string) bool {
		parent := images[parentEntity][parentID]
		if parent == nil {
			return false
		}
		state, ok := parent[rule.ParentStateField].(string)
		return ok && contains(rule.FrozenStates, state)
	}
	identities := func(images Images, row map[string]any) ([]string, error) {
		ids := make([]string, 0, len(rule.ParentPath))
		for _, step := range rule.ParentPath {
			id, err := relationRef(images, row, step.Field, step.Target)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
			row = images[step.Target][id]
		}
		return ids, nil
	}
	for id, old := range before[rule.Entity] {
		oldPath, err := identities(before, old)
		if err != nil {
			return err
		}
		oldFrozen := frozenIn(before, oldPath[len(oldPath)-1])
		next := after[rule.Entity][id]
		if next == nil {
			if oldFrozen {
				return failure("parent_frozen")
			}
			continue
		}
		newPath, err := identities(after, next)
		if err != nil {
			return err
		}
		if !slices.Equal(oldPath, newPath) && (oldFrozen || frozenIn(before, newPath[len(newPath)-1])) {
			return failure("parent_frozen")
		}
		if oldFrozen {
			for _, field := range rule.Fields {
				if !reflect.DeepEqual(old[field], next[field]) {
					return failure("parent_frozen")
				}
			}
		}
	}
	for id, row := range after[rule.Entity] {
		if before[rule.Entity][id] != nil {
			continue
		}
		parent, err := resolvePath(after, row, rule.ParentPath, false)
		if err != nil {
			return err
		}
		if frozenIn(before, parent) {
			return failure("parent_frozen")
		}
	}
	return nil
}

type reservationRow struct {
	id          string
	role        string // instruction | actual
	state       string // draft/open/closed/transferred or draft/confirmed
	purpose     string // authoritative purpose read through the owner path
	normal      bool
	quantity    *big.Int
	scope       string
	planItem    string
	instruction string
	transferred string
}

// validateReservation enforces the closed native quantity model: same-purpose
// instruction execution X, normal-only fulfillment E and normal capacity
// remainder R, purpose-preserving transfer chains, stable plan buckets and the
// combined obligation capacity. All judgments use the authoritative phase and
// purpose reached through the declared owner path; UI-supplied balances are
// never trusted, and every read stays inside the shared budget.
func (rule Rule) validateReservation(before, after Images, decimals map[string]exactdecimal.Format) error {
	res := rule.Reservation
	quantityFormat := decimals[rule.Entity+"\x00"+res.Quantity]
	parse := func(value any) (*big.Int, error) { return quantityFormat.Parse(value) }
	beforeBuckets, err := rule.planBuckets(before, decimals)
	if err != nil {
		return err
	}
	buckets, err := rule.planBuckets(after, decimals)
	if err != nil {
		return err
	}
	classify := func(images Images, plans reservationPlanIndex) (map[string]*reservationRow, error) {
		rows := map[string]*reservationRow{}
		phaseEntity := rule.Entity
		for _, step := range res.Phase.Path {
			phaseEntity = step.Target
		}
		for id, row := range images[rule.Entity] {
			document, err := resolvePath(images, row, res.Phase.Path, false)
			if err != nil {
				return nil, err
			}
			state, ok := images[phaseEntity][document][res.Phase.Field].(string)
			if !ok {
				return nil, failure("reservation_invalid")
			}
			next := &reservationRow{id: id}
			switch state {
			case res.Phase.InstructionDraft:
				next.role, next.state = "instruction", "draft"
			case res.Phase.InstructionOpen:
				next.role, next.state = "instruction", "open"
			case res.Phase.InstructionClosed:
				next.role, next.state = "instruction", "closed"
			case res.Phase.InstructionTransferred:
				next.role, next.state = "instruction", "transferred"
			case res.Phase.DeliveryDraft:
				next.role, next.state = "actual", "draft"
			case res.Phase.DeliveryConfirmed:
				next.role, next.state = "actual", "confirmed"
			default:
				return nil, failure("reservation_invalid")
			}
			purposeID, err := resolvePath(images, row, res.Purpose.Path, false)
			if err != nil {
				return nil, err
			}
			purposeRow := images[res.Purpose.Path[len(res.Purpose.Path)-1].Target][purposeID]
			purpose, ok := purposeRow[res.Purpose.Field].(string)
			if !ok || purpose == "" {
				return nil, failure("reservation_invalid")
			}
			next.normal = purpose == res.NormalPurpose
			next.purpose = purpose
			quantity, err := parse(row[res.Quantity])
			if err != nil {
				return nil, err
			}
			next.quantity = quantity
			scope, err := relationRef(images, row, res.Scope, res.Obligation.Entity)
			if err != nil {
				return nil, err
			}
			next.scope = scope
			if res.PlanItem != "" {
				item, err := optionalRef(images, row, res.PlanItem, res.Plans.Entity)
				if err != nil {
					return nil, err
				}
				next.planItem = item
				if item != "" && plans.items[item].scope != scope {
					return nil, failure("reservation_invalid")
				}
			}
			if res.InstructionSource != "" {
				target, err := optionalRef(images, row, res.InstructionSource, rule.Entity)
				if err != nil {
					return nil, err
				}
				next.instruction = target
			}
			if res.TransferredFrom != "" {
				target, err := optionalRef(images, row, res.TransferredFrom, rule.Entity)
				if err != nil {
					return nil, err
				}
				next.transferred = target
			}
			rows[id] = next
		}
		return rows, nil
	}
	beforeRows, err := classify(before, beforeBuckets)
	if err != nil {
		return err
	}
	afterRows, err := classify(after, buckets)
	if err != nil {
		return err
	}
	// Instruction rows masquerading as allocations are rejected outright.
	for _, row := range afterRows {
		if row.role == "instruction" && row.instruction != "" {
			return failure("reservation_invalid")
		}
	}
	// Live instructions, confirmed consumers and confirmed normal actuals never
	// disappear; identity fields are immutable so recorded execution (X) and
	// normal occupation (E) can never be released by edits or deletion.
	for id, old := range beforeRows {
		next := afterRows[id]
		// A source ID alone cannot preserve history if its referenced plan's
		// canonical scope/logical item/share is rewritten underneath it.
		if old.planItem != "" && (old.role == "instruction" && old.state != "draft" || old.role == "actual" && old.state == "confirmed") {
			if beforeBuckets.items[old.planItem] != buckets.items[old.planItem] {
				return failure("reservation_frozen")
			}
		}
		if next == nil {
			if old.role == "instruction" && old.state != "draft" {
				return failure("reservation_frozen")
			}
			if old.role == "actual" && old.state == "confirmed" && (old.normal || old.instruction != "") {
				return failure("reservation_frozen")
			}
			continue
		}
		if next.role != old.role {
			return failure("reservation_frozen")
		}
		if old.role == "instruction" && old.state != "draft" {
			if next.quantity.Cmp(old.quantity) != 0 || next.scope != old.scope || next.planItem != old.planItem || next.transferred != old.transferred || next.purpose != old.purpose {
				return failure("reservation_frozen")
			}
			if old.state == "closed" || old.state == "transferred" {
				if next.state != old.state {
					return failure("reservation_frozen")
				}
			} else if next.state == "draft" {
				return failure("reservation_frozen")
			}
		}
		// Confirmed consumption purposes are strictly frozen: any purpose flip or
		// rebinding would move recorded execution between purposes and restore X.
		if old.role == "actual" && old.state == "confirmed" && old.instruction != "" {
			if next.state != "confirmed" || next.purpose != old.purpose || next.scope != old.scope ||
				next.planItem != old.planItem || next.instruction != old.instruction {
				return failure("reservation_frozen")
			}
			if next.quantity.Cmp(old.quantity) < 0 {
				return failure("reservation_frozen")
			}
		} else if old.role == "actual" && old.state == "confirmed" && old.normal {
			if next.state != "confirmed" || !next.normal || next.scope != old.scope ||
				next.planItem != old.planItem || next.instruction != old.instruction {
				return failure("reservation_frozen")
			}
			if next.quantity.Cmp(old.quantity) < 0 {
				return failure("reservation_frozen")
			}
		}
	}
	// Reference consistency for every allocation row: consumption requires the
	// identical authoritative purpose, scope and stable share.
	for _, row := range afterRows {
		if row.role != "actual" || row.instruction == "" {
			continue
		}
		source := afterRows[row.instruction]
		if source == nil || source.role != "instruction" || source.state == "draft" {
			return failure("reservation_invalid")
		}
		if source.purpose != row.purpose || source.scope != row.scope || source.planItem != row.planItem {
			return failure("reservation_invalid")
		}
	}
	// Confirmed same-purpose execution (X) per instruction source, for every
	// purpose; the consistency loop above guarantees the purpose equality.
	consumedAfter := map[string]*big.Int{}
	for _, row := range afterRows {
		if row.role != "actual" || row.state != "confirmed" || row.instruction == "" {
			continue
		}
		total := consumedAfter[row.instruction]
		if total == nil {
			total = new(big.Int)
			consumedAfter[row.instruction] = total
		}
		total.Add(total, row.quantity)
	}
	consumedBefore := map[string]*big.Int{}
	for _, row := range beforeRows {
		if row.role != "actual" || row.state != "confirmed" || row.instruction == "" {
			continue
		}
		total := consumedBefore[row.instruction]
		if total == nil {
			total = new(big.Int)
			consumedBefore[row.instruction] = total
		}
		total.Add(total, row.quantity)
	}
	zero := new(big.Int)
	for id, row := range afterRows {
		if row.role != "instruction" {
			continue
		}
		x := consumedAfter[id]
		if x == nil {
			x = zero
		}
		if x.Cmp(row.quantity) > 0 {
			return failure("reservation_exhausted")
		}
	}
	// Recorded consumption is monotonic: refunds or purpose edits never reopen
	// an already-consumed instruction source.
	for id := range beforeRows {
		if beforeRows[id].role != "instruction" {
			continue
		}
		after2 := consumedAfter[id]
		if after2 == nil {
			after2 = zero
		}
		prior := consumedBefore[id]
		if prior == nil {
			prior = zero
		}
		if after2.Cmp(prior) < 0 {
			return failure("reservation_released")
		}
	}
	// New or increased confirmed consumption may only target instructions that were open
	// before this transaction or were opened inside it, for every purpose.
	for _, row := range afterRows {
		if row.role != "actual" || row.state != "confirmed" || row.instruction == "" {
			continue
		}
		prior := beforeRows[row.id]
		if prior != nil && prior.role == "actual" && prior.state == "confirmed" && prior.instruction != "" && row.quantity.Cmp(prior.quantity) <= 0 {
			continue
		}
		source := beforeRows[row.instruction]
		if source != nil && source.state != "open" {
			opened := afterRows[row.instruction]
			if source.state != "draft" || opened == nil || opened.state != "open" {
				return failure("reservation_invalid")
			}
		}
	}
	// Transfer chains: identical purpose, scope and stable share, exact
	// remainder, no duplicates, self references, cycles or zero-remainder rows.
	if res.TransferredFrom != "" {
		targets := map[string][]string{}
		for _, row := range afterRows {
			if row.transferred == "" {
				continue
			}
			// Historical carriers may later close or transfer again; only a draft
			// carrier or a non-instruction row is structurally invalid.
			if row.role != "instruction" || row.state == "draft" {
				return failure("reservation_transfer_invalid")
			}
			if row.transferred == row.id {
				return failure("reservation_transfer_invalid")
			}
			source := afterRows[row.transferred]
			if source == nil || source.role != "instruction" || source.state != "transferred" {
				return failure("reservation_transfer_invalid")
			}
			if source.purpose != row.purpose || source.scope != row.scope || source.planItem != row.planItem {
				return failure("reservation_transfer_invalid")
			}
			targets[row.transferred] = append(targets[row.transferred], row.id)
		}
		for _, list := range targets {
			if len(list) > 1 {
				return failure("reservation_transfer_invalid")
			}
		}
		for _, row := range afterRows {
			visited := map[string]bool{}
			for current := row; current != nil && current.transferred != ""; {
				if visited[current.id] {
					return failure("reservation_transfer_invalid")
				}
				visited[current.id] = true
				current = afterRows[current.transferred]
			}
		}
		for id, row := range afterRows {
			if row.role != "instruction" || row.state != "transferred" {
				continue
			}
			prior := beforeRows[id]
			newlyTransferred := prior == nil || prior.state != "transferred"
			consumed := consumedAfter[id]
			if consumed == nil {
				consumed = zero
			}
			remainder := new(big.Int).Sub(row.quantity, consumed)
			if remainder.Sign() <= 0 {
				if len(targets[id]) > 0 {
					return failure("reservation_transfer_invalid")
				}
				continue
			}
			if len(targets[id]) != 1 {
				return failure("reservation_transfer_invalid")
			}
			target := afterRows[targets[id][0]]
			if target.quantity.Cmp(remainder) != 0 {
				return failure("reservation_transfer_invalid")
			}
			if newlyTransferred {
				if beforeRows[target.id] != nil || target.state != "open" {
					return failure("reservation_transfer_invalid")
				}
				if prior != nil && prior.state != "open" {
					return failure("reservation_transfer_invalid")
				}
			}
		}
	}
	// Stable plan buckets and the combined obligation capacity.
	bucketOf := func(row *reservationRow) (reservationBucket, bool) {
		item := row.planItem
		if item == "" && row.instruction != "" {
			if source := afterRows[row.instruction]; source != nil {
				item = source.planItem
			}
		}
		if item == "" {
			return reservationBucket{}, false
		}
		key, ok := buckets.items[item]
		return key, ok
	}
	executed := map[reservationBucket]*big.Int{}
	openRemainder := map[reservationBucket]*big.Int{}
	direct := map[string]*big.Int{}
	directOpen := map[string]*big.Int{}
	addDirect := func(totals map[string]*big.Int, scope string, value *big.Int) {
		if totals[scope] == nil {
			totals[scope] = new(big.Int)
		}
		totals[scope].Add(totals[scope], value)
	}
	for _, row := range afterRows {
		if row.role == "actual" {
			if row.state != "confirmed" || !row.normal || row.quantity.Sign() <= 0 {
				continue
			}
			if key, ok := bucketOf(row); ok {
				addTotal(executed, key, row.quantity)
			} else {
				addDirect(direct, row.scope, row.quantity)
			}
			continue
		}
		if row.state != "open" {
			continue
		}
		// Only the normal purpose occupies the normal obligation capacity; other
		// purposes run their own X/R ledger without consuming the normal quota.
		if row.purpose != res.NormalPurpose {
			continue
		}
		x := consumedAfter[row.id]
		if x == nil {
			x = zero
		}
		remainder := new(big.Int).Sub(row.quantity, x)
		if remainder.Sign() < 0 {
			return failure("reservation_exhausted")
		}
		if key, ok := bucketOf(row); ok {
			addTotal(openRemainder, key, remainder)
		} else {
			addDirect(directOpen, row.scope, remainder)
		}
	}
	occupied := map[string]*big.Int{}
	occupy := func(scope string, value *big.Int) {
		if occupied[scope] == nil {
			occupied[scope] = new(big.Int)
		}
		occupied[scope].Add(occupied[scope], value)
	}
	for key, planned := range buckets.current {
		used := new(big.Int)
		if e := executed[key]; e != nil {
			used.Add(used, e)
		}
		if r := openRemainder[key]; r != nil {
			used.Add(used, r)
		}
		if used.Cmp(planned) > 0 {
			return failure("reservation_exceeded")
		}
		occupy(key.scope, planned)
	}
	seen := map[reservationBucket]bool{}
	for key := range executed {
		seen[key] = true
	}
	for key := range openRemainder {
		seen[key] = true
	}
	for key := range seen {
		if _, planned := buckets.current[key]; planned {
			continue
		}
		// A replacement plan version may never drop or shrink an executed
		// stable share; only series whose buckets carry no execution may close.
		if e := executed[key]; e != nil && e.Sign() > 0 {
			return failure("reservation_plan_required")
		}
		used := new(big.Int)
		if e := executed[key]; e != nil {
			used.Add(used, e)
		}
		if r := openRemainder[key]; r != nil {
			used.Add(used, r)
		}
		occupy(key.scope, used)
	}
	for scope, amount := range direct {
		occupy(scope, amount)
	}
	for scope, amount := range directOpen {
		occupy(scope, amount)
	}
	for id, row := range after[res.Obligation.Entity] {
		limit, err := quantityFormat.Parse(row[res.Obligation.Quantity])
		if err != nil {
			return err
		}
		total := occupied[id]
		if total == nil {
			total = zero
		}
		if total.Cmp(limit) > 0 {
			return failure("reservation_exceeded")
		}
	}
	return nil
}

func addTotal(totals map[reservationBucket]*big.Int, key reservationBucket, value *big.Int) {
	if totals[key] == nil {
		totals[key] = new(big.Int)
	}
	totals[key].Add(totals[key], value)
}

type reservationBucket struct {
	scope   string
	logical string
	share   string
}

type reservationPlanIndex struct {
	items   map[string]reservationBucket
	current map[reservationBucket]*big.Int
}

func (rule Rule) planBuckets(after Images, decimals map[string]exactdecimal.Format) (reservationPlanIndex, error) {
	index := reservationPlanIndex{items: map[string]reservationBucket{}, current: map[reservationBucket]*big.Int{}}
	res := rule.Reservation
	if res.Plans == nil {
		return index, nil
	}
	plans := res.Plans
	format := decimals[plans.Entity+"\x00"+plans.Quantity]
	for id, row := range after[plans.Entity] {
		scope, err := relationRef(after, row, plans.Scope, res.Obligation.Entity)
		if err != nil {
			return index, err
		}
		logical, err := relationRef(after, row, plans.LogicalItem.Field, plans.LogicalItem.Target)
		if err != nil {
			return index, err
		}
		share, ok := shareValue(row[plans.Share])
		if !ok {
			return index, failure("reservation_invalid")
		}
		key := reservationBucket{scope: scope, logical: logical, share: share}
		index.items[id] = key
		current, err := plans.CurrentWhen.matches(after, row, id, pathEndPolicy{allowEmptyRight: true})
		if err != nil {
			return index, err
		}
		if !current {
			continue
		}
		quantity, err := format.Parse(row[plans.Quantity])
		if err != nil {
			return index, err
		}
		addTotal(index.current, key, quantity)
	}
	return index, nil
}

// relationRef resolves a required single relation; dangling or mistyped values
// fail closed instead of silently skipping the protected row.
func relationRef(images Images, row map[string]any, field, target string) (string, error) {
	value, present := row[field]
	if !present {
		return "", failure("reference_violation")
	}
	id, ok := value.(string)
	if !ok || id == "" || images[target][id] == nil {
		return "", failure("reference_violation")
	}
	return id, nil
}

func optionalRef(images Images, row map[string]any, field, target string) (string, error) {
	value, present := row[field]
	if !present || value == nil || value == "" {
		return "", nil
	}
	id, ok := value.(string)
	if !ok || id == "" || images[target][id] == nil {
		return "", failure("reference_violation")
	}
	return id, nil
}

// shareValue accepts only integral non-negative share declarations.
func shareValue(value any) (string, bool) {
	switch share := value.(type) {
	case int:
		if share >= 0 {
			return strconv.Itoa(share), true
		}
	case int64:
		if share >= 0 {
			return strconv.FormatInt(share, 10), true
		}
	case float64:
		if share >= 0 && share == float64(int64(share)) {
			return strconv.FormatInt(int64(share), 10), true
		}
	}
	return "", false
}
