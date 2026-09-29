package queryfilter

type Operator string

const (
	OperatorContains   Operator = "contains"
	OperatorEqual      Operator = "eq"
	OperatorNotEqual   Operator = "ne"
	OperatorStartsWith Operator = "starts_with"
	OperatorEndsWith   Operator = "ends_with"
	OperatorGreater    Operator = "gt"
	OperatorLess       Operator = "lt"
	OperatorGreaterEq  Operator = "gte"
	OperatorLessEq     Operator = "lte"
	OperatorBetween    Operator = "between"
	OperatorIn         Operator = "in"
	OperatorIsNull     Operator = "is_null"
	OperatorIsNotNull  Operator = "is_not_null"
	OperatorRegex      Operator = "regex"
)

type Logic string

const (
	LogicAnd Logic = "AND"
	LogicOr  Logic = "OR"
)

type FilterExpression struct {
	Field      string             `json:"field,omitempty"`
	Operator   Operator           `json:"operator,omitempty"`
	Value      any                `json:"value,omitempty"`
	Logic      Logic              `json:"logic,omitempty"`
	Filters    []FilterExpression `json:"filters,omitempty"`
	GroupLogic Logic              `json:"groupLogic,omitempty"`
}
