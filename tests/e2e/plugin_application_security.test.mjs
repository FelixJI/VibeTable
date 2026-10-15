import test from "node:test";
import assert from "node:assert/strict";
import {
  installContrastRestoreProjectionCaptureInPage,
  selectExpectedPluginSecurityConsoleEntries,
} from "./plugin_application_security.mjs";
import { waitForCapturedBridgeMessage, captureCompletedInPage } from "./bridge_capture_wait.mjs";

const intermediate = extra => JSON.stringify({
  type: "appearance.changed",
  requestId: null,
  payload: { preference: "light", effective: "light", highContrast: true, ...extra },
});
const restored = () => JSON.stringify({
  type: "appearance.changed",
  requestId: null,
  payload: { preference: "light", effective: "light", highContrast: false },
});

function fixture() {
  const listeners = new Set();
  const webview = {
    postMessage() {},
    addEventListener(_type, callback) { listeners.add(callback); },
    removeEventListener(_type, callback) { listeners.delete(callback); },
  };
  globalThis.window = { chrome: { webview } };
  const pending = [];
  const page = {
    async evaluate(callback, value) {
      const result = callback(value);
      settle();
      return result;
    },
    waitForFunction(predicate, argument, options) {
      if (predicate(argument)) return Promise.resolve();
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          const error = new Error("Timed out");
          error.name = "TimeoutError";
          reject(error);
        }, options?.timeout ?? 30_000);
        pending.push({ predicate, argument, resolve, cancel: () => clearTimeout(timer) });
      });
    },
  };
  // Mirror the renderer contract: a waiter settles only once its own
  // predicate actually holds, never on an unrelated dispatch.
  function settle() {
    for (let index = pending.length - 1; index >= 0; index -= 1) {
      const waiter = pending[index];
      if (!waiter.predicate(waiter.argument)) continue;
      waiter.cancel();
      pending.splice(index, 1);
      waiter.resolve();
    }
  }
  function dispatch(message) {
    for (const callback of [...listeners]) callback({ data: message });
    settle();
  }
  return { page, webview, listeners, dispatch, settle };
}

test("an in-progress restore wait skips intermediate high-contrast re-projections until off", async () => {
  const f = fixture();
  try {
    const captureId = await f.page.evaluate(installContrastRestoreProjectionCaptureInPage);
    assert.match(captureId, /^appearance\/contrast-restore\/\d+$/);
    assert.equal(f.listeners.size, 1);
    let settled = null;
    const waiting = waitForCapturedBridgeMessage(f.page, 1000, captureId)
      .then(message => { settled = message; return message; });

    f.dispatch(intermediate());
    f.dispatch(intermediate({ effective: "dark" }));
    f.dispatch(JSON.stringify({ type: "appPreferences.update", payload: { theme: "dark" } }));
    f.dispatch("not json");
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(captureCompletedInPage(captureId), false, "intermediates do not complete the capture");
    assert.equal(settled, null, "the wait is still pending after intermediate re-projections");

    f.dispatch(restored());
    const projected = await waiting;
    assert.equal(projected.payload.highContrast, false);
    assert.equal(projected.payload.effective, "light");
    assert.equal(f.listeners.size, 0, "the terminal projection releases its listener");
  } finally {
    delete globalThis.window;
  }
});

test("restore projection fails closed when the Host never projects high contrast off", async () => {
  const f = fixture();
  try {
    const captureId = await f.page.evaluate(installContrastRestoreProjectionCaptureInPage);
    f.dispatch(intermediate());
    f.dispatch(intermediate());
    await assert.rejects(waitForCapturedBridgeMessage(f.page, 1, captureId), /timed out/);
    assert.equal(f.listeners.size, 0, "timeout releases its listener");
    assert.equal(captureCompletedInPage(captureId), false);
  } finally {
    delete globalThis.window;
  }
});

test("a second real installation owns a unique id and terminates the displaced waiter", async () => {
  const f = fixture();
  try {
    const firstId = await f.page.evaluate(installContrastRestoreProjectionCaptureInPage);
    let displaced = null;
    const firstWait = waitForCapturedBridgeMessage(f.page, 1000, firstId).catch(error => {
      displaced = error;
      throw error;
    });
    const secondId = await f.page.evaluate(installContrastRestoreProjectionCaptureInPage);
    assert.notEqual(firstId, secondId, "each installation owns a unique capture id");
    assert.equal(f.listeners.size, 1, "the displaced listener is released on install");

    await assert.rejects(firstWait, /CAPTURE_REPLACED/);
    assert.match(String(displaced?.message), /CAPTURE_REPLACED/);

    f.dispatch(intermediate());
    f.dispatch(restored());
    const projected = await waitForCapturedBridgeMessage(f.page, 1000, secondId);
    assert.equal(projected.payload.highContrast, false);
    assert.equal(f.listeners.size, 0, "the surviving listener completes and releases");
  } finally {
    delete globalThis.window;
  }
});

// The hosts, texts and empty console locations below are the verbatim renderer
// evidence of CI runs 38677661375 (file-io S49 failure) and the f3 retry:
// Chromium reports this scenario's deliberate attack with location.url "" and
// only quotes the attacked surfaces in the message text.
const ATTACKED_FRAME_ORIGIN = "https://2aa224baced54f428db742fe57162b99.aad94e8798074c890a08dafec01240b5.plugins.vibetable.local";
const OTHER_PLUGIN_ORIGIN = "https://0151e2f4fc9adc64f5822d6b4626ef11.de99fcb3da77a67e094a8a348766ce45.plugins.vibetable.local";
const entry = (type, text, url = "") => ({ type, text, location: { url, line: 0, column: 0, lineNumber: 0, columnNumber: 0 } });
const attackConsole = () => [
  entry("error", "Connecting to 'https://blocked.invalid/482-probe' violates the following Content Security Policy directive: \"connect-src 'none'\". The action has been blocked."),
  entry("error", "Fetch API cannot load https://blocked.invalid/482-probe. Refused to connect because it violates the document's Content Security Policy."),
  entry("error", `Connecting to '${OTHER_PLUGIN_ORIGIN}/ui/index.html' violates the following Content Security Policy directive: "connect-src 'none'". The action has been blocked.`),
  entry("error", `Fetch API cannot load ${OTHER_PLUGIN_ORIGIN}/ui/index.html. Refused to connect because it violates the document's Content Security Policy.`),
  entry("error", `Connecting to '${ATTACKED_FRAME_ORIGIN}/ui/app.js' violates the following Content Security Policy directive: "connect-src 'none'". The action has been blocked.`),
  entry("error", `Fetch API cannot load ${ATTACKED_FRAME_ORIGIN}/ui/app.js. Refused to connect because it violates the document's Content Security Policy.`),
  entry("error", `Loading the script '${OTHER_PLUGIN_ORIGIN}/ui/app.js' violates the following Content Security Policy directive: "script-src 'self'". Note that 'script-src-elem' was not explicitly set, so 'script-src' is used as a fallback. The action has been blocked.`),
  entry("error", `Executing inline script violates the following Content Security Policy directive 'script-src 'self''. Either the 'unsafe-inline' keyword, a hash ('sha256-an5mbCQqxU0A0UCTfBQk5NMeS3JTN6y0HxNGLq9MKGs='), or a nonce ('nonce-...') is required to enable inline execution. The action has been blocked.`),
  entry("error", `Unsafe attempt to initiate navigation for frame with URL 'https://app.vibetable.local/index.html?vibetable-e2e=1' from frame with URL '${ATTACKED_FRAME_ORIGIN}/ui/index.html'. The frame attempting navigation of the top-level window is sandboxed, but the flag of 'allow-top-navigation' or 'allow-top-navigation-by-user-activation' is not set.`),
  entry("warning", "Download is disallowed. The frame initiating or instantiating the download is sandboxed, but the flag ‘allow-downloads’ is not set. See https://www.chromestatus.com/feature/5706745674465280 for more details.", `${ATTACKED_FRAME_ORIGIN}/ui/index.html`),
  entry("error", "Failed to load resource: the server responded with a status of 403 (Forbidden)", `${ATTACKED_FRAME_ORIGIN}/%252e%252e/ui/app.js`),
];
// The in-page securitypolicyviolation listener captured these for the very
// same attack (verbatim from the f3 result assertions' probes details).
const attackProbes = () => ({
  violations: [
    { directive: "connect-src", blocked: "https://blocked.invalid/482-probe" },
    { directive: "connect-src", blocked: `${OTHER_PLUGIN_ORIGIN}/ui/index.html` },
    { directive: "connect-src", blocked: `${ATTACKED_FRAME_ORIGIN}/ui/app.js` },
    { directive: "script-src-elem", blocked: `${OTHER_PLUGIN_ORIGIN}/ui/app.js` },
    { directive: "script-src-elem", blocked: "inline" },
  ],
  attack: {
    fetchTargets: ["https://blocked.invalid/482-probe", `${OTHER_PLUGIN_ORIGIN}/ui/index.html`, `${ATTACKED_FRAME_ORIGIN}/ui/app.js`],
    scriptTarget: `${OTHER_PLUGIN_ORIGIN}/ui/app.js`,
    traversalUrl: `${ATTACKED_FRAME_ORIGIN}/%252e%252e/ui/app.js`,
    selfDocumentUrl: `${ATTACKED_FRAME_ORIGIN}/ui/index.html`,
  },
});

test("the acknowledgement claims exactly this run's attack diagnostics with empty console locations", () => {
  const console = attackConsole();
  const expected = selectExpectedPluginSecurityConsoleEntries(console, attackProbes(), 0);
  assert.equal(expected.length, console.length,
    "every deliberately provoked CSP/sandbox diagnostic is acknowledged, including the location-less ones");
  assert.deepEqual(new Set(expected), new Set(console));
});

test("wrong directives, attack URLs, locations and message families stay unexpected", () => {
  const probes = attackProbes();
  const unrelated = [
    // CSP wording but a URL no captured violation ever blocked.
    entry("error", "Connecting to 'https://unrelated.plugins.vibetable.local/ui/index.html' violates the following Content Security Policy directive: \"connect-src 'none'\". The action has been blocked."),
    // Attacked URL, but a message family with no matching captured directive.
    entry("error", `Loading the script '${OTHER_PLUGIN_ORIGIN}/ui/index.html' violates the following Content Security Policy directive: "script-src 'self'". The action has been blocked.`),
    // A real resource failure that quotes the attacked origin and “blocked”.
    entry("error", `Failed to load resource: net::ERR_BLOCKED_BY_CLIENT at ${ATTACKED_FRAME_ORIGIN}/ui/state.json`),
    entry("error", `Connecting to '${ATTACKED_FRAME_ORIGIN}/ui/app.js' failed unexpectedly.`),
    // Inline wording when no inline violation was captured this run.
    entry("error", "Executing inline script violates the following Content Security Policy directive 'script-src 'self''. The action has been blocked."),
    // Sandbox wording that does not reference this run's attacked frame.
    entry("error", "Unsafe attempt to initiate navigation for frame with URL 'https://app.vibetable.local/index.html' from frame with URL 'https://unrelated.plugins.vibetable.local/ui/index.html'. The frame attempting navigation of the top-level window is sandboxed, but the flag of 'allow-top-navigation' is not set."),
    entry("error", "Uncaught TypeError: cannot read properties of undefined at app.vibetable.local"),
  ];
  const withoutInlineViolation = structuredClone(probes);
  withoutInlineViolation.violations = probes.violations.filter(item => item.blocked !== "inline");
  const cases = [
    ...unrelated.slice(0, 4).map(item => [item, probes]),
    [unrelated[4], withoutInlineViolation],
    ...unrelated.slice(5).map(item => [item, probes]),
  ];
  for (const [candidate, currentProbes] of cases) {
    assert.deepEqual(selectExpectedPluginSecurityConsoleEntries([candidate], currentProbes, 0), [],
      `stays unexpected: ${candidate.text.slice(0, 60)}`);
  }
  // CSP text requires its exact effective directive and actual attack target.
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(attackConsole().slice(0, 8),
    { attack: probes.attack }, 0), []);
  const wrongDirective = attackProbes();
  wrongDirective.violations = wrongDirective.violations.map(item => ({ ...item, directive: "img-src" }));
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(attackConsole().slice(0, 8), wrongDirective, 0), []);
  const wrongTargets = attackProbes();
  wrongTargets.attack.fetchTargets = ["https://unrelated.invalid/"];
  wrongTargets.attack.scriptTarget = "https://unrelated.invalid/script.js";
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(attackConsole().slice(0, 7), wrongTargets, 0), []);
  const wrongLocations = attackConsole().slice(9).map(item => ({
    ...item, location: { url: `${ATTACKED_FRAME_ORIGIN}/ui/unrelated.html` },
  }));
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(wrongLocations, probes, 0), []);
  const lookalikeParent = { ...attackConsole()[8], text: attackConsole()[8].text
    .replace("https://app.vibetable.local/", "https://app.vibetable.local.unrelated.invalid/") };
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries([lookalikeParent], probes, 0), []);
});

test("each message family is acknowledged once and appended duplicates stay unexpected", () => {
  const original = attackConsole();
  const duplicates = attackConsole();
  const expected = selectExpectedPluginSecurityConsoleEntries([...original, ...duplicates], attackProbes(), 0);
  assert.deepEqual(expected, original);
  assert.ok(duplicates.every(item => !expected.includes(item)));
  // Two Connecting messages cannot consume the allowance for one Fetch message.
  const connectingDuplicate = { ...original[0] };
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(
    [original[0], connectingDuplicate, original[1]], attackProbes(), 0), [original[0], original[1]]);
});

test("only entries inside the attack window are acknowledged and invalid starts fail closed", () => {
  const outside = attackConsole();
  const inside = attackConsole();
  const entries = [...outside, ...inside];
  const expected = selectExpectedPluginSecurityConsoleEntries(entries, attackProbes(), outside.length);
  assert.deepEqual(expected, inside);
  assert.ok(outside.every(item => !expected.includes(item)));
  assert.deepEqual(selectExpectedPluginSecurityConsoleEntries(entries, attackProbes(), entries.length), []);
  for (const start of [-1, 0.5, NaN, Infinity, undefined, "0", entries.length + 1]) {
    assert.throws(() => selectExpectedPluginSecurityConsoleEntries(entries, attackProbes(), start), /invalid.*start/);
  }
});
