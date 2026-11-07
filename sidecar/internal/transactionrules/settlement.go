package transactionrules

import (
	"math/big"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
)

// SettlementCapacity anchors the budget rows. In the red bucket the same
// invoice_allocations entity and the same four signed columns serve as the
// original blue allocations that both request sources and actual allocations
// reference; the declaration therefore never forces these columns
// non-negative — the runtime verifies the blue role, the non-self reference
// and the four non-negative measures of every referenced capacity row. In the
// normal bucket the capacity is the delivery obligation quantity.
type SettlementCapacity struct {
	Entity     string       `json:"entity"`
	Currency   AuthorityRef `json:"currency,omitzero"`
	Unit       AuthorityRef `json:"unit,omitzero"`
	Material   AuthorityRef `json:"material,omitzero"`
	Direction  AuthorityRef `json:"direction,omitzero"`
	Directions []string     `json:"directions,omitempty"`
	Quantity   string       `json:"quantity,omitempty"`
	Net        string       `json:"net,omitempty"`
	Tax        string       `json:"tax,omitempty"`
	Gross      string       `json:"gross,omitempty"`
	// Optional marks the row-level capacity reference as nullable on an
	// external capacity: only a present null means an unknown capacity and
	// skips that row's external aggregation and capacity-only scope checks;
	// missing, empty, mistyped and dangling references still fail closed. The
	// red bucket sharing one entity never opts in.
	Optional enabledOption `json:"optional,omitempty"`
}

// SettlementRequest names the notification request source rows. The
// authoritative direction (blue/red) on the request header selects the role
// first; the independent purpose ref filters selected rows and the equally
// independent basis ref (quantity/amount) decides the carrier policy:
// amount-basis sources carry an explicit zero quantity and close only through
// amount events or closeRemaining, quantity-basis sources keep Q>0 with the
// proportional split. Request sources are reservation scope only.
type SettlementRequest struct {
	Entity      string       `json:"entity"`
	State       AuthorityRef `json:"state"`
	Direction   AuthorityRef `json:"direction"`
	Directions  []string     `json:"directions"`
	Purpose     AuthorityRef `json:"purpose,omitzero"`
	Purposes    []string     `json:"purposes,omitempty"`
	Basis       AuthorityRef `json:"basis,omitzero"`
	Currency    AuthorityRef `json:"currency,omitzero"`
	Unit        AuthorityRef `json:"unit,omitzero"`
	Material    AuthorityRef `json:"material,omitzero"`
	DraftStates []string     `json:"draftStates,omitempty"`
	OpenStates  []string     `json:"openStates,omitempty"`
	Capacity    string       `json:"capacity"`
	PlanItem    string       `json:"planItem,omitempty"`
	Quantity    string       `json:"quantity,omitempty"`
	Net         string       `json:"net,omitempty"`
	Tax         string       `json:"tax,omitempty"`
	Gross       string       `json:"gross,omitempty"`
}

// SettlementActual names the real invoice rows. The authoritative direction
// (blue/red) on the invoice header selects the role first; the independent
// line-level purpose ref (redKind such as physicalReturn/priceDiscount, null
// on blue lines) then filters the selected rows. One red invoice may mix
// physicalReturn and priceDiscount lines. After selection the rule consumes
// the magnitude of the same one-shot signed quantity the allocation rule
// reads on these rows; no second writable effect copy exists.
type SettlementActual struct {
	Entity               string       `json:"entity"`
	State                AuthorityRef `json:"state"`
	Direction            AuthorityRef `json:"direction"`
	Directions           []string     `json:"directions"`
	Currency             AuthorityRef `json:"currency,omitzero"`
	Unit                 AuthorityRef `json:"unit,omitzero"`
	Material             AuthorityRef `json:"material,omitzero"`
	Purpose              AuthorityRef `json:"purpose,omitzero"`
	Purposes             []string     `json:"purposes,omitempty"`
	DraftStates          []string     `json:"draftStates,omitempty"`
	SettledStates        []string     `json:"settledStates"`
	Request              string       `json:"request"`
	Capacity             string       `json:"capacity"`
	PlanItem             string       `json:"planItem,omitempty"`
	Quantity             string       `json:"quantity"`
	Net                  string       `json:"net,omitempty"`
	Tax                  string       `json:"tax,omitempty"`
	Gross                string       `json:"gross,omitempty"`
	ZeroQuantityPurposes []string     `json:"zeroQuantityPurposes,omitempty"`
}

// SettlementAdjustment names the shared explicit disposal events. The kind
// field first selects close events and amount-fulfillment events; other kinds
// (actual corrections) are not settlement facts for this bucket. Close events
// require the request source; fulfillment events additionally require a real
// actual allocation of the same source and capacity.
type SettlementAdjustment struct {
	Entity         string   `json:"entity"`
	Kind           string   `json:"kind"`
	CloseKinds     []string `json:"closeKinds,omitempty"`
	FulfillKinds   []string `json:"fulfillKinds,omitempty"`
	Target         string   `json:"target"`
	Allocation     string   `json:"allocation,omitempty"`
	CloseRemaining string   `json:"closeRemaining"`
	CloseQuantity  string   `json:"closeQuantity,omitempty"`
	CloseNet       string   `json:"closeNet,omitempty"`
	CloseTax       string   `json:"closeTax,omitempty"`
	CloseGross     string   `json:"closeGross,omitempty"`
}

// SettlementSpec is the closed typed shape of a settlementBalance rule. One
// declaration covers one shared budget bucket: all request purposes in the
// list and all actual purposes in the list share the capacity budget, so
// different reversal reasons naturally share one original blueprint bucket
// while a separate declaration keeps the normal bucket independent.
type SettlementSpec struct {
	Capacity   SettlementCapacity    `json:"capacity"`
	Request    SettlementRequest     `json:"request"`
	Actual     SettlementActual      `json:"actual"`
	Adjustment *SettlementAdjustment `json:"adjustment,omitempty"`
	// Plans reuses the delivery reservation plan structure for the normal
	// bucket: current plan buckets reserve their full quantity, direct sources
	// and executions occupy the obligation directly.
	Plans *ReservationPlans `json:"plans,omitempty"`
}

func (s *SettlementSpec) valid() bool {
	if s == nil || s.Capacity.Entity == "" || s.Request.Entity == "" || s.Actual.Entity == "" ||
		!s.Request.State.valid() || !s.Request.Direction.valid() || !s.Actual.State.valid() || !s.Actual.Direction.valid() ||
		s.Request.Capacity == "" || s.Actual.Request == "" || s.Actual.Capacity == "" || s.Request.Quantity != "" && s.Actual.Quantity == "" ||
		len(s.Capacity.Directions) > MaxFrozenStates || len(s.Request.Directions) < 1 || len(s.Request.Directions) > MaxFrozenStates || len(s.Actual.Directions) < 1 || len(s.Actual.Directions) > MaxFrozenStates ||
		len(s.Request.Purposes) > MaxFrozenStates || len(s.Actual.Purposes) > MaxFrozenStates ||
		len(s.Request.DraftStates) > MaxFrozenStates || len(s.Request.OpenStates) > MaxFrozenStates ||
		len(s.Actual.DraftStates) > MaxFrozenStates || len(s.Actual.SettledStates) < 1 || len(s.Actual.SettledStates) > MaxFrozenStates || len(s.Actual.ZeroQuantityPurposes) > MaxFrozenStates {
		return false
	}
	if len(s.Request.Purposes) > 0 && !s.Request.Purpose.valid() || (len(s.Actual.Purposes) > 0 || len(s.Actual.ZeroQuantityPurposes) > 0) && !s.Actual.Purpose.valid() {
		return false
	}
	if s.Request.Basis.Field != "" && !s.Request.Basis.valid() {
		return false
	}
	for _, refs := range [][3]AuthorityRef{{s.Capacity.Currency, s.Request.Currency, s.Actual.Currency}, {s.Capacity.Unit, s.Request.Unit, s.Actual.Unit}, {s.Capacity.Material, s.Request.Material, s.Actual.Material}} {
		// request and actual declare a scope dimension as a pair; the capacity
		// keeps its own ref optional and never widens an undeclared pair.
		if (refs[1].Field != "") != (refs[2].Field != "") || refs[0].Field != "" && refs[1].Field == "" {
			return false
		}
		for _, ref := range refs {
			if ref.Field != "" && !ref.valid() {
				return false
			}
		}
	}
	if !uniqueStates(s.Request.Directions) || !uniqueStates(s.Actual.Directions) || !uniqueStates(s.Capacity.Directions) ||
		!uniqueStates(s.Request.Purposes) || !uniqueStates(s.Actual.Purposes) ||
		!uniqueStates(s.Request.DraftStates) || !uniqueStates(s.Request.OpenStates) ||
		!uniqueStates(s.Actual.DraftStates) || !uniqueStates(s.Actual.SettledStates) || !uniqueStates(s.Actual.ZeroQuantityPurposes) {
		return false
	}
	if len(s.Capacity.Directions) > 0 && !s.Capacity.Direction.valid() {
		return false
	}
	if s.Plans != nil {
		plans := s.Plans
		if !s.hasQuantity() || s.hasNet() || s.hasTax() || s.hasGross() || plans.Entity == "" || plans.Quantity == "" || plans.Scope == "" || plans.LogicalItem.Field == "" ||
			plans.LogicalItem.Target == "" || plans.Share == "" || s.Request.PlanItem == "" || !plans.CurrentWhen.valid(plans.Entity) {
			return false
		}
	}
	if adj := s.Adjustment; adj != nil {
		if adj.Entity == "" || adj.Kind == "" || adj.Target == "" || adj.CloseRemaining == "" ||
			len(adj.CloseKinds)+len(adj.FulfillKinds) < 1 || len(adj.CloseKinds) > MaxFrozenStates || len(adj.FulfillKinds) > MaxFrozenStates ||
			!uniqueStates(adj.CloseKinds) || !uniqueStates(adj.FulfillKinds) || len(adj.FulfillKinds) > 0 && adj.Allocation == "" {
			return false
		}
		for _, kind := range adj.CloseKinds {
			if contains(adj.FulfillKinds, kind) {
				return false
			}
		}
	}
	for _, draft := range s.Request.DraftStates {
		if contains(s.Request.OpenStates, draft) {
			return false
		}
	}
	for _, draft := range s.Actual.DraftStates {
		if contains(s.Actual.SettledStates, draft) {
			return false
		}
	}
	if !s.sameMeasures() {
		return false
	}
	if adj := s.Adjustment; adj != nil {
		if s.requestHasQuantity() && adj.CloseQuantity == "" || !s.requestHasQuantity() && adj.CloseQuantity != "" {
			return false
		}
		for _, pair := range [][2]bool{{s.requestHasNet(), adj.CloseNet != ""}, {s.requestHasTax(), adj.CloseTax != ""}, {s.requestHasGross(), adj.CloseGross != ""}} {
			if !pair[0] && pair[1] {
				return false
			}
		}
	}
	return true
}

func (s SettlementSpec) hasQuantity() bool { return s.Capacity.Quantity != "" }
func (s SettlementSpec) hasNet() bool      { return s.Capacity.Net != "" }
func (s SettlementSpec) hasTax() bool      { return s.Capacity.Tax != "" }
func (s SettlementSpec) hasGross() bool    { return s.Capacity.Gross != "" }

func (s SettlementSpec) requestHasQuantity() bool { return s.Request.Quantity != "" }
func (s SettlementSpec) requestHasNet() bool      { return s.Request.Net != "" }
func (s SettlementSpec) requestHasTax() bool      { return s.Request.Tax != "" }
func (s SettlementSpec) requestHasGross() bool    { return s.Request.Gross != "" }

// requestMeasure reports a measure of the source's complete settlement set.
func requestMeasure(spec SettlementSpec, index int) bool {
	switch index {
	case 0:
		return spec.requestHasQuantity()
	case 1:
		return spec.requestHasNet()
	case 2:
		return spec.requestHasTax()
	default:
		return spec.requestHasGross()
	}
}

// sameMeasures keeps request and actual on one identical non-empty subset of
// the fixed four names while the capacity declares any non-empty subset of
// that source set, so the external budget never forces the sources to drop
// their complete settlement measures. The red bucket keeps the identical
// four-measure shared original-blue bucket and never enables optional.
func (s SettlementSpec) sameMeasures() bool {
	count := 0
	for _, declared := range []bool{s.requestHasQuantity(), s.requestHasNet(), s.requestHasTax(), s.requestHasGross()} {
		if declared {
			count++
		}
	}
	if count < 1 {
		return false
	}
	capacityCount := 0
	for _, declared := range []bool{s.hasQuantity(), s.hasNet(), s.hasTax(), s.hasGross()} {
		if declared {
			capacityCount++
		}
	}
	if capacityCount < 1 {
		return false
	}
	shared := s.Capacity.Entity == s.Actual.Entity
	if shared && bool(s.Capacity.Optional) {
		return false
	}
	for _, pair := range [][3]string{
		{s.Request.Quantity, s.Actual.Quantity, s.Capacity.Quantity},
		{s.Request.Net, s.Actual.Net, s.Capacity.Net},
		{s.Request.Tax, s.Actual.Tax, s.Capacity.Tax},
		{s.Request.Gross, s.Actual.Gross, s.Capacity.Gross},
	} {
		requestField, actualField, capacityField := pair[0], pair[1], pair[2]
		if (requestField != "") != (actualField != "") {
			return false
		}
		if capacityField != "" && requestField == "" {
			return false
		}
		if shared && (capacityField != "") != (requestField != "") {
			return false
		}
	}
	return true
}

type settlementBucket struct {
	measures [4]*big.Int
}

func newSettlementBucket() *settlementBucket {
	bucket := &settlementBucket{}
	for i := range bucket.measures {
		bucket.measures[i] = new(big.Int)
	}
	return bucket
}

// validateSettlement enforces the closed settlement model on the final graph.
// Rows are first selected by their authoritative direction, then the line
// purpose filters the selected rows and the independent basis decides the
// carrier policy. Selected formal rows must reference a valid capacity: in the
// red bucket the same invoice_allocations entity serves both sides, so every
// referenced original must be blue-roled, non-self and non-negative on its four
// stored measures. New or increased consumption may only target sources that
// were open before this transaction or were born inside it; a source closed by
// a closeRemaining event keeps its history but takes no new consumption. The
// optional plans structure reuses the delivery reservation buckets: current
// plans reserve their full quantity, bucketed execution and open remainder
// stay inside the plan, direct sources and executions occupy the obligation.
func (rule Rule) validateSettlement(before, after Images, decimals map[string]exactdecimal.Format) error {
	spec := *rule.Settlement
	capacity, request, actual := spec.Capacity, spec.Request, spec.Actual
	quantityFormat := exactdecimal.Format{}
	if spec.requestHasQuantity() {
		quantityFormat = decimals[request.Entity+"\x00"+request.Quantity]
	}
	moneyFormat := exactdecimal.Format{}
	for _, field := range []string{request.Net, request.Tax, request.Gross} {
		if field != "" {
			moneyFormat = decimals[request.Entity+"\x00"+field]
			break
		}
	}
	measureFormats := [4]exactdecimal.Format{quantityFormat, moneyFormat, moneyFormat, moneyFormat}
	sameScope := func(row map[string]any, ref AuthorityRef, capacityID string, capacityRef AuthorityRef) error {
		// A capacity-side ref is optional: an omitted ref simply skips the
		// transitive capacity comparison while the direct pair checks remain.
		if ref.Field == "" || capacityRef.Field == "" {
			return nil
		}
		value, err := authorityValue(after, row, ref)
		limit, capacityErr := authorityValue(after, after[capacity.Entity][capacityID], capacityRef)
		if err != nil || capacityErr != nil || value == "" || limit == "" || value != limit {
			return failure("settlement_invalid")
		}
		return nil
	}
	sameMaterial := func(left map[string]any, leftRef AuthorityRef, right map[string]any, rightRef AuthorityRef) error {
		if leftRef.Field == "" {
			return nil
		}
		a, aNull, err := authorityNullAware(after, left, leftRef)
		b, bNull, rightErr := authorityNullAware(after, right, rightRef)
		if err != nil || rightErr != nil || !aNull && a == "" || !bNull && b == "" || !aNull && !bNull && a != b {
			return failure("settlement_invalid")
		}
		return nil
	}
	matchCapacityMaterial := func(row map[string]any, ref AuthorityRef, capacityID string) error {
		original := after[capacity.Entity][capacityID]
		if capacity.Material.Field == "" {
			return nil
		}
		if err := sameMaterial(row, ref, original, capacity.Material); err != nil {
			return err
		}
		if capacity.Entity == actual.Entity && capacity.Material.Field != "" {
			if _, present := original[actual.Request]; !present {
				return failure("settlement_invalid")
			}
			sourceID, err := optionalRef(after, original, actual.Request, request.Entity)
			if err != nil {
				return failure("settlement_invalid")
			}
			if sourceID != "" {
				source := after[request.Entity][sourceID]
				if err := sameMaterial(original, capacity.Material, source, request.Material); err != nil {
					return err
				}
				if err := sameMaterial(row, ref, source, request.Material); err != nil {
					return err
				}
			}
		}
		return nil
	}
	parseRequired := func(format exactdecimal.Format, value any, code string) (*big.Int, error) {
		if value == nil {
			return nil, failure(code)
		}
		return format.Parse(value)
	}
	capacityFields := [4]string{capacity.Quantity, capacity.Net, capacity.Tax, capacity.Gross}
	verifiedCapacity := map[string]bool{}
	verifyCapacity := func(id string) error {
		if verifiedCapacity[id] {
			return nil
		}
		row := after[capacity.Entity][id]
		if capacity.Entity == actual.Entity {
			state, err := authorityValue(after, row, actual.State)
			if err != nil || !contains(actual.SettledStates, state) {
				return failure("settlement_invalid")
			}
		}
		if capacity.Direction.Field != "" {
			direction, err := authorityValue(after, row, capacity.Direction)
			if err != nil || !contains(capacity.Directions, direction) {
				return failure("settlement_invalid")
			}
		}
		for i, field := range capacityFields {
			if field == "" {
				continue
			}
			value, err := parseRequired(measureFormats[i], row[field], "settlement_invalid")
			if err != nil {
				return err
			}
			if value.Sign() < 0 {
				return failure("settlement_invalid")
			}
		}
		verifiedCapacity[id] = true
		return nil
	}
	// A selected formal row must name a real, foreign, role-valid capacity;
	// an optional external capacity additionally accepts a present null as an
	// explicitly unknown capacity while every other shape stays closed.
	requireCapacity := func(images Images, row map[string]any, field, selfID string) (string, bool, error) {
		value, present := row[field]
		if !present {
			return "", false, failure("settlement_invalid")
		}
		if value == nil {
			if capacity.Optional {
				return "", true, nil
			}
			return "", false, failure("settlement_invalid")
		}
		id, ok := value.(string)
		if !ok || id == "" || images[capacity.Entity][id] == nil || id == selfID && selfID != "" {
			return "", false, failure("settlement_invalid")
		}
		return id, false, nil
	}
	type actualFacts struct {
		effect     *big.Int
		money      [3]*big.Int
		requestID  string
		capacityID string
		planItem   string
	}
	classifyActual := func(images Images, enforce bool) (map[string]actualFacts, error) {
		facts := map[string]actualFacts{}
		for id, row := range images[actual.Entity] {
			state, err := authorityValue(images, row, actual.State)
			if err != nil {
				return nil, failure("settlement_invalid")
			}
			if contains(actual.DraftStates, state) {
				continue
			}
			direction, err := authorityValue(images, row, actual.Direction)
			if err != nil {
				return nil, failure("settlement_invalid")
			}
			if !contains(actual.Directions, direction) || !contains(actual.SettledStates, state) {
				continue
			}
			entry := actualFacts{}
			purpose := ""
			if actual.Purpose.Field != "" {
				value, isNull, err := authorityNullAware(images, row, actual.Purpose)
				if err != nil || isNull && (len(actual.Purposes) > 0 || len(actual.ZeroQuantityPurposes) > 0) || len(actual.Purposes) > 0 && !contains(actual.Purposes, value) {
					return nil, failure("settlement_invalid")
				}
				purpose = value
			}
			if spec.requestHasQuantity() {
				parsed, err := parseRequired(quantityFormat, row[actual.Quantity], "settlement_invalid")
				if err != nil {
					return nil, err
				}
				if direction == "blue" && parsed.Sign() < 0 || direction == "red" && parsed.Sign() > 0 {
					return nil, failure("settlement_invalid")
				}
				if contains(actual.ZeroQuantityPurposes, purpose) && parsed.Sign() != 0 {
					return nil, failure("settlement_invalid")
				}
				entry.effect = new(big.Int).Abs(parsed)
			}
			if requestID, err := optionalRef(images, row, actual.Request, request.Entity); err != nil {
				return nil, failure("settlement_invalid")
			} else if requestID != "" {
				entry.requestID = requestID
			}
			if actual.PlanItem != "" && spec.Plans != nil {
				if item, err := optionalRef(images, row, actual.PlanItem, spec.Plans.Entity); err != nil {
					return nil, failure("settlement_invalid")
				} else if item != "" {
					entry.planItem = item
				}
			}
			for i, field := range []string{actual.Net, actual.Tax, actual.Gross} {
				if field == "" {
					continue
				}
				parsed, err := parseRequired(moneyFormat, row[field], "settlement_invalid")
				if err != nil {
					return nil, err
				}
				if direction == "red" && parsed.Sign() > 0 {
					return nil, failure("settlement_invalid")
				}
				entry.money[i] = new(big.Int).Abs(parsed)
			}
			if id2, isNull, err := requireCapacity(images, row, actual.Capacity, id); err == nil {
				if !isNull {
					entry.capacityID = id2
				}
			} else if enforce {
				return nil, err
			}
			facts[id] = entry
		}
		return facts, nil
	}
	beforeFacts, err := classifyActual(before, false)
	if err != nil {
		return err
	}
	afterFacts, err := classifyActual(after, true)
	if err != nil {
		return err
	}
	consumed := map[string]*settlementBucket{}
	// Closed-in-before sources take no new or increased consumption.
	closedBefore := map[string]bool{}
	if adj := spec.Adjustment; adj != nil && before != nil {
		for _, row := range before[adj.Entity] {
			kind, _ := row[adj.Kind].(string)
			if !contains(adj.CloseKinds, kind) && !contains(adj.FulfillKinds, kind) {
				continue
			}
			if flag, present := row[adj.CloseRemaining]; present && flag == true && contains(adj.CloseKinds, kind) {
				if target, err := optionalRef(before, row, adj.Target, request.Entity); err == nil && target != "" {
					closedBefore[target] = true
				}
			}
		}
	}
	openBefore := func(id string) bool {
		if before == nil || before[request.Entity][id] == nil || closedBefore[id] {
			return false
		}
		state, err := authorityValue(before, before[request.Entity][id], request.State)
		return err == nil && contains(request.OpenStates, state)
	}
	sourceCapacity := map[string]string{}
	sourceCapacityNull := map[string]bool{}
	for id, row := range after[request.Entity] {
		if capacityID, isNull, err := requireCapacity(after, row, request.Capacity, ""); err == nil {
			if isNull {
				sourceCapacityNull[id] = true
			} else {
				sourceCapacity[id] = capacityID
			}
		}
	}
	beforeSourceCapacity := map[string]string{}
	beforeSourceCapacityNull := map[string]bool{}
	if before != nil {
		for id, row := range before[request.Entity] {
			if capacityID, isNull, err := requireCapacity(before, row, request.Capacity, ""); err == nil {
				if isNull {
					beforeSourceCapacityNull[id] = true
				} else {
					beforeSourceCapacity[id] = capacityID
				}
			}
		}
	}
	// A preserved known capacity attribution on governed sources or settled
	// actuals can never be erased to an unknown null; deleting the row itself
	// keeps its correction rules.
	if capacity.Optional && before != nil {
		for id, row := range before[request.Entity] {
			if after[request.Entity][id] == nil {
				continue
			}
			state, stateErr := authorityValue(before, row, request.State)
			direction, directionErr := authorityValue(before, row, request.Direction)
			if stateErr != nil || directionErr != nil || !contains(request.Directions, direction) || contains(request.DraftStates, state) {
				continue
			}
			if beforeSourceCapacity[id] != "" && sourceCapacityNull[id] {
				return failure("settlement_invalid")
			}
		}
		for id, entry := range beforeFacts {
			current, ok := afterFacts[id]
			if ok && entry.capacityID != "" && current.capacityID == "" {
				return failure("settlement_invalid")
			}
		}
	}
	executed := map[string]*big.Int{}
	planBucketOfActual := map[string]string{}
	for id, entry := range afterFacts {
		if entry.capacityID != "" {
			if err := verifyCapacity(entry.capacityID); err != nil {
				return err
			}
			if err := sameScope(after[actual.Entity][id], actual.Currency, entry.capacityID, capacity.Currency); err != nil {
				return err
			}
		}
		quantityActual := true
		if len(actual.ZeroQuantityPurposes) > 0 {
			purpose, _, err := authorityNullAware(after, after[actual.Entity][id], actual.Purpose)
			if err != nil {
				return failure("settlement_invalid")
			}
			quantityActual = !contains(actual.ZeroQuantityPurposes, purpose)
		}
		// The source basis decides the carrier policy without any red purpose:
		// an amount-basis source carries an explicit zero quantity and skips the
		// quantity scope, while a quantity source keeps it even at Q0. A
		// source-less zero-quantity actual consumes no quantity scope at all.
		if entry.requestID != "" && request.Basis.Field != "" {
			basis, err := authorityValue(after, after[request.Entity][entry.requestID], request.Basis)
			if err != nil {
				return failure("settlement_invalid")
			}
			if basis == "amount" {
				quantityActual = false
				if entry.effect != nil && entry.effect.Sign() != 0 {
					return failure("settlement_invalid")
				}
			}
		} else if entry.requestID == "" && entry.effect != nil && entry.effect.Sign() == 0 {
			quantityActual = false
		}
		if quantityActual {
			if entry.capacityID != "" {
				if err := sameScope(after[actual.Entity][id], actual.Unit, entry.capacityID, capacity.Unit); err != nil {
					return err
				}
				if err := matchCapacityMaterial(after[actual.Entity][id], actual.Material, entry.capacityID); err != nil {
					return err
				}
			}
		}
		if spec.Plans == nil && entry.capacityID != "" {
			bucket := consumed[entry.capacityID]
			if bucket == nil {
				bucket = newSettlementBucket()
				consumed[entry.capacityID] = bucket
			}
			if entry.effect != nil {
				if err := quantityFormat.Add(bucket.measures[0], entry.effect); err != nil {
					return err
				}
			}
			for i, value := range entry.money {
				if value == nil {
					continue
				}
				if err := moneyFormat.Add(bucket.measures[i+1], value); err != nil {
					return err
				}
			}
		}
		_ = id
		if entry.requestID != "" {
			sourceRow := after[request.Entity][entry.requestID]
			sourceState, err := authorityValue(after, sourceRow, request.State)
			if err != nil || sourceState == "" || contains(request.DraftStates, sourceState) {
				return failure("settlement_invalid")
			}
			if sourceCapacity[entry.requestID] != entry.capacityID || sourceCapacityNull[entry.requestID] != (entry.capacityID == "") {
				return failure("settlement_invalid")
			}
			if quantityActual {
				if err := sameMaterial(sourceRow, request.Material, after[actual.Entity][id], actual.Material); err != nil {
					return err
				}
				// The unit pair is checked directly and non-empty, so a null
				// capacity cannot silently drop the quantity scope; a known
				// capacity adds its own transitive check on top.
				if request.Unit.Field != "" {
					sourceUnit, err := authorityValue(after, sourceRow, request.Unit)
					actualUnit, actualErr := authorityValue(after, after[actual.Entity][id], actual.Unit)
					if err != nil || actualErr != nil || sourceUnit == "" || sourceUnit != actualUnit {
						return failure("settlement_invalid")
					}
				}
			}
			sourceDirection, err := authorityValue(after, sourceRow, request.Direction)
			actualDirection, actualErr := authorityValue(after, after[actual.Entity][id], actual.Direction)
			if err != nil || actualErr != nil || sourceDirection != actualDirection {
				return failure("settlement_invalid")
			}
			if request.Currency.Field != "" {
				sourceCurrency, err := authorityValue(after, sourceRow, request.Currency)
				actualCurrency, actualErr := authorityValue(after, after[actual.Entity][id], actual.Currency)
				if err != nil || actualErr != nil || sourceCurrency == "" || sourceCurrency != actualCurrency {
					return failure("settlement_invalid")
				}
			}
			if request.Basis.Field != "" && len(actual.ZeroQuantityPurposes) > 0 {
				basis, err := authorityValue(after, sourceRow, request.Basis)
				purpose, actualErr := authorityValue(after, after[actual.Entity][id], actual.Purpose)
				if err != nil || actualErr != nil || (basis == "amount") != contains(actual.ZeroQuantityPurposes, purpose) {
					return failure("settlement_invalid")
				}
			}
			if executed[entry.requestID] == nil {
				executed[entry.requestID] = new(big.Int)
			}
			if entry.effect != nil {
				if err := quantityFormat.Add(executed[entry.requestID], entry.effect); err != nil {
					return err
				}
			}
		}
		// Compare all consumed dimensions, including zero-quantity discounts.
		old := beforeFacts[id]
		increased := old.requestID != entry.requestID || old.capacityID != entry.capacityID
		for i, value := range [4]*big.Int{entry.effect, entry.money[0], entry.money[1], entry.money[2]} {
			previous := [4]*big.Int{old.effect, old.money[0], old.money[1], old.money[2]}[i]
			if value != nil && (previous == nil && value.Sign() > 0 || previous != nil && value.Cmp(previous) > 0) {
				increased = true
			}
		}
		// Admission validates a stored graph with no before image. Actual writes
		// always capture a non-nil image, including an empty initial database.
		if before != nil && increased && entry.requestID != "" {
			state, err := authorityValue(after, after[request.Entity][entry.requestID], request.State)
			confirmedNow := contains(request.OpenStates, state)
			if before != nil && before[request.Entity][entry.requestID] != nil {
				previous, previousErr := authorityValue(before, before[request.Entity][entry.requestID], request.State)
				confirmedNow = confirmedNow && previousErr == nil && contains(request.DraftStates, previous)
			}
			if err != nil || closedBefore[entry.requestID] ||
				!openBefore(entry.requestID) && !confirmedNow {
				return failure("settlement_invalid")
			}
		}
		if spec.Plans != nil && entry.planItem == "" && entry.requestID != "" {
			if row := after[request.Entity][entry.requestID]; row != nil && request.PlanItem != "" {
				if item, err := optionalRef(after, row, request.PlanItem, spec.Plans.Entity); err == nil && item != "" {
					planBucketOfActual[id] = item
				}
			}
		}
	}
	closedAmounts := map[string]*settlementBucket{}
	fulfilledAmounts := map[string]*settlementBucket{}
	closingNow := map[string]bool{}
	closingScopes := map[string]*settlementBucket{}
	if adj := spec.Adjustment; adj != nil {
		for eventID, row := range after[adj.Entity] {
			kind, _ := row[adj.Kind].(string)
			closeEvent := contains(adj.CloseKinds, kind)
			fulfillEvent := contains(adj.FulfillKinds, kind)
			if !closeEvent && !fulfillEvent {
				continue // actual correction events are not settlement facts
			}
			if flag, present := row[adj.CloseRemaining]; present && flag != nil {
				if _, ok := flag.(bool); !ok {
					return failure("settlement_invalid")
				}
			}
			target, err := relationRef(after, row, adj.Target, request.Entity)
			if err != nil {
				return failure("settlement_invalid")
			}
			direction, err := authorityValue(after, after[request.Entity][target], request.Direction)
			if err != nil {
				return failure("settlement_invalid")
			}
			if !contains(request.Directions, direction) {
				continue
			}
			allocationID := ""
			if fulfillEvent {
				allocationID, err = relationRef(after, row, adj.Allocation, actual.Entity)
				if err != nil {
					return failure("settlement_invalid")
				}
				if request.Basis.Field != "" {
					basis, err := authorityValue(after, after[request.Entity][target], request.Basis)
					if err != nil || basis != "amount" {
						return failure("settlement_invalid")
					}
				}
				allocation, ok := afterFacts[allocationID]
				if !ok || allocation.requestID != target ||
					sourceCapacity[target] != allocation.capacityID || sourceCapacityNull[target] != (allocation.capacityID == "") {
					return failure("settlement_invalid")
				}
			}
			if flag, present := row[adj.CloseRemaining]; present && flag == true && closeEvent {
				for _, field := range []string{adj.CloseQuantity, adj.CloseNet, adj.CloseTax, adj.CloseGross} {
					if field == "" {
						continue
					}
					if value, present := row[field]; !present || value == nil {
						return failure("settlement_invalid")
					}
				}
				// Only a newly written close captures today's remaining scope.
				// Admission keeps the fixed scope of historical close events.
				if before != nil && before[adj.Entity][eventID] == nil {
					if closingNow[target] {
						return failure("settlement_invalid")
					}
					closingNow[target] = true
					scope := newSettlementBucket()
					for i, field := range []string{adj.CloseQuantity, adj.CloseNet, adj.CloseTax, adj.CloseGross} {
						if field != "" {
							parsed, err := parseRequired(measureFormats[i], row[field], "settlement_invalid")
							if err != nil {
								return err
							}
							scope.measures[i] = parsed
						}
					}
					closingScopes[target] = scope
				}
			}
			bucket := closedAmounts[target]
			if bucket == nil {
				bucket = newSettlementBucket()
				closedAmounts[target] = bucket
			}
			if adj.CloseQuantity != "" {
				if value, present := row[adj.CloseQuantity]; present && value != nil {
					parsed, err := parseRequired(quantityFormat, value, "settlement_invalid")
					if err != nil {
						return err
					}
					if err := quantityFormat.Add(bucket.measures[0], parsed); err != nil {
						return err
					}
				}
			}
			for i, field := range []string{adj.CloseNet, adj.CloseTax, adj.CloseGross} {
				if field == "" {
					continue
				}
				if value, present := row[field]; present && value != nil {
					parsed, err := parseRequired(moneyFormat, value, "settlement_invalid")
					if err != nil {
						return err
					}
					if err := moneyFormat.Add(bucket.measures[i+1], parsed); err != nil {
						return err
					}
					if fulfillEvent {
						if fulfilledAmounts[allocationID] == nil {
							fulfilledAmounts[allocationID] = newSettlementBucket()
						}
						if err := moneyFormat.Add(fulfilledAmounts[allocationID].measures[i+1], parsed); err != nil {
							return err
						}
						limit := afterFacts[allocationID].money[i]
						if limit == nil || fulfilledAmounts[allocationID].measures[i+1].Cmp(limit) > 0 {
							return failure("settlement_exceeded")
						}
					}
				}
			}
			if row[adj.CloseNet] != nil && row[adj.CloseTax] != nil && row[adj.CloseGross] != nil && adj.CloseNet != "" && adj.CloseTax != "" && adj.CloseGross != "" {
				n, _ := moneyFormat.Parse(row[adj.CloseNet])
				tax, _ := moneyFormat.Parse(row[adj.CloseTax])
				g, _ := moneyFormat.Parse(row[adj.CloseGross])
				if new(big.Int).Add(n, tax).Cmp(g) != 0 {
					return failure("settlement_invalid")
				}
			}
		}
	}
	parseRequest := func(row map[string]any) ([4]*big.Int, error) {
		var values [4]*big.Int
		for i, field := range []string{request.Quantity, request.Net, request.Tax, request.Gross} {
			if field == "" {
				continue
			}
			parsed, err := parseRequired(measureFormats[i], row[field], "settlement_invalid")
			if err != nil {
				return values, err
			}
			values[i] = parsed
		}
		return values, nil
	}
	// Use the existing version/logical-item/share index rather than a second plan model.
	type bucketKey = reservationBucket
	planRule := Rule{Reservation: &ReservationSpec{Obligation: ReservationObligation{Entity: capacity.Entity}, Plans: spec.Plans}}
	planIndex, err := planRule.planBuckets(after, decimals)
	if err != nil {
		return err
	}
	beforePlanIndex, err := planRule.planBuckets(before, decimals)
	if err != nil {
		return err
	}
	planItems, planCurrent := planIndex.items, planIndex.current
	if plans := spec.Plans; plans != nil {
		// A selected source fixes the reservation bucket. An explicit actual
		// item may name another version of that bucket, but cannot move its
		// execution to a different scope/logical item/share (or a direct source).
		for _, entry := range afterFacts {
			if entry.requestID == "" || entry.planItem == "" {
				continue
			}
			sourceItem, err := optionalRef(after, after[request.Entity][entry.requestID], request.PlanItem, plans.Entity)
			sourceKey, sourceOK := planItems[sourceItem]
			actualKey, actualOK := planItems[entry.planItem]
			if err != nil || !sourceOK || !actualOK || sourceKey != actualKey {
				return failure("settlement_invalid")
			}
		}
		preserveItem := func(item string) error {
			if item != "" && beforePlanIndex.items[item] != planItems[item] {
				return failure("settlement_invalid")
			}
			return nil
		}
		for id, row := range before[request.Entity] {
			state, stateErr := authorityValue(before, row, request.State)
			direction, directionErr := authorityValue(before, row, request.Direction)
			if stateErr != nil || directionErr != nil {
				return failure("settlement_invalid")
			}
			if !contains(request.Directions, direction) || contains(request.DraftStates, state) {
				continue
			}
			item, itemErr := optionalRef(before, row, request.PlanItem, plans.Entity)
			if itemErr != nil {
				return failure("settlement_invalid")
			}
			if err := preserveItem(item); err != nil {
				return err
			}
			_ = id
		}
		for _, entry := range beforeFacts {
			item := entry.planItem
			if item == "" && entry.requestID != "" {
				item, _ = optionalRef(before, before[request.Entity][entry.requestID], request.PlanItem, plans.Entity)
			}
			if err := preserveItem(item); err != nil {
				return err
			}
		}
	}
	bucketUse := map[bucketKey]*big.Int{}
	executedPlan := map[bucketKey]*big.Int{}
	direct := map[string]*big.Int{}
	addDirect := func(scope string, value *big.Int) error {
		if value == nil {
			return nil
		}
		if direct[scope] == nil {
			direct[scope] = new(big.Int)
		}
		return quantityFormat.Add(direct[scope], value)
	}
	governed := map[string]bool{}
	for id, row := range after[request.Entity] {
		state, err := authorityValue(after, row, request.State)
		if err != nil {
			return failure("settlement_invalid")
		}
		direction, err := authorityValue(after, row, request.Direction)
		if err != nil {
			return failure("settlement_invalid")
		}
		if !contains(request.Directions, direction) {
			continue
		}
		if len(request.Purposes) > 0 {
			purpose, isNull, err := authorityNullAware(after, row, request.Purpose)
			if err != nil || isNull || !contains(request.Purposes, purpose) {
				return failure("settlement_invalid")
			}
		}
		governed[id] = true
		if contains(request.DraftStates, state) {
			continue
		}
		capacityID, isNull, err := requireCapacity(after, row, request.Capacity, "")
		if err != nil {
			return err
		}
		if !isNull {
			if err := verifyCapacity(capacityID); err != nil {
				return err
			}
			if err := sameScope(row, request.Currency, capacityID, capacity.Currency); err != nil {
				return err
			}
		}
		values, err := parseRequest(row)
		if err != nil {
			return err
		}
		for _, value := range values {
			if value != nil && value.Sign() < 0 {
				return failure("settlement_invalid")
			}
		}
		amountType := false
		if request.Basis.Field != "" {
			basis, err := authorityValue(after, row, request.Basis)
			if err != nil {
				return failure("settlement_invalid")
			}
			switch basis {
			case "amount":
				amountType = true
			case "quantity":
			default:
				return failure("settlement_invalid")
			}
		}
		if request.Purpose.Field != "" && request.Basis.Field != "" && len(actual.ZeroQuantityPurposes) > 0 {
			purpose, isNull, err := authorityNullAware(after, row, request.Purpose)
			if err != nil || isNull || amountType != contains(actual.ZeroQuantityPurposes, purpose) {
				return failure("settlement_invalid")
			}
		}
		if !amountType && !isNull {
			if err := sameScope(row, request.Unit, capacityID, capacity.Unit); err != nil {
				return err
			}
			if err := matchCapacityMaterial(row, request.Material, capacityID); err != nil {
				return err
			}
		}
		var remaining [4]*big.Int
		planItem := ""
		if spec.Plans != nil {
			item, err := optionalRef(after, row, request.PlanItem, spec.Plans.Entity)
			if err != nil {
				return failure("settlement_invalid")
			}
			// An explicit plan item fixes the real bucket scope on every formal
			// source, including closed history; a null capacity cannot drift.
			if item != "" {
				key, ok := planItems[item]
				if !ok || key.scope != capacityID {
					return failure("settlement_invalid")
				}
			}
			planItem = item
		}
		if amountType {
			if spec.requestHasQuantity() {
				if row[request.Quantity] == nil {
					return failure("settlement_invalid")
				}
				if values[0].Sign() != 0 {
					return failure("settlement_invalid")
				}
				effect := executed[id]
				if effect == nil {
					effect = new(big.Int)
				}
				closeBucket := closedAmounts[id]
				if closeBucket == nil {
					closeBucket = newSettlementBucket()
				}
				if new(big.Int).Add(effect, closeBucket.measures[0]).Sign() > 0 {
					return failure("settlement_exceeded")
				}
			}
			amounts := closedAmounts[id]
			if amounts == nil {
				amounts = newSettlementBucket()
			}
			for i := 1; i < 4; i++ {
				if !requestMeasure(spec, i) {
					continue
				}
				remaining[i] = new(big.Int).Sub(values[i], amounts.measures[i])
				if remaining[i].Sign() < 0 {
					return failure("settlement_exceeded")
				}
			}
		} else if spec.requestHasQuantity() {
			quantity := values[0]
			if quantity.Sign() <= 0 {
				return failure("settlement_invalid")
			}
			effect := executed[id]
			if effect == nil {
				effect = new(big.Int)
			}
			closeBucket := closedAmounts[id]
			if closeBucket == nil {
				closeBucket = newSettlementBucket()
			}
			if new(big.Int).Add(effect, closeBucket.measures[0]).Cmp(quantity) > 0 {
				return failure("settlement_exceeded")
			}
			for i := 1; i < 4; i++ {
				if values[i] != nil && closeBucket.measures[i].Cmp(values[i]) > 0 {
					return failure("settlement_exceeded")
				}
			}
			spent := new(big.Int).Add(effect, closeBucket.measures[0])
			remaining[0] = new(big.Int).Sub(quantity, spent)
			for i := 1; i < 3; i++ {
				if !requestMeasure(spec, i) {
					continue
				}
				remaining[i] = new(big.Int).Sub(values[i], proportionalHalfAway(values[i], spent, quantity))
			}
			if requestMeasure(spec, 3) {
				gross := new(big.Int)
				if remaining[1] != nil {
					gross.Add(gross, remaining[1])
				}
				if remaining[2] != nil {
					gross.Add(gross, remaining[2])
				}
				if spec.hasNet() && spec.hasTax() {
					remaining[3] = gross
				} else {
					remaining[3] = new(big.Int).Sub(values[3], proportionalHalfAway(values[3], spent, quantity))
				}
			}
		} else {
			amounts := closedAmounts[id]
			if amounts == nil {
				amounts = newSettlementBucket()
			}
			for i := 1; i < 4; i++ {
				if !requestMeasure(spec, i) {
					continue
				}
				remaining[i] = new(big.Int).Sub(values[i], amounts.measures[i])
				if remaining[i].Sign() < 0 {
					return failure("settlement_exceeded")
				}
			}
		}
		if closingNow[id] {
			if !amountType && spec.hasQuantity() {
				effect := executed[id]
				if effect == nil {
					effect = new(big.Int)
				}
				// Prior quantity-only close facts already consumed proportional
				// reservation; compare this fixed capture with the remaining scope.
				scope := closingScopes[id]
				priorSpent := new(big.Int).Add(effect, closedAmounts[id].measures[0])
				priorSpent.Sub(priorSpent, scope.measures[0])
				var expectedMoney [4]*big.Int
				for i := 1; i < 3; i++ {
					if values[i] != nil {
						expected := new(big.Int).Sub(values[i], proportionalHalfAway(values[i], priorSpent, values[0]))
						expectedMoney[i] = expected
						if scope.measures[i].Cmp(expected) != 0 {
							return failure("settlement_invalid")
						}
					}
				}
				if values[3] != nil {
					expected := new(big.Int).Sub(values[3], proportionalHalfAway(values[3], priorSpent, values[0]))
					if expectedMoney[1] != nil && expectedMoney[2] != nil {
						expected = new(big.Int).Add(expectedMoney[1], expectedMoney[2])
					}
					if scope.measures[3].Cmp(expected) != 0 {
						return failure("settlement_invalid")
					}
				}
			}
			for _, value := range remaining {
				if value != nil && value.Sign() != 0 {
					return failure("settlement_invalid")
				}
			}
		}
		if !contains(request.OpenStates, state) {
			continue
		}
		if spec.Plans != nil {
			if planItem != "" {
				key, ok := planItems[planItem]
				if !ok || key.scope != capacityID {
					return failure("settlement_invalid")
				}
				if bucketUse[key] == nil {
					bucketUse[key] = new(big.Int)
				}
				if err := quantityFormat.Add(bucketUse[key], remaining[0]); err != nil {
					return err
				}
			} else if capacityID != "" {
				if err := addDirect(capacityID, remaining[0]); err != nil {
					return err
				}
			}
			continue
		}
		if capacityID == "" {
			continue
		}
		bucket := consumed[capacityID]
		if bucket == nil {
			bucket = newSettlementBucket()
			consumed[capacityID] = bucket
		}
		for i, value := range remaining {
			if value == nil {
				continue
			}
			if err := measureFormats[i].Add(bucket.measures[i], value); err != nil {
				return err
			}
		}
	}
	if spec.Plans != nil {
		for id, entry := range afterFacts {
			if entry.effect == nil {
				continue
			}
			item := entry.planItem
			if item == "" {
				item = planBucketOfActual[id]
			}
			if item != "" {
				key, ok := planItems[item]
				if !ok || key.scope != entry.capacityID {
					return failure("settlement_invalid")
				}
				if bucketUse[key] == nil {
					bucketUse[key] = new(big.Int)
				}
				if err := quantityFormat.Add(bucketUse[key], entry.effect); err != nil {
					return err
				}
				if executedPlan[key] == nil {
					executedPlan[key] = new(big.Int)
				}
				if err := quantityFormat.Add(executedPlan[key], entry.effect); err != nil {
					return err
				}
				continue
			}
			if entry.capacityID != "" {
				if err := addDirect(entry.capacityID, entry.effect); err != nil {
					return err
				}
			}
		}
		for key, planned := range planCurrent {
			used := bucketUse[key]
			if used != nil && used.Cmp(planned) > 0 {
				return failure("settlement_exceeded")
			}
			if err := addDirect(key.scope, planned); err != nil {
				return err
			}
		}
		for key, used := range bucketUse {
			if planCurrent[key] == nil {
				if executedPlan[key] != nil && executedPlan[key].Sign() > 0 {
					return failure("settlement_exceeded")
				}
				if err := addDirect(key.scope, used); err != nil {
					return err
				}
			}
		}
		for scope, occupied := range direct {
			row := after[capacity.Entity][scope]
			limit, err := parseRequired(quantityFormat, row[capacity.Quantity], "settlement_invalid")
			if err != nil {
				return err
			}
			if occupied.Cmp(limit) > 0 {
				return failure("settlement_exceeded")
			}
		}
		return nil
	}
	for id, bucket := range consumed {
		row := after[capacity.Entity][id]
		for i := 0; i < 4; i++ {
			if !measureDeclared(spec, i) {
				continue
			}
			limit, err := parseRequired(measureFormats[i], row[capacityFields[i]], "settlement_invalid")
			if err != nil {
				return err
			}
			if bucket.measures[i].Cmp(limit) > 0 {
				return failure("settlement_exceeded")
			}
		}
	}
	return nil
}
func measureDeclared(spec SettlementSpec, index int) bool {
	switch index {
	case 0:
		return spec.hasQuantity()
	case 1:
		return spec.hasNet()
	case 2:
		return spec.hasTax()
	default:
		return spec.hasGross()
	}
}

// proportionalHalfAway returns roundHalfAway(value*spent/quantity) exactly on
// integer coefficients; quantity is strictly positive at every call site.
func proportionalHalfAway(value, spent, quantity *big.Int) *big.Int {
	numerator := new(big.Int).Mul(value, spent)
	quotient, remainder := new(big.Int).QuoRem(numerator, quantity, new(big.Int))
	remainder.Abs(remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(quantity) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}
