import assert from "node:assert/strict";
import fs from "node:fs/promises";
import test from "node:test";

// Guards the scenario's durable-audit observation against the verified Host
// seam: Finish(notify) precedes the bounded audit.finish, so the durable
// action terminal can land after the task terminal. Only the real packaged
// WPF run qualifies the full mutation flow itself.
const source = await fs.readFile(new URL("./plugin_sdk_examples.mjs", import.meta.url), "utf8");
const start = source.indexOf("export async function waitForDurableActionTerminal");
const end = source.indexOf("export async function exerciseSdkExamples");
assert.ok(start >= 0 && end > start, "waitForDurableActionTerminal must live in the scenario source");
const waitForDurableActionTerminal = new Function(
  source.slice(start, end).replace(/^export /u, "") + "\nreturn waitForDurableActionTerminal;",
)();

const RUN = "run-1";
const terminal = (overrides = {}) => ({
  eventType: "action",
  runId: RUN,
  targetCount: 1,
  details: { taskId: "task-1" },
  outcome: "cancelled",
  ...overrides,
});

function harness(options = {}) {
  let now = 1000000;
  const originalNow = Date.now;
  Date.now = () => now;
  const auditCalls = [], timeouts = [], waits = [], checks = [], remainings = [];
  let reads = 0;
  const rawBridgeRequest = async (_page, method, params, timeoutMs) => {
    // The observation is read-only: replaying cancel/confirm/mutation/plugin
    // actions here is an immediate failure.
    assert.equal(method, "plugin.audit.list", "business actions must never be replayed");
    auditCalls.push(params);
    timeouts.push(timeoutMs);
    remainings.push(until - now);
    if (options.bridgeRejects) throw Object.assign(new Error("PLUGIN_REQUEST_CANCELLED"), { code: "PLUGIN_REQUEST_CANCELLED" });
    reads += 1;
    if (options.nonArray) return { payload: { not: "an array" } };
    const started = [{ eventType: "action.started", runId: RUN }];
    if (options.duplicate) return { payload: [...started, terminal(), terminal()] };
    if (options.wrongTask) return { payload: [terminal({ details: { taskId: "other-task" } })] };
    if (options.wrongOutcome) return { payload: [terminal({ outcome: "succeeded" })] };
    if (options.missing) return { payload: started };
    // delayed: the terminal lands on the second observation, after the task
    // terminal (exactly the CI 11-plugin-mutation cancel race).
    if (options.delayed && reads === 1) return { payload: started };
    return { payload: [...started, terminal()] };
  };
  const page = { async waitForTimeout(delay) { waits.push(delay); now += delay; } };
  const recorder = { check(name, passed) { checks.push({ name, passed }); assert.ok(passed, name); } };
  const until = now + (options.expired ? 0 : options.budget ?? 30000);
  const run = () => waitForDurableActionTerminal(page, rawBridgeRequest, {
    projectKey: "local:synthetic",
    pluginId: "com.vibetable.examples.normalize-text",
    runId: RUN,
    taskId: "task-1",
    decision: "cancelled",
    until,
    recorder,
  });
  return { run, auditCalls, timeouts, waits, checks, remainings, restore() { Date.now = originalNow; } };
}

test("observes the durable terminal that lands after the task terminal", async () => {
  const h = harness({ delayed: true });
  try { await h.run(); } finally { h.restore(); }
  assert.equal(h.auditCalls.length, 2);
  // Every raw request timeout stays within the remaining budget and every
  // wait step is truncated to the 100ms cadence.
  assert.ok(h.timeouts.length > 0 && h.timeouts.every(timeout => timeout > 0 && timeout <= 20000));
  assert.ok(h.waits.length > 0 && h.waits.every(delay => delay > 0 && delay <= 100));
});

test("fails within the existing deadline when the terminal never lands", async () => {
  const h = harness({ missing: true });
  try {
    await assert.rejects(h.run(), /durable receipt/);
  } finally { h.restore(); }
  assert.ok(h.auditCalls.length >= 2 && h.auditCalls.length <= 301, `bounded observations: ${h.auditCalls.length}`);
});

test("sends no request when the deadline already expired at call time", async () => {
  const h = harness({ expired: true, missing: true });
  try {
    await assert.rejects(h.run(), /durable receipt/);
  } finally { h.restore(); }
  assert.equal(h.auditCalls.length, 0);
  assert.equal(h.waits.length, 0);
});

test("truncates every request timeout and wait to the remaining budget near the deadline", async () => {
  const h = harness({ budget: 160, missing: true });
  try {
    await assert.rejects(h.run(), /durable receipt/);
  } finally { h.restore(); }
  assert.ok(h.auditCalls.length >= 1 && h.auditCalls.length <= 3, `bounded: ${h.auditCalls.length}`);
  // Each raw call's timeout must respect the budget remaining AT that call,
  // not just the 20s interface default, and each wait stays within it too.
  assert.ok(h.timeouts.length === h.remainings.length
    && h.timeouts.every((timeout, index) => timeout <= h.remainings[index] && timeout <= 20000));
  assert.ok(h.waits.length > 0 && h.waits.every(delay => delay > 0 && delay <= 100)
    && h.waits.reduce((total, delay) => total + delay, 0) <= 160);
});

for (const [option, pattern, observations] of [
  ["duplicate", /durable receipt/, 1],
  ["wrongTask", /durable receipt/, 1],
  ["wrongOutcome", /durable receipt/, 1],
  ["bridgeRejects", /PLUGIN_REQUEST_CANCELLED/, 1],
  ["nonArray", /audit list must be an array/, 1],
]) test(`fails immediately on ${option} without waiting or retrying`, async () => {
  const h = harness({ [option]: true });
  try {
    await assert.rejects(h.run(), pattern);
  } finally { h.restore(); }
  assert.equal(h.auditCalls.length, observations);
  assert.equal(h.waits.length, 0);
});
