import type { DashboardPanelData } from "@/stores/dashboardStore";

const MAX_EXPORT_EDGE = 16_000;

export function exportDashboardCsv(
  fileName: string,
  data: DashboardPanelData,
): void {
  const columns = [...new Set(data.rows.flatMap((row) => Object.keys(row)))];
  const lines = [
    columns.map(csvCell).join(","),
    ...data.rows.map((row) => columns.map((column) => csvCell(row[column])).join(",")),
  ];
  downloadBlob(
    `${safeFileName(fileName)}.csv`,
    new Blob(["\uFEFF", lines.join("\r\n")], { type: "text/csv;charset=utf-8" }),
  );
}

export async function exportDashboardElementPng(
  element: HTMLElement,
  fileName: string,
): Promise<void> {
  const { toBlob } = await import("html-to-image");
  const width = Math.max(1, element.scrollWidth);
  const height = Math.max(1, element.scrollHeight);
  const scale = Math.min(2, MAX_EXPORT_EDGE / width, MAX_EXPORT_EDGE / height);
  const blob = await toBlob(element, {
    cacheBust: true,
    pixelRatio: Math.max(0.5, scale),
    backgroundColor: getComputedStyle(document.documentElement).getPropertyValue("--vt-bg").trim() || "#fff",
    width,
    height,
  });
  if (!blob) throw new Error("PNG export failed.");
  downloadBlob(`${safeFileName(fileName)}.png`, blob);
}

export function printDashboard(): void {
  window.print();
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

function downloadBlob(fileName: string, blob: Blob): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
