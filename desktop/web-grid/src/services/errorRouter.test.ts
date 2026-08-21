import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useErrorRouter } from "./errorRouter";

// Build a bridge with a controllable inbound shim.
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
  // `HostBridgeOptions.webview` is the WebViewLike object itself (NOT a
  // factory); hostBridge.ts wraps it in a `() => options.webview` closure.
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

describe("errorRouter", () => {
  beforeEach(() => setActivePinia(createPinia()));

  // CRITICAL: `useHostBridge` is a module singleton. Each test injects a fake
  // bridge via `setHostBridgeForTesting`. Without resetting to `null` here the
  // fake would leak into OTHER test files, potentially breaking them. This is
  // the architecture-debt note from the task brief.
  afterEach(() => setHostBridgeForTesting(null));

  it("leaves create failures to the correlated tableAdmin request", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const table = useTableStore();
    admin.openCreate();
    admin.beginSubmit(); // phase = submitting
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "bad name", code: "X" });
    expect(admin.phase).toBe("submitting");
    expect(admin.error).toBeNull();
    expect(table.error).toBe("bad name");
  });

  it("does not infer tableAdmin ownership from an uncorrelated failure", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const table = useTableStore();
    admin.openCreate();
    admin.beginSubmit();
    const router = useErrorRouter();
    router.init();

    emit("operation.failed", {
      message: "Workspace operation failed.",
      code: "WORKSPACE_ERROR",
    });

    expect(admin.phase).toBe("submitting");
    expect(admin.error).toBeNull();
    expect(table.error).toBe("Workspace operation failed.");
  });

  it("does not claim unrelated failures while the create form is only being edited", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const table = useTableStore();
    admin.openCreate();
    const router = useErrorRouter();
    router.init();

    emit("operation.failed", { message: "table load failed", code: "X" });

    expect(admin.phase).toBe("creating");
    expect(admin.error).toBeNull();
    expect(table.error).toBe("table load failed");
  });

  it("leaves delete failures to the correlated tableAdmin request", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const table = useTableStore();
    admin.requestDelete("users");
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "cannot delete", code: "X" });
    expect(admin.phase).toBe("deleting");
    expect(admin.error).toBeNull();
    expect(table.error).toBe("cannot delete");
  });

  it("routes to pasteStore when applying", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const paste = usePasteStore();
    paste.setPlan({ rows: 1, columns: 1, cells: [] } as never);
    paste.beginApply();
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "paste failed", code: "X" });
    expect(paste.phase).toBe("error");
    expect(paste.error).toBe("paste failed");
  });

  it("falls back to tableStore when no admin/paste active", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "load failed", code: "X" });
    expect(table.error).toBe("load failed");
  });

  it("leaves query.cursor_stale to the cursor reopen controller", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    const router = useErrorRouter();
    router.init();

    emit("operation.failed", {
      message: "cursor changed",
      code: "query.cursor_stale",
      operation: "query.cursor",
    });

    expect(table.error).toBeNull();
  });
});
