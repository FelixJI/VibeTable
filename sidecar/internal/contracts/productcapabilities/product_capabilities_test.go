package productcapabilities

import "testing"

func TestGeneratedCurrentOwnerCatalogKeepsL1OnPython(t *testing.T) {
	if !HasCurrentOwnerRPCMethod(PythonBff, "schema.getTable") {
		t.Fatal("schema.getTable must remain on pythonBff during L1")
	}
	if HasCurrentOwnerRPCMethod(GoSidecar, "schema.getTable") {
		t.Fatal("L1 must not switch schema.getTable to Go")
	}
	if !HasCurrentOwnerEventTopic(PythonBff, "data.changed") {
		t.Fatal("data.changed must remain on pythonBff during L1")
	}
}
