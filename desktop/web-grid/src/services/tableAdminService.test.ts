import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminService } from "./tableAdminService";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
} from "@/contracts";

/**
 * Shim-bridge helper, mirroring `errorRouter.test.ts`. The host bridge wraps a
 * WebView-like object; we install a fake whose `addEventListener` captures the
 * listener so a test can drive inbound events via `emit(type, payload)`.
 */
function makeShimBridge(): {
  bridge: HostBridge;
  emit: (type: string, payload: unknown) => void;
} {
  let listener: ((e: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (e: { data: unknown }) => void) => {
      listener = fn;
    },
    removeEventListener: (
      _: string,
      fn: (e: { data: unknown }) => void,
    ) => {
      if (listener === fn) listener = null;
    },
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return {
    bridge,
    emit: (type, payload) =>
      listener?.({
        data:
          typeof payload === "string"
            ? payload
            : JSON.stringify({ type, payload }),
      }),
  };
}

describe("tableAdminService", () => {
  beforeEach(() => setActivePinia(createPinia()));

  // CRITICAL: `useHostBridge` is a module singleton. Reset to null after each
  // test so the fake bridge does not leak into other test files (matches the
  // pattern + architecture-debt note in errorRouter.test.ts).
  afterEach(() => setHostBridgeForTesting(null));

  it("transitions phase creating -> idle AND closes the create modal on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    // Simulate the user opening the create modal and submitting.
    admin.openCreate();
    ui.openCreate();
    admin.beginSubmit();
    expect(admin.phase).toBe("creating");
    expect(ui.createModalOpen).toBe(true);

    // Host signals success by re-announcing the (now changed) collection list.
    emit("database.collectionsChanged", {
      tables: ["users", "orders"],
    } as CollectionsChangedPayload);

    expect(admin.phase).toBe("idle");
    expect(admin.form.name).toBe("");
    expect(ui.createModalOpen).toBe(false);
  });

  it("transitions phase deleting -> idle AND closes the delete modal on collectionsChanged", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    admin.requestDelete("orders");
    ui.openDelete("orders");
    expect(admin.phase).toBe("deleting");
    expect(ui.deleteModalOpen).toBe(true);

    emit("database.collectionsChanged", {
      tables: ["users"],
    } as CollectionsChangedPayload);

    expect(admin.phase).toBe("idle");
    expect(admin.pendingDelete).toBeNull();
    expect(ui.deleteModalOpen).toBe(false);
  });

  it("does NOT close the modal when collectionsChanged fires while idle (initial load)", () => {
    // Regression guard: collectionsChanged fires for ANY collection change,
    // including the initial load. Only an in-flight create/delete should be
    // resolved; an idle store must not be touched.
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    // Idle + create modal happens to be open (e.g. user typing in the form),
    // but no create is in flight. The collection list refresh should NOT close
    // the modal — only a phase transition from creating/deleting does.
    admin.openCreate(); // phase = creating, but...
    admin.close(); // ...back to idle (no submit pending)
    ui.openCreate();
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(true);

    emit("database.collectionsChanged", {
      tables: ["users"],
    } as CollectionsChangedPayload);

    // Phase stays idle, modal stays open: no false close.
    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(true);
  });

  it("resolves a pending create on database.opened as well", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const ui = useUiStore();
    const svc = useTableAdminService();
    svc.init();

    admin.openCreate();
    ui.openCreate();
    admin.beginSubmit();

    emit("database.opened", {
      tables: ["users", "orders"],
      views: [],
    } as DatabaseOpenedPayload);

    expect(admin.phase).toBe("idle");
    expect(ui.createModalOpen).toBe(false);
  });

  it("still updates the collections list on collectionsChanged regardless of phase", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const svc = useTableAdminService();
    svc.init();

    emit("database.collectionsChanged", {
      tables: ["users", "orders", "items"],
      capabilityHashes: { users: "abc" },
    } as CollectionsChangedPayload);

    expect(admin.collections).toHaveLength(3);
    expect(admin.collections[0]?.collection).toBe("users");
    expect(admin.collections[0]?.metadata?.capabilityHash).toBe("abc");
  });
});
