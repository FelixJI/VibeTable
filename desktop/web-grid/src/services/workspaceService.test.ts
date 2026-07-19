import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useWorkspaceService } from "./workspaceService";
import { useWorkspaceStore } from "@/stores/workspaceStore";

function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
} {
  let listener: ((event: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (event: { data: unknown }) => void) => { listener = fn; },
    removeEventListener: () => {},
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    emit: (type, payload) => listener?.({ data: JSON.stringify({ type, payload }) }),
  };
}

describe("workspaceService display names", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("projects database.opened displayNames without changing physical keys", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    emit("database.opened", {
      tables: ["vt_t_01"],
      views: [],
      displayNames: { vt_t_01: "客户清单" },
    });
    const store = useWorkspaceStore();
    expect(store.displayNames.vt_t_01).toBe("客户清单");
    expect(store.collections[0]).toMatchObject({
      collection: "vt_t_01",
      displayName: "客户清单",
    });
  });

  it("replaces labels on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    emit("database.collectionsChanged", {
      tables: ["vt_t_02"],
      displayNames: { vt_t_02: "订单" },
    });
    const store = useWorkspaceStore();
    expect(store.displayNames).toEqual({ vt_t_02: "订单" });
    expect(store.collections[0].collection).toBe("vt_t_02");
  });
});
