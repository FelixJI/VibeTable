package transactionrules

import (
	"fmt"
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

func TestDecimalBudgetIndependentOfRuleCapacity(t *testing.T) {
	d := Declaration{Contract: Contract, Version: 1, ModelID: "m", Rules: []Rule{{ID: "unique", Kind: "unique", Entity: "e", Fields: []string{"a"}}}}
	for i := 0; i < 65; i++ {
		d.Decimals = append(d.Decimals, Decimal{Entity: "e", Field: fmt.Sprint(i), Format: exactdecimal.Format{Storage: "decimalText", Digits: 2}})
	}
	if err := d.Validate(); err == nil {
		t.Fatal("65 decimal mappings accepted")
	}
	d.Decimals = d.Decimals[:64]
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
