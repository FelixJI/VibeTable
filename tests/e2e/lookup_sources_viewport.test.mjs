import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { parse, compileStyle } from "../../desktop/web-grid/node_modules/@vue/compiler-sfc/dist/compiler-sfc.esm-browser.js";
import { chromium } from "../../desktop/web-grid/node_modules/playwright-core/index.mjs";
import { observeTestPhases } from "./test_phase_evidence.mjs";

const root = new URL("../../", import.meta.url);
const output = new URL("build/lookup-sources-viewport/", root);
const source = await readFile(new URL("desktop/web-grid/src/views/WorkspaceView.vue", root), "utf8");
const { descriptor } = parse(source);
// Render the production call-site template and scoped CSS with the real Naive Modal.
// Only provenance data/dispatch is a fixture; layout and modal behavior are not stubbed.
const panelAt = descriptor.template.content.indexOf('class="lookup-sources-panel"');
assert.ok(panelAt >= 0);
const modalStart = descriptor.template.content.lastIndexOf("<NModal", panelAt);
const modalEnd = descriptor.template.content.indexOf("</NModal>", panelAt) + "</NModal>".length;
const template = descriptor.template.content.slice(modalStart, modalEnd);
const style = compileStyle({ source: descriptor.styles[0].content, id: "data-v-lookup-probe", scoped: true });
assert.deepEqual(style.errors, []);
const tokens = await readFile(new URL("desktop/web-grid/src/design-tokens/tokens.css", root), "utf8");

function sampleLayout() {
  const panel = document.querySelector(".lookup-sources-panel");
  const rect = (element) => {
    const r = element.getBoundingClientRect();
    const css = getComputedStyle(element);
    return { tag: element.className, top: r.top, bottom: r.bottom, height: r.height,
      position: css.position, transform: css.transform, margin: css.margin, overflow: css.overflow,
      scrollHeight: element.scrollHeight, clientHeight: element.clientHeight, scrollTop: element.scrollTop };
  };
  const ancestors = [];
  for (let el = panel; el; el = el.parentElement) ancestors.push(rect(el));
  return { viewport: { width: innerWidth, height: innerHeight }, panel: rect(panel),
    header: rect(panel.querySelector("header")), list: rect(panel.querySelector("ol")),
    footer: rect(panel.querySelector("footer")), active: document.activeElement.className, ancestors };
}

test("lookup source pagination keeps header and load-more inside the viewport", { timeout: 15_000 }, async (t) => {
  const phases = observeTestPhases(t);
  const browser = await phases.phase("launch Edge", () => chromium.launch({ channel: "msedge", headless: true }));
  t.after(async () => { await phases.phase("close Edge", () => browser.close()); phases.close(); });
  await phases.phase("create evidence directory", () => mkdir(output, { recursive: true }));
  const page = await phases.phase("create Edge page", () => browser.newPage({ viewport: { width: 1280, height: 720 } }));
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const reportPageErrors = () => t.diagnostic(`lookup viewport cached page errors: ${JSON.stringify(pageErrors)}`);
  t.signal.addEventListener("abort", reportPageErrors, { once: true });
  t.after(() => t.signal.removeEventListener("abort", reportPageErrors));
  await phases.phase("set lookup page content", () => page.setContent('<div id="app"></div>'));
  await phases.phase("inject lookup scoped styles", () => page.addStyleTag({ content: tokens + style.code }));
  await phases.phase("inject Vue runtime", () => page.addScriptTag({ path: fileURLToPath(new URL("desktop/web-grid/node_modules/vue/dist/vue.global.js", root)) }));
  await phases.phase("inject Naive UI runtime", () => page.addScriptTag({ path: fileURLToPath(new URL("desktop/web-grid/node_modules/naive-ui/dist/index.js", root)) }));
  await phases.phase("mount lookup Vue app", () => page.evaluate(({ template }) => {
    const { createApp, ref, reactive } = Vue;
    createApp({
      __scopeId: "data-v-lookup-probe", template: '<button id="open" @click="lookupSources.show = true">Open sources</button>' + template,
      components: { NModal: naive.NModal, NButton: naive.NButton },
      setup() {
        const lookupSourcesDialog = ref(null);
        const item = (i) => ({ collection: "sources", itemId: String(i), fieldId: "value",
          collectionLabel: "来源表", recordLabel: `来源记录 ${i + 1}`, fieldLabel: "值", value: i + 1 });
        const lookupSources = reactive({ show: false, items: Array.from({ length: 100 }, (_, i) => item(i)),
          total: 300, totalKnown: true, hasMore: true, loading: false, error: null });
        const lookupProvenance = { dispatch(intent) {
          if (intent.type === "sources.close") lookupSources.show = false;
          if (intent.type === "sources.loadMore") {
            window.lookupLoadMoreCalls = (window.lookupLoadMoreCalls || 0) + 1;
            const offset = lookupSources.items.length;
            lookupSources.items.push(...Array.from({ length: 100 }, (_, i) => item(i + offset)));
            lookupSources.hasMore = lookupSources.items.length < lookupSources.total;
          }
        } };
        return { lookupSourcesDialog, lookupSources, lookupProvenance,
          focusModalDialog(dialog) { dialog?.focus({ preventScroll: true }); },
          t(key) { return ({ "workspace.lookup.sourcesTitle": "查找引用来源", "common.close": "关闭",
            "workspace.lookup.loadMoreSources": "加载更多来源" })[key] || key; } };
      },
    }).mount("#app");
  }, { template }));
  await phases.phase("open lookup modal", () => page.locator("#open").click());
  await phases.phase("wait for initial sources attached", () => page.locator(".lookup-sources-panel li").last().waitFor({ state: "attached" }));
  // The production after-enter focus is readiness; do not wait for good geometry.
  await phases.phase("wait for lookup modal after-enter focus", () => page.waitForFunction(() => document.activeElement?.matches(".lookup-sources-panel")));
  const evidence = await phases.phase("sample layout", () => page.evaluate(sampleLayout));
  await phases.phase("write initial layout", () => writeFile(new URL("layout.json", output), JSON.stringify(evidence, null, 2)));
  await phases.phase("capture initial screenshot", () => page.screenshot({ path: fileURLToPath(new URL("panel.png", output)) }));

  await phases.phase("ordinary load-more click", () => page.locator(".lookup-sources-panel > footer button").click({ timeout: 2_000 }));
  assert.equal(await phases.phase("read load-more count", () => page.evaluate(() => window.lookupLoadMoreCalls)), 1);
  for (const region of [evidence.header, evidence.footer]) {
    assert.ok(region.top >= 0 && region.bottom <= evidence.viewport.height, JSON.stringify(evidence));
  }
  assert.ok(evidence.list.scrollHeight > evidence.list.clientHeight, JSON.stringify(evidence));
  await phases.phase("wait for appended sources", () => page.locator(".lookup-sources-panel li").nth(199).waitFor({ state: "attached" }));
  await phases.phase("resize and scroll appended sources", async () => {
    await page.setViewportSize({ width: 800, height: 600 });
    await page.locator(".lookup-sources-panel li button").last().scrollIntoViewIfNeeded();
  });
  const scrolled = await phases.phase("sample layout", () => page.evaluate(sampleLayout));
  await phases.phase("write scrolled layout", () => writeFile(new URL("scrolled-layout.json", output), JSON.stringify(scrolled, null, 2)));
  await phases.phase("capture scrolled screenshot", () => page.screenshot({ path: fileURLToPath(new URL("scrolled-panel.png", output)) }));
  for (const region of [scrolled.panel, scrolled.header, scrolled.footer]) {
    assert.ok(region.top >= 0 && region.bottom <= scrolled.viewport.height, JSON.stringify(scrolled));
  }
  assert.ok(scrolled.list.scrollTop > 0, JSON.stringify(scrolled));
  await phases.phase("second load-more click", () => page.locator(".lookup-sources-panel > footer button").click({ timeout: 2_000 }));
  assert.equal(await phases.phase("read load-more count", () => page.evaluate(() => window.lookupLoadMoreCalls)), 2);
  await phases.phase("close modal", () => page.locator(".lookup-sources-panel > header button").click({ timeout: 2_000 }));
  await phases.phase("wait for closed modal", () => page.locator(".lookup-sources-panel").waitFor({ state: "hidden" }));
  assert.deepEqual(pageErrors, []);
});
