import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { JSDOM } from "../../desktop/web-grid/node_modules/jsdom/lib/api.js";

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
  let diagnosticsDropped = false;
  const page = { async evaluate() { return diagnosticsDropped ? null : session(); }, getByTestId(id) {
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
    dropDiagnostics: () => { diagnosticsDropped = true; },
    async verify(crossWorkspace) { stores.get(first).entries = [installed(first, B)]; return verify(page, recorder, helpers, { ...first, sessionEpoch: epoch, crossWorkspace }); },
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

test("post-uninstall verification relies on authoritative switch sessions when the diagnostics snapshot missed the resume bootstrap", async () => {
  const h = await harness();
  try {
    const seeded = await h.seed();
    h.dropDiagnostics();
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

// commitInstall renders the row/nav before commitPlan's convenience setEnabled
// lands. installGateHarness below answers the real selector with a jsdom
// toggle's actual matches() and explicit state transitions; it models only the
// foreground install chain, not background catalog refresh ordering.
const installSource = section("export async function installApplication", "export async function openApplication");
const navTestid = (id, actionId = "open-app") => `nav-plugin-app-${id}/${actionId}`;
const toggleSelectorLiteral = `'button[data-testid="plugin-toggle"].enabled:not([disabled])'`;

const installGateHarness = (source = installSource) => {
  const dom = new JSDOM('<button data-testid="plugin-toggle" class="toggle-button" type="button">整体禁用</button>');
  const toggle = dom.window.document.querySelector('button[data-testid="plugin-toggle"]');
  const waiters = new Set(), requested = [];
  let state = { enabled: false, busy: false }, rowVisible = false, rowClicked = false, navVisible = false;
  let settled = false, settleState = null, failure = null, disposed = false, written = null;
  const release = () => { for (const waiter of [...waiters]) if (waiter.ready()) { waiters.delete(waiter); waiter.resolve(); } };
  const waitFor = ready => ready() ? Promise.resolve() : new Promise(resolve => { waiters.add({ ready, resolve }); });
  const drain = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  const page = {
    locator(selector) {
      if (selector === "button.plugin-row") return { filter({ hasText }) {
        assert.equal(hasText, A);
        return { waitFor: () => waitFor(() => rowVisible), async click() { rowClicked = true; } };
      } };
      return { waitFor: () => waitFor(() => toggle.matches(selector)) };
    },
    getByTestId(id) {
      requested.push(id);
      if (id === "nav-plugins" || id === "plugin-install-package") return { async click() {} };
      if (id === "plugin-install-commit") return { async click() { rowVisible = true; navVisible = true; release(); } };
      if (id === "plugin-install-plan") return { waitFor: () => waitFor(() => true) };
      assert.equal(id, navTestid(A), "the navigation oracle must stay");
      return { waitFor: () => waitFor(() => navVisible) };
    },
  };
  const fsStub = { writeFile: async (file, content) => { written = `${file}\n${content}`; } };
  const finished = new Function("fs", "path", "APP", "A", `return (${source});`)(fsStub, path, navTestid, A)
    (page, { controlsDir: "controls" }, "app-a", A, "open-app")
    .then(() => { settled = true; settleState = state; }, error => { settled = true; failure = error; });
  return {
    get settled() { return settled; }, get settleState() { return settleState; }, get written() { return written; },
    get rowClicked() { return rowClicked; }, get navVisible() { return navVisible; }, get requested() { return requested; },
    async applyToggle(next) {
      state = next;
      toggle.classList.toggle("enabled", next.enabled);
      toggle.toggleAttribute("disabled", next.busy);
      release();
      await drain();
    },
    async drain() { await drain(); },
    async finish() {
      await drain();
      assert.ok(settled, "install helper must finish once the toggle is ready");
      assert.ok(!failure, failure?.stack);
      await finished;
    },
    async dispose() {
      if (disposed) return;
      disposed = true;
      state = { enabled: true, busy: false };
      toggle.classList.add("enabled");
      toggle.removeAttribute("disabled");
      release();
      for (const waiter of [...waiters]) { waiters.delete(waiter); waiter.resolve(); }
      await drain();
      await finished; // failures are recorded on the harness, never unhandled
    },
  };
};
const installVariant = replacement => {
  const mutated = installSource.replace(toggleSelectorLiteral, replacement);
  assert.notEqual(mutated, installSource, "the gate selector anchor is gone from installApplication");
  return mutated;
};

test("installApplication completes only after the toggle is both enabled and not busy", async () => {
  const h = installGateHarness();
  try {
    await h.drain();
    assert.ok(h.written.includes("app-a.vtplugin"), "the package source must be staged first");
    assert.equal(h.rowClicked, true, "the helper still selects the installed row");
    assert.equal(h.navVisible, true, "the nav entry may already render");
    assert.equal(h.settled, false, "disabled yet not busy: the helper must not finish early");
    await h.applyToggle({ enabled: true, busy: true });
    assert.equal(h.settled, false, "enabled but still busy: the helper must not finish early");
    await h.applyToggle({ enabled: true, busy: false });
    await h.finish();
    assert.deepEqual(h.settleState, { enabled: true, busy: false });
    assert.ok(h.requested.includes(navTestid(A)), "keep the navigation oracle");
  } finally { await h.dispose(); }
});

test("the disabled window catches a gate selector that drops the enabled class check", async () => {
  const h = installGateHarness(installVariant(`'button[data-testid="plugin-toggle"]:not([disabled])'`));
  try {
    await h.drain();
    assert.equal(h.settled, true, "a selector without .enabled matches too early");
    assert.equal(h.settleState.enabled, false, "the disabled window must be observable");
  } finally { await h.dispose(); }
});

test("the enabled-but-busy window catches a gate selector that drops the not-disabled check", async () => {
  const h = installGateHarness(installVariant(`'button[data-testid="plugin-toggle"].enabled'`));
  try {
    await h.drain();
    assert.equal(h.settled, false, "without the .enabled class the variant still waits");
    await h.applyToggle({ enabled: true, busy: true });
    assert.equal(h.settled, true, "a selector without :not([disabled]) matches too early");
    assert.equal(h.settleState.busy, true, "the enabled-but-busy window must be observable");
  } finally { await h.dispose(); }
});
