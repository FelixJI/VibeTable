import { resolve } from "node:path";
import { build } from "vite";
import { afterEach, beforeAll, expect, it, vi } from "vitest";

let script: string;
beforeAll(async () => {
  const bundle = await build({
    configFile: false,
    logLevel: "silent",
    build: {
      write: false,
      minify: false,
      lib: {
        entry: resolve(import.meta.dirname, "../../../../../examples/plugins/data-overview/src/surface.ts"),
        name: "overviewSurface",
        formats: ["iife"],
      },
      rollupOptions: {
        external: ["@vibetable/plugin-sdk"],
        output: { globals: { "@vibetable/plugin-sdk": "overviewSdk" } },
      },
    },
  });
  const output = Array.isArray(bundle) ? bundle[0] : bundle;
  if (!output || !("output" in output)) throw new Error("Expected a surface bundle");
  const chunk = output.output.find(item => item.type === "chunk");
  if (!chunk) throw new Error("Missing surface script");
  script = chunk.code;
});

afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

function page() {
  return {
    contract: "vibetable.plugin-query-page.v2", items: [{ id: "1", title: "最新数据" }],
    nextCursor: null, filteredRows: 1, totalRows: 1, schemaRevision: "schema-1", dataRevision: 1, complete: true,
  };
}

async function mountSurface() {
  document.body.innerHTML = '<p id="status">加载中</p><div id="summary"></div><select id="field"></select><input id="contains"><button id="refresh">刷新</button>';
  const query = vi.fn();
  const pending: { resolve: (value: ReturnType<typeof page>) => void; reject: (error: Error) => void }[] = [];
  query.mockImplementation(() => new Promise((resolve, reject) => pending.push({ resolve, reject })));
  const api = {
    context: { read: async () => ({ collection: "articles" }), onChanged: vi.fn() },
    data: {
      describe: async () => ({
        fields: [{ fieldId: "title", displayName: "标题", sortable: true, filterOperators: ["contains"] }],
        displayName: "文章",
      }),
      query,
      releaseCursor: vi.fn(),
    },
    dispose: vi.fn(),
  };
  vi.stubGlobal("overviewSdk", { createSurfaceClient: () => api });
  // Execute the actual surface entry and collectOverview in the existing jsdom environment.
  window.eval(script);
  await vi.waitFor(() => expect(pending).toHaveLength(1));
  const input = document.querySelector<HTMLInputElement>("#contains")!;
  input.value = "最新";
  input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
  await vi.waitFor(() => expect(pending).toHaveLength(2));
  return {
    pending,
    refresh: document.querySelector<HTMLButtonElement>("#refresh")!,
    field: document.querySelector<HTMLSelectElement>("#field")!,
    input,
    status: document.querySelector<HTMLParagraphElement>("#status")!,
    summary: document.querySelector<HTMLDivElement>("#summary")!,
  };
}

it.each(["success", "failure"])("keeps the latest completed overview after an older load settles with %s", async outcome => {
  const ui = await mountSurface();
  ui.pending[1]!.resolve(page());
  await vi.waitFor(() => expect(ui.summary.textContent).toContain("最新数据"));
  const latest = ui.summary.innerHTML;
  const status = ui.status.textContent;
  if (outcome === "success") {
    ui.pending[0]!.resolve({ ...page(), items: [{ id: "old", title: "过期数据" }] });
  } else {
    ui.pending[0]!.reject(new Error("过期请求失败"));
  }
  await vi.waitFor(() => expect(ui.refresh.disabled).toBe(false));
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(ui.summary.innerHTML).toBe(latest);
  expect(ui.status.textContent).toBe(status);
  expect(ui.field.value).toBe("title");
  expect(ui.input.disabled).toBe(false);
});

it("ignores an older failure and keeps refresh disabled while the latest load is pending", async () => {
  const ui = await mountSurface();
  ui.pending[0]!.reject(new Error("过期请求失败"));
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(ui.status.textContent).toBe("加载中");
  expect(ui.refresh.disabled).toBe(true);
  ui.pending[1]!.resolve(page());
  await vi.waitFor(() => expect(ui.summary.textContent).toContain("最新数据"));
  expect(ui.status.textContent).not.toContain("过期请求失败");
  expect(ui.refresh.disabled).toBe(false);
});
