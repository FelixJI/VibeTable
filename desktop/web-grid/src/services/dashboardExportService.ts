import type { HostBridge } from "@/bridge/hostBridge";
import { BridgeOperationError, BridgeRequestRetiredError } from "@/bridge/hostBridge";
import { getActivePinia } from "pinia";
import { watch } from "vue";
import { useWorkspaceSessionStore } from "@/stores/workspaceSessionStore";
import type {
  DashboardExportSettledPayload,
  DashboardExportTargetGrantedPayload,
} from "@/contracts";
import type { DashboardPanelData } from "@/stores/dashboardStore";

const MAX_EXPORT_EDGE = 16_000;
const MAX_EXPORT_PIXELS = 64_000_000;
const MAX_EXPORT_BYTES = 32 * 1024 * 1024;
const MIN_EXPORT_SCALE = 0.05;

export type DashboardExportCode =
  | "PANEL_NOT_READY"
  | "PNG_BUDGET_EXCEEDED"
  | "PNG_BYTES_EXCEEDED"
  | "SAVE_FAILED"
  | "SAVE_TIMEOUT";

export class DashboardExportError extends Error {
  public override readonly name = "DashboardExportError";
  public readonly code: DashboardExportCode;
  public readonly info: Readonly<Record<string, string | number>>;
  public constructor(
    code: DashboardExportCode,
    message: string,
    info: Readonly<Record<string, string | number>> = {},
  ) {
    super(message);
    this.code = code;
    this.info = info;
  }
}

export interface DashboardExportReceipt {
  readonly outcome: "saved" | "cancelled";
  readonly fileName: string;
}

export interface DashboardCsvExportReceipt extends DashboardExportReceipt {
  /** Visible before/after output: the panel result was cut at the query cap. */
  readonly truncated: boolean;
  readonly maxPoints: number;
}

export async function exportDashboardCsv(
  fileName: string,
  data: DashboardPanelData,
  bridge: HostBridge,
  signal?: AbortSignal,
): Promise<DashboardCsvExportReceipt> {
  if (data.state !== "ready") {
    throw new DashboardExportError(
      "PANEL_NOT_READY",
      `面板结果当前为 ${data.state}，拒绝导出。`,
      { state: data.state },
    );
  }
  const columns = [...new Set(data.rows.flatMap((row) => Object.keys(row)))];
  const lines = [
    columns.map(csvCell).join(","),
    ...data.rows.map((row) => columns.map((column) => csvCell(row[column])).join(",")),
  ];
  const receipt = await saveDashboardOutput(
    fileName,
    "csv",
    new Blob(["\uFEFF", lines.join("\r\n")], { type: "text/csv;charset=utf-8" }),
    bridge,
    signal,
  );
  return { ...receipt, truncated: data.truncated, maxPoints: data.maxPoints };
}

export async function exportDashboardElementPng(
  element: HTMLElement,
  fileName: string,
  bridge: HostBridge,
  signal?: AbortSignal,
): Promise<DashboardExportReceipt> {
  const owner = captureExportOwner(signal);
  try {
    owner.assertCurrent();
    const { toBlob } = await import("html-to-image");
    owner.assertCurrent();
    const width = Math.max(1, Math.floor(element.scrollWidth));
    const height = Math.max(1, Math.floor(element.scrollHeight));
    // Edge and total-pixel budgets both bound the encode; the scale is never
    // floored, so an over-large element cannot silently break either cap.
    const scale = Math.min(
      2,
      MAX_EXPORT_EDGE / width,
      MAX_EXPORT_EDGE / height,
      Math.sqrt(MAX_EXPORT_PIXELS / (width * height)),
    );
    if (!(scale >= MIN_EXPORT_SCALE)) {
      throw new DashboardExportError(
        "PNG_BUDGET_EXCEEDED",
        `图像 ${width}x${height} 超出导出预算。`,
        { width, height },
      );
    }
    const blob = await toBlob(element, {
      cacheBust: true,
      pixelRatio: scale,
      backgroundColor: getComputedStyle(document.documentElement).getPropertyValue("--vt-bg").trim() || "#fff",
      width,
      height,
    });
    owner.assertCurrent();
    if (!blob) throw new Error("PNG export failed.");
    if (blob.size > MAX_EXPORT_BYTES) {
      throw new DashboardExportError(
        "PNG_BYTES_EXCEEDED",
        `PNG 编码为 ${blob.size} 字节，超出字节预算。`,
        { bytes: blob.size },
      );
    }
    return await saveDashboardOutput(fileName, "png", blob, bridge, owner.signal);
  } finally {
    owner.dispose();
  }
}

export async function printDashboard(bridge: HostBridge): Promise<{ opened: true }> {
  // The host opens the WebView2 browser print preview; the SDK reports only
  // that the dialog opened, never that printing completed or was cancelled.
  return await bridge.request("dashboard.printRequested", {}) as { opened: true };
}

async function saveDashboardOutput(
  fileName: string,
  format: "csv" | "png",
  blob: Blob,
  bridge: HostBridge,
  signal?: AbortSignal,
): Promise<DashboardExportReceipt> {
  const owner = captureExportOwner(signal);
  try {
    owner.assertCurrent();
    const safe = safeFileName(fileName);
    const blobUrl = URL.createObjectURL(blob);
    let granted: DashboardExportTargetGrantedPayload;
    try {
      granted = await bridge.request("dashboard.exportTargetRequested", {
        fileName: safe,
        format,
        blobUrl,
        byteLength: blob.size,
      }) as DashboardExportTargetGrantedPayload;
      owner.assertCurrent();
    } catch (error) {
      URL.revokeObjectURL(blobUrl);
      if (error instanceof BridgeOperationError && error.code === "CANCELLED") {
        return { outcome: "cancelled", fileName: safe };
      }
      throw error instanceof Error ? error : new Error(String(error));
    }
    return await awaitDownloadSettlement(bridge, granted, safe, blobUrl, owner.signal);
  } finally {
    owner.dispose();
  }
}

function captureExportOwner(signal?: AbortSignal): {
  signal: AbortSignal;
  assertCurrent: () => void;
  dispose: () => void;
} {
  const controller = new AbortController();
  const abort = (): void => controller.abort();
  signal?.addEventListener("abort", abort, { once: true });
  if (signal?.aborted) abort();
  const session = getActivePinia() ? useWorkspaceSessionStore() : null;
  const workspaceId = session?.activeWorkspaceId;
  const epoch = session?.sessionEpoch;
  const bound = session?.enabled;
  const checkSession = (): void => {
    if (bound && session && (!session.enabled || session.activeWorkspaceId !== workspaceId
      || session.sessionEpoch !== epoch || !session.hasOpenWorkspace || session.isTransitioning)) abort();
  };
  const stop = session ? watch(
    () => [session.enabled, session.activeWorkspaceId, session.sessionEpoch, session.isTransitioning, session.sessionState],
    checkSession, { flush: "sync" },
  ) : undefined;
  checkSession();
  return {
    signal: controller.signal,
    assertCurrent: () => {
      checkSession();
      if (controller.signal.aborted) throw new BridgeRequestRetiredError();
    },
    dispose: () => { stop?.(); signal?.removeEventListener("abort", abort); },
  };
}

async function awaitDownloadSettlement(
  bridge: HostBridge,
  granted: DashboardExportTargetGrantedPayload,
  fileName: string,
  blobUrl: string,
  signal: AbortSignal,
): Promise<DashboardExportReceipt> {
  const cleanup: { unsubscribe: (() => void) | null } = {
    unsubscribe: null,
  };
  let retire: (() => void) | undefined;
  try {
    const settled = new Promise<DashboardExportSettledPayload>((resolve, reject) => {
      retire = () => reject(new BridgeRequestRetiredError());
      signal.addEventListener("abort", retire, { once: true });
      if (signal.aborted) { retire(); return; }
      cleanup.unsubscribe = bridge.on("dashboard.exportSettled", (payload) => {
        if (payload.token !== granted.token) return;
        resolve(payload);
      });
    });
    if (!signal.aborted) triggerSingleBlobDownload(blobUrl, granted.displayName);
    const settle = await settled;
    if (settle.outcome === "saved") {
      return { outcome: "saved", fileName: granted.displayName || fileName };
    }
    if (settle.outcome === "cancelled") {
      return { outcome: "cancelled", fileName };
    }
    throw new DashboardExportError(
      settle.reason === "SAVE_TIMEOUT" ? "SAVE_TIMEOUT" : "SAVE_FAILED",
      settle.reason || "保存失败。",
      { fileName },
    );
  } finally {
    if (retire) signal.removeEventListener("abort", retire);
    cleanup.unsubscribe?.();
    // The blob must stay alive until the WebView2 download has read it.
    URL.revokeObjectURL(blobUrl);
  }
}

function triggerSingleBlobDownload(blobUrl: string, fileName: string): void {
  const anchor = document.createElement("a");
  anchor.href = blobUrl;
  anchor.download = fileName;
  anchor.rel = "noopener";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}

function csvCell(value: unknown): string {
  const raw = value === null || value === undefined
    ? ""
    : typeof value === "object" ? JSON.stringify(value) : String(value);
  return /[",\r\n]/.test(raw) ? `"${raw.replaceAll('"', '""')}"` : raw;
}

function safeFileName(value: string): string {
  const sanitized = value.trim().replace(/[<>:"/\\|?*\u0000-\u001F]/g, "_").slice(0, 120)
    || "dashboard";
  const basename = sanitized.split(".", 1)[0]?.toUpperCase();
  const normalized = basename && /^(?:CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])$/.test(basename)
    ? `_${sanitized}`
    : sanitized;
  return normalized.slice(0, 120);
}
