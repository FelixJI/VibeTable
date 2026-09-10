import assert from "node:assert/strict";
import test from "node:test";
import { selectSeededReplicaConflict, requireResolvedReplicaConflict }
  from "./directory_replica_conflict_ui.mjs";

const state = { tableId: "table-seed", tableName: "Seed" };
const item = { kind: "table", itemId: state.tableId, path: state.tableName, state: "pending" };
function inspected(id, items = [item], status = "pending") {
  return { conflictId: id, items: structuredClone(items), state: status };
}
function listed(...ids) {
  return { conflicts: ids.map((conflictId) => ({ conflictId, state: "pending" })), nextCursor: null };
}

test("selects the seeded conflict after an unrelated first row and restores its UI selection", async () => {
  const calls = [];
  const result = await selectSeededReplicaConflict(listed("unrelated", "seed", "last"), state, async (id) => {
    calls.push(id);
    return inspected(id, id === "seed" ? [item] : [{ ...item, itemId: "another" }]);
  });
  assert.equal(result.conflictId, "seed");
  assert.deepEqual(calls, ["unrelated", "seed", "last", "seed"]);
});

test("does not guess when two conflicts contain the same seeded table", async () => {
  await assert.rejects(selectSeededReplicaConflict(listed("a", "b"), state,
    async (id) => inspected(id)), /found 2/);
});

test("same table name with another stable identity cannot match", async () => {
  await assert.rejects(selectSeededReplicaConflict(listed("a"), state,
    async (id) => inspected(id, [{ ...item, itemId: "other" }])), /found 0/);
});

test("truncated lists cannot establish uniqueness", async () => {
  await assert.rejects(selectSeededReplicaConflict({ ...listed("a"), nextCursor: "more" }, state,
    () => { throw new Error("must not inspect"); }), /complete public conflict list/);
});

test("inspection cannot switch the selected public identity", async () => {
  await assert.rejects(selectSeededReplicaConflict(listed("a"), state,
    async () => inspected("b")), /different identity/);
});

test("a duplicate public conflict identity fails closed", async () => {
  await assert.rejects(selectSeededReplicaConflict(listed("a", "a"), state,
    async (id) => inspected(id)), /duplicated/);
});

test("already resolved conflicts are not candidates", async () => {
  const list = listed("old", "new");
  list.conflicts[0].state = "ready";
  const calls = [];
  await selectSeededReplicaConflict(list, state, async (id) => { calls.push(id); return inspected(id); });
  assert.deepEqual(calls, ["new", "new"]);
});

for (const change of ["extra-choice", "identity", "resolved", "removed"]) {
  test(`reselection rejects a changed seeded conflict: ${change}`, async () => {
    let calls = 0;
    await assert.rejects(selectSeededReplicaConflict(listed("seed"), state, async (id) => {
      const result = inspected(id);
      if (++calls === 1) return result;
      if (change === "extra-choice") result.items.push({ ...item, itemId: "extra" });
      if (change === "identity") result.conflictId = "other";
      if (change === "resolved") result.state = "ready";
      if (change === "removed") result.items = [];
      return result;
    }), /changed or contains unexpected/);
  });
}

test("normal restart requires both public list and exact inspection to retain applied state", () => {
  const list = listed("seed");
  list.conflicts[0].state = "ready";
  const detail = inspected("seed", [{ ...item, state: "ready" }], "ready");
  assert.doesNotThrow(() => requireResolvedReplicaConflict(list, detail, "seed", state.tableId));
  for (const changed of [
    { list: listed("seed"), detail },
    { list, detail: inspected("seed") },
    { list, detail: { ...detail, conflictId: "other" } },
    { list, detail: { ...detail, items: [{ ...item, state: "ready", itemId: "other" }] } },
    { list: { ...list, nextCursor: "more" }, detail },
  ]) assert.throws(() => requireResolvedReplicaConflict(changed.list, changed.detail,
    "seed", state.tableId), /did not survive/);
});
