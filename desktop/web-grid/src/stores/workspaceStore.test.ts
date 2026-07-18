import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useWorkspaceStore } from "./workspaceStore";

describe("workspaceStore", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("starts idle with empty collections", () => {
    const s = useWorkspaceStore();
    expect(s.phase).toBe("idle");
    expect(s.collections).toEqual([]);
    expect(s.currentTable).toBeNull();
  });

  it("beginOpen moves to opening phase", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    expect(s.phase).toBe("opening");
  });

  it("setOpened stores collections and moves to opened", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    s.setOpened([{ collection: "users", metadata: {} }]);
    expect(s.phase).toBe("opened");
    expect(s.collections).toHaveLength(1);
  });

  it("selectTable sets currentTable", () => {
    const s = useWorkspaceStore();
    s.selectTable("orders");
    expect(s.currentTable).toBe("orders");
  });

  it("setFailed records error and moves to failed", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    s.setFailed("boom");
    expect(s.phase).toBe("failed");
    expect(s.lastError).toBe("boom");
  });

  it("clear resets to idle", () => {
    const s = useWorkspaceStore();
    s.setOpened([{ collection: "x", metadata: {} }]);
    s.selectTable("x");
    s.clear();
    expect(s.phase).toBe("idle");
    expect(s.collections).toEqual([]);
    expect(s.currentTable).toBeNull();
  });
});
