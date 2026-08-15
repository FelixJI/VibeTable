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

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
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
