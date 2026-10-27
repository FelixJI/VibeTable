import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runPluginModels, installModelCatalogObserverInPage, readModelCatalogInPage,
  releaseModelCatalogObserverInPage, waitForModelCatalog } from "./plugin_models.mjs";

function seedCatalogHarness(tables) {
  const calls = [];
  const reachedCatalogAssertion = new Error("seed catalog checkpoint");
  const listeners = new Set();
  const session = { workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 2 };
  const browserWindow = {
    chrome: { webview: { addEventListener: (_type, listener) => listeners.add(listener), removeEventListener: (_type, listener) => listeners.delete(listener) } },
    __vibetableE2EBridgeDiagnostics: { workspaceSession: session },
    __vibetableE2EWorkspaceWirePort: { request: async request => {
      calls.push(request);
      throw Object.assign(new Error("Workspace v2 method is not renderer-public: schema.list."), { code: "workspace.method_not_public" });
    } },
  };
  const page = {
    evaluate: async (operation, parameters) => {
      const previous = globalThis.window;
      globalThis.window = browserWindow;
      try { return await operation(parameters); }
      finally {
        if (previous === undefined) delete globalThis.window;
        else globalThis.window = previous;
      }
    },
  };
  const recorder = { check: (name, passed, detail) => {
    assert.equal(name, "model qualification begins with an empty synthetic workspace");
    assert.deepEqual(detail.original.map(table => table.tableId), tables);
    assert.equal(passed, tables.length === 0);
    throw reachedCatalogAssertion;
  } };
  const helpers = {
    waitForShell: async () => ({ type: "database.opened", payload: { projectKey: "local:11111111111141118111111111111111", tables, views: [], displayNames: Object.fromEntries((tables ?? []).map(id => [id, `Display ${id}`])) } }),
    // Official PR537 CI evidence: legacy schema.list receives UNKNOWN_TYPE,
    // not a successful { tables } payload. Never fake support for this route.
    rawBridgeRequest: async (_page, method) => {
      calls.push({ legacy: method });
      return { type: "operation.failed", payload: { code: "UNKNOWN_TYPE", message: "Unknown message type: schema.list" } };
    },
  };
  const emit = message => page.evaluate(value => { for (const listener of listeners) listener({ data: value }); }, message);
  page.waitForFunction = async (condition, parameters, options) => {
    assert.equal(options.timeout, 30000);
    if (!await page.evaluate(condition, parameters)) throw new Error("catalog notification deadline");
  };
  return { calls, listeners, browserWindow, page, emit, initial: { tables, views: [], displayNames: Object.fromEntries((tables ?? []).map(id => [id, `Display ${id}`])), projectKey: "local:11111111111141118111111111111111" }, reachedCatalogAssertion, run: () => runPluginModels(page, recorder, {}, helpers) };
}

test("model seed uses authoritative database.opened without inventing a catalog RPC and cleans its observer", async () => {
  const scene = seedCatalogHarness([]);
  await assert.rejects(scene.run, error => error === scene.reachedCatalogAssertion);
  assert.deepEqual(scene.calls, []);
  assert.equal(scene.listeners.size, 0);
});

test("model seed retains actual nonempty catalog evidence", async () => {
  const scene = seedCatalogHarness(["tbl_existing"]);
  await assert.rejects(scene.run, error => error === scene.reachedCatalogAssertion);
  assert.deepEqual(scene.calls, []);
  assert.equal(scene.listeners.size, 0);
});

test("model seed rejects a response without the authoritative tables array", async () => {
  const scene = seedCatalogHarness(undefined);
  await assert.rejects(scene.run, /invalid Host table catalog/);
  assert.deepEqual(scene.calls, []);
  assert.equal(scene.listeners.size, 0);
});

const modelBindings = { people: { tableId: "tbl_people" }, tickets: { tableId: "tbl_tickets" }, assets: { tableId: "tbl_assets" } };
function catalogNotification(ids = ["tbl_people", "tbl_tickets", "tbl_assets"]) {
  return { type: "database.collectionsChanged", requestId: null,
    payload: { tables: ids, views: [], displayNames: Object.fromEntries(ids.map(id => [id, `Native ${id}`])) } };
}

async function observingCatalog(t) {
  const scene = seedCatalogHarness([]);
  await scene.page.evaluate(installModelCatalogObserverInPage, scene.initial);
  t.after(async () => {
    await scene.page.evaluate(releaseModelCatalogObserverInPage);
    assert.equal(scene.listeners.size, 0);
    assert.deepEqual(scene.calls, []);
  });
  return scene;
}

test("catalog wait accepts fresh complete Host projection, preserving order and display names", async t => {
  const scene = await observingCatalog(t);
  await scene.emit(JSON.stringify(catalogNotification()));
  const observed = await waitForModelCatalog(scene.page, modelBindings, 0);
  assert.deepEqual(observed.tables.map(table => table.tableId), ["tbl_people", "tbl_tickets", "tbl_assets"]);
  assert.equal(observed.tables[0].displayName, "Native tbl_people");
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, observed.revision), /catalog notification deadline/);
  await scene.emit(catalogNotification());
  assert.equal((await waitForModelCatalog(scene.page, modelBindings, observed.revision)).revision, 2);
});

test("unknown, correlated and explicitly stale messages cannot satisfy a catalog wait", async t => {
  const scene = await observingCatalog(t);
  await scene.emit({ ...catalogNotification(), type: "schema.list" });
  await scene.emit({ ...catalogNotification(), requestId: "unrelated-create-reply" });
  await scene.emit({ ...catalogNotification(), wire: { ...scene.browserWindow.__vibetableE2EBridgeDiagnostics.workspaceSession, sessionEpoch: 1 } });
  await scene.emit({ type: "operation.failed", payload: { operation: "other", code: "unrelated" } });
  assert.equal((await scene.page.evaluate(readModelCatalogInPage)).revision, 0);
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /catalog notification deadline/);
});

test("missing, incomplete and extra-table notifications cannot pass exact model catalog checks", async t => {
  const scene = await observingCatalog(t);
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /catalog notification deadline/);
  await scene.emit(catalogNotification(["tbl_people"]));
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /catalog notification deadline/);
  await scene.emit(catalogNotification(["tbl_people", "tbl_tickets", "tbl_assets", "tbl_accidental"]));
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /catalog notification deadline/);
});

test("catalog refresh failure is reported rather than accepting an older correct projection", async t => {
  const scene = await observingCatalog(t);
  await scene.emit(catalogNotification());
  await scene.emit({ type: "operation.failed", payload: { operation: "plugin_model.catalog", code: "plugin_model.catalog_refresh_failed", message: "committed catalog refresh failed" } });
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /plugin_model\.catalog_refresh_failed/);
});

test("malformed Host catalog cannot be replaced by a silent empty projection", async t => {
  const scene = await observingCatalog(t);
  await scene.emit({ type: "database.collectionsChanged", payload: { tables: [], displayNames: {} } });
  await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /invalid Host table catalog/);
});

for (const change of ["workspace", "epoch"]) {
  test(`catalog observer permanently retires on ${change} transition`, async t => {
    const scene = await observingCatalog(t);
    const original = scene.browserWindow.__vibetableE2EBridgeDiagnostics.workspaceSession;
    const different = { ...original, ...(change === "epoch" ? { sessionEpoch: 3 } : { workspaceId: "22222222-2222-4222-8222-222222222222" }) };
    await scene.emit({ type: "workspace.v2.bootstrap", payload: { session: different } });
    await scene.emit({ type: "workspace.v2.bootstrap", payload: { session: original } });
    await scene.emit(catalogNotification());
    await assert.rejects(() => waitForModelCatalog(scene.page, modelBindings, 0), /model catalog session retired/);
  });
}

test("catalog reads reject a changed active epoch even without a captured bootstrap", async t => {
  const scene = await observingCatalog(t);
  scene.browserWindow.__vibetableE2EBridgeDiagnostics.workspaceSession = { ...scene.browserWindow.__vibetableE2EBridgeDiagnostics.workspaceSession, sessionEpoch: 4 };
  await assert.rejects(() => scene.page.evaluate(readModelCatalogInPage), /model catalog session retired/);
});

// This is a scene-assertion harness, not a plugin test Host or GUI evidence.
// It verifies that the cold-reopen acceptance cannot pass on stale identity,
// changed canonical bindings, lost user data, or broadened grants.
async function coldScene(t, fault = "") {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "vibetable-model-scene-"));
  t.after(async () => {
    for (const name of ["seed.json", "54-models-reopened.json"]) await fs.rm(path.join(dir, name), { force: true });
    await fs.rmdir(dir);
  });
  const seed = {
    workspaceId: "11111111-1111-4111-8111-111111111111", sessionEpoch: 2, projectKey: "local:synthetic",
    bindings: { assets: { tableId: "tbl_assets", fields: { name: "fld_name", checked: "fld_checked" } } },
    userData: { tableId: "tbl_assets", fieldId: "fld_private", physicalName: "private_value", value: "synthetic preserved value",
      recordId: "row_synthetic", renamedFieldId: "fld_name", renamedLabel: "User renamed",
      corePhysicalName: "core_name", coreValue: "synthetic asset" },
  };
  const statePath = path.join(dir, "seed.json");
  await fs.writeFile(statePath, JSON.stringify(seed));
  const reopened = { status: "completed", bindings: structuredClone(seed.bindings) };
  if (fault === "binding") reopened.bindings.assets.tableId = "tbl_replacement";
  const chain = { click: async () => {}, waitFor: async () => {}, filter: () => chain };
  const frame = {
    url: () => "https://example.plugins.vibetable.local/ui/index.html",
    locator: () => ({ innerText: async () => JSON.stringify(reopened) }),
    evaluate: async () => ({ result: { fields: [
      { fieldId: "id" }, { fieldId: "fld_name" }, { fieldId: "fld_checked" },
      ...(fault === "grant" ? [{ fieldId: "fld_private" }] : []),
    ] } }),
  };
  const page = { getByTestId: () => chain, frameLocator: () => ({ locator: () => chain }), frames: () => [frame] };
  const checks = [];
  const recorder = { check: (name, passed) => { checks.push(name); assert.ok(passed, name); } };
  const helpers = {
    activateRestartedWorkspace: async () => ({ switched: { result: {
      workspaceId: fault === "workspace" ? "22222222-2222-4222-8222-222222222222" : seed.workspaceId,
      sessionEpoch: fault === "epoch" ? seed.sessionEpoch : seed.sessionEpoch + 1,
    } } }),
    rawBridgeRequest: async (_page, method) => {
      if (method === "schema.getTable") return { payload: { fields: [
        { identity: { fieldId: "fld_name" }, displayName: fault === "rename" ? "lost rename" : seed.userData.renamedLabel },
        { identity: { fieldId: "fld_private" }, displayName: "User private" },
      ] } };
      assert.equal(method, "query.page");
      return { payload: { rows: [{ id: seed.userData.recordId, core_name: seed.userData.coreValue,
        private_value: fault === "value" ? "overwritten" : seed.userData.value }] } };
    },
  };
  const run = () => runPluginModels(page, recorder, { evidenceDir: dir }, helpers, statePath);
  return { dir, seed, checks, run };
}

test("model cold-reopen scene requires both persisted bindings and ungranted user data evidence", async t => {
  const fixture = await coldScene(t);
  const result = await fixture.run();
  assert.equal(result.workspaceId, fixture.seed.workspaceId);
  assert.equal(result.sessionEpoch, 3);
  assert.deepEqual(result.bindings, fixture.seed.bindings);
  assert.equal(fixture.checks.length, 4);
  const evidence = JSON.parse(await fs.readFile(path.join(fixture.dir, "54-models-reopened.json"), "utf8"));
  assert.equal(evidence.previousEpoch, 2);
  assert.equal(evidence.preserved.rows[0].private_value, fixture.seed.userData.value);
});

for (const fault of ["workspace", "epoch", "binding", "value", "rename", "grant"]) {
  test(`model cold-reopen scene rejects ${fault} regression`, async t => {
    const fixture = await coldScene(t, fault);
    await assert.rejects(fixture.run, assert.AssertionError);
    await assert.rejects(fs.access(path.join(fixture.dir, "54-models-reopened.json")), { code: "ENOENT" });
  });
}
