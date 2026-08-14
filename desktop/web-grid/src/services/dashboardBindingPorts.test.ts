import { describe, expect, it } from "vitest";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { DashboardQueryExecutor, DashboardSchemaCatalog } from "./dashboardBindingPorts";

function harness(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown, requestId?: string) => void;
  posted: Array<Record<string, unknown>>;
} {
  let listener: ((event: { data: unknown }) => void) | null = null;
  const posted: Array<Record<string, unknown>> = [];
  const bridge = createHostBridge({
    timeoutMs: 1_000,
    webview: {
      addEventListener: (_type, fn) => { listener = fn; },
      removeEventListener: () => undefined,
      postMessage: (message) => { posted.push(message as Record<string, unknown>); },
    },
  });
  bridge.start();
  return {
    bridge,
    posted,
    emit: (type, payload, requestId) => listener?.({ data: JSON.stringify({ type, payload, requestId }) }),
  };
}

describe("dashboard binding host ports", () => {
  it("maps authoritative schema capabilities and caches by collection", async () => {
    const h = harness();
    const catalog = new DashboardSchemaCatalog(h.bridge);
    const loading = catalog.describe("orders", new AbortController().signal);
    const request = h.posted.at(-1)!;
    expect(request).toMatchObject({ type: "schema.describe", payload: { collection: "orders" } });
    const response = {
      contract: "vibetable.schema-describe.v1",
      collection: "orders",
      requestGeneration: 1,
      schema: {
        collection: "orders", primaryKey: "id", schemaRevision: "schema_7",
        permissionRevision: "schema_7", capabilityHash: "hash", lookupRevision: "lookup_7",
        normalizedRelations: [],
        columns: [{
          name: "amount", title: "Amount", fieldId: "fld_amount", dataType: "decimal",
          editable: true, nullable: true, filterOperators: ["eq", "gt"], groupable: true,
          summaryOperations: ["count", "sum"],
        }],
      },
      capabilities: {
        contract: "vibetable.relation-capabilities.v1", relationReadV1: true,
        relationEditV1: true, lookupQueryV1: true,
      },
    };
    h.emit("schema.describe", response, String(request.requestId));
    await expect(loading).resolves.toMatchObject({
      collectionId: "orders", revision: "schema_7",
      fields: [{ ref: "amount", fieldId: "fld_amount", groupable: true, summaryOperations: ["count", "sum"] }],
    });
    await catalog.describe("orders", new AbortController().signal);
    expect(h.posted.filter((item) => item.type === "schema.describe")).toHaveLength(1);
    catalog.invalidate("orders");
    const reloading = catalog.describe("orders", new AbortController().signal);
    const secondRequest = h.posted.at(-1)!;
    expect(h.posted.filter((item) => item.type === "schema.describe")).toHaveLength(2);
    h.emit("schema.describe", { ...response, requestGeneration: 2 }, String(secondRequest.requestId));
    await reloading;
  });

  it("forwards AbortSignal as dashboard.cancelRequested", async () => {
    const h = harness();
    const executor = new DashboardQueryExecutor(h.bridge);
    const controller = new AbortController();
    const loading = executor.execute("metric", {
      kind: "aggregate", collection: "orders", measures: [{ key: "value", op: "count", field: null }],
    }, controller.signal);
    const query = h.posted.at(-1)!;
    controller.abort();
    expect(h.posted.at(-1)).toMatchObject({
      type: "dashboard.cancelRequested", payload: { targetRequestId: query.requestId },
    });
    h.emit("dashboard.queryLoaded", { rows: [], truncated: false, maxPoints: 1 }, String(query.requestId));
    await expect(loading).resolves.toMatchObject({ rows: [] });
  });
});
