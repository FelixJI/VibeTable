import assert from "node:assert/strict";
import test from "node:test";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import {
  applyNativeClipboardCleanupResult,
  createIsolatedNativeClipboardGuard,
  createPythonClipboardProbe,
  isIsolatedNativeClipboardDesktop,
  NATIVE_CLIPBOARD_GATE_MESSAGE,
} from "./isolated_native_clipboard_guard.mjs";

const GITHUB_HOSTED_ENV = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted" };
const LOCAL_OPT_IN_ENV = { VIBETABLE_NATIVE_CLIPBOARD_TESTS: "1" };
const DEFAULT_ENV = {};

function makeHarness({ env, probeScript = [], bridgeScript = [] }) {
  const probeCalls = [];
  const bridgeCalls = [];
  const runProbe = async (command, payload) => {
    probeCalls.push({ command, payload });
    const handler = probeScript.shift();
    if (handler === undefined) throw new Error(`unexpected probe command: ${command}`);
    return typeof handler === "function" ? handler({ command, payload }) : handler;
  };
  const bridgeRequest = async (page, type, payload) => {
    bridgeCalls.push({ page, type, payload });
    const handler = bridgeScript.shift();
    if (handler === undefined) throw new Error(`unexpected bridge request: ${type}`);
    return typeof handler === "function" ? handler({ page, type, payload }) : handler;
  };
  const guard = createIsolatedNativeClipboardGuard({ env, runProbe });
  return { guard, probeCalls, bridgeCalls, bridgeRequest };
}

function successfulBridgeRoundTrip(text) {
  return [
    () => ({ payload: { written: true } }),
    () => ({ payload: { text } }),
  ];
}

test("the isolated-desktop gate only accepts github-hosted runners or the explicit opt-in", () => {
  assert.equal(isIsolatedNativeClipboardDesktop(GITHUB_HOSTED_ENV), true);
  assert.equal(isIsolatedNativeClipboardDesktop(LOCAL_OPT_IN_ENV), true);
  assert.equal(isIsolatedNativeClipboardDesktop(DEFAULT_ENV), false);
  assert.equal(isIsolatedNativeClipboardDesktop(
    { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "self-hosted" }), false);
  assert.equal(isIsolatedNativeClipboardDesktop({ GITHUB_ACTIONS: "true" }), false);
});

test("a default local environment is refused before any probe or bridge request", async () => {
  const { guard, probeCalls, bridgeCalls, bridgeRequest } = makeHarness({ env: DEFAULT_ENV });
  await assert.rejects(
    guard.seed("page", "12.5", { bridgeRequest }),
    (error) => error.message === NATIVE_CLIPBOARD_GATE_MESSAGE,
  );
  assert.deepEqual(probeCalls, []);
  assert.deepEqual(bridgeCalls, []);
});

test("a non-empty isolated clipboard is refused before any bridge request", async () => {
  const { guard, probeCalls, bridgeCalls, bridgeRequest } = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [{ ok: true, empty: false, formatCount: 3, sequence: 4 }],
  });
  await assert.rejects(
    guard.seed("page", "12.5", { bridgeRequest }),
    (error) => error.message === "the isolated test desktop clipboard must be empty (all formats) "
      + "before the first native write; found 3 format(s) and refusing to overwrite it",
  );
  assert.deepEqual(probeCalls.map((call) => call.command), ["inspect"]);
  assert.deepEqual(probeCalls[0].payload, null);
  assert.deepEqual(bridgeCalls, []);
});

test("a failing emptiness probe refuses the first write without bridge access", async () => {
  const { guard, probeCalls, bridgeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [{ ok: false, error: "open-clipboard-failed" }],
  });
  await assert.rejects(
    guard.seed("page", "12.5", { bridgeRequest }),
    /the isolated clipboard could not be inspected before the first native write: open-clipboard-failed/,
  );
  assert.deepEqual(probeCalls.map((call) => call.command), ["inspect"]);
  assert.deepEqual(bridgeCalls, []);
});

test("a successful seed inspects, writes, confirms the exact write and clears it by sequence", async () => {
  const { guard, probeCalls, bridgeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, cleared: true, alreadyEmpty: false, emptyAfter: true },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await guard.seed("page", "12.5", { bridgeRequest });
  assert.deepEqual(probeCalls.map((call) => call.command), ["inspect", "confirm-owned"]);
  assert.deepEqual(
    bridgeCalls.map((call) => ({ type: call.type, payload: call.payload })),
    [
      { type: "clipboard.writeText", payload: { text: "12.5" } },
      { type: "clipboard.readText", payload: {} },
    ],
  );
  const cleanup = await guard.release();
  assert.deepEqual(probeCalls.map((call) => call.command), ["inspect", "confirm-owned", "clear-owned"]);
  assert.deepEqual(probeCalls[2].payload, { ownedTexts: ["12.5"], expectedSequence: 12 });
  assert.deepEqual(cleanup, { status: "clean", cleared: true, alreadyEmpty: false });
  const result = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(result, cleanup), false);
  assert.equal(result.status, "passed");
  assert.deepEqual(result.nativeClipboardCleanup, cleanup);
  await assert.rejects(guard.seed("page", "again", { bridgeRequest }), /already released/);
});

test("every later seed first re-verifies this run's clipboard state by sequence", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, verified: false, refused: "sequence-changed" },
    ],
    bridgeScript: [
      ...successfulBridgeRoundTrip("12.5"),
      ...successfulBridgeRoundTrip("number-copy-sentinel"),
    ],
  });
  await guard.seed("page", "12.5", { bridgeRequest });
  await assert.rejects(
    guard.seed("page", "number-copy-sentinel", { bridgeRequest }),
    /the clipboard changed outside this run between native writes; refusing to overwrite it/,
  );
  assert.deepEqual(
    probeCalls.map((call) => call.command),
    ["inspect", "confirm-owned", "verify-sequence"],
  );
  assert.deepEqual(probeCalls[2].payload, { expectedSequence: 12 });
  // The second write never reached the bridge after the sequence refusal.
  assert.deepEqual(probeCalls.filter((call) => call.command === "confirm-owned").length, 1);
});

test("a refused post-write confirmation fails the seed and leaves cleanup indeterminate", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: false, refused: "unowned-text" },
      { ok: true, empty: false, formatCount: 2, sequence: 13 },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await assert.rejects(
    guard.seed("page", "12.5", { bridgeRequest }),
    /post-write clipboard confirmation refused \(unowned-text\)/,
  );
  // The write was issued but never confirmed, so cleanup must not clear and
  // must not treat any momentary observation as settlement.
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, {
    status: "indeterminate",
    unconfirmedWrites: 1,
    observedEmpty: false,
    formatCount: 2,
  });
  assert.deepEqual(
    probeCalls.map((call) => call.command),
    ["inspect", "confirm-owned", "inspect"],
  );
  const passed = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(passed, cleanup), true);
  assert.equal(passed.status, "failed");
  assert.equal(passed.error.code, "NATIVE_CLIPBOARD_CLEANUP_FAILED");
  assert.match(passed.error.message, /never confirmed/);
});

test("a failed clipboard round trip still binds and clears the landed write", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, cleared: true, alreadyEmpty: false, emptyAfter: true },
    ],
    bridgeScript: [
      () => ({ payload: { written: true } }),
      () => ({ payload: { text: "stale unexpected value" } }),
    ],
  });
  await assert.rejects(
    guard.seed("page", '{"nested":{"value":8}}', { bridgeRequest }),
    /Native Host clipboard round trip failed/,
  );
  // The best-effort confirmation after the failed round trip still bound the
  // exact seeded text and its sequence number for cleanup.
  assert.deepEqual(probeCalls[1].payload, { text: '{"nested":{"value":8}}' });
  const cleanup = await guard.release();
  assert.deepEqual(probeCalls[2].payload, {
    ownedTexts: ['{"nested":{"value":8}}'],
    expectedSequence: 12,
  });
  assert.deepEqual(cleanup, { status: "clean", cleared: true, alreadyEmpty: false });
});

test("adoption accepts only pre-declared exact outputs of the product copy", async () => {
  const exact = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, verified: true, sequence: 14 },
      { ok: true, cleared: true, alreadyEmpty: false, emptyAfter: true },
    ],
    bridgeScript: [
      ...successfulBridgeRoundTrip("number-copy-sentinel"),
      () => ({ payload: { text: "HT-000001\r\n" } }),
    ],
  });
  await exact.guard.seed("page", "number-copy-sentinel", { bridgeRequest: exact.bridgeRequest });
  await exact.guard.adopt(
    "page",
    ["HT-000001", "HT-000001\n", "HT-000001\r\n"],
    { bridgeRequest: exact.bridgeRequest },
  );
  await exact.guard.release();
  assert.deepEqual(exact.probeCalls[3].payload, {
    ownedTexts: ["number-copy-sentinel", "HT-000001\r\n"],
    expectedSequence: 14,
  });

  const whitespace = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
    ],
    bridgeScript: [
      ...successfulBridgeRoundTrip("number-copy-sentinel"),
      () => ({ payload: { text: "HT-000001 " } }),
      () => ({ payload: { text: "HT-000001 " } }),
    ],
  });
  await whitespace.guard.seed("page", "number-copy-sentinel", { bridgeRequest: whitespace.bridgeRequest });
  await assert.rejects(
    whitespace.guard.adopt(
      "page",
      ["HT-000001", "HT-000001\n", "HT-000001\r\n"],
      { bridgeRequest: whitespace.bridgeRequest },
    ),
    (error) => error.message === "the product-written clipboard text does not exactly match any "
      + "pre-declared expected output (3 candidate(s)); refusing to adopt it"
        && !error.message.includes("HT-000001 "),
  );
  // No confirm-owned ran, so nothing was claimed for the non-matching text.
  assert.deepEqual(
    whitespace.probeCalls.map((call) => call.command),
    ["inspect", "confirm-owned"],
  );
  await assert.rejects(
    whitespace.guard.adopt("page", [], { bridgeRequest: whitespace.bridgeRequest }),
    /pre-declared expected output \(0 candidate\(s\)\)/,
  );
});

test("adopting before the guard is armed is refused", async () => {
  const { guard, bridgeRequest } = makeHarness({ env: GITHUB_HOSTED_ENV });
  await assert.rejects(
    guard.adopt("page", ["HT-000001"], { bridgeRequest }),
    /before the guard verified the isolated clipboard/,
  );
  assert.throws(
    () => guard.beginUnconfirmedNativeWrite(),
    /before the guard verified the isolated clipboard/,
  );
});

test("a registered external copy keeps cleanup indeterminate while it may still land", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, empty: false, formatCount: 1, sequence: 12 },
    ],
    bridgeScript: successfulBridgeRoundTrip("number-copy-sentinel"),
  });
  await guard.seed("page", "number-copy-sentinel", { bridgeRequest });
  // The product's Ctrl+C Host SetText is registered as unconfirmed; it times
  // out or is still in flight, so even the unchanged owned sentinel state
  // must not be cleared and reported clean.
  guard.beginUnconfirmedNativeWrite();
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, {
    status: "indeterminate",
    unconfirmedWrites: 1,
    observedEmpty: false,
    formatCount: 1,
  });
  // No clear-owned was issued: the possibly late-landing copy decides the
  // final state, not a momentary observation.
  assert.deepEqual(
    probeCalls.map((call) => call.command),
    ["inspect", "confirm-owned", "inspect"],
  );
  const result = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(result, cleanup), true);
  assert.equal(result.status, "failed");
  assert.match(result.cleanupError, /never confirmed/);
});

test("a registered external copy settles through a successful adopt", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: GITHUB_HOSTED_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, verified: true, sequence: 14 },
      { ok: true, cleared: true, alreadyEmpty: false, emptyAfter: true },
    ],
    bridgeScript: [
      ...successfulBridgeRoundTrip("number-copy-sentinel"),
      () => ({ payload: { text: "HT-000001\r\n" } }),
    ],
  });
  await guard.seed("page", "number-copy-sentinel", { bridgeRequest });
  guard.beginUnconfirmedNativeWrite();
  await guard.adopt(
    "page",
    ["HT-000001", "HT-000001\n", "HT-000001\r\n"],
    { bridgeRequest },
  );
  // The late-completing copy landed and was confirmed, so cleanup is settled
  // and clears the confirmed state normally.
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, { status: "clean", cleared: true, alreadyEmpty: false });
  assert.deepEqual(probeCalls[3].payload, {
    ownedTexts: ["number-copy-sentinel", "HT-000001\r\n"],
    expectedSequence: 14,
  });
});

test("foreign clipboard content is preserved and the run fails honestly", async () => {
  const { guard, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, cleared: false, refused: "sequence-changed" },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await guard.seed("page", "12.5", { bridgeRequest });
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, {
    status: "kept-foreign-content",
    cleared: false,
    refused: "sequence-changed",
    formatCount: null,
  });
  const passed = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(passed, cleanup), true);
  assert.equal(passed.status, "failed");
  assert.equal(passed.error.code, "NATIVE_CLIPBOARD_CLEANUP_FAILED");
  assert.match(passed.error.message, /preserved instead of cleared/);
  const failed = { status: "failed", error: { code: "SCENARIO_FAILED" } };
  applyNativeClipboardCleanupResult(failed, cleanup);
  assert.equal(failed.status, "failed");
  assert.equal(failed.error.code, "SCENARIO_FAILED");
  assert.match(failed.cleanupError, /preserved instead of cleared/);
});

test("a write that never lands still leaves cleanup indeterminate, not clean", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: false, refused: "unowned-text" },
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
    ],
    bridgeScript: [() => ({ payload: {} }), () => ({ payload: { text: "12.5" } })],
  });
  await assert.rejects(
    guard.seed("page", "12.5", { bridgeRequest }),
    /Native Host clipboard round trip failed/,
  );
  // Even an empty observation cannot settle a possibly in-flight Host write;
  // nothing is cleared and the outcome stays a failure-grade indeterminate.
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, {
    status: "indeterminate",
    unconfirmedWrites: 1,
    observedEmpty: true,
    formatCount: 0,
  });
  assert.deepEqual(
    probeCalls.map((call) => call.command),
    ["inspect", "confirm-owned", "inspect"],
  );
  const result = { status: "passed" };
  applyNativeClipboardCleanupResult(result, cleanup);
  assert.equal(result.status, "failed");
  assert.match(result.cleanupError, /never confirmed/);
});

test("a probe-reported already-empty state with the confirmed sequence maps to clean", async () => {
  const { guard, probeCalls, bridgeRequest } = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: true, cleared: false, alreadyEmpty: true },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await guard.seed("page", "12.5", { bridgeRequest });
  // Only reachable when the probe reports an empty clipboard that still
  // carries the run's confirmed sequence; with no unconfirmed writes the
  // state is settled and mapping it to clean is sound.
  const cleanup = await guard.release();
  assert.deepEqual(probeCalls[2].payload, { ownedTexts: ["12.5"], expectedSequence: 12 });
  assert.deepEqual(cleanup, { status: "clean", cleared: false, alreadyEmpty: true });
});

test("a probe crash or failing report during cleanup becomes an honest error outcome", async () => {
  const crashing = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      () => { throw new Error("probe process crashed"); },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await crashing.guard.seed("page", "12.5", { bridgeRequest: crashing.bridgeRequest });
  const crashCleanup = await crashing.guard.release();
  assert.equal(crashCleanup.status, "error");
  assert.match(crashCleanup.message, /probe process crashed/);

  const reporting = makeHarness({
    env: LOCAL_OPT_IN_ENV,
    probeScript: [
      { ok: true, empty: true, formatCount: 0, sequence: 11 },
      { ok: true, verified: true, sequence: 12 },
      { ok: false, error: "empty-clipboard-failed" },
    ],
    bridgeScript: successfulBridgeRoundTrip("12.5"),
  });
  await reporting.guard.seed("page", "12.5", { bridgeRequest: reporting.bridgeRequest });
  const reportCleanup = await reporting.guard.release();
  assert.equal(reportCleanup.status, "error");
  assert.match(reportCleanup.message, /empty-clipboard-failed/);
  const result = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(result, reportCleanup), true);
  assert.equal(result.status, "failed");
  assert.equal(result.error.code, "NATIVE_CLIPBOARD_CLEANUP_FAILED");
});

test("a guard that was never armed releases as unused without native access", async () => {
  const { guard, probeCalls } = makeHarness({ env: GITHUB_HOSTED_ENV });
  const cleanup = await guard.release();
  assert.deepEqual(cleanup, { status: "unused" });
  assert.deepEqual(probeCalls, []);
  const result = { status: "passed" };
  assert.equal(applyNativeClipboardCleanupResult(result, cleanup), false);
  assert.equal(result.status, "passed");
  assert.equal(applyNativeClipboardCleanupResult({ status: "passed" }, null), true);
});

const fakeProbeScript = `
const command = process.argv[2];
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  if (command === "echo") {
    process.stdout.write(JSON.stringify({ ok: true, echoed: JSON.parse(input) }) + "\\n");
  } else if (command === "garbage") {
    process.stdout.write("not json\\n");
  } else if (command === "boom") {
    process.exit(3);
  } else if (command === "hang") {
    setInterval(() => {}, 1_000);
  }
});
`;

test("the probe runner writes real stdin JSON and resolves the child report", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "vt-clipboard-guard-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const fakeProbe = path.join(directory, "fake-probe.mjs");
  fs.writeFileSync(fakeProbe, fakeProbeScript, "utf8");
  const runProbe = createPythonClipboardProbe(process.execPath, { probePath: fakeProbe, timeoutMs: 5_000 });

  const payload = { ownedTexts: ["12.5", "HT-000001\r\n", "中文\n"], expectedSequence: 12 };
  const echoed = await runProbe("echo", payload);
  assert.deepEqual(echoed, { ok: true, echoed: payload });

  await assert.rejects(runProbe("garbage", payload), /returned invalid JSON/);
  await assert.rejects(runProbe("boom", payload), /exited with code 3/);
});

test("the probe runner fails closed on a hung child instead of waiting on stdin", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "vt-clipboard-guard-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const fakeProbe = path.join(directory, "fake-probe.mjs");
  fs.writeFileSync(fakeProbe, fakeProbeScript, "utf8");
  const runProbe = createPythonClipboardProbe(process.execPath, { probePath: fakeProbe, timeoutMs: 250 });
  await assert.rejects(runProbe("hang", { expectedSequence: 1 }), /timed out after 250ms/);
});
