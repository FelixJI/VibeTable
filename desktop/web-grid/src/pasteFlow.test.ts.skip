import { describe, expect, it } from "vitest";

import {
  createPasteFlow,
  errorsByRow,
  initialPasteFlowState,
  outcomeLine,
  PASTE_CELL_LIMIT,
  summaryLine,
  type PasteBridgeAction,
} from "./pasteFlow";
import type { ApplyPasteResult, PastePlan, PasteSummary } from "./contracts";

function collect() {
  const actions: PasteBridgeAction[] = [];
  const controller = createPasteFlow({
    onStateChange: () => undefined,
  });
  return { actions, controller };
}

function plan(overrides: Partial<PastePlan> = {}): PastePlan {
  return {
    collection: "vibetable_contracts",
    schemaRevision: "rev-1",
    capabilityHash: "cap-1",
    summary: { updateRows: 2, insertRows: 0, skipRows: 0, errorCount: 0, warningCount: 0 },
    rows: [],
    diagnostics: [],
    token: { token: "tok-1", expiresAt: 1e12, consumed: false },
    overflow: false,
    ...overrides,
  };
}

describe("createPasteFlow", () => {
  it("starts idle", () => {
    const { controller } = collect();
    expect(controller.getState().phase).toBe("idle");
  });

  it("routes oversize clipboards to the overflow state without submitting", () => {
    const { actions, controller } = collect();
    controller.requestPreview((a) => actions.push(a), {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: PASTE_CELL_LIMIT + 1,
    });
    expect(controller.getState().phase).toBe("overflow");
    expect(actions).toHaveLength(0);
  });

  it("submits a preview request for in-range clipboards", () => {
    const { actions, controller } = collect();
    controller.requestPreview((a) => actions.push(a), {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [[{ rawValue: "x" }]],
      cellCount: 1,
    });
    expect(controller.getState().phase).toBe("previewing");
    expect(actions).toHaveLength(1);
    expect(actions[0].kind).toBe("preview");
  });

  it("blocks submission when the plan has errors", () => {
    const { controller } = collect();
    controller.requestPreview(() => undefined, {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: 1,
    });
    controller.onPreviewReady(plan({ summary: { updateRows: 1, insertRows: 0, skipRows: 0, errorCount: 1, warningCount: 0 } }));
    expect(controller.getState().phase).toBe("preview");
    expect(controller.canSubmit()).toBe(false);
  });

  it("blocks submission when warnings exist and are not acknowledged", () => {
    const { controller } = collect();
    controller.requestPreview(() => undefined, {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: 1,
    });
    controller.onPreviewReady(plan({ summary: { updateRows: 1, insertRows: 0, skipRows: 0, errorCount: 0, warningCount: 2 } }));
    expect(controller.canSubmit()).toBe(false);
    controller.acknowledgeWarnings(true);
    expect(controller.canSubmit()).toBe(true);
  });

  it("allows submission for an error-free plan", () => {
    const { controller } = collect();
    controller.requestPreview(() => undefined, {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: 1,
    });
    controller.onPreviewReady(plan());
    expect(controller.canSubmit()).toBe(true);
  });

  it("submits apply with a fresh idempotency key", () => {
    const { actions, controller } = collect();
    controller.requestPreview(() => undefined, {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: 1,
    });
    controller.onPreviewReady(plan());
    controller.requestApply((a) => actions.push(a), "idem-1");
    expect(controller.getState().phase).toBe("applying");
    expect(actions).toHaveLength(1);
    expect(actions[0]).toMatchObject({ kind: "apply", idempotencyKey: "idem-1", token: "tok-1" });
  });

  it("refuses apply after cancellation", () => {
    const { actions, controller } = collect();
    controller.requestPreview(() => undefined, {
      collection: "vibetable_contracts",
      schemaRevision: "rev-1",
      selection: {},
      startCell: { rowKey: "1", column: "number" },
      cells: [],
      cellCount: 1,
    });
    controller.onPreviewReady(plan());
    controller.cancel();
    controller.requestApply((a) => actions.push(a), "idem-1");
    expect(actions).toHaveLength(0);
  });

  it("transitions to committed on a successful apply result", () => {
    const { controller } = collect();
    controller.onApplyResult({
      collection: "vibetable_contracts",
      outcome: "committed",
      createdRowKeys: [],
      updatedRowKeys: ["1", "2"],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "idem-1",
    } as ApplyPasteResult);
    expect(controller.getState().phase).toBe("committed");
  });

  it("transitions to conflict and shows the changed rows", () => {
    const { controller } = collect();
    controller.onApplyResult({
      collection: "vibetable_contracts",
      outcome: "conflict",
      createdRowKeys: [],
      updatedRowKeys: [],
      skippedRowKeys: [],
      conflicts: [{ rowKey: "1", currentValue: { number: "x" } }],
      requestId: "idem-1",
    } as ApplyPasteResult);
    expect(controller.getState().phase).toBe("conflict");
    expect(controller.getState().result?.conflicts).toHaveLength(1);
  });

  it("transitions to pending on a timeout result", () => {
    const { controller } = collect();
    controller.onApplyResult({
      collection: "vibetable_contracts",
      outcome: "pending",
      createdRowKeys: [],
      updatedRowKeys: [],
      skippedRowKeys: [],
      conflicts: [],
      requestId: "idem-1",
    } as ApplyPasteResult);
    expect(controller.getState().phase).toBe("pending");
  });
});

describe("presentation helpers", () => {
  it("groups errors by row index", () => {
    const grouped = errorsByRow(
      plan({
        rows: [
          {
            kind: "update",
            targetRowKey: "1",
            changes: {},
            diagnostics: [
              { rowIndex: 0, columnIndex: 1, severity: "error", code: "column_readonly", message: "readonly" },
            ],
          },
        ],
        diagnostics: [
          { rowIndex: 0, columnIndex: 0, severity: "warning", code: "x", message: "w" },
          { rowIndex: 1, columnIndex: 0, severity: "error", code: "y", message: "e" },
        ],
      }),
    );
    expect(grouped.map((g) => g.rowIndex)).toEqual([0, 1]);
    expect(grouped[0].diagnostics).toHaveLength(1);
  });

  it("summary line pluralizes warnings and errors", () => {
    const s: PasteSummary = { updateRows: 3, insertRows: 1, skipRows: 0, errorCount: 2, warningCount: 2 };
    expect(summaryLine(s)).toBe("3 to update, 1 to add, 2 warnings, 2 errors");
  });

  it("outcome line handles committed/conflict/pending", () => {
    expect(
      outcomeLine({ outcome: "committed", createdRowKeys: ["a"], updatedRowKeys: ["b"], skippedRowKeys: [], conflicts: [], collection: "c", requestId: "r" } as ApplyPasteResult),
    ).toContain("Committed");
    expect(
      outcomeLine({ outcome: "conflict", createdRowKeys: [], updatedRowKeys: [], skippedRowKeys: [], conflicts: [{ rowKey: "1", currentValue: {} }], collection: "c", requestId: "r" } as ApplyPasteResult),
    ).toContain("Conflict");
    expect(
      outcomeLine({ outcome: "pending", createdRowKeys: [], updatedRowKeys: [], skippedRowKeys: [], conflicts: [], collection: "c", requestId: "r" } as ApplyPasteResult),
    ).toContain("pending");
  });
});

describe("initial state", () => {
  it("exposes a stable initial state", () => {
    expect(initialPasteFlowState.phase).toBe("idle");
    expect(initialPasteFlowState.plan).toBeNull();
  });
});
