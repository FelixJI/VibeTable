import assert from "node:assert/strict";
import test from "node:test";
import { closeFieldSettingsDrawer } from "./webview_product_scenarios.mjs";

function fixture({ open = true, dirty = false, manager = false, failure = null } = {}) {
  const calls = [];
  let confirming = false;
  return { calls, page: {
    getByTestId(id) {
      return {
        async isVisible() {
          if (id === "field-close-button") return open;
          if (id === "field-close-confirm-discard") return confirming;
          if (id === "field-management-view") return manager;
          assert.fail(`unexpected visibility query: ${id}`);
        },
        async click() {
          calls.push(id);
          if (id === failure) throw new Error("click failed");
          if (id === "field-close-button") { confirming = dirty; open = dirty; }
          else if (id === "field-close-confirm-discard") { confirming = false; open = false; }
          else assert.equal(id, "view-all-records");
        },
        async waitFor(options) {
          assert.equal(id, "field-display-name");
          assert.equal(options.state, "hidden");
          assert.equal(open, false);
          calls.push("hidden");
        },
      };
    },
  } };
}

test("clean drawers close without any native dialog", async () => {
  const { page, calls } = fixture();
  await closeFieldSettingsDrawer(page);
  assert.deepEqual(calls, ["field-close-button", "hidden"]);
});

test("dirty drawers explicitly discard through the application confirmation", async () => {
  const { page, calls } = fixture({ dirty: true });
  await closeFieldSettingsDrawer(page);
  assert.deepEqual(calls, ["field-close-button", "field-close-confirm-discard", "hidden"]);
});

test("successful auto-close needs no second close and returns from field management", async () => {
  const { page, calls } = fixture({ open: false, manager: true });
  await closeFieldSettingsDrawer(page);
  assert.deepEqual(calls, ["hidden", "view-all-records"]);
});

for (const failure of ["field-close-button", "field-close-confirm-discard"]) {
  test(`${failure} errors are not swallowed`, async () => {
    const { page } = fixture({ dirty: true, failure });
    await assert.rejects(closeFieldSettingsDrawer(page), /click failed/);
  });
}
