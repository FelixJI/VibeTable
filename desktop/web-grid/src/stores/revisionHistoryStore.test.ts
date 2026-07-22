import { beforeEach, describe, expect, it } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useRevisionHistoryStore } from "./revisionHistoryStore";
import type { HistoryChangeSet, HistoryPage, RestorePreview } from "@/contracts";

const changeSet = (revision: string): HistoryChangeSet => ({
  rootRevisionId: revision,
  activityId: `activity-${revision}`,
  itemId: "42",
  recordLabel: "客户 A",
  action: "update",
  timestamp: "2026-07-22T08:00:00Z",
  actor: { userId: "u1", displayName: "测试用户" },
  scalarChanges: [{ field: "status", before: "new", after: "done" }],
  relationChanges: [],
});

const page = (sets: readonly HistoryChangeSet[], total = sets.length): HistoryPage => ({
  collection: "orders",
  scope: "table",
  changeSets: sets,
  total,
  hasMore: sets.length < total,
  capabilityHash: "cap-1",
  schemaRevision: "schema-1",
});

describe("revisionHistoryStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("keeps grid selection separate from the open audit scope", () => {
    const store = useRevisionHistoryStore();
    store.setSelection({ scope: "cell", itemId: "42", field: "status" });
    expect(store.panelOpen).toBe(false);

    store.open({ scope: "cell", itemId: "42", field: "status" });
    expect(store.panelOpen).toBe(true);
    expect(store.scope).toBe("cell");
    expect(store.itemId).toBe("42");
    expect(store.field).toBe("status");
  });

  it("replaces and appends server pages while tracking hasMore", () => {
    const store = useRevisionHistoryStore();
    store.beginLoad();
    store.receivePage(page([changeSet("r2")], 2));
    expect(store.changeSets.map((item) => item.rootRevisionId)).toEqual(["r2"]);
    expect(store.hasMore).toBe(true);
    expect(store.offset).toBe(1);

    store.beginLoad(true);
    store.receivePage({ ...page([changeSet("r1")], 2), hasMore: false }, true);
    expect(store.changeSets.map((item) => item.rootRevisionId)).toEqual(["r2", "r1"]);
    expect(store.hasMore).toBe(false);
    expect(store.phase).toBe("ready");
  });

  it("distinguishes empty, unavailable, and failed states", () => {
    const store = useRevisionHistoryStore();
    store.receivePage(page([]));
    expect(store.phase).toBe("empty");
    store.failLoad("capability disabled", "history_not_allowed");
    expect(store.phase).toBe("unavailable");
    store.failLoad("archive disabled", "archive_not_supported");
    expect(store.phase).toBe("unavailable");
    store.failLoad("network failed", "transport_error");
    expect(store.phase).toBe("failed");
  });

  it("requires a server preview before apply and records completion without touching undo", () => {
    const store = useRevisionHistoryStore();
    store.beginPreview({ itemId: "42", revisionId: "r1", field: "status" });
    expect(store.restorePhase).toBe("previewing");
    expect(store.canApply).toBe(false);

    const preview: RestorePreview = {
      collection: "orders",
      itemId: "42",
      scope: "cell",
      field: "status",
      targetRevision: "r1",
      currentHash: "hash",
      schemaRevision: "schema-1",
      scalarChanges: [{ field: "status", before: "done", after: "new" }],
      relationChanges: [],
      diagnostics: [],
      canApply: true,
      restorableFields: ["status"],
      token: "restore-token",
      expiresAt: "2026-07-22T09:00:00Z",
    };
    store.receivePreview(preview);
    expect(store.canApply).toBe(true);
    store.beginApply();
    expect(store.restorePhase).toBe("applying");
    store.completeRestore({
      collection: "orders",
      itemId: "42",
      restoredToRevision: "r1",
      newRevisionId: "r3",
      item: { id: 42, status: "new" },
    });
    expect(store.appliedSequence).toBe(1);
    expect(store.restorePhase).toBe("idle");
    expect(store.lastApplied?.newRevisionId).toBe("r3");
  });
});
