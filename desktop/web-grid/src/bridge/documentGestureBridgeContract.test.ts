import { describe, expect, it, vi } from "vitest";
import { createHostBridge } from "./hostBridge";

describe("document gesture bridge whitelist", () => {
  it("posts import/drop/drag-out/relink intents without paths or request ids", () => {
    const postMessage = vi.fn();
    const bridge = createHostBridge({ webview: {
      postMessage,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    bridge.notify("document.importRequested", { scope: { kind: "global" } });
    bridge.notify("document.externalDropRequested", { scope: { kind: "record", collection: "orders", itemId: 7 } });
    bridge.notify("document.dragOutRequested", { handle: "opaque-drag" });
    bridge.notify("document.relinkRequested", { handle: "opaque-missing" });
    expect(postMessage.mock.calls.map((call) => call[0])).toEqual([
      { type: "document.importRequested", payload: { scope: { kind: "global" } } },
      { type: "document.externalDropRequested", payload: { scope: { kind: "record", collection: "orders", itemId: 7 } } },
      { type: "document.dragOutRequested", payload: { handle: "opaque-drag" } },
      { type: "document.relinkRequested", payload: { handle: "opaque-missing" } },
    ]);
    expect(JSON.stringify(postMessage.mock.calls)).not.toMatch(/[A-Z]:\\|filePath|localPath|entryHandle/);
  });
});
