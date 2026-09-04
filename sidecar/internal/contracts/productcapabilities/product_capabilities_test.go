package productcapabilities

import "testing"

func TestGeneratedCurrentOwnerCatalogMovesReconcileFileAndSchemaReadsToGo(t *testing.T) {
	if HasCurrentOwnerRPCMethod(PythonBff, "file.list") {
		t.Fatal("file.list must not remain on pythonBff after L3B")
	}
	if !HasCurrentOwnerRPCMethod(GoSidecar, "file.list") {
		t.Fatal("L3B must route file.list through goSidecar")
	}
	if HasCurrentOwnerRPCMethod(PythonBff, "events.reconcile") {
		t.Fatal("events.reconcile must not remain on pythonBff after L4")
	}
	if !HasCurrentOwnerRPCMethod(GoSidecar, "events.reconcile") {
		t.Fatal("L4 must route events.reconcile through goSidecar")
	}
	if HasCurrentOwnerRPCMethod(PythonBff, "schema.getTable") {
		t.Fatal("schema.getTable must not remain on pythonBff after L3A")
	}
	if !HasCurrentOwnerRPCMethod(GoSidecar, "schema.getTable") {
		t.Fatal("L3A must route schema.getTable through goSidecar")
	}
	if !HasCurrentOwnerEventTopic(PythonBff, "data.changed") {
		t.Fatal("data.changed must remain on pythonBff during L1")
	}
}

func TestGeneratedRPCDescriptorsKeepCanonicalPolicyAndReturnCopies(t *testing.T) {
	descriptors := RPCDescriptors()
	if len(descriptors) != 102 {
		t.Fatalf("RPCDescriptors length = %d, want 102", len(descriptors))
	}
	if descriptors[0].Method != "command.list" ||
		descriptors[len(descriptors)-1].Method != "version.save" {
		t.Fatalf("RPCDescriptors are not in canonical order: %#v", descriptors)
	}

	var schema RPCDescriptor
	for _, descriptor := range descriptors {
		if descriptor.Method == "schema.getTable" {
			schema = descriptor
			break
		}
	}
	if schema != (RPCDescriptor{
		Method:       "schema.getTable",
		Scope:        WorkspaceScope,
		Audience:     RendererPublic,
		CapabilityID: "schema.query",
		Owner:        GoSidecar,
		Effect:       ReadEffect,
	}) {
		t.Fatalf("schema.getTable descriptor = %#v", schema)
	}
	if got := CurrentOwnerRPCDescriptors(GoSidecar); len(got) != 4 ||
		got[0].Method != "events.reconcile" || got[1].Method != "file.list" ||
		got[2].Method != "schema.getTable" ||
		got[3].Method != "schema.list" {
		t.Fatalf("goSidecar descriptors = %#v", got)
	}

	descriptors[0].Method = "mutated"
	if got := RPCDescriptors()[0].Method; got != "command.list" {
		t.Fatalf("RPCDescriptors leaked mutable storage: first method = %q", got)
	}
}
