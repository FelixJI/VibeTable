import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
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


// Exercise the actual scenario startup and existing UI navigation helper without
// starting the packaged Host. The saved fork-left screenshot is an already-open
// workspace on Home: waiting for Home does not navigate to Workspace Center.
const runnerSource = readFileSync(new URL("./webview_product_scenarios.mjs", import.meta.url), "utf8");
const scenarioStartup = runnerSource.slice(runnerSource.indexOf("async function scenario24("),
  runnerSource.indexOf("const scenarios ="));
const centerNavigation = runnerSource.slice(
  runnerSource.indexOf("async function openWorkspaceCenterFromSwitcher("),
  runnerSource.indexOf("async function switchWorkspaceByName("));

for (const stage of ["seed", "fork-left", "fork-right", "resolve", "verify-resolved"]) {
  test(`scenario ${stage} enters Workspace Center through UI even after last-workspace startup`, async () => {
    let activeWorkspace = !["seed", "fork-right"].includes(stage);
    let centerVisible = !activeWorkspace;
    let menuVisible = false;
    let reachedAction = false;
    const clicks = [];
    const nextAction = () => {
      assert.equal(centerVisible, true);
      reachedAction = true;
      throw new Error("scenario reached its next workspace action");
    };
    const page = {
      getByTestId(id) {
        if (id === "nav-home") return {
          async waitFor() {},
          async click() { clicks.push("home"); }, // Home alone keeps the active workspace open.
        };
        if (id === "workspace-center") return {
          async waitFor(options) {
            assert.ok(options.timeout <= 60_000);
            if (!centerVisible) throw new Error("workspace-center hidden while Home has an active workspace");
          },
          getByRole(_role, options) {
            if (options.name.test("关闭当前工作区")) return {
              async isVisible() { return activeWorkspace; },
              async click() {
                assert.equal(activeWorkspace, true);
                clicks.push("workspace.close");
                activeWorkspace = false;
              },
            };
            return { click() {
              // The real UI controller returns to Home without an open request for this card.
              if (activeWorkspace) throw new Error("current workspace card does not emit workspace.open");
              nextAction();
            } };
          },
        };
        if (id === "workspace-switcher") return {
          locator(selector) {
            assert.equal(selector, ".switcher-trigger");
            return { async click() { clicks.push("switcher"); menuVisible = true; } };
          },
        };
        if (["workspace-create", "workspace-connect"].includes(id)) return { click: nextAction };
        throw new Error(`unexpected UI control ${id}`);
      },
      locator(selector) {
        assert.equal(selector, ".n-dropdown-option");
        return { last() { return { async click() {
          assert.equal(menuVisible, true);
          clicks.push("workspace-center");
          centerVisible = true;
        } }; } };
      },
    };
    const scenario = runInNewContext(`${centerNavigation}\n${scenarioStartup}\nscenario24`, {
      fs: { async readFile() { return JSON.stringify({ workspaceName: "Seed", workspaceId: "seed-id" }); } },
      async replicaUiMethod(_page, _recorder, method, action) {
        assert.equal(method, "workspace.close");
        await action();
        return { request: { wire: { workspaceId: "seed-id", sessionEpoch: 7 } },
          result: { state: "closed", workspaceId: null, sessionEpoch: 7 } };
      },
      async activateDirectoryReplicaWorkspace(_page, options) { await options.activate(); },
    });
    await assert.rejects(scenario(page, { check(_message, condition) { assert.equal(condition, true); } }, null,
      { replicaStage: stage, replicaState: "saved-state" }),
      /scenario reached its next workspace action/);
    assert.equal(reachedAction, true);
    assert.deepEqual(clicks, ["switcher", "workspace-center",
      ...(!["seed", "fork-right"].includes(stage) ? ["workspace.close"] : [])]);
  });
}
