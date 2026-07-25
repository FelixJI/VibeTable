import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useWorkspaceService } from "./workspaceService";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { usePluginStore } from "@/stores/pluginStore";

function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
  posted: unknown[];
} {
  let listener: ((event: { data: unknown }) => void) | null = null;
  const posted: unknown[] = [];
  const shim = {
    addEventListener: (_: string, fn: (event: { data: unknown }) => void) => { listener = fn; },
    removeEventListener: () => {},
    postMessage: (message: unknown) => { posted.push(message); },
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    posted,
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

  it("switches plugin isolation to the opened product project", () => {
    const { bridge, emit, posted } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();

    emit("database.opened", {
      tables: ["orders"],
      views: [],
      projectKey: "local:workspace-a",
      projectRevision: "workspace-r7",
      currentUser: { id: "user-7", displayName: "Alice" },
      hostVersion: "2.4.1",
    });

    expect(usePluginStore().projectKey).toBe("local:workspace-a");
    expect(usePluginStore().projectRevision).toBe("workspace-r7");
    expect(usePluginStore().currentUser).toMatchObject({ id: "user-7", displayName: "Alice" });
    expect(usePluginStore().hostVersion).toBe("2.4.1");
    expect(posted.at(-1)).toMatchObject({
      type: "plugin.catalog.list",
      payload: { projectKey: "local:workspace-a" },
    });
  });

  it("advances plugin project revision when product collections change", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    useWorkspaceService().init();
    const pluginStore = usePluginStore();
    pluginStore.setProjectContext("local:workspace-a", "workspace-r7");

    emit("database.collectionsChanged", {
      tables: ["orders"],
      projectRevision: "workspace-r8",
    });

    expect(pluginStore.projectRevision).toBe("workspace-r8");
  });
});
