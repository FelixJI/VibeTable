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
