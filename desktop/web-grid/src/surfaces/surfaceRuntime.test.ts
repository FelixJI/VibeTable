import { describe, expect, it, vi } from "vitest";
import type { InterfaceDefinition } from "@/contracts/generated/workbench";
import { SurfaceRuntimeController, bindingDependencyLayers, collectPageBindingIds } from "./surfaceRuntime";

const definition: InterfaceDefinition = {
  contractVersion: "1.0",
  interfaceId: "if-1",
  name: "Operations",
  bindings: [
    binding("visible", "orders", ["name"]),
    binding("hidden", "customers", ["name"]),
  ],
  actions: [],
  pages: [
    { pageId: "one", title: "One", elements: [{ elementId: "list", kind: "record-list", bindingId: "visible", actionId: null, text: null, width: "full", children: [] }] },
    { pageId: "two", title: "Two", elements: [{ elementId: "detail", kind: "record-detail", bindingId: "hidden", actionId: null, text: null, width: "full", children: [] }] },
  ],
};

function binding(bindingId: string, tableId: string, fields: string[]) {
  return {
    bindingId,
    query: {
      contractVersion: "1.0" as const, tableId, fields,
      filters: [], sorts: [], cursor: null, pageSize: 100,
    },
    variables: [],
  };
}

describe("SurfaceRuntimeController", () => {
  it("collects nested binding references", () => {
    expect([...collectPageBindingIds([{ elementId: "section", kind: "section", bindingId: null, actionId: null, text: null, width: "full", children: definition.pages[0]!.elements }])]).toEqual(["visible"]);
  });

  it("never queries bindings from invisible pages", async () => {
    const read = vi.fn(async (binding: { bindingId: string }) => ({
      rows: [{ binding: binding.bindingId }],
      offset: 0,
      filteredRows: 1,
    }));
    const runtime = new SurfaceRuntimeController({ read });

    await runtime.activate(definition, "one");

    expect(read).toHaveBeenCalledTimes(1);
    expect(read.mock.calls[0]?.[0].bindingId).toBe("visible");
    expect(runtime.data).toEqual({
      visible: {
        state: "ready",
        rows: [{ binding: "visible" }],
        offset: 0,
        filteredRows: 1,
        error: null,
      },
    });
  });

  it("evaluates selected-record dependencies in topological layers", async () => {
    const source = binding("source", "orders", ["id"]);
    const middle = {
      ...binding("middle", "lines", ["orderId", "id"]),
      variables: [{
        variableId: "order", targetFieldId: "orderId", operator: "eq" as const,
        source: "selectedRecordField" as const, sourceBindingId: "source", sourceFieldId: "id", value: null,
      }],
    };
    const leaf = {
      ...binding("leaf", "notes", ["lineId"]),
      variables: [{
        variableId: "line", targetFieldId: "lineId", operator: "eq" as const,
        source: "selectedRecordField" as const, sourceBindingId: "middle", sourceFieldId: "id", value: null,
      }],
    };
    expect(bindingDependencyLayers([leaf, source, middle])?.map((layer) =>
      layer.map((item) => item.bindingId))).toEqual([["source"], ["middle"], ["leaf"]]);
    expect(bindingDependencyLayers([
      { ...source, variables: [{ ...middle.variables[0]!, sourceBindingId: "middle" }] },
      { ...middle, variables: [{ ...middle.variables[0]!, sourceBindingId: "source" }] },
    ])).toBeNull();
  });
});
