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
    { fieldId: "amount", displayName: "数量", logicalType: "number", resultType: "number", readonly: false, nullable: true, display: null, options: [], constraints: null, filterOperators: ["eq", "gt"], sortable: true },
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
  const host = sdk.createOfflineSurfaceHost({
    pluginId: "com.vibetable.examples.normalize-text", workspaceId: "workspace-a", surfaceToken: "surface-a",
    risk: "write",
    context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["1", "2", "3"] },
    descriptions: { articles: description },
    permissions: { data: [{ collection: "$active", operations: ["read", "query", "update"], fields: ["*"] }] },
    rowGuards,
    queryAdapter: async query => {
      const offset = query.cursor ? Number(query.cursor) : 0;
      const items = rows.slice(offset, offset + 2);
      return { contract: "vibetable.plugin-query-page.v2", items,
        nextCursor: offset + items.length < rows.length ? String(offset + items.length) : null,
        filteredRows: rows.length, totalRows: rows.length, schemaRevision: "schema-1", dataRevision: 1, complete: true };
    },
    previewMutation: async plan => { plans.push(plan); },
    submitMutation: async plan => { outcomes.push(plan); return { status: "succeeded", taskId: `task-${outcomes.length}` }; },
    ...overrides,
  });
  const api = sdk.createSurfaceClient("surface-a", host.transport);
  return { api, host, plans, outcomes };
}

test("纯函数保持原值语义：仅收敛空白，不改写其他内容", () => {
  assert.equal(normalizeText("  Vibe   Table  "), "Vibe Table");
  assert.equal(normalizeText("Vibe"), "Vibe");
  assert.equal(normalizeText("\t a \n b "), "a b");
});

test("仅选中行产生受保护的更新计划，非字符串与无变化值被跳过", async () => {
  const { api, plans, outcomes } = fixture();
  try {
    const summary = await normalizeSelection(api);
    assert.match(summary, /已规范化 3 处文本/);
    assert.equal(plans.length, 1);
    const operations = plans[0].operations;
    assert.deepEqual(operations.map(op => [op.primaryKey, Object.keys(op.values)[0]]), [["1", "title"], ["1", "note"], ["3", "note"]]);
    assert.equal(operations[0].expectedDigest, digest(1));
    assert.equal(operations[0].values.title, "Vibe Table");
    assert.equal(operations[0].values.note, undefined);
    assert.equal(operations[2].values.note, "x y");
    assert.equal(outcomes.length, 1);
    // 未选中记录（id 4）不被读取修改。
    assert.ok(!JSON.stringify(plans).includes("未选择"));
  } finally { api.dispose(); }
});

test("无更新时直接完成，不请求确认", async () => {
  let confirmations = 0;
  const { api } = fixture({
    context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["5"] },
    previewMutation: async () => { confirmations += 1; },
  });
  try {
    assert.match(await normalizeSelection(api), /没有需要规范化的文本/);
    assert.equal(confirmations, 0);
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
  try { await assert.rejects(normalizeSelection(api), error => error.code === "plugin_guard_unavailable"); }
  finally { api.dispose(); }
});

test("选中记录缺失时要求重新选择；提前停止会释放剩余游标", async () => {
  const missing = fixture({ context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["1", "404"] } });
  try {
    await assert.rejects(collectNormalization(missing.api, "articles", description, new Set(["1", "404"])),
      error => error.code === "plugin_selection_stale");
  } finally { missing.api.dispose(); }
  // 选中第 1 行即停止分页：未消费的继续页句柄必须显式释放。
  const released = [];
  const early = fixture({ context: { projectKey: "local:workspace-a", collection: "articles", selectedKeys: ["1"] } });
  const originalRelease = early.api.data.releaseCursor.bind(early.api.data);
  early.api.data.releaseCursor = async cursor => { released.push(cursor); await originalRelease(cursor); };
  try {
    await normalizeSelection(early.api);
    assert.ok(released.length >= 1, "提前停止必须释放剩余句柄");
    assert.equal(early.host.cursorCount, 0);
  } finally { early.api.dispose(); }
});
