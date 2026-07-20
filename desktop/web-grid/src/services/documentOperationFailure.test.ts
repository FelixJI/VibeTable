import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useDocumentWorkspaceService } from "./documentWorkspaceService";
import { useDocumentWorkspaceStore } from "@/stores/documentWorkspaceStore";

describe("document operation failure notification", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("routes the typed host notification only to the document workspace store", () => {
    const listeners: Array<(event: { data: unknown }) => void> = [];
    const bridge = createHostBridge({ webview: {
      postMessage: () => undefined,
      addEventListener: (_type, listener) => listeners.push(listener),
      removeEventListener: () => undefined,
    } });
    bridge.start();
    setHostBridgeForTesting(bridge);
    useDocumentWorkspaceService();

    listeners[0]?.({ data: {
      type: "document.operationFailed",
      payload: {
        message: "拖入请求没有携带原生文件对象",
        code: "DOCUMENT_DROP_OBJECTS_MISSING",
      },
    } });

    const store = useDocumentWorkspaceStore();
    expect(store.phase).toBe("failed");
    expect(store.lastError).toBe("拖入请求没有携带原生文件对象");
    expect(store.lastErrorCode).toBe("DOCUMENT_DROP_OBJECTS_MISSING");
  });
});
