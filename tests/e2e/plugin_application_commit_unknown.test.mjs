import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const source = await fs.readFile(new URL("./plugin_application_commit_unknown.mjs", import.meta.url), "utf8");
const body = source.slice(source.indexOf("export async function runApplicationCommitUnknown")).replace(/^export /u, "");

test("unknown commit requests native recovery, observes sealing, releases the write, and waits for one new page", async () => {
  const events = [];
  const element = { async click() {}, async fill() {}, async waitFor() {}, async isVisible() { return true; } };
  const frame = {
    getByRole: () => element, locator: () => element,
    async evaluate(callback) { return String(callback).includes("window.name") ? "old-token" : "plugin_source_denied"; },
  };
  const page = { getByTestId: () => element, locator: () => element };
  const nativeInput = async (_runtime, action, buttonId) => {
    events.push(action);
    assert.equal(buttonId, action === "click" ? "plugin-confirm-approve" : "plugin-recover");
    return { status: "completed" };
  };
  let refreshed = false;
  const runtime = {
    controlsDir: "controls", dataRoot: "owned/local-data",
    async refreshPluginProductPage() {
      assert.ok(events.includes("recover"), "waiting for a new page cannot initiate recovery");
      assert.equal(refreshed, false, "one recovery produces one new page");
      assert.equal(events.at(-1), "release", "release the blocked write after sealing and before waiting");
      refreshed = true; events.push("refresh"); return page;
    },
  };
  const waitFile = async (file, predicate) => {
    const text = file.endsWith("mutation-barrier.ready.json")
      ? JSON.stringify({ point: "after_record", pid: 1 })
      : "Renderer recovery sealed plugin sessions;";
    assert.ok(predicate(text));
    if (file.endsWith("vibetable-trace.log")) {
      assert.ok(events.includes("recover"), "sealing is observed after actual native input");
      events.push("sealed");
    }
    return text;
  };
  const fakeFs = { async writeFile(file) {
    assert.equal(file, path.join(runtime.controlsDir, "mutation-barrier.release"));
    assert.equal(events.at(-1), "sealed"); events.push("release");
  } };
  const helpers = {
    async waitForShell() { return { payload: { projectKey: "workspace" } }; },
    async createSimpleTable() { return { tableId: "table" }; },
    async beginBridgeMessageCapture() {}, async waitForCapturedBridgeMessage() { return { payload: { snapshot: { taskId: "task" } } }; },
    async selectTable() {},
    async rawBridgeRequest(_page, method) {
      return { payload: method === "query.page" ? { rows: [] }
        : { state: "aborted", error: { code: "plugin_commit_unknown" } } };
    },
  };
  const run = new Function("fs", "path", "nativePluginInput", "installApplication", "openApplication", "waitFile",
    `return (${body});`)(fakeFs, path, nativeInput, async () => {}, async () => frame, waitFile);
  await run(page, { check(name, passed) { assert.ok(passed, name); } }, runtime, helpers);
  assert.deepEqual(events, ["click", "recover", "sealed", "release", "refresh"]);
});
