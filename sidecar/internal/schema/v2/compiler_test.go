package v2_test

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestCompilerEmitsValueAndHiddenPresenceWithProviderIdentities(t *testing.T) {
	t.Parallel()
	definition := validNumberDefinition()
	definition.Value.Required = true
	compiled, err := v2.CompileField(definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := compiled.Value.(*core.NumberField)
	if !ok {
		t.Fatalf("value field type = %T", compiled.Value)
	}
	if number.Id != definition.Identity.ProviderFieldID ||
		number.Name != definition.Identity.PhysicalName ||
		number.Required {
		t.Fatalf("compiled number = %#v", number)
	}
	presence, ok := compiled.Presence.(*core.BoolField)
	if !ok {
		t.Fatalf("presence field type = %T", compiled.Presence)
	}
	if presence.Id != definition.Value.Presence.ProviderFieldID ||
		presence.Name != definition.Value.Presence.PhysicalName ||
		!presence.Hidden ||
		presence.Required {
		t.Fatalf("compiled presence = %#v", presence)
	}
}

func TestCompilerStoresStableSelectOptionIDsInsteadOfLabels(t *testing.T) {
	t.Parallel()
	definition := validNumberDefinition()
	definition.LogicalType = v2.LogicalSelect
	definition.Storage.Kind = v2.StorageSelect
	definition.Display.Kind = v2.DisplaySelect
	one := 1
	definition.Constraints.Selection.Max = &one
	definition.Select = &v2.SelectSpec{Options: []v2.SelectOption{
		{
			OptionID: "opt_01JOPTIONA", Label: "进行中", Color: "blue",
			Order: 10, State: v2.OptionActive,
		},
		{
			OptionID: "opt_01JOPTIONB", Label: "已完成", Color: "green",
			Order: 20, State: v2.OptionRetired,
		},
	}}
	compiled, err := v2.CompileField(definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	selectField, ok := compiled.Value.(*core.SelectField)
	if !ok {
		t.Fatalf("value field type = %T", compiled.Value)
	}
	if len(selectField.Values) != 2 ||
		selectField.Values[0] != "opt_01JOPTIONA" ||
		selectField.Values[1] != "opt_01JOPTIONB" {
		t.Fatalf("stored select values = %#v", selectField.Values)
	}
}

func TestCompileUniqueIndexIgnoresMissingButIncludesExplicitZero(t *testing.T) {
	t.Parallel()
	definition := validNumberDefinition()
	definition.Constraints.Unique.Enabled = true
	index, ok, err := v2.CompileUniqueIndex("orders", definition)
	if err != nil {
		t.Fatal(err)
	}
	if !ok ||
		!strings.Contains(index, "`f_01jabcde`") ||
		!strings.Contains(index, "WHERE `__vt_has_f_01jabcde` = 1") {
		t.Fatalf("partial unique index = %q", index)
	}
}
