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
	for _, topic := range []string{"data.changed", "realtime.recovered"} {
		if !HasCurrentOwnerEventTopic(GoSidecar, topic) || HasCurrentOwnerEventTopic(PythonBff, topic) {
			t.Fatalf("%s must route through goSidecar after L4", topic)
		}
	}
	if !HasCurrentOwnerEventTopic(WpfHost, "task.changed") || HasCurrentOwnerEventTopic(PythonBff, "task.changed") {
		t.Fatal("task.changed renderer envelope must belong to wpfHost after L4")
	}
}

func TestGeneratedRPCDescriptorsKeepCanonicalPolicyAndReturnCopies(t *testing.T) {
	descriptors := RPCDescriptors()
	if len(descriptors) != 104 {
		t.Fatalf("RPCDescriptors length = %d, want 104", len(descriptors))
	}
	if descriptors[0].Method != "command.list" ||
		descriptors[len(descriptors)-1].Method != "version.save" {
		t.Fatalf("RPCDescriptors are not in canonical order: %#v", descriptors)
	}

	var settings RPCDescriptor
	for _, descriptor := range descriptors {
		if descriptor.Method == "field.settings.describe" {
			settings = descriptor
		}
	}
	if settings != (RPCDescriptor{
		Method: "field.settings.describe", Scope: WorkspaceScope, Audience: RendererPublic,
		CapabilityID: "schema.query", Owner: GoSidecar, Effect: ReadEffect,
	}) {
		t.Fatalf("field.settings.describe descriptor = %#v", settings)
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
	if got := CurrentOwnerRPCDescriptors(GoSidecar); len(got) != 20 ||
		got[0].Method != "events.reconcile" || got[1] != settings || got[2].Method != "file.list" ||
		got[3] != (RPCDescriptor{
			Method: "history.read", Scope: WorkspaceScope, Audience: RendererPublic,
			CapabilityID: "history.restore", Owner: GoSidecar, Effect: ReadEffect,
		}) || got[4].Method != "lookup.list" || got[5] != (RPCDescriptor{Method: "lookup.query", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[6] != (RPCDescriptor{Method: "lookup.valuePage", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[7].Method != "query.cursorFetch" || got[8].Method != "query.cursorOpen" || got[9].Method != "query.page" || got[10].Method != "query.readRows" ||
		got[11].Method != "query.selectionOpen" || got[12] != (RPCDescriptor{Method: "query.validateSnapshot", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "schema.query", Owner: GoSidecar, Effect: ReadEffect}) || got[13].Method != "query.view" || got[14] != (RPCDescriptor{Method: "relation.inspectPair", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[15].Method != "relation.previewDelta" || got[16] != (RPCDescriptor{Method: "relation.searchTargets", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[17].Method != "schema.describe" ||
		got[18].Method != "schema.getTable" || got[19].Method != "schema.list" {
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
