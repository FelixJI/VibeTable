package transactionrules

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNewRuleNullShapesMatchPython(t *testing.T) {
	var corpus struct {
		Rules   map[string]json.RawMessage `json:"rules"`
		Invalid []struct {
			Base string
			Path []string
		} `json:"invalid"`
		Valid []struct {
			Base  string
			Path  []string
			Value any
			Omit  bool
		} `json:"valid"`
	}
	raw, err := os.ReadFile("../../../contracts/plugin-transaction-null-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, base string, path []string, value any, omit, valid bool) {
		t.Helper()
		var rule any
		if err := json.Unmarshal(corpus.Rules[base], &rule); err != nil {
			t.Fatal(err)
		}
		current := rule
		for i, key := range path {
			switch node := current.(type) {
			case map[string]any:
				if i == len(path)-1 {
					if omit {
						delete(node, key)
					} else {
						node[key] = value
					}
				} else {
					current = node[key]
				}
			case []any:
				index, err := strconv.Atoi(key)
				if err != nil {
					t.Fatal(err)
				}
				if i == len(path)-1 {
					node[index] = value
				} else {
					current = node[index]
				}
			}
		}
		declaration := map[string]any{"contract": Contract, "modelId": "m", "version": 1, "rules": []any{rule}, "decimals": []any{
			map[string]any{"entity": "sources", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative"},
			map[string]any{"entity": "obligations", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative"},
		}}
		encoded, _ := json.Marshal(declaration)
		_, err := Decode(encoded)
		if (err == nil) != valid {
			t.Errorf("valid=%v got %v: %s", valid, err, encoded)
		}
	}
	for base := range corpus.Rules {
		t.Run(base+"/omitted-options", func(t *testing.T) { check(t, base, nil, nil, false, true) })
	}
	for _, tc := range corpus.Invalid {
		t.Run(tc.Base+"/null/"+strings.Join(tc.Path, "/"), func(t *testing.T) { check(t, tc.Base, tc.Path, nil, false, false) })
	}
	for _, tc := range corpus.Valid {
		t.Run(tc.Base+"/valid/"+strings.Join(tc.Path, "/"), func(t *testing.T) { check(t, tc.Base, tc.Path, tc.Value, tc.Omit, true) })
	}
}
