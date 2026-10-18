import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath, pathToFileURL } from "node:url";
const root = fileURLToPath(new URL("../", import.meta.url));
const output = fileURLToPath(new URL(`../../../../build/qa/task484/offline-${process.pid}/`, import.meta.url));
await mkdir(output, { recursive: true });
for (const [entry, name] of [["src/records.ts", "records.mjs"], ["tests/sdk-exports.ts", "sdk.mjs"], ["src/audit-action.ts", "audit.mjs"]]) {
  await build({ absWorkingDir: root, entryPoints: [entry], outfile: `${output}/${name}`, bundle: true, format: "esm", platform: "neutral", target: "es2022", logLevel: "silent" });
}
const records = await import(pathToFileURL(`${output}/records.mjs`));
const sdk = await import(pathToFileURL(`${output}/sdk.mjs`));
const { runAudit } = await import(pathToFileURL(`${output}/audit.mjs`));
// This independent corpus is produced/checked by the Go authority contract tests.
const corpus = JSON.parse(await readFile(new URL("../../../../tests/contract/fixtures/plugin-capabilities-v2.json", import.meta.url), "utf8"));
const DESCRIPTION = corpus.description;
const request = corpus.request;
const permissions = { data: [{ collection: "$active", operations: ["read", "query", "create", "update", "delete"], fields: ["*"] }], privateStorage: true };
const code = expected => error => error.code === expected;
function fixture(overrides = {}) {
  const queries = [];
  let revision = "1";
  const host = sdk.createOfflineSurfaceHost({
    pluginId: "plugin-a", workspaceId: "workspace-a", surfaceToken: "surface-a", risk: "destructive",
    context: { projectKey: "local:workspace-a", collection: DESCRIPTION.collection },
    descriptions: { [DESCRIPTION.collection]: DESCRIPTION }, permissions,
    revision: () => revision,
    queryAdapter: async query => {
      queries.push(query);
      const index = query.cursor ? Number(query.cursor.split("-").at(-1)) - 1 : 0;
      const page = structuredClone(corpus.pages[index]);
      // The corpus is used only for its exact producer request; specialized ports must be explicit.
      assert.deepEqual({ ...query, cursor: undefined }, { ...request, cursor: undefined });
      return page;
    }, ...overrides,
  });
  const api = sdk.createSurfaceClient(overrides.surfaceToken ?? "surface-a", host.transport);
  return { api, host, queries, changeRevision: () => revision = "2" };
}
const queryThrough = api => (request, signal) => api.data.query(request, signal);

test("真实 Go 550 条语料经 surface SDK 分页：0/false/null、总数与末页", async () => {
  const { api, queries } = fixture();
  try {
    const fold = await records.foldAllPages(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, request.fields, { pageSize: request.pageSize, sorts: request.sorts });
    assert.equal(fold.totalRows, 550); assert.equal(fold.rows.length, 550); assert.equal(fold.pagesRead, 3);
    assert.ok(fold.rows.some(row => row.fld_amount === 0)); assert.ok(fold.rows.some(row => row.fld_amount === null)); assert.ok(fold.rows.some(row => row.fld_enabled === false));
    const pager = records.createPager(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, request.fields, 200, request.sorts);
    assert.equal((await pager.open()).hasPrev, false);
    assert.equal((await pager.next()).pageIndex, 1);
    const last = await pager.next(); assert.equal(last.items.length, 150); assert.equal(last.hasNext, false);
    await assert.rejects(pager.next(), /末页/);
    assert.equal((await pager.prev()).pageIndex, 1);
    assert.ok(queries.length > 3);
  } finally { api.dispose(); }
});
test("独立语料反例与真实查询错误码一致，失败不进入 authority port", async () => {
  for (const item of corpus.rejectCases) {
    const { api, queries } = fixture();
    try { await assert.rejects(api.data.query({ ...request, ...item.patch }), code(item.code), item.name); assert.equal(queries.length, 0); }
    finally { api.dispose(); }
  }
});
test("游标绑定精确请求、会话、工作区和 data revision，且单次消费", async () => {
  const a = fixture(); const b = fixture({ workspaceId: "workspace-b", surfaceToken: "surface-b" });
  try {
    const first = await a.api.data.query(request);
    await assert.rejects(b.api.data.query({ ...request, cursor: first.nextCursor }), code("plugin_cursor_invalid"));
    const second = await a.api.data.query({ ...request, cursor: first.nextCursor });
    await assert.rejects(a.api.data.query({ ...request, cursor: first.nextCursor }), code("plugin_cursor_invalid"));
    a.changeRevision();
    await assert.rejects(a.api.data.query({ ...request, cursor: second.nextCursor }), code("plugin_cursor_stale"));
  } finally { a.api.dispose(); b.api.dispose(); }
});
test("空表、不完整计算、分页失败/取消不会推进已显示页", async () => {
  let fail = false;
  const { api } = fixture({ queryAdapter: async () => {
    if (fail) throw new sdk.PluginCapabilityError("plugin_query_failed", "synthetic failure");
    return { ...corpus.pages[0], items: [], totalRows: 0, nextCursor: null };
  } });
  try {
    const pager = records.createPager(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, ["id"]);
    const view = await pager.open(); assert.equal(view.empty, true); assert.equal(view.hasNext, false);
    assert.equal(records.overviewSummary(DESCRIPTION, { totalRows: 0, complete: true }).totalRows, 0);
    fail = true; await assert.rejects(pager.open()); assert.equal(pager.view, view);
    const abort = new AbortController(); abort.abort(); await assert.rejects(pager.open(abort.signal), code("plugin_cancel_requested")); assert.equal(pager.view, view);
    await assert.rejects(records.foldAllPages(async () => ({ ...corpus.pages[0], complete: false }), async () => {}, request.collection, request.fields), /未把不完整结果/);
  } finally { api.dispose(); }
});
test("中间页失败后 next 重试仍读取同一目标页；数据收缩不跳页", async () => {
  let fail = false, shrink = false;
  const { api, changeRevision } = fixture({ queryAdapter: async query => {
    if (fail && query.cursor) throw new sdk.PluginCapabilityError("plugin_query_failed", "fail");
    const index = query.cursor ? Number(query.cursor.split("-").at(-1)) - 1 : 0;
    return { ...corpus.pages[index], ...(shrink ? { nextCursor: null } : {}) };
  } });
  try {
    const pager = records.createPager(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, request.fields, 200, request.sorts);
    await pager.open(); fail = true; await assert.rejects(pager.next()); assert.equal(pager.view.pageIndex, 0);
    fail = false; assert.equal((await pager.next()).pageIndex, 1);
    shrink = true; changeRevision();
    await assert.rejects(pager.next(), code("plugin_cursor_stale"));
    await assert.rejects(pager.next(), /页已不存在/); assert.equal(pager.view.pageIndex, 1);
  } finally { api.dispose(); }
});
const plan = { contract: "vibetable.surface-mutation-plan.v1", collection: request.collection, schemaRevision: DESCRIPTION.schemaRevision, operations: [{ kind: "create", values: { fld_amount: 0 } }] };
test("共享终态语料与真实 Host 同合同；没有默认成功或自动重放", async () => {
  const outcomes = JSON.parse(await readFile(new URL("../../../../tests/contract/fixtures/plugin-surface-outcomes-v1.json", import.meta.url), "utf8"));
  for (const item of outcomes.cases) {
    let submits = 0;
    const { api } = fixture({
      risk: item.scenario === "read-only" ? "read" : "destructive",
      pluginApi: item.scenario === "unsupported-protocol" ? "2.x" : "3.x",
      previewMutation: async () => {}, submitMutation: async () => {
        submits++;
        if (item.scenario === "cancel-before-approval") throw new sdk.PluginCapabilityError("plugin_cancel_requested", "Host cancelled before approval");
        if (item.scenario === "conflict") throw new sdk.PluginCapabilityError("mutation.digest_conflict", "Authority rolled back the guarded mutation");
        if (["lost-receipt", "incomplete-receipt"].includes(item.scenario)) throw new sdk.PluginCapabilityError("plugin_commit_unknown", "No complete commit receipt");
        return { status: item.scenario === "rejected" ? "rejected" : "succeeded", taskId: "task" };
      },
    });
    try {
      const operation = async () => {
        if (item.scenario === "unsupported-protocol") return api.context.read();
        const preview = await api.mutation.preview(plan);
        try { return await api.mutation.submit(preview.planId); }
        finally { await assert.rejects(api.mutation.submit(preview.planId), code("plugin_plan_stale")); }
      };
      if (item.code) await assert.rejects(operation(), code(item.code), item.scenario);
      else assert.equal((await operation()).status, item.status, item.scenario);
      assert.equal(submits, ["read-only", "unsupported-protocol"].includes(item.scenario) ? 0 : 1);
    } finally { api.dispose(); }
  }
  assert.deepEqual(records.describeMutationFailure({ code: "plugin_commit_unknown" }), { status: "unknown", retry: false });
  const missing = fixture(); await assert.rejects(missing.api.mutation.preview(plan), code("plugin_capability_invalid")); missing.api.dispose();
});
test("在途查询真实 AbortSignal 与提交取消请求经消息合同传递", async () => {
  let release; const gate = new Promise(resolve => release = resolve);
  const query = fixture({ queryAdapter: async () => { await gate; return corpus.pages[0]; } });
  const abort = new AbortController(); const pending = query.api.data.query(request, abort.signal); abort.abort(); release();
  await assert.rejects(pending, code("plugin_cancel_requested")); query.api.dispose();
  const mutation = fixture({ previewMutation: async () => {}, submitMutation: (_plan, signal) => new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(new sdk.PluginCapabilityError("plugin_cancel_requested", "cancel before approval")), { once: true })) });
  const preview = await mutation.api.mutation.preview(plan); const cancel = new AbortController();
  const submitted = mutation.api.mutation.submit(preview.planId, cancel.signal); cancel.abort();
  await assert.rejects(submitted, code("plugin_cancel_requested")); mutation.api.dispose();
});
test("SDK deadline：普通请求 timeout、提交 unknown，不重放", async t => {
  const api = sdk.createSurfaceClient("silent-host", { postMessage() {}, addEventListener() {}, removeEventListener() {} });
  t.mock.timers.enable({ apis: ["setTimeout"] });
  try {
    const query = assert.rejects(api.data.query(request), code("plugin_timeout")); t.mock.timers.tick(20000); await query;
    const submit = assert.rejects(api.mutation.submit("lost-receipt"), code("plugin_commit_unknown")); t.mock.timers.tick(125000); await submit;
  } finally { api.dispose(); }
});
test("表单只回写已改原值：空文本/null、0/false、JSON数组与关系", () => {
  const field = (fieldId, logicalType, resultType = logicalType) => ({ ...DESCRIPTION.fields[1], fieldId, displayName: fieldId, logicalType, resultType, readonly: false, nullable: true });
  const text = field("title", "text"), json = field("data", "json"), relation = field("owner", "relation");
  const description = { ...DESCRIPTION, fields: [text, json, relation, ...DESCRIPTION.fields] };
  assert.equal(records.parseFormValue(text, ""), ""); assert.equal(records.parseFormValue(text, "", true), null);
  assert.equal(records.parseFormValue(DESCRIPTION.fields[1], "0"), 0); assert.equal(records.parseFormValue(DESCRIPTION.fields[3], "false"), false);
  assert.equal(records.formInputValue(json, "raw text"), '"raw text"');
  assert.deepEqual(records.parseFormValue(json, '[0,false,null]'), [0, false, null]); assert.equal(records.parseFormValue(relation, "target-record"), "target-record");
  assert.deepEqual(records.formValues(description, new Map([["title", { text: "", isNull: false, changed: false }], ["data", { text: '[0,false,null]', isNull: false, changed: false }], ["owner", { text: "target-record", isNull: false, changed: false }]]), false), {});
  assert.throws(() => records.parseFormValue(json, "{"), /JSON/);
});
test("批量规范化只更新有 guard 的变化原文，不写显示文本", () => {
  const description = { ...DESCRIPTION, fields: [...DESCRIPTION.fields, { ...DESCRIPTION.fields[0], fieldId: "title", displayName: "Title", readonly: false }] };
  assert.throws(() => records.buildNormalizationPlan(DESCRIPTION, [], {}, "id"), /授权/);
  const preview = records.buildNormalizationPlan(description, [{ id: "one", title: " a  b " }, { id: "two", title: "" }, { id: "three", title: " c " }, { id: "four", title: null }], { one: "go-owned-guard" }, "title");
  assert.deepEqual(preview.plan.operations, [{ kind: "update", primaryKey: "one", expectedDigest: "go-owned-guard", values: { title: "a b" } }]);
  assert.equal(preview.unchanged, 2); assert.equal(preview.skipped, 1);
});
test("两个插件 × 两个工作区存储持久隔离，重开共享本身份，退役句柄拒绝", async () => {
  const storage = new Map(); const sessions = [];
  try {
    for (const pluginId of ["plugin-a", "plugin-b"]) for (const workspaceId of ["workspace-a", "workspace-b"]) {
      const item = fixture({ pluginId, workspaceId, storage, surfaceToken: `${pluginId}:${workspaceId}` }); sessions.push(item);
      await item.api.storage.set("settings", `${pluginId}/${workspaceId}`);
    }
    for (let i = 0; i < sessions.length; i++) assert.equal(await sessions[i].api.storage.get("settings"), ["plugin-a/workspace-a", "plugin-a/workspace-b", "plugin-b/workspace-a", "plugin-b/workspace-b"][i]);
    const reopened = fixture({ pluginId: "plugin-a", workspaceId: "workspace-a", surfaceToken: "new-token", storage }); sessions.push(reopened);
    assert.equal(await reopened.api.storage.get("settings"), "plugin-a/workspace-a");
    sessions[0].host.retire(); await assert.rejects(sessions[0].api.data.query(request), code("plugin_source_denied"));
  } finally { for (const item of sessions) item.api.dispose(); }
});
test("无 UI 动作真实消费者：仅通过 SDK 读元数据与权威计数、存储快照", async () => {
  const { api } = fixture({ queryAdapter: async query => { assert.deepEqual(query.fields, ["id"]); assert.equal(query.pageSize, 1); return { ...corpus.pages[0], items: corpus.pages[0].items.slice(0, 1), nextCursor: "remaining" }; } });
  try {
    assert.match(await runAudit(api), /权威计数 550 条/);
    const snapshot = await api.storage.get("auditSnapshot"); assert.equal(snapshot.totalRows, 550); assert.equal(snapshot.collection, request.collection); assert.equal(snapshot.schemaRevision, DESCRIPTION.schemaRevision);
  } finally { api.dispose(); }
});


test("离线 surface 不制造未授权/只读字段、旧schema或缺adapter的成功", async () => {
  const { api } = fixture({ previewMutation: async () => {} });
  try {
    await assert.rejects(api.mutation.preview({ ...plan, schemaRevision: "old-schema" }), code("plugin_cursor_stale"));
    await assert.rejects(api.mutation.preview({ ...plan, operations: [{ kind: "create", values: { id: "forged" } }] }), code("plugin_write_denied"));
    await assert.rejects(api.mutation.preview({ ...plan, operations: [{ kind: "create", values: { hidden: "forged" } }] }), code("plugin_write_denied"));
    await assert.rejects(api.data.query({ ...request, collection: "forbidden" }), code("plugin_read_denied"));
    for (let i = 0; i < 100; i++) await api.context.read();
  } finally { api.dispose(); }
});


test("共享 Go 反例：只授业务字段时不能凭 primaryKey 更新或删除", async () => {
  const corpus = JSON.parse(await readFile(new URL("../../../../tests/contract/fixtures/plugin-surface-outcomes-v1.json", import.meta.url), "utf8"));
  let previews = 0, submits = 0;
  const { api } = fixture({ permissions: { data: [{ collection: "$active", operations: ["read", "query", "update", "delete"], fields: ["fld_amount"] }] }, previewMutation: async () => { previews++; }, submitMutation: async () => { submits++; return { status: "succeeded", taskId: "must-not-submit" }; } });
  try {
    for (const item of corpus.idGrantCases) {
      const operation = { kind: item.operation, primaryKey: "ungranted-record", expectedDigest: "invented-guard", ...(item.operation === "update" ? { values: { fld_amount: 77 } } : {}) };
      await assert.rejects(api.mutation.preview({ ...plan, operations: [operation] }), code(item.code));
    }
    assert.equal(previews, 0); assert.equal(submits, 0);
  } finally { api.dispose(); }
});


test("550/50真实分页往返100次：前进一步只读一页，游标始终至多1个", async () => {
  const rows = corpus.pages.flatMap(page => page.items); let calls = 0;
  const { api, host } = fixture({ queryAdapter: async query => {
    calls++; const offset = query.cursor ? Number(query.cursor) : 0;
    return { ...corpus.pages[0], items: rows.slice(offset, offset + 50), nextCursor: offset + 50 < rows.length ? String(offset + 50) : null };
  } });
  const pager = records.createPager(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, request.fields, 50, request.sorts);
  try {
    await pager.open(); assert.equal(host.cursorCount, 1);
    for (let i = 0; i < 100; i++) {
      const before = calls;
      assert.equal((await pager.next()).pageIndex, 1); assert.equal(calls, before + 1); assert.equal(host.cursorCount, 1);
      assert.equal((await pager.prev()).pageIndex, 0); assert.equal(host.cursorCount, 1);
    }
    await pager.close(); assert.equal(host.cursorCount, 0);
  } finally { api.dispose(); }
});
test("离线与Host共享64游标容量：第65个拒绝，释放后恢复且跨客户端不制造额度", async () => {
  const { api, host } = fixture(); const handles = [];
  const peer = sdk.createSurfaceClient("surface-a", host.transport);
  try {
    for (let i = 0; i < 64; i++) handles.push((await api.data.query(request)).nextCursor);
    assert.equal(host.cursorCount, 64);
    await assert.rejects(peer.data.query(request), code("plugin_query_limit"));
    await api.data.releaseCursor(handles[0]); assert.equal(host.cursorCount, 63);
    await peer.data.query(request); assert.equal(host.cursorCount, 64);
  } finally { peer.dispose(); api.dispose(); }
  assert.equal(host.cursorCount, 0);
});
test("失效/取消/关闭旧pager：迟到cursor释放，旧页面不覆盖，容量回到零", async () => {
  let resolve; let delayed = false;
  const { api, host, changeRevision } = fixture({ queryAdapter: async query => {
    if (delayed) await new Promise(done => resolve = done);
    return query.cursor ? corpus.pages[1] : corpus.pages[0];
  } });
  const pager = records.createPager(queryThrough(api), cursor => api.data.releaseCursor(cursor), request.collection, request.fields, 200, request.sorts);
  try {
    const first = await pager.open(); changeRevision();
    await assert.rejects(pager.next(), code("plugin_cursor_stale")); assert.equal(pager.view, first); assert.equal(host.cursorCount, 0);
    delayed = true; const pending = pager.next(); const outcome = assert.rejects(pending, code("plugin_cancel_requested"));
    await new Promise(done => setImmediate(done));
    const closed = pager.close(); resolve(); await outcome; await closed;
    assert.equal(pager.view, first); assert.equal(host.cursorCount, 0);
  } finally { api.dispose(); }
});


test("分页查询失败且释放失败时保留原句柄，显式关闭才能重试清理", async () => {
  let rejectRelease = true, releases = 0;
  const { api, host, changeRevision } = fixture();
  const pager = records.createPager(queryThrough(api), async cursor => {
    releases++;
    if (rejectRelease) throw new Error("release receipt lost");
    await api.data.releaseCursor(cursor);
  }, request.collection, request.fields, 200, request.sorts);
  try {
    const first = await pager.open(); changeRevision();
    await assert.rejects(pager.next(), /release receipt lost/);
    assert.equal(pager.view, first); assert.equal(releases, 1);
    await new Promise(done => setImmediate(done)); assert.equal(releases, 1);
    rejectRelease = false; await pager.close(); assert.equal(releases, 2); assert.equal(host.cursorCount, 0);
  } finally { api.dispose(); }
});
