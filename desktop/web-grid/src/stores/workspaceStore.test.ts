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
    s.setOpened([{ collection: "users", metadata: {} }], { users: "Users" });
    expect(s.phase).toBe("opened");
    expect(s.collections).toHaveLength(1);
  });

  it("stores display names separately from physical collection keys", () => {
    const s = useWorkspaceStore();
    s.setOpened(
      [{ collection: "vt_t_01", metadata: {} }],
      { vt_t_01: "客户清单" },
    );
    expect(s.displayNames).toEqual({ vt_t_01: "客户清单" });
    expect(s.collections[0].collection).toBe("vt_t_01");
    expect(s.collections[0]).not.toHaveProperty("displayName");
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
    s.setOpened([{ collection: "x", metadata: {} }], { x: "X" });
    s.selectTable("x");
    s.clear();
    expect(s.phase).toBe("idle");
    expect(s.collections).toEqual([]);
    expect(s.displayNames).toEqual({});
    expect(s.currentTable).toBeNull();
  });

  it("keeps the catalog array reference when an identical catalog is rewritten", () => {
    const s = useWorkspaceStore();
    s.setOpened(
      [{ collection: "orders", metadata: {} }, { collection: "v_recent", metadata: { kind: "view" } }],
      { orders: "订单", v_recent: "近期" },
    );
    const reference = s.collections;
    const names = s.displayNames;
    s.setCollections(
      [{ collection: "orders", metadata: {} }, { collection: "v_recent", metadata: { kind: "view" } }],
      { orders: "订单", v_recent: "近期" },
    );
    expect(s.collections).toBe(reference);
    expect(s.displayNames).toBe(names);
  });

  it("treats absent metadata and an empty metadata object as the same catalog", () => {
    const s = useWorkspaceStore();
    s.setCollections([{ collection: "orders" }], {});
    const reference = s.collections;
    s.setCollections([{ collection: "orders", metadata: {} }], {});
    expect(s.collections).toBe(reference);
  });

  it("replaces the catalog reference when content actually changes", () => {
    const s = useWorkspaceStore();
    s.setCollections([{ collection: "orders" }], { orders: "订单" });
    const reference = s.collections;
    s.setCollections(
      [{ collection: "orders" }, { collection: "users" }],
      { orders: "订单", users: "用户" },
    );
    expect(s.collections).not.toBe(reference);
    expect(s.collections.map((item) => item.collection)).toEqual(["orders", "users"]);
    expect(s.displayNames).toEqual({ orders: "订单", users: "用户" });
  });

  it("keeps phase transitions on an equal-content reopen without replacing the catalog", () => {
    const s = useWorkspaceStore();
    s.setOpened([{ collection: "orders" }], { orders: "订单" });
    const reference = s.collections;
    s.setFailed("transient");
    s.setOpened([{ collection: "orders", metadata: {} }], { orders: "订单" });
    expect(s.phase).toBe("opened");
    expect(s.lastError).toBeNull();
    expect(s.collections).toBe(reference);
  });
});
