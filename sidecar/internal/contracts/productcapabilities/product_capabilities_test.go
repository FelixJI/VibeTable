package productcapabilities

import "testing"

func TestGeneratedCurrentOwnerCatalogPreservesUnmigratedRPCAndRoutesRealtime(t *testing.T) {
	if !HasCurrentOwnerRPCMethod(PythonBff, "schema.getTable") {
		t.Fatal("schema.getTable must remain on pythonBff during L1")
	}
	if HasCurrentOwnerRPCMethod(GoSidecar, "schema.getTable") {
		t.Fatal("L1 must not switch schema.getTable to Go")
	}
	if !HasCurrentOwnerEventTopic(GoSidecar, "data.changed") ||
		!HasCurrentOwnerEventTopic(GoSidecar, "realtime.recovered") ||
		!HasCurrentOwnerEventTopic(WpfHost, "task.changed") {
		t.Fatal("Go owns realtime data/recovery and Host routes the disjoint Go/Python task producers")
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
		Owner:        PythonBff,
		Effect:       ReadEffect,
	}) {
		t.Fatalf("schema.getTable descriptor = %#v", schema)
	}
	if got := CurrentOwnerRPCDescriptors(GoSidecar); len(got) != 2 || got[0] != (RPCDescriptor{
		Method: "events.reconcile", Scope: WorkspaceScope, Audience: RendererPublic,
		CapabilityID: "realtime", Owner: GoSidecar, Effect: ReadEffect,
	}) || got[1] != (RPCDescriptor{
		Method: "schema.list", Scope: WorkspaceScope, Audience: RendererPublic,
		CapabilityID: "schema.query", Owner: GoSidecar, Effect: ReadEffect,
	}) {
		t.Fatalf("goSidecar descriptors = %#v", got)
	}

	descriptors[0].Method = "mutated"
	if got := RPCDescriptors()[0].Method; got != "command.list" {
		t.Fatalf("RPCDescriptors leaked mutable storage: first method = %q", got)
	}
}
