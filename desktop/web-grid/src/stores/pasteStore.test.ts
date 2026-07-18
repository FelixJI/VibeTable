import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { usePasteStore } from "./pasteStore";
import type {
  ApplyPasteResult,
  PastePlan,
  PasteSummary,
} from "@/contracts";

/**
 * Build a valid `PastePlan` with the REAL contract fields. The brief's example
 * used invented fields (`rows: 2, columns: 3, cells: []`) that do not exist on
 * `PastePlan` (see `src/contracts/index.ts`). The real shape carries a
 * `summary: PasteSummary`, a `rows: PastePlanRow[]` list, a top-level
 * `diagnostics` array, a `token`, and an `overflow` flag.
 */
function makePlan(opts: {
  readonly updateRows?: number;
  readonly insertRows?: number;
  readonly skipRows?: number;
  readonly errorCount?: number;
  readonly warningCount?: number;
  readonly overflow?: boolean;
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
    overflow: opts.overflow ?? false,
  };
}

/** Build a valid `ApplyPasteResult` with the REAL contract fields. */
function makeResult(opts: {
  readonly created?: readonly (number | string)[];
  readonly updated?: readonly (number | string)[];
  readonly skipped?: readonly (number | string)[];
  readonly outcome?: ApplyPasteResult["outcome"];
} = {}): ApplyPasteResult {
  return {
    collection: "users",
    outcome: opts.outcome ?? "committed",
    createdRowKeys: opts.created ?? [],
    updatedRowKeys: opts.updated ?? [],
    skippedRowKeys: opts.skipped ?? [],
    conflicts: [],
    requestId: "req-1",
  };
}

describe("pasteStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle", () => {
    const s = usePasteStore();
    expect(s.phase).toBe("idle");
    expect(s.plan).toBeNull();
    expect(s.result).toBeNull();
    expect(s.acked).toBe(false);
    expect(s.error).toBeNull();
    expect(s.summaryText).toBe("");
  });

  it("setPlan moves to previewing, resets ack, and derives summaryText from plan.summary", () => {
    const s = usePasteStore();
    // summaryText should sum updateRows + insertRows from PasteSummary.
    s.setPlan(makePlan({ updateRows: 2, insertRows: 3 }));
    expect(s.phase).toBe("previewing");
    expect(s.acked).toBe(false);
    expect(s.result).toBeNull();
    expect(s.error).toBeNull();
    // 2 updates + 3 inserts = 5 rows to write.
    expect(s.summaryText).toBe("将写入 5 行");
  });

  it("toggleAck flips acknowledgement", () => {
    const s = usePasteStore();
    s.toggleAck();
    expect(s.acked).toBe(true);
    s.toggleAck();
    expect(s.acked).toBe(false);
  });

  it("beginApply moves to applying", () => {
    const s = usePasteStore();
    s.setPlan(makePlan());
    s.beginApply();
    expect(s.phase).toBe("applying");
  });

  it("setResult moves to applied and derives summaryText from the result", () => {
    const s = usePasteStore();
    s.setPlan(makePlan({ updateRows: 1 }));
    s.setResult(makeResult({ created: [10, 11], updated: [20] }));
    expect(s.phase).toBe("applied");
    expect(s.summaryText).toBe("已创建 2 行，更新 1 行");
  });

  it("setError moves to error", () => {
    const s = usePasteStore();
    s.setError("bad");
    expect(s.phase).toBe("error");
    expect(s.error).toBe("bad");
  });

  it("reset returns to idle", () => {
    const s = usePasteStore();
    s.setPlan(makePlan({ updateRows: 1 }));
    s.toggleAck();
    s.reset();
    expect(s.phase).toBe("idle");
    expect(s.plan).toBeNull();
    expect(s.result).toBeNull();
    expect(s.acked).toBe(false);
    expect(s.error).toBeNull();
    expect(s.summaryText).toBe("");
  });
});
