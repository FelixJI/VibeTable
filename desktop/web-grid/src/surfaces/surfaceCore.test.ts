import { describe, expect, it, vi } from "vitest";
import type {
  InterfaceAction,
  InterfaceCommitRequest,
  InterfaceDefinition,
  InterfaceSnapshot,
} from "@/contracts/generated/workbench";
import {
  ActionRuntime,
  InMemorySurfaceRepository,
  SurfaceEditorController,
  validateInterfaceDefinition,
  type ActionRuntimePorts,
} from "./surfaceCore";

function definition(): InterfaceDefinition {
  return {
    contractVersion: "1.0",
    interfaceId: "if_orders",
    name: "Orders workspace",
	bindings: [{
	  bindingId: "orders",
	  query: {
		contractVersion: "1.0", tableId: "orders", fields: ["name", "status"],
		filters: [], sorts: [], cursor: null, pageSize: 100,
	  },
	  variables: [],
	}],
    actions: [
      action("create", "record.create", "orders"),
      action("update", "record.update", "orders"),
      action("refresh", "binding.refresh", "orders"),
      { ...action("navigate", "navigate", null), targetPageId: "detail" },
      { ...action("plugin", "plugin", null), pluginId: "crm", pluginActionId: "sync", requiresConfirmation: true },
    ],
    pages: [
      {
        pageId: "list", title: "Orders", elements: [{
          elementId: "layout", kind: "section", bindingId: null, actionId: null,
          text: null, width: "full", children: [{
            elementId: "records", kind: "record-list", bindingId: "orders", actionId: null,
            text: null, width: "full", children: [],
          }],
        }],
      },
      { pageId: "detail", title: "Order", elements: [] },
    ],
  };
}

function action(
  actionId: string,
  kind: InterfaceAction["kind"],
  bindingId: string | null,
): InterfaceAction {
  return {
    actionId, kind, bindingId, targetPageId: null, pluginId: null, pluginActionId: null,
    requiresConfirmation: false,
  };
}

describe("interface definition validation", () => {
  it("accepts a constrained element tree and rejects dangling/raw action paths", () => {
    expect(validateInterfaceDefinition(definition())).toEqual([]);
    const invalid = structuredClone(definition());
    (invalid.pages[0]!.elements[0]!.children[0] as unknown as { bindingId: string }).bindingId = "missing";
    const mutableActions = invalid.actions as unknown as Array<InterfaceAction | Record<string, unknown>>;
    mutableActions.push({ ...action("raw", "navigate", null), targetPageId: "missing" });
    mutableActions.push({
      actionId: "rpc", kind: "rpc", bindingId: null, targetPageId: null,
      pluginId: null, pluginActionId: null, requiresConfirmation: false,
    });
    expect(validateInterfaceDefinition(invalid).map((item) => item.code)).toEqual([
      "surface.binding_missing", "surface.page_missing", "surface.action_unknown",
    ]);
  });
});

describe("SurfaceEditorController", () => {
  it("starts a new independent Interface draft without reading persistence", () => {
    const repository = new InMemorySurfaceRepository();
    const controller = new SurfaceEditorController(repository);
    const draft = definition();

    controller.start(draft);

    expect(controller.state).toMatchObject({
      phase: "ready",
      revision: null,
      dirty: true,
      draft: { interfaceId: draft.interfaceId },
    });
    expect(controller.runtimeContext.activePageId).toBe("list");
  });

  it("keeps runtime context out of the definition and saves with expected revision", async () => {
    const repo = new InMemorySurfaceRepository([{ definition: definition(), revision: "surface_1" }]);
    const controller = new SurfaceEditorController(repo);
    await controller.open("if_orders");
    controller.dispatch({ type: "rename", name: "Order operations" });
    controller.setRuntimeContext({ selectedRecordId: "r1", activePageId: "detail" });
    expect(controller.state.dirty).toBe(true);
    expect(controller.state.draft).not.toHaveProperty("runtimeContext");
    await controller.save("commit-1");
    expect(controller.state).toMatchObject({ dirty: false, revision: "surface_2", phase: "ready" });
    await expect(repo.load("if_orders", new AbortController().signal)).resolves.toMatchObject({
      definition: { name: "Order operations" }, revision: "surface_2",
    });
  });

  it("surfaces a revision conflict without replacing the local draft", async () => {
    const base = definition();
    let current: InterfaceSnapshot = { definition: base, revision: "surface_2" };
    const repo = {
      list: async () => [],
      load: async () => current,
      commit: async (request: InterfaceCommitRequest) => {
        expect(request.expectedRevision).toBe("surface_1");
        const error = Object.assign(new Error("changed elsewhere"), { code: "surface.edit_conflict" });
        throw error;
      },
      delete: async () => undefined,
    };
    const controller = new SurfaceEditorController(repo);
    current = { definition: base, revision: "surface_1" };
    await controller.open("if_orders");
    controller.dispatch({ type: "rename", name: "Local draft" });
    current = { definition: { ...base, name: "Remote" }, revision: "surface_2" };
    await controller.save("commit-2");
    expect(controller.state).toMatchObject({ phase: "conflict", dirty: true, draft: { name: "Local draft" } });
    await controller.reload();
    expect(controller.state).toMatchObject({ phase: "ready", dirty: false, draft: { name: "Remote" } });
  });
});

describe("ActionRuntime", () => {
  it("executes only the closed action adapters with confirmation and cancellation", async () => {
    const ports: ActionRuntimePorts = {
      createRecord: vi.fn(async () => ({ recordId: "r2" })),
      updateRecord: vi.fn(async () => ({ recordId: "r1" })),
      refreshBinding: vi.fn(async () => undefined),
      navigate: vi.fn(async () => undefined),
      runPluginAction: vi.fn(async () => ({ taskId: "t1" })),
      confirm: vi.fn(async () => false),
    };
    const runtime = new ActionRuntime(ports);
    const context = { definition: definition(), values: { name: "Ada" }, recordId: "r1" };
    await expect(runtime.execute(definition().actions[0]!, context, new AbortController().signal))
      .resolves.toMatchObject({ state: "succeeded", value: { recordId: "r2" } });
    await expect(runtime.execute(definition().actions[4]!, context, new AbortController().signal))
      .resolves.toMatchObject({ state: "rejected" });
    expect(ports.runPluginAction).not.toHaveBeenCalled();

    const controller = new AbortController();
    controller.abort();
    await expect(runtime.execute(definition().actions[2]!, context, controller.signal))
      .resolves.toMatchObject({ state: "cancelled" });
    const raw = { ...action("raw", "navigate", null), kind: "http" } as unknown as InterfaceAction;
    await expect(runtime.execute(raw, context, new AbortController().signal))
      .resolves.toMatchObject({ state: "failed", error: { code: "surface.action_unknown" } });
  });
});
