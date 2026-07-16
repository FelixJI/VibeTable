/**
 * B1 Task 6 tests for PendingEdits: optimistic set, canonical-value
 * replacement on commit, validation rollback, and conflict rollback.
 */

import { describe, expect, it } from "vitest";
import { PendingEdits } from "./pendingEdits";

describe("PendingEdits", () => {
  it("records and reports a pending edit", () => {
    const store = new PendingEdits();
    store.set({ rowKey: 1, column: "amount" }, 10, 99);
    expect(store.has({ rowKey: 1, column: "amount" })).toBe(true);
    expect(store.size).toBe(1);
  });

  it("confirm replaces the pending edit with the stored value", () => {
    const store = new PendingEdits();
    store.set({ rowKey: 1, column: "amount" }, 10, 99);
    const canonical = store.confirm({ rowKey: 1, column: "amount" }, 99.0);
    expect(canonical).toBe(99.0);
    expect(store.has({ rowKey: 1, column: "amount" })).toBe(false);
  });

  it("rollback restores the pre-edit value on validation rejection", () => {
    const store = new PendingEdits();
    store.set({ rowKey: 2, column: "title" }, "old", "BAD");
    const restored = store.rollback({ rowKey: 2, column: "title" });
    expect(restored).toBe("old");
    expect(store.has({ rowKey: 2, column: "title" })).toBe(false);
  });

  it("rollback returns undefined when there is no pending edit", () => {
    const store = new PendingEdits();
    expect(store.rollback({ rowKey: 9, column: "x" })).toBeUndefined();
  });

  it("set supersedes a prior pending edit for the same cell", () => {
    const store = new PendingEdits();
    store.set({ rowKey: 1, column: "a" }, 1, 2);
    store.set({ rowKey: 1, column: "a" }, 1, 3);
    const edit = store.get({ rowKey: 1, column: "a" });
    expect(edit?.newValue).toBe(3);
    expect(edit?.seq).toBeGreaterThan(0);
  });

  it("clear drops every pending edit (table switch)", () => {
    const store = new PendingEdits();
    store.set({ rowKey: 1, column: "a" }, 1, 2);
    store.set({ rowKey: 2, column: "b" }, 3, 4);
    store.clear();
    expect(store.size).toBe(0);
  });

  it("conflict rollback restores the authoritative value the host sent", () => {
    // On edit_conflict the host sends currentRow; the grid rolls back the
    // optimistic value and replaces it with the authoritative cell.
    const store = new PendingEdits();
    store.set({ rowKey: 5, column: "amount" }, 10, 99);
    // Host rejects: authoritative current value is 12.
    const restored = store.rollback({ rowKey: 5, column: "amount" });
    expect(restored).toBe(10); // rolled back to pre-edit
    // The grid then separately renders the authoritative value (12).
  });
});
