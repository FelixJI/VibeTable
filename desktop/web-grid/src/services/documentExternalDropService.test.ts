import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDocumentWorkspaceService } from "./documentWorkspaceService";

describe("document external drop transport", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("keeps file metadata out of JSON and sends File objects only as additionalObjects", () => {
    const postMessage = vi.fn();
    const postMessageWithAdditionalObjects = vi.fn();
    const bridge = createHostBridge({ webview: {
      postMessage,
      postMessageWithAdditionalObjects,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const file = new File(["private contents"], "confidential.xlsx", {
      type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    });
    useDocumentWorkspaceService().dispatch({
      type: "document.externalDropRequested",
      scope: { kind: "global" },
      files: [file],
    });

    expect(postMessage).not.toHaveBeenCalled();
    expect(postMessageWithAdditionalObjects).toHaveBeenCalledTimes(1);
    const [envelope, objects] = postMessageWithAdditionalObjects.mock.calls[0]!;
    expect(envelope).toEqual({
      type: "document.externalDropRequested",
      nativeObjects: true,
      payload: { scope: { kind: "global" } },
    });
    expect(JSON.stringify(envelope)).not.toMatch(/confidential|xlsx|private contents|path|base64/i);
    expect(objects).toEqual([file]);
  });

  it("falls back to the path-free notify when additionalObjects is unavailable", () => {
    const postMessage = vi.fn();
    const bridge = createHostBridge({ webview: {
      postMessage,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    } });
    setHostBridgeForTesting(bridge);
    const file = new File(["ignored"], "fallback.pdf", { type: "application/pdf" });
    useDocumentWorkspaceService().dispatch({
      type: "document.externalDropRequested",
      scope: { kind: "record", collection: "orders", itemId: 7 },
      files: [file],
    });

    expect(postMessage).toHaveBeenCalledWith({
      type: "document.externalDropRequested",
      payload: { scope: { kind: "record", collection: "orders", itemId: 7 } },
    });
    expect(JSON.stringify(postMessage.mock.calls)).not.toMatch(/fallback\.pdf|ignored|path|base64/i);
  });
});
