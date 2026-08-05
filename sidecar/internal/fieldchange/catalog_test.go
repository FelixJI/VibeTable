package fieldchange

import (
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestLifecycleDependencyCodeUsesRelationSpecificContract(t *testing.T) {
	if got := lifecycleDependencyCode(v2.FieldDefinition{}); got != "field.lifecycle.dependencies" {
		t.Fatalf("scalar dependency code = %q", got)
	}
	if got := lifecycleDependencyCode(v2.FieldDefinition{
		Relation: &v2.RelationSpec{},
	}); got != "relation.delete.dependency_blocked" {
		t.Fatalf("relation dependency code = %q", got)
	}
}

func TestLookupMetadataDependencyIncludesEveryRelationPathStep(t *testing.T) {
	metadata := `{"relationFieldId":"root_relation","path":[` +
		`{"relationFieldId":"root_relation"},` +
		`{"relationFieldId":"middle_relation"}],"targetFieldId":"target_name"}`
	for _, fieldID := range []string{"root_relation", "middle_relation", "target_name"} {
		depends, err := lookupMetadataDependsOnField(metadata, fieldID)
		if err != nil || !depends {
			t.Fatalf("dependency %q = %v, err=%v", fieldID, depends, err)
		}
	}
	depends, err := lookupMetadataDependsOnField(metadata, "unrelated")
	if err != nil || depends {
		t.Fatalf("unrelated dependency = %v, err=%v", depends, err)
	}
}
