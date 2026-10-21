import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { mkdir } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const output = fileURLToPath(new URL(`../../../../build/qa/task486/normalize-${process.pid}/`, import.meta.url));
await mkdir(output, { recursive: true });
for (const [entry, name] of [["src/normalize-action.ts", "action.mjs"], ["tests/sdk-exports.ts", "sdk.mjs"]]) {
  await build({ absWorkingDir: root, entryPoints: [entry], outfile: `${output}/${name}`, bundle: true, format: "esm", platform: "neutral", target: "es2022", logLevel: "silent" });
}
const { normalizeText, collectNormalization, normalizeSelection } = await import(pathToFileURL(`${output}/action.mjs`));
const sdk = await import(pathToFileURL(`${output}/sdk.mjs`));

const digest = id => `sha256:${String(id).padStart(64, "0")}`;
const description = {
  contract: "vibetable.plugin-data.v2", collection: "articles", displayName: "合成表", schemaRevision: "schema-1",
  fields: [
    { fieldId: "id", displayName: "ID", logicalType: "text", resultType: "text", readonly: true, nullable: false, display: null, options: [], constraints: null, filterOperators: ["eq", "in"], sortable: true },
    { fieldId: "title", displayName: "标题", logicalType: "text", resultType: "text", readonly: false, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "contains"], sortable: true },
    { fieldId: "note", displayName: "备注", logicalType: "text", resultType: "text", readonly: false, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "contains"], sortable: false },
    { fieldId: "amount", displayName: "数量", logicalType: "number", resultType: "number", readonly: false, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "gte"], sortable: true },
  ],
};
const rows = [
  { id: "1", title: "  Vibe   Table  ", note: " keep ", amount: 1 },
  { id: "2", title: 0, note: null, amount: 2 },
  { id: "3", title: "clean", note: " x  y ", amount: 3 },
  { id: "4", title: " 未选择 ", note: "unchanged", amount: 4 },
  { id: "5", title: "already clean", note: "fixed", amount: 5 },
];
const rowGuards = { articles: { 1: digest(1), 2: digest(2), 3: digest(3), 5: digest(5) } };

function fixture(overrides = {}) {
  const plans = [];
  const outcomes = [];
  const queries = [];
  const host = sdk.createOfflineSurfaceHost({
    pluginId: "com.vibetable.examples.normalize-text", workspaceId: "workspace-a", surfaceToken: "surface-a",
    risk: "write",
    context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["1", "2", "3"] },
    descriptions: { articles: description },
    permissions: { data: [{ collection: "$active", operations: ["read", "query", "update"], fields: ["*"] }] },
    rowGuards,
    queryAdapter: async query => {
      queries.push(structuredClone(query));
      // The real authority answers an ids query with exactly those rows: a
      // full-table scan is never requested for selected-record normalization.
      assert.ok(Array.isArray(query.ids), "normalization must query by selected ids");
      assert.ok(query.ids.length <= 200, "ids batches respect the 200-item contract limit");
      const wanted = new Set(query.ids.map(String));
      const items = rows.filter(row => wanted.has(String(row.id)));
      return { contract: "vibetable.plugin-query-page.v2", items, nextCursor: null,
        filteredRows: items.length, totalRows: rows.length, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
    previewMutation: async plan => { plans.push(plan); },
    submitMutation: async plan => { outcomes.push(plan); return { status: "succeeded", taskId: `task-${outcomes.length}` }; },
    ...overrides,
  });
  const api = sdk.createSurfaceClient("surface-a", host.transport);
  return { api, host, plans, outcomes, queries };
}

test("纯函数保持原值语义：仅收敛空白，不改写其他内容", () => {
  assert.equal(normalizeText("  Vibe   Table  "), "Vibe Table");
  assert.equal(normalizeText("Vibe"), "Vibe");
  assert.equal(normalizeText("\t a \n b "), "a b");
});

test("同一记录的多字段变更合并为一个 update，字段变更数独立统计（Go 顺序 apply 规则）", async () => {
  const { api, plans, queries } = fixture();
  try {
    const scan = await collectNormalization(api, "articles", description, new Set(["1", "2", "3"]));
    // 记录 1 的 title+note 合并为一个 update（同一原始 expectedDigest 只出现一次）；
    // 记录 3 只有 note 变化。真实 Go 按顺序 apply：若同一行有两个携带同一原始
    // digest 的 update，第二个必然 digest_conflict 并回滚整批。
    assert.equal(scan.operations.length, 2);
    assert.deepEqual(scan.operations.map(op => op.primaryKey), ["1", "3"]);
    assert.deepEqual(scan.operations[0].values, { title: "Vibe Table", note: "keep" });
    assert.equal(scan.operations[0].expectedDigest, digest(1));
    assert.deepEqual(scan.operations[1].values, { note: "x y" });
    assert.equal(scan.changedFields, 3);
    assert.equal(scan.skipped, 3);
    assert.equal(scan.scanned, 3);
    // 查询按选中 ids 直达，不携带未选中记录。
    assert.deepEqual(queries[0].ids, ["1", "2", "3"]);
    assert.ok(queries.every(query => !query.ids.includes("4")));
    assert.ok(queries.every(query => query.fields.includes("id") && query.fields.includes("title") && query.fields.includes("note")));
    // 端到端摘要按“变化文本值数”计数，与记录数（操作数）区分。
    const summary = await normalizeSelection(api);
    assert.match(summary, /已规范化 3 处文本/);
    assert.equal(plans.length, 1);
    assert.equal(plans[0].operations.length, 2);
  } finally { api.dispose(); }
});

test("超过 200 条选中按 ids 分批，仍保持 guard、精确计数与单批内合并", async () => {
  const bigRows = Array.from({ length: 405 }, (_, index) => ({
    id: String(index + 1), title: `  t ${index}  `, note: index % 7 === 0 ? null : ` n${index} `, amount: index,
  }));
  const bigGuards = { articles: Object.fromEntries(bigRows.map(row => [row.id, digest(row.id)])) };
  const batchSizes = [];
  const { api } = fixture({
    context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: bigRows.map(row => row.id) },
    rowGuards: bigGuards,
    queryAdapter: async query => {
      batchSizes.push(query.ids.length);
      const wanted = new Set(query.ids.map(String));
      const items = bigRows.filter(row => wanted.has(String(row.id)));
      return { contract: "vibetable.plugin-query-page.v2", items, nextCursor: null,
        filteredRows: items.length, totalRows: bigRows.length, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
  });
  try {
    const scan = await collectNormalization(api, "articles", description, new Set(bigRows.map(row => row.id)));
    assert.deepEqual(batchSizes, [200, 200, 5]);
    assert.equal(scan.scanned, 405);
    assert.equal(scan.operations.length, 405);
    // 每条记录一个 update；带 note 的行合并两个字段值，无 note 的行只有 title。
    // 索引 0,7,14,…共 58 行 note 为 null（非字符串跳过），其余 347 行 note 变化。
    const nullNotes = 58;
    const withNote = scan.operations.filter(op => op.values.note !== undefined);
    assert.equal(withNote.length, 405 - nullNotes);
    assert.equal(scan.changedFields, 405 + (405 - nullNotes));
    assert.equal(scan.skipped, nullNotes);
    assert.ok(scan.operations.every(op => op.expectedDigest === digest(op.primaryKey)));
  } finally { api.dispose(); }
});

test("ids 查询若被权威分页则跟随续页到空，不泄漏任何句柄", async () => {
  const cursorFollows = [];
  const { api, host } = fixture({
    queryAdapter: async query => {
      cursorFollows.push(query.cursor ?? null);
      const wanted = new Set(query.ids.map(String));
      const items = rows.filter(row => wanted.has(String(row.id)));
      // 权威把同一批 ids 拆成两页：首页带 nextCursor，末页为空。
      if (!query.cursor) {
        return { contract: "vibetable.plugin-query-page.v2", items: items.slice(0, 2),
          nextCursor: "ids-page-2", filteredRows: items.length, totalRows: rows.length,
          schemaRevision: "schema-1", dataRevision: 1, complete: true };
      }
      return { contract: "vibetable.plugin-query-page.v2", items: items.slice(2),
        nextCursor: null, filteredRows: items.length, totalRows: rows.length,
        schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
  });
  try {
    const scan = await collectNormalization(api, "articles", description, new Set(["1", "2", "3"]));
    assert.equal(scan.scanned, 3);
    assert.deepEqual(cursorFollows, [null, "ids-page-2"]);
    assert.equal(host.cursorCount, 0, "续页句柄必须波消费完毕，不残留");
  } finally { api.dispose(); }
});

test("处理中途失败时释放未消费的续页句柄，fail closed 不产生部分提交", async () => {
  const released = [];
  const { api, host } = fixture({
    rowGuards: { articles: { 3: digest(3) } }, // 首页记录 1 缺 guard → 首页即失败
    queryAdapter: async query => {
      const wanted = new Set(query.ids.map(String));
      const items = rows.filter(row => wanted.has(String(row.id)));
      return { contract: "vibetable.plugin-query-page.v2", items, nextCursor: "leak-check",
        filteredRows: items.length, totalRows: rows.length, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
  });
  const originalRelease = api.data.releaseCursor.bind(api.data);
  api.data.releaseCursor = async cursor => { released.push(cursor); await originalRelease(cursor); };
  try {
    await assert.rejects(collectNormalization(api, "articles", description, new Set(["1", "2", "3"])),
      error => error.code === "plugin_guard_unavailable");
    // 宿主把权威 nextCursor 重签为 opaque 句柄，原文不外泄；此处断言失败路径
    // 确实调用了显式释放且宿主句柄归零。
    assert.equal(released.length, 1, "失败路径必须释放本页未消费句柄");
    assert.equal(host.cursorCount, 0);
  } finally { api.dispose(); }
});

test("选中记录缺失时按实际返回计数并要求重新选择", async () => {
  const { api } = fixture({ context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["1", "404"] } });
  try {
    await assert.rejects(collectNormalization(api, "articles", description, new Set(["1", "404"])),
      error => error.code === "plugin_selection_stale");
  } finally { api.dispose(); }
});

test("无更新时直接完成且不请求确认；未选中记录不读取", async () => {
  let confirmations = 0;
  const { api, queries } = fixture({
    context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["5"] },
    previewMutation: async () => { confirmations += 1; },
  });
  try {
    const summary = await normalizeSelection(api);
    assert.match(summary, /扫描 1 条选中记录：没有需要规范化的文本/);
    assert.equal(confirmations, 0);
    assert.deepEqual(queries.map(query => query.ids), [["5"]]);
  } finally { api.dispose(); }
});

test("宿主拒绝确认时报告明确拒绝，不产生提交", async () => {
  const { api } = fixture({
    submitMutation: async () => { throw new sdk.PluginCapabilityError("plugin_mutation_rejected", "拒绝"); },
  });
  try {
    await assert.rejects(normalizeSelection(api), error => error.code === "plugin_mutation_rejected");
  } finally { api.dispose(); }
});

test("没有选中或没有活动表时明确失败", async () => {
  for (const context of [{ projectKey: "local:workspace-a", collection: null, selectedKeys: [] },
    { projectKey: "local:workspace-a", collection: "articles", selectedKeys: [] }]) {
    const { api } = fixture({ context });
    try { await assert.rejects(normalizeSelection(api), error => error.code === "plugin_input_invalid"); }
    finally { api.dispose(); }
  }
});

test("缺少并发保护时 fail closed，不生成部分计划", async () => {
  const { api } = fixture({ rowGuards: { articles: {} } });
  try { await assert.rejects(collectNormalization(api, "articles", description, new Set(["1", "2", "3"])), error => error.code === "plugin_guard_unavailable"); }
  finally { api.dispose(); }
});
