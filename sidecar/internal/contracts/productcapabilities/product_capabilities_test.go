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
	for _, method := range []string{"gridState.get", "gridState.save", "settings.readDevice", "settings.saveDevice"} {
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
	if len(descriptors) != 110 {
		t.Fatalf("RPCDescriptors length = %d, want 110", len(descriptors))
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
	contentMethods := map[string]bool{"contentProfile.commit": true, "contentProfile.delete": true, "contentProfile.load": true, "recordDocumentLink.commit": true, "recordDocumentLink.delete": true, "recordDocumentLink.list": true, "recordDocumentLink.repair": true}
	allGo := CurrentOwnerRPCDescriptors(GoSidecar)
	if len(allGo) != 57 {
		t.Fatalf("goSidecar count = %d", len(allGo))
	}
	schemaMethods := map[string]Effect{
		"schema.table.create": WriteEffect, "schema.delete": WriteEffect,
		"field.change.plan": WriteEffect, "field.change.apply": WriteEffect,
		"field.change.cancel": WriteEffect, "field.change.status": ReadEffect,
		"field.recycleBin.list": ReadEffect,
	}
	otherGo := []RPCDescriptor{}
	for _, descriptor := range allGo {
		if effect, migrated := schemaMethods[descriptor.Method]; migrated {
			capability := "schema.definition"
			if effect == ReadEffect {
				capability = "schema.query"
			}
			if descriptor.Owner != GoSidecar || descriptor.Scope != WorkspaceScope || descriptor.Audience != RendererPublic || descriptor.CapabilityID != capability || descriptor.Effect != effect {
				t.Fatalf("schema/field descriptor = %#v", descriptor)
			}
			delete(schemaMethods, descriptor.Method)
			continue
		}
		if !contentMethods[descriptor.Method] {
			otherGo = append(otherGo, descriptor)
			continue
		}
		effect := WriteEffect
		if descriptor.Method == "contentProfile.load" || descriptor.Method == "recordDocumentLink.list" {
			effect = ReadEffect
		}
		if descriptor.Owner != GoSidecar || descriptor.Scope != WorkspaceScope || descriptor.Audience != RendererPublic || descriptor.CapabilityID != "content.model" || descriptor.Effect != effect {
			t.Fatalf("content descriptor = %#v", descriptor)
		}
	}
	if len(schemaMethods) != 0 {
		t.Fatalf("missing migrated descriptors: %v", schemaMethods)
	}
	if got := otherGo; len(got) != 43 ||
		got[0].Method != "events.reconcile" || got[1] != settings || got[2].Method != "file.list" ||
		got[3] != (RPCDescriptor{Method: "formula.draft.validate", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "schema.formula", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[4] != (RPCDescriptor{Method: "formula.preview", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "schema.formula", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[5] != (RPCDescriptor{Method: "formula.validate", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "schema.formula", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[6] != (RPCDescriptor{Method: "history.applyRestore", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "history.restore", Owner: GoSidecar, Effect: WriteEffect}) || got[7] != (RPCDescriptor{Method: "history.previewRestore", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "history.restore", Owner: GoSidecar, Effect: ReadEffect}) || got[8] != (RPCDescriptor{
		Method: "history.read", Scope: WorkspaceScope, Audience: RendererPublic,
		CapabilityID: "history.restore", Owner: GoSidecar, Effect: ReadEffect,
	}) || got[9] != (RPCDescriptor{Method: "insights.dashboardQueryLimits", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[10] != (RPCDescriptor{Method: "insights.deleteDashboardWorkspace", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: WriteEffect}) ||
		got[11] != (RPCDescriptor{Method: "insights.executeDashboardQuery", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[12] != (RPCDescriptor{Method: "insights.listDashboards", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[13] != (RPCDescriptor{Method: "insights.panelManifest", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[14] != (RPCDescriptor{Method: "insights.readDashboardWorkspace", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[15] != (RPCDescriptor{Method: "insights.saveDashboardDraft", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "insights", Owner: GoSidecar, Effect: WriteEffect}) ||
		got[16] != (RPCDescriptor{Method: "interface.commit", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "content.model", Owner: GoSidecar, Effect: WriteEffect}) || got[17] != (RPCDescriptor{Method: "interface.delete", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "content.model", Owner: GoSidecar, Effect: WriteEffect}) || got[18] != (RPCDescriptor{Method: "interface.list", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "content.model", Owner: GoSidecar, Effect: ReadEffect}) || got[19] != (RPCDescriptor{Method: "interface.load", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "content.model", Owner: GoSidecar, Effect: ReadEffect}) || got[20].Method != "lookup.list" || got[21] != (RPCDescriptor{Method: "lookup.query", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[22] != (RPCDescriptor{Method: "lookup.valuePage", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[23] != (RPCDescriptor{Method: "mutation.apply", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "data.mutation", Owner: GoSidecar, Effect: WriteEffect}) || got[24] != (RPCDescriptor{Method: "mutation.preview", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "data.mutation", Owner: GoSidecar, Effect: ReadEffect}) ||
		got[25].Method != "preset.delete" || got[26].Method != "preset.list" || got[27].Method != "preset.save" || got[28].Method != "query.cursorFetch" || got[29].Method != "query.cursorOpen" || got[30].Method != "query.page" || got[31].Method != "query.readRows" ||
		got[32].Method != "query.selectionOpen" || got[33] != (RPCDescriptor{Method: "query.validateSnapshot", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "schema.query", Owner: GoSidecar, Effect: ReadEffect}) || got[34].Method != "query.view" || got[35] != (RPCDescriptor{Method: "relation.inspectPair", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[36] != (RPCDescriptor{Method: "relation.previewDelta", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[37] != (RPCDescriptor{Method: "relation.searchTargets", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "relation.lookup", Owner: GoSidecar, Effect: ReadEffect}) || got[38].Method != "schema.describe" ||
		got[39].Method != "schema.getTable" || got[40].Method != "schema.list" ||
		got[41] != (RPCDescriptor{Method: "settings.commitWorkCalendar", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "workspace.calendar", Owner: GoSidecar, Effect: WriteEffect}) ||
		got[42] != (RPCDescriptor{Method: "settings.readWorkCalendar", Scope: WorkspaceScope, Audience: RendererPublic, CapabilityID: "workspace.calendar", Owner: GoSidecar, Effect: ReadEffect}) {
		t.Fatalf("goSidecar descriptors = %#v", got)
	}
	if got := CurrentOwnerRPCDescriptors(WpfHost); len(got) != 4 ||
		got[0].Method != "gridState.get" || got[1].Method != "gridState.save" ||
		got[2].Method != "settings.readDevice" || got[3].Method != "settings.saveDevice" {
		t.Fatalf("wpfHost descriptors = %#v, want gridState.get, gridState.save, settings.readDevice and settings.saveDevice", got)
	}

	descriptors[0].Method = "mutated"
	if got := RPCDescriptors()[0].Method; got != "command.list" {
		t.Fatalf("RPCDescriptors leaked mutable storage: first method = %q", got)
	}
}
