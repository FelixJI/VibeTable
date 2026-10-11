import type { HostBridge } from "@/bridge/hostBridge";
import { BridgeOperationError } from "@/bridge/hostBridge";
import type {
  DashboardExportSettledPayload,
  DashboardExportTargetGrantedPayload,
} from "@/contracts";
import type { DashboardPanelData } from "@/stores/dashboardStore";

const MAX_EXPORT_EDGE = 16_000;
const MAX_EXPORT_PIXELS = 64_000_000;
const MAX_EXPORT_BYTES = 32 * 1024 * 1024;
const MIN_EXPORT_SCALE = 0.05;
const SAVE_SETTLE_TIMEOUT_MS = 120_000;

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
  );
  return { ...receipt, truncated: data.truncated, maxPoints: data.maxPoints };
}

export async function exportDashboardElementPng(
  element: HTMLElement,
  fileName: string,
  bridge: HostBridge,
): Promise<DashboardExportReceipt> {
  const { toBlob } = await import("html-to-image");
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
  if (!blob) throw new Error("PNG export failed.");
  if (blob.size > MAX_EXPORT_BYTES) {
    throw new DashboardExportError(
      "PNG_BYTES_EXCEEDED",
      `PNG 编码为 ${blob.size} 字节，超出字节预算。`,
      { bytes: blob.size },
    );
  }
  return saveDashboardOutput(fileName, "png", blob, bridge);
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
): Promise<DashboardExportReceipt> {
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
  } catch (error) {
    URL.revokeObjectURL(blobUrl);
    if (error instanceof BridgeOperationError && error.code === "CANCELLED") {
      return { outcome: "cancelled", fileName: safe };
    }
    throw error instanceof Error ? error : new Error(String(error));
  }
  return await awaitDownloadSettlement(bridge, granted, safe, blobUrl);
}

async function awaitDownloadSettlement(
  bridge: HostBridge,
  granted: DashboardExportTargetGrantedPayload,
  fileName: string,
  blobUrl: string,
): Promise<DashboardExportReceipt> {
  const cleanup: { unsubscribe: (() => void) | null; timer: ReturnType<typeof setTimeout> | null } = {
    unsubscribe: null,
    timer: null,
  };
  try {
    const settled = new Promise<DashboardExportSettledPayload>((resolve, reject) => {
      cleanup.timer = setTimeout(() => {
        reject(new DashboardExportError(
          "SAVE_TIMEOUT",
          "保存未在限定时间内完成。",
          { fileName },
        ));
      }, SAVE_SETTLE_TIMEOUT_MS);
      cleanup.unsubscribe = bridge.on("dashboard.exportSettled", (payload) => {
        if (payload.token !== granted.token) return;
        if (cleanup.timer !== null) clearTimeout(cleanup.timer);
        resolve(payload);
      });
    });
    triggerSingleBlobDownload(blobUrl, granted.displayName);
    const settle = await settled;
    if (settle.outcome === "saved") {
      return { outcome: "saved", fileName: granted.displayName || fileName };
    }
    if (settle.outcome === "cancelled") {
      return { outcome: "cancelled", fileName };
    }
    throw new DashboardExportError(
      "SAVE_FAILED",
      settle.reason || "保存失败。",
      { fileName },
    );
  } finally {
    if (cleanup.timer !== null) clearTimeout(cleanup.timer);
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
