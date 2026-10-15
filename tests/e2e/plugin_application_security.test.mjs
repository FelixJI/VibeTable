import test from "node:test";
import assert from "node:assert/strict";
import { installContrastRestoreProjectionCaptureInPage } from "./plugin_application_security.mjs";
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
