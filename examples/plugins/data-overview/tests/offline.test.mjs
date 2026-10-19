import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { mkdir } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const output = fileURLToPath(new URL(`../../../../build/qa/task486/overview-${process.pid}/`, import.meta.url));
await mkdir(output, { recursive: true });
for (const [entry, name] of [["src/overview-action.ts", "overview.mjs"], ["tests/sdk-exports.ts", "sdk.mjs"]]) {
  await build({ absWorkingDir: root, entryPoints: [entry], outfile: `${output}/${name}`, bundle: true, format: "esm", platform: "neutral", target: "es2022", logLevel: "silent" });
}
const { collectOverview, defaultField } = await import(pathToFileURL(`${output}/overview.mjs`));
const sdk = await import(pathToFileURL(`${output}/sdk.mjs`));

const description = {
  contract: "vibetable.plugin-data.v2", collection: "articles", displayName: "合成表", schemaRevision: "schema-1",
  fields: [
    { fieldId: "id", displayName: "ID", logicalType: "text", resultType: "text", readonly: true, nullable: false, display: null, options: [], constraints: null, filterOperators: ["eq", "in"], sortable: true },
    { fieldId: "title", displayName: "标题", logicalType: "text", resultType: "text", readonly: true, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "contains"], sortable: true },
    { fieldId: "amount", displayName: "数量", logicalType: "number", resultType: "number", readonly: true, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "gte"], sortable: false },
  ],
};
const code = expected => error => error.code === expected;

function fixture(total, overrides = {}) {
  const requests = [];
  const host = sdk.createOfflineSurfaceHost({
    pluginId: "com.vibetable.examples.data-overview", workspaceId: "workspace-a", surfaceToken: "surface-a",
    context: { projectKey: "local:workspace-a", collection: "articles" },
    descriptions: { articles: description },
    permissions: { data: [{ collection: "$active", operations: ["read", "query"], fields: ["*"] }] },
    queryAdapter: async query => {
      requests.push(structuredClone(query));
      const offset = query.cursor ? Number(query.cursor) : 0;
      const size = query.pageSize ?? 100;
      const items = Array.from({ length: Math.min(size, total - offset) }, (_, index) => ({
        id: String(offset + index + 1),
        title: offset + index < 2 ? null : `row-${String(offset + index).padStart(3, "0")}`,
      }));
      return { contract: "vibetable.plugin-query-page.v2", items,
        nextCursor: offset + items.length < total ? String(offset + items.length) : null,
        filteredRows: total, totalRows: total, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
    ...overrides,
  });
  const api = sdk.createSurfaceClient("surface-a", host.transport);
  return { api, host, requests };
}

test("无活动表时明确失败", async () => {
  const { api } = fixture(0, { context: { projectKey: "local:workspace-a", collection: null } });
  try { await assert.rejects(collectOverview(api, {}), code("plugin_input_invalid")); }
  finally { api.dispose(); }
});

for (const total of [0, 550, 6200]) {
  test(`全量分页统计 ${total} 行：逐页消费游标并汇总权威计数`, async () => {
    const { api, requests } = fixture(total);
    try {
      const { stats } = await collectOverview(api, {});
      assert.equal(stats.count, total);
      assert.equal(stats.totalRows, total);
      assert.equal(stats.filteredRows, total);
      assert.equal(stats.fieldId, "title");
      assert.equal(stats.nonNullCount, Math.max(0, total - 2));
      assert.equal(stats.pagesRead, Math.max(1, Math.ceil(total / 200)));
      assert.ok(requests.every(query => query.fields.includes("id")));
    } finally { api.dispose(); }
  });
}

test("contains 筛选进入权威查询；未授权字段被拒绝", async () => {
  const { api, requests } = fixture(550);
  try {
    const { stats } = await collectOverview(api, { fieldId: "title", contains: "row-" });
    assert.equal(stats.count, 550);
    assert.deepEqual(requests[0].filters, [{ field: "title", operator: "contains", value: "row-" }]);
    await assert.rejects(collectOverview(api, { fieldId: "fld_unauthorized" }), code("plugin_read_denied"));
  } finally { api.dispose(); }
});

test("显式选择不可排序字段时不排序仍完整分页；默认聚焦只选可排序字段", async () => {
  const descriptionNoSortable = { ...description,
    fields: [description.fields[0], { ...description.fields[2], sortable: false }] };
  const { api, requests } = fixture(3, { descriptions: { articles: descriptionNoSortable } });
  try {
    const { stats } = await collectOverview(api, { fieldId: "amount" });
    assert.equal(stats.fieldId, "amount");
    assert.deepEqual(requests[0].sorts, []);
    assert.equal(stats.count, 3);
    assert.equal(defaultField(descriptionNoSortable), null);
  } finally { api.dispose(); }
});

test("计算未就绪时不把截断当全量：complete 反映到结果；释放未消费句柄", async () => {
  const released = [];
  let incomplete = false;
  const { api, host } = fixture(450, { queryAdapter: async query => {
    const offset = query.cursor ? Number(query.cursor) : 0;
    const items = Array.from({ length: Math.min(200, 450 - offset) }, (_, index) => ({ id: String(offset + index + 1) }));
    return { contract: "vibetable.plugin-query-page.v2", items,
      nextCursor: offset + items.length < 450 ? String(offset + items.length) : null,
      filteredRows: 450, totalRows: 450, schemaRevision: "schema-1", dataRevision: 1, complete: !incomplete };
  } });
  const originalRelease = api.data.releaseCursor.bind(api.data);
  api.data.releaseCursor = async cursor => { released.push(cursor); await originalRelease(cursor); };
  try {
    const ready = await collectOverview(api, {});
    assert.equal(ready.stats.complete, true);
    incomplete = true;
    const stale = await collectOverview(api, {});
    assert.equal(stale.stats.complete, false);
    assert.equal(stale.stats.count, 450);
    assert.equal(host.cursorCount, 0);
  } finally { api.dispose(); }
});

test("权威计数与分页累计不一致时拒绝，不误报全量", async () => {
  const { api } = fixture(550, { queryAdapter: async query => {
    const offset = query.cursor ? Number(query.cursor) : 0;
    const items = Array.from({ length: Math.min(200, 400 - offset) }, (_, index) => ({ id: String(offset + index + 1) }));
    return { contract: "vibetable.plugin-query-page.v2", items,
      nextCursor: offset + items.length < 400 ? String(offset + items.length) : null,
      filteredRows: 550, totalRows: 550, schemaRevision: "schema-1", dataRevision: 1, complete: true };
  } });
  try { await assert.rejects(collectOverview(api, {}), code("plugin_query_failed")); }
  finally { api.dispose(); }
});

test("defaultField 选择第一个可排序的非 id 字段", () => {
  assert.equal(defaultField(description), "title");
  assert.equal(defaultField({ ...description, fields: [description.fields[0]] }), null);
});
