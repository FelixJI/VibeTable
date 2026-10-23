import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useWorkspaceStore } from "./workspaceStore";
import { useWorkspaceSessionStore } from "./workspaceSessionStore";
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

it("clearing history preserves uncertainty and only its own recovery query can confirm it", () => {
  setActivePinia(createPinia()); const store = useHistoryStore();
  const beforeUnknown = store.captureWriteRefresh(); store.markWriteUncertain();
  beforeUnknown(); expect(store.writeUncertain).toBe(true);
  const firstRecovery = store.captureWriteRefresh(); store.clear(); expect(store.writeUncertain).toBe(true);
  store.markWriteUncertain(); firstRecovery(); expect(store.writeUncertain).toBe(true);
  store.captureWriteRefresh()(); expect(store.writeUncertain).toBe(false);
  store.markWriteUncertain(); const retired = store.captureWriteRefresh(); store.retireWriteScope();
  store.markWriteUncertain(); retired(); expect(store.writeUncertain).toBe(true);
});

it("review7 authority retirement invalidates old uncertainty and recovery capabilities", () => {
  setActivePinia(createPinia()); const store = useHistoryStore(); const ws = useWorkspaceStore();
  ws.selectTable("a"); store.markWriteUncertain(); const oldMark = store.captureWriteUncertainty();
  const oldRead = store.captureWriteRefresh(); ws.selectTable("b"); store.markWriteUncertain();
  oldRead(); expect(store.writeUncertain).toBe(true);
  useWorkspaceSessionStore().sessionEpoch += 1; expect(store.writeUncertain).toBe(false);
  oldMark(); ws.selectTable("a"); expect(store.writeUncertain).toBe(false);
  store.markWriteUncertain(); oldRead(); expect(store.writeUncertain).toBe(true);
});

it.each(["clear", "undo", "redo"])("review12 zero submission preserves redo alongside later %s activity", async activity => {
  setActivePinia(createPinia()); const store = useHistoryStore();
  const entry = { kind: "updateCell", label: "old", undo: vi.fn(), redo: vi.fn() } as never;
  store.push(entry); await store.undo(); const restore = store.beginAction();
  if (activity === "clear") store.clear();
  else {
    store.push({ kind: "updateCell", label: "new", undo: vi.fn(), redo: vi.fn() } as never, true);
    await store.undo(); if (activity === "redo") await store.redo();
  }
  const before = [...store.redoStack]; restore(true); expect(store.redoStack).toEqual(activity === "clear" ? before : [entry, ...before]);
});

it("review12 late settlements cannot lower the invalidation watermark for a pending undo", async () => {
  setActivePinia(createPinia()); const store = useHistoryStore();
  let complete!: () => void;
  store.push({ kind: "updateCell", label: "old", undo: () => new Promise<void>(resolve => { complete = resolve; }), redo: vi.fn() } as never);
  const older = store.beginAction(); const undo = store.undo(); const newer = store.beginAction();
  newer(false); older(false); complete(); await undo;
  expect(store.canRedo).toBe(false); expect(store.redoStack).toHaveLength(0);
});

it("review12 hidden redo candidates retain the same 50 entry bound", async () => {
  setActivePinia(createPinia()); const store = useHistoryStore(); const settlements: ((notSubmitted: boolean) => void)[] = [];
  for (let i = 0; i < 55; i++) {
    store.push({ kind: "updateCell", label: `e${i}`, undo: vi.fn(), redo: vi.fn() } as never, true);
    await store.undo(); settlements.push(store.beginAction());
  }
  expect(store.redoStack).toHaveLength(0);
  settlements.forEach(settle => settle(true));
  expect(store.redoStack).toHaveLength(50); expect(store.redoStack[0]?.label).toBe("e5");
});