import assert from "node:assert/strict";
import test from "node:test";
import { sdk } from "./load-sdk.mjs";

function parentChannel() {
  const listeners = new Set();
  const parent = { postMessage: message => calls.push(message) };
  const calls = [];
  return { calls, parent, addEventListener: (_, f) => listeners.add(f), removeEventListener: (_, f) => listeners.delete(f),
    emit: (event, payload, overrides = {}) => { for (const f of listeners) f({ origin: "https://app.vibetable.local", source: parent,
      data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "s1", event, payload }, ...overrides }); },
    reply(request, result) { this.emit("uiResponse", { requestId: request.payload.requestId, result }); }, listeners };
}
const native = () => ({ postMessage() {}, addEventListener() {}, removeEventListener() {} });
test("public surface SDK consumes trusted current presentation and owned tasks, never approval", async () => {
  const parent = parentChannel();
  const client = sdk.createSurfaceClient("s1", native(), parent);
  const showing = client.ui.notifications.show("保存结果请核对");
  assert.equal(parent.calls.at(-1).payload.method, "notification.show");
  parent.reply(parent.calls.at(-1), { shown: true });
  assert.deepEqual(await showing, { shown: true });
  const themes = [];
  client.context.onChanged(value => themes.push(value));
  const theme = { contract: "vibetable.plugin-theme.v1", mode: "dark", locale: "en-US", density: "compact", variables: {} };
  parent.emit("themeChanged", theme, { origin: "https://evil.test" });
  parent.emit("themeChanged", theme, { source: {} });
  assert.equal(themes.length, 0);
  parent.emit("themeChanged", theme);
  assert.equal(themes.at(-1).locale, "en-US");
  const seen = [];
  client.tasks.onChanged(tasks => seen.push(tasks));
  parent.emit("tasksChanged", [{ taskId: "t1", state: "aborted", revision: 2, cancelRequested: false,
    error: { code: "plugin_commit_unknown", message: "核对数据", recoverability: "none" } }]);
  parent.emit("tasksChanged", [{ taskId: "t1", state: "running", revision: 3, cancelRequested: false }]);
  assert.equal(seen.at(-1)[0].state, "aborted");
  assert.equal(seen.at(-1)[0].error.code, "plugin_commit_unknown");
  assert.equal("approve" in client.tasks, false);
  client.dispose();
  assert.equal(parent.listeners.size, 0);
  await assert.rejects(client.ui.navigation.back(), e => e.code === "plugin_surface_closed");
});


test("current authorized context uses fresh parent locale/theme without replacing authority fields", async () => {
  const parent = parentChannel();
  const listeners = new Set(); let wire;
  const native = { postMessage: value => { wire = value; }, addEventListener: (_, f) => listeners.add(f), removeEventListener: (_, f) => listeners.delete(f) };
  const client = sdk.createSurfaceClient("s1", native, parent);
  const context = client.context.read();
  for (const f of listeners) f({ data: { contract: sdk.SURFACE_API_CONTRACT, requestId: wire.requestId,
    result: { projectKey: "workspace-owned", collection: "authorized", user: { role: "reader" }, locale: "zh-CN", theme: "light", density: "comfortable" } } });
  await Promise.resolve();
  const request = parent.calls.at(-1);
  assert.equal(request.payload.method, "context.current");
  parent.reply(request, { contract: "vibetable.plugin-theme.v1", locale: "en-US", mode: "dark", density: "compact", variables: {} });
  assert.deepEqual(await context, { projectKey: "workspace-owned", collection: "authorized", user: { role: "reader" }, locale: "en-US", theme: "dark", density: "compact" });
  client.dispose();
});

test("UI calls are bounded, ignore stale tokens, and dispose settles without replay", async () => {
  const parent = parentChannel(); const client = sdk.createSurfaceClient("s1", native(), parent);
  const calls = Array.from({ length: 8 }, () => client.tasks.list());
  const settled = Promise.allSettled(calls);
  await assert.rejects(client.ui.navigation.back(), error => error.code === "plugin_concurrency_limit");
  const request = parent.calls.at(-1);
  parent.emit("uiResponse", { requestId: request.payload.requestId, result: [] }, { data: { contract: "vibetable.plugin-surface.v1", surfaceToken: "old-token", event: "uiResponse", payload: { requestId: request.payload.requestId, result: [] } } });
  const count = parent.calls.length;
  client.dispose();
  assert.ok((await settled).every(item => item.status === "rejected" && item.reason.code === "plugin_surface_closed"));
  assert.equal(parent.calls.length, count);
  assert.equal(parent.listeners.size, 0);
});

test("UI public catalog matches SDK capability query and unavailable parent fails closed", async () => {
  const { readFile } = await import("node:fs/promises");
  const catalog = JSON.parse(await readFile(new URL("../../../contracts/plugin-surface-api.v1.json", import.meta.url), "utf8"));
  const client = sdk.createSurfaceClient("s1", native());
  assert.deepEqual(client.ui.capabilities(), { methods: catalog.ui.methods, unsupported: catalog.ui.unsupported });
  await assert.rejects(client.context.current(), error => error.code === "plugin_api_unsupported");
  assert.throws(() => client.context.onChanged(() => {}), error => error.code === "plugin_api_unsupported");
  assert.throws(() => client.tasks.onChanged(() => {}), error => error.code === "plugin_api_unsupported");
  await assert.rejects(client.tasks.cancel("not-owned"), error => error.code === "plugin_api_unsupported");
  client.dispose();
});


test("recent completion of an old active task survives bounded SDK history eviction", async () => {
  const parent = parentChannel(); const client = sdk.createSurfaceClient("s1", native(), parent);
  let last = [];
  client.tasks.onChanged(value => { last = value; });
  const make = (taskId, state = "succeeded", revision = 1) => ({ taskId, state, revision, cancelRequested: false });
  parent.emit("tasksChanged", [make("old-active", "running"), ...Array.from({ length: 255 }, (_, i) => make(`done-${i}`))]);
  parent.emit("tasksChanged", [{ ...make("old-active", "aborted", 2), error: { code: "plugin_commit_unknown", message: "核对数据", recoverability: "none" } }]);
  parent.emit("tasksChanged", [make("new-completion")]);
  assert.equal(last.length, 256);
  assert.equal(last.find(task => task.taskId === "old-active")?.error.code, "plugin_commit_unknown");
  assert.equal(last.some(task => task.taskId === "done-0"), false);
  client.dispose();
});
