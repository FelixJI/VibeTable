import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { build } from "vite";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import type { JsonObject } from "../../../../../sdk/plugin/src/types";
import type { SurfacePresentation } from "../../../../../sdk/plugin/src/surface-ui";

const definitions = {
  products: { table: "ERP 商品", fields: { sku: ["商品编码", "text"], name: ["名称", "text"], price: ["单价分", "number"] } },
  customers: { table: "ERP 客户", fields: { name: ["名称", "text"], email: ["邮箱", "text"] } },
  orders: { table: "ERP 订单", fields: { number: ["订单号", "text"], customer: ["客户", "relation"], product: ["商品", "relation"], quantity: ["数量", "number"], price: ["单价分", "number"], total: ["金额分", "number"], status: ["状态", "text"] } },
};

let script: string;
let html: string;
beforeAll(async () => {
  const root = resolve(import.meta.dirname, "../../../../../examples/plugins/simple-erp");
  html = await readFile(resolve(root, "ui/index.html"), "utf8");
  const bundle = await build({ configFile: false, logLevel: "silent", build: {
    write: false, minify: false, lib: { entry: resolve(root, "src/app.ts"), name: "simpleErp", formats: ["iife"] },
    rollupOptions: { external: ["@vibetable/plugin-sdk"], output: { globals: { "@vibetable/plugin-sdk": "erpSdk" } } },
  } });
  const output = Array.isArray(bundle) ? bundle[0] : bundle;
  if (!output || !("output" in output)) throw new Error("Missing ERP output");
  const chunk = output.output.find(item => item.type === "chunk");
  if (!chunk) throw new Error("Missing ERP script");
  script = chunk.code;
});
afterEach(() => { window.dispatchEvent(new Event("pagehide")); document.body.innerHTML = ""; document.documentElement.removeAttribute("style"); delete document.documentElement.dataset.theme; delete document.documentElement.dataset.density; vi.unstubAllGlobals(); });
class CapabilityError extends Error { constructor(readonly code: string) { super(code); } }
async function mount(themeArrivesFirst = false) {
  document.body.innerHTML = new DOMParser().parseFromString(html, "text/html").body.innerHTML;
  const descriptions = Object.entries(definitions).map(([collection, definition]) => ({
    contract: "vibetable.plugin-data.v2", collection, displayName: definition.table, schemaRevision: "s1",
    fields: [{ fieldId: "id", displayName: "ID", logicalType: "text", readonly: true }, ...Object.entries(definition.fields).map(([fieldId, [displayName, logicalType]]) => ({ fieldId, displayName, logicalType, readonly: false, resultType: logicalType }))],
  }));
  const rows: Record<string, JsonObject[]> = { products: [{ id: "p1", sku: "P1", name: "<img src=x>", price: 1299 }], customers: [{ id: "c1", name: "客户甲", email: "a@example.test" }], orders: [] };
  const submit = vi.fn(async () => ({ status: "rejected", taskId: "t1" }));
  const preview = vi.fn(async () => ({ planId: "plan1", affectedCount: 1 }));
  const callbacks: ((event: { type: "invalidated" }) => void)[] = [];
  let themeChanged!: (theme: SurfacePresentation) => void;
  const initialTheme: SurfacePresentation = { contract: "vibetable.plugin-theme.v1", mode: "dark", locale: "zh-CN", density: "compact", variables: { "--vt-plugin-bg": "#123456", "--vt-plugin-text": "#fafafa" } };
  const api = {
    context: { current: async () => {
      if (themeArrivesFirst) themeChanged({ ...initialTheme, mode: "light", density: "comfortable" });
      return initialTheme;
    }, onChanged: (listener: (theme: SurfacePresentation) => void) => { themeChanged = listener; return () => {}; } },
    tasks: { onChanged: () => () => {} },
    ui: { navigation: { back: vi.fn(async () => ({ navigated: true })) } },
    data: {
      describe: async ({ collection }: { collection?: string }) => collection ? descriptions.find(item => item.collection === collection) : { tables: descriptions },
      async *pages({ collection }: { collection: string }) { yield { schemaRevision: "s1", totalRows: rows[collection].length, complete: true, items: rows[collection], rowGuards: { p1: "guard1" } }; },
      subscribe: async (_request: unknown, callback: (event: { type: "invalidated" }) => void) => { callbacks.push(callback); return { subscriptionId: String(callbacks.length) }; },
      unsubscribe: vi.fn(async () => {}),
    },
    mutation: { preview, submit }, dispose: vi.fn(),
  };
  vi.stubGlobal("erpSdk", { createSurfaceClient: () => api, PluginCapabilityError: CapabilityError });
  window.eval(script);
  await vi.waitFor(() => expect(document.querySelector("#status")?.textContent).toContain("已读取三张表"));
  return { api, callbacks, preview, submit, themeChanged };
}
const click = (selector: string) => document.querySelector<HTMLButtonElement>(selector)!.click();
function fill(name: string, value: string): void {
  const input = document.querySelector<HTMLInputElement | HTMLSelectElement>(`[name=${name}]`)!;
  input.value = value; input.dispatchEvent(new Event("input", { bubbles: true }));
}
it("实际 ERP 入口安全显示名称、生成整数金额计划；拒绝保留表单", async () => {
  const { preview, submit } = await mount();
  expect(document.querySelector("#list img")).toBeNull();
  expect(document.querySelector("#list")?.textContent).toContain("<img src=x>");
  click('[data-section="orders"]'); click("#new");
  fill("number", "SO-1"); fill("product", "p1"); fill("customer", "c1"); fill("quantity", "2");
  document.querySelector("#form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await vi.waitFor(() => expect(submit).toHaveBeenCalledWith("plan1", expect.any(AbortSignal)));
  expect(preview).toHaveBeenCalledWith(expect.objectContaining({ operations: [{ kind: "create", values: { number: "SO-1", product: "p1", customer: "c1", quantity: 2, price: 1299, total: 2598, status: "草稿" } }] }));
  await vi.waitFor(() => expect(document.querySelector("#status")?.textContent).toContain("表单已保留"));
  expect(document.querySelector<HTMLInputElement>('[name="number"]')?.value).toBe("SO-1");
  click('[data-section="products"]');
  expect(document.querySelector<HTMLElement>("#leave")?.hidden).toBe(false);
  click("#leave-cancel");
  expect(document.querySelector("#title")?.textContent).toBe("订单");
});
it("提交结果未知禁止重复提交；实时失效阻止编辑", async () => {
  const { submit, callbacks } = await mount();
  click("[data-edit]"); fill("name", "新名称");
  submit.mockRejectedValueOnce(new CapabilityError("plugin_commit_unknown"));
  document.querySelector("#form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await vi.waitFor(() => expect(document.querySelector("#status")?.textContent).toContain("提交结果未知"));
  expect(document.querySelector<HTMLButtonElement>('#form button[type="submit"]')?.disabled).toBe(true);
  expect(document.querySelector<HTMLButtonElement>("#new")?.disabled).toBe(true);
  callbacks[0]({ type: "invalidated" });
  expect(document.querySelector<HTMLButtonElement>("[data-edit]")?.disabled).toBe(true);
  click("#discard"); click("#leave-confirm"); click("#reload");
  await vi.waitFor(() => expect(document.querySelector("#status")?.textContent).toContain("已读取三张表"));
  expect(document.querySelector<HTMLElement>("#uncertain")?.hidden).toBe(false);
  expect(document.querySelector<HTMLButtonElement>("#new")?.disabled).toBe(true);
});

it("保存期间冻结输入；已成功但刷新失败仍明确报告保存成功", async () => {
  const { api, submit } = await mount();
  click("[data-edit]"); fill("name", "新名称");
  let finish!: (value: { status: string; taskId: string }) => void;
  submit.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  document.querySelector("#form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await vi.waitFor(() => expect(submit).toHaveBeenCalled());
  expect(document.querySelector<HTMLInputElement>('[name="name"]')?.disabled).toBe(true);
  vi.spyOn(api.data, "describe").mockRejectedValueOnce(new Error("读取失败"));
  finish({ status: "succeeded", taskId: "t1" });
  await vi.waitFor(() => expect(document.querySelector("#status")?.textContent).toContain("保存已成功，但刷新失败"));
  expect(document.querySelector<HTMLElement>("#editor")?.hidden).toBe(true);
});

it("首次主题与密度生效；后续切换移除旧 token，保留输入", async () => {
  const { themeChanged } = await mount();
  const root = document.documentElement;
  expect(root.dataset.theme).toBe("dark"); expect(root.dataset.density).toBe("compact");
  expect(root.style.getPropertyValue("--vt-plugin-bg")).toBe("#123456");
  click("#new"); fill("name", "未保存商品");
  themeChanged({ contract: "vibetable.plugin-theme.v1", mode: "light", locale: "en-US", density: "comfortable", variables: { "--vt-plugin-bg": "#ffffff" } });
  expect(root.dataset.density).toBe("comfortable"); expect(root.style.colorScheme).toBe("light");
  expect(root.style.getPropertyValue("--vt-plugin-text")).toBe("");
  expect(document.querySelector<HTMLInputElement>('[name="name"]')?.value).toBe("未保存商品");
});

it("字段校验定位首个错误、关联说明且不发起写入", async () => {
  const { preview } = await mount();
  click("#new"); fill("sku", "P2"); fill("name", "商品乙"); fill("price", "1.001");
  const form = document.querySelector("#form")!;
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  const price = document.querySelector<HTMLInputElement>('[name="price"]')!;
  expect(document.activeElement).toBe(price);
  expect(price.getAttribute("aria-invalid")).toBe("true");
  expect(document.getElementById(price.getAttribute("aria-describedby")!)?.textContent).toContain("两位小数");
  expect(preview).not.toHaveBeenCalled();
  fill("price", "0");
  expect(price.hasAttribute("aria-invalid")).toBe(false);
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await vi.waitFor(() => expect(preview).toHaveBeenCalled());
});

it("离开确认阻止其它动作，Escape 保留草稿并恢复焦点", async () => {
  const { api } = await mount();
  click("#new"); fill("name", "草稿");
  const input = document.querySelector<HTMLInputElement>('[name="name"]')!; input.focus();
  click("#back");
  expect(document.activeElement?.id).toBe("leave-cancel");
  expect(document.querySelector<HTMLButtonElement>('[data-section="orders"]')?.disabled).toBe(true);
  expect(api.ui.navigation.back).not.toHaveBeenCalled();
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  expect(document.activeElement).toBe(input); expect(input.value).toBe("草稿");
  expect(document.querySelector<HTMLElement>("#leave")?.hidden).toBe(true);
});

it("迟到的首次主题快照不覆盖已收到的新主题", async () => {
  await mount(true);
  expect(document.documentElement.dataset.theme).toBe("light");
  expect(document.documentElement.dataset.density).toBe("comfortable");
});

it.each([
  { section: "products", inputs: {}, field: "sku" },
  { section: "customers", inputs: { name: "客户乙", email: "invalid" }, field: "email" },
  { section: "orders", inputs: { number: "SO-2", product: "p1", customer: "c1", quantity: "1.5" }, field: "quantity" },
])("$section 表单拒绝无效的 $field 且保留输入", async ({ section, inputs, field }) => {
  const { preview } = await mount();
  click(`[data-section="${section}"]`); click("#new");
  for (const [name, value] of Object.entries(inputs)) fill(name, value);
  document.querySelector("#form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  expect(document.activeElement?.id).toBe(`field-${field}`);
  expect(document.activeElement?.getAttribute("aria-invalid")).toBe("true");
  expect(preview).not.toHaveBeenCalled();
});
