import { describe, expect, it, vi } from "vitest";
import { createHostBridge } from "./hostBridge";

describe("preset/version bridge whitelist", () => {
  it("posts only the fixed preset and content-version use cases", () => {
    const postMessage = vi.fn();
    const bridge = createHostBridge({
      webview: {
        postMessage,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      },
    });
    const view = {
      filters: [], sorts: [], search: "", visibleFields: [], layout: "table",
    } as const;

    bridge.notify("preset.list", { collection: "orders" });
    bridge.notify("preset.save", {
      collection: "orders", name: "My view", view, operationId: "op-1",
    });
    bridge.notify("preset.delete", {
      presetId: "p1", expectedRevision: "rev-p1", operationId: "op-2",
    });
    bridge.notify("version.list", { collection: "orders", itemId: "row-1" });
    bridge.notify("version.create", {
      collection: "orders", itemId: "row-1", key: "draft",
      name: "Draft", operationId: "op-3",
    });
    bridge.notify("version.save", {
      collection: "orders", itemId: "row-1", versionId: "v1",
      values: {}, operationId: "op-4",
    });
    bridge.notify("version.compare", {
      collection: "orders", itemId: "row-1", versionId: "v1",
    });
    bridge.notify("version.promote", {
      collection: "orders", itemId: "row-1", versionId: "v1",
      mainHash: "hash-1", operationId: "op-5",
    });
    bridge.notify("version.delete", {
      collection: "orders", itemId: "row-1", versionId: "v1",
      expectedRevision: "rev-v1", operationId: "op-6",
    });

    expect(postMessage.mock.calls.map(([message]) => message.type)).toEqual([
      "preset.list", "preset.save", "preset.delete",
      "version.list", "version.create", "version.save",
      "version.compare", "version.promote", "version.delete",
    ]);
  });
});
