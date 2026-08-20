import { beforeEach, describe, expect, it, vi } from "vitest";

import { createStructuredDialogFocus } from "./dialogFocus";

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
  beforeEach(() => {
    document.body.innerHTML = "";
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

  it("does not steal focus after the user moves to another control", () => {
    const originalCell = document.createElement("button");
    const replacementCell = document.createElement("button");
    const otherControl = document.createElement("button");
    document.body.append(originalCell, otherControl);
    let currentCell = originalCell;
    const { grid, emit } = createGridHarness(() => currentCell);
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

    originalCell.remove();
    document.body.append(replacementCell);
    currentCell = replacementCell;
    emit("renderComplete");

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
