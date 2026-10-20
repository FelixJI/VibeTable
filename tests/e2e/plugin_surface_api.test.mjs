import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";

// These tests guard orchestration and failure assertions. Only the real S48
// packaged WPF run qualifies the native confirmation, timer, and Go authority.
const source = await fs.readFile(new URL("./plugin_applications.mjs", import.meta.url), "utf8");
const body = source.slice(source.indexOf("export async function exerciseApplicationConfirmationExpiry"),
  source.indexOf("export async function exerciseApplicationWorkspaceIsolation")).replace(/^export /u, "").trim();
const A = "com.vibetable.e2e.application-a";

function harness(options = {}) {
  let now = 1000000, serial = 0, active, captured;
  const originalNow = Date.now, originalWindow = globalThis.window, originalCall = globalThis.call;
  Date.now = () => now;
  globalThis.window = {};
  const state = { projectKey: "local:synthetic", tableId: "table" };
  const rows = [{ id: "row", text: "native-approved", revision: 1 }];
  const events = [], tasks = new Map(), plans = new Map(), submissions = [], inputs = [], checks = [], waits = [];
  const reject = code => Object.assign(new Error(code), { code });
  function finish(code) {
    active.task.state = code === "plugin_timeout" ? "failed" : "cancelled";
    active.task.error = { code };
    events.push({ eventType: "action", runId: active.task.runId, outcome: "failed", errorCode: code });
    if (options.expiryWrites) rows[0].revision++;
    active.reject(reject(code)); active.done = true;
  }
  globalThis.call = async (method, params) => {
    if (method === "context.read") return { collection: state.tableId };
    if (method === "data.describe") return { collection: state.tableId, schemaRevision: "schema", fields: [{ fieldId: "text", logicalType: "text" }] };
    if (method === "data.query") return { items: structuredClone(rows), rowGuards: { row: "existing-guard" } };
    if (method === "mutation.preview") { const planId = `plan-${++serial}`; plans.set(planId, params); return { planId }; }
    assert.equal(method, "mutation.submit");
    submissions.push(params.planId);
    const plan = plans.get(params.planId);
    if (!plan) {
      if (options.replayWrites && active.task.state === "succeeded" || options.expiredReplayWrites && active.task.state === "failed") rows[0].revision++;
      if (options.replayAddsAudit && active.task.state === "succeeded") events.push({ eventType: "action", runId: active.task.runId, outcome: "succeeded" });
      if (options.replayChangesReceipt && active.task.state === "succeeded") active.task.state = "failed";
      throw reject("plugin_plan_stale");
    }
    plans.delete(params.planId);
    const taskId = `task-${serial}`, runId = `run-${serial}`;
    const task = { taskId, runId, pluginId: A, projectKey: state.projectKey, collection: state.tableId, state: "running" };
    tasks.set(taskId, task);
    active = { task, plan, expiresAt: now + (options.shortValidity ? 5000 : 120000), done: false };
    captured = { payload: { snapshot: { runId, pendingConfirmation: { interactionId: `interaction-${serial}`, expiresAt: active.expiresAt / 1000 } } } };
    events.push({ eventType: "action.started", runId });
    return new Promise((resolve, reject) => { active.resolve = resolve; active.reject = reject; });
  };
  const frame = {
    async evaluate(callback, value) { return callback(value); },
    async waitForFunction(callback) { await new Promise(resolve => setImmediate(resolve)); assert.ok(callback(), "wait for an actual terminal, never guess one"); },
  };
  const page = {
    locator(selector) { return {
      async getAttribute(name) { assert.equal(selector, ".host-runtime:has(.host-confirmation)"); assert.equal(name, "data-task-id"); return active.task.taskId; },
      async waitFor({ state }) { assert.equal(selector, ".host-confirmation"); assert.equal(active.done, state === "hidden"); },
    }; },
    async waitForTimeout(delay) {
      waits.push(delay); now += delay;
      if (now >= active.expiresAt && !active.done) finish(options.cancelInsteadOfExpiry ? "plugin_cancel_requested" : options.unknownOutcome ? "plugin_commit_unknown" : "plugin_timeout");
    },
  };
  const nativeInput = async (_runtime, action, buttonId) => {
    inputs.push([action, buttonId, active.task.taskId]);
    if (active.done) return { status: "failed", error: options.nativeFailure ?? "expected one current owned native plugin window" };
    if (action === "click") {
      rows[0].text = active.plan.operations[0].values.text; rows[0].revision++;
      active.task.state = "succeeded"; active.done = true;
      events.push({ eventType: "action", runId: active.task.runId, outcome: "succeeded" });
      active.resolve({ status: "succeeded", taskId: active.task.taskId, receipt: { committed: true } });
    }
    return { status: "completed" };
  };
  const helpers = {
    async beginBridgeMessageCapture(_page, types) { assert.deepEqual(types, ["plugin.interaction.requested"]); },
    async waitForCapturedBridgeMessage() { return structuredClone(captured); },
    async rawBridgeRequest(_page, method, params) {
      if (method === "query.page") { assert.equal(params.tableId, state.tableId); return { payload: { rows: structuredClone(rows) } }; }
      if (method === "plugin.audit.list") return { payload: structuredClone(events) };
      if (method === "plugin.task.get") return { payload: structuredClone(tasks.get(params.taskId)) };
      assert.equal(method, "plugin.interaction.resolve"); assert.equal(params.runId, "run-1");
      return { type: method, payload: { status: options.lateApproval ? "resolved" : "expired" } };
    },
  };
  const run = new Function("nativePluginInput", "A", `return (${body});`)(nativeInput, A);
  return { submissions, inputs, checks, waits, rows, async run() {
    try { await run(page, frame, { check(name, passed) { assert.ok(passed, name); checks.push(name); } }, {}, helpers, state); }
    finally { Date.now = originalNow; globalThis.window = originalWindow; globalThis.call = originalCall; }
  } };
}

test("orchestration waits the full published TTL, then recovers once and replays the same consumed plans", async () => {
  const h = harness(); await h.run();
  assert.ok(h.waits.reduce((total, delay) => total + delay, 0) >= 120000);
  assert.deepEqual(h.submissions, ["plan-1", "plan-1", "plan-2", "plan-2"]);
  assert.deepEqual(h.inputs.map(([action, , task]) => [action, task]), [["observe", "task-1"], ["click", "task-1"], ["click", "task-2"]]);
  assert.equal(h.rows[0].revision, 2);
  assert.ok(h.checks.some(name => name.includes("acknowledged receipt")));
});

for (const [option, expected] of [
  ["shortValidity", /full 120 second/],
  ["cancelInsteadOfExpiry", /known timeout/],
  ["lateApproval", /cannot be revived/],
  ["expiryWrites", /zero business writes/],
  ["unknownOutcome", /known timeout/],
  ["expiredReplayWrites", /cannot be revived/],
  ["replayAddsAudit", /without another write or audit/],
  ["replayWrites", /without another write or audit/],
  ["replayChangesReceipt", /acknowledged receipt/],
]) test(`qualification rejects ${option}`, async () => {
  await assert.rejects(harness({ [option]: true }).run(), expected);
});

test("an unrelated native driver failure cannot masquerade as a retired confirmation", async () => {
  await assert.rejects(harness({ nativeFailure: "foreground synchronization failed" }).run(), /cannot be revived/);
});
