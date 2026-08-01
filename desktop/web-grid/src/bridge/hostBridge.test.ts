import { describe, expect, it, vi, beforeEach, afterEach, type Mock } from "vitest";
import { createPinia, setActivePinia } from "pinia";

import { createHostBridge } from "./hostBridge";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type {
  BridgeMessage,
  DatabaseOpenedPayload,
  TablePage,
} from "@/contracts";

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
    setActivePinia(createPinia());
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

  it("round-trips the closed application preferences RPC types", async () => {
    const ids = ["preferences-get", "preferences-update"];
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => ids.shift()!,
    });
    bridge.start();

    const get = bridge.request("appPreferences.get", {});
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "appPreferences.get",
      requestId: "preferences-get",
      payload: {},
    });
    webview.emit({
      type: "appPreferences.get",
      requestId: "preferences-get",
      payload: {
        minimizeToTrayOnClose: false,
        startWithWindows: false,
        updateProxy: "direct",
        customUpdateProxyUrl: "",
      },
    });
    await expect(get).resolves.toEqual({
      minimizeToTrayOnClose: false,
      startWithWindows: false,
      updateProxy: "direct",
      customUpdateProxyUrl: "",
    });

    const update = bridge.request("appPreferences.update", {
      minimizeToTrayOnClose: true,
    });
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "appPreferences.update",
      requestId: "preferences-update",
      payload: { minimizeToTrayOnClose: true },
    });
    webview.emit({
      type: "appPreferences.update",
      requestId: "preferences-update",
      payload: {
        minimizeToTrayOnClose: true,
        startWithWindows: false,
        updateProxy: "direct",
        customUpdateProxyUrl: "",
      },
    });
    await expect(update).resolves.toEqual({
      minimizeToTrayOnClose: true,
      startWithWindows: false,
      updateProxy: "direct",
      customUpdateProxyUrl: "",
    });

    bridge.stop();
  });

  it("correlates update RPCs and keeps installation beyond the ordinary timeout", async () => {
    vi.useFakeTimers();
    const ids = ["update-check", "update-install"];
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => ids.shift()!,
    });
    bridge.start();

    const check = bridge.request("update.check", {});
    webview.emit({
      type: "update.check",
      requestId: "update-check",
      payload: {
        currentVersion: "1.0.0",
        latestVersion: "1.1.0",
        updateAvailable: true,
        canInstall: true,
        installUnavailableReason: null,
        downloadBytes: 1024,
        releaseUrl: "https://github.com/FelixJI/VibeTable/releases/tag/v1.1.0",
        notesTruncated: false,
        releases: [],
      },
    });
    await expect(check).resolves.toMatchObject({ latestVersion: "1.1.0" });

    const install = bridge.request("update.install", {});
    let settled = false;
    void install.then(
      () => { settled = true; },
      () => { settled = true; },
    );
    await vi.advanceTimersByTimeAsync(1000);
    expect(settled).toBe(false);
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "update.install",
      requestId: "update-install",
      payload: {},
    });
    webview.emit({
      type: "update.install",
      requestId: "update-install",
      payload: { status: "restarting" },
    });
    await expect(install).resolves.toEqual({ status: "restarting" });

    bridge.stop();
  });

  it("allows only the closed workspace v2 request type and correlates its reply", async () => {
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "workspace-v2-1",
    });
    bridge.start();
    const wire = {
      scope: "global" as const,
      operationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      sequence: 1,
    };
    const pending = bridge.request("workspace.v2.request", {
      method: "workspace.list",
      params: {},
      wire,
    });
    expect(webview.postMessage).toHaveBeenCalledWith({
      type: "workspace.v2.request",
      requestId: "workspace-v2-1",
      wire,
      payload: {
        method: "workspace.list",
        params: {},
        wire,
      },
    });

    const reply = {
      method: "workspace.list",
      wire,
      ok: true,
      result: { workspaces: [] },
      error: null,
    };
    webview.emit({
      type: "workspace.v2.response",
      requestId: "workspace-v2-1",
      payload: reply,
    });
    await expect(pending).resolves.toEqual(reply);
    bridge.stop();
  });

  it("keeps a native-picker workspace request beyond the ordinary RPC timeout", async () => {
    vi.useFakeTimers();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 30_000,
      generateRequestId: () => "workspace-picker-1",
    });
    bridge.start();
    const wire = {
      scope: "global" as const,
      operationId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      sequence: 1,
    };

    const pending = bridge.request("workspace.v2.request", {
      method: "workspace.create",
      params: {
        displayName: "自选位置",
        locationPolicy: "other",
        selectedRootGrant: "host-picker://workspace-root",
        storageMode: "direct",
        encryptionMode: "convenient",
        userMarkedSync: false,
      },
      wire,
    });
    let settled = false;
    void pending.then(
      () => { settled = true; },
      () => { settled = true; },
    );

    await vi.advanceTimersByTimeAsync(30_000);
    expect(settled).toBe(false);

    const reply = {
      method: "workspace.create",
      wire,
      ok: true,
      result: { workspaceId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", status: "created" },
      error: null,
    };
    webview.emit({
      type: "workspace.v2.response",
      requestId: "workspace-picker-1",
      payload: reply,
    });
    await expect(pending).resolves.toEqual(reply);
    bridge.stop();
  });

  it("adds a strictly scoped workspace envelope to legacy product requests", async () => {
    const session = useWorkspaceSessionStore();
    session.configureCapabilities(["workspace.session.v2"]);
    session.setWorkspaces([{
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      displayName: "E2E",
      selectedRoot: "D:\\E2E",
      activityRoot: null,
      storageKind: "fixed",
      coordinationStrength: "strong",
      lastOpenedAt: null,
      lastKnownHealth: "healthy",
      lastSnapshotAt: null,
      lastSyncAt: null,
      pendingSync: false,
    }]);
    session.applySession({
      contractVersion: "2.0",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      sessionEpoch: 7,
      state: "openedWritable",
      openMode: "writable",
      writable: true,
      provisional: false,
      phase: "idle",
      errorCode: null,
    });
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "scoped-legacy-1",
    });
    bridge.start();
    const pending = bridge.request("table.pageRequested", {
      table: "contracts",
      offset: 0,
      limit: 50,
    });
    const posted = webview.postMessage.mock.calls[0]![0] as BridgeMessage;
    expect(posted).toMatchObject({
      type: "table.pageRequested",
      requestId: "scoped-legacy-1",
      scope: {
        scope: "workspace",
        workspaceId: "11111111-1111-4111-8111-111111111111",
        sessionEpoch: 7,
      },
    });
    expect((posted.scope as { sequence: number }).sequence).toBeGreaterThan(0);

    webview.emit({
      type: "table.pageLoaded",
      requestId: "scoped-legacy-1",
      payload: {
        table: "contracts",
        columns: [],
        rows: [],
        offset: 0,
        limit: 50,
        totalRows: 0,
        mode: "client",
      },
    });
    await expect(pending).resolves.toMatchObject({ table: "contracts" });
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

  it("correlates the fixed daily quote RPC by its same response type", async () => {
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "quote-req-1",
    });
    bridge.start();

    const pending = bridge.request("dailyQuote.fetch", {
      provider: "quotable",
      style: "philosophy",
      locale: "en-US",
    });
    expect(webview.postMessage).toHaveBeenCalledWith({
      type: "dailyQuote.fetch",
      requestId: "quote-req-1",
      payload: {
        provider: "quotable",
        style: "philosophy",
        locale: "en-US",
      },
    });

    webview.emit({
      type: "dailyQuote.fetch",
      requestId: "quote-req-1",
      payload: {
        text: "Know thyself.",
        attribution: "Socrates",
        url: "https://quotable.io/quotes/socrates",
      },
    });
    await expect(pending).resolves.toMatchObject({ text: "Know thyself." });
    bridge.stop();
  });

  it("keeps a request pending when a correlated response has the wrong type", async () => {
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-mismatch",
      onDiagnostic,
    });
    bridge.start();

    const pending = bridge.request("dashboard.listRequested", {});
    let settled = false;
    void pending.finally(() => {
      settled = true;
    });

    webview.emit({
      type: "dashboard.queryLoaded",
      requestId: "req-mismatch",
      payload: { rows: [{ forged: true }] },
    });
    await Promise.resolve();

    expect(settled).toBe(false);
    expect(onDiagnostic).toHaveBeenCalledWith(expect.objectContaining({
      kind: "mismatched-response",
      type: "dashboard.queryLoaded",
      reason: expect.stringContaining("expected: dashboard.listLoaded"),
    }));

    webview.emit({
      type: "dashboard.listLoaded",
      requestId: "req-mismatch",
      payload: { dashboards: [] },
    });
    await expect(pending).resolves.toEqual({ dashboards: [] });
    bridge.stop();
  });

  it("still rejects operation.failed after an earlier mismatched response", async () => {
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-mismatch-fail",
    });
    bridge.start();

    const pending = bridge.request("history.queryRequested", {
      collection: "orders",
      scope: "table",
      limit: 50,
      offset: 0,
    });
    webview.emit({
      type: "history.restoreApplied",
      requestId: "req-mismatch-fail",
      payload: { forged: true },
    });
    webview.emit({
      type: "operation.failed",
      requestId: "req-mismatch-fail",
      payload: { message: "history unavailable", code: "HISTORY_FAILED" },
    });

    await expect(pending).rejects.toMatchObject({
      message: "history unavailable",
      code: "HISTORY_FAILED",
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
    const taskHandler = vi.fn();
    bridge.on("database.opened", openedHandler);
    bridge.on("data.changed", () => undefined);
    bridge.on("task.changed", taskHandler);
    bridge.start();

    const opened: DatabaseOpenedPayload = { tables: ["contracts"], views: [] };
    webview.emit({ type: "database.opened", payload: opened });

    expect(openedHandler).toHaveBeenCalledTimes(1);
    expect(openedHandler).toHaveBeenCalledWith(opened);
    const task = {
      contractVersion: "1.0",
      topic: "task.changed",
      eventId: "evt-1",
      sequence: 1,
      occurredAt: "2026-07-24T08:31:00Z",
      taskId: "job-1",
      taskType: "formulaBackfill",
      state: "running",
      progress: 0.5,
      cursor: "row:5000",
      error: null,
    } as const;
    webview.emit({ type: "task.changed", payload: task });
    expect(taskHandler).toHaveBeenCalledWith(task);

    bridge.stop();
  });

  it("parses inbound messages delivered as a JSON string (PostWebMessageAsString contract)", () => {
    // The C# host posts via CoreWebView2.PostWebMessageAsString(json), so
    // window.chrome.webview delivers event.data as a JSON *string*, not a
    // parsed object. The bridge must parse it before dispatching.
    const onDiagnostic = vi.fn();
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "req-s",
      onDiagnostic,
    });

    const openedHandler = vi.fn();
    bridge.on("database.opened", openedHandler);
    bridge.start();

    const opened: DatabaseOpenedPayload = { tables: ["contracts", "users"], views: [] };
    // Mirrors the real wire shape: a single JSON.stringify of the envelope.
    webview.emit(JSON.stringify({ type: "database.opened", payload: opened }));

    expect(onDiagnostic).not.toHaveBeenCalled();
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

  it("reports a rejected synthetic File boundary as unavailable", () => {
    const postMessageWithAdditionalObjects = vi.fn(() => {
      throw new DOMException("File is not backed by disk");
    });
    Object.assign(webview, { postMessageWithAdditionalObjects });
    const bridge = createHostBridge({ webview, timeoutMs: 1000 });
    const file = new File(["content"], "synthetic.txt", { type: "text/plain" });

    const posted = bridge.notifyWithAdditionalObjects(
      "file.uploadRequested",
      {
        tableId: "tbl_files",
        recordId: "row-1",
        fieldId: "tbl_files.attachments",
        schemaRevision: "schema_0001",
        expectedDigest: `sha256:${"a".repeat(64)}`,
      },
      [file],
    );

    expect(posted).toBe(false);
    expect(postMessageWithAdditionalObjects).toHaveBeenCalledTimes(1);
  });

  it("correlates a native attachment request before reporting completion", async () => {
    const postMessageWithAdditionalObjects = vi.fn();
    Object.assign(webview, { postMessageWithAdditionalObjects });
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => "attachment-req-1",
    });
    bridge.start();
    const file = new File(["content"], "receipt.txt", { type: "text/plain" });

    const pending = bridge.requestWithAdditionalObjects(
      "file.uploadRequested",
      {
        tableId: "tbl_files",
        recordId: "row-1",
        fieldId: "tbl_files.attachments",
        schemaRevision: "schema_0001",
        expectedDigest: `sha256:${"a".repeat(64)}`,
      },
      [file],
    );

    expect(pending).not.toBeNull();
    expect(postMessageWithAdditionalObjects).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "file.uploadRequested",
        requestId: "attachment-req-1",
      }),
      [file],
    );
    webview.emit({
      type: "file.uploadRequested",
      requestId: "attachment-req-1",
      payload: {
        contractVersion: "1.0",
        status: "applied",
        changeSetId: "change-1",
      },
    });
    await expect(pending).resolves.toMatchObject({ status: "applied" });
    bridge.stop();
  });

  it("whitelists the revision history query and page notification contract", () => {
    const bridge = createHostBridge({ webview, timeoutMs: 1000 });
    const loaded = vi.fn();
    bridge.on("history.pageLoaded", loaded);
    bridge.start();

    bridge.notify("history.queryRequested", {
      collection: "orders",
      scope: "cell",
      itemId: "42",
      field: "status",
      search: "done",
      limit: 50,
      offset: 0,
    });
    expect(webview.postMessage).toHaveBeenCalledWith(expect.objectContaining({
      type: "history.queryRequested",
      payload: expect.objectContaining({ scope: "cell", itemId: "42", field: "status" }),
    }));

    webview.emit({
      type: "history.pageLoaded",
      payload: {
        collection: "orders",
        scope: "cell",
        itemId: "42",
        field: "status",
        changeSets: [],
        total: 0,
        hasMore: false,
        capabilityHash: "cap",
        schemaRevision: "schema",
      },
    });
    expect(loaded).toHaveBeenCalledTimes(1);
    bridge.stop();
  });

  it("correlates native dashboard requests and posts dashboard cancellation", async () => {
    let counter = 0;
    const bridge = createHostBridge({
      webview,
      timeoutMs: 1000,
      generateRequestId: () => `dashboard-${++counter}`,
    });
    bridge.start();

    const list = bridge.request("dashboard.listRequested", {});
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "dashboard.listRequested",
      requestId: "dashboard-1",
      payload: {},
    });
    webview.emit({
      type: "dashboard.listLoaded",
      requestId: "dashboard-1",
      payload: { dashboards: [] },
    });
    await expect(list).resolves.toEqual({ dashboards: [] });

    const query = bridge.requestWithHandle("dashboard.queryRequested", {
      panelType: "metric",
      query: {
        kind: "aggregate",
        collection: "orders",
        measures: [{ key: "total", op: "sum", field: "amount" }],
      },
    });
    expect(query.requestId).toBe("dashboard-2");
    webview.emit({
      type: "dashboard.queryLoaded",
      requestId: "dashboard-2",
      payload: { rows: [{ total: 42 }], truncated: false, maxPoints: 100_000 },
    });
    await expect(query.promise).resolves.toEqual({
      rows: [{ total: 42 }], truncated: false, maxPoints: 100_000,
    });

    bridge.notify("dashboard.cancelRequested", { targetRequestId: "dashboard-2" });
    expect(webview.postMessage).toHaveBeenLastCalledWith({
      type: "dashboard.cancelRequested",
      payload: { targetRequestId: "dashboard-2" },
    });
    bridge.stop();
  });

  it("blocks retired relation/Lookup schema writers while keeping read-only lookup access", () => {
    const bridge = createHostBridge({ webview, timeoutMs: 1000 });
    bridge.start();

    expect(() => (bridge.notify as (type: string, payload: unknown) => void)("table_admin.previewRelationChange", {
      collection: "orders",
      action: "delete",
      relationId: "orders.contract",
      expectedSchemaRevision: "schema-1",
    })).toThrow(/non-whitelisted/);
    expect(() => (bridge.notify as (type: string, payload: unknown) => void)("table_admin.applyRelationChange", {
      planId: "plan-1",
      operationId: "op-1",
      expectedSchemaRevision: "schema-1",
      cascadeLookupIds: [],
    })).toThrow(/non-whitelisted/);
    expect(() => (bridge.notify as (type: string, payload: unknown) => void)("lookup.create", {
      definition: {},
      requestId: "old-lookup-writer",
    })).toThrow(/non-whitelisted/);
    bridge.notify("lookup.list", { collection: "orders" });

    expect(webview.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "lookup.list" }));
    bridge.stop();
  });
});
