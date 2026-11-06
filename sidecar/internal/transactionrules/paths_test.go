package transactionrules

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/exactdecimal"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
)

func TestCurrentBalanceAndCommonTargetUseFinalIndexedGraph(t *testing.T) {
	raw := `{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":2,"decimals":[{"entity":"entries","field":"amount","storage":"decimalText","scale":0,"digits":3},{"entity":"items","field":"limit","storage":"decimalText","scale":0,"digits":3}],"rules":[
	{"id":"scope","kind":"sameTarget","entity":"entries","left":[{"field":"version","target":"versions"},{"field":"series","target":"series"}],"right":[{"field":"item","target":"items"},{"field":"series","target":"series"}]},
	{"id":"cap","kind":"balance","entity":"entries","relation":"item","target":"items","amount":"amount","limit":"limit","currentWhen":{"left":[{"field":"version","target":"versions"}],"right":[{"field":"item","target":"items"},{"field":"series","target":"series"},{"field":"current","target":"versions"}]}}]}`
	d, err := Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r := Registration{Declaration: d}
	images := Images{
		"items": {"i": {"series": "p", "limit": "10"}}, "versions": {"v1": {"series": "p"}, "v2": {"series": "p"}, "foreign": {"series": "q"}},
		"series":  {"p": {"current": "v2"}, "q": {"current": nil}},
		"entries": {"history": {"version": "v1", "item": "i", "amount": "10"}, "new": {"version": "v2", "item": "i", "amount": "6"}},
	}
	check := func(want string) {
		t.Helper()
		err := r.Validate(nil, images)
		if want == "" && err != nil || want != "" && (err == nil || err.Error() != want) {
			t.Fatalf("wanted %q got %v", want, err)
		}
	}
	check("")
	images["entries"]["new"]["version"] = "foreign"
	check("transaction.same_target_violation")
	images["entries"]["new"]["version"] = "v2"
	images["entries"]["extra"] = map[string]any{"version": "v2", "item": "i", "amount": "5"}
	check("transaction.balance_violation")
	images["series"]["p"]["current"] = "v1"
	check("")
	images["series"]["p"]["current"] = nil
	check("")
	images["series"]["p"]["current"] = "missing"
	check("transaction.reference_violation")
}

func TestOptionalPointerOnlySkipsAnEmptyFirstHop(t *testing.T) {
	raw := `{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":1,"rules":[{"id":"ownership","kind":"sameTarget","entity":"series","whenPresent":"current","left":[{"field":"current","target":"versions"},{"field":"series","target":"series"}],"right":[]}]}`
	d, err := Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r := Registration{Declaration: d}
	images := Images{"series": {"p": {"current": nil}, "q": {"current": nil}}, "versions": {"v": {"series": "p"}}}
	if err := r.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	images["series"]["p"]["current"] = "v"
	if err := r.Validate(nil, images); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{"q", nil, "absent"} {
		images["versions"]["v"]["series"] = value
		if err := r.Validate(nil, images); err == nil {
			t.Fatalf("invalid pointer accepted %v", value)
		}
	}
	images["series"]["p"]["current"] = "missing"
	if err := r.Validate(nil, images); err == nil {
		t.Fatal("dangling first hop skipped")
	}
	d.Rules[0].WhenPresent = "other"
	if d.Validate() == nil {
		t.Fatal("unrelated optional field accepted")
	}
}

func TestTransactionDecodeRejectsPythonAttributeNames(t *testing.T) {
	raw := `{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":1,"decimals":[{"entity":"e","field":"q","storage":"decimalText","scale":0,"digits":3}],"rules":[{"id":"scope","kind":"sameTarget","entity":"e","left":[{"field":"a","target":"e"}],"right":[],"whenPresent":"a","skipEmptyLeftEnd":true},{"id":"cap","kind":"balance","entity":"e","relation":"a","target":"e","amount":"q","limit":"q","currentWhen":{"left":[{"field":"a","target":"e"}],"right":[{"field":"a","target":"e"}]}},{"id":"freeze","kind":"state","entity":"e","field":"state","transitions":{"done":[]},"frozenStates":["done"]}]}`
	if _, err := Decode([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for public, private := range map[string]string{"modelId": "model_id", "whenPresent": "when_present", "skipEmptyLeftEnd": "skip_empty_left_end", "currentWhen": "current_when", "frozenStates": "frozen_states"} {
		if _, err := Decode([]byte(strings.Replace(raw, public, private, 1))); err == nil {
			t.Errorf("accepted %s in a manifest", private)
		}
	}
}

func TestOptionalLeftEndRequiresIntactPaths(t *testing.T) {
	rule := Rule{ID: "owner", Kind: "sameTarget", Entity: "rows", WhenPresent: "exception", SkipEmptyLeftEnd: true, PathMatch: PathMatch{
		Left: []RelationStep{{"exception", "exceptions"}, {"source", "sources"}, {"owner", "owners"}}, Right: []RelationStep{{"owner", "owners"}},
	}}
	for _, tc := range []struct {
		name                   string
		row, exception, source map[string]any
		want                   string
	}{
		{"no exception", map[string]any{"exception": nil, "owner": "o"}, nil, nil, ""},
		{"null terminal", map[string]any{"exception": "e", "owner": "o"}, map[string]any{"source": "s"}, map[string]any{"owner": nil}, ""},
		{"empty terminal", map[string]any{"exception": "e", "owner": "o"}, map[string]any{"source": "s"}, map[string]any{"owner": ""}, ""},
		{"missing terminal", map[string]any{"exception": "e", "owner": "o"}, map[string]any{"source": "s"}, map[string]any{}, "transaction.reference_violation"},
		{"dangling terminal", map[string]any{"exception": "e", "owner": "o"}, map[string]any{"source": "s"}, map[string]any{"owner": "gone"}, "transaction.reference_violation"},
		{"null middle", map[string]any{"exception": "e", "owner": "o"}, map[string]any{"source": nil}, nil, "transaction.reference_violation"},
		{"missing first key", map[string]any{"owner": "o"}, nil, nil, "transaction.reference_violation"},
		{"broken right", map[string]any{"exception": "e", "owner": "gone"}, map[string]any{"source": "s"}, map[string]any{"owner": nil}, "transaction.reference_violation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Registration{Declaration: Declaration{Rules: []Rule{rule}}}
			graph := Images{"rows": {"r": tc.row}, "exceptions": {"e": tc.exception}, "sources": {"s": tc.source}, "owners": {"o": {}}}
			err := r.Validate(nil, graph)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatalf("wanted %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTransactionDecimalSignIsOptInAndExact(t *testing.T) {
	for _, tc := range []struct {
		sign  decimalSign
		value string
		ok    bool
	}{
		{"", "-0.001", true}, {"nonnegative", "-0.001", false}, {"nonnegative", "0.000", true}, {"nonnegative", "0.001", true}, {"positive", "0.000", false}, {"positive", "-0.001", false}, {"positive", "0.001", true},
	} {
		d := Decimal{Entity: "rows", Field: "q", Format: exactdecimal.Format{Storage: "decimalText", Scale: 3, Digits: 12}, Sign: tc.sign}
		r := Registration{Declaration: Declaration{Decimals: []Decimal{d}}}
		err := r.Validate(nil, Images{"rows": {"row": {"q": tc.value}}})
		if (err == nil) != tc.ok {
			t.Fatalf("sign %q quantity %q: %v", tc.sign, tc.value, err)
		}
	}
	valid := `{"contract":"vibetable.plugin-transactions.v1","modelId":"m","version":1,"rules":[{"id":"r","kind":"atMostOne","entity":"e","fields":["a","b"]}],"decimals":[{"entity":"e","field":"q","storage":"decimalText","scale":3,"digits":6,"sign":%s}]}`
	for _, bad := range []string{`""`, `"signed"`, `null`, `0`} {
		if _, err := Decode([]byte(fmt.Sprintf(valid, bad))); err == nil {
			t.Fatalf("invalid sign %s", bad)
		}
	}
}

func TestDecimalBudgetIndependentOfRuleCapacity(t *testing.T) {
	d := Declaration{Contract: Contract, Version: 1, ModelID: "m", Rules: []Rule{{ID: "unique", Kind: "unique", Entity: "e", Fields: []string{"a"}}}}
	for i := 0; i < 66; i++ {
		d.Decimals = append(d.Decimals, Decimal{Entity: "e", Field: fmt.Sprint(i), Format: exactdecimal.Format{Storage: "decimalText", Digits: 2}})
	}
	if err := d.Validate(); err == nil {
		t.Fatal("66 decimal mappings accepted")
	}
	// The 65th mapping registers legally and keeps every earlier declaration.
	d.Decimals = d.Decimals[:65]
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGuardCoverageRequiresExactCanonicalSuperset(t *testing.T) {
	old := Registration{WorkspaceID: "w", PluginID: "p", InstallationID: "i", Declaration: Declaration{ModelID: "m", Version: 1, Rules: []Rule{{ID: "a", Kind: "unique", Entity: "e", Fields: []string{"a"}}}}, Bindings: map[string]pluginmodel.Binding{"e": {TableID: "t", Fields: map[string]string{"a": "fa", "b": "fb"}}}}
	next := old
	next.Declaration.Version++
	next.Declaration.Rules = append([]Rule{}, old.Declaration.Rules...)
	next.Declaration.Rules = append(next.Declaration.Rules, Rule{ID: "b", Kind: "unique", Entity: "e", Fields: []string{"b"}})
	if got := activeGuards([]Registration{old, next}); len(got) != 1 || got[0].Declaration.Version != 2 {
		t.Fatalf("superset: %v", got)
	}
	next.Declaration.Rules[0].Fields = []string{"b"}
	if got := activeGuards([]Registration{old, next}); len(got) != 2 {
		t.Fatal("changed rule superseded immutable guard")
	}
	next.Declaration.Rules[0] = old.Declaration.Rules[0]
	next.Bindings = map[string]pluginmodel.Binding{"e": {TableID: "other", Fields: old.Bindings["e"].Fields}}
	if got := activeGuards([]Registration{old, next}); len(got) != 2 {
		t.Fatal("physical binding replaced old guard")
	}
}
