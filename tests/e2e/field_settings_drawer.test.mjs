import assert from "node:assert/strict";
import test from "node:test";
import {
  closeFieldSettingsDrawer,
  fieldApplyReceiptAutoCloses,
  settleFieldSettingsAfterApply,
} from "./webview_product_scenarios.mjs";

function fixture({ open = true, dirty = false, manager = false, failure = null, autoClose = false } = {}) {
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
          // autoClose models the product's post-apply behavior: the drawer
          // closes itself, so the close button disappears without a click.
          if (autoClose) open = false;
          assert.equal(open, false);
          calls.push("hidden");
        },
      };
    },
  } };
}

const applyReceipt = (payload) => ({ type: "field.change.apply", payload });

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

test("only successful non-migration create/purge receipts report an auto-close", async () => {
  assert.equal(fieldApplyReceiptAutoCloses(applyReceipt({ action: "create" })), true);
  assert.equal(fieldApplyReceiptAutoCloses(applyReceipt({ action: "purge" })), true);
  assert.equal(fieldApplyReceiptAutoCloses(applyReceipt({ action: "update" })), false);
  assert.equal(fieldApplyReceiptAutoCloses(applyReceipt({ action: "retire" })), false);
  assert.equal(fieldApplyReceiptAutoCloses(applyReceipt({ action: "create", migrationJobId: "job_1" })), false);
  assert.equal(fieldApplyReceiptAutoCloses({ type: "operation.failed", payload: { action: "create" } }), false);
  assert.equal(fieldApplyReceiptAutoCloses(null), false);
});

test("a create receipt waits for the real auto-close instead of racing the vanishing close button", async () => {
  // The close button is still rendered when the receipt lands; the product is
  // about to remove it. Clicking it here (the old isVisible-then-click habit)
  // must fail this contract, and field management still returns to the grid
  // only after the drawer has actually closed.
  const { page, calls } = fixture({ open: true, autoClose: true, manager: true });
  await settleFieldSettingsAfterApply(page, applyReceipt({ action: "create" }));
  assert.deepEqual(calls, ["hidden", "view-all-records"]);
});

test("a purge receipt also settles through the automatic close", async () => {
  const { page, calls } = fixture({ open: true, autoClose: true });
  await settleFieldSettingsAfterApply(page, applyReceipt({ action: "purge" }));
  assert.deepEqual(calls, ["hidden"]);
});

test("an update receipt re-describes clean and is closed manually by the user", async () => {
  const { page, calls } = fixture({ open: true, autoClose: false });
  await settleFieldSettingsAfterApply(page, applyReceipt({ action: "update" }));
  assert.deepEqual(calls, ["field-close-button", "hidden"]);
});

test("a migration-tracked receipt keeps the drawer open until the user closes it", async () => {
  const { page, calls } = fixture({ open: true, autoClose: false });
  await settleFieldSettingsAfterApply(page, applyReceipt({ action: "create", migrationJobId: "job_1" }));
  assert.deepEqual(calls, ["field-close-button", "hidden"]);
});

test("settle failures on the manual path are not swallowed", async () => {
  const { page } = fixture({ open: true, failure: "field-close-button" });
  await assert.rejects(
    settleFieldSettingsAfterApply(page, applyReceipt({ action: "update" })),
    /click failed/,
  );
});
