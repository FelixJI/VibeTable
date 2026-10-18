import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
const root = new URL("../../", import.meta.url);
const require = createRequire(new URL("examples/plugins/record-manager/package.json", root));
const { build } = require("esbuild");
const compile = async (entry, globalName) => (await build({ entryPoints: [fileURLToPath(new URL(entry, root))], bundle: true, format: "iife", globalName, platform: "neutral", write: false, logLevel: "silent" })).outputFiles[0].text;
const [app, testing, ui, surface] = await Promise.all([
  compile("examples/plugins/record-manager/src/app.ts", "recordManager"), compile("sdk/plugin/src/testing.ts", "testSDK"),
  readFile(new URL("examples/plugins/record-manager/ui/index.html", root), "utf8"),
  readFile(new URL("desktop/web-grid/src/components/plugins/PluginSurfaceHost.vue", root), "utf8"),
]);
const sandbox = /sandbox="([^"]+)"/.exec(surface)?.[1];
assert.equal(sandbox, "allow-scripts allow-same-origin");
const evidence = new URL("build/qa/task484/review-r4/sandbox/", root);
const fixture = `
const fields = ["id", "title"].map(fieldId => ({ fieldId, displayName: fieldId, logicalType: "text", resultType: "text", readonly: fieldId === "id", nullable: true, display: null, options: [], constraints: null, filterOperators: [], sortable: true }));
window.submitted = []; window.calls = [];
const listeners = new Set();
const host = testSDK.createOfflineSurfaceHost({ pluginId: "sandbox-test", workspaceId: "workspace-test", surfaceToken: window.name, risk: "destructive", context: { collection: "articles" },
 descriptions: { articles: { collection: "articles", displayName: "Articles", contract: "vibetable.plugin-data.v2", schemaRevision: "schema-one", fields } },
 permissions: { data: [{ collection: "$active", operations: ["read", "query", "create", "update", "delete"], fields: ["*"] }] },
 queryAdapter: async () => ({ contract: "vibetable.plugin-query-page.v2", items: [], totalRows: 0, filteredRows: 0, nextCursor: null, complete: true, schemaRevision: "schema-one", dataRevision: 1 }),
 previewMutation: async () => {}, submitMutation: async plan => { window.submitted.push(plan); return { status: "succeeded", taskId: "sandbox-task" }; },
});
const transport = { postMessage(message) {
 window.calls.push(message);
 if (message.method === "data.subscribe" || message.method === "data.unsubscribe") queueMicrotask(() => {
   for (const listener of listeners) listener({ data: { contract: "vibetable.plugin-api.v1", requestId: message.requestId, result: { subscriptionId: "sandbox-subscription" } } });
 }); else host.transport.postMessage(message);
}, addEventListener(type, listener) { listeners.add(listener); host.transport.addEventListener(type, listener); }, removeEventListener(type, listener) { listeners.delete(listener); host.transport.removeEventListener(type, listener); } };
window.chrome ??= {}; window.chrome.webview = transport;
`;
const outer = `<iframe name="sandbox-surface" sandbox="${sandbox}" src="https://synthetic.plugins.vibetable.local/ui/index.html"></iframe><script>
addEventListener("message", event => {
 const message = event.data;
 if (message?.event !== "action") return;
 event.source.postMessage({ contract: "vibetable.plugin-surface.v1", surfaceToken: message.surfaceToken, event: "uiResponse", payload: { requestId: message.payload.requestId,
 result: { contract: "vibetable.plugin-theme.v1", mode: "light", locale: "zh-CN", density: "comfortable", variables: {} } } }, event.origin);
});
</script>`;

test("真实Edge sandbox禁止allow-forms：点击创建和输入Enter都经受控SDK保存", { timeout: 30_000 }, async t => {
  await mkdir(evidence, { recursive: true });
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  const messages = []; page.on("console", message => messages.push({ type: message.type(), text: message.text() }));
  page.on("pageerror", error => messages.push({ type: "pageerror", text: error.message }));
  await page.route("https://**/*", async route => {
    const url = new URL(route.request().url());
    const body = url.hostname === "app.vibetable.local" ? outer : url.pathname === "/testing.js" ? testing : url.pathname === "/fixture.js" ? fixture : url.pathname === "/dist/app.js" ? app : ui.replace("</head>", '<script src="/testing.js"></script><script src="/fixture.js"></script></head>');
    await route.fulfill({ status: 200, contentType: url.pathname.endsWith(".js") ? "text/javascript" : "text/html", body });
  });
  let state;
  try {
    await page.goto("https://app.vibetable.local/");
    const frame = page.frameLocator("iframe");
    await frame.locator("#status").filter({ hasText: "ready" }).waitFor({ timeout: 5000 });
    for (const [index, interaction] of ["click", "Enter"].entries()) {
      await frame.locator('nav a[data-page="detail"]').click();
      await frame.locator('[data-field-id="title"]').fill(`sandbox-${index}`);
      if (interaction === "click") await frame.getByRole("button", { name: "预览并创建", exact: true }).click();
      else await frame.locator('[data-field-id="title"]').press("Enter");
      await frame.locator("#status").filter({ hasText: "创建：已提交。" }).waitFor({ timeout: 3000 });
      const submitted = await frame.locator("body").evaluate(() => window.submitted);
      assert.equal(submitted.length, index + 1);
      assert.deepEqual(submitted[index].operations, [{ kind: "create", values: { title: `sandbox-${index}` } }]);
      await frame.locator('nav a[data-page="overview"]').click();
    }
    state = await frame.locator("body").evaluate(() => ({ submitted: window.submitted, methods: window.calls.map(call => call.method) }));
    assert.equal(state.methods.filter(method => method === "mutation.preview").length, 2);
    assert.equal(state.methods.filter(method => method === "mutation.submit").length, 2);
    assert.equal(messages.some(message => message.type === "pageerror" || /Blocked form submission/.test(message.text)), false);
  } finally {
    await page.screenshot({ path: fileURLToPath(new URL("record-manager.png", evidence)), fullPage: true });
    await writeFile(new URL("result.json", evidence), JSON.stringify({ sandbox, state, messages }, null, 2));
    t.diagnostic(JSON.stringify({ sandbox, messages }));
  }
});
