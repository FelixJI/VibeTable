import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createStructuredDialogFocus,
  type StructuredDialogFocusOutcome,
} from "./dialogFocus";

function outcomesWithoutLeaseIdentity(outcomes: StructuredDialogFocusOutcome[]) {
  expect(new Set(outcomes.map(({ leaseId }) => leaseId)).size).toBe(1);
  return outcomes.map(({ leaseId: _leaseId, ...outcome }) => outcome);
}

function createGridHarness(cell: () => HTMLElement, rowExists: () => boolean = () => true) {
  const handlers = new Map<string, Set<() => void>>();
  const decoyCell = document.createElement("button");
  return {
    grid: {
      getRows: () => rowExists()
        ? [
            {
              getIndex: () => "another-row",
              getCell: () => ({ getElement: () => decoyCell }),
            },
            {
              getIndex: () => "row-7",
              getCell: (field: string) => ({
                getElement: field === "payload" ? cell : () => decoyCell,
              }),
            },
          ]
        : [],
      on: (event: string, handler: () => void) => {
        const listeners = handlers.get(event) ?? new Set<() => void>();
        listeners.add(handler);
        handlers.set(event, listeners);
      },
      off: (event: string, handler: () => void) => {
        handlers.get(event)?.delete(handler);
      },
    },
    emit: (event: string) => {
      for (const handler of handlers.get(event) ?? []) handler();
    },
  };
}

describe("structured dialog focus", () => {
  let originalBodyTabIndex: string | null;

  beforeEach(() => {
    originalBodyTabIndex = document.body.getAttribute("tabindex");
    document.body.innerHTML = "";
  });

  afterEach(() => {
    if (originalBodyTabIndex === null) document.body.removeAttribute("tabindex");
    else document.body.setAttribute("tabindex", originalBodyTabIndex);
  });

  it("keeps logical cell focus when a committed row is rebuilt after restore", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    const { grid, emit } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    const lease = dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    });
    lease.restore();
    expect(document.activeElement).toBe(originalCell);

    originalCell.remove();
    document.body.append(replacementCell);
    currentCell = replacementCell;
    emit("renderComplete");

    expect(document.activeElement).toBe(replacementCell);
    dialogFocus.dispose();
  });

  it("reprojects focus when the captured cell is silently replaced", async () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    gridRoot.append(originalCell);
    document.body.append(gridRoot);
    let currentCell = originalCell;
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    expect(document.activeElement).toBe(originalCell);

    const mutationDelivered = new Promise<void>((resolve) => {
      const observer = new MutationObserver(() => {
        observer.disconnect();
        resolve();
      });
      observer.observe(gridRoot, { childList: true, subtree: true });
    });
    originalCell.remove();
    currentCell = replacementCell;
    gridRoot.append(replacementCell);
    expect(document.activeElement).toBe(document.body);
    await mutationDelivered;

    expect(document.activeElement).toBe(replacementCell);
    dialogFocus.dispose();
  });

  it("reprojects when the captured node stays connected but no longer owns the identity", () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    gridRoot.append(originalCell, replacementCell);
    document.body.append(gridRoot);
    let currentCell = originalCell;
    const reportOutcome = vi.fn();
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });

    const lease = dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
      target: "json",
    });
    currentCell = replacementCell;
    lease.restore();

    expect(document.activeElement).toBe(replacementCell);
    const outcomes = reportOutcome.mock.calls.map(
      ([outcome]) => outcome as StructuredDialogFocusOutcome,
    );
    expect(outcomesWithoutLeaseIdentity(outcomes).at(-1)).toEqual({
      state: "restored",
      target: "json",
      via: "reprojected",
    });
    dialogFocus.dispose();
  });

  it("keeps the lease when a closing focus owner moves focus to the document body", () => {
    document.body.tabIndex = -1;
    const cell = document.createElement("button");
    document.body.append(cell);
    const { grid } = createGridHarness(() => cell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: cell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    expect(document.activeElement).toBe(cell);

    document.body.focus();

    expect(document.activeElement).toBe(cell);
    dialogFocus.dispose();
  });

  it("reclaims focus from Tabulator's tableholder sink without user intent", () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const tableholder = document.createElement("div");
    tableholder.className = "tabulator-tableholder";
    tableholder.tabIndex = 0;
    const targetCell = document.createElement("button");
    tableholder.append(targetCell);
    gridRoot.append(tableholder);
    document.body.append(gridRoot);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    tableholder.focus();

    expect(document.activeElement).toBe(targetCell);
    dialogFocus.dispose();
  });

  it("reclaims focus from another Tabulator cell without user intent", () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const targetCell = document.createElement("button");
    targetCell.className = "tabulator-cell";
    const decoyCell = document.createElement("button");
    decoyCell.className = "tabulator-cell";
    gridRoot.append(targetCell, decoyCell);
    document.body.append(gridRoot);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    decoyCell.focus();

    expect(document.activeElement).toBe(targetCell);
    dialogFocus.dispose();
  });

  it("reprojects after the tableholder takes focus before the replacement cell mounts", async () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const tableholder = document.createElement("div");
    tableholder.className = "tabulator-tableholder";
    tableholder.tabIndex = 0;
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    tableholder.append(originalCell);
    gridRoot.append(tableholder);
    document.body.append(gridRoot);
    let currentCell = originalCell;
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    originalCell.remove();
    tableholder.focus();
    expect(document.activeElement).toBe(tableholder);

    const mutationDelivered = new Promise<void>((resolve) => {
      const observer = new MutationObserver(() => {
        observer.disconnect();
        resolve();
      });
      observer.observe(tableholder, { childList: true });
    });
    currentCell = replacementCell;
    tableholder.append(replacementCell);
    await mutationDelivered;

    expect(document.activeElement).toBe(replacementCell);
    dialogFocus.dispose();
  });

  it("does not treat an unscoped tableholder class as captured grid infrastructure", () => {
    const targetCell = document.createElement("button");
    const unrelatedTableholder = document.createElement("div");
    unrelatedTableholder.className = "tabulator-tableholder";
    unrelatedTableholder.tabIndex = 0;
    document.body.append(targetCell, unrelatedTableholder);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    unrelatedTableholder.focus();

    expect(document.activeElement).toBe(unrelatedTableholder);
    dialogFocus.dispose();
  });

  it("allows another in-grid control to own focus without user intent", () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const tableholder = document.createElement("div");
    tableholder.className = "tabulator-tableholder";
    const targetCell = document.createElement("button");
    const headerFilter = document.createElement("input");
    tableholder.append(targetCell);
    gridRoot.append(tableholder, headerFilter);
    document.body.append(gridRoot);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    headerFilter.focus();

    expect(document.activeElement).toBe(headerFilter);
    dialogFocus.dispose();
  });

  it("preserves the focus lease across the native Shift+F10 command sequence", () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const targetCell = document.createElement("button");
    const infrastructureCell = document.createElement("button");
    targetCell.className = "tabulator-cell";
    infrastructureCell.className = "tabulator-cell";
    gridRoot.append(targetCell, infrastructureCell);
    document.body.append(gridRoot);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    targetCell.dispatchEvent(new KeyboardEvent("keydown", {
      bubbles: true,
      key: "Shift",
      shiftKey: true,
    }));
    infrastructureCell.focus();
    expect(document.activeElement).toBe(targetCell);

    targetCell.dispatchEvent(new KeyboardEvent("keydown", {
      bubbles: true,
      key: "F10",
      shiftKey: true,
    }));
    infrastructureCell.focus();
    expect(document.activeElement).toBe(targetCell);
    dialogFocus.dispose();
  });

  it.each([
    ["pointer", () => new PointerEvent("pointerdown", { bubbles: true })],
    ["keyboard navigation", () => new KeyboardEvent("keydown", {
      bubbles: true,
      key: "ArrowRight",
    })],
  ])("allows %s intent to move focus within the grid", (_intent, createEvent) => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const targetCell = document.createElement("button");
    const intendedCell = document.createElement("button");
    targetCell.className = "tabulator-cell";
    intendedCell.className = "tabulator-cell";
    gridRoot.append(targetCell, intendedCell);
    document.body.append(gridRoot);
    const { grid } = createGridHarness(() => targetCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: targetCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    intendedCell.dispatchEvent(createEvent());
    intendedCell.focus();

    expect(document.activeElement).toBe(intendedCell);
    dialogFocus.dispose();
  });

  it("restores the logical cell after the row is temporarily absent during reprojection", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    expect(document.activeElement).toBe(originalCell);

    originalCell.remove();
    rowExists = false;
    emit("renderComplete");

    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    expect(document.activeElement).toBe(replacementCell);
    dialogFocus.dispose();
  });

  it("restores an exact captured cell when the row disappears before the first release", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    const lease = dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    });
    originalCell.remove();
    rowExists = false;
    lease.restore();

    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    expect(document.activeElement).toBe(replacementCell);
    dialogFocus.dispose();
  });

  it("fails closed when capture did not verify the exact current cell identity", () => {
    const staleCell = document.createElement("button");
    const currentCell = document.createElement("button");
    document.body.append(staleCell, currentCell);
    const reportOutcome = vi.fn();
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });

    dialogFocus.capture({
      element: staleCell,
      rowKey: "row-7",
      field: "payload",
      target: "json",
    }).restore();

    expect(document.activeElement).toBe(document.body);
    const outcomes = reportOutcome.mock.calls.map(
      ([outcome]) => outcome as StructuredDialogFocusOutcome,
    );
    expect(outcomesWithoutLeaseIdentity(outcomes)).toEqual([
      { state: "claimed", target: "json" },
      { state: "released", target: "json" },
      { state: "cancelled", reason: "stale", target: "json" },
    ]);
    dialogFocus.dispose();
  });

  it("reports the bounded focus lease state machine without business identity", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    const reportOutcome = vi.fn();
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });

    const lease = dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
      target: "json",
    });
    originalCell.remove();
    rowExists = false;
    lease.restore();
    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    const outcomes = reportOutcome.mock.calls.map(
      ([outcome]) => outcome as StructuredDialogFocusOutcome,
    );
    expect(outcomesWithoutLeaseIdentity(outcomes)).toEqual([
      { state: "claimed", target: "json" },
      { state: "released", target: "json" },
      { state: "pending", reason: "row", target: "json" },
      { state: "restored", target: "json", via: "reprojected" },
    ]);
    expect(JSON.stringify(reportOutcome.mock.calls)).not.toContain("row-7");
    expect(JSON.stringify(reportOutcome.mock.calls)).not.toContain("payload");
    dialogFocus.dispose();
  });

  it("allocates lease identities monotonically across focus service instances", () => {
    const cell = document.createElement("button");
    document.body.append(cell);
    const { grid } = createGridHarness(() => cell);
    const firstOutcomes: Array<{ leaseId: number; state: string }> = [];
    const secondOutcomes: Array<{ leaseId: number; state: string }> = [];
    const dependencies = {
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    };
    const first = createStructuredDialogFocus({
      ...dependencies,
      reportOutcome: outcome => firstOutcomes.push(outcome),
    });
    first.capture({ element: cell, rowKey: "row-7", field: "payload", target: "json" });
    first.dispose();
    const second = createStructuredDialogFocus({
      ...dependencies,
      reportOutcome: outcome => secondOutcomes.push(outcome),
    });
    second.capture({ element: cell, rowKey: "row-7", field: "payload", target: "json" });

    expect(secondOutcomes[0]?.leaseId).toBeGreaterThan(firstOutcomes[0]?.leaseId ?? 0);
    second.dispose();
  });

  it.each([
    ["stale", (lease: { cancel(): void }) => lease.cancel()],
    ["window", () => window.dispatchEvent(new Event("blur"))],
    ["disposed", (_lease: unknown, focus: { dispose(): void }) => focus.dispose()],
    ["scope", (_lease: unknown, _focus: unknown, changeScope: () => void) => changeScope()],
  ] as const)("reports %s cancellation as a closed reason", (reason, cancel) => {
    const cell = document.createElement("button");
    document.body.append(cell);
    const reportOutcome = vi.fn();
    let scopeChanged: () => void = () => undefined;
    const { grid } = createGridHarness(() => cell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: (listener) => {
        scopeChanged = listener;
        return () => undefined;
      },
      reportOutcome,
    });
    const lease = dialogFocus.capture({ element: cell, rowKey: "row-7", field: "payload" });

    cancel(lease, dialogFocus, scopeChanged);

    const outcomes = reportOutcome.mock.calls.map(
      ([outcome]) => outcome as StructuredDialogFocusOutcome,
    );
    expect(outcomesWithoutLeaseIdentity(outcomes).at(-1)).toEqual({
      state: "cancelled",
      reason,
    });
    dialogFocus.dispose();
  });

  it("reports external focus ownership before abandoning the lease", () => {
    const cell = document.createElement("button");
    const other = document.createElement("button");
    document.body.append(cell, other);
    const reportOutcome = vi.fn();
    let rowExists = true;
    const { grid } = createGridHarness(() => cell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });
    const lease = dialogFocus.capture({ element: cell, rowKey: "row-7", field: "payload" });
    cell.remove();
    rowExists = false;
    lease.restore();

    other.focus();

    const outcomes = reportOutcome.mock.calls.map(
      ([outcome]) => outcome as StructuredDialogFocusOutcome,
    );
    expect(outcomesWithoutLeaseIdentity(outcomes).at(-1)).toEqual({
      state: "cancelled",
      reason: "external",
    });
    dialogFocus.dispose();
  });

  it.each([
    ["grid", "missing", document.createElement("button")],
    ["cell", "detached", document.createElement("button")],
    ["focus-rejected", "connected", document.createElement("div")],
  ] as const)("reports a pending %s layer", (reason, releaseState, cell) => {
    document.body.append(cell);
    const reportOutcome = vi.fn();
    let gridAvailable = true;
    const grid = {
      getRows: () => [{
        getIndex: () => "row-7",
        getCell: () => ({ getElement: () => cell }),
      }],
      on: vi.fn(),
      off: vi.fn(),
    };
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => gridAvailable ? grid : null,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });
    const lease = dialogFocus.capture({ element: cell, rowKey: "row-7", field: "payload" });
    if (releaseState === "missing") gridAvailable = false;
    if (releaseState === "detached") cell.remove();

    lease.restore();

    expect(reportOutcome.mock.calls.some(([outcome]) => (
      outcome.state === "pending" && outcome.reason === reason
    ))).toBe(true);
    dialogFocus.dispose();
  });

  it("reports claim, release, and the first terminal outcome exactly once", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    const reportOutcome = vi.fn();
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
      reportOutcome,
    });
    const lease = dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
      target: "json",
    });
    originalCell.remove();
    rowExists = false;

    lease.restore();
    lease.restore();
    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");
    emit("renderComplete");
    dialogFocus.dispose();

    const outcomes = reportOutcome.mock.calls.map(([outcome]) => outcome);
    expect(new Set(outcomes.map(({ leaseId }) => leaseId)).size).toBe(1);
    expect(outcomes.filter(({ state }) => state === "claimed")).toHaveLength(1);
    expect(outcomes.filter(({ state }) => state === "released")).toHaveLength(1);
    expect(outcomesWithoutLeaseIdentity(
      outcomes.filter(({ state }) => state === "restored" || state === "cancelled"),
    )).toEqual([{ state: "restored", target: "json", via: "reprojected" }]);
  });

  it("does not steal focus after the user moves to another control", async () => {
    const gridRoot = document.createElement("div");
    gridRoot.className = "tabulator";
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    const otherControl = document.createElement("button");
    gridRoot.append(originalCell);
    document.body.append(gridRoot, otherControl);
    let currentCell = originalCell;
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    otherControl.focus();

    const mutationDelivered = new Promise<void>((resolve) => {
      const observer = new MutationObserver(() => {
        observer.disconnect();
        resolve();
      });
      observer.observe(gridRoot, { childList: true, subtree: true });
    });
    originalCell.remove();
    currentCell = replacementCell;
    gridRoot.append(replacementCell);
    await mutationDelivered;

    expect(document.activeElement).toBe(otherControl);
    dialogFocus.dispose();
  });

  it("does not steal focus when the user moves elsewhere during a reprojection gap", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    const otherControl = document.createElement("button");
    document.body.append(originalCell, otherControl);
    let currentCell = originalCell;
    let rowExists = true;
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    originalCell.remove();
    rowExists = false;
    emit("renderComplete");
    otherControl.focus();

    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    expect(document.activeElement).toBe(otherControl);
    dialogFocus.dispose();
  });

  it("cancels a stale lease when a newer dialog captures focus", () => {
    const firstCell = document.createElement("button");
    const secondCell = document.createElement("button");
    document.body.append(firstCell, secondCell);
    let currentCell = secondCell;
    const { grid } = createGridHarness(() => currentCell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    const stale = dialogFocus.capture({
      element: firstCell,
      rowKey: "row-7",
      field: "payload",
    });
    const current = dialogFocus.capture({
      element: secondCell,
      rowKey: "row-7",
      field: "payload",
    });
    stale.restore();
    expect(document.activeElement).toBe(document.body);

    current.restore();
    expect(document.activeElement).toBe(secondCell);
    dialogFocus.dispose();
  });

  it.each([
    ["workspace", { workspaceId: "workspace-2", sessionEpoch: 7, tableId: "items" }],
    ["session", { workspaceId: "workspace-1", sessionEpoch: 8, tableId: "items" }],
    ["table", { workspaceId: "workspace-1", sessionEpoch: 7, tableId: "archive" }],
  ])("cancels a pending lease when the %s scope changes", (_label, nextScope) => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    let scope = { workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" };
    let scopeChanged: (() => void) | null = null;
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => scope,
      subscribeScope: (listener) => {
        scopeChanged = listener;
        return () => { scopeChanged = null; };
      },
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    originalCell.remove();
    rowExists = false;
    emit("renderComplete");

    scope = nextScope;
    (scopeChanged as (() => void) | null)?.();

    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    expect(document.activeElement).toBe(document.body);
    dialogFocus.dispose();
  });

  it("fails closed without warnings when a connected target row is absent", () => {
    const staleCell = document.createElement("button");
    document.body.append(staleCell);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const handlers = new Map<string, () => void>();
    let targetRowExists = false;
    const grid = {
      getRows: () => targetRowExists
        ? [{
            getIndex: () => "row-7",
            getCell: () => ({ getElement: () => staleCell }),
          }]
        : [{ getIndex: () => "another-row", getCell: vi.fn() }],
      on: (event: string, handler: () => void) => handlers.set(event, handler),
      off: (event: string) => handlers.delete(event),
    };
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: staleCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    targetRowExists = true;
    handlers.get("renderComplete")?.();

    expect(document.activeElement).toBe(document.body);
    expect(warn).not.toHaveBeenCalled();
    dialogFocus.dispose();
    warn.mockRestore();
  });

  it("does not restore after the application window loses focus", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    document.body.append(originalCell);
    let currentCell = originalCell;
    let rowExists = true;
    const { grid, emit } = createGridHarness(() => currentCell, () => rowExists);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });

    dialogFocus.capture({
      element: originalCell,
      rowKey: "row-7",
      field: "payload",
    }).restore();
    originalCell.remove();
    rowExists = false;
    emit("renderComplete");
    window.dispatchEvent(new Event("blur"));

    document.body.append(replacementCell);
    currentCell = replacementCell;
    rowExists = true;
    emit("renderComplete");

    expect(document.activeElement).toBe(document.body);
    dialogFocus.dispose();
  });

  it("does not restore when the application window loses focus before release", () => {
    const cell = document.createElement("button");
    document.body.append(cell);
    const { grid } = createGridHarness(() => cell);
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => grid,
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });
    const lease = dialogFocus.capture({
      element: cell,
      rowKey: "row-7",
      field: "payload",
    });

    window.dispatchEvent(new Event("blur"));
    lease.restore();

    expect(document.activeElement).toBe(document.body);
    dialogFocus.dispose();
  });

  it("releases each focus lease at most once", () => {
    const cell = document.createElement("button");
    document.body.append(cell);
    const on = vi.fn();
    const dialogFocus = createStructuredDialogFocus({
      getGrid: () => ({
        getRows: () => [{
          getIndex: () => "row-7",
          getCell: () => ({ getElement: () => cell }),
        }],
        on,
        off: vi.fn(),
      }),
      getScope: () => ({ workspaceId: "workspace-1", sessionEpoch: 7, tableId: "items" }),
      subscribeScope: () => () => undefined,
    });
    const lease = dialogFocus.capture({
      element: cell,
      rowKey: "row-7",
      field: "payload",
    });

    lease.restore();
    lease.restore();

    expect(on).toHaveBeenCalledTimes(1);
    dialogFocus.dispose();
  });
});
