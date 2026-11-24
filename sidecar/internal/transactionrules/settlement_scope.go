package transactionrules

// Each role's direct relation points at the same logical target. A null
// relation is unknown; missing fields, malformed values and dangling IDs
// cannot serve as attribution evidence.
type SettlementScopeDimension struct {
	Capacity string `json:"capacity"`
	Request  string `json:"request"`
	Actual   string `json:"actual"`
	Target   string `json:"target"`
}

func scopeRelation(images Images, row map[string]any, field, target string) (string, error) {
	if row == nil { // optional external capacity, or no previous row
		return "", nil
	}
	if value, present := row[field]; present && value == nil {
		return "", nil
	}
	id, err := relationRef(images, row, field, target)
	if err != nil {
		return "", failure("settlement_invalid")
	}
	return id, nil
}

// Validate the final pair on admission and writes. When the same protected
// row already used this capacity, changing the capacity must also retain
// the attribution that was known before, even if it was only inherited.
func (s SettlementSpec) checkScope(before, after Images, row, previousRow map[string]any, capacityID string, actual bool) error {
	for _, dimension := range s.ScopeDimensions {
		field := dimension.Request
		if actual {
			field = dimension.Actual
		}
		snapshot, err := scopeRelation(after, row, field, dimension.Target)
		if err != nil {
			return err
		}
		anchor, err := scopeRelation(after, after[s.Capacity.Entity][capacityID], dimension.Capacity, dimension.Target)
		if err != nil || snapshot != "" && anchor != "" && snapshot != anchor {
			return failure("settlement_invalid")
		}
		if previousRow == nil {
			continue
		}
		previousAnchor, err := scopeRelation(before, before[s.Capacity.Entity][capacityID], dimension.Capacity, dimension.Target)
		if err != nil {
			return err
		}
		if previousAnchor == anchor {
			continue
		}
		frozen, err := scopeRelation(before, previousRow, field, dimension.Target)
		if err != nil {
			return err
		}
		if frozen == "" {
			frozen = previousAnchor
		}
		if frozen != "" && frozen != anchor {
			return failure("settlement_invalid")
		}
	}
	return nil
}
