import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import test, { after } from "node:test";
import { JSDOM } from "../../desktop/web-grid/node_modules/jsdom/lib/api.js";
import { build } from "../../desktop/web-grid/node_modules/vite/dist/node/index.js";

// Compile the real view controller and controls against their installed Vue runtime.
const dom = new JSDOM("<body></body>", { url: "https://app.vibetable.local/", pretendToBeVisual: true });
for (const name of ["window", "document", "Element", "HTMLElement", "SVGElement", "Node", "MutationObserver", "InputEvent"])
  globalThis[name] = dom.window[name];
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
Object.defineProperty(globalThis, "localStorage", { value: dom.window.localStorage, configurable: true });
after(() => dom.window.close());
globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window);
const root = new URL("../../desktop/web-grid/", import.meta.url);
const output = await build({
  configFile: false, root: fileURLToPath(root), logLevel: "silent",
  plugins: [(await import("../../desktop/web-grid/node_modules/@vitejs/plugin-vue/dist/index.mjs")).default(), {
    name: "calculation-search-readiness",
    resolveId: id => id.endsWith("journey-readiness") ? "\0journey-readiness" : undefined,
    load: id => id === "\0journey-readiness" ? `
      export { createApp, h, nextTick, ref, effectScope } from 'vue';
      export { createPinia, setActivePinia } from 'pinia';
      export { createPresetViewController } from '@/workspace/presetViewController.ts';
      export { useWorkspaceStore } from '@/stores/workspaceStore.ts';
      export { useTableStore } from '@/stores/tableStore.ts';
      export { useViewQueryStore } from '@/stores/viewQueryStore.ts';
      export { useUiStore } from '@/stores/uiStore.ts';
      export { usePresetVersionStore } from '@/stores/presetVersionStore.ts';
      export { default as ViewQueryControls } from '@/components/grid/ViewQueryControls.vue';
    ` : undefined,
  }],
  resolve: { alias: { "@": fileURLToPath(new URL("src/", root)) } },
  build: { write: false, minify: false, lib: { entry: "journey-readiness", formats: ["es"] } },
});
const app = await import(`data:text/javascript;base64,${Buffer.from((Array.isArray(output) ? output[0] : output).output.find(file => file.type === "chunk").code).toString("base64")}`);
const journey = await fs.readFile(new URL("./calculation_chain_journey.mjs", import.meta.url), "utf8");
const searchStart = journey.indexOf('  await selectTable(page, sourceName);', journey.indexOf('// Change the source'));
const searchEnd = journey.indexOf('  await waitForVisibleRowCount(page, 1);', searchStart);
assert.ok(searchStart >= 0 && searchEnd > searchStart);
const search = new Function("page", "selectTable", "sourceName", "fillNInput", "sources", "amount",
  `return (async () => { ${journey.slice(searchStart, searchEnd)} })();`);

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}
const tick = async () => { await new Promise(resolve => setImmediate(resolve)); await app.nextTick(); };

test("source keyword waits for its table and completed view restore before the single UI fill", { timeout: 10_000 }, async () => {
  app.setActivePinia(app.createPinia());
  const workspace = app.useWorkspaceStore(), table = app.useTableStore();
  const query = app.useViewQueryStore(), ui = app.useUiStore(), presets = app.usePresetVersionStore();
  const hostRead = deferred();
  const queries = [], fills = [];
  const scope = app.effectScope();
  const controller = scope.run(() => app.createPresetViewController({
    workspace, table, query, ui, presets,
    grid: { current: app.ref({ getColumns: () => [] }) },
    presentation: { identity: app.ref("workspace:1"), service: {
      read: () => hostRead.promise,
      save: async (_table, state) => ({ state, revision: "r2", conflict: false }),
    } },
    service: { listPresets: async () => ({ collection: "source", presets: [] }),
      savePreset: async () => { throw new Error("unexpected preset write"); }, deletePreset: async () => {} },
    executeQuery: (_table, state) => queries.push(state),
    refreshLookups: () => {}, reportError: error => { throw error; },
    defaultCompensationError: () => new Error("compensation"),
  }));
  const target = document.createElement("div");
  document.body.append(target);
  const rendered = app.createApp({ render: () => app.h(app.ViewQueryControls, {
    columns: table.schema ?? [], filters: query.filters, groups: query.groups,
    summaries: query.summaries, visibleFields: query.visibleFields, keyword: query.search,
    disabled: controller.presentationLoading.value,
    onKeywordChange: keyword => controller.dispatch({ type: "keyword.changed", keyword }),
  }) });
  rendered.mount(target);
  const header = document.createElement("div");
  header.className = "grid-wrapper";
  header.innerHTML = '<div class="tabulator-col" tabulator-field="main_amount"></div>';
  document.body.append(header);
  let poll;
  const page = { waitForFunction: (predicate, argument) => new Promise(resolve => {
    poll = () => { if (predicate(argument)) resolve(); };
    poll();
  }) };
  const fillNInput = async (_page, id, value) => {
    fills.push(value);
    const input = document.querySelector(`[data-testid="${id}"] input`);
    input.value = value;
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
    await app.nextTick();
  };
  try {
    const selected = deferred();
    const searching = search(page, async () => {
      workspace.selectTable("source");
      table.schema = [{ name: "source_amount", title: "金额", dataType: "decimal", editable: true, nullable: true }];
      selected.resolve();
    }, "链路来源", fillNInput, [{ marker: "来源-000" }], { physicalName: "source_amount" });
    await selected.promise;
    await tick();
    assert.equal(document.querySelector('[data-testid="view-query-controls"]').getAttribute("aria-busy"), "true");
    assert.deepEqual(fills, [], "the keyword must not be filled during view restore");
    // The old unguarded UI fill is ignored by the real initializing controller.
    await fillNInput(page, "view-keyword", "来源-000");
    assert.equal(query.search, "");
    assert.equal(queries.filter(state => state.keyword === "来源-000").length, 0);
    fills.length = 0;
    hostRead.resolve({ state: {}, revision: "r1", conflict: false });
    await tick();
    poll();
    await tick();
    assert.deepEqual(fills, [], "a ready toolbar from another grid must not release the source search");
    header.firstElementChild.setAttribute("tabulator-field", "source_amount");
    poll();
    await searching;
    await tick();
    assert.deepEqual(fills, ["来源-000"]);
    assert.equal(query.search, "来源-000");
    assert.equal(document.querySelector('[data-testid="view-keyword"] input').value, "来源-000");
    assert.equal(queries.filter(state => state.keyword === "来源-000").length, 1);
  } finally {
    rendered.unmount();
    scope.stop();
    target.remove();
    header.remove();
  }
});
