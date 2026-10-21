import { describe, expect, it } from "vitest";
import { IMPORT_SOURCES, localFileAcceptList, type ImportSourceId } from "./importSources";

describe("importSources catalog", () => {
  it("registers exactly the three formal sources with stable ids", () => {
    expect(IMPORT_SOURCES.map((source) => source.id)).toEqual<ImportSourceId[]>([
      "csv", "xlsx", "feishuBase",
    ]);
    for (const source of IMPORT_SOURCES) {
      expect(source.labelKey.startsWith("importManagement.source.")).toBe(true);
    }
  });

  it("routes the .base wizard to the Host without local file accepts", () => {
    const wizard = IMPORT_SOURCES.filter((source) => source.accept === null);
    expect(wizard.map((source) => source.id)).toEqual(["feishuBase"]);
    expect(wizard[0]!.available).toBe(true);
    expect(wizard[0]!.unavailableReasonKey).toBe("importManagement.source.unavailableTooltip");
    // The retired online cloud providers never reappear as wizard entries.
    expect(IMPORT_SOURCES.some((source) => (["feishu", "wps"] as string[]).includes(source.id))).toBe(false);
  });

  it("exposes file accepts only for the available local sources", () => {
    const locals = IMPORT_SOURCES.filter((source) => source.available && source.accept !== null);
    expect(locals.map((source) => source.id)).toEqual(["csv", "xlsx"]);
    expect(localFileAcceptList()).toEqual([".csv", ".xlsx", ".xlsm"]);
  });
});
