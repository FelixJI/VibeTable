import assert from "node:assert/strict";
import test from "node:test";
import { closeFieldSettingsDrawer } from "./webview_product_scenarios.mjs";

function makeDrawerPage({
  dirty = false,
  clickError = null,
  hiddenError = null,
  dialogAcceptError = null,
  gateAccept = false,
} = {}) {
  const dialogListeners = new Set();
  const eventWaiters = [];
  const calls = { acceptedDialogs: 0, hiddenChecks: 0, eventWaitCount: 0, unknownOff: 0 };
  let releaseAccept;
  const acceptGate = gateAccept
    ? new Promise((resolve) => {
        releaseAccept = resolve;
      })
    : null;

  function fireDialog() {
    const dialog = {
      type: "confirm",
      message: "字段设置尚未保存，确定放弃更改吗？",
      async accept() {
        if (dialogAcceptError) throw dialogAcceptError;
        if (acceptGate) await acceptGate;
        calls.acceptedDialogs += 1;
      },
    };
    for (const waiter of eventWaiters.splice(0)) waiter.resolve(dialog);
    for (const listener of [...dialogListeners]) void listener(dialog);
  }

  const page = {
    on(type, listener) {
      assert.equal(type, "dialog");
      dialogListeners.add(listener);
    },
    off(type, listener) {
      assert.equal(type, "dialog");
      if (!dialogListeners.delete(listener)) calls.unknownOff += 1;
    },
    // Legacy helper path: a faithful bounded dialog wait that never fires on
    // a clean close, so the old implementation pays its full timeout.
    waitForEvent(type, options = {}) {
      calls.eventWaitCount += 1;
      assert.equal(type, "dialog");
      return new Promise((resolve, reject) => {
        const timer = setTimeout(
          () => reject(Object.assign(
            new Error(`dialog wait timed out after ${options.timeout}ms`),
            { name: "TimeoutError" },
          )),
          options.timeout ?? 30_000,
        );
        timer.unref?.();
        eventWaiters.push({ resolve: (dialog) => { clearTimeout(timer); resolve(dialog); } });
      });
    },
    getByTestId(id) {
      if (id === "field-close-button") {
        return {
          click: async () => {
            if (clickError) throw clickError;
            if (dirty) fireDialog();
          },
        };
      }
      if (id === "field-display-name") {
        return {
          waitFor: async (options) => {
            calls.hiddenChecks += 1;
            assert.equal(options.state, "hidden");
            if (hiddenError) throw hiddenError;
            if (dirty && calls.acceptedDialogs === 0) {
              throw new Error("drawer stayed open because the confirm dialog was never accepted");
            }
          },
        };
      }
      assert.fail(`unexpected test id: ${id}`);
    },
  };
  return { page, dialogListeners, calls, releaseAccept };
}

test("a clean close finishes immediately without waiting for a dialog", { timeout: 1_200 }, async () => {
  const { page, dialogListeners, calls } = makeDrawerPage({ dirty: false });

  await closeFieldSettingsDrawer(page);

  assert.equal(calls.hiddenChecks, 1, "the drawer hidden wait must still run");
  assert.equal(calls.eventWaitCount, 0, "a clean close must not pay any dialog wait");
  assert.equal(calls.acceptedDialogs, 0);
  assert.equal(dialogListeners.size, 0, "the helper must remove its own listener");
  assert.equal(calls.unknownOff, 0, "the helper must not remove listeners it did not add");
});

test("a dirty drawer waits for the accept to settle before the hidden wait", async () => {
  const { page, dialogListeners, calls, releaseAccept } = makeDrawerPage({
    dirty: true,
    gateAccept: true,
  });
  const unrelated = () => {};
  page.on("dialog", unrelated);

  const closing = closeFieldSettingsDrawer(page);
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls.hiddenChecks, 0, "hidden must not run while the accept is still pending");

  releaseAccept();
  await closing;

  assert.equal(calls.acceptedDialogs, 1, "the dirty confirm dialog must be accepted");
  assert.equal(calls.hiddenChecks, 1);
  assert.equal(calls.eventWaitCount, 0, "the helper listens via page.on, not waitForEvent");
  assert.ok(dialogListeners.has(unrelated), "other listeners must stay registered");
  assert.equal(dialogListeners.size, 1, "only the helper's own listener is removed");
  assert.equal(calls.unknownOff, 0);
});

test("accept failures propagate and only the helper's listener is removed", async () => {
  const { page, dialogListeners } = makeDrawerPage({
    dirty: true,
    dialogAcceptError: new Error("accept exploded"),
  });
  const unrelated = () => {};
  page.on("dialog", unrelated);

  await assert.rejects(closeFieldSettingsDrawer(page), /accept exploded/);
  assert.ok(dialogListeners.has(unrelated));
  assert.equal(dialogListeners.size, 1, "the helper must clean up after an accept failure");
});

test("a hidden-wait failure still removes only the helper's own listener", async () => {
  const { page, dialogListeners } = makeDrawerPage({
    dirty: false,
    hiddenError: new Error("drawer never hid"),
  });
  const unrelated = () => {};
  page.on("dialog", unrelated);

  await assert.rejects(closeFieldSettingsDrawer(page), /drawer never hid/);
  assert.ok(dialogListeners.has(unrelated));
  assert.equal(dialogListeners.size, 1, "the helper must clean up after a hidden-wait failure");
});

test("click failures propagate and clean up the temporary listener", async () => {
  const { page, dialogListeners, calls } = makeDrawerPage({
    dirty: true,
    clickError: new Error("close button vanished"),
  });
  const unrelated = () => {};
  page.on("dialog", unrelated);

  await assert.rejects(closeFieldSettingsDrawer(page), /close button vanished/);
  assert.ok(dialogListeners.has(unrelated));
  assert.equal(dialogListeners.size, 1);
  assert.equal(calls.unknownOff, 0);
});
