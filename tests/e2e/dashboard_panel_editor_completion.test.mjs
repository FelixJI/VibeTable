import assert from "node:assert/strict";
import test from "node:test";

import { awaitDashboardPanelReady } from "./dashboard_panel_editor_completion.mjs";

// Deterministic deferred-locator contract for the helper's real wait and
// failure propagation. This does not claim to reproduce real focus events or
// keyboard layout handling — those stay with scenario16's packaged CI run.

function deferredEditor() {
  let release;
  const detached = new Promise((resolve) => { release = resolve; });
  return {
    release,
    page: {
      getByTestId: (testId) => {
        assert.equal(testId, "dashboard-panel-editor");
        return { waitFor: () => detached };
      },
    },
  };
}

const panel = { waitFor: async () => undefined };

test("does not return while the editor teardown wait is still pending", async () => {
  const editor = deferredEditor();
  let settled = false;
  const ready = awaitDashboardPanelReady(editor.page, panel).then(() => { settled = true; });
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(settled, false);
  editor.release();
  await ready;
  assert.equal(settled, true);
});

test("propagates the editor wait failure unchanged", async () => {
  const failure = new Error("editor teardown failed");
  const page = {
    getByTestId: () => ({ waitFor: () => Promise.reject(failure) }),
  };
  await assert.rejects(
    () => awaitDashboardPanelReady(page, panel),
    (error) => error === failure,
  );
});
