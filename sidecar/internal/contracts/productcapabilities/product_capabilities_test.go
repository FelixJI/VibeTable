package productcapabilities

import "testing"

func TestGeneratedCurrentOwnerCatalogKeepsMigratedOwners(t *testing.T) {
	for _, method := range []string{"file.list", "history.read", "lookup.list", "query.readRows", "schema.describe"} {
		if HasCurrentOwnerRPCMethod(PythonBff, method) {
			t.Fatalf("%s must not remain on pythonBff after its Go migration", method)
		}
		if !HasCurrentOwnerRPCMethod(GoSidecar, method) {
			t.Fatalf("%s must route through goSidecar", method)
		}
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
	for _, method := range []string{"settings.readDevice", "settings.saveDevice"} {
		if HasCurrentOwnerRPCMethod(PythonBff, method) {
			t.Fatalf("%s must not remain on pythonBff after L6", method)
		}
		if !HasCurrentOwnerRPCMethod(WpfHost, method) {
			t.Fatalf("L6 must route %s through wpfHost", method)
		}
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
	if got := CurrentOwnerRPCDescriptors(GoSidecar); len(got) != 16 ||
		got[0].Method != "events.reconcile" || got[1].Method != "file.list" ||
		got[2] != (RPCDescriptor{
			Method: "history.read", Scope: WorkspaceScope, Audience: RendererPublic,
			CapabilityID: "history.restore", Owner: GoSidecar, Effect: ReadEffect,
		}) || got[3].Method != "lookup.list" || got[4].Method != "lookup.valuePage" || got[5].Method != "query.cursorFetch" || got[6].Method != "query.cursorOpen" || got[7].Method != "query.page" || got[8].Method != "query.readRows" ||
		got[9].Method != "query.selectionOpen" || got[10].Method != "query.view" || got[11].Method != "relation.previewDelta" || got[12] != (RPCDescriptor{Method: "relation.searchTargets", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[13].Method != "schema.describe" ||
		got[14].Method != "schema.getTable" || got[15].Method != "schema.list" {
		t.Fatalf("goSidecar descriptors = %#v", got)
	}
	if got := CurrentOwnerRPCDescriptors(WpfHost); len(got) != 2 ||
		got[0].Method != "settings.readDevice" || got[1].Method != "settings.saveDevice" {
		t.Fatalf("wpfHost descriptors = %#v, want settings.readDevice and settings.saveDevice", got)
	}

	descriptors[0].Method = "mutated"
	if got := RPCDescriptors()[0].Method; got != "command.list" {
		t.Fatalf("RPCDescriptors leaked mutable storage: first method = %q", got)
	}
}
