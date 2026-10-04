package sourceimport

import (
	"context"
	"reflect"
	"testing"
)

func TestPreviewDoesNotMutateOrRetainProviderBuffers(t *testing.T) {
	source := relationSnapshot()
	refs := []string{"r1", "r2"}
	source.Tables[0].Records[0].Values["f_peer"] = refs
	before := append([]string{}, refs...)
	plan, err := Preview(context.Background(), source, allSelectedOptions(), nil)
	if err != nil || !plan.CanApply {
		t.Fatalf("preview: %v, %#v", err, plan.Diagnostics)
	}
	if !reflect.DeepEqual(source.Tables[0].Records[0].Values["f_peer"], before) {
		t.Fatal("preview modified the caller's typed provider buffer")
	}
	source.Tables[0].Fields[0].Name = "changed"
	refs[0] = "changed"
	for _, table := range plan.Tables {
		if table.SourceID != "src_projects" {
			continue
		}
		for _, field := range table.Fields {
			if field.Source.ID == "f_title" && field.Source.Name == "changed" {
				t.Fatal("plan retained mutable provider field")
			}
		}
		for _, row := range table.Records {
			if row.ID != "r1" {
				continue
			}
			got, err := stringList(row.Values["f_peer"])
			if err != nil || !reflect.DeepEqual(got, before) {
				t.Fatal("plan retained mutable provider record buffer")
			}
		}
	}
}
