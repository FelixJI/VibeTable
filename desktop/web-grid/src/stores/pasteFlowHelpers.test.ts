import { describe, it, expect, beforeEach, afterEach } from "vitest";
import type {
  ApplyPasteResult,
  PastePlan,
  PasteSummary,
} from "@/contracts";
import { setLocale, getLocale } from "@/i18n";
import { summaryLine, outcomeLine } from "./pasteFlowHelpers";

/**
 * Build a valid `PastePlan` with the REAL contract fields (see pasteStore.test
 * for the shape rationale).
 */
function makePlan(opts: {
  readonly updateRows?: number;
  readonly insertRows?: number;
  readonly skipRows?: number;
  readonly errorCount?: number;
  readonly warningCount?: number;
} = {}): PastePlan {
  const summary: PasteSummary = {
    updateRows: opts.updateRows ?? 0,
    insertRows: opts.insertRows ?? 0,
    skipRows: opts.skipRows ?? 0,
    errorCount: opts.errorCount ?? 0,
    warningCount: opts.warningCount ?? 0,
  };
  return {
    collection: "users",
    schemaRevision: "schema-1",
    capabilityHash: "cap-1",
    summary,
    rows: [],
    diagnostics: [],
    token: { token: "tok-1", expiresAt: 0, consumed: false },
    overflow: false,
  };
}

function makeResult(opts: {
  readonly created?: readonly (number | string)[];
  readonly updated?: readonly (number | string)[];
  readonly skipped?: readonly (number | string)[];
} = {}): ApplyPasteResult {
  return {
    collection: "users",
    outcome: "committed",
    createdRowKeys: opts.created ?? [],
    updatedRowKeys: opts.updated ?? [],
    skippedRowKeys: opts.skipped ?? [],
    conflicts: [],
    requestId: "req-1",
  };
}

describe("pasteFlowHelpers i18n", () => {
  const prevLocale = getLocale();

  beforeEach(() => setLocale("zh-CN"));
  // The i18n module holds locale in module state; restore so other test files
  // (which assume the default zh-CN) are not affected.
  afterEach(() => setLocale(prevLocale));

  describe("summaryLine", () => {
    it("returns empty for null plan", () => {
      expect(summaryLine(null)).toBe("");
    });

    it("zh-CN stays byte-identical to the pre-i18n hardcoded strings", () => {
      // Base: updateRows + insertRows.
      expect(summaryLine(makePlan({ updateRows: 2, insertRows: 3 }))).toBe(
        "将写入 5 行",
      );
      // + skipRows.
      expect(
        summaryLine(makePlan({ updateRows: 1, skipRows: 2 })),
      ).toBe("将写入 1 行，跳过 2 行");
      // + errorCount (errors take precedence over warnings).
      expect(
        summaryLine(makePlan({ updateRows: 1, errorCount: 4 })),
      ).toBe("将写入 1 行，4 项错误");
      // + warningCount (no errors).
      expect(
        summaryLine(makePlan({ updateRows: 1, warningCount: 7 })),
      ).toBe("将写入 1 行，7 项警告");
    });

    it("en-US produces translated summary", () => {
      setLocale("en-US");
      expect(summaryLine(makePlan({ updateRows: 2, insertRows: 3 }))).toBe(
        "Will write 5 rows",
      );
      expect(
        summaryLine(makePlan({ updateRows: 1, skipRows: 2 })),
      ).toBe("Will write 1 rows, Skip 2 rows");
      expect(
        summaryLine(makePlan({ updateRows: 1, warningCount: 7 })),
      ).toBe("Will write 1 rows, 7 warnings");
    });
  });

  describe("outcomeLine", () => {
    it("returns empty for null result", () => {
      expect(outcomeLine(null)).toBe("");
    });

    it("zh-CN stays byte-identical to the pre-i18n hardcoded strings", () => {
      expect(outcomeLine(makeResult({ created: [10, 11], updated: [20] }))).toBe(
        "已创建 2 行，更新 1 行",
      );
      expect(
        outcomeLine(makeResult({ created: [1], updated: [2], skipped: [3] })),
      ).toBe("已创建 1 行，更新 1 行，跳过 1 行");
      expect(outcomeLine(makeResult())).toBe("无变更");
    });

    it("en-US produces translated outcome", () => {
      setLocale("en-US");
      expect(
        outcomeLine(makeResult({ created: [10, 11], updated: [20] })),
      ).toBe("Created 2 rows, Updated 1 rows");
      expect(outcomeLine(makeResult())).toBe("No changes");
    });
  });
});
