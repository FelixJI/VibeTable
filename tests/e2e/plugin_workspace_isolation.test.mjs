import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

// Exercise the new acceptance orchestration, never substitute these fakes for
// the real S48 Host/package/workspace qualification.
const source = await fs.readFile(new URL("./plugin_applications.mjs", import.meta.url), "utf8");
const section = (start, end) => source.slice(source.indexOf(start), source.indexOf(end)).replace(/^export /u, "").trim();
const seedSource = section("export async function exerciseApplicationWorkspaceIsolation", "export async function verifyOtherWorkspaceAfterLifecycle");
const verifySource = section("export async function verifyOtherWorkspaceAfterLifecycle", "export async function runPluginApplications");
const A = "com.vibetable.e2e.application-a", B = "com.vibetable.e2e.application-b";
const buildRoot = fileURLToPath(new URL("../../build/task480", import.meta.url));

async function harness(options = {}) {
  await fs.mkdir(buildRoot, { recursive: true });
  const controlsDir = await fs.mkdtemp(path.join(buildRoot, "workspace-isolation-"));
  const readControl = path.join(controlsDir, "attachment-replacement-source.txt");
  await fs.writeFile(readControl, "original controls\n");
  const first = { workspaceId: "11111111-1111-4111-8111-111111111111", projectKey: "local:11111111111141118111111111111111", tableId: "table-a", name: "E2E Product Workspace" };
  const second = { workspaceId: "22222222-2222-4222-8222-222222222222", projectKey: "local:22222222222242228222222222222222", tableId: "table-b", name: "E2E Plugin Scope Isolation" };
  const installed = (workspace, id = A) => ({ pluginId: id, projectKey: workspace.projectKey, installationId: `install-${workspace.tableId}`, revision: 2, version: "1.0.0", packageHash: "sha256:" + "a".repeat(64) });
  const stores = new Map([[first, { entries: [installed(first), installed(first, B)], value: "plugin-a", rows: [{ id: "row-a", text: "synthetic original" }] }],
    [second, { entries: [], value: null, rows: [] }]]);
  let active = first, epoch = 1, serial = 0, capture = false, opened = false;
  const grants = new Map(), frames = [], checks = [], events = [], fileReads = [], released = [];
  const frame = () => {
    const workspace = active, token = `token-${++serial}`;
    const value = { detached: false, isDetached() { return this.detached; }, locator(selector) {
      assert.equal(selector, "#rows"); return { async innerText() { return JSON.stringify(stores.get(workspace).rows); } };
    }, async evaluate(callback, argument) {
      const previousCall = globalThis.call, previousWindow = globalThis.window;
      globalThis.window = { name: token };
      globalThis.call = async (method, params = {}, supplied = token) => {
        const deny = code => { throw Object.assign(new Error("safe failure"), { code }); };
        if (supplied !== token) return deny("plugin_source_denied");
        const store = stores.get(workspace);
        if (method === "context.read") return { projectKey: workspace.projectKey, collection: workspace.tableId };
        if (method === "storage.get") return options.leakPrivate && workspace === second ? "plugin-a" : store.value;
        if (method === "storage.set") { store.value = params.value; return { saved: true }; }
        if (method === "file.pickRead") { const grantId = `grant-${++serial}`; grants.set(grantId, { workspace, source: (await fs.readFile(readControl, "utf8")).trim() }); return { grantId }; }
        if (method === "file.read") {
          const grant = grants.get(params.grantId);
          if (!grant || grant.workspace !== workspace && !options.allowOldGrant) return deny("plugin_file_denied");
          const content = await fs.readFile(grant.source);
          const offset = grant.offset ?? 0;
          const bytes = options.emptyNonterminal && workspace === second ? Buffer.alloc(0)
            : content.subarray(offset, offset + (options.chunkSize ?? 256 * 1024));
          grant.offset = offset + bytes.length;
          const eof = !options.emptyNonterminal && bytes.length === 0;
          fileReads.push({ grantId: params.grantId, workspace, bytes: bytes.length, eof });
          return { eof, base64: bytes.toString("base64") };
        }
        if (method === "file.release") { released.push(params.grantId); grants.delete(params.grantId); return { released: true }; }
        if (method === "data.query") return deny("plugin_read_denied");
        throw new Error(`unhandled method ${method}`);
      };
      try { return await callback(argument); }
      finally { globalThis.call = previousCall; globalThis.window = previousWindow; }
    } };
    frames.push(value); return value;
  };
  const originalFrame = frame();
  const activate = workspace => { for (const item of frames) item.detached = true; active = workspace; epoch++; };
  const session = () => ({ workspaceId: active.workspaceId, sessionEpoch: epoch });
  const locator = { locator() { return this; }, first() { return this; }, async fill(name) { assert.equal(name, second.name); }, async waitFor() {}, async click() {} };
  const page = { async evaluate() { return session(); }, getByTestId(id) {
    if (id === "workspace-center") return { getByRole() { return { async waitFor() {}, async click() { assert.ok(capture, "bind activation capture before opening"); activate(second); opened = true; } }; } };
    return locator;
  } };
  const helpers = {
    async rawBridgeRequest(_page, method, params) {
      events.push([method, active.tableId]);
      if (method === "plugin.catalog.list") { assert.equal(params.projectKey, active.projectKey); return { payload: stores.get(active).entries }; }
      assert.equal(method, "query.page"); assert.equal(params.tableId, active.tableId, "queries must use the currently opened workspace table");
      return { payload: { rows: structuredClone(stores.get(active).rows) } };
    },
    async createSimpleTable() { assert.equal(active, second); assert.ok(opened); return { tableId: second.tableId, field: { physicalName: "text" } }; },
    async applyProductMutation(_page, tableId, operations) { assert.equal(active, second); assert.equal(tableId, second.tableId); stores.get(second).rows = [{ id: "row-b", ...operations[0].values }]; },
    async openWorkspaceCenterFromSwitcher() { originalFrame.detached = true; },
    async beginWritableWorkspaceBootstrapCapture(_page, minimum, method) { assert.equal(minimum, 1); assert.equal(method, "workspace.open"); capture = true; },
    async waitForCapturedBridgeMessage() { assert.ok(opened); return { payload: { session: session() } }; },
    async switchWorkspaceByName(_page, name, minimum) { assert.equal(minimum, epoch); activate(name === first.name ? first : second); return { payload: { session: session() } }; },
    async selectTable(_page, name) { assert.equal(name, active === first ? "Application Tasks" : "Other Application Tasks"); },
  };
  const installApplication = async () => { assert.equal(active, second); stores.get(second).entries = [installed(second)]; };
  const openApplication = async () => frame();
  const seed = new Function("fs", "path", "A", "installApplication", "openApplication", `return (${seedSource});`)(fs, path, A, installApplication, openApplication);
  const verify = new Function("A", "B", "openApplication", `return (${verifySource});`)(A, B, openApplication);
  const recorder = { check(name, passed) { assert.ok(passed, name); checks.push(name); } };
  return { first, second, stores, checks, events, fileReads, released, readControl, controlsDir,
    async seed() { return seed(page, recorder, { controlsDir }, helpers, first, originalFrame); },
    async verify(crossWorkspace) { stores.get(first).entries = [installed(first, B)]; return verify(page, recorder, helpers, { ...first, crossWorkspace }); },
    async cleanup() {
      for (const name of ["attachment-replacement-source.txt", "48-workspace-a-private.txt", "48-workspace-b-private.txt"]) await fs.unlink(path.join(controlsDir, name));
      assert.equal(path.dirname(path.resolve(controlsDir)), path.resolve(buildRoot));
      await fs.rmdir(controlsDir);
    },
  };
}

test("same-plugin workspace acceptance returns and persists the other authority for post-uninstall verification", async () => {
  const h = await harness();
  try {
    const seeded = await h.seed();
    assert.equal(seeded.crossWorkspace.workspaceId, h.second.workspaceId);
    assert.equal(seeded.sessionEpoch, 3);
    const reads = h.fileReads.filter(item => item.workspace === h.second);
    assert.deepEqual(reads.map(({ bytes, eof }) => ({ bytes, eof })), [
      { bytes: 16, eof: false }, { bytes: 0, eof: true },
    ]);
    assert.deepEqual(h.released, [reads[0].grantId]);
    assert.equal(h.stores.get(h.first).value, "plugin-a");
    assert.equal(h.stores.get(h.second).value, "workspace-b");
    assert.equal(await fs.readFile(h.readControl, "utf8"), "original controls\n");
    await h.verify(JSON.parse(JSON.stringify(seeded.crossWorkspace)));
    assert.ok(h.checks.some(name => name.includes("explicit uninstall and the second plugin")));
  } finally { await h.cleanup(); }
});

for (const [option, reason] of [["leakPrivate", "without old private settings"], ["allowOldGrant", "unread grant and table"]]) {
  test(`workspace acceptance rejects ${option} even when the old frame detached`, async () => {
    const h = await harness({ [option]: true });
    try { await assert.rejects(h.seed(), new RegExp(reason)); assert.equal(await fs.readFile(h.readControl, "utf8"), "original controls\n"); }
    finally { await h.cleanup(); }
  });
}

test("post-uninstall verification rejects a changed other-workspace installation revision", async () => {
  const h = await harness();
  try {
    const seeded = await h.seed();
    h.stores.get(h.second).entries[0].revision++;
    await assert.rejects(h.verify(seeded.crossWorkspace), /cannot change the other installation/u);
  } finally { await h.cleanup(); }
});

test("new workspace file acceptance consumes every content block before the empty EOF block", async () => {
  const h = await harness({ chunkSize: 4 });
  try {
    await h.seed();
    const reads = h.fileReads.filter(item => item.workspace === h.second);
    assert.deepEqual(reads.map(item => item.bytes), [4, 4, 4, 4, 0]);
    assert.deepEqual(reads.map(item => item.eof), [false, false, false, false, true]);
    assert.deepEqual(h.released, [reads[0].grantId]);
  } finally { await h.cleanup(); }
});

test("new workspace file acceptance rejects an empty nonterminal block and releases its grant", async () => {
  const h = await harness({ emptyNonterminal: true });
  try {
    await assert.rejects(h.seed(), /invalid workspace file block/u);
    const reads = h.fileReads.filter(item => item.workspace === h.second);
    assert.equal(reads.length, 1);
    assert.deepEqual(h.released, [reads[0].grantId]);
    assert.equal(await fs.readFile(h.readControl, "utf8"), "original controls\n");
  } finally { await h.cleanup(); }
});
