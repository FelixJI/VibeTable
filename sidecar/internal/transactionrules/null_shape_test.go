package transactionrules

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNewRuleNullShapesMatchPython(t *testing.T) {
	type edit struct {
		Path  []string
		Value any
		Omit  bool
	}
	var corpus struct {
		Rules   map[string]json.RawMessage `json:"rules"`
		Invalid []struct {
			Base string
			edit
			Also *edit
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
	check := func(t *testing.T, base string, valid bool, edits ...edit) {
		t.Helper()
		var rule any
		if err := json.Unmarshal(corpus.Rules[base], &rule); err != nil {
			t.Fatal(err)
		}
		for _, change := range edits {
			current := rule
			for i, key := range change.Path {
				switch node := current.(type) {
				case map[string]any:
					if i == len(change.Path)-1 {
						if change.Omit {
							delete(node, key)
						} else {
							node[key] = change.Value
						}
					} else {
						current = node[key]
					}
				case []any:
					index, err := strconv.Atoi(key)
					if err != nil {
						t.Fatal(err)
					}
					if i == len(change.Path)-1 {
						node[index] = change.Value
					} else {
						current = node[index]
					}
				}
			}
		}
		declaration := map[string]any{"contract": Contract, "modelId": "m", "version": 1, "rules": []any{rule}, "decimals": []any{
			map[string]any{"entity": "sources", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative"},
			map[string]any{"entity": "obligations", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative"},
			map[string]any{"entity": "allocations", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "nullable": true},
			map[string]any{"entity": "allocations", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "allocations", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "allocations", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "invoice_lines", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "nullable": true},
			map[string]any{"entity": "invoice_lines", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "invoice_lines", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "invoice_lines", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "blueprint_lines", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative"},
			map[string]any{"entity": "blueprint_lines", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38},
			map[string]any{"entity": "blueprint_lines", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38},
			map[string]any{"entity": "blueprint_lines", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38},
			map[string]any{"entity": "request_sources", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative", "nullable": true},
			map[string]any{"entity": "request_sources", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "request_sources", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "request_sources", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "actual_lines", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "nullable": true},
			map[string]any{"entity": "actual_lines", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "actual_lines", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "actual_lines", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38, "nullable": true},
			map[string]any{"entity": "adjustments", "field": "quantity", "storage": "decimalText", "scale": 0, "digits": 38, "sign": "nonnegative", "nullable": true},
			map[string]any{"entity": "adjustments", "field": "net", "storage": "decimalText", "scale": 2, "digits": 38, "sign": "nonnegative", "nullable": true},
			map[string]any{"entity": "adjustments", "field": "tax", "storage": "decimalText", "scale": 2, "digits": 38, "sign": "nonnegative", "nullable": true},
			map[string]any{"entity": "adjustments", "field": "gross", "storage": "decimalText", "scale": 2, "digits": 38, "sign": "nonnegative", "nullable": true},
		}}
		encoded, _ := json.Marshal(declaration)
		parsed, err := Decode(encoded)
		if (err == nil) != valid {
			t.Errorf("valid=%v got %v: %s", valid, err, encoded)
		}
		if err == nil && len(parsed.Rules) == 1 && parsed.Rules[0].Allocation != nil && len(parsed.Rules[0].Allocation.RequireCompleteWhen) > 0 {
			persisted, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := Decode(persisted)
			if err != nil || !reloaded.Retains(parsed) {
				t.Fatalf("conditional coverage changed when persisted: %v", err)
			}
		}
	}
	for base := range corpus.Rules {
		t.Run(base+"/omitted-options", func(t *testing.T) { check(t, base, true) })
	}
	for _, tc := range corpus.Invalid {
		t.Run(tc.Base+"/invalid/"+strings.Join(tc.Path, "/"), func(t *testing.T) {
			edits := []edit{tc.edit}
			if tc.Also != nil {
				edits = append(edits, *tc.Also)
			}
			check(t, tc.Base, false, edits...)
		})
	}
	for _, tc := range corpus.Valid {
		t.Run(tc.Base+"/valid/"+strings.Join(tc.Path, "/"), func(t *testing.T) { check(t, tc.Base, true, edit{tc.Path, tc.Value, tc.Omit}) })
	}
}
