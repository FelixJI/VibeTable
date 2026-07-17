import { describe, expect, it, vi, beforeEach, afterEach, type Mock } from "vitest";

import { createHostBridge } from "./hostBridge";
import type {
  BridgeMessage,
  DatabaseOpenedPayload,
  TablePage,
} from "./contracts";

/**
 * Minimal shape of `window.chrome.webview` used by HostBridge. Only the
 * members the bridge touches are surfaced; the real WebView2 object exposes
 * more, but the bridge is intentionally narrow.
 */
interface WebViewShim {
  postMessage: (msg: unknown) => void;
  addEventListener: (
    type: string,
    listener: (ev: { readonly data: unknown }) => void,
  ) => void;
  removeEventListener: (
    type: string,
    listener: (ev: { readonly data: unknown }) => void,
  ) => void;
}

/** Builds a fresh webview shim with a `postMessage` spy and a real listener list. */
function makeWebview(): WebViewShim & {
  postMessage: Mock;
  listeners: Array<(ev: { readonly data: unknown }) => void>;
  emit: (data: unknown) => void;
} {
  const listeners: Array<(ev: { readonly data: unknown }) => void> = [];
  return {
    listeners,
    postMessage: vi.fn((_msg: unknown) => undefined) as Mock,
    addEventListener: (_type, listener) => {
      listeners.push(listener);
    },
    removeEventListener: (_type, listener) => {
      const i = listeners.indexOf(listener);
      if (i >= 0) listeners.splice(i, 1);
    },
    emit: (data: unknown) => {
      for (const l of listeners) l({ data });
    },
  };
}

describe("HostBridge", () => {
  let webview: ReturnType<typeof makeWebview>;

  beforeEach(() => {
    webview = makeWebview();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("posts a request envelope with a unique requestId and resolves on the matching response", async () => {
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      // Deterministic IDs so the test can assert and synthesize the reply.
      generateRequestId: () => "req-1",
    });
    bridge.start();

    const payload = { table: "contracts", offset: 0, limit: 50 };
    const pending = bridge.request("table.pageRequested", payload);

    // Outbound envelope shape.
    expect(webview.postMessage).toHaveBeenCalledTimes(1);
    const posted = webview.postMessage.mock.calls[0]![0] as BridgeMessage;
    expect(posted.type).toBe("table.pageRequested");
    expect(posted.requestId).toBe("req-1");
    expect(posted.payload).toEqual(payload);

    // Resolves ONLY on the matching requestId.
    const page: TablePage = {
      table: "contracts",
      columns: [
        { name: "id", title: "Id", dataType: "integer", editable: false, nullable: false },
      ],
      rows: [{ rowKey: 1, id: 1 }],
      offset: 0,
      limit: 50,
      totalRows: 1,
      mode: "client",
    };
    webview.emit({ type: "table.pageLoaded", requestId: "req-1", payload: page });

    await expect(pending).resolves.toEqual(page);

    bridge.stop();
  });

  it("uses a fresh requestId per request and does NOT cross-resolve a sibling", async () => {
    let counter = 0;
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => `req-${++counter}`,
    });
    bridge.start();

    const a = bridge.request("table.pageRequested", {
      table: "a",
      offset: 0,
      limit: 10,
    });
    const b = bridge.request("table.pageRequested", {
      table: "b",
      offset: 0,
      limit: 10,
    });

    const postedA = webview.postMessage.mock.calls[0]![0] as BridgeMessage;
    const postedB = webview.postMessage.mock.calls[1]![0] as BridgeMessage;
    expect(postedA.requestId).toBe("req-1");
    expect(postedB.requestId).toBe("req-2");
    expect(postedA.requestId).not.toBe(postedB.requestId);

    // Reply for B only. A must remain pending. Attach a no-op catch so that
    // stop()'s rejection of A later doesn't surface as an unhandled rejection.
    let aResolved = false;
    a.then(
      () => {
        aResolved = true;
      },
      () => {
        /* expected: A is rejected by stop() at end of test */
      },
    );

    webview.emit({
      type: "table.pageLoaded",
      requestId: "req-2",
      payload: { table: "b", columns: [], rows: [], offset: 0, limit: 10, totalRows: 0, mode: "client" },
    });

    await expect(b).resolves.toBeTruthy();
    // Yield a microtask; A should still be pending.
    await Promise.resolve();
    expect(aResolved).toBe(false);

    bridge.stop();
  });

  it("rejects on operation.failed with the matching requestId", async () => {
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-fail",
    });
    bridge.start();

    const pending = bridge.request("table.pageRequested", {
      table: "x",
      offset: 0,
      limit: 10,
    });

    webview.emit({
      type: "operation.failed",
      requestId: "req-fail",
      payload: { message: "no such table", code: "ENOENT" },
    });

    await expect(pending).rejects.toMatchObject({
      message: "no such table",
      code: "ENOENT",
    });

    bridge.stop();
  });

  it("fans out operation.failed with null requestId (PostReply production shape)", () => {
    // Regression: the C# host serializes a null RequestId as `"requestId":null`
    // when a notify-based request (e.g. table-admin create/delete, which use
    // bridge.notify with no requestId) fails. PostReply emits an envelope of
    // the shape `{ type, requestId, payload }` with requestId=null. The bridge
    // must treat null as absent (not malformed) so the failure reaches
    // on("operation.failed") handlers instead of being silently dropped.
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "should-not-match",
      onDiagnostic,
    });

    const failedHandler = vi.fn();
    bridge.on("operation.failed", failedHandler);
    bridge.start();

    // No pending request() was made for this id; ensure the bridge does not
    // falsely resolve an unrelated pending entry.
    bridge
      .request("table.pageRequested", { table: "x", offset: 0, limit: 10 })
      .catch(() => undefined); // swallow eventual stop() rejection

    webview.emit({
      type: "operation.failed",
      requestId: null,
      payload: { message: "boom" },
    });

    expect(failedHandler).toHaveBeenCalledTimes(1);
    expect(failedHandler).toHaveBeenCalledWith({ message: "boom" });
    // Must NOT have been dropped as malformed.
    expect(onDiagnostic).not.toHaveBeenCalled();

    bridge.stop();
  });

  it("times out after the configured duration when no response arrives", async () => {
    vi.useFakeTimers();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 150,
      generateRequestId: () => "req-timeout",
    });
    bridge.start();

    const pending = bridge.request("table.pageRequested", {
      table: "t",
      offset: 0,
      limit: 10,
    });

    // Pre-timeout: still pending.
    vi.advanceTimersByTime(149);
    let rejectedEarly = false;
    pending.catch(() => {
      rejectedEarly = true;
    });
    await Promise.resolve();
    expect(rejectedEarly).toBe(false);

    vi.advanceTimersByTime(1);

    await expect(pending).rejects.toThrow(/timeout/i);

    bridge.stop();
  });

  it("ignores unknown inbound message types and reports them via onDiagnostic", () => {
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-x",
      onDiagnostic,
    });
    bridge.start();

    // Unknown type, well-formed envelope.
    webview.emit({ type: "mystery.event", requestId: "req-x", payload: {} });

    // Diagnostic fired exactly once for the unknown type.
    expect(onDiagnostic).toHaveBeenCalledTimes(1);
    const arg = onDiagnostic.mock.calls[0]![0];
    expect(arg).toMatchObject({ kind: "unknown-type", type: "mystery.event" });

    bridge.stop();
  });

  it("ignores malformed inbound messages (missing/invalid type) and reports them", () => {
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-y",
      onDiagnostic,
    });
    bridge.start();

    // Missing type entirely.
    webview.emit({ requestId: "req-y", payload: {} });
    // Type of wrong shape.
    webview.emit({ type: 42, payload: {} });
    // Not an object at all.
    webview.emit("garbage");

    expect(onDiagnostic).toHaveBeenCalledTimes(3);
    for (const call of onDiagnostic.mock.calls) {
      expect(call[0].kind).toBe("malformed");
    }

    bridge.stop();
  });

  it("delivers whitelisted inbound events to typed handlers", () => {
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-h",
      onDiagnostic,
    });

    const openedHandler = vi.fn();
    bridge.on("database.opened", openedHandler);
    bridge.on("directus.changed", () => undefined);
    bridge.start();

    const opened: DatabaseOpenedPayload = { tables: ["contracts"], views: [] };
    webview.emit({ type: "database.opened", payload: opened });

    expect(openedHandler).toHaveBeenCalledTimes(1);
    expect(openedHandler).toHaveBeenCalledWith(opened);

    bridge.stop();
  });

  it("notify posts a fire-and-forget envelope with no requestId (e.g. app.ready)", () => {
    const bridge = createHostBridge({ webview, timeoutMs: 1000 });
    bridge.start();

    bridge.notify("app.ready", {});

    expect(webview.postMessage).toHaveBeenCalledTimes(1);
    const posted = webview.postMessage.mock.calls[0]![0] as BridgeMessage;
    expect(posted.type).toBe("app.ready");
    // Notify is fire-and-forget; no requestId is attached.
    expect(posted.requestId).toBeUndefined();
    expect(posted.payload).toEqual({});

    bridge.stop();
  });
});
