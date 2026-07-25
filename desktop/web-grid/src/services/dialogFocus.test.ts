import { beforeEach, describe, expect, it, vi } from "vitest";
import { restoreStructuredDialogFocus } from "./dialogFocus";

describe("restoreStructuredDialogFocus", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("focuses the original trigger while it is still connected", () => {
    const trigger = document.createElement("button");
    const fallback = document.createElement("button");
    document.body.append(trigger, fallback);
    const getRow = vi.fn(() => ({
      getCell: () => ({ getElement: () => fallback }),
    }));

    expect(restoreStructuredDialogFocus(
      { getRow },
      { element: trigger, rowKey: "row-1", field: "payload" },
    )).toBe(true);
    expect(document.activeElement).toBe(trigger);
  });

  it("resolves the current Tabulator cell when the original node was replaced", () => {
    const detachedTrigger = document.createElement("button");
    const currentCell = document.createElement("button");
    document.body.append(currentCell);
    const getCell = vi.fn(() => ({ getElement: () => currentCell }));
    const getRow = vi.fn(() => ({ getCell }));

    expect(restoreStructuredDialogFocus(
      { getRow },
      { element: detachedTrigger, rowKey: "row-7", field: "payload" },
    )).toBe(true);
    expect(getRow).toHaveBeenCalledWith("row-7");
    expect(getCell).toHaveBeenCalledWith("payload");
    expect(document.activeElement).toBe(currentCell);
  });
});
