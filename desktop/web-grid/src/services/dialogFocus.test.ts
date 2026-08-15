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
    const getRows = vi.fn(() => [{
      getIndex: () => "row-1",
      getCell: () => ({ getElement: () => fallback }),
    }]);

    expect(restoreStructuredDialogFocus(
      { getRows },
      { element: trigger, rowKey: "row-1", field: "payload" },
    )).toBe(true);
    expect(document.activeElement).toBe(trigger);
  });

  it("resolves the current Tabulator cell from the enumerated rows", () => {
    const detachedTrigger = document.createElement("button");
    const currentCell = document.createElement("button");
    document.body.append(currentCell);
    const getCell = vi.fn(() => ({ getElement: () => currentCell }));
    const getRows = vi.fn(() => [
      { getIndex: () => "row-6", getCell: vi.fn() },
      { getIndex: () => "row-7", getCell },
    ]);

    expect(restoreStructuredDialogFocus(
      { getRows },
      { element: detachedTrigger, rowKey: "row-7", field: "payload" },
    )).toBe(true);
    expect(getRows).toHaveBeenCalledTimes(1);
    expect(getCell).toHaveBeenCalledWith("payload");
    expect(document.activeElement).toBe(currentCell);
  });

  it("fails closed without warnings when the row is not in the snapshot", () => {
    const detachedTrigger = document.createElement("button");
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const getRows = vi.fn(() => [{ getIndex: () => "row-6", getCell: vi.fn() }]);

    expect(restoreStructuredDialogFocus(
      { getRows },
      { element: detachedTrigger, rowKey: "row-9", field: "payload" },
    )).toBe(false);
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});
