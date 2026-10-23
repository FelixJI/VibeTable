import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useHistoryStore } from "./historyStore";

describe("historyStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts empty", () => {
    const s = useHistoryStore();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(false);
  });

  it("push makes undo available", () => {
    const s = useHistoryStore();
    s.push({ kind: "updateCell", label: "edit", undo: vi.fn(), redo: vi.fn() } as never);
    expect(s.canUndo).toBe(true);
    expect(s.canRedo).toBe(false);
  });

  it("undo calls entry.undo and moves to redo stack", async () => {
    const s = useHistoryStore();
    const undo = vi.fn().mockResolvedValue(undefined);
    const redo = vi.fn().mockResolvedValue(undefined);
    s.push({ kind: "updateCell", label: "edit", undo, redo } as never);
    await s.undo();
    expect(undo).toHaveBeenCalledOnce();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(true);
  });

  it("redo calls entry.redo and moves back to undo stack", async () => {
    const s = useHistoryStore();
    const undo = vi.fn().mockResolvedValue(undefined);
    const redo = vi.fn().mockResolvedValue(undefined);
    s.push({ kind: "updateCell", label: "edit", undo, redo } as never);
    await s.undo();
    await s.redo();
    expect(redo).toHaveBeenCalledOnce();
    expect(s.canUndo).toBe(true);
    expect(s.canRedo).toBe(false);
  });

  it("clear empties both stacks", () => {
    const s = useHistoryStore();
    s.push({ kind: "updateCell", label: "x", undo: vi.fn(), redo: vi.fn() } as never);
    s.clear();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(false);
  });

  it("stack caps at 50 entries (FIFO)", () => {
    const s = useHistoryStore();
    for (let i = 0; i < 55; i++) {
      s.push({ kind: "updateCell", label: `e${i}`, undo: vi.fn(), redo: vi.fn() } as never);
    }
    // Internal stack length capped; verify via undo behavior on oldest.
    expect(s.undoStackSize).toBe(50);
  });
});

it("new action during pending undo invalidates that future redo", async () => {
  setActivePinia(createPinia());
  const store = useHistoryStore();
  let resolve!: () => void;
  store.push({ kind: "updateCell", label: "edit", undo: () => new Promise<void>((r) => { resolve = r; }), redo: vi.fn() } as never);
  const undo = store.undo();
  store.beginAction();
  resolve(); await undo;
  expect(store.canRedo).toBe(false);
});

it("late confirmation can preserve redo created after submission", async () => {
  setActivePinia(createPinia());
  const store = useHistoryStore();
  store.push({ kind: "updateCell", label: "edit", undo: vi.fn(), redo: vi.fn() } as never);
  store.beginAction();
  await store.undo();
  store.push({ kind: "updateCell", label: "late", undo: vi.fn(), redo: vi.fn() } as never, true);
  expect(store.canRedo).toBe(true);
  expect(store.undoStackSize).toBe(1);
});

it("a rejected retired history operation does not put its error into the new generation", async () => {
  setActivePinia(createPinia());
  const store = useHistoryStore();
  let reject!: (error: Error) => void;
  store.push({ kind: "updateCell", label: "edit", undo: () => new Promise<void>((_, r) => { reject = r; }), redo: vi.fn() } as never);
  const undo = store.undo(); store.clear(); reject(new Error("old error")); await undo;
  expect(store.lastError).toBeNull(); expect(store.canUndo).toBe(false);
});

it("confirmed entries use authoritative commit order despite callback arrival order", () => {
  setActivePinia(createPinia());
  const store = useHistoryStore();
  store.push({ kind: "updateCell", label: "later", dataRevision: 3, undo: vi.fn() } as never, true);
  store.push({ kind: "updateCell", label: "earlier", dataRevision: 2, undo: vi.fn() } as never, true);
  expect(store.undoStack.map((entry) => entry.label)).toEqual(["earlier", "later"]);
});
