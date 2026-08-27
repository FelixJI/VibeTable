import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  exportDashboardCsv,
  exportDashboardElementPng,
  printDashboard,
} from "./dashboardExportService";

const toBlob = vi.fn();

vi.mock("html-to-image", () => ({ toBlob }));

describe("dashboardExportService", () => {
  const createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:dashboard");
  const revokeObjectURL = vi.fn();
  const downloadedNames: string[] = [];

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
    downloadedNames.length = 0;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      downloadedNames.push(this.download);
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    toBlob.mockReset();
  });

  it("exports the union of row columns as escaped UTF-8 CSV", () => {
    exportDashboardCsv("  Sales/Q3:*?  ", {
      rows: [
        { name: "Ada, Inc.", note: 'said "hello"', amount: 2 },
        { name: null, extra: { active: true } },
      ],
    } as never);

    const anchor = document.querySelector("a");
    expect(anchor).toBeNull();
    expect(createObjectURL).toHaveBeenCalledOnce();
    expect(createObjectURL.mock.calls[0]?.[0]).toBeInstanceOf(Blob);
    expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledOnce();
    vi.runAllTimers();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:dashboard");
  });

  it("avoids Windows reserved device names for CSV downloads", () => {
    exportDashboardCsv("CON", { rows: [] } as never);

    expect(downloadedNames).toEqual(["_CON.csv"]);
  });

  it.each([
    ["con", "_con.csv"],
    ["CON.report", "_CON.report.csv"],
  ])("avoids case-insensitive and extended CON basenames", (input, expected) => {
    exportDashboardCsv(input, { rows: [] } as never);

    expect(downloadedNames).toEqual([expected]);
  });

  it.each([
    "PRN", "AUX", "NUL",
    ...Array.from({ length: 9 }, (_, index) => `COM${index + 1}`),
    ...Array.from({ length: 9 }, (_, index) => `LPT${index + 1}`),
    "COM¹", "COM²", "COM³",
    "LPT¹", "LPT²", "LPT³",
  ])("avoids the Windows %s device basename", (input) => {
    exportDashboardCsv(input, { rows: [] } as never);

    expect(downloadedNames).toEqual([`_${input}.csv`]);
  });

  it.each([
    ["销售报表📊", "销售报表📊.csv"],
    ["COM0", "COM0.csv"],
    ["COM10", "COM10.csv"],
    ["LPT0", "LPT0.csv"],
    ["LPT10", "LPT10.csv"],
  ])("preserves the legal export name %s", (input, expected) => {
    exportDashboardCsv(input, { rows: [] } as never);

    expect(downloadedNames).toEqual([expected]);
  });

  it("uses the same Windows device-name rule for PNG downloads", async () => {
    const element = document.createElement("section");
    toBlob.mockResolvedValueOnce(new Blob(["png"], { type: "image/png" }));

    await exportDashboardElementPng(element, "lpt9.chart");

    expect(downloadedNames).toEqual(["_lpt9.chart.png"]);
  });

  it("keeps the existing export-name bound after prefixing a reserved basename", () => {
    const suffix = "a".repeat(116);

    exportDashboardCsv(`CON.${suffix}`, { rows: [] } as never);

    expect(downloadedNames).toEqual([`_CON.${"a".repeat(115)}.csv`]);
  });

  it("renders a bounded PNG with theme fallback and reports empty encodes", async () => {
    const element = document.createElement("section");
    Object.defineProperties(element, {
      scrollWidth: { value: 20_000 },
      scrollHeight: { value: 0 },
    });
    const blob = new Blob(["png"], { type: "image/png" });
    toBlob.mockResolvedValueOnce(blob);

    await exportDashboardElementPng(element, "");

    expect(toBlob).toHaveBeenCalledWith(element, expect.objectContaining({
      width: 20_000,
      height: 1,
      pixelRatio: 0.8,
      backgroundColor: "#fff",
    }));
    expect(createObjectURL).toHaveBeenCalledWith(blob);

    toBlob.mockResolvedValueOnce(null);
    await expect(exportDashboardElementPng(element, "broken"))
      .rejects.toThrow("PNG export failed");
  });

  it("delegates printing to the browser", () => {
    const print = vi.spyOn(window, "print").mockImplementation(() => undefined);
    printDashboard();
    expect(print).toHaveBeenCalledOnce();
  });
});
