import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import { JSDOM } from "../../desktop/web-grid/node_modules/jsdom/lib/api.js";
const require = createRequire(new URL("../../examples/plugins/record-manager/package.json", import.meta.url));
const { build } = require("esbuild");
const root = new URL("../../", import.meta.url);
const compile = async entry => (await build({ entryPoints: [fileURLToPath(new URL(entry, root))], bundle: true, format: "iife", globalName: "module", platform: "neutral", write: false, logLevel: "silent" })).outputFiles[0].text;
// SDK testing lives outside the production entry; only the test process imports it.
const testingSource = await compile("sdk/plugin/src/testing.ts");
const appSource = await compile("examples/plugins/record-manager/src/app.ts");
const html = await readFile(new URL("examples/plugins/record-manager/ui/index.html", root), "utf8");
const wait = async predicate => { const end = Date.now() + 3000; while (!predicate()) { if (Date.now() > end) throw new Error("DOM did not reach the expected state"); await new Promise(resolve => setTimeout(resolve, 1)); } };
const field = (fieldId, logicalType, readonly = false) => ({ fieldId, displayName: fieldId, logicalType, resultType: logicalType, readonly, nullable: true, display: null, options: [], constraints: null, filterOperators: [], sortable: true });
async function fixture({ rows = [{ id: "record-one", title: "", json: [0, false, null], owner: "target-id" }], guards = { "record-one": "go-guard" }, submit = async () => ({ status: "succeeded", taskId: "task" }), preview = async () => {} } = {}) {
  const dom = new JSDOM(html, { url: "https://synthetic.plugins.vibetable.local/ui/index.html", runScripts: "outside-only" });
  const window = dom.window; window.name = "surface-test"; window.TextEncoder = TextEncoder; window.structuredClone = structuredClone;
  window.eval(`${testingSource}; globalThis.testing = module;`);
  const table = { collection: "articles", displayName: "Articles", contract: "vibetable.plugin-data.v2", schemaRevision: "schema-one", fields: [field("id", "text", true), field("title", "text"), field("json", "json"), field("owner", "relation")] };
  const plans = [], calls = [], subscribers = new Set(); let revision = 1;
  const host = window.testing.createOfflineSurfaceHost({ pluginId: "test-plugin", workspaceId: "test-workspace", surfaceToken: window.name, risk: "destructive", context: { collection: "articles" }, descriptions: { articles: table }, rowGuards: { articles: guards }, revision: () => String(revision), permissions: { data: [{ collection: "$active", operations: ["read", "query", "create", "update", "delete"], fields: ["*"] }] },
    queryAdapter: async query => {
      const matching = query.ids ? rows.filter(row => query.ids.includes(row.id)) : rows;
      const offset = query.cursor ? Number(query.cursor) : 0, size = query.pageSize ?? 100;
      return { contract: "vibetable.plugin-query-page.v2", items: matching.slice(offset, offset + size).map(row => Object.fromEntries(query.fields.map(id => [id, row[id] ?? null]))), totalRows: rows.length, filteredRows: matching.length, nextCursor: offset + size < matching.length ? String(offset + size) : null, complete: true, schemaRevision: "schema-one", dataRevision: revision };
    }, previewMutation: async plan => { plans.push(plan); await preview(plan); }, submitMutation: submit,
  });
  const send = data => { for (const listener of subscribers) listener({ data }); };
  const transport = { postMessage(message) {
    calls.push(message);
    if (message.method === "data.subscribe") queueMicrotask(() => send({ contract: "vibetable.plugin-api.v1", requestId: message.requestId, result: { subscriptionId: "subscription-one" } }));
    else if (message.method === "data.unsubscribe") queueMicrotask(() => send({ contract: "vibetable.plugin-api.v1", requestId: message.requestId, result: null }));
    else host.transport.postMessage(message);
  }, addEventListener(type, listener) { subscribers.add(listener); host.transport.addEventListener(type, listener); }, removeEventListener(type, listener) { subscribers.delete(listener); host.transport.removeEventListener(type, listener); } };
  const parent = { postMessage(message) { if (message.event !== "action") return; queueMicrotask(() => window.dispatchEvent(new window.MessageEvent("message", { origin: "https://app.vibetable.local", source: parent, data: { contract: "vibetable.plugin-surface.v1", surfaceToken: window.name, event: "uiResponse", payload: { requestId: message.payload.requestId, result: { contract: "vibetable.plugin-theme.v1", mode: "light", locale: "zh-CN", density: "comfortable", variables: {} } } } }))); } };
  Object.defineProperty(window, "parent", { value: parent }); window.chrome = { webview: transport };
  window.eval(appSource);
  const $ = selector => window.document.querySelector(selector);
  await wait(() => $("#status").textContent === "ready");
  const route = async hash => { window.location.hash = hash; await new Promise(resolve => setTimeout(resolve, 10)); };
  const close = () => { window.dispatchEvent(new window.Event("pagehide")); window.close(); };
  const input = (id, text) => { const item = $(`[data-field-id="${id}"]`); item.value = text; item.dispatchEvent(new window.Event("input", { bubbles: true })); };
  const save = () => $("#record-save").click();
  return { window, $, plans, calls, route, close, input, save, get cursorCount() { return host.cursorCount; }, invalidate() { revision++; send({ contract: "vibetable.plugin-api.v1", surfaceToken: window.name, event: "data.invalidated", subscriptionId: "subscription-one", collection: "articles", sequence: revision }); } };
}
test("真实应用 DOM：缺行 guard 和不存在记录均不能降级为创建", async () => {
  const app = await fixture({ guards: {} });
  try {
    await app.route("#detail/record-one"); assert.equal(app.$('#record-save').disabled, true);
    app.save(); await new Promise(resolve => setImmediate(resolve)); assert.equal(app.plans.length, 0);
    await app.route("#detail/missing"); assert.equal(app.$("#record-form"), null); assert.match(app.$("#detail-body").textContent, /未切换为创建/); assert.equal(app.plans.length, 0);
  } finally { app.close(); }
});
test("真实应用 DOM：编辑只提交改动，原有空串/JSON/关系不被格式化回写", async () => {
  const app = await fixture();
  try {
    await app.route("#detail/record-one"); assert.equal(app.$('[data-null-field-id="title"]').checked, false);
    app.input("title", "updated"); app.save(); await wait(() => app.$("#status").textContent === "保存：已提交。");
    assert.deepEqual(JSON.parse(JSON.stringify(app.plans[0].operations)), [{ kind: "update", primaryKey: "record-one", expectedDigest: "go-guard", values: { title: "updated" } }]);
    assert.equal(app.$('#record-save').disabled, true);
  } finally { app.close(); }
});
test("真实应用 DOM：在途提交可取消，重复 submit 不启动第二次计划，未知结果不重放", async () => {
  let submits = 0;
  const app = await fixture({ submit: (_plan, signal) => { submits++; return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject({ code: "plugin_cancel_requested" }), { once: true })); } });
  try {
    await app.route("#detail/record-one"); app.input("title", "new"); app.save(); await wait(() => submits === 1);
    const cancel = [...app.window.document.querySelectorAll("button")].find(button => button.textContent === "取消在途提交"); assert.equal(cancel.disabled, false);
    app.save(); assert.equal(app.plans.length, 1); cancel.click(); await wait(() => app.$("#status").textContent === "保存：已取消。"); assert.equal(submits, 1); assert.equal(cancel.disabled, true);
  } finally { app.close(); }
  const unknown = await fixture({ submit: async () => { throw { code: "plugin_commit_unknown" }; } });
  try {
    await unknown.route("#detail/record-one"); unknown.input("title", "changed"); unknown.save(); await wait(() => unknown.$("#status").textContent.includes("提交结果未知"));
    unknown.save(); assert.equal(unknown.plans.length, 1); assert.equal(unknown.$('#record-save').disabled, true);
  } finally { unknown.close(); }
});
test("真实应用 DOM：前进后退按钮更新；失效后取消旧页并从首页重查", async () => {
  const app = await fixture({ rows: Array.from({ length: 101 }, (_, i) => ({ id: `r-${i}`, title: String(i) })) });
  try {
    await app.route("#list"); assert.equal(app.$("#page-prev").disabled, true); assert.equal(app.$("#page-next").disabled, false);
    app.$("#page-next").click(); await wait(() => app.$("#status").textContent.includes("第 2 页")); assert.equal(app.$("#page-prev").disabled, false);
    app.$("#page-next").click(); await wait(() => app.$("#status").textContent.includes("第 3 页")); assert.equal(app.$("#page-next").disabled, true);
    app.$("#page-prev").click(); await wait(() => app.$("#status").textContent.includes("第 2 页")); assert.equal(app.$("#page-next").disabled, false);
    app.invalidate(); await wait(() => app.$("#status").textContent.includes("第 1 页")); assert.equal(app.$("#page-prev").disabled, true);
  } finally { app.close(); }
});


test("真实应用DOM：概览100次失效/刷新均释放探针，不耗尽64句柄", async () => {
  const app = await fixture({ rows: Array.from({ length: 550 }, (_, i) => ({ id: `r-${i}`, title: String(i) })) });
  try {
    assert.equal(app.cursorCount, 0);
    for (let i = 0; i < 100; i++) {
      const before = app.calls.filter(call => call.method === "data.releaseCursor").length;
      app.invalidate();
      await wait(() => app.calls.filter(call => call.method === "data.releaseCursor").length > before);
      assert.equal(app.cursorCount, 0);
    }
    assert.match(app.$("#overview-body").textContent, /权威记录数：550 条/);
  } finally { app.close(); }
});


test("真实应用DOM：preview期间禁止取消，九次保存均进入submit后才可取消且不遗留计划", async () => {
  let releasePreview, submits = 0;
  const app = await fixture({ preview: () => new Promise(resolve => releasePreview = resolve), submit: (_plan, signal) => {
    submits++; return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject({ code: "plugin_cancel_requested" }), { once: true }));
  } });
  try {
    for (let i = 0; i < 9; i++) {
      await app.route("#list"); await app.route("#detail/record-one");
      app.input("title", `changed-${i}`); app.save(); await wait(() => app.plans.length === i + 1);
      const cancel = [...app.window.document.querySelectorAll("button")].find(button => button.textContent === "取消在途提交");
      assert.equal(cancel.disabled, true); cancel.click(); assert.equal(submits, i);
      releasePreview(); await wait(() => submits === i + 1);
      assert.equal(cancel.disabled, false); cancel.click();
      await wait(() => app.$("#status").textContent === "保存：已取消。");
      assert.equal(cancel.disabled, true);
    }
    assert.equal(submits, 9); assert.equal(app.plans.length, 9);
    assert.equal(app.calls.filter(call => call.method === "mutation.submit").length, 9);
  } finally { releasePreview?.(); app.close(); }
});
