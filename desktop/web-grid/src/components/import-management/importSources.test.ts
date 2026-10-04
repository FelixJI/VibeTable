import { describe, expect, it } from "vitest";
import { IMPORT_SOURCES, localFileAcceptList, type ImportSourceId } from "./importSources";

describe("importSources catalog", () => {
  it("registers exactly the four contract sources with stable ids", () => {
    expect(IMPORT_SOURCES.map((source) => source.id)).toEqual<ImportSourceId[]>([
      "csv", "xlsx", "feishu", "wps",
    ]);
    for (const source of IMPORT_SOURCES) {
      expect(source.labelKey.startsWith("importManagement.source.")).toBe(true);
    }
  });

  it("keeps cloud sources explicitly unavailable without picker accepts", () => {
    for (const cloud of IMPORT_SOURCES.filter((source) => !source.available)) {
      expect(cloud.id === "feishu" || cloud.id === "wps").toBe(true);
      expect(cloud.accept).toBeNull();
      expect(cloud.unavailableReasonKey).toBe("importManagement.source.unavailableTooltip");
    }
  });

  it("exposes file accepts only for the available local sources", () => {
    const locals = IMPORT_SOURCES.filter((source) => source.available);
    expect(locals.map((source) => source.id)).toEqual(["csv", "xlsx"]);
    expect(localFileAcceptList()).toEqual([".csv", ".xlsx", ".xlsm"]);
  });
});
