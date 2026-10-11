import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";

import { createHostBridge, type WebViewLike } from "@/bridge/hostBridge";
import {
  exportDashboardCsv,
  exportDashboardElementPng,
  printDashboard,
} from "./dashboardExportService";
import type { DashboardPanelData } from "@/stores/dashboardStore";

const toBlob = vi.fn();

vi.mock("html-to-image", () => ({ toBlob }));

interface PostedMessage {
  readonly type: string;
  readonly requestId?: string;
  readonly payload?: unknown;
}

function readyData(overrides: Partial<DashboardPanelData> = {}): DashboardPanelData {
  return {
    state: "ready",
    rows: [
      { name: "Ada, Inc.", note: 'said "hello"', amount: 2 },
      { name: null, extra: { active: true } },
    ],
    truncated: false,
    maxPoints: 100,
    updatedAt: 1,
    error: null,
    ...overrides,
  };
}

describe("dashboardExportService native output", () => {
  const createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:https://app.vibetable.local/abc");
  const revokeObjectURL = vi.fn();
  const clicks: string[] = [];
  let posted: PostedMessage[] = [];
  let listener: ((event: { readonly data: unknown }) => void) | null = null;
  let sequence = 0;

  function reply(message: unknown): void {
    listener?.({ data: message });
  }

  beforeEach(() => {
    setActivePinia(createPinia());
    vi.useFakeTimers();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
    posted = [];
    clicks.length = 0;
    sequence = 0;
    listener = null;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      expect(this.download).not.toBe("");
      clicks.push(this.href);
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    toBlob.mockReset();
  });

  function makeBridge(): ReturnType<typeof createHostBridge> {
    const webview: WebViewLike = {
      postMessage: (message: unknown) => {
        posted.push(message as PostedMessage);
      },
      addEventListener: (_type, handler) => {
        listener = handler;
      },
      removeEventListener: () => {
        listener = null;
      },
    };
    const bridge = createHostBridge({
      webview,
      generateRequestId: () => `r${++sequence}`,
      timeoutMs: 1_000,
    });
    bridge.start();
    return bridge;
  }

  async function grantTarget(payload: Record<string, unknown> = {}): Promise<void> {
    const request = posted.at(-1)!;
    reply({
      type: "dashboard.exportTargetGranted",
      requestId: request.requestId,
      payload: { token: "download-1", displayName: "sales.csv", ...payload },
    });
    // Flush the request microtasks without draining the settle timeout.
    await vi.advanceTimersByTimeAsync(0);
  }

  it.each(["idle", "queued", "loading", "stale", "failed"] as const)(
    "rejects CSV export while the panel is %s",
    async (state) => {
      const bridge = makeBridge();
      await expect(
        exportDashboardCsv("sales", { ...readyData(), state }, bridge),
      ).rejects.toMatchObject({ code: "PANEL_NOT_READY" });
      expect(posted).toEqual([]);
      expect(clicks).toEqual([]);
      expect(revokeObjectURL).not.toHaveBeenCalled();
      bridge.stop();
    },
  );

  it("exports ready CSV through a registered single blob download", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv("  Sales/Q3:*?  ", readyData(), bridge);
    await vi.advanceTimersByTimeAsync(0);
    expect(posted.map((item) => item.type)).toEqual(["dashboard.exportTargetRequested"]);
    expect(posted[0]!.payload).toMatchObject({
      fileName: "Sales_Q3___",
      format: "csv",
      blobUrl: "blob:https://app.vibetable.local/abc",
      byteLength: expect.any(Number),
    });
    await grantTarget();
    expect(clicks).toEqual(["blob:https://app.vibetable.local/abc"]);
    reply({
      type: "dashboard.exportSettled",
      payload: { token: "download-1", outcome: "saved" },
    });
    await expect(promise).resolves.toEqual({
      outcome: "saved",
      fileName: "sales.csv",
      truncated: false,
      maxPoints: 100,
    });
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:https://app.vibetable.local/abc");
    bridge.stop();
  });

  it("keeps the blob alive until the host settles the download", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv("sales", readyData(), bridge);
    await vi.advanceTimersByTimeAsync(0);
    await grantTarget();
    expect(clicks).toHaveLength(1);
    expect(revokeObjectURL).not.toHaveBeenCalled();
    reply({
      type: "dashboard.exportSettled",
      payload: { token: "other", outcome: "saved" },
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(revokeObjectURL).not.toHaveBeenCalled();
    reply({
      type: "dashboard.exportSettled",
      payload: { token: "download-1", outcome: "failed", reason: "disk full" },
    });
    await expect(promise).rejects.toMatchObject({ code: "SAVE_FAILED" });
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    bridge.stop();
  });

  it("reports host dialog cancellation without downloading", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv("sales", readyData(), bridge);
    await vi.advanceTimersByTimeAsync(0);
    const request = posted.at(-1)!;
    reply({
      type: "operation.failed",
      requestId: request.requestId,
      payload: { message: "Export cancelled.", code: "CANCELLED" },
    });
    await expect(promise).resolves.toEqual({
      outcome: "cancelled",
      fileName: "sales",
      truncated: false,
      maxPoints: 100,
    });
    expect(clicks).toEqual([]);
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    bridge.stop();
  });

  it("propagates host rejection of the export target", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv("sales", readyData(), bridge);
    await vi.advanceTimersByTimeAsync(0);
    const request = posted.at(-1)!;
    reply({
      type: "operation.failed",
      requestId: request.requestId,
      payload: { message: "no workspace", code: "BACKEND_UNAVAILABLE" },
    });
    await expect(promise).rejects.toMatchObject({ code: "BACKEND_UNAVAILABLE" });
    expect(clicks).toEqual([]);
    bridge.stop();
  });

  it("uses the host deadline and reports its terminal failure", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv("sales", readyData(), bridge);
    await vi.advanceTimersByTimeAsync(0);
    await grantTarget();
    await vi.advanceTimersByTimeAsync(120_001);
    expect(revokeObjectURL).not.toHaveBeenCalled();
    reply({ type: "dashboard.exportSettled",
      payload: { token: "download-1", outcome: "failed", reason: "SAVE_TIMEOUT" } });
    await expect(promise).rejects.toMatchObject({ code: "SAVE_TIMEOUT" });
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    bridge.stop();
  });

  it("exports truncated-ready CSV data with a visible truncation flag", async () => {
    const bridge = makeBridge();
    const promise = exportDashboardCsv(
      "sales",
      readyData({ truncated: true, maxPoints: 1_000 }),
      bridge,
    );
    await vi.advanceTimersByTimeAsync(0);
    await grantTarget();
    reply({
      type: "dashboard.exportSettled",
      payload: { token: "download-1", outcome: "saved" },
    });
    await expect(promise).resolves.toMatchObject({
      outcome: "saved",
      truncated: true,
      maxPoints: 1_000,
    });
    bridge.stop();
  });

  it("renders a bounded PNG with edge and total-pixel budgets", async () => {
    const element = document.createElement("section");
    Object.defineProperties(element, {
      scrollWidth: { value: 20_000 },
      scrollHeight: { value: 8_000 },
    });
    const blob = new Blob(["png"], { type: "image/png" });
    toBlob.mockResolvedValueOnce(blob);

    const bridge = makeBridge();
    const promise = exportDashboardElementPng(element, "board", bridge);
    await vi.advanceTimersByTimeAsync(0);
    // 64,000,000 / (20,000 * 8,000) = 0.4 < min(2, 0.8, 2) so the pixel
    // budget must dominate without the former 0.5 floor breaking the cap.
    expect(toBlob).toHaveBeenCalledWith(element, expect.objectContaining({
      width: 20_000,
      height: 8_000,
      pixelRatio: Math.sqrt(64_000_000 / (20_000 * 8_000)),
    }));
    await grantTarget({ displayName: "board.png" });
    reply({
      type: "dashboard.exportSettled",
      payload: { token: "download-1", outcome: "saved" },
    });
    await expect(promise).resolves.toEqual({ outcome: "saved", fileName: "board.png" });
    bridge.stop();
  });

  it("rejects a delayed PNG from a retired workspace before requesting a new target", async () => {
    const session = useWorkspaceSessionStore();
    session.activeWorkspaceId = "workspace-a";
    session.sessionEpoch = 1;
    session.sessionState = "openedWritable";
    session.setWorkspaces([{
      contractVersion: "2.0", workspaceId: "workspace-a", displayName: "A",
      selectedRoot: "C:\\fixture-a", activityRoot: null, storageKind: "fixed",
      coordinationStrength: "strong", lastOpenedAt: null, lastKnownHealth: "healthy",
      lastSnapshotAt: null, lastSyncAt: null, pendingSync: false,
    }]);
    session.configureCapabilities(["workspace.session.v2"]);
    let finish!: (blob: Blob) => void;
    toBlob.mockReturnValueOnce(new Promise<Blob>((resolve) => { finish = resolve; }));
    const bridge = makeBridge();
    const pending = exportDashboardElementPng(document.createElement("section"), "board-a", bridge);
    await vi.advanceTimersByTimeAsync(0);
    expect(toBlob).toHaveBeenCalledTimes(1);
    session.activeWorkspaceId = "workspace-b";
    session.sessionEpoch = 2;
    finish(new Blob(["png-a"]));
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(posted).toEqual([]);
    expect(createObjectURL).not.toHaveBeenCalled();
    bridge.stop();
  });

  it("rejects a retired dashboard before the lazy PNG import finishes", async () => {
    const owner = new AbortController();
    const bridge = makeBridge();
    const pending = exportDashboardElementPng(document.createElement("section"), "old-board", bridge, owner.signal);
    owner.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(toBlob).not.toHaveBeenCalled();
    expect(posted).toEqual([]);
    bridge.stop();
  });

  it("retires a granted download wait and ignores its old saved receipt", async () => {
    const owner = new AbortController();
    const bridge = makeBridge();
    const pending = exportDashboardCsv("old-board", readyData(), bridge, owner.signal);
    await vi.advanceTimersByTimeAsync(0);
    await grantTarget();
    owner.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    reply({ type: "dashboard.exportSettled", payload: { token: "download-1", outcome: "saved" } });
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    bridge.stop();
  });

  it("rejects a PNG whose budget would over-shrink the image", async () => {
    const element = document.createElement("section");
    Object.defineProperties(element, {
      scrollWidth: { value: 500_000 },
      scrollHeight: { value: 8_000 },
    });
    const bridge = makeBridge();
    await expect(exportDashboardElementPng(element, "board", bridge))
      .rejects.toMatchObject({ code: "PNG_BUDGET_EXCEEDED" });
    expect(toBlob).not.toHaveBeenCalled();
    expect(posted).toEqual([]);
    bridge.stop();
  });

  it("rejects a PNG over the byte budget before contacting the host", async () => {
    const element = document.createElement("section");
    toBlob.mockResolvedValueOnce(new Blob([new Uint8Array(33 * 1024 * 1024)]));
    const bridge = makeBridge();
    await expect(exportDashboardElementPng(element, "board", bridge))
      .rejects.toMatchObject({ code: "PNG_BYTES_EXCEEDED" });
    expect(posted).toEqual([]);
    expect(clicks).toEqual([]);
    bridge.stop();
  });

  it("reports empty PNG encodes", async () => {
    const element = document.createElement("section");
    toBlob.mockResolvedValueOnce(null);
    const bridge = makeBridge();
    await expect(exportDashboardElementPng(element, "broken", bridge))
      .rejects.toThrow("PNG export failed");
    bridge.stop();
  });

  it("opens the native browser print dialog through the host", async () => {
    const bridge = makeBridge();
    const promise = printDashboard(bridge);
    await vi.advanceTimersByTimeAsync(0);
    expect(posted.map((item) => item.type)).toEqual(["dashboard.printRequested"]);
    reply({
      type: "dashboard.printDialogOpened",
      requestId: posted[0]!.requestId,
      payload: { opened: true },
    });
    await expect(promise).resolves.toEqual({ opened: true });
    bridge.stop();
  });

  it("surfaces print dialog failures", async () => {
    const bridge = makeBridge();
    const promise = printDashboard(bridge);
    await vi.advanceTimersByTimeAsync(0);
    reply({
      type: "operation.failed",
      requestId: posted[0]!.requestId,
      payload: { message: "print unavailable", code: "PRINT_FAILED" },
    });
    await expect(promise).rejects.toThrow("print unavailable");
    bridge.stop();
  });
});
